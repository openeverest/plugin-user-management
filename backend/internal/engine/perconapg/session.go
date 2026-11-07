package perconapg

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/pgaccess"
)

const (
	connectTimeout  = 10 * time.Second
	lockTimeout     = "10s"
	applicationName = "openeverest-plugin-user-management"
	caCertKey       = "ca.crt"
)

// Every role except PostgreSQL's predefined ones, with the roles it's a member of.
const rolesQuery = `
SELECT r.rolname,
       COALESCE(array_agg(DISTINCT g.rolname::text) FILTER (WHERE g.rolname IS NOT NULL), '{}')
FROM pg_catalog.pg_roles r
LEFT JOIN pg_catalog.pg_auth_members m ON m.member = r.oid
LEFT JOIN pg_catalog.pg_roles g ON g.oid = m.roleid
WHERE r.rolname !~ '^pg_'
GROUP BY r.rolname
ORDER BY r.rolname`

// One role's attributes, stored password, comment and memberships. Reading
// pg_authid takes a superuser.
const roleQuery = `
SELECT a.rolcanlogin, a.rolsuper, a.rolcreatedb, a.rolcreaterole, a.rolreplication, a.rolbypassrls,
       COALESCE(a.rolpassword, ''),
       COALESCE(pg_catalog.shobj_description(a.oid, 'pg_authid'), ''),
       ARRAY(SELECT DISTINCT g.rolname::text
             FROM pg_catalog.pg_auth_members m
             JOIN pg_catalog.pg_roles g ON g.oid = m.roleid
             WHERE m.member = a.oid
             ORDER BY 1)
FROM pg_catalog.pg_authid a
WHERE a.rolname = $1`

// dbRole is a role as the catalog reports it.
type dbRole struct {
	name     string
	memberOf []string
}

// Every database with its owner and comment.
const databasesQuery = `
SELECT d.datname, pg_catalog.pg_get_userbyid(d.datdba), d.datistemplate,
       COALESCE(pg_catalog.shobj_description(d.oid, 'pg_database'), '')
FROM pg_catalog.pg_database d
ORDER BY d.datname`

// dbDatabase is a database as the catalog reports it.
type dbDatabase struct {
	name     string
	owner    string
	template bool
	comment  string
}

func (d dbDatabase) managed() bool {
	return d.comment == managedComment
}

// session reads and changes the roles and databases of a cluster.
type session interface {
	roles(ctx context.Context) ([]dbRole, error)
	// role returns the role's state, or nil when it doesn't exist.
	role(ctx context.Context, name string) (*roleState, error)
	databases(ctx context.Context) ([]dbDatabase, error)
	// exec runs the statements in one transaction.
	exec(ctx context.Context, stmts []string) error
	// execEach runs the statements one by one, outside a transaction, as
	// CREATE DATABASE and DROP DATABASE require.
	execEach(ctx context.Context, stmts []string) error
	// grantTargets runs pgaccess.SchemasQuery and pgaccess.ObjectsQuery for
	// the groups in the session's database.
	grantTargets(ctx context.Context, read, readWrite string) ([]string, []pgaccess.Object, error)
	// existingRoles returns which of the roles exist.
	existingRoles(ctx context.Context, names []string) ([]string, error)
	close(ctx context.Context)
}

// dialer connects to a database of the cluster.
type dialer func(ctx context.Context, c cluster, database string) (session, error)

type pgxSession struct {
	conn *pgx.Conn
}

// pgxDialer connects to the primary as the postgres superuser, which the
// provider always declares, over TLS verified against the cluster's CA.
func pgxDialer(kube kubernetes.Interface) dialer {
	return func(ctx context.Context, c cluster, database string) (session, error) {
		secrets := kube.CoreV1().Secrets(c.namespace)
		creds, err := secrets.Get(ctx, c.superuserSecret, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: superuser secret %q not found", engine.ErrNotReady, c.superuserSecret)
		}
		if err != nil {
			return nil, fmt.Errorf("get superuser secret %q: %w", c.superuserSecret, err)
		}
		user, password := string(creds.Data["user"]), string(creds.Data["password"])
		if user == "" || password == "" {
			return nil, fmt.Errorf("superuser secret %q has no credentials", c.superuserSecret)
		}
		dsn := url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(user, password),
			Host:     net.JoinHostPort(c.host, strconv.FormatInt(c.port, 10)),
			Path:     "/" + database,
			RawQuery: url.Values{"sslmode": {"require"}}.Encode(),
		}
		cfg, err := pgx.ParseConfig(dsn.String())
		if err != nil {
			return nil, fmt.Errorf("postgres connection config: %w", err)
		}
		cfg.ConnectTimeout = connectTimeout
		cfg.RuntimeParams["application_name"] = applicationName
		cfg.RuntimeParams["lock_timeout"] = lockTimeout
		ca, err := secrets.Get(ctx, c.tlsSecret, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, fmt.Errorf("%w: TLS secret %q not found", engine.ErrNotReady, c.tlsSecret)
		}
		if err != nil {
			return nil, fmt.Errorf("get TLS secret %q: %w", c.tlsSecret, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca.Data[caCertKey]) {
			return nil, fmt.Errorf("TLS secret %q has no valid %s", c.tlsSecret, caCertKey)
		}
		cfg.TLSConfig = &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: c.host}
		cfg.Fallbacks = nil
		conn, err := pgx.ConnectConfig(ctx, cfg)
		if err != nil {
			return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
		}
		return &pgxSession{conn: conn}, nil
	}
}

func (s *pgxSession) roles(ctx context.Context) ([]dbRole, error) {
	rows, err := s.conn.Query(ctx, rolesQuery)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	defer rows.Close()
	var roles []dbRole
	for rows.Next() {
		var r dbRole
		if err := rows.Scan(&r.name, &r.memberOf); err != nil {
			return nil, fmt.Errorf("list roles: %w", err)
		}
		roles = append(roles, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	return roles, nil
}

func (s *pgxSession) role(ctx context.Context, name string) (*roleState, error) {
	var r roleState
	err := s.conn.QueryRow(ctx, roleQuery, name).Scan(
		&r.login, &r.superuser, &r.createDB, &r.createRole, &r.replication, &r.bypassRLS,
		&r.verifier, &r.comment, &r.memberOf)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read role %q: %w", name, err)
	}
	return &r, nil
}

func (s *pgxSession) exec(ctx context.Context, stmts []string) error {
	return pgx.BeginFunc(ctx, s.conn, func(tx pgx.Tx) error {
		for _, stmt := range stmts {
			if _, err := tx.Exec(ctx, stmt); err != nil {
				return err
			}
		}
		return nil
	})
}

func (s *pgxSession) databases(ctx context.Context) ([]dbDatabase, error) {
	rows, err := s.conn.Query(ctx, databasesQuery)
	if err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	defer rows.Close()
	var dbs []dbDatabase
	for rows.Next() {
		var d dbDatabase
		if err := rows.Scan(&d.name, &d.owner, &d.template, &d.comment); err != nil {
			return nil, fmt.Errorf("list databases: %w", err)
		}
		dbs = append(dbs, d)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("list databases: %w", err)
	}
	return dbs, nil
}

func (s *pgxSession) execEach(ctx context.Context, stmts []string) error {
	for _, stmt := range stmts {
		if _, err := s.conn.Exec(ctx, stmt); err != nil {
			return err
		}
	}
	return nil
}

func (s *pgxSession) grantTargets(ctx context.Context, read, readWrite string) ([]string, []pgaccess.Object, error) {
	rows, err := s.conn.Query(ctx, pgaccess.SchemasQuery, read, readWrite)
	if err != nil {
		return nil, nil, fmt.Errorf("list schemas: %w", err)
	}
	schemas, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, nil, fmt.Errorf("list schemas: %w", err)
	}
	rows, err = s.conn.Query(ctx, pgaccess.ObjectsQuery, read, readWrite)
	if err != nil {
		return nil, nil, fmt.Errorf("list objects: %w", err)
	}
	objects, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (pgaccess.Object, error) {
		var o pgaccess.Object
		err := row.Scan(&o.Name, &o.Sequence)
		return o, err
	})
	if err != nil {
		return nil, nil, fmt.Errorf("list objects: %w", err)
	}
	return schemas, objects, nil
}

func (s *pgxSession) existingRoles(ctx context.Context, names []string) ([]string, error) {
	rows, err := s.conn.Query(ctx, pgaccess.ExistingRolesQuery, names)
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	roles, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return nil, fmt.Errorf("list roles: %w", err)
	}
	return roles, nil
}

func (s *pgxSession) close(ctx context.Context) {
	_ = s.conn.Close(ctx)
}
