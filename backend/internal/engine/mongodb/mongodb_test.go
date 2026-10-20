package mongodb

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

type fakeSession struct {
	users   []dbUser
	created []dbUser
	updated []string
	dropped []string
	pwd     map[string]string
	err     error
	closed  bool
}

func (f *fakeSession) usersInfo(context.Context) ([]dbUser, error) { return f.users, nil }

func (f *fakeSession) createUser(_ context.Context, db, user, password string, roles []mongoRole) error {
	if f.err != nil {
		return f.err
	}
	f.created = append(f.created, dbUser{ID: db + "." + user, User: user, DB: db, Roles: roles})
	f.pwd = map[string]string{user: password}
	return nil
}

func (f *fakeSession) updateUser(_ context.Context, db, user, password string, roles []mongoRole) error {
	f.updated = append(f.updated, db+"."+user)
	if password != "" {
		f.pwd = map[string]string{user: password}
	}
	if roles != nil {
		for i := range f.users {
			if f.users[i].DB == db && f.users[i].User == user {
				f.users[i].Roles = roles
			}
		}
	}
	return nil
}

func (f *fakeSession) dropUser(_ context.Context, db, user string) error {
	f.dropped = append(f.dropped, db+"."+user)
	return nil
}

func (f *fakeSession) close(context.Context) { f.closed = true }

var testCluster = cluster{
	host:        "mongo-rs0.ns.svc.cluster.local:27017",
	adminUser:   "userAdmin",
	adminPass:   "secret",
	systemUsers: map[string]bool{"userAdmin": true, "clusterMonitor": true, "databaseAdmin": true},
}

func newTestEngine(s *fakeSession, c cluster) *Engine {
	return &Engine{
		resolve: func(context.Context, engine.Target) (cluster, error) { return c, nil },
		dial:    func(context.Context, cluster) (session, error) { return s, nil },
	}
}

var target = engine.Target{Namespace: "ns", Instance: "mongo"}

func seededSession() *fakeSession {
	return &fakeSession{users: []dbUser{
		{ID: "admin.clusterMonitor", User: "clusterMonitor", DB: "admin", Roles: []mongoRole{{Role: "clusterMonitor", DB: "admin"}}},
		{ID: "admin.app", User: "app", DB: "admin", Roles: []mongoRole{{Role: "readWrite", DB: "shop"}, {Role: "readAnyDatabase", DB: "admin"}}},
		{ID: "admin.boss", User: "boss", DB: "admin", Roles: []mongoRole{{Role: "root", DB: "admin"}}},
		{ID: "shop.legacy", User: "legacy", DB: "shop", Roles: []mongoRole{{Role: "read", DB: "shop"}}},
		{ID: "admin.sneaky", User: "sneaky", DB: "admin", Roles: []mongoRole{{Role: "dbOwner", DB: "admin"}}},
	}}
}

func TestListUsers(t *testing.T) {
	s := seededSession()
	users, err := newTestEngine(s, testCluster).ListUsers(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if !s.closed {
		t.Error("session not closed")
	}

	type summary struct {
		id         string
		system     bool
		manageable bool
	}
	var got []summary
	for _, u := range users {
		got = append(got, summary{u.ID, u.System, u.Manageable})
	}
	want := []summary{
		{"admin.app", false, true},
		{"admin.boss", false, false},   // root isn't grantable
		{"admin.sneaky", false, false}, // dbOwner on admin isn't grantable
		{"shop.legacy", false, true},
		{"admin.clusterMonitor", true, false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("users = %+v, want %+v", got, want)
	}

	wantRoles := []engine.RoleGrant{{Role: "readWrite", Database: "shop"}, {Role: "readAnyDatabase"}}
	if !reflect.DeepEqual(users[0].Roles, wantRoles) {
		t.Errorf("roles = %+v, want %+v", users[0].Roles, wantRoles)
	}
}

func TestCreateUser(t *testing.T) {
	s := seededSession()
	creds, err := newTestEngine(s, testCluster).CreateUser(context.Background(), target, engine.NewUser{
		Username: "reporting",
		Password: "p@ss word",
		Roles:    []engine.RoleGrant{{Role: "read", Database: "shop"}, {Role: "read", Database: "shop"}, {Role: "clusterMonitor", Database: "ignored"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := dbUser{ID: "admin.reporting", User: "reporting", DB: "admin", Roles: []mongoRole{{Role: "read", DB: "shop"}, {Role: "clusterMonitor", DB: "admin"}}}
	if len(s.created) != 1 || !reflect.DeepEqual(s.created[0], want) {
		t.Fatalf("created = %+v, want %+v", s.created, want)
	}
	wantURI := "mongodb://reporting:p%40ss%20word@mongo-rs0.ns.svc.cluster.local:27017/?authSource=admin"
	if creds.URI != wantURI || creds.Password != "p@ss word" || creds.Username != "reporting" {
		t.Errorf("credentials = %+v, want URI %s", creds, wantURI)
	}
}

func TestCreateUserTLSURI(t *testing.T) {
	c := testCluster
	c.requireTLS = true
	creds, err := newTestEngine(seededSession(), c).CreateUser(context.Background(), target, engine.NewUser{
		Username: "u", Password: "password", Roles: []engine.RoleGrant{{Role: "readAnyDatabase"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if want := "mongodb://u:password@mongo-rs0.ns.svc.cluster.local:27017/?authSource=admin&tls=true"; creds.URI != want {
		t.Errorf("URI = %s, want %s", creds.URI, want)
	}
}

func TestCreateUserRejects(t *testing.T) {
	cases := map[string]struct {
		user engine.NewUser
		want error
	}{
		"bad username":         {engine.NewUser{Username: "-x", Roles: []engine.RoleGrant{{Role: "read", Database: "a"}}}, engine.ErrInvalid},
		"no roles":             {engine.NewUser{Username: "x"}, engine.ErrInvalid},
		"ungrantable role":     {engine.NewUser{Username: "x", Roles: []engine.RoleGrant{{Role: "root"}}}, engine.ErrInvalid},
		"reserved database":    {engine.NewUser{Username: "x", Roles: []engine.RoleGrant{{Role: "dbOwner", Database: "admin"}}}, engine.ErrInvalid},
		"missing database":     {engine.NewUser{Username: "x", Roles: []engine.RoleGrant{{Role: "read"}}}, engine.ErrInvalid},
		"invalid database":     {engine.NewUser{Username: "x", Roles: []engine.RoleGrant{{Role: "read", Database: "a.b"}}}, engine.ErrInvalid},
		"operator system user": {engine.NewUser{Username: "userAdmin", Roles: []engine.RoleGrant{{Role: "read", Database: "a"}}}, engine.ErrConflict},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			s := seededSession()
			_, err := newTestEngine(s, testCluster).CreateUser(context.Background(), target, tc.user)
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
			if len(s.created) != 0 {
				t.Fatalf("user created: %+v", s.created)
			}
		})
	}
}

func TestChangesOnlyManageableUsers(t *testing.T) {
	ctx := context.Background()
	for id, want := range map[string]error{
		"admin.app":            nil,
		"shop.legacy":          nil,
		"admin.boss":           engine.ErrNotManageable,
		"admin.clusterMonitor": engine.ErrNotManageable,
		"admin.missing":        engine.ErrNotFound,
		"nodot":                engine.ErrNotFound,
	} {
		t.Run(id, func(t *testing.T) {
			s := seededSession()
			e := newTestEngine(s, testCluster)

			if err := e.UpdateRoles(ctx, target, id, []engine.RoleGrant{{Role: "read", Database: "x"}}); !errors.Is(err, want) {
				t.Errorf("UpdateRoles err = %v, want %v", err, want)
			}
			if _, err := e.ResetPassword(ctx, target, id, "newpassword"); !errors.Is(err, want) {
				t.Errorf("ResetPassword err = %v, want %v", err, want)
			}
			if err := e.DeleteUser(ctx, target, id); !errors.Is(err, want) {
				t.Errorf("DeleteUser err = %v, want %v", err, want)
			}
			changed := len(s.updated) + len(s.dropped)
			if want == nil && changed != 3 {
				t.Errorf("expected 2 updates and 1 drop, got updated=%v dropped=%v", s.updated, s.dropped)
			}
			if want != nil && changed != 0 {
				t.Errorf("unexpected changes: updated=%v dropped=%v", s.updated, s.dropped)
			}
		})
	}
}

func TestResetPasswordUsesUserAuthDatabase(t *testing.T) {
	s := seededSession()
	creds, err := newTestEngine(s, testCluster).ResetPassword(context.Background(), target, "shop.legacy", "newpassword")
	if err != nil {
		t.Fatal(err)
	}
	if s.pwd["legacy"] != "newpassword" {
		t.Errorf("password not updated: %v", s.pwd)
	}
	if want := "mongodb://legacy:newpassword@mongo-rs0.ns.svc.cluster.local:27017/?authSource=shop"; creds.URI != want {
		t.Errorf("URI = %s, want %s", creds.URI, want)
	}
}

func TestUpdateRolesReplacesRoles(t *testing.T) {
	s := seededSession()
	err := newTestEngine(s, testCluster).UpdateRoles(context.Background(), target, "admin.app", []engine.RoleGrant{{Role: "dbAdmin", Database: "shop"}})
	if err != nil {
		t.Fatal(err)
	}
	if got := s.users[1].Roles; !reflect.DeepEqual(got, []mongoRole{{Role: "dbAdmin", DB: "shop"}}) {
		t.Errorf("roles = %+v", got)
	}
}
