package cnpg

import (
	"context"
	"fmt"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/engine/pgaccess"
)

var databaseResource = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "databases"}

var _ engine.PostgreSQLEngine = (*Engine)(nil)

// PostgreSQL's own databases, which CloudNativePG refuses to declare.
var reservedDatabases = []string{defaultDatabase, "template0", "template1"}

// databaseObjectName derives the Database resource of a database, like
// databaseRoleName does for users; the two kinds never share a name space.
func databaseObjectName(instance, database string) string {
	return databaseRoleName(instance, database)
}

// ValidateDatabase implements engine.DatabaseEngine.
func (e *Engine) ValidateDatabase(name string) error {
	if name == "" || len(name) > maxUsernameBytes {
		return fmt.Errorf("%w: PostgreSQL database names are 1 to %d characters", engine.ErrInvalidDatabase, maxUsernameBytes)
	}
	if slices.Contains(reservedDatabases, name) {
		return fmt.Errorf("%w: database %q is reserved", engine.ErrInvalidDatabase, name)
	}
	return nil
}

// ListDatabases implements engine.DatabaseEngine. The application database
// and databases that other Database resources declare are system databases,
// listed even before they exist, so the plugin never declares them.
func (e *Engine) ListDatabases(ctx context.Context, target engine.Target) ([]engine.Database, error) {
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
	dbs, err := s.databases(ctx)
	if err != nil {
		return nil, err
	}
	reserved, err := e.reservedDatabases(ctx, c)
	if err != nil {
		return nil, err
	}
	out := make([]engine.Database, 0, len(dbs))
	listed := map[string]bool{}
	for _, d := range dbs {
		out = append(out, engine.Database{Name: d.name, Owner: d.owner, System: d.template || reserved[d.name]})
		listed[d.name] = true
	}
	for name := range reserved {
		if !listed[name] {
			out = append(out, engine.Database{Name: name, System: true})
		}
	}
	return out, nil
}

// reservedDatabases are PostgreSQL's own databases, the application
// database and those declared by Database resources the plugin didn't create.
func (e *Engine) reservedDatabases(ctx context.Context, c cluster) (map[string]bool, error) {
	reserved := map[string]bool{c.database: true}
	for _, name := range reservedDatabases {
		reserved[name] = true
	}
	list, err := e.dyn.Resource(databaseResource).Namespace(c.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list Databases: %w", err)
	}
	for _, item := range list.Items {
		if _, ours := item.GetLabels()[instanceLabel]; ours {
			continue
		}
		if clusterName, _, _ := unstructured.NestedString(item.Object, "spec", "cluster", "name"); clusterName != c.name {
			continue
		}
		if name, _, _ := unstructured.NestedString(item.Object, "spec", "name"); name != "" {
			reserved[name] = true
		}
	}
	return reserved, nil
}

// SyncDatabase implements engine.DatabaseEngine with a Database resource and
// a DatabaseRole per group role, owned by the Cluster and retained when
// deleted unless ReleaseDatabase drops them. CloudNativePG doesn't declare
// privileges, so the plugin then grants the group roles their access in the
// database as its owner.
func (e *Engine) SyncDatabase(ctx context.Context, target engine.Target, want engine.DesiredDatabase) error {
	if err := e.ValidateDatabase(want.Name); err != nil {
		return err
	}
	c, err := e.resolve(ctx, target)
	if err != nil {
		return err
	}
	reserved, err := e.reservedDatabases(ctx, c)
	if err != nil {
		return err
	}
	if reserved[want.Name] {
		return fmt.Errorf("%w: database %q is reserved for the operator", engine.ErrInvalidDatabase, want.Name)
	}
	if err := e.checkExtensions(ctx, c, want.PostgreSQL); err != nil {
		return err
	}
	obj := databaseApplyConfiguration(c, want)
	if err := e.apply(ctx, obj); err != nil {
		return err
	}
	if err := e.databaseFailure(ctx, obj, want.PostgreSQL); err != nil {
		return err
	}
	for _, access := range pgaccess.AccessRoles {
		if err := e.apply(ctx, groupRoleApplyConfiguration(c, pgaccess.GroupRole(want.Name, access))); err != nil {
			return err
		}
	}
	return e.grantAccess(ctx, c, want)
}

// groupRoleApplyConfiguration declares a group role: no login, no password,
// retained when the DatabaseRole goes away unless the database is dropped.
func groupRoleApplyConfiguration(c cluster, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databaseRoleResource.GroupVersion().String(),
		"kind":       "DatabaseRole",
		"metadata": map[string]any{
			"name":      databaseRoleName(c.name, name),
			"namespace": c.namespace,
			"labels":    map[string]any{instanceLabel: c.name},
			"ownerReferences": []any{map[string]any{
				"apiVersion": clusterResource.GroupVersion().String(),
				"kind":       "Cluster",
				"name":       c.name,
				"uid":        string(c.uid),
			}},
		},
		"spec": map[string]any{
			"cluster":                   map[string]any{"name": c.name},
			"name":                      name,
			"comment":                   roleComment,
			"login":                     false,
			"databaseRoleReclaimPolicy": reclaimRetain,
		},
	}}
}

// grantAccess gives the group roles their access, connected to the database
// as its owner. Until CloudNativePG has created the database, its owner's
// password and the group roles, the server's error is reported as ErrInProgress.
func (e *Engine) grantAccess(ctx context.Context, c cluster, want engine.DesiredDatabase) error {
	secret, err := e.kube.CoreV1().Secrets(c.namespace).Get(ctx, want.OwnerPassword.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return fmt.Errorf("%w: the owner's password Secret %q is missing", engine.ErrInProgress, want.OwnerPassword.Name)
	}
	if err != nil {
		return fmt.Errorf("get owner password secret: %w", err)
	}
	password := string(secret.Data[want.OwnerPassword.Key])
	if password == "" {
		return fmt.Errorf("%w: the owner's password Secret %q has no %q key", engine.ErrInProgress, want.OwnerPassword.Name, want.OwnerPassword.Key)
	}
	s, err := e.dialAs(ctx, c, want.Name, want.Owner, password)
	if err != nil {
		return inProgress(err, "can't connect to the database as its owner")
	}
	defer s.close(ctx)
	read, readWrite := pgaccess.GroupRole(want.Name, pgaccess.RoleRead), pgaccess.GroupRole(want.Name, pgaccess.RoleReadWrite)
	schemas, objects, err := s.grantTargets(ctx, read, readWrite)
	if err != nil {
		return inProgress(err, "PostgreSQL can't list the database's objects")
	}
	connect := pgaccess.Connect{Restricted: want.RestrictConnect}
	if connect.Restricted {
		if connect.Roles, err = s.existingRoles(ctx, connectRoles); err != nil {
			return inProgress(err, "PostgreSQL can't list the roles that connect to every database")
		}
	}
	if err := s.exec(ctx, pgaccess.PrivilegeStatements(want.Name, want.Owner, schemas, objects, connect)); err != nil {
		return inProgress(err, "PostgreSQL can't grant access to the database")
	}
	return nil
}

// connectRoles are the operator's roles that connect to every database: the
// metrics exporter runs per-database queries, and Poolers authenticate users.
var connectRoles = []string{"cnpg_pooler_pgbouncer", "cnpg_metrics_exporter"}

// inProgress reports a server error as ErrInProgress with what failed;
// other errors are returned as they are, to be retried.
func inProgress(err error, what string) error {
	if reason := pgaccess.ServerReason(err); reason != "" {
		return fmt.Errorf("%w: %s: %s", engine.ErrInProgress, what, reason)
	}
	return err
}

func databaseApplyConfiguration(c cluster, want engine.DesiredDatabase) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databaseResource.GroupVersion().String(),
		"kind":       "Database",
		"metadata": map[string]any{
			"name":      databaseObjectName(c.name, want.Name),
			"namespace": c.namespace,
			"labels":    map[string]any{instanceLabel: c.name},
			"ownerReferences": []any{map[string]any{
				"apiVersion": clusterResource.GroupVersion().String(),
				"kind":       "Cluster",
				"name":       c.name,
				"uid":        string(c.uid),
			}},
		},
		"spec": databaseSpec(c, want),
	}}
}

// databaseSpec is the Database's spec. Extensions to drop stay declared
// with ensure absent until CloudNativePG has dropped them.
func databaseSpec(c cluster, want engine.DesiredDatabase) map[string]any {
	spec := map[string]any{
		"cluster":               map[string]any{"name": c.name},
		"name":                  want.Name,
		"owner":                 want.Owner,
		"ensure":                "present",
		"databaseReclaimPolicy": reclaimRetain,
	}
	if pg := want.PostgreSQL; pg != nil && len(pg.Extensions)+len(pg.DropExtensions) > 0 {
		var exts []any
		for _, name := range pg.Extensions {
			exts = append(exts, map[string]any{"name": name, "ensure": "present"})
		}
		for _, name := range pg.DropExtensions {
			exts = append(exts, map[string]any{"name": name, "ensure": "absent"})
		}
		spec["extensions"] = exts
	}
	return spec
}

// checkExtensions refuses extensions the policy doesn't allow. Those the
// instance doesn't have are left to CloudNativePG, which reports them.
func (e *Engine) checkExtensions(ctx context.Context, c cluster, pg *engine.PostgreSQLDatabase) error {
	if pg == nil || len(pg.Extensions) == 0 {
		return nil
	}
	available, err := e.availableExtensions(ctx, c)
	if err != nil {
		return err
	}
	if forbidden, _ := e.extensions.Check(pg.Extensions, available); len(forbidden) > 0 {
		return fmt.Errorf("%w: extensions %s can't be created: the plugin's policy doesn't allow them", engine.ErrInvalidDatabase, strings.Join(forbidden, ", "))
	}
	return nil
}

func (e *Engine) availableExtensions(ctx context.Context, c cluster) ([]engine.PostgreSQLExtension, error) {
	if !c.ready {
		return nil, fmt.Errorf("%w: PostgreSQL cluster is %q", engine.ErrNotReady, c.phase)
	}
	s, err := e.dial(ctx, c)
	if err != nil {
		return nil, err
	}
	defer s.close(ctx)
	return s.availableExtensions(ctx)
}

// AvailableExtensions implements engine.PostgreSQLEngine.
func (e *Engine) AvailableExtensions(ctx context.Context, target engine.Target) ([]engine.PostgreSQLExtension, error) {
	c, err := e.resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	available, err := e.availableExtensions(ctx, c)
	if err != nil {
		return nil, err
	}
	return e.extensions.Filter(available), nil
}

// databaseFailure returns CloudNativePG's error for the Database, as
// ErrInProgress, once it has reconciled the current spec.
// With extensions to create or drop, it also waits until CloudNativePG has
// applied them, so the controller only records them once they are.
func (e *Engine) databaseFailure(ctx context.Context, applied *unstructured.Unstructured, pg *engine.PostgreSQLDatabase) error {
	obj, err := e.dyn.Resource(databaseResource).Namespace(applied.GetNamespace()).Get(ctx, applied.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil
	}
	// CloudNativePG only records observedGeneration when it applies a spec, so
	// a failure is always from its latest attempt, whatever the generation.
	if reasons := extensionFailures(obj); len(reasons) > 0 {
		return fmt.Errorf("%w: CloudNativePG can't apply the database's extensions: %s", engine.ErrInProgress, strings.Join(reasons, "; "))
	}
	if reason := failureReason(obj); reason != "" {
		return fmt.Errorf("%w: CloudNativePG can't apply the database: %s", engine.ErrInProgress, reason)
	}
	observed, _, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
	if observed != obj.GetGeneration() && pg != nil && len(pg.Extensions)+len(pg.DropExtensions) > 0 {
		return fmt.Errorf("%w: CloudNativePG is applying the database's extensions", engine.ErrInProgress)
	}
	return nil
}

// extensionFailures are CloudNativePG's errors for the Database's extensions.
func extensionFailures(obj *unstructured.Unstructured) []string {
	statuses, _, _ := unstructured.NestedSlice(obj.Object, "status", "extensions")
	var reasons []string
	for _, item := range statuses {
		st, ok := item.(map[string]any)
		if !ok || st["applied"] == true {
			continue
		}
		name, _ := st["name"].(string)
		message, _ := st["message"].(string)
		reasons = append(reasons, name+": "+message)
	}
	return reasons
}

// ReleaseDatabase implements engine.DatabaseEngine by deleting the Database
// resource, then the group roles' DatabaseRoles, with the delete reclaim
// policy when the database is dropped: the roles can only be dropped once
// the database holding their privileges is gone.
func (e *Engine) ReleaseDatabase(ctx context.Context, target engine.Target, name string, drop bool) error {
	if err := e.releaseObject(ctx, databaseResource, target.Namespace, databaseObjectName(target.Instance, name),
		"databaseReclaimPolicy", drop, "database"); err != nil {
		return err
	}
	for _, access := range pgaccess.AccessRoles {
		if err := e.releaseObject(ctx, databaseRoleResource, target.Namespace, databaseRoleName(target.Instance, pgaccess.GroupRole(name, access)),
			"databaseRoleReclaimPolicy", drop, "database's roles"); err != nil {
			return err
		}
	}
	return nil
}
