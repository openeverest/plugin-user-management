// Package engine defines the database-agnostic contract the plugin uses to
// reconcile InstanceUsers into an instance's database. Each supported
// provider implements Engine.
package engine

import (
	"context"
	"errors"

	"github.com/openeverest/plugin-user-management/backend/internal/password"
)

// RoleScope tells where a role is granted.
type RoleScope string

const (
	// ScopeDatabase roles are granted on one database the user picks.
	ScopeDatabase RoleScope = "database"
	// ScopeCluster roles apply to the whole instance.
	ScopeCluster RoleScope = "cluster"
)

// Role is a role users can be granted through the plugin.
type Role struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Scope       RoleScope `json:"scope"`
}

// RoleGrant is a role granted to a user, on a database for ScopeDatabase roles.
type RoleGrant struct {
	Role     string `json:"role"`
	Database string `json:"database,omitempty"`
}

// User is a user as it exists in the database.
type User struct {
	Username string `json:"username"`
	// Database is the database the user authenticates against, if the engine has one.
	Database string      `json:"database,omitempty"`
	Roles    []RoleGrant `json:"roles"`
	// System users are created and used by the operator.
	System bool `json:"system"`
}

// SecretKeyRef points to a key of a Secret in the instance's namespace.
type SecretKeyRef struct {
	Name string
	Key  string
}

// DesiredUser is a user the plugin manages, as declared by an InstanceUser.
type DesiredUser struct {
	Username string
	// Roles as returned by NormalizeRoles.
	Roles    []RoleGrant
	Password SecretKeyRef
}

// Target is the instance whose users are managed.
type Target struct {
	Namespace string
	Instance  string
}

// Engine reconciles the users of instances of one provider.
type Engine interface {
	// Roles lists the roles the plugin can grant.
	Roles() []Role
	// NormalizeRoles validates grants against Roles and returns them in the
	// form ListUsers reports them, so the two can be compared.
	NormalizeRoles(grants []RoleGrant) ([]RoleGrant, error)
	// ValidateUser rejects, with ErrInvalid, a user the engine can't declare
	// whatever its roles: a reserved or too long username, an unusable
	// password Secret key.
	ValidateUser(user DesiredUser) error
	// SyncUsers declares the complete set of users the plugin manages on the
	// instance. Users dropped from the set are not removed from the database
	// (see DropUser): they stop being managed, or keep their last declaration.
	SyncUsers(ctx context.Context, target Target, users []DesiredUser) error
	// ListUsers reads the users that exist in the database.
	ListUsers(ctx context.Context, target Target) ([]User, error)
	// AuthDatabase is the database managed users authenticate against, as
	// reported in User.Database; empty for engines without one.
	AuthDatabase() string
	// DropUser removes a user that is no longer declared. A missing user is
	// not an error. ErrInProgress means the removal was started and DropUser
	// must be called again until it returns nil.
	DropUser(ctx context.Context, target Target, username string) error
	// ConnectionURI returns a URI that connects to the instance as the user
	// from inside the Kubernetes cluster.
	ConnectionURI(ctx context.Context, target Target, username, password string) (string, error)
}

var (
	// ErrInvalid wraps errors caused by an invalid user declaration.
	ErrInvalid = errors.New("invalid user")
	// ErrNotReady means the instance's database can't be reached yet.
	ErrNotReady = errors.New("instance is not ready")
	// ErrInProgress means the operator is still carrying out a change.
	ErrInProgress = errors.New("in progress")
)

// PendingReasoner is implemented by engines whose operator reports why a
// declared user isn't applied yet.
type PendingReasoner interface {
	// PendingReason explains why the user isn't applied, or returns "" when
	// the operator reports no problem.
	PendingReason(ctx context.Context, target Target, username string) string
}

// CredentialKind tells how users of an engine authenticate.
type CredentialKind string

const (
	// CredentialPassword is a database password.
	CredentialPassword CredentialKind = "password"
	// CredentialAPIKey is an API key, sent with every request.
	CredentialAPIKey CredentialKind = "apiKey"
)

// APIKeyEngine is implemented by engines whose users authenticate with API
// keys instead of passwords. The plugin keeps the key where it keeps
// passwords, but generates and validates it with the engine's rules.
type APIKeyEngine interface {
	GenerateKey() (string, error)
	ValidateKey(key string) error
}

// ConnectionPropertiesProvider is implemented by engines whose clients need
// more than a username, password and URI, e.g. the name of a model. The
// properties are added to generated credentials Secrets and responses.
type ConnectionPropertiesProvider interface {
	// ConnectionProperties returns the extra Secret keys and values. They
	// never include username, password or uri.
	ConnectionProperties(ctx context.Context, target Target) (map[string]string, error)
}

// CredentialKindOf returns how users of the engine authenticate.
func CredentialKindOf(e Engine) CredentialKind {
	if _, ok := e.(APIKeyEngine); ok {
		return CredentialAPIKey
	}
	return CredentialPassword
}

// GeneratePassword returns a new password, or API key, for a user of the engine.
func GeneratePassword(e Engine) (string, error) {
	if k, ok := e.(APIKeyEngine); ok {
		return k.GenerateKey()
	}
	return password.Generate()
}

// ValidatePassword checks a user-supplied password, or API key, for the engine.
func ValidatePassword(e Engine, value string) error {
	if k, ok := e.(APIKeyEngine); ok {
		return k.ValidateKey(value)
	}
	return password.Validate(value)
}

// ConnectionProperties returns the engine's extra connection properties, or
// nil when it has none or they aren't known yet.
func ConnectionProperties(ctx context.Context, e Engine, target Target) map[string]string {
	p, ok := e.(ConnectionPropertiesProvider)
	if !ok {
		return nil
	}
	props, err := p.ConnectionProperties(ctx, target)
	if err != nil {
		return nil
	}
	return props
}

// SameRoles reports whether two normalized grant lists hold the same grants.
func SameRoles(a, b []RoleGrant) bool {
	set := map[RoleGrant]int{}
	for _, g := range a {
		set[g] = 1
	}
	for _, g := range b {
		if set[g] == 0 {
			return false
		}
		set[g] = 2
	}
	for _, v := range set {
		if v != 2 {
			return false
		}
	}
	return true
}
