package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openeverest/plugin-user-management/backend/api/v1alpha1"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

type release struct {
	name string
	drop bool
}

type fakeDatabaseEngine struct {
	fakeEngine
	dbs        []engine.Database
	listDBErr  error
	syncDBErr  error
	releaseErr error
	syncedDBs  []engine.DesiredDatabase
	released   []release
}

func (f *fakeDatabaseEngine) ValidateDatabase(name string) error {
	if name == "postgres" {
		return errors.Join(engine.ErrInvalid, errors.New(`database "postgres" is reserved`))
	}
	return nil
}

func (f *fakeDatabaseEngine) ListDatabases(context.Context, engine.Target) ([]engine.Database, error) {
	return f.dbs, f.listDBErr
}

func (f *fakeDatabaseEngine) SyncDatabase(_ context.Context, _ engine.Target, db engine.DesiredDatabase) error {
	f.syncedDBs = append(f.syncedDBs, db)
	return f.syncDBErr
}

func (f *fakeDatabaseEngine) ReleaseDatabase(_ context.Context, _ engine.Target, name string, drop bool) error {
	f.released = append(f.released, release{name, drop})
	return f.releaseErr
}

func owner(name, username, instanceName, phase string) *v1alpha1.InstanceUser {
	u := dbUser(name, username)
	u.Spec.InstanceRef.Name = instanceName
	u.Status.Phase = phase
	return u
}

func instanceDatabase(name, database, ownerName string) *v1alpha1.InstanceDatabase {
	created = created.Add(time.Minute)
	return &v1alpha1.InstanceDatabase{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created)},
		Spec: v1alpha1.InstanceDatabaseSpec{
			InstanceRef: v1alpha1.InstanceReference{Name: "mongo"},
			Name:        database,
			Owner:       v1alpha1.DatabaseOwner{InstanceUserRef: v1alpha1.InstanceUserReference{Name: ownerName}},
		},
	}
}

func deletingDatabase(db *v1alpha1.InstanceDatabase, policy string) *v1alpha1.InstanceDatabase {
	now := metav1.Now()
	db.DeletionTimestamp = &now
	db.Finalizers = []string{DatabaseFinalizer}
	db.Spec.DeletionPolicy = policy
	db.Status.Declared = true
	return db
}

type dbHarness struct {
	t      *testing.T
	client client.Client
	engine *fakeDatabaseEngine
	r      *DatabaseReconciler
}

func newDBHarness(t *testing.T, objs ...client.Object) *dbHarness {
	c := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.InstanceUser{}, &v1alpha1.InstanceDatabase{}).
		WithIndex(&v1alpha1.InstanceDatabase{}, InstanceIndex, func(obj client.Object) []string {
			return []string{obj.(*v1alpha1.InstanceDatabase).Spec.InstanceRef.Name}
		}).
		Build()
	eng := &fakeDatabaseEngine{}
	return &dbHarness{t: t, client: c, engine: eng, r: &DatabaseReconciler{Client: c, Engines: map[string]engine.Engine{
		"provider-cloudnative-pg": eng,
		"percona-server-mongodb":  &fakeEngine{},
	}}}
}

func (h *dbHarness) reconcile() ctrl.Result {
	h.t.Helper()
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "mongo"}})
	if err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (h *dbHarness) get(name string) *v1alpha1.InstanceDatabase {
	h.t.Helper()
	db := &v1alpha1.InstanceDatabase{}
	if err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, db); err != nil {
		h.t.Fatalf("get %s: %v", name, err)
	}
	return db
}

func (h *dbHarness) expectPhase(name, phase, message string) *v1alpha1.InstanceDatabase {
	h.t.Helper()
	db := h.get(name)
	if db.Status.Phase != phase || !strings.Contains(db.Status.Message, message) {
		h.t.Fatalf("%s: phase = %q, message = %q; want %q containing %q", name, db.Status.Phase, db.Status.Message, phase, message)
	}
	return db
}

func TestDatabaseLifecycle(t *testing.T) {
	h := newDBHarness(t, instance("provider-cloudnative-pg"),
		owner("orders-owner", "orders_owner", "mongo", v1alpha1.PhaseReady),
		instanceDatabase("orders", "orders", "orders-owner"))

	if res := h.reconcile(); res.RequeueAfter == 0 {
		t.Error("pending database not requeued")
	}
	db := h.expectPhase("orders", v1alpha1.PhasePending, "waiting for the database to be created")
	if !db.Status.Declared || db.Status.Owner != "orders_owner" {
		t.Fatalf("status = %+v", db.Status)
	}
	if !reflect.DeepEqual(db.Finalizers, []string{DatabaseFinalizer}) || db.Labels[InstanceLabel] != "mongo" ||
		len(db.OwnerReferences) != 1 || db.OwnerReferences[0].UID != "instance-uid" {
		t.Fatalf("metadata = %+v", db.ObjectMeta)
	}
	if want := []engine.DesiredDatabase{{Name: "orders", Owner: "orders_owner"}}; !reflect.DeepEqual(h.engine.syncedDBs, want) {
		t.Fatalf("synced = %+v", h.engine.syncedDBs)
	}

	h.engine.dbs = []engine.Database{{Name: "orders", Owner: "orders_owner"}}
	h.reconcile()
	db = h.expectPhase("orders", v1alpha1.PhaseReady, "")
	if c := db.Status.Conditions; len(c) != 1 || c[0].Status != metav1.ConditionTrue {
		t.Fatalf("conditions = %+v", c)
	}

	// An owner change is applied and Pending until the database reports it.
	u := &v1alpha1.InstanceUser{}
	_ = h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "orders-owner"}, u)
	newOwner := owner("new-owner", "new_owner", "mongo", v1alpha1.PhaseReady)
	if err := h.client.Create(context.Background(), newOwner); err != nil {
		t.Fatal(err)
	}
	if err := h.client.Status().Update(context.Background(), newOwner); err != nil {
		t.Fatal(err)
	}
	db.Spec.Owner.InstanceUserRef.Name = "new-owner"
	if err := h.client.Update(context.Background(), db); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	h.expectPhase("orders", v1alpha1.PhasePending, "")
	if last := h.engine.syncedDBs[len(h.engine.syncedDBs)-1]; last.Owner != "new_owner" {
		t.Fatalf("last sync = %+v", last)
	}
}

func TestDatabaseWaitsForOwner(t *testing.T) {
	h := newDBHarness(t, instance("provider-cloudnative-pg"),
		owner("not-ready", "nr", "mongo", v1alpha1.PhasePending),
		owner("elsewhere", "el", "other-instance", v1alpha1.PhaseReady),
		instanceDatabase("missing-owner", "a", "nobody"),
		instanceDatabase("pending-owner", "b", "not-ready"),
		instanceDatabase("foreign-owner", "c", "elsewhere"))

	h.reconcile()
	h.expectPhase("missing-owner", v1alpha1.PhasePending, `owner InstanceUser "nobody" not found`)
	h.expectPhase("pending-owner", v1alpha1.PhasePending, `waiting for owner InstanceUser "not-ready" to be Ready`)
	h.expectPhase("foreign-owner", v1alpha1.PhaseFailed, `belongs to instance "other-instance"`)
	if len(h.engine.syncedDBs) != 0 {
		t.Fatalf("synced without a ready owner: %+v", h.engine.syncedDBs)
	}
	if h.get("missing-owner").Status.Declared {
		t.Fatal("claimed the name without an owner")
	}
}

func TestDatabaseNoTakeover(t *testing.T) {
	h := newDBHarness(t, instance("provider-cloudnative-pg"),
		owner("o", "o", "mongo", v1alpha1.PhaseReady),
		instanceDatabase("existing", "existing", "o"),
		instanceDatabase("system", "app", "o"),
		instanceDatabase("reserved", "postgres", "o"),
		instanceDatabase("first", "dup", "o"),
		instanceDatabase("second", "dup", "o"),
		instanceDatabase("existing-no-owner", "existing2", "nobody"))
	h.engine.dbs = []engine.Database{{Name: "existing", Owner: "someone"}, {Name: "app", System: true}, {Name: "existing2"}}

	h.reconcile()
	// A conflict is reported before the owner, which may still appear.
	h.expectPhase("existing-no-owner", v1alpha1.PhaseFailed, `database "existing2" already exists`)
	h.expectPhase("existing", v1alpha1.PhaseFailed, `database "existing" already exists and isn't managed`)
	h.expectPhase("system", v1alpha1.PhaseFailed, `database "app" is reserved for the operator`)
	h.expectPhase("reserved", v1alpha1.PhaseFailed, `database "postgres" is reserved`)
	h.expectPhase("first", v1alpha1.PhasePending, "")
	h.expectPhase("second", v1alpha1.PhaseFailed, `another InstanceDatabase already declares database "dup"`)
	if want := []engine.DesiredDatabase{{Name: "dup", Owner: "o"}}; !reflect.DeepEqual(h.engine.syncedDBs, want) {
		t.Fatalf("synced = %+v", h.engine.syncedDBs)
	}

	// Once declared, the database existing is expected.
	h.engine.dbs = append(h.engine.dbs, engine.Database{Name: "dup", Owner: "o"})
	h.reconcile()
	h.expectPhase("first", v1alpha1.PhaseReady, "")
}

func TestDatabaseEngineErrors(t *testing.T) {
	h := newDBHarness(t, instance("provider-cloudnative-pg"),
		owner("o", "o", "mongo", v1alpha1.PhaseReady),
		instanceDatabase("orders", "orders", "o"))

	h.engine.listDBErr = errors.Join(engine.ErrNotReady, errors.New("PostgreSQL cluster is initializing"))
	h.reconcile()
	h.expectPhase("orders", v1alpha1.PhasePending, "initializing")

	h.engine.listDBErr = nil
	h.engine.syncDBErr = errors.Join(engine.ErrInProgress, errors.New(`CloudNativePG can't apply the database: role "o" does not exist`))
	h.reconcile()
	h.expectPhase("orders", v1alpha1.PhasePending, `role "o" does not exist`)

	h.engine.syncDBErr = errors.Join(engine.ErrInvalid, errors.New("bad name"))
	h.reconcile()
	h.expectPhase("orders", v1alpha1.PhaseFailed, "bad name")

	h.engine.syncDBErr = errors.New("connection reset")
	if _, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "mongo"}}); err == nil {
		t.Fatal("unexpected error not returned")
	}
}

func TestDatabaseUnsupportedProvider(t *testing.T) {
	h := newDBHarness(t, instance("percona-server-mongodb"),
		owner("o", "o", "mongo", v1alpha1.PhaseReady),
		instanceDatabase("orders", "orders", "o"),
		deletingDatabase(instanceDatabase("old", "old", "o"), v1alpha1.DeletionPolicyDelete))

	h.reconcile()
	h.expectPhase("orders", v1alpha1.PhaseFailed, `databases are not supported for provider "percona-server-mongodb"`)
	if err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "old"}, &v1alpha1.InstanceDatabase{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted database kept: %v", err)
	}
}

func TestDatabaseDeletion(t *testing.T) {
	notDeclared := deletingDatabase(instanceDatabase("never", "never", "o"), v1alpha1.DeletionPolicyDelete)
	notDeclared.Status.Declared = false
	h := newDBHarness(t, instance("provider-cloudnative-pg"),
		deletingDatabase(instanceDatabase("dropped", "dropped", "o"), v1alpha1.DeletionPolicyDelete),
		deletingDatabase(instanceDatabase("kept", "kept", "o"), v1alpha1.DeletionPolicyRetain),
		notDeclared)

	h.engine.releaseErr = errors.Join(engine.ErrInProgress, errors.New("database is being accessed by other users"))
	if res := h.reconcile(); res.RequeueAfter == 0 {
		t.Error("deleting databases not requeued")
	}
	h.expectPhase("dropped", v1alpha1.PhaseDeleting, "being accessed")
	h.expectPhase("kept", v1alpha1.PhaseDeleting, "being accessed")
	want := []release{{"dropped", true}, {"kept", false}}
	if !reflect.DeepEqual(h.engine.released, want) {
		t.Fatalf("released = %+v, want %+v", h.engine.released, want)
	}
	if err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "never"}, &v1alpha1.InstanceDatabase{}); !apierrors.IsNotFound(err) {
		t.Fatalf("undeclared database not released: %v", err)
	}

	h.engine.releaseErr = nil
	h.reconcile()
	for _, name := range []string{"dropped", "kept"} {
		if err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, &v1alpha1.InstanceDatabase{}); !apierrors.IsNotFound(err) {
			t.Errorf("%s still exists: %v", name, err)
		}
	}
}

func TestDatabaseInstanceGone(t *testing.T) {
	h := newDBHarness(t,
		instanceDatabase("orders", "orders", "o"),
		deletingDatabase(instanceDatabase("old", "old", "o"), v1alpha1.DeletionPolicyDelete))
	h.reconcile()
	h.expectPhase("orders", v1alpha1.PhaseFailed, `instance "mongo" not found`)
	if len(h.engine.released) != 0 {
		t.Fatalf("released without an instance: %+v", h.engine.released)
	}
	if err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "old"}, &v1alpha1.InstanceDatabase{}); !apierrors.IsNotFound(err) {
		t.Fatalf("deleted database kept: %v", err)
	}
}

// Deleting a declared InstanceDatabase applies its policy even while a newer
// one with the same name exists; the newer one takes over once it's gone.
func TestDatabaseDeletionWithSameNameRecreated(t *testing.T) {
	old := deletingDatabase(instanceDatabase("old", "orders", "o"), v1alpha1.DeletionPolicyDelete)
	h := newDBHarness(t, instance("provider-cloudnative-pg"),
		owner("o", "o", "mongo", v1alpha1.PhaseReady),
		old,
		instanceDatabase("new", "orders", "o"))
	h.engine.dbs = []engine.Database{{Name: "orders", Owner: "o"}}

	h.reconcile()
	if want := []release{{"orders", true}}; !reflect.DeepEqual(h.engine.released, want) {
		t.Fatalf("released = %+v, want %+v", h.engine.released, want)
	}
	h.expectPhase("new", v1alpha1.PhaseFailed, "already exists")

	// Dropped: the newer InstanceDatabase claims and creates it again.
	h.engine.dbs = nil
	h.reconcile()
	if db := h.expectPhase("new", v1alpha1.PhasePending, "waiting for the database"); !db.Status.Declared {
		t.Fatal("name not claimed")
	}
	if want := []engine.DesiredDatabase{{Name: "orders", Owner: "o"}}; !reflect.DeepEqual(h.engine.syncedDBs, want) {
		t.Fatalf("synced = %+v", h.engine.syncedDBs)
	}
}
