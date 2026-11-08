package perconapg

import (
	"context"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/pgaccess"
)

var target = engine.Target{Namespace: "ns", Instance: "pg"}

func pgCluster(spec map[string]any, state string) *unstructured.Unstructured {
	if spec == nil {
		spec = map[string]any{}
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "pgv2.percona.com/v2",
		"kind":       "PerconaPGCluster",
		"metadata":   map[string]any{"name": "pg", "namespace": "ns"},
		"spec":       spec,
		"status":     map[string]any{"state": state},
	}}
}

// providerSpec is spec as provider-percona-postgresql renders it.
func providerSpec() map[string]any {
	return map[string]any{
		"proxy": map[string]any{"pgBouncer": map[string]any{"replicas": int64(1)}},
		"users": []any{map[string]any{"name": "postgres", "databases": []any{"postgres"}}},
	}
}

func passwordSecret(name, key, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Data:       map[string][]byte{key: []byte(value)},
	}
}

type fakeSession struct {
	dbRoles   []dbRole
	state     map[string]*roleState
	dbs       []dbDatabase
	schemas   []string
	objects   []pgaccess.Object
	dialed    []string
	existing  []string
	available []engine.PostgreSQLExtension
	installed []string
	executed  [][]string
	execErr   error
	closed    bool
}

func (f *fakeSession) roles(context.Context) ([]dbRole, error) { return f.dbRoles, nil }
func (f *fakeSession) role(_ context.Context, name string) (*roleState, error) {
	return f.state[name], nil
}
func (f *fakeSession) databases(context.Context) ([]dbDatabase, error) { return f.dbs, nil }
func (f *fakeSession) exec(_ context.Context, stmts []string) error {
	if f.execErr != nil {
		return f.execErr
	}
	f.executed = append(f.executed, stmts)
	return nil
}
func (f *fakeSession) execEach(ctx context.Context, stmts []string) error { return f.exec(ctx, stmts) }
func (f *fakeSession) grantTargets(context.Context, string, string) ([]string, []pgaccess.Object, error) {
	return f.schemas, f.objects, nil
}
func (f *fakeSession) availableExtensions(context.Context) ([]engine.PostgreSQLExtension, error) {
	return f.available, nil
}
func (f *fakeSession) installedExtensions(context.Context) ([]string, error) {
	return f.installed, nil
}
func (f *fakeSession) existingRoles(_ context.Context, names []string) ([]string, error) {
	var found []string
	for _, n := range names {
		if slices.Contains(f.existing, n) {
			found = append(found, n)
		}
	}
	return found, nil
}
func (f *fakeSession) close(context.Context) { f.closed = true }

func newTestEngine(s *fakeSession, obj *unstructured.Unstructured, secrets ...runtime.Object) *Engine {
	gvrs := map[schema.GroupVersionResource]string{clusterResource: "PerconaPGClusterList"}
	var objects []runtime.Object
	if obj != nil {
		objects = append(objects, obj)
	}
	return &Engine{
		kube: kubefake.NewClientset(secrets...),
		dyn:  dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), gvrs, objects...),
		dial: func(_ context.Context, _ cluster, database string) (session, error) {
			if s != nil {
				s.dialed = append(s.dialed, database)
			}
			return s, nil
		},
	}
}

func mustVerifier(t *testing.T, password string) string {
	t.Helper()
	v, err := newVerifier(password)
	if err != nil {
		t.Fatal(err)
	}
	return v
}

func TestNormalizeRoles(t *testing.T) {
	got, err := normalizeRoles([]engine.RoleGrant{
		{Role: "pg_read_all_data"},
		{Role: "pg_read_all_data", Database: "shop"},
		{Role: "pg_monitor"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.RoleGrant{{Role: "pg_read_all_data"}, {Role: "pg_monitor"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized = %+v, want %+v", got, want)
	}
	if got, err := normalizeRoles(nil); err != nil || len(got) != 0 {
		t.Fatalf("no roles: %v, %v", got, err)
	}
	for name, grants := range map[string][]engine.RoleGrant{
		"ungrantable role": {{Role: "pg_execute_server_program"}},
		"superuser":        {{Role: "postgres"}},
	} {
		if _, err := normalizeRoles(grants); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestValidateUser(t *testing.T) {
	for _, user := range []engine.DesiredUser{
		{Username: "orders_svc", Password: engine.SecretKeyRef{Name: "s", Key: "password"}},
		{Username: "Orders.svc@team", Password: engine.SecretKeyRef{Name: "s", Key: "pw"}},
	} {
		if err := validateUser(user); err != nil {
			t.Errorf("%q: %v", user.Username, err)
		}
	}
	for name, user := range map[string]engine.DesiredUser{
		"superuser":         {Username: "postgres"},
		"pmm user":          {Username: "monitor"},
		"pgmonitor user":    {Username: "ccp_monitoring"},
		"exporter user":     {Username: "everest-monitor"},
		"predefined prefix": {Username: "pg_reader"},
		"operator prefix":   {Username: "_crunchyrepl"},
		"too long":          {Username: strings.Repeat("a", 64)},
	} {
		if err := validateUser(user); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
}

func TestClusterFrom(t *testing.T) {
	c := clusterFrom(pgCluster(providerSpec(), "ready"))
	if !c.ready || c.host != "pg-primary.ns.svc" || c.port != 5432 {
		t.Fatalf("cluster = %+v", c)
	}
	if !c.pgBouncer || c.pgBouncerHost != "pg-pgbouncer.ns.svc" || c.pgBouncerPort != 5432 {
		t.Fatalf("pgbouncer = %v %s:%d", c.pgBouncer, c.pgBouncerHost, c.pgBouncerPort)
	}
	if c.superuserSecret != "pg-pguser-postgres" || c.tlsSecret != "pg-cluster-cert" {
		t.Fatalf("secrets = %q, %q", c.superuserSecret, c.tlsSecret)
	}
	for _, name := range []string{"postgres", "monitor", "ccp_monitoring", "everest-monitor", "_crunchyrepl", "_crunchypgbouncer"} {
		if !c.isSystemRole(name) {
			t.Errorf("%q is not a system role", name)
		}
	}
	if c.isSystemRole("app") {
		t.Error("app is a system role")
	}

	c = clusterFrom(pgCluster(map[string]any{
		"port":    int64(6432),
		"proxy":   map[string]any{"pgBouncer": map[string]any{"replicas": int64(0), "port": int64(7432)}},
		"secrets": map[string]any{"customTLSSecret": map[string]any{"name": "my-tls"}},
		"users": []any{
			map[string]any{"name": "postgres", "secretName": "my-root"},
			map[string]any{"name": "app_owner"},
		},
	}, "initializing"))
	if c.ready || c.state != "initializing" || c.port != 6432 || c.pgBouncer || c.pgBouncerPort != 7432 {
		t.Fatalf("cluster = %+v", c)
	}
	if c.superuserSecret != "my-root" || c.tlsSecret != "my-tls" {
		t.Fatalf("secrets = %q, %q", c.superuserSecret, c.tlsSecret)
	}
	if !c.isSystemRole("app_owner") || !reflect.DeepEqual(c.specUsers, []string{"postgres", "app_owner"}) {
		t.Fatalf("spec users = %v", c.specUsers)
	}

	if c := clusterFrom(pgCluster(nil, "ready")); c.pgBouncer {
		t.Fatal("pgbouncer without spec.proxy")
	}
}

func TestVerifier(t *testing.T) {
	v := mustVerifier(t, "s3cret pass")
	if !strings.HasPrefix(v, "SCRAM-SHA-256$4096:") {
		t.Fatalf("verifier = %q", v)
	}
	if !verifierMatches(v, "s3cret pass") {
		t.Fatal("verifier doesn't match its password")
	}
	if verifierMatches(v, "other") {
		t.Fatal("verifier matches another password")
	}
	if mustVerifier(t, "s3cret pass") == v {
		t.Fatal("verifiers aren't salted")
	}
	for _, stored := range []string{"", "md5c5f1f8c6f1f1", "SCRAM-SHA-256$x:AA==$AA==:AA==", "SCRAM-SHA-256$99999999:AA==$AA==:AA=="} {
		if verifierMatches(stored, "s3cret pass") {
			t.Errorf("%q matches", stored)
		}
	}
	// Generated by PostgreSQL 17, which normalizes passwords with SASLprep
	// (NFKC maps ① to 1).
	for password, stored := range map[string]string{
		"pencil":     "SCRAM-SHA-256$4096:DQHxWxtwdN6ZIF7hIOE7lA==$JeoByCoyKBo2l2npIsYmTk4ImFqztBFw6DYZan0CEok=:L06hXdoUIw2R37hjwa8jRCfAqA1RqDWcczQadf9gf2E=",
		"pässwörd ①": "SCRAM-SHA-256$4096:2daAFMQNaJ0pXixtJYXfew==$icLqRWCYaN0VjAuX0AlN16TGGrLoeHc/JVDH3AuzufY=:98Ti9HPhaCXrKAUmGS3wj7LqPiImGGBNj1ag2jBndik=",
	} {
		if !verifierMatches(stored, password) {
			t.Errorf("PostgreSQL's verifier of %q doesn't match", password)
		}
	}
}

func TestPlanRoleCreate(t *testing.T) {
	grants := []string{"pg_read_all_data", "pg_monitor"}
	stmts, err := planRole(`app"x`, grants, "pw", nil, func(string) (string, error) { return "SCRAM-v", nil })
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`CREATE ROLE "app""x" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS PASSWORD 'SCRAM-v'`,
		`COMMENT ON ROLE "app""x" IS 'Managed by OpenEverest'`,
		`GRANT "pg_read_all_data" TO "app""x"`,
		`GRANT "pg_monitor" TO "app""x"`,
	}
	if !reflect.DeepEqual(stmts, want) {
		t.Fatalf("statements =\n%s\nwant\n%s", strings.Join(stmts, "\n"), strings.Join(want, "\n"))
	}
}

func TestPlanRoleExisting(t *testing.T) {
	verifier := mustVerifier(t, "pw")
	grants := []string{"pg_read_all_data", "pg_monitor"}
	inSync := roleState{login: true, verifier: verifier, comment: managedComment, memberOf: []string{"pg_monitor", "pg_read_all_data"}}
	noVerifier := func(string) (string, error) { return "SCRAM-new", nil }

	stmts, err := planRole("app", grants, "pw", &inSync, noVerifier)
	if err != nil || len(stmts) != 0 {
		t.Fatalf("in sync: statements = %v, err = %v", stmts, err)
	}

	drifted := roleState{
		login: true, superuser: true, verifier: verifier, comment: managedComment,
		memberOf: []string{"pg_read_all_data", "pg_write_all_data", "app_owner"},
	}
	stmts, err = planRole("app", grants, "new-pw", &drifted, noVerifier)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		`ALTER ROLE "app" WITH LOGIN NOSUPERUSER NOCREATEDB NOCREATEROLE NOREPLICATION NOBYPASSRLS`,
		`ALTER ROLE "app" WITH PASSWORD 'SCRAM-new'`,
		`GRANT "pg_monitor" TO "app"`,
		`REVOKE "pg_write_all_data" FROM "app"`,
		`REVOKE "app_owner" FROM "app"`,
	}
	if !reflect.DeepEqual(stmts, want) {
		t.Fatalf("statements =\n%s\nwant\n%s", strings.Join(stmts, "\n"), strings.Join(want, "\n"))
	}

	unmanaged := inSync
	unmanaged.comment = ""
	var errUnmanaged errUnmanagedRole
	if _, err := planRole("app", grants, "pw", &unmanaged, noVerifier); !errors.As(err, &errUnmanaged) {
		t.Fatalf("unmanaged: err = %v", err)
	}
}

func TestSyncUsers(t *testing.T) {
	s := &fakeSession{state: map[string]*roleState{
		"other": {login: true, comment: "created by a DBA"},
	}}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"),
		passwordSecret("app-credentials", "password", "pw-app"),
		passwordSecret("byo", "secret", "pw-byo"),
		passwordSecret("empty", "password", ""),
	)
	read := []engine.RoleGrant{{Role: "pg_read_all_data"}}
	users := []engine.DesiredUser{
		{Username: "app", Roles: read, Password: engine.SecretKeyRef{Name: "app-credentials", Key: "password"}},
		{Username: "byo", Roles: read, Password: engine.SecretKeyRef{Name: "byo", Key: "secret"}},
		{Username: "nosecret", Roles: read, Password: engine.SecretKeyRef{Name: "missing", Key: "password"}},
		{Username: "nokey", Roles: read, Password: engine.SecretKeyRef{Name: "empty", Key: "password"}},
		{Username: "other", Roles: read, Password: engine.SecretKeyRef{Name: "app-credentials", Key: "password"}},
	}
	if err := e.SyncUsers(context.Background(), target, users); err != nil {
		t.Fatal(err)
	}
	if !s.closed {
		t.Error("session not closed")
	}
	if len(s.executed) != 2 || !strings.HasPrefix(s.executed[0][0], `CREATE ROLE "app" `) || !strings.HasPrefix(s.executed[1][0], `CREATE ROLE "byo" `) {
		t.Fatalf("executed = %v", s.executed)
	}
	verifier := strings.TrimSuffix(strings.SplitN(s.executed[1][0], "PASSWORD '", 2)[1], "'")
	if !verifierMatches(verifier, "pw-byo") {
		t.Fatalf("byo password not applied: %q", s.executed[1][0])
	}

	ctx := context.Background()
	for username, want := range map[string]string{
		"app":      "applying the user in PostgreSQL",
		"nosecret": `password Secret "missing" not found`,
		"nokey":    `password Secret "empty" has no "password" key`,
		"other":    `user "other" already exists in the database and isn't managed by OpenEverest`,
	} {
		if got := e.PendingReason(ctx, target, username); got != want {
			t.Errorf("%s: pending reason = %q, want %q", username, got, want)
		}
	}
}

func TestSyncUsersPostgresError(t *testing.T) {
	s := &fakeSession{execErr: &pgconn.PgError{Message: "permission denied to grant role", Detail: "line one\nline two"}}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"), passwordSecret("p", "password", "pw"))
	users := []engine.DesiredUser{{Username: "app", Roles: []engine.RoleGrant{{Role: "pg_monitor"}}, Password: engine.SecretKeyRef{Name: "p", Key: "password"}}}
	if err := e.SyncUsers(context.Background(), target, users); err != nil {
		t.Fatal(err)
	}
	want := "PostgreSQL can't apply the user: permission denied to grant role: line one; line two"
	if got := e.PendingReason(context.Background(), target, "app"); got != want {
		t.Fatalf("pending reason = %q, want %q", got, want)
	}

	s.execErr = errors.New("connection reset")
	if err := e.SyncUsers(context.Background(), target, users); err == nil {
		t.Fatal("connection error not returned")
	}
}

func TestSyncUsersReservedBySpec(t *testing.T) {
	spec := providerSpec()
	spec["users"] = append(spec["users"].([]any), map[string]any{"name": "app_owner"})
	s := &fakeSession{}
	e := newTestEngine(s, pgCluster(spec, "ready"), passwordSecret("p", "password", "pw"))
	users := []engine.DesiredUser{{Username: "app_owner", Roles: []engine.RoleGrant{{Role: "pg_monitor"}}, Password: engine.SecretKeyRef{Name: "p", Key: "password"}}}
	if err := e.SyncUsers(context.Background(), target, users); err != nil {
		t.Fatal(err)
	}
	if len(s.executed) != 0 {
		t.Fatalf("executed = %v", s.executed)
	}
	if got := e.PendingReason(context.Background(), target, "app_owner"); !strings.Contains(got, "reserved for the operator") {
		t.Fatalf("pending reason = %q", got)
	}
}

func TestNotReady(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine(&fakeSession{}, pgCluster(providerSpec(), "initializing"))
	if _, err := e.ListUsers(ctx, target); !errors.Is(err, engine.ErrNotReady) {
		t.Fatalf("initializing: err = %v", err)
	}
	e = newTestEngine(&fakeSession{}, nil)
	if _, err := e.ListUsers(ctx, target); !errors.Is(err, engine.ErrNotReady) {
		t.Fatalf("missing cluster: err = %v", err)
	}
}

func TestListUsers(t *testing.T) {
	spec := providerSpec()
	spec["users"] = append(spec["users"].([]any), map[string]any{"name": "everest-monitor"}, map[string]any{"name": "app_owner"})
	s := &fakeSession{dbRoles: []dbRole{
		{name: "_crunchypgbouncer"},
		{name: "app", memberOf: []string{"pg_read_all_data"}},
		{name: "everest-monitor"},
		{name: "postgres"},
	}}
	e := newTestEngine(s, pgCluster(spec, "ready"))
	users, err := e.ListUsers(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.User{
		{Username: "_crunchypgbouncer", Roles: []engine.RoleGrant{}, System: true},
		{Username: "app", Roles: []engine.RoleGrant{{Role: "pg_read_all_data"}}},
		{Username: "everest-monitor", Roles: []engine.RoleGrant{}, System: true},
		{Username: "postgres", Roles: []engine.RoleGrant{}, System: true},
		{Username: "app_owner", Roles: []engine.RoleGrant{}, System: true},
	}
	if !reflect.DeepEqual(users, want) {
		t.Fatalf("users =\n%+v\nwant\n%+v", users, want)
	}
}

func TestDropUser(t *testing.T) {
	ctx := context.Background()
	s := &fakeSession{state: map[string]*roleState{
		"app":   {login: true, comment: managedComment},
		"other": {login: true},
	}}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"))

	for _, username := range []string{"missing", "other", "postgres"} {
		if err := e.DropUser(ctx, target, username); err != nil {
			t.Fatalf("%s: %v", username, err)
		}
	}
	if len(s.executed) != 0 {
		t.Fatalf("dropped a role the plugin doesn't manage: %v", s.executed)
	}

	if err := e.DropUser(ctx, target, "app"); err != nil {
		t.Fatal(err)
	}
	if want := [][]string{{`DROP ROLE IF EXISTS "app"`}}; !reflect.DeepEqual(s.executed, want) {
		t.Fatalf("executed = %v", s.executed)
	}

	s.execErr = &pgconn.PgError{Message: `role "app" cannot be dropped because some objects depend on it`, Detail: "owner of table orders"}
	err := e.DropUser(ctx, target, "app")
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), "owner of table orders") {
		t.Fatalf("err = %v, want ErrInProgress with the reason", err)
	}
}

func TestConnectionURI(t *testing.T) {
	ctx := context.Background()
	e := newTestEngine(&fakeSession{}, pgCluster(providerSpec(), "ready"))
	uri, err := e.ConnectionURI(ctx, target, "app", "p@ss/word")
	if err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(uri)
	if err != nil {
		t.Fatal(err)
	}
	if pw, _ := u.User.Password(); u.Host != "pg-pgbouncer.ns.svc:5432" || u.Path != "/postgres" || pw != "p@ss/word" || u.Query().Get("sslmode") != "require" {
		t.Fatalf("uri = %s", uri)
	}

	spec := providerSpec()
	spec["proxy"] = map[string]any{"pgBouncer": map[string]any{"replicas": int64(0)}}
	e = newTestEngine(&fakeSession{}, pgCluster(spec, "ready"))
	uri, err = e.ConnectionURI(ctx, target, "app", "pw")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(uri, "@pg-primary.ns.svc:5432/postgres") {
		t.Fatalf("uri = %s", uri)
	}
}

func TestNormalizeDatabaseRoles(t *testing.T) {
	got, err := normalizeRoles([]engine.RoleGrant{
		{Role: "read", Database: "orders"},
		{Role: "read", Database: "orders"},
		{Role: "readWrite", Database: "billing"},
		{Role: "pg_monitor", Database: "ignored"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.RoleGrant{{Role: "read", Database: "orders"}, {Role: "readWrite", Database: "billing"}, {Role: "pg_monitor"}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("normalized = %+v, want %+v", got, want)
	}
	for name, grants := range map[string][]engine.RoleGrant{
		"no database":      {{Role: "read"}},
		"invalid database": {{Role: "readWrite", Database: "Orders-DB"}},
	} {
		if _, err := normalizeRoles(grants); !errors.Is(err, engine.ErrInvalid) {
			t.Errorf("%s: err = %v, want ErrInvalid", name, err)
		}
	}
	if err := validateUser(engine.DesiredUser{Username: "oe_orders_read"}); !errors.Is(err, engine.ErrInvalid) {
		t.Errorf("group role name accepted as a username: %v", err)
	}
}

func TestSyncUsersDatabaseRoles(t *testing.T) {
	s := &fakeSession{state: map[string]*roleState{}}
	e := newTestEngine(s, pgCluster(providerSpec(), "ready"), passwordSecret("p", "password", "pw"))
	users := []engine.DesiredUser{{
		Username: "app",
		Roles:    []engine.RoleGrant{{Role: "read", Database: "orders"}, {Role: "readWrite", Database: "billing"}, {Role: "pg_monitor"}},
		Password: engine.SecretKeyRef{Name: "p", Key: "password"},
	}}
	if err := e.SyncUsers(context.Background(), target, users); err != nil {
		t.Fatal(err)
	}
	if len(s.executed) != 1 {
		t.Fatalf("executed = %q", s.executed)
	}
	want := []string{`GRANT "oe_orders_read" TO "app"`, `GRANT "oe_billing_readwrite" TO "app"`, `GRANT "pg_monitor" TO "app"`}
	if got := s.executed[0][2:]; !reflect.DeepEqual(got, want) {
		t.Fatalf("grants = %q, want %q", got, want)
	}
}

func TestListUsersDatabaseRoles(t *testing.T) {
	s := &fakeSession{
		dbRoles: []dbRole{
			{name: "app", memberOf: []string{"oe_orders_read", "pg_monitor", "oe_gone_read"}},
			{name: "oe_orders_read"},
		},
		dbs: []dbDatabase{{name: "orders"}, {name: "postgres"}},
	}
	users, err := newTestEngine(s, pgCluster(providerSpec(), "ready")).ListUsers(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.User{
		// A group of a database that no longer exists isn't a grant on it.
		{Username: "app", Roles: []engine.RoleGrant{{Role: "read", Database: "orders"}, {Role: "pg_monitor"}, {Role: "oe_gone_read"}}},
		{Username: "oe_orders_read", Roles: []engine.RoleGrant{}, System: true, Hidden: true},
		{Username: "postgres", Roles: []engine.RoleGrant{}, System: true},
	}
	if !reflect.DeepEqual(users, want) {
		t.Fatalf("users =\n%+v\nwant\n%+v", users, want)
	}
}
