package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"regexp"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/validation"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/plugin-user-management/backend/api/v1alpha1"
	"github.com/openeverest/plugin-user-management/backend/internal/controller"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/password"
)

const (
	defaultK8sCluster = "main"
	maxBodyBytes      = 64 << 10
	// Leaves room for the "-credentials" suffix and the hash within name limits.
	maxNameStemLength = 200
)

// Mirrors the DatabaseUser CRD's username pattern.
var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$`)

var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

type server struct {
	everest *everestClient
	kube    client.Client
	// engines by provider name (Instance spec.providerRef.name).
	engines map[string]engine.Engine
}

type instanceRef struct {
	k8sCluster string
	namespace  string
	name       string
}

// userView is a row of the users list.
type userView struct {
	// ID is the DatabaseUser's name; empty for users the plugin doesn't manage.
	ID       string             `json:"id,omitempty"`
	Username string             `json:"username"`
	Database string             `json:"database,omitempty"`
	Roles    []engine.RoleGrant `json:"roles"`
	// Managed users are declared by a DatabaseUser and can be changed.
	Managed bool `json:"managed"`
	// System users are created and used by the operator.
	System  bool   `json:"system"`
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
	// OwnPasswordSecret users keep their password in a Secret managed
	// outside the plugin, so the plugin can't show or reset it.
	OwnPasswordSecret bool `json:"ownPasswordSecret,omitempty"`
}

type usersResponse struct {
	Users []userView `json:"users"`
	// Roles the plugin can grant on this instance.
	Roles []engine.Role `json:"roles"`
	// CanManage tells whether the caller may create, change and delete users.
	CanManage bool `json:"canManage"`
	// DatabaseError explains why users that exist only in the database are missing.
	DatabaseError string `json:"databaseError,omitempty"`
}

type credentialsResponse struct {
	ID       string `json:"id"`
	Username string `json:"username"`
	Password string `json:"password"`
	// URI is empty until the instance's database is reachable.
	URI string `json:"uri,omitempty"`
	// Secret holds the credentials, for applications that mount them.
	Secret string `json:"secret"`
}

type createUserRequest struct {
	Username string `json:"username"`
	// Generated when empty.
	Password string             `json:"password"`
	Roles    []engine.RoleGrant `json:"roles"`
}

type updateRolesRequest struct {
	Roles []engine.RoleGrant `json:"roles"`
}

type resetPasswordRequest struct {
	// Generated when empty.
	Password string `json:"password"`
}

func badRequest(message string) error {
	return &statusError{status: http.StatusBadRequest, message: message}
}

func conflict(message string) error {
	return &statusError{status: http.StatusConflict, message: message}
}

// userToken returns the caller's token. X-Everest-User is the spec'd header;
// until the host sets it, the proxy forwards the user's Authorization header.
func userToken(r *http.Request) (string, error) {
	if v := r.Header.Get("X-Everest-User"); v != "" {
		return v, nil
	}
	if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
		return token, nil
	}
	return "", &statusError{status: http.StatusUnauthorized, message: "missing auth token"}
}

func parseInstanceRef(r *http.Request) (instanceRef, error) {
	q := r.URL.Query()
	ref := instanceRef{
		k8sCluster: q.Get("k8sCluster"),
		namespace:  q.Get("namespace"),
		name:       q.Get("instance"),
	}
	if ref.k8sCluster == "" {
		ref.k8sCluster = defaultK8sCluster
	}
	if len(validation.IsDNS1123Subdomain(ref.k8sCluster)) > 0 {
		return ref, badRequest("invalid k8sCluster")
	}
	if len(validation.IsDNS1123Label(ref.namespace)) > 0 {
		return ref, badRequest("invalid namespace")
	}
	if len(validation.IsDNS1123Subdomain(ref.name)) > 0 {
		return ref, badRequest("invalid instance")
	}
	return ref, nil
}

// access is a request authorized against an instance.
type access struct {
	engine    engine.Engine
	target    engine.Target
	canManage bool
}

// authorize resolves the instance as the calling user and the engine that
// manages its users. With requireManage, it also requires the caller to be
// allowed to change users.
func (s *server) authorize(r *http.Request, requireManage bool) (access, error) {
	token, err := userToken(r)
	if err != nil {
		return access{}, err
	}
	ref, err := parseInstanceRef(r)
	if err != nil {
		return access{}, err
	}
	in, err := s.everest.getInstance(r.Context(), token, ref)
	if err != nil {
		return access{}, err
	}
	eng, ok := s.engines[in.Spec.ProviderRef.Name]
	if !ok {
		return access{}, &statusError{status: http.StatusNotFound, message: "user management is not supported for this provider"}
	}
	canManage, err := s.everest.canManageUsers(r.Context(), token, ref)
	if err != nil {
		return access{}, err
	}
	if requireManage && !canManage {
		return access{}, &statusError{status: http.StatusForbidden, message: "managing users requires the read-connection permission on the instance"}
	}
	return access{
		engine:    eng,
		target:    engine.Target{Namespace: ref.namespace, Instance: ref.name},
		canManage: canManage,
	}, nil
}

func decodeBody(r *http.Request, w http.ResponseWriter, v any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return badRequest("invalid request body")
	}
	return nil
}

// passwordOrGenerated validates a requested password, or generates one.
func passwordOrGenerated(requested string) (string, error) {
	if requested == "" {
		return password.Generate()
	}
	if err := password.Validate(requested); err != nil {
		return "", badRequest(err.Error())
	}
	return requested, nil
}

// userResourceName derives a valid, collision-free DatabaseUser name from
// the username, which may hold characters Kubernetes names can't.
func userResourceName(instance, username string) string {
	sum := sha256.Sum256([]byte(username))
	stem := instance + "-" + strings.Trim(invalidNameChars.ReplaceAllString(strings.ToLower(username), "-"), "-")
	if len(stem) > maxNameStemLength {
		stem = stem[:maxNameStemLength]
	}
	return strings.Trim(stem, "-") + "-" + hex.EncodeToString(sum[:])[:6]
}

func (s *server) instanceUsers(ctx context.Context, target engine.Target) ([]v1alpha1.DatabaseUser, error) {
	var list v1alpha1.DatabaseUserList
	err := s.kube.List(ctx, &list, client.InNamespace(target.Namespace), client.MatchingFields{controller.InstanceIndex: target.Instance})
	return list.Items, err
}

// databaseUser returns the DatabaseUser with the given ID if it belongs to the target instance.
func (s *server) databaseUser(ctx context.Context, target engine.Target, id string) (*v1alpha1.DatabaseUser, error) {
	u := &v1alpha1.DatabaseUser{}
	if err := s.kube.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: id}, u); err != nil {
		return nil, err
	}
	if u.Spec.InstanceRef.Name != target.Instance {
		return nil, &statusError{status: http.StatusNotFound, message: "user not found"}
	}
	return u, nil
}

func toAPIGrants(grants []engine.RoleGrant) []v1alpha1.RoleGrant {
	out := make([]v1alpha1.RoleGrant, 0, len(grants))
	for _, g := range grants {
		out = append(out, v1alpha1.RoleGrant{Role: g.Role, Database: g.Database})
	}
	return out
}

func managedView(eng engine.Engine, u *v1alpha1.DatabaseUser) userView {
	v := userView{
		ID:                u.Name,
		Username:          u.Spec.Username,
		Database:          eng.AuthDatabase(),
		Roles:             controller.ToEngineGrants(u.Spec.Roles),
		Managed:           true,
		Phase:             u.Status.Phase,
		Message:           u.Status.Message,
		OwnPasswordSecret: u.Spec.PasswordSecretRef != nil,
	}
	if v.Phase == "" {
		v.Phase = v1alpha1.PhasePending
	}
	if !u.DeletionTimestamp.IsZero() {
		v.Phase = v1alpha1.PhaseDeleting
		if u.Status.Phase != v1alpha1.PhaseDeleting {
			v.Message = ""
		}
	}
	return v
}

// GET /api/users?k8sCluster=&namespace=&instance=
func (s *server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	a, err := s.authorize(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	declared, err := s.instanceUsers(r.Context(), a.target)
	if err != nil {
		writeError(w, err)
		return
	}
	resp := usersResponse{Users: []userView{}, Roles: a.engine.Roles(), CanManage: a.canManage}
	managed := map[string]bool{}
	for i := range declared {
		resp.Users = append(resp.Users, managedView(a.engine, &declared[i]))
		managed[declared[i].Spec.Username] = true
	}

	dbUsers, err := a.engine.ListUsers(r.Context(), a.target)
	switch {
	case errors.Is(err, engine.ErrNotReady):
		resp.DatabaseError = err.Error()
	case err != nil:
		log.Printf("list database users of %s/%s: %v", a.target.Namespace, a.target.Instance, err)
		resp.DatabaseError = "the database can't be reached"
	}
	for _, u := range dbUsers {
		if u.Database == a.engine.AuthDatabase() && managed[u.Username] {
			continue
		}
		resp.Users = append(resp.Users, userView{Username: u.Username, Database: u.Database, Roles: u.Roles, System: u.System})
	}

	// Managed users first, then other users, then the operator's.
	rank := func(v userView) int {
		switch {
		case v.Managed:
			return 0
		case !v.System:
			return 1
		}
		return 2
	}
	sort.SliceStable(resp.Users, func(i, j int) bool {
		a, b := resp.Users[i], resp.Users[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return a.Username+"."+a.Database < b.Username+"."+b.Database
	})
	writeJSON(w, http.StatusOK, resp)
}

// POST /api/users?k8sCluster=&namespace=&instance=
func (s *server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
	if err := decodeBody(r, w, &req); err != nil {
		writeError(w, err)
		return
	}
	if !usernamePattern.MatchString(req.Username) {
		writeError(w, badRequest("username must be 1-64 letters, digits, '_', '.', '@' or '-', starting with a letter or digit"))
		return
	}
	pwd, err := passwordOrGenerated(req.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	a, err := s.authorize(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	roles, err := a.engine.NormalizeRoles(req.Roles)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := a.engine.ValidateUser(engine.DesiredUser{Username: req.Username, Roles: roles}); err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	declared, err := s.instanceUsers(ctx, a.target)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, u := range declared {
		if u.Spec.Username == req.Username {
			writeError(w, conflict("user already exists"))
			return
		}
	}
	// Fails fast for the common case; the controller enforces it either way.
	if dbUsers, err := a.engine.ListUsers(ctx, a.target); err == nil {
		for _, u := range dbUsers {
			if u.Username == req.Username && u.Database == a.engine.AuthDatabase() {
				writeError(w, conflict("user already exists in the database"))
				return
			}
		}
	}

	name := userResourceName(a.target.Instance, req.Username)
	user := &v1alpha1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: a.target.Namespace,
			Labels:    map[string]string{controller.InstanceLabel: a.target.Instance},
		},
		Spec: v1alpha1.DatabaseUserSpec{
			InstanceRef: v1alpha1.InstanceReference{Name: a.target.Instance},
			Username:    req.Username,
			Roles:       toAPIGrants(roles),
		},
	}
	creds := credentialsResponse{ID: name, Username: req.Username, Password: pwd, Secret: controller.CredentialsSecretName(user)}
	creds.URI, _ = a.engine.ConnectionURI(ctx, a.target, req.Username, pwd)

	// The Secret comes first so the controller adopts this password instead of generating one.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      creds.Secret,
			Namespace: a.target.Namespace,
			Labels:    map[string]string{controller.InstanceLabel: a.target.Instance},
		},
		Data: map[string][]byte{
			v1alpha1.SecretKeyUsername: []byte(req.Username),
			v1alpha1.SecretKeyPassword: []byte(pwd),
		},
	}
	if creds.URI != "" {
		secret.Data[v1alpha1.SecretKeyURI] = []byte(creds.URI)
	}
	if err := s.kube.Create(ctx, secret); err != nil {
		writeError(w, err)
		return
	}
	if err := s.kube.Create(ctx, user); err != nil {
		_ = s.kube.Delete(ctx, secret)
		writeError(w, err)
		return
	}
	log.Printf("declared user %q on %s/%s as DatabaseUser %s", req.Username, a.target.Namespace, a.target.Instance, name)
	writeJSON(w, http.StatusCreated, creds)
}

// PUT /api/users/{id}/roles?k8sCluster=&namespace=&instance=
func (s *server) handleUpdateRoles(w http.ResponseWriter, r *http.Request) {
	var req updateRolesRequest
	if err := decodeBody(r, w, &req); err != nil {
		writeError(w, err)
		return
	}
	a, err := s.authorize(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	roles, err := a.engine.NormalizeRoles(req.Roles)
	if err != nil {
		writeError(w, err)
		return
	}
	u, err := s.databaseUser(r.Context(), a.target, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	orig := u.DeepCopy()
	u.Spec.Roles = toAPIGrants(roles)
	if err := s.kube.Patch(r.Context(), u, client.MergeFrom(orig)); err != nil {
		writeError(w, err)
		return
	}
	log.Printf("updated roles of DatabaseUser %s/%s", u.Namespace, u.Name)
	w.WriteHeader(http.StatusNoContent)
}

// generatedSecret returns the plugin-managed credentials Secret of a user.
func (s *server) generatedSecret(ctx context.Context, u *v1alpha1.DatabaseUser) (*corev1.Secret, error) {
	if u.Spec.PasswordSecretRef != nil {
		return nil, conflict("the password is kept in Secret " + u.Spec.PasswordSecretRef.Name + ", which is managed outside OpenEverest")
	}
	secret := &corev1.Secret{}
	err := s.kube.Get(ctx, types.NamespacedName{Namespace: u.Namespace, Name: controller.CredentialsSecretName(u)}, secret)
	if apierrors.IsNotFound(err) {
		return nil, conflict("the user's credentials are not created yet")
	}
	return secret, err
}

func credentialsOf(u *v1alpha1.DatabaseUser, secret *corev1.Secret) credentialsResponse {
	return credentialsResponse{
		ID:       u.Name,
		Username: u.Spec.Username,
		Password: string(secret.Data[v1alpha1.SecretKeyPassword]),
		URI:      string(secret.Data[v1alpha1.SecretKeyURI]),
		Secret:   secret.Name,
	}
}

// GET /api/users/{id}/credentials?k8sCluster=&namespace=&instance=
func (s *server) handleGetCredentials(w http.ResponseWriter, r *http.Request) {
	a, err := s.authorize(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	u, err := s.databaseUser(r.Context(), a.target, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	secret, err := s.generatedSecret(r.Context(), u)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, credentialsOf(u, secret))
}

// POST /api/users/{id}/password?k8sCluster=&namespace=&instance=
func (s *server) handleResetPassword(w http.ResponseWriter, r *http.Request) {
	var req resetPasswordRequest
	if err := decodeBody(r, w, &req); err != nil {
		writeError(w, err)
		return
	}
	pwd, err := passwordOrGenerated(req.Password)
	if err != nil {
		writeError(w, err)
		return
	}
	a, err := s.authorize(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	u, err := s.databaseUser(ctx, a.target, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	secret, err := s.generatedSecret(ctx, u)
	if err != nil {
		writeError(w, err)
		return
	}
	// The operator notices the new password in the Secret and applies it.
	orig := secret.DeepCopy()
	secret.Data[v1alpha1.SecretKeyPassword] = []byte(pwd)
	delete(secret.Data, v1alpha1.SecretKeyURI)
	if uri, err := a.engine.ConnectionURI(ctx, a.target, u.Spec.Username, pwd); err == nil {
		secret.Data[v1alpha1.SecretKeyURI] = []byte(uri)
	}
	if err := s.kube.Patch(ctx, secret, client.MergeFrom(orig)); err != nil {
		writeError(w, err)
		return
	}
	log.Printf("reset password of DatabaseUser %s/%s", u.Namespace, u.Name)
	writeJSON(w, http.StatusOK, credentialsOf(u, secret))
}

// DELETE /api/users/{id}?k8sCluster=&namespace=&instance=
func (s *server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	a, err := s.authorize(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	u, err := s.databaseUser(r.Context(), a.target, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	if err := s.kube.Delete(r.Context(), u); client.IgnoreNotFound(err) != nil {
		writeError(w, err)
		return
	}
	log.Printf("deleted DatabaseUser %s/%s", u.Namespace, u.Name)
	w.WriteHeader(http.StatusNoContent)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	// Responses can carry passwords.
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("writeJSON error: %v", err)
	}
}

func writeError(w http.ResponseWriter, err error) {
	status, msg := http.StatusInternalServerError, "internal error"
	var se *statusError
	switch {
	case errors.As(err, &se):
		status, msg = se.status, se.message
	case errors.Is(err, engine.ErrInvalid):
		status, msg = http.StatusBadRequest, err.Error()
	case apierrors.IsNotFound(err):
		status, msg = http.StatusNotFound, "user not found"
	case apierrors.IsAlreadyExists(err):
		status, msg = http.StatusConflict, "user already exists"
	case apierrors.IsInvalid(err):
		status, msg = http.StatusBadRequest, err.Error()
	default:
		log.Printf("request failed: %v", err)
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
