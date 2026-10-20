package mongodb

import (
	"context"
	"fmt"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var psmdbResource = schema.GroupVersionResource{Group: "psmdb.percona.com", Version: "v1", Resource: "perconaservermongodbs"}

const (
	defaultPort      = "27017"
	psmdbStateReady  = "ready"
	tlsModeRequire   = "requireTLS"
	userAdminUserKey = "MONGODB_USER_ADMIN_USER"
	userAdminPassKey = "MONGODB_USER_ADMIN_PASSWORD"
	caCertKey        = "ca.crt"
)

// cluster is what the plugin needs to reach a PerconaServerMongoDB cluster.
type cluster struct {
	// host:port of the replica set or mongos service.
	host        string
	requireTLS  bool
	caPEM       []byte
	adminUser   string
	adminPass   string
	systemUsers map[string]bool
}

// clusterResolver reads a PerconaServerMongoDB cluster's connection settings.
// The provider names the PSMDB custom resource after the Instance.
type clusterResolver struct {
	kube kubernetes.Interface
	dyn  dynamic.Interface
}

func (r *clusterResolver) resolve(ctx context.Context, target engine.Target) (cluster, error) {
	psmdb, err := r.dyn.Resource(psmdbResource).Namespace(target.Namespace).Get(ctx, target.Instance, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return cluster{}, fmt.Errorf("%w: MongoDB cluster not found", engine.ErrNotReady)
	}
	if err != nil {
		return cluster{}, fmt.Errorf("get PerconaServerMongoDB: %w", err)
	}

	state, _, _ := unstructured.NestedString(psmdb.Object, "status", "state")
	host, _, _ := unstructured.NestedString(psmdb.Object, "status", "host")
	if state != psmdbStateReady || host == "" {
		return cluster{}, fmt.Errorf("%w: MongoDB cluster is %q", engine.ErrNotReady, state)
	}
	if !strings.Contains(host, ":") {
		host += ":" + defaultPort
	}
	c := cluster{host: host}

	usersSecret, _, _ := unstructured.NestedString(psmdb.Object, "spec", "secrets", "users")
	if usersSecret == "" {
		usersSecret = target.Instance + "-secrets" // operator default
	}
	secret, err := r.kube.CoreV1().Secrets(target.Namespace).Get(ctx, usersSecret, metav1.GetOptions{})
	if err != nil {
		return cluster{}, fmt.Errorf("get users secret %q: %w", usersSecret, err)
	}
	c.adminUser = string(secret.Data[userAdminUserKey])
	c.adminPass = string(secret.Data[userAdminPassKey])
	if c.adminUser == "" || c.adminPass == "" {
		return cluster{}, fmt.Errorf("users secret %q has no user admin credentials", usersSecret)
	}
	c.systemUsers = map[string]bool{}
	for key, value := range secret.Data {
		if strings.HasPrefix(key, "MONGODB_") && strings.HasSuffix(key, "_USER") && len(value) > 0 {
			c.systemUsers[string(value)] = true
		}
	}

	mode, _, _ := unstructured.NestedString(psmdb.Object, "spec", "tls", "mode")
	if mode == tlsModeRequire {
		c.requireTLS = true
		sslSecret, _, _ := unstructured.NestedString(psmdb.Object, "spec", "secrets", "ssl")
		if sslSecret == "" {
			sslSecret = target.Instance + "-ssl" // operator default
		}
		ssl, err := r.kube.CoreV1().Secrets(target.Namespace).Get(ctx, sslSecret, metav1.GetOptions{})
		if err != nil {
			return cluster{}, fmt.Errorf("get TLS secret %q: %w", sslSecret, err)
		}
		c.caPEM = ssl.Data[caCertKey]
	}
	return c, nil
}
