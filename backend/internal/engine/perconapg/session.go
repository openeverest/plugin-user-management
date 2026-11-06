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

// session reads and changes the roles of a cluster.
type session interface {
	roles(ctx context.Context) ([]dbRole, error)
	// role returns the role's state, or nil when it doesn't exist.
	role(ctx context.Context, name string) (*roleState, error)
	// exec runs the statements in one transaction.
	exec(ctx context.Context, stmts []string) error
	close(ctx context.Context)
}

type dialer func(ctx context.Context, c cluster) (session, error)

type pgxSession struct {
	conn *pgx.Conn
}

// pgxDialer connects to the primary as the postgres superuser, which the
// provider always declares, over TLS verified against the cluster's CA.
func pgxDialer(kube kubernetes.Interface) dialer {
	return func(ctx context.Context, c cluster) (session, error) {
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
			Path:     "/" + defaultDatabase,
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

func (s *pgxSession) close(ctx context.Context) {
	_ = s.conn.Close(ctx)
}
