package mongodb

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

type fakeSession struct {
	users   []dbUser
	dropErr error
	dropped []string
	closed  bool
}

func (f *fakeSession) usersInfo(context.Context) ([]dbUser, error) { return f.users, nil }

func (f *fakeSession) dropUser(_ context.Context, db, user string) error {
	f.dropped = append(f.dropped, db+"."+user)
	return f.dropErr
}

func (f *fakeSession) close(context.Context) { f.closed = true }

var testCluster = cluster{
	hosts:       []string{"mongo-rs0.ns.svc.cluster.local:27017"},
	adminUser:   "userAdmin",
	adminPass:   "secret",
	systemUsers: map[string]bool{"userAdmin": true, "clusterMonitor": true},
}

var target = engine.Target{Namespace: "ns", Instance: "mongo"}

type recorder struct{ applied [][]psmdbUser }

func newTestEngine(s *fakeSession, c cluster) (*Engine, *recorder) {
	rec := &recorder{}
	return &Engine{
		resolve: func(context.Context, engine.Target) (cluster, error) { return c, nil },
		dial:    func(context.Context, cluster) (session, error) { return s, nil },
		applyUsers: func(_ context.Context, _ engine.Target, users []psmdbUser) error {
			rec.applied = append(rec.applied, users)
			return nil
		},
	}, rec
}

func TestNormalizeRoles(t *testing.T) {
	e, _ := newTestEngine(&fakeSession{}, testCluster)
	got, err := e.NormalizeRoles([]engine.RoleGrant{
		{Role: "readWrite", Database: "shop"},
		{Role: "readWrite", Database: "shop"},
		{Role: "clusterMonitor", Database: "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.RoleGrant{{Role: "readWrite", Database: "shop"}, {Role: "clusterMonitor"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized = %+v, want %+v", got, want)
	}

	for name, grants := range map[string][]engine.RoleGrant{
		"none":              nil,
		"ungrantable role":  {{Role: "root"}},
		"reserved database": {{Role: "dbOwner", Database: "admin"}},
		"missing database":  {{Role: "read"}},
		"invalid database":  {{Role: "read", Database: "a.b"}},
	} {
		if _, err := e.NormalizeRoles(grants); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestSyncUsersDeclaresOperatorUsers(t *testing.T) {
	e, rec := newTestEngine(&fakeSession{}, testCluster)
	err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{{
		Username: "app",
		Roles:    []engine.RoleGrant{{Role: "readWrite", Database: "shop"}, {Role: "readAnyDatabase"}},
		Password: engine.SecretKeyRef{Name: "app-credentials", Key: "password"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	want := []psmdbUser{{
		Name:              "app",
		DB:                "admin",
		PasswordSecretRef: psmdbSecretRef{Name: "app-credentials", Key: "password"},
		Roles:             []psmdbRole{{Name: "readWrite", DB: "shop"}, {Name: "readAnyDatabase", DB: "admin"}},
	}}
	if len(rec.applied) != 1 || !reflect.DeepEqual(rec.applied[0], want) {
		t.Fatalf("applied = %+v, want %+v", rec.applied, want)
	}

	if err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{{Username: "x", Roles: []engine.RoleGrant{{Role: "root"}}}}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("err = %v, want ErrInvalid", err)
	}
	if len(rec.applied) != 1 {
		t.Fatal("applied invalid users")
	}
}

func TestUsersApplyConfiguration(t *testing.T) {
	obj, err := usersApplyConfiguration(target, []psmdbUser{{Name: "app", DB: "admin", Roles: []psmdbRole{{Name: "read", DB: "shop"}}}})
	if err != nil {
		t.Fatal(err)
	}
	if obj.GetName() != "mongo" || obj.GetNamespace() != "ns" || obj.GetKind() != "PerconaServerMongoDB" || obj.GetAPIVersion() != "psmdb.percona.com/v1" {
		t.Errorf("object identity = %s %s %s/%s", obj.GetAPIVersion(), obj.GetKind(), obj.GetNamespace(), obj.GetName())
	}
	users, _, _ := unstructured.NestedSlice(obj.Object, "spec", "users")
	if len(users) != 1 || users[0].(map[string]any)["name"] != "app" {
		t.Errorf("users = %+v", users)
	}
	if len(obj.Object) != 4 {
		t.Errorf("apply configuration must only carry identity and spec.users: %+v", obj.Object)
	}

	empty, err := usersApplyConfiguration(target, nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, found, _ := unstructured.NestedSlice(empty.Object, "spec", "users"); found {
		t.Error("no users must release spec.users rather than set it")
	}
}

func TestApplyUsersRequiresExistingCluster(t *testing.T) {
	err := newResolver([]runtime.Object{}).applyUsers(context.Background(), target, nil)
	if !errors.Is(err, engine.ErrNotReady) {
		t.Fatalf("err = %v, want ErrNotReady", err)
	}
}

func TestListUsers(t *testing.T) {
	s := &fakeSession{users: []dbUser{
		{User: "clusterMonitor", DB: "admin", Roles: []mongoRole{{Role: "clusterMonitor", DB: "admin"}}},
		{User: "app", DB: "admin", Roles: []mongoRole{{Role: "readWrite", DB: "shop"}, {Role: "readAnyDatabase", DB: "admin"}}},
		{User: "clusterMonitor", DB: "shop", Roles: []mongoRole{{Role: "read", DB: "shop"}}},
	}}
	e, _ := newTestEngine(s, testCluster)
	users, err := e.ListUsers(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !s.closed {
		t.Error("session not closed")
	}
	want := []engine.User{
		{Username: "clusterMonitor", Database: "admin", Roles: []engine.RoleGrant{{Role: "clusterMonitor"}}, System: true},
		{Username: "app", Database: "admin", Roles: []engine.RoleGrant{{Role: "readWrite", Database: "shop"}, {Role: "readAnyDatabase"}}},
		// Same name in another database isn't the operator's user.
		{Username: "clusterMonitor", Database: "shop", Roles: []engine.RoleGrant{{Role: "read", Database: "shop"}}},
	}
	if !reflect.DeepEqual(users, want) {
		t.Fatalf("users = %+v, want %+v", users, want)
	}
}

func TestDropUser(t *testing.T) {
	s := &fakeSession{dropErr: errUserNotFound}
	e, _ := newTestEngine(s, testCluster)
	if err := e.DropUser(context.Background(), target, "app"); err != nil {
		t.Fatalf("missing user: err = %v", err)
	}
	if !reflect.DeepEqual(s.dropped, []string{"admin.app"}) {
		t.Errorf("dropped = %v", s.dropped)
	}

	s.dropErr = errors.New("boom")
	if err := e.DropUser(context.Background(), target, "app"); err == nil {
		t.Fatal("expected error")
	}
}

func TestConnectionURIReplicaSetMembers(t *testing.T) {
	c := testCluster
	c.hosts = []string{"mongo-rs0-0.mongo-rs0:27017", "mongo-rs0-1.mongo-rs0:27017"}
	e, _ := newTestEngine(&fakeSession{}, c)
	uri, err := e.ConnectionURI(context.Background(), target, "app", "password")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mongodb://app:password@mongo-rs0-0.mongo-rs0:27017,mongo-rs0-1.mongo-rs0:27017/?authSource=admin"; uri != want {
		t.Errorf("uri = %q, want %q", uri, want)
	}
}

func TestConnectionURI(t *testing.T) {
	e, _ := newTestEngine(&fakeSession{}, testCluster)
	uri, err := e.ConnectionURI(context.Background(), target, "app", "p@ss word")
	if err != nil {
		t.Fatal(err)
	}
	if want := "mongodb://app:p%40ss%20word@mongo-rs0.ns.svc.cluster.local:27017/?authSource=admin"; uri != want {
		t.Errorf("uri = %s, want %s", uri, want)
	}

	c := testCluster
	c.requireTLS = true
	e, _ = newTestEngine(&fakeSession{}, c)
	uri, _ = e.ConnectionURI(context.Background(), target, "app", "password")
	if want := "mongodb://app:password@mongo-rs0.ns.svc.cluster.local:27017/?authSource=admin&tls=true"; uri != want {
		t.Errorf("uri = %s, want %s", uri, want)
	}
}
