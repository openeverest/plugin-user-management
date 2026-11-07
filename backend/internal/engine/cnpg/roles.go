package cnpg

import (
	"fmt"
	"slices"
	"strings"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/pgaccess"
)

// The roles the plugin grants: read and readWrite on one InstanceDatabase,
// through group roles the plugin declares for it, and PostgreSQL predefined
// roles, which apply to every database of the instance. CloudNativePG
// declares memberships but not privileges, so the plugin grants the group
// roles' privileges itself, as the database's owner. Deliberately excludes
// superuser, createrole, replication, bypassrls and the predefined roles that
// reach the server's files or programs: those would let anyone who can use
// the plugin mint a superuser.
var roleCatalog = append(slices.Clone(pgaccess.Roles),
	engine.Role{Name: "pg_read_all_data", Scope: engine.ScopeCluster, Description: "Read all tables, views and sequences in all databases."},
	engine.Role{Name: "pg_write_all_data", Scope: engine.ScopeCluster, Description: "Insert, update and delete rows in all tables in all databases; add pg_read_all_data to read them."},
	engine.Role{Name: "pg_monitor", Scope: engine.ScopeCluster, Description: "Read monitoring views and statistics."},
)

const (
	// PostgreSQL silently truncates longer identifiers (NAMEDATALEN - 1).
	maxUsernameBytes = 63
	// CloudNativePG reads the password from this key of the password Secret.
	passwordSecretKey = "password"
)

// CloudNativePG refuses DatabaseRoles for these.
var (
	reservedUsernames        = map[string]bool{"postgres": true, "streaming_replica": true}
	reservedUsernamePrefixes = []string{"pg_", "cnpg_", pgaccess.GroupPrefix}
)

func isCatalogRole(name string) bool {
	for _, r := range roleCatalog {
		if r.Name == name {
			return true
		}
	}
	return false
}

// normalizeRoles validates grants. Database-scoped roles keep their
// database; predefined roles apply to all databases and lose theirs. A user
// may have no roles, e.g. the owner of a database.
func normalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	seen := map[engine.RoleGrant]bool{}
	normalized := make([]engine.RoleGrant, 0, len(grants))
	for _, g := range grants {
		if !isCatalogRole(g.Role) {
			return nil, fmt.Errorf("%w: role %q can't be granted", engine.ErrInvalid, g.Role)
		}
		grant := engine.RoleGrant{Role: g.Role}
		if pgaccess.IsDatabaseRole(g.Role) {
			var err error
			if grant, err = pgaccess.NormalizeGrant(g); err != nil {
				return nil, err
			}
		}
		if !seen[grant] {
			seen[grant] = true
			normalized = append(normalized, grant)
		}
	}
	return normalized, nil
}

func validateUser(user engine.DesiredUser) error {
	if len(user.Username) > maxUsernameBytes {
		return fmt.Errorf("%w: PostgreSQL usernames are at most %d characters", engine.ErrInvalid, maxUsernameBytes)
	}
	if reservedUsernames[user.Username] {
		return fmt.Errorf("%w: user %q is reserved for the operator", engine.ErrInvalid, user.Username)
	}
	for _, prefix := range reservedUsernamePrefixes {
		if strings.HasPrefix(user.Username, prefix) {
			return fmt.Errorf("%w: usernames starting with %q are reserved", engine.ErrInvalid, prefix)
		}
	}
	if user.Password.Key != "" && user.Password.Key != passwordSecretKey {
		return fmt.Errorf("%w: the password must be under the %q key of its Secret", engine.ErrInvalid, passwordSecretKey)
	}
	return nil
}
