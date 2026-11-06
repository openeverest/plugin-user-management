package cnpg

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/pgaccess"
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

func ownerSecret(password string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "owner-credentials", Namespace: "ns"},
		Data:       map[string][]byte{"password": []byte(password)},
	}
}

var ordersOwned = engine.DesiredDatabase{
	Name: "orders", Owner: "orders_owner",
	OwnerPassword: engine.SecretKeyRef{Name: "owner-credentials", Key: "password"},
}

func TestSyncDatabaseDeclaresDatabase(t *testing.T) {
	s := &fakeSession{objects: []pgaccess.Object{{Name: "public.sales"}}}
	e := newTestEngine(s, []runtime.Object{cnpgCluster(appBootstrap, true)}, ownerSecret("owner-pw"))
	if err := e.SyncDatabase(context.Background(), target, ordersOwned); err != nil {
		t.Fatal(err)
	}
	if len(e.applied) != 3 {
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

	// The group roles, without login, retained.
	for i, group := range []string{"oe_orders_read", "oe_orders_readwrite"} {
		role := e.applied[i+1]
		wantSpec := map[string]any{
			"cluster": map[string]any{"name": "pg"}, "name": group, "comment": "Managed by OpenEverest",
			"login": false, "databaseRoleReclaimPolicy": "retain",
		}
		if role.GetKind() != "DatabaseRole" || role.GetName() != databaseRoleName("pg", group) || !reflect.DeepEqual(role.Object["spec"], wantSpec) {
			t.Fatalf("group role = %s %s %v", role.GetKind(), role.GetName(), role.Object["spec"])
		}
	}

	// Access is granted in the database, as its owner.
	if !reflect.DeepEqual(s.dialedAs, []string{"orders/orders_owner/owner-pw"}) {
		t.Fatalf("dialed as %v", s.dialedAs)
	}
	if want := [][]string{pgaccess.PrivilegeStatements("orders", "orders_owner", nil, s.objects, pgaccess.Connect{})}; !reflect.DeepEqual(s.executed, want) {
		t.Fatalf("executed = %q", s.executed)
	}

	for _, name := range []string{"app", "postgres"} {
		if err := e.SyncDatabase(context.Background(), target, engine.DesiredDatabase{Name: name, Owner: "o"}); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestSyncDatabaseWaitsToGrant(t *testing.T) {
	// The owner's password isn't there yet.
	e := newTestEngine(&fakeSession{}, []runtime.Object{cnpgCluster(appBootstrap, true)})
	err := e.SyncDatabase(context.Background(), target, ordersOwned)
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), `password Secret "owner-credentials" is missing`) {
		t.Fatalf("err = %v", err)
	}

	// CloudNativePG hasn't created a group role yet.
	s := &fakeSession{execErr: &pgconn.PgError{Message: `role "oe_orders_read" does not exist`}}
	e = newTestEngine(s, []runtime.Object{cnpgCluster(appBootstrap, true)}, ownerSecret("pw"))
	err = e.SyncDatabase(context.Background(), target, ordersOwned)
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), `can't grant access to the database: role "oe_orders_read" does not exist`) {
		t.Fatalf("err = %v", err)
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
	e = newTestEngine(&fakeSession{}, []runtime.Object{cnpgCluster(appBootstrap, true), failed}, ownerSecret("pw"))
	if err := e.SyncDatabase(context.Background(), target, ordersOwned); err != nil {
		t.Fatalf("stale status: err = %v", err)
	}
}

func TestReleaseDatabase(t *testing.T) {
	for _, drop := range []bool{false, true} {
		name := databaseObjectName("pg", "orders")
		obj := databaseObject(name, map[string]any{instanceLabel: "pg"}, map[string]any{
			"cluster": map[string]any{"name": "pg"}, "name": "orders", "databaseReclaimPolicy": "retain",
		}, map[string]any{"applied": true})
		groups := []runtime.Object{}
		for _, group := range []string{"oe_orders_read", "oe_orders_readwrite"} {
			groups = append(groups, databaseRole(databaseRoleName("pg", group), map[string]any{instanceLabel: "pg"}, map[string]any{
				"cluster": map[string]any{"name": "pg"}, "name": group, "databaseRoleReclaimPolicy": "retain",
			}, map[string]any{"applied": true}))
		}
		e := newTestEngine(&fakeSession{}, append([]runtime.Object{obj}, groups...))

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
		// Then each group role's DatabaseRole, with the same reclaim policy.
		for _, group := range []string{"oe_orders_read", "oe_orders_readwrite"} {
			if err := e.ReleaseDatabase(context.Background(), target, "orders", drop); !errors.Is(err, engine.ErrInProgress) {
				t.Fatalf("drop=%v: err = %v, want ErrInProgress while releasing %s", drop, err, group)
			}
			role, err := e.dyn.Resource(databaseRoleResource).Namespace("ns").Get(context.Background(), databaseRoleName("pg", group), metav1.GetOptions{})
			if !apierrors.IsNotFound(err) {
				t.Fatalf("drop=%v: %s not deleted: %v %v", drop, group, role, err)
			}
		}
		if err := e.ReleaseDatabase(context.Background(), target, "orders", drop); err != nil {
			t.Fatalf("drop=%v: err = %v, want nil once everything is gone", drop, err)
		}
	}
}

func TestDatabaseRolesForUsers(t *testing.T) {
	passwordSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-credentials", Namespace: "ns"}}
	s := &fakeSession{
		dbRoles: []dbRole{{name: "svc", memberOf: []string{"oe_orders_readwrite", "pg_monitor"}}, {name: "oe_orders_readwrite"}},
		dbs:     []dbDatabase{{name: "orders"}, {name: "postgres"}},
	}
	e := newTestEngine(s, []runtime.Object{cnpgCluster(appBootstrap, true)}, passwordSecret)

	roles, err := normalizeRoles([]engine.RoleGrant{{Role: "readWrite", Database: "orders"}, {Role: "pg_monitor"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{
		{Username: "svc", Roles: roles, Password: engine.SecretKeyRef{Name: "app-credentials", Key: "password"}},
	}); err != nil {
		t.Fatal(err)
	}
	inRoles, _, _ := unstructured.NestedStringSlice(e.applied[0].Object, "spec", "inRoles")
	if !reflect.DeepEqual(inRoles, []string{"oe_orders_readwrite", "pg_monitor"}) {
		t.Fatalf("inRoles = %v", inRoles)
	}

	users, err := e.ListUsers(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range users {
		switch u.Username {
		case "svc":
			if !engine.SameRoles(u.Roles, roles) || u.System {
				t.Errorf("svc = %+v, want %+v", u, roles)
			}
		case "oe_orders_readwrite":
			if !u.System {
				t.Errorf("group role isn't a system user: %+v", u)
			}
		}
	}

	if err := validateUser(engine.DesiredUser{Username: "oe_orders_read"}); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("group role name accepted as a username: %v", err)
	}
	if _, err := normalizeRoles([]engine.RoleGrant{{Role: "read"}}); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("read without a database: %v", err)
	}
}

func TestSyncDatabaseRestrictConnect(t *testing.T) {
	s := &fakeSession{existing: []string{"cnpg_metrics_exporter"}}
	e := newTestEngine(s, []runtime.Object{cnpgCluster(appBootstrap, true)}, ownerSecret("pw"))
	want := ordersOwned
	want.RestrictConnect = true
	if err := e.SyncDatabase(context.Background(), target, want); err != nil {
		t.Fatal(err)
	}
	stmts := pgaccess.PrivilegeStatements("orders", "orders_owner", nil, nil, pgaccess.Connect{Restricted: true, Roles: []string{"cnpg_metrics_exporter"}})
	if !reflect.DeepEqual(s.executed, [][]string{stmts}) {
		t.Fatalf("executed = %q", s.executed)
	}
}
