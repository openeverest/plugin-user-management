// Package mongodb manages users of Percona Server for MongoDB instances
// through the user administrator account the operator creates.
package mongodb

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"

	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// ProviderName is the provider (Instance spec.providerRef.name) this engine serves.
const ProviderName = "percona-server-mongodb"

var usernamePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$`)

// Engine implements engine.Engine for Percona Server for MongoDB.
type Engine struct {
	resolve func(ctx context.Context, target engine.Target) (cluster, error)
	dial    dialer
}

// New returns an engine that reads cluster settings from Kubernetes and
// connects to MongoDB with the official driver.
func New(kube kubernetes.Interface, dyn dynamic.Interface) *Engine {
	r := &clusterResolver{kube: kube, dyn: dyn}
	return &Engine{resolve: r.resolve, dial: dialDriver}
}

// Roles implements engine.Engine.
func (e *Engine) Roles() []engine.Role {
	return roleCatalog
}

func (e *Engine) connect(ctx context.Context, target engine.Target) (cluster, session, error) {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return cluster{}, nil, err
	}
	s, err := e.dial(ctx, c)
	if err != nil {
		return cluster{}, nil, err
	}
	return c, s, nil
}

func toUser(c cluster, u dbUser) engine.User {
	user := engine.User{
		ID:       u.ID,
		Username: u.User,
		Database: u.DB,
		Roles:    make([]engine.RoleGrant, 0, len(u.Roles)),
		System:   u.DB == adminDatabase && c.systemUsers[u.User],
	}
	user.Manageable = !user.System && len(u.Roles) > 0
	for _, r := range u.Roles {
		grant, grantable := fromMongoRole(r)
		user.Roles = append(user.Roles, grant)
		user.Manageable = user.Manageable && grantable
	}
	return user
}

// ListUsers implements engine.Engine.
func (e *Engine) ListUsers(ctx context.Context, target engine.Target) ([]engine.User, error) {
	c, s, err := e.connect(ctx, target)
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
		users = append(users, toUser(c, u))
	}
	// Users first, then the operator's system users, each alphabetically.
	sort.Slice(users, func(i, j int) bool {
		if users[i].System != users[j].System {
			return !users[i].System
		}
		return users[i].ID < users[j].ID
	})
	return users, nil
}

// CreateUser implements engine.Engine. Users are created in the admin
// database, which is where MongoDB clients authenticate by default.
func (e *Engine) CreateUser(ctx context.Context, target engine.Target, user engine.NewUser) (engine.Credentials, error) {
	if !usernamePattern.MatchString(user.Username) {
		return engine.Credentials{}, fmt.Errorf("%w: username must be 1-64 letters, digits, '_', '.', '@' or '-', starting with a letter or digit", engine.ErrInvalid)
	}
	roles, err := toMongoRoles(user.Roles)
	if err != nil {
		return engine.Credentials{}, err
	}
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return engine.Credentials{}, err
	}
	defer s.close(ctx)

	if c.systemUsers[user.Username] {
		return engine.Credentials{}, fmt.Errorf("%w: %q is reserved for the operator", engine.ErrConflict, user.Username)
	}
	if err := s.createUser(ctx, adminDatabase, user.Username, user.Password, roles); err != nil {
		return engine.Credentials{}, err
	}
	return credentials(c, user.Username, user.Password, adminDatabase), nil
}

// manageableUser finds the user with the given ID and checks the plugin may change it.
func manageableUser(ctx context.Context, c cluster, s session, id string) (dbUser, error) {
	db, name, ok := strings.Cut(id, ".")
	if !ok || db == "" || name == "" {
		return dbUser{}, engine.ErrNotFound
	}
	users, err := s.usersInfo(ctx)
	if err != nil {
		return dbUser{}, err
	}
	for _, u := range users {
		if u.DB == db && u.User == name {
			if !toUser(c, u).Manageable {
				return dbUser{}, engine.ErrNotManageable
			}
			return u, nil
		}
	}
	return dbUser{}, engine.ErrNotFound
}

// UpdateRoles implements engine.Engine. The given roles replace the user's roles.
func (e *Engine) UpdateRoles(ctx context.Context, target engine.Target, id string, grants []engine.RoleGrant) error {
	roles, err := toMongoRoles(grants)
	if err != nil {
		return err
	}
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return err
	}
	defer s.close(ctx)

	u, err := manageableUser(ctx, c, s, id)
	if err != nil {
		return err
	}
	return s.updateUser(ctx, u.DB, u.User, "", roles)
}

// ResetPassword implements engine.Engine.
func (e *Engine) ResetPassword(ctx context.Context, target engine.Target, id, password string) (engine.Credentials, error) {
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return engine.Credentials{}, err
	}
	defer s.close(ctx)

	u, err := manageableUser(ctx, c, s, id)
	if err != nil {
		return engine.Credentials{}, err
	}
	if err := s.updateUser(ctx, u.DB, u.User, password, nil); err != nil {
		return engine.Credentials{}, err
	}
	return credentials(c, u.User, password, u.DB), nil
}

// DeleteUser implements engine.Engine.
func (e *Engine) DeleteUser(ctx context.Context, target engine.Target, id string) error {
	c, s, err := e.connect(ctx, target)
	if err != nil {
		return err
	}
	defer s.close(ctx)

	u, err := manageableUser(ctx, c, s, id)
	if err != nil {
		return err
	}
	return s.dropUser(ctx, u.DB, u.User)
}

func credentials(c cluster, username, password, authDB string) engine.Credentials {
	query := url.Values{"authSource": {authDB}}
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
	return engine.Credentials{Username: username, Password: password, URI: uri.String()}
}
