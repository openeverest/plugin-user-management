package perconapg

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/pgaccess"
)

var _ engine.PostgreSQLEngine = (*Engine)(nil)

// ValidateDatabase implements engine.DatabaseEngine.
func (e *Engine) ValidateDatabase(name string) error {
	if name == "" || len(name) > maxUsernameBytes {
		return fmt.Errorf("%w: PostgreSQL database names are 1 to %d characters", engine.ErrInvalidDatabase, maxUsernameBytes)
	}
	if slices.Contains(reservedDatabases, name) {
		return fmt.Errorf("%w: database %q is reserved", engine.ErrInvalidDatabase, name)
	}
	return nil
}

// ListDatabases implements engine.DatabaseEngine. Databases the operator
// creates for spec.users are listed even before they exist.
func (e *Engine) ListDatabases(ctx context.Context, target engine.Target) ([]engine.Database, error) {
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return nil, err
	}
	defer s.close(ctx)
	dbs, err := s.databases(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]engine.Database, 0, len(dbs))
	listed := map[string]bool{}
	for _, d := range dbs {
		out = append(out, engine.Database{Name: d.name, Owner: d.owner, System: d.template || c.systemDatabases[d.name]})
		listed[d.name] = true
	}
	for name := range c.systemDatabases {
		if !listed[name] {
			out = append(out, engine.Database{Name: name, System: true})
		}
	}
	return out, nil
}

// SyncDatabase implements engine.DatabaseEngine: it creates the database
// with its owner, marked as managed, or changes the owner, then keeps its
// group roles and their access current. The controller only declares a
// database it claimed before it existed, so an existing database without the
// mark is one whose creation was interrupted.
func (e *Engine) SyncDatabase(ctx context.Context, target engine.Target, want engine.DesiredDatabase) error {
	if err := e.ValidateDatabase(want.Name); err != nil {
		return err
	}
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return err
	}
	defer s.close(ctx)
	if c.systemDatabases[want.Name] {
		return fmt.Errorf("%w: database %q is reserved for the operator", engine.ErrInvalidDatabase, want.Name)
	}
	current, err := findDatabase(ctx, s, want.Name)
	if err != nil {
		return err
	}
	if stmts := planDatabase(want, current); len(stmts) > 0 {
		if err := s.execEach(ctx, stmts); err != nil {
			return inProgress(err, "PostgreSQL can't apply the database")
		}
	}
	if err := ensureGroups(ctx, s, want.Name); err != nil {
		return err
	}
	if err := e.grantAccess(ctx, c, want); err != nil {
		return err
	}
	return e.syncExtensions(ctx, c, want)
}

// syncExtensions creates the wanted extensions and drops those the plugin
// no longer manages, in the database, as the superuser. Drops don't
// cascade: an extension that objects depend on stays, with PostgreSQL's
// error reported.
func (e *Engine) syncExtensions(ctx context.Context, c cluster, want engine.DesiredDatabase) error {
	pg := want.PostgreSQL
	if pg == nil || len(pg.Extensions)+len(pg.DropExtensions) == 0 {
		return nil
	}
	s, err := e.dial(ctx, c, want.Name)
	if err != nil {
		return err
	}
	defer s.close(ctx)
	available, err := s.availableExtensions(ctx)
	if err != nil {
		return err
	}
	forbidden, missing := e.extensions.Check(pg.Extensions, available)
	if len(forbidden) > 0 {
		return fmt.Errorf("%w: extensions %s can't be created: the plugin's policy doesn't allow them", engine.ErrInvalidDatabase, strings.Join(forbidden, ", "))
	}
	installed, err := s.installedExtensions(ctx)
	if err != nil {
		return err
	}
	var problems []string
	for _, name := range pg.DropExtensions {
		if !slices.Contains(installed, name) || slices.Contains(pg.Extensions, name) {
			continue
		}
		if err := s.exec(ctx, []string{"DROP EXTENSION IF EXISTS " + quoteIdent(name)}); err != nil {
			reason := pgaccess.ServerReason(err)
			if reason == "" {
				return err
			}
			problems = append(problems, "can't drop "+name+": "+reason)
		}
	}
	for _, name := range pg.Extensions {
		switch {
		case slices.Contains(missing, name):
			problems = append(problems, "extension "+name+" is not available on the instance")
		case !slices.Contains(installed, name):
			if err := s.exec(ctx, []string{"CREATE EXTENSION IF NOT EXISTS " + quoteIdent(name)}); err != nil {
				reason := pgaccess.ServerReason(err)
				if reason == "" {
					return err
				}
				problems = append(problems, "can't create "+name+": "+reason)
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", engine.ErrInProgress, strings.Join(problems, "; "))
	}
	return nil
}

// AvailableExtensions implements engine.PostgreSQLEngine.
func (e *Engine) AvailableExtensions(ctx context.Context, target engine.Target) ([]engine.PostgreSQLExtension, error) {
	_, s, err := e.connect(ctx, target)
	if err != nil {
		return nil, err
	}
	defer s.close(ctx)
	available, err := s.availableExtensions(ctx)
	if err != nil {
		return nil, err
	}
	return e.extensions.Filter(available), nil
}

// ensureGroups creates the database's group roles, marked as managed.
func ensureGroups(ctx context.Context, s session, database string) error {
	for _, access := range pgaccess.AccessRoles {
		name := pgaccess.GroupRole(database, access)
		current, err := s.role(ctx, name)
		if err != nil {
			return err
		}
		if current != nil {
			if !current.managed() {
				return fmt.Errorf("%w: role %q exists and isn't managed by OpenEverest", engine.ErrInProgress, name)
			}
			continue
		}
		stmts := []string{
			"CREATE ROLE " + quoteIdent(name) + " WITH NOLOGIN",
			fmt.Sprintf("COMMENT ON ROLE %s IS %s", quoteIdent(name), quoteLiteral(managedComment)),
		}
		if err := s.exec(ctx, stmts); err != nil {
			return inProgress(err, "PostgreSQL can't create the database's roles")
		}
	}
	return nil
}

// grantAccess gives the group roles their access, in the database, as the superuser.
func (e *Engine) grantAccess(ctx context.Context, c cluster, want engine.DesiredDatabase) error {
	s, err := e.dial(ctx, c, want.Name)
	if err != nil {
		return err
	}
	defer s.close(ctx)
	read, readWrite := pgaccess.GroupRole(want.Name, pgaccess.RoleRead), pgaccess.GroupRole(want.Name, pgaccess.RoleReadWrite)
	schemas, objects, err := s.grantTargets(ctx, read, readWrite)
	if err != nil {
		return inProgress(err, "PostgreSQL can't list the database's objects")
	}
	connect := pgaccess.Connect{Restricted: want.RestrictConnect}
	if connect.Restricted {
		if connect.Roles, err = s.existingRoles(ctx, connectRoles); err != nil {
			return inProgress(err, "PostgreSQL can't list the roles that connect to every database")
		}
	}
	if err := s.exec(ctx, pgaccess.PrivilegeStatements(want.Name, want.Owner, schemas, objects, connect)); err != nil {
		return inProgress(err, "PostgreSQL can't grant access to the database")
	}
	return nil
}

// connectRoles are the operator's and provider's roles that connect to every
// database: PgBouncer runs its auth_query in the client's database as
// _crunchypgbouncer, and the monitoring users collect per-database metrics.
var connectRoles = []string{"_crunchypgbouncer", "ccp_monitoring", "monitor", "everest-monitor"}

// inProgress reports a server error as ErrInProgress with what failed;
// other errors are returned as they are, to be retried.
func inProgress(err error, what string) error {
	if reason := pgaccess.ServerReason(err); reason != "" {
		return fmt.Errorf("%w: %s: %s", engine.ErrInProgress, what, reason)
	}
	return err
}

// planDatabase returns the statements that make the database match: it
// exists, is owned by the owner and carries the plugin's comment.
func planDatabase(want engine.DesiredDatabase, current *dbDatabase) []string {
	db, owner := quoteIdent(want.Name), quoteIdent(want.Owner)
	comment := fmt.Sprintf("COMMENT ON DATABASE %s IS %s", db, quoteLiteral(managedComment))
	if current == nil {
		return []string{fmt.Sprintf("CREATE DATABASE %s OWNER %s", db, owner), comment}
	}
	var stmts []string
	if current.owner != want.Owner {
		stmts = append(stmts, fmt.Sprintf("ALTER DATABASE %s OWNER TO %s", db, owner))
	}
	if !current.managed() {
		stmts = append(stmts, comment)
	}
	return stmts
}

// ReleaseDatabase implements engine.DatabaseEngine. Only databases the
// plugin created are touched: dropped, or unmarked so they're no longer
// managed. Idle sessions, such as PgBouncer's pooled server connections, are
// closed before dropping; PostgreSQL refuses to drop a database while
// sessions run queries or transactions, and its error is reported until they end.
func (e *Engine) ReleaseDatabase(ctx context.Context, target engine.Target, name string, drop bool) error {
	if e.ValidateDatabase(name) != nil {
		return nil
	}
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return err
	}
	defer s.close(ctx)
	if c.systemDatabases[name] {
		return nil
	}
	current, err := findDatabase(ctx, s, name)
	if err != nil {
		return err
	}
	if current != nil {
		if !current.managed() {
			return nil
		}
		stmts := []string{fmt.Sprintf("COMMENT ON DATABASE %s IS NULL", quoteIdent(name))}
		if drop {
			stmts = []string{
				fmt.Sprintf("SELECT pg_catalog.pg_terminate_backend(pid) FROM pg_catalog.pg_stat_activity "+
					"WHERE datname = %s AND state = 'idle' AND pid <> pg_catalog.pg_backend_pid()", quoteLiteral(name)),
				"DROP DATABASE IF EXISTS " + quoteIdent(name),
			}
		}
		if err := s.execEach(ctx, stmts); err != nil {
			return inProgress(err, "PostgreSQL can't drop the database")
		}
	}
	if !drop {
		return nil
	}
	// The group roles go with the database, also when a previous attempt
	// dropped the database but not them; memberships go with the roles.
	var drops []string
	for _, access := range pgaccess.AccessRoles {
		group := pgaccess.GroupRole(name, access)
		state, err := s.role(ctx, group)
		if err != nil {
			return err
		}
		if state != nil && state.managed() {
			drops = append(drops, "DROP ROLE IF EXISTS "+quoteIdent(group))
		}
	}
	if len(drops) == 0 {
		return nil
	}
	return inProgress(s.exec(ctx, drops), "PostgreSQL can't drop the database's roles")
}

func findDatabase(ctx context.Context, s session, name string) (*dbDatabase, error) {
	dbs, err := s.databases(ctx)
	if err != nil {
		return nil, err
	}
	for i := range dbs {
		if dbs[i].name == name {
			return &dbs[i], nil
		}
	}
	return nil, nil
}
