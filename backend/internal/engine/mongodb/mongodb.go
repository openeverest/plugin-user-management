// Package mongodb reconciles users of Percona Server for MongoDB instances
// through the operator's own user API: the plugin declares users in the
// PerconaServerMongoDB's spec.users and the operator creates them and keeps
// their roles and passwords in sync.
package mongodb

import (
	"context"
	"errors"
	"fmt"
	"net/url"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// ProviderName is the provider (Instance spec.providerRef.name) this engine serves.
const ProviderName = "percona-server-mongodb"

// Engine implements engine.Engine for Percona Server for MongoDB.
type Engine struct {
	resolve    func(ctx context.Context, target engine.Target) (cluster, error)
	dial       dialer
	applyUsers func(ctx context.Context, target engine.Target, users []psmdbUser) error
}

// New returns an engine that reads and writes PerconaServerMongoDB resources
// and reads users with the official MongoDB driver.
func New(kube kubernetes.Interface, dyn dynamic.Interface) *Engine {
	r := &clusterResolver{kube: kube, dyn: dyn}
	return &Engine{resolve: r.resolve, dial: dialDriver, applyUsers: r.applyUsers}
}

// Roles implements engine.Engine.
func (e *Engine) Roles() []engine.Role {
	return roleCatalog
}

// AuthDatabase implements engine.Engine.
func (e *Engine) AuthDatabase() string {
	return adminDatabase
}

// NormalizeRoles implements engine.Engine.
func (e *Engine) NormalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	roles, err := toMongoRoles(grants)
	if err != nil {
		return nil, err
	}
	normalized := make([]engine.RoleGrant, 0, len(roles))
	for _, r := range roles {
		grant, _ := fromMongoRole(r)
		normalized = append(normalized, grant)
	}
	return normalized, nil
}

// ValidateUser implements engine.Engine. The DatabaseUser username pattern
// already fits MongoDB, and the operator reads any password key.
func (e *Engine) ValidateUser(engine.DesiredUser) error {
	return nil
}

// SyncUsers implements engine.Engine. Users are created in the admin
// database, which is where MongoDB clients authenticate by default.
func (e *Engine) SyncUsers(ctx context.Context, target engine.Target, users []engine.DesiredUser) error {
	declared := make([]psmdbUser, 0, len(users))
	for _, u := range users {
		roles, err := toMongoRoles(u.Roles)
		if err != nil {
			return fmt.Errorf("user %q: %w", u.Username, err)
		}
		pu := psmdbUser{
			Name:              u.Username,
			DB:                adminDatabase,
			PasswordSecretRef: psmdbSecretRef{Name: u.Password.Name, Key: u.Password.Key},
		}
		for _, r := range roles {
			pu.Roles = append(pu.Roles, psmdbRole{Name: r.Role, DB: r.DB})
		}
		declared = append(declared, pu)
	}
	return e.applyUsers(ctx, target, declared)
}

// ListUsers implements engine.Engine.
func (e *Engine) ListUsers(ctx context.Context, target engine.Target) ([]engine.User, error) {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	s, err := e.dial(ctx, c)
	if err != nil {
		return nil, err
	}
	defer s.close(ctx)

	dbUsers, err := s.usersInfo(ctx)
	if err != nil {
		return nil, err
	}
	users := make([]engine.User, 0, len(dbUsers))
	for _, u := range dbUsers {
		user := engine.User{
			Username: u.User,
			Database: u.DB,
			Roles:    make([]engine.RoleGrant, 0, len(u.Roles)),
			System:   u.DB == adminDatabase && c.systemUsers[u.User],
		}
		for _, r := range u.Roles {
			grant, _ := fromMongoRole(r)
			user.Roles = append(user.Roles, grant)
		}
		users = append(users, user)
	}
	return users, nil
}

// DropUser implements engine.Engine. The operator doesn't remove users that
// leave spec.users, so the plugin drops them itself.
func (e *Engine) DropUser(ctx context.Context, target engine.Target, username string) error {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return err
	}
	s, err := e.dial(ctx, c)
	if err != nil {
		return err
	}
	defer s.close(ctx)

	if err := s.dropUser(ctx, adminDatabase, username); err != nil && !errors.Is(err, errUserNotFound) {
		return err
	}
	return nil
}

// ConnectionURI implements engine.Engine.
func (e *Engine) ConnectionURI(ctx context.Context, target engine.Target, username, password string) (string, error) {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return "", err
	}
	query := url.Values{"authSource": {adminDatabase}}
	if c.requireTLS {
		query.Set("tls", "true")
	}
	uri := url.URL{
		Scheme:   "mongodb",
		User:     url.UserPassword(username, password),
		Host:     c.host,
		Path:     "/",
		RawQuery: query.Encode(),
	}
	return uri.String(), nil
}
