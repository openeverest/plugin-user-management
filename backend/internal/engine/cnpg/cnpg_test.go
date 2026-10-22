package cnpg

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var target = engine.Target{Namespace: "ns", Instance: "pg"}

func cnpgCluster(spec map[string]any, ready bool) *unstructured.Unstructured {
	status := "False"
	if ready {
		status = "True"
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata":   map[string]any{"name": "pg", "namespace": "ns", "uid": "cluster-uid"},
		"spec":       spec,
		"status": map[string]any{
			"phase":      "Cluster in healthy state",
			"conditions": []any{map[string]any{"type": "Ready", "status": status}},
		},
	}}
}

func databaseRole(name string, labels map[string]any, spec, status map[string]any) *unstructured.Unstructured {
	metadata := map[string]any{"name": name, "namespace": "ns", "generation": int64(1)}
	if labels != nil {
		metadata["labels"] = labels
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "DatabaseRole",
		"metadata":   metadata,
		"spec":       spec,
		"status":     status,
	}}
}

type fakeSession struct {
	dbRoles []dbRole
	closed  bool
}

func (f *fakeSession) roles(context.Context) ([]dbRole, error) { return f.dbRoles, nil }
func (f *fakeSession) close(context.Context)                   { f.closed = true }

type testEngine struct {
	*Engine
	applied []*unstructured.Unstructured
}

func newTestEngine(s *fakeSession, objects []runtime.Object, secrets ...runtime.Object) *testEngine {
	gvrs := map[schema.GroupVersionResource]string{
		clusterResource:      "ClusterList",
		databaseRoleResource: "DatabaseRoleList",
	}
	te := &testEngine{}
	te.Engine = &Engine{
		kube: kubefake.NewClientset(secrets...),
		dyn:  dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, objects...),
		dial: func(context.Context, cluster) (session, error) { return s, nil },
		apply: func(_ context.Context, obj *unstructured.Unstructured) error {
			te.applied = append(te.applied, obj)
			return nil
		},
	}
	return te
}

func TestNormalizeRoles(t *testing.T) {
	got, err := normalizeRoles([]engine.RoleGrant{
		{Role: "pg_read_all_data"},
		{Role: "pg_read_all_data", Database: "shop"},
		{Role: "pg_monitor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.RoleGrant{{Role: "pg_read_all_data"}, {Role: "pg_monitor"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized = %+v, want %+v", got, want)
	}

	for name, grants := range map[string][]engine.RoleGrant{
		"none":             nil,
		"ungrantable role": {{Role: "pg_execute_server_program"}},
		"superuser":        {{Role: "postgres"}},
	} {
		if _, err := normalizeRoles(grants); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestValidateUser(t *testing.T) {
	valid := engine.DesiredUser{Username: "orders_svc", Password: engine.SecretKeyRef{Name: "s", Key: "password"}}
	if err := validateUser(valid); err != nil {
		t.Fatalf("valid user: %v", err)
	}
	for name, user := range map[string]engine.DesiredUser{
		"superuser":          {Username: "postgres"},
		"replication user":   {Username: "streaming_replica"},
		"predefined prefix":  {Username: "pg_reader"},
		"operator prefix":    {Username: "cnpg_pooler"},
		"too long":           {Username: strings.Repeat("a", 64)},
		"other password key": {Username: "app", Password: engine.SecretKeyRef{Name: "s", Key: "pw"}},
	} {
		if err := validateUser(user); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestClusterFrom(t *testing.T) {
	obj := cnpgCluster(map[string]any{
		"bootstrap": map[string]any{"initdb": map[string]any{
			"database": "orders", "owner": "orders_owner", "secret": map[string]any{"name": "orders-app"},
		}},
		"superuserSecret": map[string]any{"name": "pg-root"},
		"managed":         map[string]any{"roles": []any{map[string]any{"name": "dba"}}},
	}, true)
	_ = unstructured.SetNestedField(obj.Object, "pg-server-ca", "status", "certificates", "serverCASecret")

	c := clusterFrom(obj)
	if !c.ready || c.uid != "cluster-uid" || c.host != "pg-rw.ns.svc" || c.database != "orders" || c.caSecret != "pg-server-ca" {
		t.Errorf("cluster = %+v", c)
	}
	if !reflect.DeepEqual(c.loginSecrets, []string{"orders-app", "pg-root"}) {
		t.Errorf("login secrets = %v", c.loginSecrets)
	}
	for _, role := range []string{"postgres", "streaming_replica", "orders_owner", "dba", "cnpg_pooler_pgbouncer"} {
		if !c.isSystemRole(role) {
			t.Errorf("%s is not a system role", role)
		}
	}
	if c.isSystemRole("app") {
		t.Error("app is a system role without being the owner")
	}

	defaults := clusterFrom(cnpgCluster(map[string]any{}, false))
	if defaults.ready || defaults.database != "postgres" || defaults.caSecret != "pg-ca" ||
		!reflect.DeepEqual(defaults.loginSecrets, []string{"pg-app", "pg-superuser"}) {
		t.Errorf("defaults = %+v", defaults)
	}
}

func TestSyncUsersDeclaresDatabaseRoles(t *testing.T) {
	passwordSecret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "app-credentials", Namespace: "ns"}}
	e := newTestEngine(&fakeSession{}, []runtime.Object{cnpgCluster(map[string]any{}, true)}, passwordSecret)

	err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{
		{Username: "postgres", Roles: []engine.RoleGrant{{Role: "pg_monitor"}}, Password: engine.SecretKeyRef{Name: "x", Key: "password"}},
		{
			Username: "app",
			Roles:    []engine.RoleGrant{{Role: "pg_read_all_data"}, {Role: "pg_write_all_data"}},
			Password: engine.SecretKeyRef{Name: "app-credentials", Key: "password"},
		},
	})
	if !errors.Is(err, engine.ErrInvalid) || !strings.Contains(err.Error(), `user "postgres"`) {
		t.Errorf("err = %v, want the reserved user rejected", err)
	}
	if len(e.applied) != 1 {
		t.Fatalf("applied %d DatabaseRoles, want the valid user's only", len(e.applied))
	}

	role := e.applied[0]
	if role.GetName() != databaseRoleName("pg", "app") || role.GetNamespace() != "ns" || role.GetLabels()[instanceLabel] != "pg" {
		t.Errorf("metadata = %s/%s %v", role.GetNamespace(), role.GetName(), role.GetLabels())
	}
	if owners := role.GetOwnerReferences(); len(owners) != 1 || owners[0].Kind != "Cluster" || owners[0].UID != "cluster-uid" {
		t.Errorf("owners = %+v", owners)
	}
	wantSpec := map[string]any{
		"cluster":                   map[string]any{"name": "pg"},
		"name":                      "app",
		"comment":                   roleComment,
		"login":                     true,
		"inRoles":                   []any{"pg_read_all_data", "pg_write_all_data"},
		"passwordSecret":            map[string]any{"name": "app-credentials"},
		"databaseRoleReclaimPolicy": "retain",
	}
	if !reflect.DeepEqual(role.Object["spec"], wantSpec) {
		t.Errorf("spec = %+v", role.Object["spec"])
	}

	secret, err := e.kube.CoreV1().Secrets("ns").Get(context.Background(), "app-credentials", metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if secret.Labels[reloadLabel] != "true" {
		t.Errorf("password secret labels = %v, want the reload label", secret.Labels)
	}
}

func TestSyncUsersNeedsCluster(t *testing.T) {
	e := newTestEngine(&fakeSession{}, nil)
	if err := e.SyncUsers(context.Background(), target, nil); !errors.Is(err, engine.ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
}

func TestListUsers(t *testing.T) {
	s := &fakeSession{dbRoles: []dbRole{
		{name: "app", memberOf: []string{}},
		{name: "orders_svc", memberOf: []string{"pg_read_all_data"}},
		{name: "postgres", memberOf: []string{}},
		{name: "streaming_replica", memberOf: []string{}},
	}}
	objects := []runtime.Object{
		cnpgCluster(map[string]any{
			"bootstrap": map[string]any{"initdb": map[string]any{"database": "app", "owner": "app"}},
			"managed":   map[string]any{"roles": []any{map[string]any{"name": "dba"}}},
		}, true),
		// Declared outside the plugin, not created yet.
		databaseRole("gitops-reporting", nil, map[string]any{"cluster": map[string]any{"name": "pg"}, "name": "reporting", "inRoles": []any{"pg_monitor"}}, nil),
		// The plugin's own and another cluster's are not foreign to this cluster.
		databaseRole("pg-orders-svc-abc123", map[string]any{instanceLabel: "pg"}, map[string]any{"cluster": map[string]any{"name": "pg"}, "name": "orders_svc"}, nil),
		databaseRole("other-x", nil, map[string]any{"cluster": map[string]any{"name": "other"}, "name": "x"}, nil),
	}
	e := newTestEngine(s, objects)

	users, err := e.ListUsers(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !s.closed {
		t.Error("session not closed")
	}
	want := []engine.User{
		{Username: "app", Roles: []engine.RoleGrant{}, System: true},
		{Username: "orders_svc", Roles: []engine.RoleGrant{{Role: "pg_read_all_data"}}},
		{Username: "postgres", Roles: []engine.RoleGrant{}, System: true},
		{Username: "streaming_replica", Roles: []engine.RoleGrant{}, System: true},
		{Username: "reporting", Roles: []engine.RoleGrant{{Role: "pg_monitor"}}},
		{Username: "dba", Roles: []engine.RoleGrant{}, System: true},
	}
	if !reflect.DeepEqual(users, want) {
		t.Fatalf("users = %+v\nwant    %+v", users, want)
	}
}

func TestListUsersNotReady(t *testing.T) {
	for name, objects := range map[string][]runtime.Object{
		"missing":      nil,
		"initializing": {cnpgCluster(map[string]any{}, false)},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newTestEngine(&fakeSession{}, objects).ListUsers(context.Background(), target)
			if !errors.Is(err, engine.ErrNotReady) {
				t.Fatalf("err = %v, want ErrNotReady", err)
			}
		})
	}
}

func TestDropUser(t *testing.T) {
	name := databaseRoleName("pg", "app")
	role := databaseRole(name, map[string]any{instanceLabel: "pg"}, map[string]any{
		"cluster": map[string]any{"name": "pg"}, "name": "app", "databaseRoleReclaimPolicy": "retain",
	}, map[string]any{"applied": true})
	e := newTestEngine(&fakeSession{}, []runtime.Object{role})
	roles := e.dyn.Resource(databaseRoleResource).Namespace("ns")

	var patchedPolicy string
	fake := e.dyn.(*dynamicfake.FakeDynamicClient)
	fake.PrependReactor("delete", "databaseroles", func(k8stesting.Action) (bool, runtime.Object, error) {
		current, err := fake.Tracker().Get(databaseRoleResource, "ns", name)
		if err != nil {
			return true, nil, err
		}
		patchedPolicy, _, _ = unstructured.NestedString(current.(*unstructured.Unstructured).Object, "spec", "databaseRoleReclaimPolicy")
		return false, nil, nil
	})

	if err := e.DropUser(context.Background(), target, "app"); !errors.Is(err, engine.ErrInProgress) {
		t.Fatalf("err = %v, want ErrInProgress while CloudNativePG drops the role", err)
	}
	if patchedPolicy != "delete" {
		t.Errorf("reclaim policy at deletion = %q, want delete", patchedPolicy)
	}
	if _, err := roles.Get(context.Background(), name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Errorf("DatabaseRole not deleted: %v", err)
	}

	// Gone once CloudNativePG dropped the role.
	if err := e.DropUser(context.Background(), target, "app"); err != nil {
		t.Fatalf("err = %v, want nil once the DatabaseRole is gone", err)
	}
}

func TestDropUserReportsOperatorError(t *testing.T) {
	name := databaseRoleName("pg", "app")
	role := databaseRole(name, map[string]any{instanceLabel: "pg"}, map[string]any{"name": "app"},
		map[string]any{"applied": false, "message": "role owns objects"})
	now := metav1.Now()
	role.SetDeletionTimestamp(&now)
	role.SetFinalizers([]string{"cnpg.io/deleteDatabaseRole"})
	e := newTestEngine(&fakeSession{}, []runtime.Object{role})

	err := e.DropUser(context.Background(), target, "app")
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), "role owns objects") {
		t.Fatalf("err = %v, want ErrInProgress with the operator's reason", err)
	}
}

func TestPendingReason(t *testing.T) {
	failing := databaseRole(databaseRoleName("pg", "app"), nil, map[string]any{"name": "app"},
		map[string]any{"applied": false, "message": "the username in secret does not match role"})
	applied := databaseRole(databaseRoleName("pg", "ok"), nil, map[string]any{"name": "ok"}, map[string]any{"applied": true})
	e := newTestEngine(&fakeSession{}, []runtime.Object{failing, applied})

	if got := e.PendingReason(context.Background(), target, "app"); !strings.Contains(got, "does not match role") {
		t.Errorf("reason = %q", got)
	}
	for _, username := range []string{"ok", "missing"} {
		if got := e.PendingReason(context.Background(), target, username); got != "" {
			t.Errorf("%s: reason = %q, want none", username, got)
		}
	}
}

func TestConnectionURI(t *testing.T) {
	e := newTestEngine(&fakeSession{}, []runtime.Object{cnpgCluster(map[string]any{
		"bootstrap": map[string]any{"initdb": map[string]any{"database": "app", "owner": "app"}},
	}, true)})
	uri, err := e.ConnectionURI(context.Background(), target, "orders_svc", "p@ss word")
	if err != nil {
		t.Fatal(err)
	}
	if want := "postgresql://orders_svc:p%40ss%20word@pg-rw.ns.svc:5432/app?sslmode=require"; uri != want {
		t.Errorf("uri = %s, want %s", uri, want)
	}
}

func TestDatabaseRoleName(t *testing.T) {
	a, b := databaseRoleName("pg", "Orders_Svc"), databaseRoleName("pg", "orders_svc")
	if a == b {
		t.Error("usernames differing in case must not share a DatabaseRole")
	}
	if !strings.HasPrefix(b, "pg-orders-svc-") {
		t.Errorf("name = %s", b)
	}
	if long := databaseRoleName(strings.Repeat("i", 250), "app"); len(long) > 253 {
		t.Errorf("name too long: %d", len(long))
	}
}
