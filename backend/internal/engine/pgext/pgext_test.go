package pgext

import (
	"reflect"
	"testing"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var available = []engine.PostgreSQLExtension{
	{Name: "pg_trgm", Trusted: true},
	{Name: "plpgsql", Trusted: true},
	{Name: "vector"},
	{Name: "file_fdw"},
}

func TestPolicy(t *testing.T) {
	p := Policy{AllowTrusted: true, Allowed: []string{"vector", "postgis"}}
	got := p.Filter(available)
	want := []engine.PostgreSQLExtension{{Name: "pg_trgm", Trusted: true}, {Name: "vector"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("filter = %+v", got)
	}
	forbidden, missing := p.Check([]string{"vector", "file_fdw", "plpgsql", "postgis", "pg_trgm"}, available)
	if !reflect.DeepEqual(forbidden, []string{"file_fdw", "plpgsql"}) || !reflect.DeepEqual(missing, []string{"postgis"}) {
		t.Fatalf("forbidden = %v, missing = %v", forbidden, missing)
	}

	// Without trusted extensions, only the allowlist.
	if got := (Policy{Allowed: []string{"vector"}}).Filter(available); len(got) != 1 || got[0].Name != "vector" {
		t.Fatalf("filter = %+v", got)
	}
	// An admin can't make plpgsql managed.
	if (Policy{Allowed: []string{"plpgsql"}}).Allows(engine.PostgreSQLExtension{Name: "plpgsql"}) {
		t.Fatal("plpgsql allowed")
	}
}

func TestParseList(t *testing.T) {
	if got := ParseList(" vector, ,postgis "); !reflect.DeepEqual(got, []string{"vector", "postgis"}) {
		t.Fatalf("got %v", got)
	}
	if got := ParseList(""); got != nil {
		t.Fatalf("got %v", got)
	}
}
