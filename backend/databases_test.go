package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openeverest/plugin-user-management/backend/api/v1alpha1"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

const dbProvider = "provider-cloudnative-pg"

// fakeDBEngine is a PostgreSQL-like engine: it manages databases and its
// users may have no roles.
type fakeDBEngine struct {
	*fakeEngine
	dbs        []engine.Database
	extensions []engine.PostgreSQLExtension
}

func (f *fakeDBEngine) AvailableExtensions(context.Context, engine.Target) ([]engine.PostgreSQLExtension, error) {
	return f.extensions, nil
}

func (f *fakeDBEngine) NormalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	if len(grants) == 0 {
		return []engine.RoleGrant{}, nil
	}
	return f.fakeEngine.NormalizeRoles(grants)
}

func (f *fakeDBEngine) ValidateDatabase(name string) error {
	if name == "postgres" {
		return errors.Join(engine.ErrInvalidDatabase, errors.New(`database "postgres" is reserved`))
	}
	return nil
}

func (f *fakeDBEngine) ListDatabases(context.Context, engine.Target) ([]engine.Database, error) {
	return f.dbs, nil
}

func (f *fakeDBEngine) SyncDatabase(context.Context, engine.Target, engine.DesiredDatabase) error {
	return nil
}

func (f *fakeDBEngine) ReleaseDatabase(context.Context, engine.Target, string, bool) error {
	return nil
}

func instanceDB(name, database, owner string) *v1alpha1.InstanceDatabase {
	return &v1alpha1.InstanceDatabase{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Spec: v1alpha1.InstanceDatabaseSpec{
			InstanceRef: v1alpha1.InstanceReference{Name: "mongo"},
			Name:        database,
			Owner:       v1alpha1.DatabaseOwner{InstanceUserRef: v1alpha1.InstanceUserReference{Name: owner}},
		},
		Status: v1alpha1.InstanceDatabaseStatus{Phase: v1alpha1.PhaseReady},
	}
}

func (e *testEnv) instanceDatabase(name string) *v1alpha1.InstanceDatabase {
	db := &v1alpha1.InstanceDatabase{}
	if err := e.kube.Get(context.Background(), types.NamespacedName{Namespace: "ns", Name: name}, db); err != nil {
		e.t.Fatalf("get InstanceDatabase %s: %v", name, err)
	}
	return db
}

func setDatabases(env *testEnv, dbs []engine.Database) {
	env.dbEngine.dbs = dbs
}

func TestListDatabases(t *testing.T) {
	env := newTestEnv(t, dbProvider, http.StatusOK, http.StatusOK,
		declared("mongo-owner-1", "orders_owner", "mongo"),
		instanceDB("mongo-orders-1", "orders", "mongo-owner-1"))
	setDatabases(env, []engine.Database{{Name: "orders", Owner: "orders_owner"}, {Name: "legacy", Owner: "dba"}, {Name: "postgres", Owner: "postgres", System: true}})

	rec := env.do("GET", "/api/databases"+testQuery, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var resp databasesResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.Supported || !resp.CanManage || len(resp.Databases) != 3 {
		t.Fatalf("response = %+v", resp)
	}
	orders, legacy, postgres := resp.Databases[0], resp.Databases[1], resp.Databases[2]
	if orders.ID != "mongo-orders-1" || orders.Name != "orders" || orders.Owner != "orders_owner" || orders.OwnerID != "mongo-owner-1" ||
		!orders.Managed || orders.Connect != "Public" || orders.DeletionPolicy != "Retain" || orders.Phase != "Ready" {
		t.Errorf("orders = %+v", orders)
	}
	if legacy.Managed || legacy.System || legacy.Owner != "dba" {
		t.Errorf("legacy = %+v", legacy)
	}
	if !postgres.System {
		t.Errorf("postgres = %+v", postgres)
	}

	// Engines without databases report them unsupported, not an error.
	rec = newTestEnv(t, "percona-server-mongodb", http.StatusOK, http.StatusOK).do("GET", "/api/databases"+testQuery, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"supported":false`) {
		t.Fatalf("unsupported: %d %s", rec.Code, rec.Body)
	}
}

func TestCreateDatabase(t *testing.T) {
	env := newTestEnv(t, dbProvider, http.StatusOK, http.StatusOK,
		declared("mongo-owner-1", "orders_owner", "mongo"),
		declared("other-owner-1", "x", "other"),
		instanceDB("mongo-billing-1", "billing", "mongo-owner-1"))
	setDatabases(env, []engine.Database{{Name: "legacy"}})

	rec := env.do("POST", "/api/databases"+testQuery, `{"name":"orders","ownerId":"mongo-owner-1","connect":"Granted"}`)
	if rec.Code != http.StatusCreated {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body)
	}
	var view databaseView
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	db := env.instanceDatabase(view.ID)
	if db.Spec.Name != "orders" || db.Spec.Owner.InstanceUserRef.Name != "mongo-owner-1" || db.Spec.Connect != "Granted" ||
		db.Spec.DeletionPolicy != "Retain" || db.Spec.InstanceRef.Name != "mongo" || db.Labels["plugin-user-management.plugins.openeverest.io/instance"] != "mongo" {
		t.Fatalf("InstanceDatabase = %+v", db)
	}
	if view.Owner != "orders_owner" || view.Phase != "Pending" {
		t.Fatalf("view = %+v", view)
	}

	for name, tc := range map[string]struct {
		body string
		code int
	}{
		"invalid name":           {`{"name":"Orders-DB","ownerId":"mongo-owner-1"}`, http.StatusBadRequest},
		"reserved":               {`{"name":"postgres","ownerId":"mongo-owner-1"}`, http.StatusBadRequest},
		"no owner":               {`{"name":"x"}`, http.StatusBadRequest},
		"unknown owner":          {`{"name":"x","ownerId":"nobody"}`, http.StatusBadRequest},
		"owner of another":       {`{"name":"x","ownerId":"other-owner-1"}`, http.StatusBadRequest},
		"bad connect":            {`{"name":"x","ownerId":"mongo-owner-1","connect":"Nobody"}`, http.StatusBadRequest},
		"already declared":       {`{"name":"billing","ownerId":"mongo-owner-1"}`, http.StatusConflict},
		"exists in the instance": {`{"name":"legacy","ownerId":"mongo-owner-1"}`, http.StatusConflict},
	} {
		if rec := env.do("POST", "/api/databases"+testQuery, tc.body); rec.Code != tc.code {
			t.Errorf("%s: status = %d, want %d: %s", name, rec.Code, tc.code, rec.Body)
		}
	}

	rec = newTestEnv(t, "percona-server-mongodb", http.StatusOK, http.StatusOK, declared("mongo-owner-1", "o", "mongo")).
		do("POST", "/api/databases"+testQuery, `{"name":"orders","ownerId":"mongo-owner-1"}`)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("unsupported engine: status = %d", rec.Code)
	}
}

func TestUpdateAndDeleteDatabase(t *testing.T) {
	env := newTestEnv(t, dbProvider, http.StatusOK, http.StatusOK,
		declared("mongo-owner-1", "a", "mongo"), declared("mongo-owner-2", "b", "mongo"),
		instanceDB("mongo-orders-1", "orders", "mongo-owner-1"), instanceDB("mongo-billing-1", "billing", "mongo-owner-1"))

	if rec := env.do("PUT", "/api/databases/mongo-orders-1"+testQuery, `{"ownerId":"mongo-owner-2","connect":"Granted"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if db := env.instanceDatabase("mongo-orders-1"); db.Spec.Owner.InstanceUserRef.Name != "mongo-owner-2" || db.Spec.Connect != "Granted" {
		t.Fatalf("updated = %+v", db.Spec)
	}

	// Deleting keeps the data unless asked to drop it. The fake client has no
	// finalizers, so the policy is checked through the patch before deletion.
	env.instanceDatabase("mongo-billing-1")
	if rec := env.do("DELETE", "/api/databases/mongo-billing-1"+testQuery+"&drop=true", ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	if rec := env.do("DELETE", "/api/databases/mongo-orders-1"+testQuery, ""); rec.Code != http.StatusNoContent {
		t.Fatalf("delete: %d %s", rec.Code, rec.Body)
	}
	var list v1alpha1.InstanceDatabaseList
	_ = env.kube.List(context.Background(), &list)
	if len(list.Items) != 0 {
		t.Fatalf("left: %+v", list.Items)
	}
	if rec := env.do("DELETE", "/api/databases/missing"+testQuery, ""); rec.Code != http.StatusNotFound {
		t.Fatalf("missing: %d", rec.Code)
	}
}

func TestDeleteDatabaseSetsPolicy(t *testing.T) {
	for _, tc := range []struct {
		query, policy string
	}{{"&drop=true", "Delete"}, {"", "Retain"}} {
		db := instanceDB("mongo-orders-1", "orders", "mongo-owner-1")
		db.Finalizers = []string{"plugin-user-management.plugins.openeverest.io/release-database"}
		db.Spec.DeletionPolicy = "Delete"
		if tc.policy == "Delete" {
			db.Spec.DeletionPolicy = "Retain"
		}
		env := newTestEnv(t, dbProvider, http.StatusOK, http.StatusOK, declared("mongo-owner-1", "a", "mongo"), db)
		if rec := env.do("DELETE", "/api/databases/mongo-orders-1"+testQuery+tc.query, ""); rec.Code != http.StatusNoContent {
			t.Fatalf("delete: %d %s", rec.Code, rec.Body)
		}
		got := env.instanceDatabase("mongo-orders-1")
		if got.Spec.DeletionPolicy != tc.policy || got.DeletionTimestamp.IsZero() {
			t.Errorf("query %q: policy = %q, deleting = %v", tc.query, got.Spec.DeletionPolicy, !got.DeletionTimestamp.IsZero())
		}
	}
}

func TestDatabasesAuthorization(t *testing.T) {
	env := newTestEnv(t, dbProvider, http.StatusOK, http.StatusForbidden, declared("mongo-owner-1", "a", "mongo"), instanceDB("mongo-orders-1", "orders", "mongo-owner-1"))
	if rec := env.do("GET", "/api/databases"+testQuery, ""); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"canManage":false`) {
		t.Fatalf("list read only: %d %s", rec.Code, rec.Body)
	}
	for _, req := range []struct{ method, path, body string }{
		{"POST", "/api/databases" + testQuery, `{"name":"x","ownerId":"mongo-owner-1"}`},
		{"PUT", "/api/databases/mongo-orders-1" + testQuery, `{"ownerId":"mongo-owner-1"}`},
		{"DELETE", "/api/databases/mongo-orders-1" + testQuery, ""},
	} {
		if rec := env.do(req.method, req.path, req.body); rec.Code != http.StatusForbidden {
			t.Errorf("%s %s: %d", req.method, req.path, rec.Code)
		}
	}
}

func TestUsersResponseCapabilities(t *testing.T) {
	for provider, want := range map[string]string{
		dbProvider:               `"rolesRequired":false,"databasesSupported":true`,
		"percona-server-mongodb": `"rolesRequired":true,"databasesSupported":false`,
	} {
		rec := newTestEnv(t, provider, http.StatusOK, http.StatusOK).do("GET", "/api/users"+testQuery, "")
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("%s: %s", provider, rec.Body)
		}
	}
	// A PostgreSQL user can be created without roles, e.g. a database's owner.
	env := newTestEnv(t, dbProvider, http.StatusOK, http.StatusOK)
	if rec := env.do("POST", "/api/users"+testQuery, `{"username":"orders_owner","roles":[]}`); rec.Code != http.StatusCreated {
		t.Fatalf("create without roles: %d %s", rec.Code, rec.Body)
	}
}

func TestHiddenUsersNotListed(t *testing.T) {
	env := newTestEnv(t, dbProvider, http.StatusOK, http.StatusOK)
	env.engine.dbUsers = []engine.User{{Username: "oe_orders_read", System: true, Hidden: true}, {Username: "postgres", System: true}}
	body := env.do("GET", "/api/users"+testQuery, "").Body.String()
	if strings.Contains(body, "oe_orders_read") || !strings.Contains(body, `"username":"postgres"`) {
		t.Fatalf("users = %s", body)
	}
}

func TestDatabaseExtensionsAPI(t *testing.T) {
	env := newTestEnv(t, dbProvider, http.StatusOK, http.StatusOK,
		declared("mongo-owner-1", "o", "mongo"),
		instanceDB("mongo-billing-1", "billing", "mongo-owner-1"))
	env.dbEngine.extensions = []engine.PostgreSQLExtension{{Name: "pg_trgm", Trusted: true}, {Name: "vector", Description: "vector type"}}

	// The list offers the extensions and shows each database's.
	body := env.do("GET", "/api/databases"+testQuery, "").Body.String()
	if !strings.Contains(body, `"postgresql":{"extensions":[{"name":"pg_trgm","trusted":true},{"name":"vector","description":"vector type","trusted":false}]}`) {
		t.Fatalf("list = %s", body)
	}

	rec := env.do("POST", "/api/databases"+testQuery, `{"name":"orders","ownerId":"mongo-owner-1","postgresql":{"extensions":["vector","pg_trgm","vector"]}}`)
	if rec.Code != http.StatusCreated || !strings.Contains(rec.Body.String(), `"postgresql":{"extensions":["vector","pg_trgm"]}`) {
		t.Fatalf("create: %d %s", rec.Code, rec.Body)
	}
	var view databaseView
	_ = json.Unmarshal(rec.Body.Bytes(), &view)
	if db := env.instanceDatabase(view.ID); len(db.Spec.PostgreSQL.Extensions) != 2 || db.Spec.PostgreSQL.Extensions[0].Name != "vector" {
		t.Fatalf("spec = %+v", db.Spec.PostgreSQL)
	}

	for name, body := range map[string]string{
		"not offered":  `{"name":"x","ownerId":"mongo-owner-1","postgresql":{"extensions":["file_fdw"]}}`,
		"invalid name": `{"name":"x","ownerId":"mongo-owner-1","postgresql":{"extensions":["Bad Name"]}}`,
	} {
		if rec := env.do("POST", "/api/databases"+testQuery, body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s: %d %s", name, rec.Code, rec.Body)
		}
	}

	// Updating without the block keeps the extensions; with it replaces them.
	if rec := env.do("PUT", "/api/databases/"+view.ID+testQuery, `{"ownerId":"mongo-owner-1"}`); rec.Code != http.StatusNoContent {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if db := env.instanceDatabase(view.ID); db.Spec.PostgreSQL == nil || len(db.Spec.PostgreSQL.Extensions) != 2 {
		t.Fatalf("kept = %+v", db.Spec.PostgreSQL)
	}
	if rec := env.do("PUT", "/api/databases/"+view.ID+testQuery, `{"ownerId":"mongo-owner-1","postgresql":{"extensions":[]}}`); rec.Code != http.StatusNoContent {
		t.Fatalf("update: %d %s", rec.Code, rec.Body)
	}
	if db := env.instanceDatabase(view.ID); db.Spec.PostgreSQL != nil {
		t.Fatalf("cleared = %+v", db.Spec.PostgreSQL)
	}
}
