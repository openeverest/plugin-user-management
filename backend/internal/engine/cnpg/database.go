package cnpg

import (
	"context"
	"fmt"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var databaseResource = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "databases"}

var _ engine.DatabaseEngine = (*Engine)(nil)

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

// SyncDatabase implements engine.DatabaseEngine with a Database resource,
// owned by the Cluster and retained when deleted unless ReleaseDatabase drops it.
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
	obj := databaseApplyConfiguration(c, want)
	if err := e.apply(ctx, obj); err != nil {
		return err
	}
	return e.databaseFailure(ctx, obj)
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
		"spec": map[string]any{
			"cluster":               map[string]any{"name": c.name},
			"name":                  want.Name,
			"owner":                 want.Owner,
			"ensure":                "present",
			"databaseReclaimPolicy": reclaimRetain,
		},
	}}
}

// databaseFailure returns CloudNativePG's error for the Database, as
// ErrInProgress, once it has reconciled the current spec.
func (e *Engine) databaseFailure(ctx context.Context, applied *unstructured.Unstructured) error {
	obj, err := e.dyn.Resource(databaseResource).Namespace(applied.GetNamespace()).Get(ctx, applied.GetName(), metav1.GetOptions{})
	if err != nil {
		return nil
	}
	observed, _, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
	if observed != obj.GetGeneration() {
		return nil
	}
	if reason := failureReason(obj); reason != "" {
		return fmt.Errorf("%w: CloudNativePG can't apply the database: %s", engine.ErrInProgress, reason)
	}
	return nil
}

// ReleaseDatabase implements engine.DatabaseEngine by deleting the Database
// resource, with the delete reclaim policy when the database is dropped.
func (e *Engine) ReleaseDatabase(ctx context.Context, target engine.Target, name string, drop bool) error {
	return e.releaseObject(ctx, databaseResource, target.Namespace, databaseObjectName(target.Instance, name),
		"databaseReclaimPolicy", drop, "database")
}
