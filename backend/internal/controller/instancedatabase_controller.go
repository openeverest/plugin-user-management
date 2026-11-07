package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openeverest/plugin-user-management/backend/api/v1alpha1"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// DatabaseFinalizer releases, or drops, the database before the
// InstanceDatabase goes away.
const DatabaseFinalizer = "plugin-user-management.plugins.openeverest.io/release-database"

// DatabaseReconciler reconciles all InstanceDatabases of an instance at once,
// so one read of the instance's databases serves them all and the oldest
// InstanceDatabase wins when two declare the same database.
type DatabaseReconciler struct {
	Client client.Client
	// Engines by provider name (Instance spec.providerRef.name). Only those
	// implementing engine.DatabaseEngine manage databases.
	Engines map[string]engine.Engine
}

// IndexDatabaseInstance registers the InstanceIndex field index for InstanceDatabases.
func IndexDatabaseInstance(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &v1alpha1.InstanceDatabase{}, InstanceIndex, func(obj client.Object) []string {
		db, ok := obj.(*v1alpha1.InstanceDatabase)
		if !ok {
			return nil
		}
		return []string{db.Spec.InstanceRef.Name}
	})
}

// SetupWithManager registers the reconciler.
func (r *DatabaseReconciler) SetupWithManager(mgr ctrl.Manager) error {
	byInstance := func(name string, obj client.Object) []reconcile.Request {
		return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: name}}}
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named("instancedatabase").
		Watches(&v1alpha1.InstanceDatabase{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []reconcile.Request {
				db, ok := obj.(*v1alpha1.InstanceDatabase)
				if !ok {
					return nil
				}
				return byInstance(db.Spec.InstanceRef.Name, db)
			})).
		// An owner becoming Ready, or renamed, lets its databases proceed.
		Watches(&v1alpha1.InstanceUser{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []reconcile.Request {
				u, ok := obj.(*v1alpha1.InstanceUser)
				if !ok {
					return nil
				}
				return byInstance(u.Spec.InstanceRef.Name, u)
			})).
		Complete(r)
}

// databaseState is the outcome of reconciling one InstanceDatabase.
type databaseState struct {
	db            *v1alpha1.InstanceDatabase
	owner         string
	ownerPassword engine.SecretKeyRef
	phase         string
	message       string
	declared      bool
	// available is sticky: set once SyncDatabase succeeded.
	available bool
	// ownerProblem keeps the database from being declared; a conflict with
	// an existing database is reported first, as it won't resolve itself.
	ownerProblem string
	ownerInvalid bool
}

func (s *databaseState) fail(message string) {
	s.phase, s.message = v1alpha1.PhaseFailed, message
}

func (s *databaseState) pending(message string) {
	s.phase, s.message = v1alpha1.PhasePending, message
}

// Reconcile reconciles the InstanceDatabases of the instance named by req.
func (r *DatabaseReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var list v1alpha1.InstanceDatabaseList
	if err := r.Client.List(ctx, &list, client.InNamespace(req.Namespace), client.MatchingFields{InstanceIndex: req.Name}); err != nil {
		return ctrl.Result{}, err
	}
	if len(list.Items) == 0 {
		return ctrl.Result{}, nil
	}
	// Oldest first: it wins when two InstanceDatabases declare the same database.
	sort.Slice(list.Items, func(i, j int) bool {
		a, b := list.Items[i], list.Items[j]
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		}
		return a.Name < b.Name
	})
	var live, deleting []*v1alpha1.InstanceDatabase
	for i := range list.Items {
		if list.Items[i].DeletionTimestamp.IsZero() {
			live = append(live, &list.Items[i])
		} else {
			deleting = append(deleting, &list.Items[i])
		}
	}

	instance := &unstructured.Unstructured{}
	instance.SetGroupVersionKind(InstanceGVK)
	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		return ctrl.Result{}, r.abandon(ctx, live, deleting, fmt.Sprintf("instance %q not found", req.Name))
	}
	if !instance.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, r.abandon(ctx, live, deleting, fmt.Sprintf("instance %q is being deleted", req.Name))
	}
	provider, _, _ := unstructured.NestedString(instance.Object, "spec", "providerRef", "name")
	eng, ok := r.Engines[provider].(engine.DatabaseEngine)
	if !ok {
		return ctrl.Result{}, r.abandon(ctx, live, deleting, fmt.Sprintf("databases are not supported for provider %q", provider))
	}
	target := engine.Target{Namespace: req.Namespace, Instance: req.Name}

	states := make([]*databaseState, 0, len(live))
	names := map[string]bool{}
	for _, db := range live {
		if err := ensureInstanceMetadata(ctx, r.Client, db, instance, DatabaseFinalizer); err != nil {
			return ctrl.Result{}, err
		}
		s := &databaseState{db: db, declared: db.Status.Declared, available: db.Status.Available}
		states = append(states, s)
		if err := eng.ValidateDatabase(db.Spec.Name); err != nil {
			s.fail(err.Error())
			continue
		}
		if names[db.Spec.Name] {
			s.fail(fmt.Sprintf("another InstanceDatabase already declares database %q on this instance", db.Spec.Name))
			continue
		}
		names[db.Spec.Name] = true
		owner, err := r.owner(ctx, db)
		var pending errOwnerPending
		var invalid errOwnerInvalid
		switch {
		case errors.As(err, &invalid):
			s.ownerProblem, s.ownerInvalid = invalid.Error(), true
		case errors.As(err, &pending):
			s.ownerProblem = pending.Error()
		case err != nil:
			return ctrl.Result{}, err
		}
		if owner != nil {
			s.owner, s.ownerPassword = owner.Spec.Username, PasswordRef(owner)
		}
	}

	result, err := r.syncDatabases(ctx, eng, target, states, deleting)
	if err != nil {
		return ctrl.Result{}, err
	}
	for _, s := range states {
		if err := r.writeStatus(ctx, s); err != nil {
			return ctrl.Result{}, err
		}
		if s.phase == v1alpha1.PhasePending {
			result.RequeueAfter = pendingRequeue
		}
	}
	return result, nil
}

// errOwnerPending and errOwnerInvalid explain why a database can't be
// declared with its owner yet, or at all.
type (
	errOwnerPending string
	errOwnerInvalid string
)

func (e errOwnerPending) Error() string { return string(e) }
func (e errOwnerInvalid) Error() string { return string(e) }

// owner returns the InstanceUser owning the database once the plugin has
// declared it, or an errOwnerPending or errOwnerInvalid as the state's outcome.
func (r *DatabaseReconciler) owner(ctx context.Context, db *v1alpha1.InstanceDatabase) (*v1alpha1.InstanceUser, error) {
	name := db.Spec.Owner.InstanceUserRef.Name
	u := &v1alpha1.InstanceUser{}
	err := r.Client.Get(ctx, types.NamespacedName{Namespace: db.Namespace, Name: name}, u)
	if apierrors.IsNotFound(err) {
		return nil, errOwnerPending(fmt.Sprintf("owner InstanceUser %q not found", name))
	}
	if err != nil {
		return nil, err
	}
	if u.Spec.InstanceRef.Name != db.Spec.InstanceRef.Name {
		return nil, errOwnerInvalid(fmt.Sprintf("owner InstanceUser %q belongs to instance %q", name, u.Spec.InstanceRef.Name))
	}
	if !u.DeletionTimestamp.IsZero() {
		return nil, errOwnerPending(fmt.Sprintf("owner InstanceUser %q is being deleted", name))
	}
	// The owner's role only needs to exist; waiting for it to be Ready would
	// deadlock an owner granted a role on its own database.
	if !u.Status.Declared {
		return nil, errOwnerPending(fmt.Sprintf("waiting for owner InstanceUser %q to be created", name))
	}
	return u, nil
}

// syncDatabases declares the valid databases, releases deleted ones and
// works out each database's phase.
func (r *DatabaseReconciler) syncDatabases(ctx context.Context, eng engine.DatabaseEngine, target engine.Target,
	states []*databaseState, deleting []*v1alpha1.InstanceDatabase,
) (ctrl.Result, error) {
	dbs, err := eng.ListDatabases(ctx, target)
	if err != nil {
		if !errors.Is(err, engine.ErrNotReady) {
			return ctrl.Result{}, err
		}
		for _, s := range states {
			if s.phase == "" {
				s.pending(err.Error())
			}
		}
		// Deleted databases stay until the instance can be reached to release them.
		return ctrl.Result{RequeueAfter: pendingRequeue}, nil
	}
	existing := map[string]engine.Database{}
	for _, db := range dbs {
		existing[db.Name] = db
	}
	// Databases a live InstanceDatabase claimed and declared: a deleted
	// InstanceDatabase with the same name hands them over instead of
	// releasing them.
	claimed := map[string]bool{}

	for _, s := range states {
		if s.phase != "" {
			continue
		}
		name := s.db.Spec.Name
		if found, ok := existing[name]; ok && !s.declared {
			if found.System {
				s.fail(fmt.Sprintf("database %q is reserved for the operator", name))
			} else {
				s.fail(fmt.Sprintf("database %q already exists and isn't managed by this InstanceDatabase", name))
			}
			continue
		}
		if s.ownerProblem != "" {
			if s.ownerInvalid {
				s.fail(s.ownerProblem)
			} else {
				s.pending(s.ownerProblem)
			}
			continue
		}
		if !s.declared {
			// Claim the name before creating the database: once it exists, only
			// the InstanceDatabase that claimed it may manage it.
			s.declared = true
			if err := r.patchStatus(ctx, s.db, func(st *v1alpha1.InstanceDatabaseStatus) { st.Declared = true }); err != nil {
				return ctrl.Result{}, err
			}
		}
		claimed[name] = true
		err := eng.SyncDatabase(ctx, target, engine.DesiredDatabase{
			Name: name, Owner: s.owner, OwnerPassword: s.ownerPassword,
			RestrictConnect: s.db.Spec.Connect == v1alpha1.ConnectGranted,
		})
		switch {
		case errors.Is(err, engine.ErrInvalid):
			s.fail(err.Error())
		case errors.Is(err, engine.ErrInProgress), errors.Is(err, engine.ErrNotReady):
			s.pending(err.Error())
		case err != nil:
			return ctrl.Result{}, fmt.Errorf("sync database %q: %w", name, err)
		default:
			s.available = true
			if found, ok := existing[name]; ok && (found.Owner == "" || found.Owner == s.owner) {
				s.phase = v1alpha1.PhaseReady
			} else {
				s.pending("waiting for the database to be created")
			}
		}
	}

	var result ctrl.Result
	for _, db := range deleting {
		if db.Status.Declared && !claimed[db.Spec.Name] {
			drop := db.Spec.DeletionPolicy == v1alpha1.DeletionPolicyDelete
			err := eng.ReleaseDatabase(ctx, target, db.Spec.Name, drop)
			if errors.Is(err, engine.ErrInProgress) {
				s := &databaseState{db: db, declared: true, owner: db.Status.Owner, phase: v1alpha1.PhaseDeleting, message: err.Error()}
				if err := r.writeStatus(ctx, s); err != nil {
					return ctrl.Result{}, err
				}
				result.RequeueAfter = pendingRequeue
				continue
			}
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("release database %q: %w", db.Spec.Name, err)
			}
			log.FromContext(ctx).Info("released database", "database", db.Spec.Name, "dropped", drop, "instanceDatabase", db.Name)
		}
		if err := removeFinalizer(ctx, r.Client, db, DatabaseFinalizer); err != nil {
			return ctrl.Result{}, err
		}
	}
	return result, nil
}

// abandon fails live databases and releases deleted ones when the instance's
// databases can't be managed.
func (r *DatabaseReconciler) abandon(ctx context.Context, live, deleting []*v1alpha1.InstanceDatabase, message string) error {
	for _, db := range deleting {
		if err := removeFinalizer(ctx, r.Client, db, DatabaseFinalizer); err != nil {
			return err
		}
	}
	for _, db := range live {
		s := &databaseState{db: db, declared: db.Status.Declared, owner: db.Status.Owner}
		s.fail(message)
		if err := r.writeStatus(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

func (r *DatabaseReconciler) patchStatus(ctx context.Context, db *v1alpha1.InstanceDatabase, mutate func(*v1alpha1.InstanceDatabaseStatus)) error {
	orig := db.DeepCopy()
	mutate(&db.Status)
	return client.IgnoreNotFound(r.Client.Status().Patch(ctx, db, client.MergeFrom(orig)))
}

func (r *DatabaseReconciler) writeStatus(ctx context.Context, s *databaseState) error {
	db := s.db
	return r.patchStatus(ctx, db, func(st *v1alpha1.InstanceDatabaseStatus) {
		st.Phase = s.phase
		st.Message = s.message
		st.Declared = s.declared
		st.Available = st.Available || s.available
		st.ObservedGeneration = db.Generation
		if s.owner != "" {
			st.Owner = s.owner
		}
		condition := metav1.Condition{
			Type:               v1alpha1.ConditionReady,
			Status:             metav1.ConditionFalse,
			Reason:             s.phase,
			Message:            s.message,
			ObservedGeneration: db.Generation,
		}
		if s.phase == v1alpha1.PhaseReady {
			condition.Status = metav1.ConditionTrue
		}
		meta.SetStatusCondition(&st.Conditions, condition)
	})
}
