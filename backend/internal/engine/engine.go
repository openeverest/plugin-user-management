// Package engine defines the database-agnostic contract the plugin uses to
// reconcile InstanceUsers into an instance's database. Each supported
// provider implements Engine.
package engine

import (
	"context"
	"errors"
	"time"

	"github.com/openeverest/plugin-user-management/backend/internal/password"
)

// RoleScope tells where a role is granted.
type RoleScope string

const (
	// ScopeDatabase roles are granted on one database the user picks.
	ScopeDatabase RoleScope = "database"
	// ScopeBucket roles are granted on one object-storage bucket the user picks.
	ScopeBucket RoleScope = "bucket"
	// ScopeCluster roles apply to the whole instance.
	ScopeCluster RoleScope = "cluster"
)

// Role is a role users can be granted through the plugin.
type Role struct {
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Scope       RoleScope `json:"scope"`
	// Includes are roles this one grants too, on the same database. The UI
	// rejects granting them next to it, as NormalizeRoles would drop them.
	Includes []string `json:"includes,omitempty"`
}

// RoleGrant is a role granted to a user on a scoped resource.
type RoleGrant struct {
	Role     string `json:"role"`
	Database string `json:"database,omitempty"`
	Bucket   string `json:"bucket,omitempty"`
}

// User is a user as it exists in the database.
type User struct {
	Username string `json:"username"`
	// Database is the database the user authenticates against, if the engine has one.
	Database string      `json:"database,omitempty"`
	Roles    []RoleGrant `json:"roles"`
	// System users are created and used by the operator.
	System bool `json:"system"`
	// Hidden users are roles the plugin creates for its own use, such as
	// per-database group roles; they're listed so they're never declared,
	// but not shown.
	Hidden bool `json:"-"`
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
	// ManagedCredentials is true when the plugin owns the credentials Secret.
	ManagedCredentials bool
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
	// ErrInvalidDatabase wraps errors caused by an invalid database
	// declaration; it is also an ErrInvalid.
	ErrInvalidDatabase error = invalidError("invalid database")
	// ErrNotReady means the instance's database can't be reached yet.
	ErrNotReady = errors.New("instance is not ready")
	// ErrInProgress means the operator is still carrying out a change.
	ErrInProgress = errors.New("in progress")
)

// invalidError is an ErrInvalid with its own message.
type invalidError string

func (e invalidError) Error() string        { return string(e) }
func (e invalidError) Is(target error) bool { return target == ErrInvalid }

// PendingReasoner is implemented by engines whose operator reports why a
// declared user isn't applied yet.
type PendingReasoner interface {
	// PendingReason explains why the user isn't applied, or returns "" when
	// the operator reports no problem.
	PendingReason(ctx context.Context, target Target, username string) string
}

// UserProblemReporter is implemented by engines that can fail to apply part
// of a user whose roles already match, e.g. a password the database rejects.
type UserProblemReporter interface {
	// UserProblem returns why the user couldn't be applied in the last
	// SyncUsers, or "" when it was applied.
	UserProblem(target Target, username string) string
}

// CredentialKind tells how users of an engine authenticate.
type CredentialKind string

const (
	// CredentialPassword is a database password.
	CredentialPassword CredentialKind = "password"
	// CredentialAPIKey is an API key, sent with every request.
	CredentialAPIKey CredentialKind = "apiKey"
	// CredentialS3 is an S3 access key ID and secret access key pair.
	CredentialS3 CredentialKind = "s3"
)

// APIKeyEngine is implemented by engines whose users authenticate with API
// keys instead of passwords. The plugin keeps the key where it keeps
// passwords, but generates and validates it with the engine's rules.
type APIKeyEngine interface {
	GenerateKey() (string, error)
	ValidateKey(key string) error
}

// CredentialKindProvider is implemented by engines with a credential form
// other than database passwords or model API keys.
type CredentialKindProvider interface {
	CredentialKind() CredentialKind
}

// ExternalCredentialsProvider reports whether an engine accepts credentials
// from a Secret it does not own.
type ExternalCredentialsProvider interface {
	ExternalCredentialsSupported() bool
}

// ExternalCredentialsSupported reports whether the engine accepts a
// passwordSecretRef. Engines support external credentials unless they opt out.
func ExternalCredentialsSupported(e Engine) bool {
	provider, ok := e.(ExternalCredentialsProvider)
	return !ok || provider.ExternalCredentialsSupported()
}

// CredentialSecretPreparer is implemented by engines that need another field
// in a plugin-owned credential Secret, such as an S3 access key ID. The
// function may add fields but must preserve fields it does not own.
type CredentialSecretPreparer interface {
	PrepareCredentialSecret(ctx context.Context, target Target, username, password string, data map[string][]byte) error
}

// ConnectionPropertiesProvider is implemented by engines whose clients need
// more than a username, password and URI, e.g. the name of a model. The
// properties are added to generated credentials Secrets and responses.
type ConnectionPropertiesProvider interface {
	// ConnectionProperties returns the extra Secret keys and values. They
	// never include username, password or uri.
	ConnectionProperties(ctx context.Context, target Target) (map[string]string, error)
}

// Database is a database as it exists in the instance.
type Database struct {
	Name string
	// Owner is the username of the database's owner, if the engine has owners.
	Owner string
	// System databases are created or declared by the operator or provider.
	System bool
}

// DesiredDatabase is a database the plugin manages, as declared by an
// InstanceDatabase.
type DesiredDatabase struct {
	Name string
	// Owner is the owner's username.
	Owner string
	// OwnerPassword is where the owner's password is kept, for engines that
	// act in the database as its owner.
	OwnerPassword SecretKeyRef
	// RestrictConnect lets only the owner and the users granted a role on
	// the database connect to it, instead of every user.
	RestrictConnect bool
	// PostgreSQL holds PostgreSQL-only options. Only PostgreSQLEngines get
	// it; it's nil for other engines.
	PostgreSQL *PostgreSQLDatabase
}

// PostgreSQLDatabase are the PostgreSQL-only options of a database.
type PostgreSQLDatabase struct {
	// Extensions to have in the database.
	Extensions []string
	// DropExtensions were managed by the plugin and are no longer wanted.
	DropExtensions []string
}

// PostgreSQLExtension is an extension a database can be given.
type PostgreSQLExtension struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	// Trusted extensions can be created by a database's owner.
	Trusted bool `json:"trusted"`
}

// PostgreSQLEngine is implemented by engines of PostgreSQL instances,
// which accept DesiredDatabase.PostgreSQL in SyncDatabase.
type PostgreSQLEngine interface {
	DatabaseEngine
	// AvailableExtensions lists the extensions the instance has that the
	// plugin's policy lets users create.
	AvailableExtensions(ctx context.Context, target Target) ([]PostgreSQLExtension, error)
}

// DatabaseEngine is implemented by engines that manage the databases of an
// instance.
type DatabaseEngine interface {
	// ValidateDatabase rejects, with ErrInvalid, a database name the engine
	// can't declare.
	ValidateDatabase(name string) error
	// ListDatabases reads the databases that exist in the instance, and those
	// the operator or provider will create.
	ListDatabases(ctx context.Context, target Target) ([]Database, error)
	// SyncDatabase declares the database with its owner. ErrInProgress means
	// the operator can't apply it yet; the error says why.
	SyncDatabase(ctx context.Context, target Target, db DesiredDatabase) error
	// ReleaseDatabase stops managing a database that is no longer declared,
	// dropping it when drop is set. A missing database is not an error.
	// ErrInProgress means the release was started and ReleaseDatabase must be
	// called again until it returns nil.
	ReleaseDatabase(ctx context.Context, target Target, name string, drop bool) error
}

// GrantedConnectEngine is implemented by DatabaseEngines whose databases
// only ever let their owner and the users granted a role connect: they
// ignore DesiredDatabase.RestrictConnect.
type GrantedConnectEngine interface {
	DatabaseEngine
	OnlyGrantedConnect()
}

// Resyncer is implemented by engines whose applied users and databases can
// be lost without the plugin noticing, e.g. on a failover, so the plugin
// checks them again every ResyncInterval once they're Ready.
type Resyncer interface {
	ResyncInterval() time.Duration
}

// CredentialKindOf returns how users of the engine authenticate.
func CredentialKindOf(e Engine) CredentialKind {
	if k, ok := e.(CredentialKindProvider); ok {
		return k.CredentialKind()
	}
	if _, ok := e.(APIKeyEngine); ok {
		return CredentialAPIKey
	}
	return CredentialPassword
}

// PrepareCredentialSecret adds engine-specific fields to a plugin-owned
// credentials Secret.
func PrepareCredentialSecret(ctx context.Context, e Engine, target Target, username, password string, data map[string][]byte) error {
	p, ok := e.(CredentialSecretPreparer)
	if !ok {
		return nil
	}
	return p.PrepareCredentialSecret(ctx, target, username, password, data)
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
