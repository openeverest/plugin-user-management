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
)

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
	s := &fakeSession{}
	spec := providerSpec()
	spec["users"] = append(spec["users"].([]any), map[string]any{"name": "app", "databases": []any{"appdb"}})
	e := newTestEngine(s, pgCluster(spec, "ready"))

	if err := e.SyncDatabase(ctx, target, engine.DesiredDatabase{Name: "orders", Owner: "o"}); err != nil {
		t.Fatal(err)
	}
	if len(s.executed) != 1 || s.executed[0][0] != `CREATE DATABASE "orders" OWNER "o"` {
		t.Fatalf("executed = %v", s.executed)
	}
	if err := e.SyncDatabase(ctx, target, engine.DesiredDatabase{Name: "appdb", Owner: "o"}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("operator database: err = %v", err)
	}

	s.execErr = &pgconn.PgError{Message: `role "o" does not exist`}
	err := e.SyncDatabase(ctx, target, engine.DesiredDatabase{Name: "billing", Owner: "o"})
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), `role "o" does not exist`) {
		t.Fatalf("err = %v, want ErrInProgress with the reason", err)
	}

	if err := newTestEngine(s, pgCluster(spec, "initializing")).SyncDatabase(ctx, target, engine.DesiredDatabase{Name: "x", Owner: "o"}); !errors.Is(err, engine.ErrNotReady) {
		t.Fatalf("not ready: err = %v", err)
	}
}

func TestReleaseDatabase(t *testing.T) {
	ctx := context.Background()
	s := &fakeSession{dbs: []dbDatabase{
		{name: "orders", owner: "o", comment: managedComment},
		{name: "foreign", owner: "o"},
	}}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"))

	for _, name := range []string{"missing", "foreign", "postgres"} {
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
	want := [][]string{{`COMMENT ON DATABASE "orders" IS NULL`}, {`DROP DATABASE IF EXISTS "orders"`}}
	if !reflect.DeepEqual(s.executed, want) {
		t.Fatalf("executed = %v", s.executed)
	}

	s.execErr = &pgconn.PgError{Message: `database "orders" is being accessed by other users`, Detail: "There are 2 other sessions using the database."}
	err := e.ReleaseDatabase(ctx, target, "orders", true)
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), "2 other sessions") {
		t.Fatalf("err = %v, want ErrInProgress with the reason", err)
	}
}
