package main

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"strings"

	"k8s.io/apimachinery/pkg/util/validation"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/password"
)

const (
	defaultK8sCluster = "main"
	maxBodyBytes      = 64 << 10
)

type server struct {
	everest *everestClient
	// engines by provider name (Instance spec.providerRef.name).
	engines map[string]engine.Engine
}

type instanceRef struct {
	k8sCluster string
	namespace  string
	name       string
}

type usersResponse struct {
	Users []engine.User `json:"users"`
	// Roles the plugin can grant on this instance.
	Roles []engine.Role `json:"roles"`
	// CanManage tells whether the caller may create, change and delete users.
	CanManage bool `json:"canManage"`
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

// GET /api/users?k8sCluster=&namespace=&instance=
func (s *server) handleListUsers(w http.ResponseWriter, r *http.Request) {
	a, err := s.authorize(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	users, err := a.engine.ListUsers(r.Context(), a.target)
	if err != nil {
		writeError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, usersResponse{Users: users, Roles: a.engine.Roles(), CanManage: a.canManage})
}

// POST /api/users?k8sCluster=&namespace=&instance=
func (s *server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	var req createUserRequest
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
	creds, err := a.engine.CreateUser(r.Context(), a.target, engine.NewUser{Username: req.Username, Password: pwd, Roles: req.Roles})
	if err != nil {
		writeError(w, err)
		return
	}
	log.Printf("created user %q on %s/%s", req.Username, a.target.Namespace, a.target.Instance)
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
	id := r.PathValue("id")
	if err := a.engine.UpdateRoles(r.Context(), a.target, id, req.Roles); err != nil {
		writeError(w, err)
		return
	}
	log.Printf("updated roles of user %q on %s/%s", id, a.target.Namespace, a.target.Instance)
	w.WriteHeader(http.StatusNoContent)
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
	id := r.PathValue("id")
	creds, err := a.engine.ResetPassword(r.Context(), a.target, id, pwd)
	if err != nil {
		writeError(w, err)
		return
	}
	log.Printf("reset password of user %q on %s/%s", id, a.target.Namespace, a.target.Instance)
	writeJSON(w, http.StatusOK, creds)
}

// DELETE /api/users/{id}?k8sCluster=&namespace=&instance=
func (s *server) handleDeleteUser(w http.ResponseWriter, r *http.Request) {
	a, err := s.authorize(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	id := r.PathValue("id")
	if err := a.engine.DeleteUser(r.Context(), a.target, id); err != nil {
		writeError(w, err)
		return
	}
	log.Printf("deleted user %q on %s/%s", id, a.target.Namespace, a.target.Instance)
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
	case errors.Is(err, engine.ErrNotFound):
		status, msg = http.StatusNotFound, err.Error()
	case errors.Is(err, engine.ErrConflict):
		status, msg = http.StatusConflict, err.Error()
	case errors.Is(err, engine.ErrNotManageable):
		status, msg = http.StatusForbidden, err.Error()
	case errors.Is(err, engine.ErrNotReady):
		status, msg = http.StatusConflict, err.Error()
	default:
		log.Printf("request failed: %v", err)
		status, msg = http.StatusBadGateway, "database request failed"
	}
	writeJSON(w, status, map[string]string{"error": msg})
}
