package pgaccess

import (
	"reflect"
	"strings"
	"testing"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

func TestGroupRole(t *testing.T) {
	if got := GroupRole("orders", RoleRead); got != "oe_orders_read" {
		t.Errorf("read = %q", got)
	}
	if got := GroupRole("orders", RoleReadWrite); got != "oe_orders_readwrite" {
		t.Errorf("readWrite = %q", got)
	}
	long := strings.Repeat("a", 63)
	other := strings.Repeat("a", 62) + "b"
	for _, role := range AccessRoles {
		a, b := GroupRole(long, role), GroupRole(other, role)
		if len(a) > 63 || len(b) > 63 || a == b || !strings.HasPrefix(a, GroupPrefix) {
			t.Errorf("%s: long names %q and %q", role, a, b)
		}
	}
}

func TestMemberOfAndGrants(t *testing.T) {
	grants := []engine.RoleGrant{{Role: RoleRead, Database: "orders"}, {Role: RoleReadWrite, Database: strings.Repeat("x", 60)}, {Role: "pg_monitor"}}
	memberOf := MemberOf(grants)
	if memberOf[0] != "oe_orders_read" || memberOf[2] != "pg_monitor" {
		t.Fatalf("memberOf = %q", memberOf)
	}
	got := Grants(memberOf, []string{"orders", strings.Repeat("x", 60), "postgres"})
	if !reflect.DeepEqual(got, grants) {
		t.Fatalf("grants = %+v, want %+v", got, grants)
	}
}

func TestPrivilegeStatements(t *testing.T) {
	got := PrivilegeStatements("orders", "orders_owner", []string{"sales"},
		[]Object{{Name: "sales.orders"}, {Name: `sales."Seq"`, Sequence: true}})
	want := []string{
		`GRANT CONNECT ON DATABASE "orders" TO "oe_orders_read", "oe_orders_readwrite"`,
		`GRANT USAGE ON SCHEMA sales TO "oe_orders_read", "oe_orders_readwrite"`,
		`GRANT SELECT ON TABLE sales.orders TO "oe_orders_read"`,
		`GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE sales.orders TO "oe_orders_readwrite"`,
		`GRANT SELECT ON SEQUENCE sales."Seq" TO "oe_orders_read"`,
		`GRANT USAGE, SELECT, UPDATE ON SEQUENCE sales."Seq" TO "oe_orders_readwrite"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "orders_owner" GRANT USAGE ON SCHEMAS TO "oe_orders_read", "oe_orders_readwrite"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "orders_owner" GRANT SELECT ON TABLES TO "oe_orders_read"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "orders_owner" GRANT SELECT ON SEQUENCES TO "oe_orders_read"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "orders_owner" GRANT SELECT, INSERT, UPDATE, DELETE ON TABLES TO "oe_orders_readwrite"`,
		`ALTER DEFAULT PRIVILEGES FOR ROLE "orders_owner" GRANT USAGE, SELECT, UPDATE ON SEQUENCES TO "oe_orders_readwrite"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("statements =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}

// A hashed name can't be produced as another database's plain name: the
// hash follows a "-", which database names can't contain.
func TestGroupRoleNoCollision(t *testing.T) {
	for _, long := range []string{"customer_orders_archive_for_the_analytics_team_2026_q4", strings.Repeat("a", 63)} {
		for _, role := range AccessRoles {
			if len(GroupPrefix+long+groupSuffixes[role]) <= 63 {
				continue
			}
			hashed := GroupRole(long, role)
			if !strings.Contains(hashed, "-") || len(hashed) > 63 {
				t.Fatalf("%s: hashed name %q", role, hashed)
			}
			// The database name an attacker would pick to reproduce it.
			crafted := strings.TrimPrefix(strings.TrimSuffix(hashed, groupSuffixes[role]), GroupPrefix)
			if databaseName.MatchString(crafted) {
				t.Fatalf("%s: %q is a valid database name with group %q", role, crafted, hashed)
			}
		}
	}
}
