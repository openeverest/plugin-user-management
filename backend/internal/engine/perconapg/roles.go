package perconapg

import (
	"fmt"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// The roles the plugin grants: PostgreSQL predefined roles, which apply to
// every database of the instance, as for CloudNativePG. Deliberately
// excludes superuser, createrole, replication, bypassrls and the predefined
// roles that reach the server's files or programs: those would let anyone who
// can use the plugin mint a superuser.
var roleCatalog = []engine.Role{
	{Name: "pg_read_all_data", Scope: engine.ScopeCluster, Description: "Read all tables, views and sequences in all databases."},
	{Name: "pg_write_all_data", Scope: engine.ScopeCluster, Description: "Insert, update and delete rows in all tables in all databases; add pg_read_all_data to read them."},
	{Name: "pg_monitor", Scope: engine.ScopeCluster, Description: "Read monitoring views and statistics."},
}

const (
	// PostgreSQL silently truncates longer identifiers (NAMEDATALEN - 1).
	maxUsernameBytes = 63
	// roleComment marks the roles the plugin created, so it never alters or
	// drops a role it doesn't manage.
	roleComment = "Managed by OpenEverest"
	// The attributes of every managed role: it can log in and nothing more.
	roleAttributes = "LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS"
)

// PostgreSQL reserves pg_ role names; the operator's own roles start with _crunchy.
var reservedUsernamePrefixes = []string{"pg_", operatorPrefix}

func isCatalogRole(name string) bool {
	for _, r := range roleCatalog {
		if r.Name == name {
			return true
		}
	}
	return false
}

// normalizeRoles validates grants and returns them without databases, as
// predefined roles apply to all databases.
func normalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	if len(grants) == 0 {
		return nil, fmt.Errorf("%w: at least one role is required", engine.ErrInvalid)
	}
	seen := map[string]bool{}
	normalized := make([]engine.RoleGrant, 0, len(grants))
	for _, g := range grants {
		if !isCatalogRole(g.Role) {
			return nil, fmt.Errorf("%w: role %q can't be granted", engine.ErrInvalid, g.Role)
		}
		if !seen[g.Role] {
			seen[g.Role] = true
			normalized = append(normalized, engine.RoleGrant{Role: g.Role})
		}
	}
	return normalized, nil
}

// validateUser rejects usernames PostgreSQL or the operator reserve. The
// plugin reads the password itself, so any Secret key works.
func validateUser(user engine.DesiredUser) error {
	if len(user.Username) > maxUsernameBytes {
		return fmt.Errorf("%w: PostgreSQL usernames are at most %d characters", engine.ErrInvalid, maxUsernameBytes)
	}
	if slices.Contains(operatorRoles, user.Username) {
		return fmt.Errorf("%w: user %q is reserved for the operator", engine.ErrInvalid, user.Username)
	}
	for _, prefix := range reservedUsernamePrefixes {
		if strings.HasPrefix(user.Username, prefix) {
			return fmt.Errorf("%w: usernames starting with %q are reserved", engine.ErrInvalid, prefix)
		}
	}
	return nil
}

// roleState is a role as the catalog reports it, for SyncUsers and DropUser.
type roleState struct {
	login       bool
	superuser   bool
	createDB    bool
	createRole  bool
	replication bool
	bypassRLS   bool
	// verifier is the stored password (rolpassword), empty without one.
	verifier string
	comment  string
	memberOf []string
}

func (s roleState) managed() bool {
	return s.comment == roleComment
}

func (s roleState) hasRoleAttributes() bool {
	return s.login && !s.superuser && !s.createDB && !s.createRole && !s.replication && !s.bypassRLS
}

// errUnmanagedRole means the role exists but the plugin didn't create it.
type errUnmanagedRole string

func (e errUnmanagedRole) Error() string {
	return fmt.Sprintf("user %q already exists in the database and isn't managed by OpenEverest", string(e))
}

// planRole returns the statements that make the role match the user: a
// login role with only the granted memberships and the given password. A
// nil current means the role doesn't exist yet. newVerifier hashes the
// password, so it never reaches the server, or its logs, in plain text.
func planRole(username string, grants []engine.RoleGrant, password string, current *roleState, newVerifier func(string) (string, error)) ([]string, error) {
	role := quoteIdent(username)
	var stmts []string
	if current == nil {
		verifier, err := newVerifier(password)
		if err != nil {
			return nil, err
		}
		stmts = append(stmts,
			fmt.Sprintf("CREATE ROLE %s WITH %s PASSWORD %s", role, roleAttributes, quoteLiteral(verifier)),
			fmt.Sprintf("COMMENT ON ROLE %s IS %s", role, quoteLiteral(roleComment)),
		)
		current = &roleState{}
	} else {
		if !current.managed() {
			return nil, errUnmanagedRole(username)
		}
		if !current.hasRoleAttributes() {
			stmts = append(stmts, fmt.Sprintf("ALTER ROLE %s WITH %s", role, roleAttributes))
		}
		if !verifierMatches(current.verifier, password) {
			verifier, err := newVerifier(password)
			if err != nil {
				return nil, err
			}
			stmts = append(stmts, fmt.Sprintf("ALTER ROLE %s WITH PASSWORD %s", role, quoteLiteral(verifier)))
		}
	}

	desired := map[string]bool{}
	for _, g := range grants {
		desired[g.Role] = true
		if !slices.Contains(current.memberOf, g.Role) {
			stmts = append(stmts, fmt.Sprintf("GRANT %s TO %s", quoteIdent(g.Role), role))
		}
	}
	for _, r := range current.memberOf {
		if !desired[r] {
			stmts = append(stmts, fmt.Sprintf("REVOKE %s FROM %s", quoteIdent(r), role))
		}
	}
	return stmts, nil
}

func quoteIdent(name string) string {
	return pgx.Identifier{name}.Sanitize()
}

// quoteLiteral quotes a string constant, with standard_conforming_strings on
// as it is by default.
func quoteLiteral(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "''") + "'"
}

func grantsOf(roles []string) []engine.RoleGrant {
	grants := make([]engine.RoleGrant, 0, len(roles))
	for _, r := range roles {
		grants = append(grants, engine.RoleGrant{Role: r})
	}
	return grants
}
