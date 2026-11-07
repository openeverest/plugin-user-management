// Package pgext decides which PostgreSQL extensions users may create in a
// database, for the engines that manage PostgreSQL.
package pgext

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// Policy is which extensions users may create.
type Policy struct {
	// AllowTrusted allows PostgreSQL's trusted extensions, which a
	// database's owner could create anyway.
	AllowTrusted bool
	// Allowed are further extensions the platform admin allows.
	Allowed []string
}

// never are extensions the plugin doesn't manage: plpgsql is in every database.
var never = []string{"plpgsql"}

// Allows reports whether users may create the extension.
func (p Policy) Allows(ext engine.PostgreSQLExtension) bool {
	if slices.Contains(never, ext.Name) {
		return false
	}
	return p.AllowTrusted && ext.Trusted || slices.Contains(p.Allowed, ext.Name)
}

// ParseList parses a comma-separated list of extension names.
func ParseList(s string) []string {
	var names []string
	for _, n := range strings.Split(s, ",") {
		if n = strings.TrimSpace(n); n != "" {
			names = append(names, n)
		}
	}
	return names
}

// Every extension the instance has, with whether any version is trusted.
const availableQuery = `
SELECT a.name, COALESCE(a.comment, ''),
       EXISTS (SELECT 1 FROM pg_catalog.pg_available_extension_versions v WHERE v.name = a.name AND v.trusted)
FROM pg_catalog.pg_available_extensions a
ORDER BY a.name`

// Available lists the extensions the instance has; any database will do.
func Available(ctx context.Context, conn *pgx.Conn) ([]engine.PostgreSQLExtension, error) {
	rows, err := conn.Query(ctx, availableQuery)
	if err != nil {
		return nil, fmt.Errorf("list available extensions: %w", err)
	}
	exts, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (engine.PostgreSQLExtension, error) {
		var e engine.PostgreSQLExtension
		err := row.Scan(&e.Name, &e.Description, &e.Trusted)
		return e, err
	})
	if err != nil {
		return nil, fmt.Errorf("list available extensions: %w", err)
	}
	return exts, nil
}

// Installed lists the extensions of the connection's database.
func Installed(ctx context.Context, conn *pgx.Conn) ([]string, error) {
	rows, err := conn.Query(ctx, `SELECT extname FROM pg_catalog.pg_extension ORDER BY 1`)
	if err != nil {
		return nil, fmt.Errorf("list extensions: %w", err)
	}
	names, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list extensions: %w", err)
	}
	return names, nil
}

// Filter returns the available extensions the policy allows.
func (p Policy) Filter(available []engine.PostgreSQLExtension) []engine.PostgreSQLExtension {
	out := []engine.PostgreSQLExtension{}
	for _, e := range available {
		if p.Allows(e) {
			out = append(out, e)
		}
	}
	return out
}

// Check sorts wanted extensions into those the policy forbids and those the
// instance doesn't have; the rest can be created.
func (p Policy) Check(wanted []string, available []engine.PostgreSQLExtension) (forbidden, missing []string) {
	byName := map[string]engine.PostgreSQLExtension{}
	for _, e := range available {
		byName[e.Name] = e
	}
	for _, name := range wanted {
		ext, ok := byName[name]
		switch {
		case slices.Contains(never, name):
			forbidden = append(forbidden, name)
		case !ok:
			// Whether it's trusted is only known once the instance has it.
			missing = append(missing, name)
		case !p.Allows(ext):
			forbidden = append(forbidden, name)
		}
	}
	sort.Strings(forbidden)
	sort.Strings(missing)
	return forbidden, missing
}
