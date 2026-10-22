// Package cnpg reconciles users of CloudNativePG instances through the
// operator's own role API: the plugin declares each user as a DatabaseRole
// and the operator creates the role, keeps its memberships and password in
// sync and drops it when asked to.
package cnpg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// ProviderName is the provider (Instance spec.providerRef.name) this engine serves.
const ProviderName = "provider-cloudnative-pg"

// Engine implements engine.Engine for CloudNativePG.
type Engine struct {
	kube  kubernetes.Interface
	dyn   dynamic.Interface
	dial  dialer
	apply func(ctx context.Context, obj *unstructured.Unstructured) error
}

// New returns an engine that writes DatabaseRoles, reads Clusters and
// reads roles with pgx.
func New(kube kubernetes.Interface, dyn dynamic.Interface) *Engine {
	e := &Engine{kube: kube, dyn: dyn, dial: pgxDialer(kube)}
	e.apply = e.applyDatabaseRole
	return e
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

// SyncUsers implements engine.Engine with one DatabaseRole per user. Users
// that leave the set keep their DatabaseRole as last declared until DropUser
// or the Cluster removes it.
func (e *Engine) SyncUsers(ctx context.Context, target engine.Target, users []engine.DesiredUser) error {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return err
	}
	var errs []error
	for _, u := range users {
		if err := e.declareUser(ctx, c, u); err != nil {
			errs = append(errs, fmt.Errorf("user %q: %w", u.Username, err))
		}
	}
	return errors.Join(errs...)
}

func (e *Engine) declareUser(ctx context.Context, c cluster, user engine.DesiredUser) error {
	if err := validateUser(user); err != nil {
		return err
	}
	roles, err := normalizeRoles(user.Roles)
	if err != nil {
		return err
	}
	user.Roles = roles
	if err := e.ensureReloadLabel(ctx, c.namespace, user.Password.Name); err != nil {
		return err
	}
	return e.apply(ctx, databaseRoleApplyConfiguration(c, user))
}

// ListUsers implements engine.Engine. Roles that other DatabaseRoles or the
// Cluster spec declare are listed even before the operator creates them, so
// the plugin never declares, and takes over, the same role.
func (e *Engine) ListUsers(ctx context.Context, target engine.Target) ([]engine.User, error) {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	if !c.ready {
		return nil, fmt.Errorf("%w: PostgreSQL cluster is %q", engine.ErrNotReady, c.phase)
	}
	s, err := e.dial(ctx, c)
	if err != nil {
		return nil, err
	}
	defer s.close(ctx)
	dbRoles, err := s.roles(ctx)
	if err != nil {
		return nil, err
	}

	users := make([]engine.User, 0, len(dbRoles))
	listed := map[string]bool{}
	for _, r := range dbRoles {
		users = append(users, engine.User{Username: r.name, Roles: grantsOf(r.memberOf), System: c.isSystemRole(r.name)})
		listed[r.name] = true
	}
	foreign, err := e.foreignRoles(ctx, c)
	if err != nil {
		return nil, err
	}
	for _, u := range foreign {
		if !listed[u.Username] {
			users = append(users, u)
			listed[u.Username] = true
		}
	}
	for _, name := range c.managedRoles {
		if !listed[name] {
			users = append(users, engine.User{Username: name, Roles: []engine.RoleGrant{}, System: true})
			listed[name] = true
		}
	}
	return users, nil
}

// DropUser implements engine.Engine.
func (e *Engine) DropUser(ctx context.Context, target engine.Target, username string) error {
	return e.dropDatabaseRole(ctx, target, username)
}

// ConnectionURI implements engine.Engine. It connects to the primary,
// through the read-write service, and the application database.
func (e *Engine) ConnectionURI(ctx context.Context, target engine.Target, username, password string) (string, error) {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return "", err
	}
	uri := url.URL{
		Scheme:   "postgresql",
		User:     url.UserPassword(username, password),
		Host:     net.JoinHostPort(c.host, postgresPort),
		Path:     "/" + c.database,
		RawQuery: url.Values{"sslmode": {"require"}}.Encode(),
	}
	return uri.String(), nil
}
