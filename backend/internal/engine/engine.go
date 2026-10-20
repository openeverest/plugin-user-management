// Package engine defines the database-agnostic contract the plugin uses to
// manage users of an instance. Each supported provider implements Engine.
package engine

import (
	"context"
	"errors"
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

// User is a database user.
type User struct {
	// ID identifies the user within the instance; opaque to callers.
	ID       string `json:"id"`
	Username string `json:"username"`
	// Database is the database the user authenticates against, if the engine has one.
	Database string      `json:"database,omitempty"`
	Roles    []RoleGrant `json:"roles"`
	// System users are created and used by the operator; changing them would break the instance.
	System bool `json:"system"`
	// Manageable users can be changed through the plugin: they are not system
	// users and hold only roles the plugin can grant, so editing them can't
	// strip or hand out privileges the plugin doesn't manage.
	Manageable bool `json:"manageable"`
}

// NewUser is a user to create.
type NewUser struct {
	Username string
	Password string
	Roles    []RoleGrant
}

// Credentials are returned once, right after a user is created or its password is reset.
type Credentials struct {
	Username string `json:"username"`
	Password string `json:"password"`
	// URI connects to the instance as the user from inside the Kubernetes cluster.
	URI string `json:"uri"`
}

// Target is the instance whose users are managed.
type Target struct {
	Namespace string
	Instance  string
}

// Engine manages the users of instances of one provider.
type Engine interface {
	// Roles lists the roles the plugin can grant.
	Roles() []Role
	ListUsers(ctx context.Context, target Target) ([]User, error)
	CreateUser(ctx context.Context, target Target, user NewUser) (Credentials, error)
	UpdateRoles(ctx context.Context, target Target, id string, roles []RoleGrant) error
	ResetPassword(ctx context.Context, target Target, id, password string) (Credentials, error)
	DeleteUser(ctx context.Context, target Target, id string) error
}

var (
	// ErrInvalid wraps errors caused by invalid input.
	ErrInvalid = errors.New("invalid request")
	// ErrNotFound means the user does not exist.
	ErrNotFound = errors.New("user not found")
	// ErrConflict means the user already exists.
	ErrConflict = errors.New("user already exists")
	// ErrNotManageable means the user can't be changed through the plugin.
	ErrNotManageable = errors.New("user can't be managed through the plugin")
	// ErrNotReady means the instance can't be reached yet.
	ErrNotReady = errors.New("instance is not ready")
)
