package mongodb

import (
	"fmt"
	"regexp"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// adminDatabase holds cluster-wide role grants and the users the plugin creates.
const adminDatabase = "admin"

// The roles the plugin grants. Deliberately excludes roles that reach user
// administration or cluster administration on the whole instance (root,
// userAdminAnyDatabase, clusterAdmin, backup/restore, ...): those would let
// anyone who can use the plugin mint a superuser.
var roleCatalog = []engine.Role{
	{Name: "read", Scope: engine.ScopeDatabase, Description: "Read data in the database."},
	{Name: "readWrite", Scope: engine.ScopeDatabase, Description: "Read and write data in the database."},
	{Name: "dbAdmin", Scope: engine.ScopeDatabase, Description: "Manage indexes, statistics and schema of the database."},
	{Name: "userAdmin", Scope: engine.ScopeDatabase, Description: "Manage users and roles of the database."},
	{Name: "dbOwner", Scope: engine.ScopeDatabase, Description: "Any action on the database (readWrite, dbAdmin and userAdmin)."},
	{Name: "readAnyDatabase", Scope: engine.ScopeCluster, Description: "Read data in all databases."},
	{Name: "readWriteAnyDatabase", Scope: engine.ScopeCluster, Description: "Read and write data in all databases."},
	{Name: "dbAdminAnyDatabase", Scope: engine.ScopeCluster, Description: "dbAdmin on all databases."},
	{Name: "clusterMonitor", Scope: engine.ScopeCluster, Description: "Read-only access to monitoring tools."},
}

// Database-scoped roles on these would reach users or internals of the whole instance.
var reservedDatabases = map[string]bool{"admin": true, "config": true, "local": true}

// MongoDB forbids these in database names; also keeps names to a sane length.
var databaseNamePattern = regexp.MustCompile(`^[^/\\. "$*<>:|?\x00]{1,63}$`)

func catalogRole(name string) (engine.Role, bool) {
	for _, r := range roleCatalog {
		if r.Name == name {
			return r, true
		}
	}
	return engine.Role{}, false
}

// mongoRole is a role as MongoDB stores it.
type mongoRole struct {
	Role string `bson:"role"`
	DB   string `bson:"db"`
}

// toMongoRoles validates grants and converts them to MongoDB roles.
func toMongoRoles(grants []engine.RoleGrant) ([]mongoRole, error) {
	if len(grants) == 0 {
		return nil, fmt.Errorf("%w: at least one role is required", engine.ErrInvalid)
	}
	seen := map[mongoRole]bool{}
	roles := make([]mongoRole, 0, len(grants))
	for _, g := range grants {
		role, ok := catalogRole(g.Role)
		if !ok {
			return nil, fmt.Errorf("%w: role %q can't be granted", engine.ErrInvalid, g.Role)
		}
		mr := mongoRole{Role: g.Role, DB: adminDatabase}
		if role.Scope == engine.ScopeDatabase {
			if !databaseNamePattern.MatchString(g.Database) {
				return nil, fmt.Errorf("%w: role %q needs a valid database name", engine.ErrInvalid, g.Role)
			}
			if reservedDatabases[g.Database] {
				return nil, fmt.Errorf("%w: role %q can't be granted on the %q database", engine.ErrInvalid, g.Role, g.Database)
			}
			mr.DB = g.Database
		}
		if !seen[mr] {
			seen[mr] = true
			roles = append(roles, mr)
		}
	}
	return roles, nil
}

// fromMongoRole converts a stored role to a grant and reports whether the
// plugin could have granted it.
func fromMongoRole(r mongoRole) (engine.RoleGrant, bool) {
	role, ok := catalogRole(r.Role)
	if ok && role.Scope == engine.ScopeCluster && r.DB == adminDatabase {
		return engine.RoleGrant{Role: r.Role}, true
	}
	grantable := ok && role.Scope == engine.ScopeDatabase && !reservedDatabases[r.DB]
	return engine.RoleGrant{Role: r.Role, Database: r.DB}, grantable
}
