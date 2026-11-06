// Package perconapg reconciles users of Percona Operator for PostgreSQL
// instances in SQL. The operator's spec.users can't grant role memberships
// (it applies options with ALTER ROLE), only reads passwords from Secrets it
// writes itself and never drops users, so the plugin creates, alters and
// drops login roles directly, as the postgres superuser, and leaves
// spec.users to the provider.
package perconapg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"sync"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/pgaccess"
)

// ProviderName is the provider (Instance spec.providerRef.name) this engine serves.
const ProviderName = "provider-percona-postgresql"

// Engine implements engine.Engine for the Percona Operator for PostgreSQL.
type Engine struct {
	kube kubernetes.Interface
	dyn  dynamic.Interface
	dial dialer
	// problems keeps why a user couldn't be applied in the last SyncUsers,
	// by namespace/instance/username, for PendingReason.
	problems sync.Map
}

// New returns an engine that reads PerconaPGClusters and manages roles with pgx.
func New(kube kubernetes.Interface, dyn dynamic.Interface) *Engine {
	return &Engine{kube: kube, dyn: dyn, dial: pgxDialer(kube)}
}

// Roles implements engine.Engine.
func (e *Engine) Roles() []engine.Role {
	return roleCatalog
}

// AuthDatabase implements engine.Engine. PostgreSQL roles belong to the
// whole instance, not to a database.
func (e *Engine) AuthDatabase() string {
	return ""
}

// NormalizeRoles implements engine.Engine.
func (e *Engine) NormalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	return normalizeRoles(grants)
}

// ValidateUser implements engine.Engine.
func (e *Engine) ValidateUser(user engine.DesiredUser) error {
	return validateUser(user)
}

func problemKey(target engine.Target, username string) string {
	return target.Namespace + "/" + target.Instance + "/" + username
}

// connect connects to the postgres database once the cluster is ready.
func (e *Engine) connect(ctx context.Context, target engine.Target) (cluster, session, error) {
	return e.connectTo(ctx, target, defaultDatabase)
}

func (e *Engine) connectTo(ctx context.Context, target engine.Target, database string) (cluster, session, error) {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return cluster{}, nil, err
	}
	if !c.ready {
		state := c.state
		if state == "" {
			state = "initializing"
		}
		return cluster{}, nil, fmt.Errorf("%w: PostgreSQL cluster is %q", engine.ErrNotReady, state)
	}
	s, err := e.dial(ctx, c, database)
	if err != nil {
		return cluster{}, nil, err
	}
	return c, s, nil
}

// SyncUsers implements engine.Engine. Each user is a login role with the
// user's password and memberships, applied in its own transaction. Users
// that leave the set keep their role as last applied until DropUser.
func (e *Engine) SyncUsers(ctx context.Context, target engine.Target, users []engine.DesiredUser) error {
	if len(users) == 0 {
		return nil
	}
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return err
	}
	defer s.close(ctx)

	var errs []error
	for _, u := range users {
		problem, err := e.declareUser(ctx, c, s, u)
		if err != nil {
			errs = append(errs, fmt.Errorf("user %q: %w", u.Username, err))
			continue
		}
		if problem != "" {
			e.problems.Store(problemKey(target, u.Username), problem)
		} else {
			e.problems.Delete(problemKey(target, u.Username))
		}
	}
	return errors.Join(errs...)
}

// declareUser applies one user. A problem keeps the user from being applied
// until the user or the database changes; an error is worth retrying.
func (e *Engine) declareUser(ctx context.Context, c cluster, s session, user engine.DesiredUser) (problem string, err error) {
	if err := validateUser(user); err != nil {
		return err.Error(), nil
	}
	if c.isSystemRole(user.Username) {
		return fmt.Sprintf("user %q is reserved for the operator", user.Username), nil
	}
	roles, err := normalizeRoles(user.Roles)
	if err != nil {
		return err.Error(), nil
	}
	password, problem, err := e.readPassword(ctx, c.namespace, user.Password)
	if err != nil || problem != "" {
		return problem, err
	}
	current, err := s.role(ctx, user.Username)
	if err != nil {
		return "", err
	}
	stmts, err := planRole(user.Username, pgaccess.MemberOf(roles), password, current, newVerifier)
	var unmanaged errUnmanagedRole
	if errors.As(err, &unmanaged) {
		return unmanaged.Error(), nil
	}
	if err != nil || len(stmts) == 0 {
		return "", err
	}
	if err := s.exec(ctx, stmts); err != nil {
		if reason := pgaccess.ServerReason(err); reason != "" {
			return "PostgreSQL can't apply the user: " + reason, nil
		}
		return "", err
	}
	return "", nil
}

// readPassword returns the user's password, or a problem that keeps the
// user from having one.
func (e *Engine) readPassword(ctx context.Context, namespace string, ref engine.SecretKeyRef) (password, problem string, err error) {
	secret, err := e.kube.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", fmt.Sprintf("password Secret %q not found", ref.Name), nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get password secret %q: %w", ref.Name, err)
	}
	password = string(secret.Data[ref.Key])
	if password == "" {
		return "", fmt.Sprintf("password Secret %q has no %q key", ref.Name, ref.Key), nil
	}
	return password, "", nil
}

// ListUsers implements engine.Engine. Users declared in spec.users are
// listed even before the operator creates them, so the plugin never
// declares, and takes over, the same role. Memberships of the per-database
// group roles are reported as database-scoped grants.
func (e *Engine) ListUsers(ctx context.Context, target engine.Target) ([]engine.User, error) {
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return nil, err
	}
	defer s.close(ctx)
	dbRoles, err := s.roles(ctx)
	if err != nil {
		return nil, err
	}
	dbs, err := s.databases(ctx)
	if err != nil {
		return nil, err
	}
	databases := make([]string, 0, len(dbs))
	for _, d := range dbs {
		databases = append(databases, d.name)
	}

	users := make([]engine.User, 0, len(dbRoles))
	listed := map[string]bool{}
	for _, r := range dbRoles {
		users = append(users, engine.User{
			Username: r.name,
			Roles:    pgaccess.Grants(r.memberOf, databases),
			System:   c.isSystemRole(r.name) || pgaccess.IsGroupRole(r.name),
		})
		listed[r.name] = true
	}
	for _, name := range c.specUsers {
		if !listed[name] {
			users = append(users, engine.User{Username: name, Roles: []engine.RoleGrant{}, System: true})
			listed[name] = true
		}
	}
	return users, nil
}

// DropUser implements engine.Engine. Only roles the plugin created are
// dropped. PostgreSQL refuses to drop a role that still owns objects or
// holds privileges; its error is reported until they're reassigned or dropped.
func (e *Engine) DropUser(ctx context.Context, target engine.Target, username string) error {
	if validateUser(engine.DesiredUser{Username: username}) != nil {
		return nil
	}
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return err
	}
	defer s.close(ctx)
	if c.isSystemRole(username) {
		return nil
	}
	current, err := s.role(ctx, username)
	if err != nil {
		return err
	}
	if current == nil || !current.managed() {
		e.problems.Delete(problemKey(target, username))
		return nil
	}
	if err := s.exec(ctx, []string{"DROP ROLE IF EXISTS " + quoteIdent(username)}); err != nil {
		if reason := pgaccess.ServerReason(err); reason != "" {
			return fmt.Errorf("%w: PostgreSQL can't drop the role: %s", engine.ErrInProgress, reason)
		}
		return err
	}
	e.problems.Delete(problemKey(target, username))
	return nil
}

// PendingReason implements engine.PendingReasoner. Without a problem, the
// role was just applied and shows up on the next reconcile.
func (e *Engine) PendingReason(_ context.Context, target engine.Target, username string) string {
	if v, ok := e.problems.Load(problemKey(target, username)); ok {
		return v.(string)
	}
	return "applying the user in PostgreSQL"
}

// ConnectionURI implements engine.Engine. It connects through PgBouncer
// when it runs, as the Instance's own connection details do, or else to the
// primary, and to the postgres database.
func (e *Engine) ConnectionURI(ctx context.Context, target engine.Target, username, password string) (string, error) {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return "", err
	}
	host, port := c.clientHost()
	uri := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(username, password),
		Host:     net.JoinHostPort(host, strconv.FormatInt(port, 10)),
		Path:     "/" + defaultDatabase,
		RawQuery: url.Values{"sslmode": {"require"}}.Encode(),
	}
	return uri.String(), nil
}
