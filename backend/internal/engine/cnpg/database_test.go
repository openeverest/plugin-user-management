package cnpg

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

func databaseObject(name string, labels map[string]any, spec, status map[string]any) *unstructured.Unstructured {
	obj := databaseRole(name, labels, spec, status)
	obj.SetKind("Database")
	return obj
}

var appBootstrap = map[string]any{"bootstrap": map[string]any{"initdb": map[string]any{"database": "app", "owner": "app"}}}

func TestValidateDatabase(t *testing.T) {
	e := &Engine{}
	if err := e.ValidateDatabase("orders"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "postgres", "template0", "template1", strings.Repeat("a", 64)} {
		if err := e.ValidateDatabase(name); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%q: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestListDatabases(t *testing.T) {
	s := &fakeSession{dbs: []dbDatabase{
		{name: "app", owner: "app"},
		{name: "orders", owner: "orders_owner"},
		{name: "postgres", owner: "postgres"},
		{name: "template1", owner: "postgres", template: true},
	}}
	objects := []runtime.Object{
		cnpgCluster(appBootstrap, true),
		// Declared by the provider, or anyone else, for this cluster.
		databaseObject("pg-extensions", nil, map[string]any{"cluster": map[string]any{"name": "pg"}, "name": "app"}, nil),
		databaseObject("analytics", nil, map[string]any{"cluster": map[string]any{"name": "pg"}, "name": "analytics"}, nil),
		databaseObject("other-cluster", nil, map[string]any{"cluster": map[string]any{"name": "other"}, "name": "elsewhere"}, nil),
		// The plugin's own.
		databaseObject(databaseObjectName("pg", "orders"), map[string]any{instanceLabel: "pg"},
			map[string]any{"cluster": map[string]any{"name": "pg"}, "name": "orders"}, nil),
	}
	got, err := newTestEngine(s, objects).ListDatabases(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	want := []engine.Database{
		{Name: "analytics", System: true},
		{Name: "app", Owner: "app", System: true},
		{Name: "orders", Owner: "orders_owner"},
		{Name: "postgres", Owner: "postgres", System: true},
		{Name: "template0", System: true},
		{Name: "template1", Owner: "postgres", System: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("databases =\n%+v\nwant\n%+v", got, want)
	}

	if _, err := newTestEngine(s, []runtime.Object{cnpgCluster(appBootstrap, false)}).ListDatabases(context.Background(), target); !errors.Is(err, engine.ErrNotReady) {
		t.Fatalf("not ready: err = %v", err)
	}
}

func TestSyncDatabaseDeclaresDatabase(t *testing.T) {
	e := newTestEngine(&fakeSession{}, []runtime.Object{cnpgCluster(appBootstrap, true)})
	if err := e.SyncDatabase(context.Background(), target, engine.DesiredDatabase{Name: "orders", Owner: "orders_owner"}); err != nil {
		t.Fatal(err)
	}
	if len(e.applied) != 1 {
		t.Fatalf("applied %d objects", len(e.applied))
	}
	obj := e.applied[0]
	if obj.GetKind() != "Database" || obj.GetName() != databaseObjectName("pg", "orders") || obj.GetLabels()[instanceLabel] != "pg" {
		t.Fatalf("object = %s %s %v", obj.GetKind(), obj.GetName(), obj.GetLabels())
	}
	if refs := obj.GetOwnerReferences(); len(refs) != 1 || refs[0].Kind != "Cluster" || refs[0].UID != "cluster-uid" {
		t.Fatalf("owner references = %+v", refs)
	}
	wantSpec := map[string]any{
		"cluster":               map[string]any{"name": "pg"},
		"name":                  "orders",
		"owner":                 "orders_owner",
		"ensure":                "present",
		"databaseReclaimPolicy": "retain",
	}
	if !reflect.DeepEqual(obj.Object["spec"], wantSpec) {
		t.Fatalf("spec = %v", obj.Object["spec"])
	}

	for _, name := range []string{"app", "postgres"} {
		if err := e.SyncDatabase(context.Background(), target, engine.DesiredDatabase{Name: name, Owner: "o"}); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestSyncDatabaseReportsOperatorError(t *testing.T) {
	name := databaseObjectName("pg", "orders")
	failed := databaseObject(name, map[string]any{instanceLabel: "pg"}, map[string]any{"name": "orders"},
		map[string]any{"applied": false, "message": `role "orders_owner" does not exist`, "observedGeneration": int64(1)})
	e := newTestEngine(&fakeSession{}, []runtime.Object{cnpgCluster(appBootstrap, true), failed})
	err := e.SyncDatabase(context.Background(), target, engine.DesiredDatabase{Name: "orders", Owner: "orders_owner"})
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want ErrInProgress with the operator's reason", err)
	}

	// A failure from an older generation isn't reported.
	_ = unstructured.SetNestedField(failed.Object, int64(0), "status", "observedGeneration")
	e = newTestEngine(&fakeSession{}, []runtime.Object{cnpgCluster(appBootstrap, true), failed})
	if err := e.SyncDatabase(context.Background(), target, engine.DesiredDatabase{Name: "orders", Owner: "orders_owner"}); err != nil {
		t.Fatalf("stale status: err = %v", err)
	}
}

func TestReleaseDatabase(t *testing.T) {
	for _, drop := range []bool{false, true} {
		name := databaseObjectName("pg", "orders")
		obj := databaseObject(name, map[string]any{instanceLabel: "pg"}, map[string]any{
			"cluster": map[string]any{"name": "pg"}, "name": "orders", "databaseReclaimPolicy": "retain",
		}, map[string]any{"applied": true})
		e := newTestEngine(&fakeSession{}, []runtime.Object{obj})

		var policy string
		fake := e.dyn.(*dynamicfake.FakeDynamicClient)
		fake.PrependReactor("delete", "databases", func(k8stesting.Action) (bool, runtime.Object, error) {
			current, err := fake.Tracker().Get(databaseResource, "ns", name)
			if err != nil {
				return true, nil, err
			}
			policy, _, _ = unstructured.NestedString(current.(*unstructured.Unstructured).Object, "spec", "databaseReclaimPolicy")
			return false, nil, nil
		})

		if err := e.ReleaseDatabase(context.Background(), target, "orders", drop); !errors.Is(err, engine.ErrInProgress) {
			t.Fatalf("drop=%v: err = %v, want ErrInProgress", drop, err)
		}
		if want := map[bool]string{false: "retain", true: "delete"}[drop]; policy != want {
			t.Errorf("drop=%v: reclaim policy at deletion = %q, want %q", drop, policy, want)
		}
		if _, err := e.dyn.Resource(databaseResource).Namespace("ns").Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Errorf("drop=%v: Database not deleted: %v", drop, err)
		}
		if err := e.ReleaseDatabase(context.Background(), target, "orders", drop); err != nil {
			t.Fatalf("drop=%v: err = %v, want nil once the Database is gone", drop, err)
		}
	}
}
