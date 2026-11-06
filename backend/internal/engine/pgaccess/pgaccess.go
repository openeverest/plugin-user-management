// Package pgaccess grants PostgreSQL users access to one database, for the
// engines that manage PostgreSQL: each managed database gets one group role
// per access level, holding that access to the database's objects and to
// those its owner creates later, and users are made members of them.
package pgaccess

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// Database-scoped roles of the catalog.
const (
	RoleRead      = "read"
	RoleReadWrite = "readWrite"
)

// Roles are the database-scoped roles the PostgreSQL engines offer.
var Roles = []engine.Role{
	{Name: RoleRead, Scope: engine.ScopeDatabase, Description: "Connect to the database and read its tables, views and sequences, including ones its owner creates later."},
	{Name: RoleReadWrite, Scope: engine.ScopeDatabase, Description: "Connect to the database, read, insert, update and delete rows in its tables and use its sequences, including ones its owner creates later."},
}

// AccessRoles are the database-scoped roles, in the order their group roles are created.
var AccessRoles = []string{RoleRead, RoleReadWrite}

const (
	// GroupPrefix starts the names of the group roles; usernames can't.
	GroupPrefix = "oe_"
	// PostgreSQL silently truncates longer identifiers (NAMEDATALEN - 1).
	maxIdentifierBytes = 63
)

var groupSuffixes = map[string]string{RoleRead: "_read", RoleReadWrite: "_readwrite"}

// databaseName is the InstanceDatabase name pattern.
var databaseName = regexp.MustCompile(`^[a-z_][a-z0-9_]{0,62}$`)

// IsDatabaseRole reports whether the role is granted on one database.
func IsDatabaseRole(role string) bool {
	_, ok := groupSuffixes[role]
	return ok
}

// GroupRole is the group role granting the role on the database. Names that
// would be too long keep a hash of the database name to stay unique; the hash
// follows a "-", which database names can't contain, so a hashed name never
// equals another database's plain one.
func GroupRole(database, role string) string {
	suffix := groupSuffixes[role]
	name := GroupPrefix + database + suffix
	if len(name) <= maxIdentifierBytes {
		return name
	}
	sum := sha256.Sum256([]byte(database))
	hash := "-" + hex.EncodeToString(sum[:])[:8]
	keep := maxIdentifierBytes - len(GroupPrefix) - len(hash) - len(suffix)
	return GroupPrefix + database[:keep] + hash + suffix
}

// NormalizeGrant validates a database-scoped grant.
func NormalizeGrant(g engine.RoleGrant) (engine.RoleGrant, error) {
	if g.Database == "" {
		return engine.RoleGrant{}, fmt.Errorf("%w: role %q needs a database", engine.ErrInvalid, g.Role)
	}
	if !databaseName.MatchString(g.Database) {
		return engine.RoleGrant{}, fmt.Errorf("%w: database %q can't be managed: names are lowercase letters, digits and underscores", engine.ErrInvalid, g.Database)
	}
	return engine.RoleGrant{Role: g.Role, Database: g.Database}, nil
}

// MemberOf returns the roles a user with the grants is a member of: the
// group role of database-scoped grants, the granted role itself otherwise.
func MemberOf(grants []engine.RoleGrant) []string {
	roles := make([]string, 0, len(grants))
	for _, g := range grants {
		if g.Database != "" && IsDatabaseRole(g.Role) {
			roles = append(roles, GroupRole(g.Database, g.Role))
		} else {
			roles = append(roles, g.Role)
		}
	}
	return roles
}

// Grants maps the roles a user is a member of back to grants, given the
// databases that exist, so ListUsers reports grants as NormalizeRoles does.
func Grants(memberOf, databases []string) []engine.RoleGrant {
	groups := map[string]engine.RoleGrant{}
	for _, db := range databases {
		for _, role := range AccessRoles {
			groups[GroupRole(db, role)] = engine.RoleGrant{Role: role, Database: db}
		}
	}
	grants := make([]engine.RoleGrant, 0, len(memberOf))
	for _, r := range memberOf {
		if g, ok := groups[r]; ok {
			grants = append(grants, g)
		} else {
			grants = append(grants, engine.RoleGrant{Role: r})
		}
	}
	return grants
}

// IsGroupRole reports whether the role is one of the group roles.
func IsGroupRole(name string) bool {
	return strings.HasPrefix(name, GroupPrefix)
}

// The queries below run in the database, with the read and readWrite group
// roles as $1 and $2. They only return what the current user can grant on,
// the objects it owns directly or through a role (all of them for a
// superuser), and only what lacks a privilege, so applying the result is
// cheap when nothing changed. Objects owned by others aren't covered.

// SchemasQuery lists the schemas the groups can't use yet.
const SchemasQuery = `
SELECT pg_catalog.quote_ident(n.nspname)
FROM pg_catalog.pg_namespace n
WHERE n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
  AND pg_catalog.pg_has_role(current_user, n.nspowner, 'USAGE')
  AND NOT (pg_catalog.has_schema_privilege($1, n.oid, 'USAGE')
           AND pg_catalog.has_schema_privilege($2, n.oid, 'USAGE'))
ORDER BY 1`

// ObjectsQuery lists the tables, views and sequences the groups lack a
// privilege on, with whether each is a sequence.
const ObjectsQuery = `
SELECT pg_catalog.quote_ident(n.nspname) || '.' || pg_catalog.quote_ident(c.relname), c.relkind = 'S'
FROM pg_catalog.pg_class c
JOIN pg_catalog.pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f', 'S')
  AND n.nspname !~ '^pg_' AND n.nspname <> 'information_schema'
  AND pg_catalog.pg_has_role(current_user, c.relowner, 'USAGE')
  AND CASE WHEN c.relkind = 'S' THEN
        NOT (pg_catalog.has_sequence_privilege($1, c.oid, 'SELECT')
             AND pg_catalog.has_sequence_privilege($2, c.oid, 'USAGE')
             AND pg_catalog.has_sequence_privilege($2, c.oid, 'SELECT')
             AND pg_catalog.has_sequence_privilege($2, c.oid, 'UPDATE'))
      ELSE
        NOT (pg_catalog.has_table_privilege($1, c.oid, 'SELECT')
             AND pg_catalog.has_table_privilege($2, c.oid, 'SELECT')
             AND pg_catalog.has_table_privilege($2, c.oid, 'INSERT')
             AND pg_catalog.has_table_privilege($2, c.oid, 'UPDATE')
             AND pg_catalog.has_table_privilege($2, c.oid, 'DELETE'))
      END
ORDER BY 1`

// ExistingRolesQuery returns which of the roles in $1 exist.
const ExistingRolesQuery = `
SELECT r.rolname FROM pg_catalog.pg_roles r WHERE r.rolname = ANY($1) ORDER BY 1`

// Connect is who can connect to the database.
type Connect struct {
	// Restricted revokes CONNECT from PUBLIC; the owner, the group roles and
	// superusers keep it.
	Restricted bool
	// Roles keep CONNECT when restricted: the operator's roles that connect
	// to every database, such as its connection pooler's. They must exist.
	Roles []string
}

// Object is a table, view or sequence, by its quoted qualified name.
type Object struct {
	Name     string
	Sequence bool
}

// PrivilegeStatements returns the statements that give the database's group
// roles their access, and set who else can connect, run in the database by
// its owner or a superuser, in one transaction: the schemas and objects come
// from SchemasQuery and ObjectsQuery. All statements are idempotent, so
// switching connect back to public undoes the restriction. Default
// privileges cover schemas and objects the owner creates later.
func PrivilegeStatements(database, owner string, schemas []string, objects []Object, connect Connect) []string {
	read := ident(GroupRole(database, RoleRead))
	readWrite := ident(GroupRole(database, RoleReadWrite))
	both := read + ", " + readWrite
	db := ident(database)
	stmts := []string{fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", db, both)}
	if connect.Restricted {
		stmts = append(stmts, fmt.Sprintf("REVOKE CONNECT ON DATABASE %s FROM PUBLIC", db))
		if len(connect.Roles) > 0 {
			roles := make([]string, 0, len(connect.Roles))
			for _, r := range connect.Roles {
				roles = append(roles, ident(r))
			}
			stmts = append(stmts, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO %s", db, strings.Join(roles, ", ")))
		}
	} else {
		stmts = append(stmts, fmt.Sprintf("GRANT CONNECT ON DATABASE %s TO PUBLIC", db))
	}
	for _, schema := range schemas {
		stmts = append(stmts, fmt.Sprintf("GRANT USAGE ON SCHEMA %s TO %s", schema, both))
	}
	for _, o := range objects {
		if o.Sequence {
			stmts = append(stmts,
				fmt.Sprintf("GRANT SELECT ON SEQUENCE %s TO %s", o.Name, read),
				fmt.Sprintf("GRANT USAGE, SELECT, UPDATE ON SEQUENCE %s TO %s", o.Name, readWrite))
		} else {
			stmts = append(stmts,
				fmt.Sprintf("GRANT SELECT ON TABLE %s TO %s", o.Name, read),
				fmt.Sprintf("GRANT SELECT, INSERT, UPDATE, DELETE ON TABLE %s TO %s", o.Name, readWrite))
		}
	}
	defaults := "ALTER DEFAULT PRIVILEGES FOR ROLE " + ident(owner) + " GRANT "
	return append(stmts,
		defaults+"USAGE ON SCHEMAS TO "+both,
		defaults+"SELECT ON TABLES TO "+read,
		defaults+"SELECT ON SEQUENCES TO "+read,
		defaults+"SELECT, INSERT, UPDATE, DELETE ON TABLES TO "+readWrite,
		defaults+"USAGE, SELECT, UPDATE ON SEQUENCES TO "+readWrite,
	)
}

// ServerReason is the PostgreSQL server's error message, with its detail, or
// "" when err didn't come from the server.
func ServerReason(err error) string {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return ""
	}
	if pgErr.Detail == "" {
		return pgErr.Message
	}
	return pgErr.Message + ": " + strings.ReplaceAll(strings.TrimSpace(pgErr.Detail), "\n", "; ")
}

func ident(name string) string {
	return pgx.Identifier{name}.Sanitize()
}
