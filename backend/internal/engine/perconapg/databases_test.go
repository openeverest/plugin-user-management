package perconapg

import (
	"context"
	"errors"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/pgaccess"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/pgext"
)

func TestValidateDatabase(t *testing.T) {
	e := &Engine{}
	if err := e.ValidateDatabase("orders"); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", "postgres", "template0", "template1", strings.Repeat("a", 64)} {
		if err := e.ValidateDatabase(name); !errors.Is(err, engine.ErrInvalid) || !strings.HasPrefix(err.Error(), "invalid database: ") {
			t.Errorf("%q: err = %v, want an invalid database error", name, err)
		}
	}
}

func TestListDatabases(t *testing.T) {
	spec := providerSpec()
	spec["users"] = append(spec["users"].([]any), map[string]any{"name": "app", "databases": []any{"appdb", "later"}})
	s := &fakeSession{dbs: []dbDatabase{
		{name: "appdb", owner: "postgres"},
		{name: "orders", owner: "orders_owner", comment: managedComment},
		{name: "postgres", owner: "postgres"},
		{name: "template1", owner: "postgres", template: true},
	}}
	got, err := newTestEngine(s, pgCluster(spec, "ready")).ListDatabases(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(got, func(i, j int) bool { return got[i].Name < got[j].Name })
	want := []engine.Database{
		{Name: "appdb", Owner: "postgres", System: true},
		{Name: "later", System: true},
		{Name: "orders", Owner: "orders_owner"},
		{Name: "postgres", Owner: "postgres", System: true},
		{Name: "template0", System: true},
		{Name: "template1", Owner: "postgres", System: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("databases =\n%+v\nwant\n%+v", got, want)
	}
}

func TestPlanDatabase(t *testing.T) {
	want := engine.DesiredDatabase{Name: "orders", Owner: "orders_owner"}
	for name, tc := range map[string]struct {
		current *dbDatabase
		stmts   []string
	}{
		"missing": {nil, []string{
			`CREATE DATABASE "orders" OWNER "orders_owner"`,
			`COMMENT ON DATABASE "orders" IS 'Managed by OpenEverest'`,
		}},
		"in sync": {&dbDatabase{name: "orders", owner: "orders_owner", comment: managedComment}, nil},
		"owner changed": {&dbDatabase{name: "orders", owner: "old", comment: managedComment}, []string{
			`ALTER DATABASE "orders" OWNER TO "orders_owner"`,
		}},
		"interrupted creation": {&dbDatabase{name: "orders", owner: "orders_owner"}, []string{
			`COMMENT ON DATABASE "orders" IS 'Managed by OpenEverest'`,
		}},
	} {
		if got := planDatabase(want, tc.current); !reflect.DeepEqual(got, tc.stmts) {
			t.Errorf("%s: statements = %q, want %q", name, got, tc.stmts)
		}
	}
}

func TestSyncDatabase(t *testing.T) {
	ctx := context.Background()
	s := &fakeSession{objects: []pgaccess.Object{{Name: "public.sales"}, {Name: "public.sales_id_seq", Sequence: true}}}
	spec := providerSpec()
	spec["users"] = append(spec["users"].([]any), map[string]any{"name": "app", "databases": []any{"appdb"}})
	e := newTestEngine(s, pgCluster(spec, "ready"))

	if err := e.SyncDatabase(ctx, target, engine.DesiredDatabase{Name: "orders", Owner: "o"}); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{`CREATE DATABASE "orders" OWNER "o"`, `COMMENT ON DATABASE "orders" IS 'Managed by OpenEverest'`},
		{`CREATE ROLE "oe_orders_read" WITH NOLOGIN`, `COMMENT ON ROLE "oe_orders_read" IS 'Managed by OpenEverest'`},
		{`CREATE ROLE "oe_orders_readwrite" WITH NOLOGIN`, `COMMENT ON ROLE "oe_orders_readwrite" IS 'Managed by OpenEverest'`},
		pgaccess.PrivilegeStatements("orders", "o", nil, s.objects, pgaccess.Connect{}),
	}
	if !reflect.DeepEqual(s.executed, want) {
		t.Fatalf("executed =\n%q\nwant\n%q", s.executed, want)
	}
	// Access is granted in the database itself.
	if !reflect.DeepEqual(s.dialed, []string{"postgres", "orders"}) {
		t.Fatalf("dialed = %v", s.dialed)
	}

	// Existing groups not created by the plugin are never taken over.
	s.executed = nil
	s.dbs = []dbDatabase{{name: "billing", owner: "o", comment: managedComment}}
	s.state = map[string]*roleState{"oe_billing_read": {}}
	err := e.SyncDatabase(ctx, target, engine.DesiredDatabase{Name: "billing", Owner: "o"})
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), `role "oe_billing_read" exists and isn't managed`) {
		t.Fatalf("foreign group: err = %v", err)
	}
	if len(s.executed) != 0 {
		t.Fatalf("executed = %q", s.executed)
	}

	if err := e.SyncDatabase(ctx, target, engine.DesiredDatabase{Name: "appdb", Owner: "o"}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("operator database: err = %v", err)
	}

	s.state, s.dbs = nil, nil
	s.execErr = &pgconn.PgError{Message: `role "o" does not exist`}
	err = e.SyncDatabase(ctx, target, engine.DesiredDatabase{Name: "payments", Owner: "o"})
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), `role "o" does not exist`) {
		t.Fatalf("err = %v, want ErrInProgress with the reason", err)
	}

	if err := newTestEngine(s, pgCluster(spec, "initializing")).SyncDatabase(ctx, target, engine.DesiredDatabase{Name: "x", Owner: "o"}); !errors.Is(err, engine.ErrNotReady) {
		t.Fatalf("not ready: err = %v", err)
	}
}

func TestReleaseDatabase(t *testing.T) {
	ctx := context.Background()
	managedGroup := &roleState{comment: managedComment}
	s := &fakeSession{
		dbs: []dbDatabase{
			{name: "orders", owner: "o", comment: managedComment},
			{name: "foreign", owner: "o"},
		},
		state: map[string]*roleState{
			"oe_orders_read": managedGroup, "oe_orders_readwrite": managedGroup,
			"oe_foreign_read": managedGroup, "oe_gone_read": managedGroup, "oe_gone_readwrite": {},
		},
	}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"))

	for _, name := range []string{"foreign", "postgres"} {
		if err := e.ReleaseDatabase(ctx, target, name, true); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
	if len(s.executed) != 0 {
		t.Fatalf("touched a database the plugin doesn't manage: %v", s.executed)
	}

	if err := e.ReleaseDatabase(ctx, target, "orders", false); err != nil {
		t.Fatal(err)
	}
	if err := e.ReleaseDatabase(ctx, target, "orders", true); err != nil {
		t.Fatal(err)
	}
	// The database is already gone: its leftover managed groups are dropped.
	if err := e.ReleaseDatabase(ctx, target, "gone", true); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{`COMMENT ON DATABASE "orders" IS NULL`},
		{
			`SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity WHERE datname = 'orders' AND state = 'idle' AND pid <> pg_catalog.pg_backend_pid()`,
			`DROP DATABASE IF EXISTS "orders"`,
		},
		{`DROP ROLE IF EXISTS "oe_orders_read"`, `DROP ROLE IF EXISTS "oe_orders_readwrite"`},
		{`DROP ROLE IF EXISTS "oe_gone_read"`},
	}
	if !reflect.DeepEqual(s.executed, want) {
		t.Fatalf("executed =\n%q\nwant\n%q", s.executed, want)
	}

	s.execErr = &pgconn.PgError{Message: `database "orders" is being accessed by other users`, Detail: "There are 2 other sessions using the database."}
	err := e.ReleaseDatabase(ctx, target, "orders", true)
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), "2 other sessions") {
		t.Fatalf("err = %v, want ErrInProgress with the reason", err)
	}
}

func TestSyncDatabaseRestrictConnect(t *testing.T) {
	s := &fakeSession{existing: []string{"_crunchypgbouncer", "everest-monitor"}}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"))
	if err := e.SyncDatabase(context.Background(), target, engine.DesiredDatabase{Name: "orders", Owner: "o", RestrictConnect: true}); err != nil {
		t.Fatal(err)
	}
	want := pgaccess.PrivilegeStatements("orders", "o", nil, nil, pgaccess.Connect{Restricted: true, Roles: []string{"_crunchypgbouncer", "everest-monitor"}})
	if got := s.executed[len(s.executed)-1]; !reflect.DeepEqual(got, want) {
		t.Fatalf("executed =\n%q\nwant\n%q", got, want)
	}
}

func TestSyncDatabaseExtensions(t *testing.T) {
	ctx := context.Background()
	s := &fakeSession{
		dbs:       []dbDatabase{{name: "orders", owner: "o", comment: managedComment}},
		state:     map[string]*roleState{"oe_orders_read": {comment: managedComment}, "oe_orders_readwrite": {comment: managedComment}},
		available: []engine.PostgreSQLExtension{{Name: "pg_trgm", Trusted: true}, {Name: "vector"}, {Name: "file_fdw"}, {Name: "hstore", Trusted: true}},
		installed: []string{"plpgsql", "hstore"},
	}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"))
	e.extensions = pgext.Policy{AllowTrusted: true, Allowed: []string{"vector", "postgis"}}

	want := engine.DesiredDatabase{Name: "orders", Owner: "o", PostgreSQL: &engine.PostgreSQLDatabase{
		Extensions: []string{"pg_trgm", "vector"}, DropExtensions: []string{"hstore"},
	}}
	if err := e.SyncDatabase(ctx, target, want); err != nil {
		t.Fatal(err)
	}
	var extStmts []string
	for _, stmts := range s.executed {
		for _, stmt := range stmts {
			if strings.Contains(stmt, "EXTENSION") {
				extStmts = append(extStmts, stmt)
			}
		}
	}
	wantStmts := []string{`DROP EXTENSION IF EXISTS "hstore"`, `CREATE EXTENSION IF NOT EXISTS "pg_trgm"`, `CREATE EXTENSION IF NOT EXISTS "vector"`}
	if !reflect.DeepEqual(extStmts, wantStmts) {
		t.Fatalf("statements = %q", extStmts)
	}

	// Not allowed: Failed; not on the instance: Pending with the reason.
	want.PostgreSQL = &engine.PostgreSQLDatabase{Extensions: []string{"file_fdw"}}
	if err := e.SyncDatabase(ctx, target, want); !errors.Is(err, engine.ErrInvalid) || !strings.Contains(err.Error(), "file_fdw") {
		t.Fatalf("forbidden: err = %v", err)
	}
	want.PostgreSQL = &engine.PostgreSQLDatabase{Extensions: []string{"postgis"}}
	if err := e.SyncDatabase(ctx, target, want); !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), "postgis is not available") {
		t.Fatalf("missing: err = %v", err)
	}
}

func TestDropExtensionRefused(t *testing.T) {
	s := &fakeSession{
		dbs:       []dbDatabase{{name: "orders", owner: "o", comment: managedComment}},
		state:     map[string]*roleState{"oe_orders_read": {comment: managedComment}, "oe_orders_readwrite": {comment: managedComment}},
		installed: []string{"vector"},
	}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"))
	// Every exec fails like a DROP that objects depend on.
	s.execErr = &pgconn.PgError{Message: "cannot drop extension vector because other objects depend on it"}
	err := e.syncExtensions(context.Background(), clusterFrom(pgCluster(providerSpec(), "ready")),
		engine.DesiredDatabase{Name: "orders", PostgreSQL: &engine.PostgreSQLDatabase{DropExtensions: []string{"vector"}}})
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), "can't drop vector: cannot drop extension vector because other objects depend on it") {
		t.Fatalf("err = %v", err)
	}
}

func TestAvailableExtensions(t *testing.T) {
	s := &fakeSession{available: []engine.PostgreSQLExtension{{Name: "pg_trgm", Trusted: true}, {Name: "vector"}, {Name: "plpgsql", Trusted: true}}}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"))
	e.extensions = pgext.Policy{AllowTrusted: true}
	got, err := e.AvailableExtensions(context.Background(), target)
	if err != nil || len(got) != 1 || got[0].Name != "pg_trgm" {
		t.Fatalf("available = %+v, %v", got, err)
	}
}
