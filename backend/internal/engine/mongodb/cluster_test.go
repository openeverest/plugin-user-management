package mongodb

import (
	"context"
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

func psmdb(spec, status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "psmdb.percona.com/v1",
		"kind":       "PerconaServerMongoDB",
		"metadata":   map[string]any{"name": "mongo", "namespace": "ns"},
		"spec":       spec,
		"status":     status,
	}}
}

func secret(name string, data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func newResolver(objects []runtime.Object, secrets ...runtime.Object) *clusterResolver {
	scheme := runtime.NewScheme()
	gvrs := map[schema.GroupVersionResource]string{psmdbResource: "PerconaServerMongoDBList"}
	return &clusterResolver{
		kube: kubefake.NewClientset(secrets...),
		dyn:  dynamicfake.NewSimpleDynamicClientWithCustomListKinds(scheme, gvrs, objects...),
	}
}

var usersSecretData = map[string]string{
	"MONGODB_USER_ADMIN_USER":          "userAdmin",
	"MONGODB_USER_ADMIN_PASSWORD":      "uapass",
	"MONGODB_CLUSTER_MONITOR_USER":     "clusterMonitor",
	"MONGODB_CLUSTER_MONITOR_PASSWORD": "x",
	"MONGODB_DATABASE_ADMIN_USER":      "databaseAdmin",
	"PMM_SERVER_TOKEN":                 "token",
}

func TestResolve(t *testing.T) {
	r := newResolver(
		[]runtime.Object{psmdb(
			map[string]any{"secrets": map[string]any{"users": "everest-secrets-mongo"}},
			map[string]any{"state": "ready", "host": "mongo-rs0.ns.svc.cluster.local"},
		)},
		secret("everest-secrets-mongo", usersSecretData),
	)
	c, err := r.resolve(context.Background(), engine.Target{Namespace: "ns", Instance: "mongo"})
	if err != nil {
		t.Fatal(err)
	}
	if c.host != "mongo-rs0.ns.svc.cluster.local:27017" {
		t.Errorf("host = %q", c.host)
	}
	if c.adminUser != "userAdmin" || c.adminPass != "uapass" || c.requireTLS {
		t.Errorf("cluster = %+v", c)
	}
	for _, u := range []string{"userAdmin", "clusterMonitor", "databaseAdmin"} {
		if !c.systemUsers[u] {
			t.Errorf("%s not a system user", u)
		}
	}
	if len(c.systemUsers) != 3 {
		t.Errorf("system users = %v", c.systemUsers)
	}
}

func TestResolveRequireTLS(t *testing.T) {
	r := newResolver(
		[]runtime.Object{psmdb(
			map[string]any{"tls": map[string]any{"mode": "requireTLS"}},
			map[string]any{"state": "ready", "host": "mongo-mongos.ns.svc.cluster.local:27018"},
		)},
		secret("mongo-secrets", usersSecretData),
		secret("mongo-ssl", map[string]string{"ca.crt": "PEM"}),
	)
	c, err := r.resolve(context.Background(), engine.Target{Namespace: "ns", Instance: "mongo"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.requireTLS || string(c.caPEM) != "PEM" || c.host != "mongo-mongos.ns.svc.cluster.local:27018" {
		t.Errorf("cluster = %+v", c)
	}
}

func TestResolveNotReady(t *testing.T) {
	for name, objects := range map[string][]runtime.Object{
		"missing":      nil,
		"initializing": {psmdb(map[string]any{}, map[string]any{"state": "initializing"})},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newResolver(objects).resolve(context.Background(), engine.Target{Namespace: "ns", Instance: "mongo"})
			if !errors.Is(err, engine.ErrNotReady) {
				t.Fatalf("err = %v, want ErrNotReady", err)
			}
		})
	}
}
