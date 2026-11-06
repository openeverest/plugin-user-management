package perconapg

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var clusterResource = schema.GroupVersionResource{Group: "pgv2.percona.com", Version: "v2", Resource: "perconapgclusters"}

const (
	defaultPort     = 5432
	defaultDatabase = "postgres"
	stateReady      = "ready"
	superuser       = "postgres"
	// The operator's internal roles (_crunchyrepl, _crunchypgbouncer, ...).
	operatorPrefix = "_crunchy"
)

// operatorRoles are created by the operator, or reserved for it, whether or
// not the PerconaPGCluster lists them in spec.users: "monitor" is the PMM
// user and "everest-monitor" the provider's metrics exporter user, which the
// operator would make a superuser and take over once declared.
var operatorRoles = []string{superuser, "monitor", "ccp_monitoring", "everest-monitor"}

// reservedDatabases are PostgreSQL's own databases.
var reservedDatabases = []string{defaultDatabase, "template0", "template1"}

// cluster is what the plugin needs from a PerconaPGCluster. The provider
// names the PerconaPGCluster after the Instance.
type cluster struct {
	name      string
	namespace string
	ready     bool
	state     string
	// host of the primary Service, which always points at the primary.
	host string
	port int64
	// pgBouncer is set when PgBouncer runs; clients connect through it.
	pgBouncer     bool
	pgBouncerHost string
	pgBouncerPort int64
	// systemRoles are created by the operator or declared in spec.users.
	systemRoles map[string]bool
	// specUsers are the users declared in spec.users, in order.
	specUsers []string
	// systemDatabases are PostgreSQL's own, and those the operator creates
	// for spec.users.
	systemDatabases map[string]bool
	// superuserSecret holds the postgres superuser's credentials.
	superuserSecret string
	// tlsSecret holds the server certificate and its CA (ca.crt).
	tlsSecret string
}

func (c cluster) isSystemRole(name string) bool {
	return c.systemRoles[name] || strings.HasPrefix(name, operatorPrefix)
}

// clientHost is where applications connect: PgBouncer when it runs, as in
// the Instance's own connection details, or else the primary.
func (c cluster) clientHost() (string, int64) {
	if c.pgBouncer {
		return c.pgBouncerHost, c.pgBouncerPort
	}
	return c.host, c.port
}

func clusterFrom(obj *unstructured.Unstructured) cluster {
	name, namespace := obj.GetName(), obj.GetNamespace()
	c := cluster{
		name:            name,
		namespace:       namespace,
		host:            fmt.Sprintf("%s-primary.%s.svc", name, namespace),
		port:            nestedPort(obj, "spec", "port"),
		pgBouncerHost:   fmt.Sprintf("%s-pgbouncer.%s.svc", name, namespace),
		systemRoles:     map[string]bool{},
		systemDatabases: map[string]bool{},
		superuserSecret: name + "-pguser-" + superuser,
		tlsSecret:       name + "-cluster-cert",
	}
	for _, r := range operatorRoles {
		c.systemRoles[r] = true
	}
	for _, d := range reservedDatabases {
		c.systemDatabases[d] = true
	}
	c.state, _, _ = unstructured.NestedString(obj.Object, "status", "state")
	c.ready = c.state == stateReady

	if bouncer, found, _ := unstructured.NestedMap(obj.Object, "spec", "proxy", "pgBouncer"); found && bouncer != nil {
		replicas, hasReplicas, _ := unstructured.NestedInt64(bouncer, "replicas")
		c.pgBouncer = !hasReplicas || replicas != 0
		c.pgBouncerPort = nestedPort(&unstructured.Unstructured{Object: bouncer}, "port")
	}

	users, _, _ := unstructured.NestedSlice(obj.Object, "spec", "users")
	for _, item := range users {
		user, ok := item.(map[string]any)
		if !ok {
			continue
		}
		name, _ := user["name"].(string)
		if name == "" {
			continue
		}
		c.systemRoles[name] = true
		c.specUsers = append(c.specUsers, name)
		if secret, _ := user["secretName"].(string); name == superuser && secret != "" {
			c.superuserSecret = secret
		}
		databases, _ := user["databases"].([]any)
		for _, d := range databases {
			if db, ok := d.(string); ok && db != "" {
				c.systemDatabases[db] = true
			}
		}
	}

	if secret, _, _ := unstructured.NestedString(obj.Object, "spec", "secrets", "customTLSSecret", "name"); secret != "" {
		c.tlsSecret = secret
	}
	return c
}

func nestedPort(obj *unstructured.Unstructured, fields ...string) int64 {
	if port, found, _ := unstructured.NestedInt64(obj.Object, fields...); found && port > 0 {
		return port
	}
	return defaultPort
}

func (e *Engine) resolve(ctx context.Context, target engine.Target) (cluster, error) {
	obj, err := e.dyn.Resource(clusterResource).Namespace(target.Namespace).Get(ctx, target.Instance, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return cluster{}, fmt.Errorf("%w: PostgreSQL cluster not found", engine.ErrNotReady)
	}
	if err != nil {
		return cluster{}, fmt.Errorf("get PerconaPGCluster: %w", err)
	}
	return clusterFrom(obj), nil
}
