package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

const testQuery = "?namespace=ns&instance=mongo"

type fakeEngine struct {
	created   []engine.NewUser
	updated   map[string][]engine.RoleGrant
	passwords map[string]string
	deleted   []string
	err       error
}

func (f *fakeEngine) Roles() []engine.Role {
	return []engine.Role{{Name: "read", Scope: engine.ScopeDatabase}}
}

func (f *fakeEngine) ListUsers(context.Context, engine.Target) ([]engine.User, error) {
	return []engine.User{{ID: "admin.app", Username: "app", Manageable: true}}, f.err
}

func (f *fakeEngine) CreateUser(_ context.Context, _ engine.Target, u engine.NewUser) (engine.Credentials, error) {
	if f.err != nil {
		return engine.Credentials{}, f.err
	}
	f.created = append(f.created, u)
	return engine.Credentials{Username: u.Username, Password: u.Password, URI: "mongodb://x"}, nil
}

func (f *fakeEngine) UpdateRoles(_ context.Context, _ engine.Target, id string, roles []engine.RoleGrant) error {
	if f.err != nil {
		return f.err
	}
	f.updated[id] = roles
	return nil
}

func (f *fakeEngine) ResetPassword(_ context.Context, _ engine.Target, id, pwd string) (engine.Credentials, error) {
	if f.err != nil {
		return engine.Credentials{}, f.err
	}
	f.passwords[id] = pwd
	return engine.Credentials{Username: id, Password: pwd}, nil
}

func (f *fakeEngine) DeleteUser(_ context.Context, _ engine.Target, id string) error {
	if f.err != nil {
		return f.err
	}
	f.deleted = append(f.deleted, id)
	return nil
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

func newTestServer(t *testing.T, provider string, instanceStatus, connectionStatus int) (*fakeEngine, http.Handler) {
	eng := &fakeEngine{updated: map[string][]engine.RoleGrant{}, passwords: map[string]string{}}
	everest := fakeEverest(t, provider, instanceStatus, connectionStatus)
	s := &server{
		everest: newEverestClient(everest.URL),
		engines: map[string]engine.Engine{"percona-server-mongodb": eng},
	}
	return eng, newMux(s)
}

func do(h http.Handler, method, target, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestListUsers(t *testing.T) {
	for name, tc := range map[string]struct {
		connectionStatus int
		wantCanManage    bool
	}{
		"can manage":         {http.StatusOK, true},
		"read only":          {http.StatusForbidden, false},
		"instance not ready": {http.StatusNotFound, false},
	} {
		t.Run(name, func(t *testing.T) {
			_, h := newTestServer(t, "percona-server-mongodb", http.StatusOK, tc.connectionStatus)
			rec := do(h, http.MethodGet, "/api/users"+testQuery, "")
			if rec.Code != http.StatusOK {
				t.Fatalf("status = %d: %s", rec.Code, rec.Body)
			}
			var resp usersResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.CanManage != tc.wantCanManage || len(resp.Users) != 1 || len(resp.Roles) != 1 {
				t.Errorf("response = %+v", resp)
			}
			if strings.Contains(rec.Body.String(), "admin-secret") {
				t.Error("instance credentials leaked")
			}
		})
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
		"no token":             {"percona-server-mongodb", 200, 200, "GET", "/api/users" + testQuery, "", "", 401},
		"bad namespace":        {"percona-server-mongodb", 200, 200, "GET", "/api/users?namespace=NS&instance=mongo", "", "token", 400},
		"cannot read instance": {"percona-server-mongodb", 403, 200, "GET", "/api/users" + testQuery, "", "token", 403},
		"unsupported provider": {"mssql", 200, 200, "GET", "/api/users" + testQuery, "", "token", 404},
		"create read only":     {"percona-server-mongodb", 200, 403, "POST", "/api/users" + testQuery, `{"username":"a","roles":[]}`, "token", 403},
		"update read only":     {"percona-server-mongodb", 200, 403, "PUT", "/api/users/admin.a/roles" + testQuery, `{"roles":[]}`, "token", 403},
		"reset read only":      {"percona-server-mongodb", 200, 403, "POST", "/api/users/admin.a/password" + testQuery, `{}`, "token", 403},
		"delete read only":     {"percona-server-mongodb", 200, 403, "DELETE", "/api/users/admin.a" + testQuery, "", "token", 403},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			eng, h := newTestServer(t, tc.provider, tc.instanceStatus, tc.connectionStatus)
			req := httptest.NewRequest(tc.method, tc.path, strings.NewReader(tc.body))
			if tc.token != "" {
				req.Header.Set("Authorization", "Bearer "+tc.token)
			}
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", rec.Code, tc.want, rec.Body)
			}
			if len(eng.created)+len(eng.updated)+len(eng.passwords)+len(eng.deleted) != 0 {
				t.Error("engine called despite failed authorization")
			}
		})
	}
}

func TestCreateUserGeneratesPassword(t *testing.T) {
	eng, h := newTestServer(t, "percona-server-mongodb", http.StatusOK, http.StatusOK)
	rec := do(h, http.MethodPost, "/api/users"+testQuery, `{"username":"app","roles":[{"role":"read","database":"shop"}]}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	if rec.Header().Get("Cache-Control") != "no-store" {
		t.Error("credentials response is cacheable")
	}
	var creds engine.Credentials
	if err := json.Unmarshal(rec.Body.Bytes(), &creds); err != nil {
		t.Fatal(err)
	}
	if len(eng.created) != 1 || creds.Password == "" || eng.created[0].Password != creds.Password {
		t.Errorf("created = %+v, creds = %+v", eng.created, creds)
	}
}

func TestCreateUserRejectsWeakPassword(t *testing.T) {
	eng, h := newTestServer(t, "percona-server-mongodb", http.StatusOK, http.StatusOK)
	rec := do(h, http.MethodPost, "/api/users"+testQuery, `{"username":"app","password":"short","roles":[]}`)
	if rec.Code != http.StatusBadRequest || len(eng.created) != 0 {
		t.Fatalf("status = %d, created = %v", rec.Code, eng.created)
	}
}

func TestUserMutations(t *testing.T) {
	eng, h := newTestServer(t, "percona-server-mongodb", http.StatusOK, http.StatusOK)

	if rec := do(h, http.MethodPut, "/api/users/admin.a%40b/roles"+testQuery, `{"roles":[{"role":"read","database":"x"}]}`); rec.Code != http.StatusNoContent {
		t.Fatalf("update status = %d: %s", rec.Code, rec.Body)
	}
	if got := eng.updated["admin.a@b"]; len(got) != 1 || got[0].Database != "x" {
		t.Errorf("updated = %+v", eng.updated)
	}

	if rec := do(h, http.MethodPost, "/api/users/admin.a/password"+testQuery, `{"password":"chosen-password"}`); rec.Code != http.StatusOK {
		t.Fatalf("reset status = %d: %s", rec.Code, rec.Body)
	}
	if eng.passwords["admin.a"] != "chosen-password" {
		t.Errorf("passwords = %+v", eng.passwords)
	}

	if rec := do(h, http.MethodDelete, "/api/users/admin.a"+testQuery, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete status = %d: %s", rec.Code, rec.Body)
	}
	if len(eng.deleted) != 1 || eng.deleted[0] != "admin.a" {
		t.Errorf("deleted = %+v", eng.deleted)
	}
}

func TestEngineErrors(t *testing.T) {
	for err, want := range map[error]int{
		engine.ErrInvalid:        http.StatusBadRequest,
		engine.ErrNotFound:       http.StatusNotFound,
		engine.ErrConflict:       http.StatusConflict,
		engine.ErrNotManageable:  http.StatusForbidden,
		engine.ErrNotReady:       http.StatusConflict,
		context.DeadlineExceeded: http.StatusBadGateway,
	} {
		eng, h := newTestServer(t, "percona-server-mongodb", http.StatusOK, http.StatusOK)
		eng.err = err
		if rec := do(h, http.MethodDelete, "/api/users/admin.a"+testQuery, ""); rec.Code != want {
			t.Errorf("%v: status = %d, want %d", err, rec.Code, want)
		}
	}
}
