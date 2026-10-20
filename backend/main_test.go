package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openeverest/plugin-user-management/backend/api/v1alpha1"
	"github.com/openeverest/plugin-user-management/backend/internal/controller"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

const testQuery = "?namespace=ns&instance=mongo"

type fakeEngine struct {
	dbUsers []engine.User
	listErr error
}

func (f *fakeEngine) Roles() []engine.Role {
	return []engine.Role{{Name: "read", Scope: engine.ScopeDatabase}, {Name: "readAnyDatabase", Scope: engine.ScopeCluster}}
}

func (f *fakeEngine) AuthDatabase() string { return "admin" }

func (f *fakeEngine) NormalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	if len(grants) == 0 {
		return nil, errors.Join(engine.ErrInvalid, errors.New("at least one role is required"))
	}
	out := []engine.RoleGrant{}
	for _, g := range grants {
		switch g.Role {
		case "read":
			out = append(out, g)
		case "readAnyDatabase":
			out = append(out, engine.RoleGrant{Role: g.Role})
		default:
			return nil, errors.Join(engine.ErrInvalid, errors.New("role can't be granted"))
		}
	}
	return out, nil
}

func (f *fakeEngine) SyncUsers(context.Context, engine.Target, []engine.DesiredUser) error {
	return nil
}

func (f *fakeEngine) ListUsers(context.Context, engine.Target) ([]engine.User, error) {
	return f.dbUsers, f.listErr
}

func (f *fakeEngine) DropUser(context.Context, engine.Target, string) error { return nil }

func (f *fakeEngine) ConnectionURI(_ context.Context, _ engine.Target, username, password string) (string, error) {
	return "mongodb://" + username + ":" + password + "@host/", nil
}

// fakeEverest serves an Instance and its connection endpoint with the given statuses.
func fakeEverest(t *testing.T, provider string, instanceStatus, connectionStatus int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer token" {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		switch r.URL.Path {
		case "/v1/clusters/main/namespaces/ns/instances/mongo":
			w.WriteHeader(instanceStatus)
			_, _ = w.Write([]byte(`{"spec":{"providerRef":{"name":"` + provider + `"}}}`))
		case "/v1/clusters/main/namespaces/ns/instances/mongo/connection":
			w.WriteHeader(connectionStatus)
			_, _ = w.Write([]byte(`{"password":"admin-secret"}`))
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

type testEnv struct {
	t      *testing.T
	kube   client.Client
	engine *fakeEngine
	h      http.Handler
}

func newTestEnv(t *testing.T, provider string, instanceStatus, connectionStatus int, objs ...client.Object) *testEnv {
	t.Helper()
	scheme, err := newScheme()
	if err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithIndex(&v1alpha1.DatabaseUser{}, controller.InstanceIndex, func(obj client.Object) []string {
			return []string{obj.(*v1alpha1.DatabaseUser).Spec.InstanceRef.Name}
		}).
		Build()
	eng := &fakeEngine{}
	s := &server{
		everest: newEverestClient(fakeEverest(t, provider, instanceStatus, connectionStatus).URL),
		kube:    kube,
		engines: map[string]engine.Engine{"percona-server-mongodb": eng},
	}
	return &testEnv{t: t, kube: kube, engine: eng, h: newMux(s)}
}

func (e *testEnv) do(method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	e.h.ServeHTTP(rec, req)
	return rec
}

func (e *testEnv) databaseUsers() []v1alpha1.DatabaseUser {
	var list v1alpha1.DatabaseUserList
	if err := e.kube.List(context.Background(), &list); err != nil {
		e.t.Fatal(err)
	}
	return list.Items
}

func (e *testEnv) secret(name string) *corev1.Secret {
	s := &corev1.Secret{}
	if err := e.kube.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: name}, s); err != nil {
		e.t.Fatalf("get secret %s: %v", name, err)
	}
	return s
}

func declared(name, username, instance string) *v1alpha1.DatabaseUser {
	return &v1alpha1.DatabaseUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec: v1alpha1.DatabaseUserSpec{
			InstanceRef: v1alpha1.InstanceReference{Name: instance},
			Username:    username,
			Roles:       []v1alpha1.RoleGrant{{Role: "read", Database: "shop"}},
		},
		Status: v1alpha1.DatabaseUserStatus{Phase: v1alpha1.PhaseReady},
	}
}

func credentialsSecret(name, pwd string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Data:       map[string][]byte{"username": []byte("app"), "password": []byte(pwd), "uri": []byte("mongodb://old")},
	}
}

func TestListMergesDeclaredAndDatabaseUsers(t *testing.T) {
	env := newTestEnv(t, "percona-server-mongodb", http.StatusOK, http.StatusForbidden,
		declared("mongo-app-1", "app", "mongo"),
		declared("other-x-1", "x", "other"),
	)
	env.engine.dbUsers = []engine.User{
		{Username: "app", Database: "admin", Roles: []engine.RoleGrant{{Role: "read", Database: "shop"}}},
		{Username: "userAdmin", Database: "admin", System: true},
		{Username: "legacy", Database: "shop"},
	}
	rec := env.do(http.MethodGet, "/api/users"+testQuery, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var resp usersResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	type row struct {
		id, username string
		managed      bool
		system       bool
		phase        string
	}
	var got []row
	for _, u := range resp.Users {
		got = append(got, row{u.ID, u.Username, u.Managed, u.System, u.Phase})
	}
	want := []row{
		{"mongo-app-1", "app", true, false, "Ready"},
		{"", "legacy", false, false, ""},
		{"", "userAdmin", false, true, ""},
	}
	if len(got) != len(want) {
		t.Fatalf("users = %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("user %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	if resp.CanManage || len(resp.Roles) != 2 {
		t.Errorf("response = %+v", resp)
	}
	if strings.Contains(rec.Body.String(), "admin-secret") {
		t.Error("instance credentials leaked")
	}
}

func TestListWhenDatabaseUnreachable(t *testing.T) {
	env := newTestEnv(t, "percona-server-mongodb", http.StatusOK, http.StatusOK, declared("mongo-app-1", "app", "mongo"))
	env.engine.listErr = engine.ErrNotReady
	var resp usersResponse
	rec := env.do(http.MethodGet, "/api/users"+testQuery, "")
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if rec.Code != http.StatusOK || len(resp.Users) != 1 || resp.DatabaseError == "" {
		t.Fatalf("status = %d, response = %+v", rec.Code, resp)
	}
}

func TestCreateUserDeclaresDatabaseUser(t *testing.T) {
	env := newTestEnv(t, "percona-server-mongodb", http.StatusOK, http.StatusOK)
	rec := env.do(http.MethodPost, "/api/users"+testQuery, `{"username":"Shop_App@x","roles":[{"role":"read","database":"shop"},{"role":"readAnyDatabase","database":"x"}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var creds credentialsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &creds); err != nil {
		t.Fatal(err)
	}

	users := env.databaseUsers()
	if len(users) != 1 {
		t.Fatalf("DatabaseUsers = %+v", users)
	}
	u := users[0]
	if !strings.HasPrefix(u.Name, "mongo-shop-app-x-") || u.Spec.Username != "Shop_App@x" || u.Spec.InstanceRef.Name != "mongo" || u.Labels[controller.InstanceLabel] != "mongo" {
		t.Errorf("DatabaseUser = %+v", u)
	}
	wantRoles := []v1alpha1.RoleGrant{{Role: "read", Database: "shop"}, {Role: "readAnyDatabase"}}
	if len(u.Spec.Roles) != 2 || u.Spec.Roles[0] != wantRoles[0] || u.Spec.Roles[1] != wantRoles[1] {
		t.Errorf("roles = %+v", u.Spec.Roles)
	}

	secret := env.secret(u.Name + "-credentials")
	if creds.Password == "" || string(secret.Data["password"]) != creds.Password || creds.Secret != secret.Name || creds.ID != u.Name {
		t.Errorf("creds = %+v, secret = %v", creds, secret.Data)
	}
	if creds.URI != "mongodb://Shop_App@x:"+creds.Password+"@host/" {
		t.Errorf("uri = %s", creds.URI)
	}
}

func TestCreateUserRejects(t *testing.T) {
	cases := map[string]struct {
		body string
		want int
	}{
		"bad username":        {`{"username":"-x","roles":[{"role":"read","database":"a"}]}`, http.StatusBadRequest},
		"short password":      {`{"username":"x","password":"short","roles":[{"role":"read","database":"a"}]}`, http.StatusBadRequest},
		"ungrantable role":    {`{"username":"x","roles":[{"role":"root"}]}`, http.StatusBadRequest},
		"already declared":    {`{"username":"app","roles":[{"role":"read","database":"a"}]}`, http.StatusConflict},
		"exists in database":  {`{"username":"boss","roles":[{"role":"read","database":"a"}]}`, http.StatusConflict},
		"unknown body fields": {`{"username":"x","roles":[],"extra":1}`, http.StatusBadRequest},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t, "percona-server-mongodb", http.StatusOK, http.StatusOK, declared("mongo-app-1", "app", "mongo"))
			env.engine.dbUsers = []engine.User{{Username: "boss", Database: "admin"}}
			if rec := env.do(http.MethodPost, "/api/users"+testQuery, tc.body); rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if n := len(env.databaseUsers()); n != 1 {
				t.Errorf("DatabaseUsers = %d", n)
			}
		})
	}
}

func TestUpdateRoles(t *testing.T) {
	env := newTestEnv(t, "percona-server-mongodb", http.StatusOK, http.StatusOK, declared("mongo-app-1", "app", "mongo"), declared("other-x-1", "x", "other"))
	if rec := env.do(http.MethodPut, "/api/users/mongo-app-1/roles"+testQuery, `{"roles":[{"role":"readAnyDatabase","database":"x"}]}`); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	for _, u := range env.databaseUsers() {
		if u.Name == "mongo-app-1" && (len(u.Spec.Roles) != 1 || u.Spec.Roles[0] != (v1alpha1.RoleGrant{Role: "readAnyDatabase"})) {
			t.Errorf("roles = %+v", u.Spec.Roles)
		}
	}
	if rec := env.do(http.MethodPut, "/api/users/mongo-app-1/roles"+testQuery, `{"roles":[{"role":"root"}]}`); rec.Code != http.StatusBadRequest {
		t.Errorf("ungrantable role: status = %d", rec.Code)
	}
	// A user of another instance isn't reachable through this one.
	if rec := env.do(http.MethodPut, "/api/users/other-x-1/roles"+testQuery, `{"roles":[{"role":"read","database":"a"}]}`); rec.Code != http.StatusNotFound {
		t.Errorf("other instance: status = %d", rec.Code)
	}
}

func TestCredentialsAndPasswordReset(t *testing.T) {
	own := declared("mongo-own-1", "own", "mongo")
	own.Spec.PasswordSecretRef = &v1alpha1.SecretKeySelector{Name: "my-secret", Key: "pw"}
	env := newTestEnv(t, "percona-server-mongodb", http.StatusOK, http.StatusOK,
		declared("mongo-app-1", "app", "mongo"), credentialsSecret("mongo-app-1-credentials", "old-password"), own)

	rec := env.do(http.MethodGet, "/api/users/mongo-app-1/credentials"+testQuery, "")
	var creds credentialsResponse
	_ = json.Unmarshal(rec.Body.Bytes(), &creds)
	if rec.Code != http.StatusOK || creds.Password != "old-password" || creds.Secret != "mongo-app-1-credentials" {
		t.Fatalf("status = %d, creds = %+v", rec.Code, creds)
	}

	rec = env.do(http.MethodPost, "/api/users/mongo-app-1/password"+testQuery, `{}`)
	_ = json.Unmarshal(rec.Body.Bytes(), &creds)
	secret := env.secret("mongo-app-1-credentials")
	if rec.Code != http.StatusOK || creds.Password == "old-password" || string(secret.Data["password"]) != creds.Password {
		t.Fatalf("status = %d, creds = %+v, secret = %v", rec.Code, creds, secret.Data)
	}
	if string(secret.Data["uri"]) != "mongodb://app:"+creds.Password+"@host/" {
		t.Errorf("uri not refreshed: %s", secret.Data["uri"])
	}

	if rec := env.do(http.MethodGet, "/api/users/mongo-own-1/credentials"+testQuery, ""); rec.Code != http.StatusConflict {
		t.Errorf("credentials with own Secret: status = %d", rec.Code)
	}
	if rec := env.do(http.MethodPost, "/api/users/mongo-own-1/password"+testQuery, `{}`); rec.Code != http.StatusConflict {
		t.Errorf("reset with own Secret: status = %d", rec.Code)
	}
}

func TestDeleteUser(t *testing.T) {
	env := newTestEnv(t, "percona-server-mongodb", http.StatusOK, http.StatusOK, declared("mongo-app-1", "app", "mongo"))
	if rec := env.do(http.MethodDelete, "/api/users/mongo-app-1"+testQuery, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if n := len(env.databaseUsers()); n != 0 {
		t.Errorf("DatabaseUsers left: %d", n)
	}
	if rec := env.do(http.MethodDelete, "/api/users/mongo-app-1"+testQuery, ""); rec.Code != http.StatusNotFound {
		t.Errorf("second delete: status = %d", rec.Code)
	}
}

func TestAuthorization(t *testing.T) {
	cases := map[string]struct {
		provider         string
		instanceStatus   int
		connectionStatus int
		method, path     string
		body             string
		token            string
		want             int
	}{
		"no token":              {"percona-server-mongodb", 200, 200, "GET", "/api/users" + testQuery, "", "", 401},
		"bad namespace":         {"percona-server-mongodb", 200, 200, "GET", "/api/users?namespace=NS&instance=mongo", "", "token", 400},
		"cannot read instance":  {"percona-server-mongodb", 403, 200, "GET", "/api/users" + testQuery, "", "token", 403},
		"unsupported provider":  {"mssql", 200, 200, "GET", "/api/users" + testQuery, "", "token", 404},
		"create read only":      {"percona-server-mongodb", 200, 403, "POST", "/api/users" + testQuery, `{"username":"a","roles":[{"role":"read","database":"a"}]}`, "token", 403},
		"update read only":      {"percona-server-mongodb", 200, 403, "PUT", "/api/users/mongo-app-1/roles" + testQuery, `{"roles":[{"role":"read","database":"a"}]}`, "token", 403},
		"credentials read only": {"percona-server-mongodb", 200, 403, "GET", "/api/users/mongo-app-1/credentials" + testQuery, "", "token", 403},
		"reset read only":       {"percona-server-mongodb", 200, 403, "POST", "/api/users/mongo-app-1/password" + testQuery, `{}`, "token", 403},
		"delete read only":      {"percona-server-mongodb", 200, 403, "DELETE", "/api/users/mongo-app-1" + testQuery, "", "token", 403},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			env := newTestEnv(t, tc.provider, tc.instanceStatus, tc.connectionStatus,
				declared("mongo-app-1", "app", "mongo"), credentialsSecret("mongo-app-1-credentials", "old-password"))
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			env.h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if strings.Contains(rec.Body.String(), "old-password") {
				t.Error("credentials leaked")
			}
			users := env.databaseUsers()
			if len(users) != 1 || users[0].Spec.Roles[0].Database != "shop" {
				t.Error("DatabaseUsers changed despite failed authorization")
			}
			if string(env.secret("mongo-app-1-credentials").Data["password"]) != "old-password" {
				t.Error("password changed despite failed authorization")
			}
		})
	}
}

func TestUserResourceName(t *testing.T) {
	a := userResourceName("mongo", "App_User@corp")
	b := userResourceName("mongo", "app-user-corp")
	if a == b {
		t.Error("usernames mapping to the same stem must not collide")
	}
	if !strings.HasPrefix(a, "mongo-app-user-corp-") {
		t.Errorf("name = %s", a)
	}
	long := userResourceName(strings.Repeat("i", 63), strings.Repeat("u", 64)+"@"+strings.Repeat("v", 200))
	if len(long)+len("-credentials") > 253 {
		t.Errorf("name too long: %d", len(long))
	}
}
