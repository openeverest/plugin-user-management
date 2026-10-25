package kserve

import (
	"context"
	"errors"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"

	"github.com/openeverest/plugin-user-management/backend/internal/controller"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var target = engine.Target{Namespace: "ns", Instance: "qwen"}

const validKey = "oe_sk_0123456789abcdefghijklmnopqrstuvwxyz"

func connSecret(data map[string]string) *corev1.Secret {
	s := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "qwen-conn", Namespace: "ns"}, Data: map[string][]byte{}}
	for k, v := range data {
		s.Data[k] = []byte(v)
	}
	return s
}

func gatewayConn() *corev1.Secret {
	return connSecret(map[string]string{
		"type": gatewayConnectionType, "uri": "https://llm.example.com/v1", "model": "qwen3-4b",
		"username": "k_instance", "password": "instance-key",
	})
}

func passwordSecret(name, value string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "ns"},
		Data:       map[string][]byte{"password": []byte(value)},
	}
}

func keySecret(name, kind, owner, models string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "ns",
			Labels:      map[string]string{keyLabel: "true", ownerKindLabel: kind, ownerNameLabel: owner},
			Annotations: map[string]string{keyIDAnnotation: "k_" + owner, modelsAnnotation: models},
		},
		Data: map[string][]byte{keyDataKey: []byte("x")},
	}
}

func newEngine(objects ...runtime.Object) (*Engine, *kubefake.Clientset) {
	instance := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "core.openeverest.io/v1alpha1",
		"kind":       "Instance",
		"metadata":   map[string]any{"name": "qwen", "namespace": "ns", "uid": "instance-uid"},
	}}
	kube := kubefake.NewClientset(objects...)
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme(), instance)
	return New(kube, dyn), kube
}

func getSecret(t *testing.T, kube *kubefake.Clientset, name string) *corev1.Secret {
	t.Helper()
	s, err := kube.CoreV1().Secrets("ns").Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get secret %s: %v", name, err)
	}
	return s
}

func TestInstanceLabelMatchesController(t *testing.T) {
	if instanceLabel != controller.InstanceLabel {
		t.Fatalf("instanceLabel = %q, want the controller's %q", instanceLabel, controller.InstanceLabel)
	}
}

func TestRolesAndUsers(t *testing.T) {
	e, _ := newEngine()
	roles, err := e.NormalizeRoles([]engine.RoleGrant{{Role: RoleInvoke, Database: "x"}, {Role: RoleInvoke}})
	if err != nil || len(roles) != 1 || roles[0] != (engine.RoleGrant{Role: RoleInvoke}) {
		t.Fatalf("NormalizeRoles = %v, %v", roles, err)
	}
	for _, grants := range [][]engine.RoleGrant{nil, {{Role: "readWrite"}}} {
		if _, err := e.NormalizeRoles(grants); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("NormalizeRoles(%v) error = %v, want ErrInvalid", grants, err)
		}
	}
	for _, name := range []string{"orders-app", "team.a", "alice_1"} {
		if err := e.ValidateUser(engine.DesiredUser{Username: name}); err != nil {
			t.Fatalf("ValidateUser(%q) = %v", name, err)
		}
	}
	for _, name := range []string{"", "alice@example.com", "trailing-", strings.Repeat("a", 64)} {
		if err := e.ValidateUser(engine.DesiredUser{Username: name}); !errors.Is(err, engine.ErrInvalid) {
			t.Fatalf("ValidateUser(%q) = %v, want ErrInvalid", name, err)
		}
	}
	if engine.CredentialKindOf(e) != engine.CredentialAPIKey {
		t.Fatalf("credential kind = %q", engine.CredentialKindOf(e))
	}
}

func TestKeys(t *testing.T) {
	e, _ := newEngine()
	key, err := engine.GeneratePassword(e)
	if err != nil || !strings.HasPrefix(key, keyPrefix) {
		t.Fatalf("GeneratePassword = %q, %v", key, err)
	}
	if err := engine.ValidatePassword(e, key); err != nil {
		t.Fatalf("generated key rejected: %v", err)
	}
	for _, bad := range []string{"short-password", strings.Repeat("a", 31), strings.Repeat("a", 129), strings.Repeat("a", 32) + " b"} {
		if err := e.ValidateKey(bad); err == nil {
			t.Fatalf("ValidateKey(%q) accepted", bad)
		}
	}
	id, err := newKeyID()
	if err != nil || !validKeyID.MatchString(id) || len(id) != 18 {
		t.Fatalf("newKeyID = %q, %v", id, err)
	}
	if name := keySecretName(engine.Target{Namespace: "ns", Instance: strings.Repeat("m", 300)}, "alice"); len(name) > 253 {
		t.Fatalf("key secret name too long: %d", len(name))
	}
}

func TestNotReady(t *testing.T) {
	cases := map[string]*corev1.Secret{
		"no connection details": nil,
		"not on the gateway":    connSecret(map[string]string{"type": "kserve", "uri": "http://svc"}),
		"keys disabled":         connSecret(map[string]string{"type": gatewayConnectionType, "uri": "http://gw/v1"}),
	}
	for name, conn := range cases {
		t.Run(name, func(t *testing.T) {
			var objects []runtime.Object
			if conn != nil {
				objects = append(objects, conn)
			}
			e, _ := newEngine(objects...)
			if _, err := e.ListUsers(context.Background(), target); !errors.Is(err, engine.ErrNotReady) {
				t.Fatalf("ListUsers error = %v, want ErrNotReady", err)
			}
			if err := e.SyncUsers(context.Background(), target, nil); !errors.Is(err, engine.ErrNotReady) {
				t.Fatalf("SyncUsers error = %v, want ErrNotReady", err)
			}
		})
	}
}

func TestSyncUsers(t *testing.T) {
	ctx := context.Background()
	e, kube := newEngine(gatewayConn(), passwordSecret("alice-credentials", validKey))
	alice := engine.DesiredUser{
		Username: "alice", Roles: []engine.RoleGrant{{Role: RoleInvoke}},
		Password: engine.SecretKeyRef{Name: "alice-credentials", Key: "password"},
	}
	if err := e.SyncUsers(ctx, target, []engine.DesiredUser{alice}); err != nil {
		t.Fatalf("SyncUsers: %v", err)
	}

	name := keySecretName(target, "alice")
	s := getSecret(t, kube, name)
	if string(s.Data[keyDataKey]) != validKey || len(s.Data) != 1 {
		t.Fatalf("key secret data = %v", s.Data)
	}
	wantLabels := map[string]string{
		keyLabel: "true", ownerKindLabel: ownerKindUser, ownerNameLabel: "alice",
		managedByLabel: managedBy, instanceLabel: "qwen",
	}
	for k, v := range wantLabels {
		if s.Labels[k] != v {
			t.Fatalf("label %s = %q, want %q", k, s.Labels[k], v)
		}
	}
	id := s.Annotations[keyIDAnnotation]
	if s.Annotations[modelsAnnotation] != "ns/qwen" || !validKeyID.MatchString(id) {
		t.Fatalf("annotations = %v", s.Annotations)
	}
	if len(s.OwnerReferences) != 1 || s.OwnerReferences[0].UID != "instance-uid" || s.OwnerReferences[0].Kind != "Instance" {
		t.Fatalf("owner references = %v", s.OwnerReferences)
	}

	t.Run("idempotent", func(t *testing.T) {
		kube.ClearActions()
		if err := e.SyncUsers(ctx, target, []engine.DesiredUser{alice}); err != nil {
			t.Fatalf("SyncUsers: %v", err)
		}
		for _, a := range kube.Actions() {
			if a.GetVerb() != "get" {
				t.Fatalf("unexpected %s on an unchanged key", a.GetVerb())
			}
		}
	})

	t.Run("password change rotates the key", func(t *testing.T) {
		rotated := validKey + "-rotated"
		if _, err := kube.CoreV1().Secrets("ns").Update(ctx, passwordSecret("alice-credentials", rotated), metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := e.SyncUsers(ctx, target, []engine.DesiredUser{alice}); err != nil {
			t.Fatalf("SyncUsers: %v", err)
		}
		s := getSecret(t, kube, name)
		if got := string(s.Data[keyDataKey]); got != rotated {
			t.Fatalf("api_key = %q, want %q", got, rotated)
		}
		if s.Annotations[keyIDAnnotation] != id {
			t.Fatalf("key id changed on rotation: %q, want %q", s.Annotations[keyIDAnnotation], id)
		}
	})

	t.Run("an unusable key removes the key secret", func(t *testing.T) {
		if _, err := kube.CoreV1().Secrets("ns").Update(ctx, passwordSecret("alice-credentials", "weak"), metav1.UpdateOptions{}); err != nil {
			t.Fatal(err)
		}
		if err := e.SyncUsers(ctx, target, []engine.DesiredUser{alice}); err != nil {
			t.Fatalf("SyncUsers: %v", err)
		}
		if _, err := kube.CoreV1().Secrets("ns").Get(ctx, name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
			t.Fatalf("key secret still exists: %v", err)
		}
		if reason := e.PendingReason(ctx, target, "alice"); !strings.Contains(reason, "not usable") {
			t.Fatalf("PendingReason = %q", reason)
		}
	})

	t.Run("a foreign secret with the key's name is not taken over", func(t *testing.T) {
		bobName := keySecretName(target, "bob")
		foreign := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: bobName, Namespace: "ns"}}
		if _, err := kube.CoreV1().Secrets("ns").Create(ctx, foreign, metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		if _, err := kube.CoreV1().Secrets("ns").Create(ctx, passwordSecret("bob-credentials", validKey), metav1.CreateOptions{}); err != nil {
			t.Fatal(err)
		}
		bob := engine.DesiredUser{Username: "bob", Password: engine.SecretKeyRef{Name: "bob-credentials", Key: "password"}}
		if err := e.SyncUsers(ctx, target, []engine.DesiredUser{bob}); err == nil || !strings.Contains(err.Error(), "not managed by the plugin") {
			t.Fatalf("SyncUsers error = %v", err)
		}
		if err := e.DropUser(ctx, target, "bob"); err != nil {
			t.Fatalf("DropUser: %v", err)
		}
		getSecret(t, kube, bobName)
	})
}

func TestListUsersAndDrop(t *testing.T) {
	ctx := context.Background()
	mine := keySecret(keySecretName(target, "alice"), ownerKindUser, "alice", "ns/qwen")
	mine.Labels[managedByLabel] = managedBy
	e, kube := newEngine(
		gatewayConn(),
		keySecret("qwen-ai-gateway-key", ownerKindInstance, "qwen", "ns/qwen"),
		mine,
		keySecret("handmade", "app", "batch-job", "ns/llama,ns/qwen"),
		keySecret("other-model", ownerKindUser, "carol", "ns/llama"),
		keySecret("no-owner", "", "", "ns/qwen"),
	)

	users, err := e.ListUsers(ctx, target)
	if err != nil {
		t.Fatalf("ListUsers: %v", err)
	}
	got := map[string]bool{}
	for _, u := range users {
		got[u.Username] = u.System
		if len(u.Roles) != 1 || u.Roles[0].Role != RoleInvoke {
			t.Fatalf("user %q roles = %v", u.Username, u.Roles)
		}
	}
	want := map[string]bool{"qwen": true, "alice": false, "batch-job": false, "no-owner": false}
	if len(got) != len(want) {
		t.Fatalf("users = %v, want %v", got, want)
	}
	for name, system := range want {
		if s, ok := got[name]; !ok || s != system {
			t.Fatalf("users = %v, want %v", got, want)
		}
	}

	if err := e.DropUser(ctx, target, "alice"); err != nil {
		t.Fatalf("DropUser: %v", err)
	}
	if _, err := kube.CoreV1().Secrets("ns").Get(ctx, mine.Name, metav1.GetOptions{}); !apierrors.IsNotFound(err) {
		t.Fatalf("key secret still exists: %v", err)
	}
	if err := e.DropUser(ctx, target, "alice"); err != nil {
		t.Fatalf("DropUser of a missing user: %v", err)
	}
}

func TestConnectionDetails(t *testing.T) {
	ctx := context.Background()
	e, _ := newEngine(gatewayConn())
	uri, err := e.ConnectionURI(ctx, target, "alice", validKey)
	if err != nil || uri != "https://llm.example.com/v1" {
		t.Fatalf("ConnectionURI = %q, %v", uri, err)
	}
	props := engine.ConnectionProperties(ctx, e, target)
	if len(props) != 1 || props["model"] != "qwen3-4b" {
		t.Fatalf("ConnectionProperties = %v", props)
	}
}
