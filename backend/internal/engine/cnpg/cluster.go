package cnpg

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var clusterResource = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}

const (
	postgresPort    = "5432"
	defaultDatabase = "postgres"
	conditionReady  = "Ready"
	operatorPrefix  = "cnpg_"
)

// Bootstrap methods that create the application database and its owner, in
// the order CloudNativePG looks them up.
var bootstrapMethods = []string{"initdb", "recovery", "pg_basebackup"}

// cluster is what the plugin needs from a CloudNativePG Cluster. The
// provider names the Cluster after the Instance.
type cluster struct {
	name      string
	namespace string
	uid       types.UID
	ready     bool
	phase     string
	// host of the read-write service, which always points at the primary.
	host     string
	database string
	// systemRoles are created by the operator or declared in the Cluster spec.
	systemRoles  map[string]bool
	managedRoles []string
	caSecret     string
	// loginSecrets hold credentials to read the role catalog with, in order of preference.
	loginSecrets []string
}

func (c cluster) isSystemRole(name string) bool {
	return c.systemRoles[name] || strings.HasPrefix(name, operatorPrefix)
}

func clusterFrom(obj *unstructured.Unstructured) cluster {
	c := cluster{
		name:        obj.GetName(),
		namespace:   obj.GetNamespace(),
		uid:         obj.GetUID(),
		host:        fmt.Sprintf("%s-rw.%s.svc", obj.GetName(), obj.GetNamespace()),
		database:    defaultDatabase,
		systemRoles: map[string]bool{"postgres": true, "streaming_replica": true},
	}
	c.phase, _, _ = unstructured.NestedString(obj.Object, "status", "phase")
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, item := range conditions {
		if condition, ok := item.(map[string]any); ok && condition["type"] == conditionReady {
			c.ready = condition["status"] == string(metav1.ConditionTrue)
		}
	}

	appSecret := c.name + "-app"
	for _, method := range bootstrapMethods {
		database, _, _ := unstructured.NestedString(obj.Object, "spec", "bootstrap", method, "database")
		if database == "" {
			continue
		}
		c.database = database
		if owner, _, _ := unstructured.NestedString(obj.Object, "spec", "bootstrap", method, "owner"); owner != "" {
			c.systemRoles[owner] = true
		}
		if secret, _, _ := unstructured.NestedString(obj.Object, "spec", "bootstrap", method, "secret", "name"); secret != "" {
			appSecret = secret
		}
		break
	}
	superuserSecret, _, _ := unstructured.NestedString(obj.Object, "spec", "superuserSecret", "name")
	if superuserSecret == "" {
		superuserSecret = c.name + "-superuser"
	}
	c.loginSecrets = []string{appSecret, superuserSecret}

	c.caSecret, _, _ = unstructured.NestedString(obj.Object, "status", "certificates", "serverCASecret")
	if c.caSecret == "" {
		c.caSecret = c.name + "-ca"
	}

	roles, _, _ := unstructured.NestedSlice(obj.Object, "spec", "managed", "roles")
	for _, item := range roles {
		if role, ok := item.(map[string]any); ok {
			if name, ok := role["name"].(string); ok && name != "" {
				c.systemRoles[name] = true
				c.managedRoles = append(c.managedRoles, name)
			}
		}
	}
	return c
}

func (e *Engine) resolve(ctx context.Context, target engine.Target) (cluster, error) {
	obj, err := e.dyn.Resource(clusterResource).Namespace(target.Namespace).Get(ctx, target.Instance, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return cluster{}, fmt.Errorf("%w: PostgreSQL cluster not found", engine.ErrNotReady)
	}
	if err != nil {
		return cluster{}, fmt.Errorf("get CloudNativePG cluster: %w", err)
	}
	return clusterFrom(obj), nil
}
