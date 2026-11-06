package main

import (
	"context"
	"errors"
	"log"
	"net/http"
	"regexp"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/openeverest/plugin-user-management/backend/api/v1alpha1"
	"github.com/openeverest/plugin-user-management/backend/internal/controller"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// Mirrors the InstanceDatabase CRD's name pattern.
var databaseNamePattern = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// databaseView is a row of the databases list.
type databaseView struct {
	// ID is the InstanceDatabase's name; empty for databases the plugin doesn't manage.
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
	// Owner is the owner's username; OwnerID its InstanceUser, for managed databases.
	Owner          string `json:"owner,omitempty"`
	OwnerID        string `json:"ownerId,omitempty"`
	Connect        string `json:"connect,omitempty"`
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
	// Managed databases are declared by an InstanceDatabase and can be changed.
	Managed bool `json:"managed"`
	// System databases are PostgreSQL's, the provider's or the operator's.
	System  bool   `json:"system"`
	Phase   string `json:"phase,omitempty"`
	Message string `json:"message,omitempty"`
}

type databasesResponse struct {
	Databases []databaseView `json:"databases"`
	// Supported tells whether the instance's engine manages databases.
	Supported bool `json:"supported"`
	CanManage bool `json:"canManage"`
	// DatabaseError explains why databases that exist only in the instance are missing.
	DatabaseError string `json:"databaseError,omitempty"`
}

type createDatabaseRequest struct {
	Name string `json:"name"`
	// OwnerID is the owner's InstanceUser.
	OwnerID string `json:"ownerId"`
	Connect string `json:"connect"`
}

type updateDatabaseRequest struct {
	OwnerID string `json:"ownerId"`
	Connect string `json:"connect"`
}

func databaseEngine(a access) (engine.DatabaseEngine, error) {
	if eng, ok := a.engine.(engine.DatabaseEngine); ok {
		return eng, nil
	}
	return nil, &statusError{status: http.StatusNotFound, message: "databases are not supported for this provider"}
}

func validConnect(connect string) (string, error) {
	switch connect {
	case "":
		return v1alpha1.ConnectPublic, nil
	case v1alpha1.ConnectPublic, v1alpha1.ConnectGranted:
		return connect, nil
	}
	return "", badRequest("connect must be Public or Granted")
}

func (s *server) instanceDatabases(ctx context.Context, target engine.Target) ([]v1alpha1.InstanceDatabase, error) {
	var list v1alpha1.InstanceDatabaseList
	err := s.kube.List(ctx, &list, client.InNamespace(target.Namespace), client.MatchingFields{controller.InstanceIndex: target.Instance})
	return list.Items, err
}

// instanceDatabase returns the InstanceDatabase with the given ID if it belongs to the target instance.
func (s *server) instanceDatabase(ctx context.Context, target engine.Target, id string) (*v1alpha1.InstanceDatabase, error) {
	db := &v1alpha1.InstanceDatabase{}
	if err := s.kube.Get(ctx, types.NamespacedName{Namespace: target.Namespace, Name: id}, db); err != nil {
		return nil, err
	}
	if db.Spec.InstanceRef.Name != target.Instance {
		return nil, &statusError{status: http.StatusNotFound, message: "database not found"}
	}
	return db, nil
}

// ownerUser returns the owner's InstanceUser if it belongs to the target instance.
func (s *server) ownerUser(ctx context.Context, target engine.Target, id string) (*v1alpha1.InstanceUser, error) {
	if id == "" {
		return nil, badRequest("choose the database's owner")
	}
	u, err := s.instanceUser(ctx, target, id)
	if apierrors.IsNotFound(err) || err != nil && errors.As(err, new(*statusError)) {
		return nil, badRequest("the owner must be a user of this instance managed by OpenEverest")
	}
	return u, err
}

func managedDatabaseView(db *v1alpha1.InstanceDatabase, owners map[string]string) databaseView {
	v := databaseView{
		ID:             db.Name,
		Name:           db.Spec.Name,
		OwnerID:        db.Spec.Owner.InstanceUserRef.Name,
		Owner:          db.Status.Owner,
		Connect:        db.Spec.Connect,
		DeletionPolicy: db.Spec.DeletionPolicy,
		Managed:        true,
		Phase:          db.Status.Phase,
		Message:        db.Status.Message,
	}
	if username, ok := owners[v.OwnerID]; ok {
		v.Owner = username
	}
	if v.Connect == "" {
		v.Connect = v1alpha1.ConnectPublic
	}
	if v.DeletionPolicy == "" {
		v.DeletionPolicy = v1alpha1.DeletionPolicyRetain
	}
	if v.Phase == "" {
		v.Phase = v1alpha1.PhasePending
	}
	if !db.DeletionTimestamp.IsZero() {
		v.Phase = v1alpha1.PhaseDeleting
		if db.Status.Phase != v1alpha1.PhaseDeleting {
			v.Message = ""
		}
	}
	return v
}

// GET /api/databases?k8sCluster=&namespace=&instance=
func (s *server) handleListDatabases(w http.ResponseWriter, r *http.Request) {
	a, err := s.authorize(r, false)
	if err != nil {
		writeError(w, err)
		return
	}
	resp := databasesResponse{Databases: []databaseView{}, CanManage: a.canManage}
	eng, err := databaseEngine(a)
	if err != nil {
		writeJSON(w, http.StatusOK, resp)
		return
	}
	resp.Supported = true
	ctx := r.Context()
	declared, err := s.instanceDatabases(ctx, a.target)
	if err != nil {
		writeError(w, err)
		return
	}
	users, err := s.instanceUsers(ctx, a.target)
	if err != nil {
		writeError(w, err)
		return
	}
	owners := map[string]string{}
	for _, u := range users {
		owners[u.Name] = u.Spec.Username
	}
	managed := map[string]bool{}
	for i := range declared {
		resp.Databases = append(resp.Databases, managedDatabaseView(&declared[i], owners))
		managed[declared[i].Spec.Name] = true
	}

	dbs, err := eng.ListDatabases(ctx, a.target)
	switch {
	case errors.Is(err, engine.ErrNotReady):
		resp.DatabaseError = err.Error()
	case err != nil:
		log.Printf("list databases of %s/%s: %v", a.target.Namespace, a.target.Instance, err)
		resp.DatabaseError = "the database can't be reached"
	}
	for _, d := range dbs {
		if !managed[d.Name] {
			resp.Databases = append(resp.Databases, databaseView{Name: d.Name, Owner: d.Owner, System: d.System})
		}
	}

	// Managed databases first, then other databases, then the system's.
	rank := func(v databaseView) int {
		switch {
		case v.Managed:
			return 0
		case !v.System:
			return 1
		}
		return 2
	}
	sort.SliceStable(resp.Databases, func(i, j int) bool {
		a, b := resp.Databases[i], resp.Databases[j]
		if rank(a) != rank(b) {
			return rank(a) < rank(b)
		}
		return a.Name < b.Name
	})
	writeJSON(w, http.StatusOK, resp)
}

// POST /api/databases?k8sCluster=&namespace=&instance=
func (s *server) handleCreateDatabase(w http.ResponseWriter, r *http.Request) {
	var req createDatabaseRequest
	if err := decodeBody(r, w, &req); err != nil {
		writeError(w, err)
		return
	}
	if !databaseNamePattern.MatchString(req.Name) {
		writeError(w, badRequest("database names are 1-63 lowercase letters, digits and underscores, starting with a letter or an underscore"))
		return
	}
	connect, err := validConnect(req.Connect)
	if err != nil {
		writeError(w, err)
		return
	}
	a, err := s.authorize(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	eng, err := databaseEngine(a)
	if err != nil {
		writeError(w, err)
		return
	}
	if err := eng.ValidateDatabase(req.Name); err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	owner, err := s.ownerUser(ctx, a.target, req.OwnerID)
	if err != nil {
		writeError(w, err)
		return
	}
	declared, err := s.instanceDatabases(ctx, a.target)
	if err != nil {
		writeError(w, err)
		return
	}
	for _, db := range declared {
		if db.Spec.Name == req.Name {
			writeError(w, conflict("database already exists"))
			return
		}
	}
	// Fails fast for the common case; the controller enforces it either way.
	if dbs, err := eng.ListDatabases(ctx, a.target); err == nil {
		for _, d := range dbs {
			if d.Name == req.Name {
				writeError(w, conflict("database already exists in the instance"))
				return
			}
		}
	}

	db := &v1alpha1.InstanceDatabase{
		ObjectMeta: metav1.ObjectMeta{
			Name:      userResourceName(a.target.Instance, req.Name),
			Namespace: a.target.Namespace,
			Labels:    map[string]string{controller.InstanceLabel: a.target.Instance},
		},
		Spec: v1alpha1.InstanceDatabaseSpec{
			InstanceRef:    v1alpha1.InstanceReference{Name: a.target.Instance},
			Name:           req.Name,
			Owner:          v1alpha1.DatabaseOwner{InstanceUserRef: v1alpha1.InstanceUserReference{Name: owner.Name}},
			DeletionPolicy: v1alpha1.DeletionPolicyRetain,
			Connect:        connect,
		},
	}
	if err := s.kube.Create(ctx, db); err != nil {
		writeError(w, err)
		return
	}
	log.Printf("declared database %q on %s/%s as InstanceDatabase %s", req.Name, a.target.Namespace, a.target.Instance, db.Name)
	writeJSON(w, http.StatusCreated, managedDatabaseView(db, map[string]string{owner.Name: owner.Spec.Username}))
}

// PUT /api/databases/{id}?k8sCluster=&namespace=&instance=
func (s *server) handleUpdateDatabase(w http.ResponseWriter, r *http.Request) {
	var req updateDatabaseRequest
	if err := decodeBody(r, w, &req); err != nil {
		writeError(w, err)
		return
	}
	connect, err := validConnect(req.Connect)
	if err != nil {
		writeError(w, err)
		return
	}
	a, err := s.authorize(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := databaseEngine(a); err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	db, err := s.instanceDatabase(ctx, a.target, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	owner, err := s.ownerUser(ctx, a.target, req.OwnerID)
	if err != nil {
		writeError(w, err)
		return
	}
	orig := db.DeepCopy()
	db.Spec.Owner.InstanceUserRef.Name = owner.Name
	db.Spec.Connect = connect
	if err := s.kube.Patch(ctx, db, client.MergeFrom(orig)); err != nil {
		writeError(w, err)
		return
	}
	log.Printf("updated InstanceDatabase %s/%s", db.Namespace, db.Name)
	w.WriteHeader(http.StatusNoContent)
}

// DELETE /api/databases/{id}?k8sCluster=&namespace=&instance=&drop=true
// drops the database and its data; without drop it's kept and no longer managed.
func (s *server) handleDeleteDatabase(w http.ResponseWriter, r *http.Request) {
	a, err := s.authorize(r, true)
	if err != nil {
		writeError(w, err)
		return
	}
	if _, err := databaseEngine(a); err != nil {
		writeError(w, err)
		return
	}
	ctx := r.Context()
	db, err := s.instanceDatabase(ctx, a.target, r.PathValue("id"))
	if err != nil {
		writeError(w, err)
		return
	}
	policy := v1alpha1.DeletionPolicyRetain
	if r.URL.Query().Get("drop") == "true" {
		policy = v1alpha1.DeletionPolicyDelete
	}
	if db.Spec.DeletionPolicy != policy {
		orig := db.DeepCopy()
		db.Spec.DeletionPolicy = policy
		if err := s.kube.Patch(ctx, db, client.MergeFrom(orig)); err != nil {
			writeError(w, err)
			return
		}
	}
	if err := s.kube.Delete(ctx, db); err != nil && !apierrors.IsNotFound(err) {
		writeError(w, err)
		return
	}
	log.Printf("deleted InstanceDatabase %s/%s (%s)", db.Namespace, db.Name, policy)
	w.WriteHeader(http.StatusNoContent)
}
