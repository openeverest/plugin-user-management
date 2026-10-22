package cnpg

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"time"

	"github.com/jackc/pgx/v5"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
)

const (
	connectTimeout  = 10 * time.Second
	applicationName = "openeverest-plugin-user-management"
	caCertKey       = "ca.crt"
)

// Every role except PostgreSQL's predefined ones, with the roles it's a member of.
const rolesQuery = `
SELECT r.rolname,
       COALESCE(array_agg(g.rolname ORDER BY g.rolname) FILTER (WHERE g.rolname IS NOT NULL), '{}')
FROM pg_catalog.pg_roles r
LEFT JOIN pg_catalog.pg_auth_members m ON m.member = r.oid
LEFT JOIN pg_catalog.pg_roles g ON g.oid = m.roleid
WHERE r.rolname !~ '^pg_'
GROUP BY r.rolname
ORDER BY r.rolname`

// dbRole is a role as the catalog reports it.
type dbRole struct {
	name     string
	memberOf []string
}

// session reads the role catalog of a cluster.
type session interface {
	roles(ctx context.Context) ([]dbRole, error)
	close(ctx context.Context)
}

type dialer func(ctx context.Context, c cluster) (session, error)

type pgxSession struct {
	conn *pgx.Conn
}

// pgxDialer connects with the first usable credentials of the cluster: the
// application owner, or the superuser when superuser access is enabled. Any
// role can read the role catalog.
func pgxDialer(kube kubernetes.Interface) dialer {
	return func(ctx context.Context, c cluster) (session, error) {
		user, password, err := loginCredentials(ctx, kube, c)
		if err != nil {
			return nil, err
		}
		dsn := url.URL{
			Scheme:   "postgres",
			User:     url.UserPassword(user, password),
			Host:     net.JoinHostPort(c.host, postgresPort),
			Path:     "/" + c.database,
			RawQuery: url.Values{"sslmode": {"require"}}.Encode(),
		}
		cfg, err := pgx.ParseConfig(dsn.String())
		if err != nil {
			return nil, fmt.Errorf("postgres connection config: %w", err)
		}
		cfg.ConnectTimeout = connectTimeout
		cfg.RuntimeParams["application_name"] = applicationName
		ca, err := kube.CoreV1().Secrets(c.namespace).Get(ctx, c.caSecret, metav1.GetOptions{})
		if err != nil {
			return nil, fmt.Errorf("get CA secret %q: %w", c.caSecret, err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(ca.Data[caCertKey]) {
			return nil, fmt.Errorf("CA secret %q has no valid %s", c.caSecret, caCertKey)
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

func loginCredentials(ctx context.Context, kube kubernetes.Interface, c cluster) (string, string, error) {
	for _, name := range c.loginSecrets {
		secret, err := kube.CoreV1().Secrets(c.namespace).Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			continue
		}
		if err != nil {
			return "", "", fmt.Errorf("get credentials secret %q: %w", name, err)
		}
		user, password := string(secret.Data["username"]), string(secret.Data["password"])
		if user != "" && password != "" {
			return user, password, nil
		}
	}
	return "", "", errors.New("no PostgreSQL credentials to read users with")
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

func (s *pgxSession) close(ctx context.Context) {
	_ = s.conn.Close(ctx)
}
