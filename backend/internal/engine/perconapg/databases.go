package perconapg

import (
	"context"
	"fmt"
	"slices"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var _ engine.DatabaseEngine = (*Engine)(nil)

// ValidateDatabase implements engine.DatabaseEngine.
func (e *Engine) ValidateDatabase(name string) error {
	if name == "" || len(name) > maxUsernameBytes {
		return fmt.Errorf("%w: PostgreSQL database names are 1 to %d characters", engine.ErrInvalid, maxUsernameBytes)
	}
	if slices.Contains(reservedDatabases, name) {
		return fmt.Errorf("%w: database %q is reserved", engine.ErrInvalid, name)
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
// with its owner, marked as managed, or changes the owner. The controller
// only declares a database it claimed before it existed, so an existing
// database without the mark is one whose creation was interrupted.
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
		return fmt.Errorf("%w: database %q is reserved for the operator", engine.ErrInvalid, want.Name)
	}
	current, err := findDatabase(ctx, s, want.Name)
	if err != nil {
		return err
	}
	stmts := planDatabase(want, current)
	if len(stmts) == 0 {
		return nil
	}
	if err := s.execEach(ctx, stmts); err != nil {
		if reason := postgresReason(err); reason != "" {
			return fmt.Errorf("%w: PostgreSQL can't apply the database: %s", engine.ErrInProgress, reason)
		}
		return err
	}
	return nil
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
// managed. PostgreSQL refuses to drop a database with open sessions; its
// error is reported until they're closed.
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
	if err != nil || current == nil || !current.managed() {
		return err
	}
	stmt := fmt.Sprintf("COMMENT ON DATABASE %s IS NULL", quoteIdent(name))
	if drop {
		stmt = "DROP DATABASE IF EXISTS " + quoteIdent(name)
	}
	if err := s.execEach(ctx, []string{stmt}); err != nil {
		if reason := postgresReason(err); reason != "" {
			return fmt.Errorf("%w: PostgreSQL can't drop the database: %s", engine.ErrInProgress, reason)
		}
		return err
	}
	return nil
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
