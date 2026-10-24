// Package kserve manages API keys for models served by provider-kserve
// through the Envoy AI Gateway. A user of a model is a consumer with its own
// API key: the engine writes one key Secret per user in the format the
// provider's gateway auth renderer reads, which allows the key to call the
// model, and deletes it to revoke the key.
package kserve

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base32"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"regexp"
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// ProviderName is the provider (Instance spec.providerRef.name) this engine serves.
const ProviderName = "provider-kserve"

// RoleInvoke lets a user's key call the model.
const RoleInvoke = "invoke"

// The key Secret format provider-kserve's gateway auth renderer reads; see its
// docs/proposals/ai-gateway-api-keys.md, section 6.1.
const (
	keyLabel         = "openeverest.io/ai-gateway-key"
	ownerKindLabel   = "openeverest.io/ai-gateway-key-owner-kind"
	ownerNameLabel   = "openeverest.io/ai-gateway-key-owner-name"
	managedByLabel   = "app.kubernetes.io/managed-by"
	keyIDAnnotation  = "openeverest.io/ai-gateway-key-id"
	modelsAnnotation = "openeverest.io/ai-gateway-models"
	keyDataKey       = "api_key"

	ownerKindInstance = "instance"
	ownerKindUser     = "user"
	managedBy         = "plugin-user-management"

	// instanceLabel is the controller's InstanceLabel: changes to labeled
	// Secrets requeue the instance, so a deleted key Secret is recreated.
	instanceLabel = "plugin-user-management.plugins.openeverest.io/instance"

	// The provider publishes the gateway address, model and its own key in
	// the Instance's connection Secret, with this type.
	connectionSecretSuffix = "-conn"
	gatewayConnectionType  = "kserve-ai-gateway"

	keySecretInfix = "-api-key-"
	keyPrefix      = "oe_sk_"
	keyIDPrefix    = "k_"
	minKeyLength   = 32
	maxKeyLength   = 128
)

var (
	instanceResource = schema.GroupVersionResource{Group: "core.openeverest.io", Version: "v1alpha1", Resource: "instances"}
	lowerBase32      = base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").WithPadding(base32.NoPadding)
	// The provider's key ID format.
	validKeyID = regexp.MustCompile(`^k_[a-z0-9]+$`)
)

var roleCatalog = []engine.Role{{
	Name:        RoleInvoke,
	Scope:       engine.ScopeCluster,
	Description: "Call the model through the Envoy AI Gateway with this user's API key.",
}}

func invokeGrants() []engine.RoleGrant {
	return []engine.RoleGrant{{Role: RoleInvoke}}
}

// Engine implements engine.Engine for models served by provider-kserve.
type Engine struct {
	kube kubernetes.Interface
	dyn  dynamic.Interface
	// problems keeps why a user's key couldn't be written in the last
	// SyncUsers, by namespace/instance/username, for PendingReason.
	problems sync.Map
}

// New returns an engine that writes key Secrets and reads the Instance and
// its connection Secret.
func New(kube kubernetes.Interface, dyn dynamic.Interface) *Engine {
	return &Engine{kube: kube, dyn: dyn}
}

// Roles implements engine.Engine.
func (e *Engine) Roles() []engine.Role {
	return roleCatalog
}

// AuthDatabase implements engine.Engine. Keys belong to the whole model.
func (e *Engine) AuthDatabase() string {
	return ""
}

// NormalizeRoles implements engine.Engine.
func (e *Engine) NormalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	if len(grants) == 0 {
		return nil, fmt.Errorf("%w: at least one role is required", engine.ErrInvalid)
	}
	for _, g := range grants {
		if g.Role != RoleInvoke {
			return nil, fmt.Errorf("%w: role %q can't be granted", engine.ErrInvalid, g.Role)
		}
	}
	return invokeGrants(), nil
}

// ValidateUser implements engine.Engine. The username is the key's owner
// name label, so it must be a valid label value.
func (e *Engine) ValidateUser(user engine.DesiredUser) error {
	if user.Username == "" || len(validation.IsValidLabelValue(user.Username)) > 0 {
		return fmt.Errorf("%w: usernames of models are at most 63 letters, digits, '-', '_' or '.', and start and end with a letter or digit", engine.ErrInvalid)
	}
	return nil
}

// GenerateKey implements engine.APIKeyEngine with the provider's key format,
// whose prefix lets secret scanners find leaked keys.
func (e *Engine) GenerateKey() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api key: %w", err)
	}
	return keyPrefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// ValidateKey implements engine.APIKeyEngine. Keys travel in HTTP headers, so
// they are printable ASCII without spaces, and long enough not to be guessed.
func (e *Engine) ValidateKey(key string) error {
	if len(key) < minKeyLength || len(key) > maxKeyLength {
		return fmt.Errorf("API keys must be %d to %d characters long", minKeyLength, maxKeyLength)
	}
	for i := 0; i < len(key); i++ {
		if key[i] <= ' ' || key[i] > '~' {
			return fmt.Errorf("API keys may only contain printable ASCII characters without spaces")
		}
	}
	return nil
}

// model is what the engine needs from a model Instance's connection Secret.
type model struct {
	uri  string
	name string
}

// resolve reads the model's connection details. Keys are only useful once
// the model is exposed through the Envoy AI Gateway with API keys on.
func (e *Engine) resolve(ctx context.Context, target engine.Target) (model, error) {
	conn, err := e.kube.CoreV1().Secrets(target.Namespace).Get(ctx, target.Instance+connectionSecretSuffix, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return model{}, fmt.Errorf("%w: the model has no connection details yet", engine.ErrNotReady)
	}
	if err != nil {
		return model{}, fmt.Errorf("get connection secret: %w", err)
	}
	if string(conn.Data["type"]) != gatewayConnectionType {
		return model{}, fmt.Errorf("%w: the model is not exposed through the Envoy AI Gateway", engine.ErrNotReady)
	}
	if len(conn.Data["password"]) == 0 {
		return model{}, fmt.Errorf("%w: API keys are disabled on the Envoy AI Gateway", engine.ErrNotReady)
	}
	return model{uri: string(conn.Data["uri"]), name: string(conn.Data["model"])}, nil
}

// modelRef is how key Secrets name the models they may call.
func modelRef(target engine.Target) string {
	return target.Namespace + "/" + target.Instance
}

// keySecretName is the user's key Secret. It never ends like the provider's
// own key Secret (<instance>-ai-gateway-key).
func keySecretName(target engine.Target, username string) string {
	sum := sha256.Sum256([]byte(username))
	suffix := keySecretInfix + hex.EncodeToString(sum[:])[:10]
	stem := target.Instance
	if limit := validation.DNS1123SubdomainMaxLength - len(suffix); len(stem) > limit {
		stem = strings.TrimRight(stem[:limit], "-.")
	}
	return stem + suffix
}

// newKeyID returns a random key ID, as it appears in authorization rules,
// quota buckets and metrics. It is random rather than derived from the user:
// the provider leaves out every key whose ID another key Secret also claims,
// so a predictable ID would let anyone who can write a Secret disable it.
func newKeyID() (string, error) {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generate api key id: %w", err)
	}
	return keyIDPrefix + lowerBase32.EncodeToString(b), nil
}

func problemKey(target engine.Target, username string) string {
	return modelRef(target) + "/" + username
}

// SyncUsers implements engine.Engine with one key Secret per user, holding
// the user's password as its API key. A user whose key can't be read loses
// its key Secret, so a broken or weak key never stays usable.
func (e *Engine) SyncUsers(ctx context.Context, target engine.Target, users []engine.DesiredUser) error {
	if _, err := e.resolve(ctx, target); err != nil {
		return err
	}
	instance, err := e.dyn.Resource(instanceResource).Namespace(target.Namespace).Get(ctx, target.Instance, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("get instance: %w", err)
	}
	owner := metav1.OwnerReference{
		APIVersion: instance.GetAPIVersion(),
		Kind:       instance.GetKind(),
		Name:       instance.GetName(),
		UID:        instance.GetUID(),
	}

	var errs []error
	for _, u := range users {
		if err := e.ValidateUser(u); err != nil {
			errs = append(errs, err)
			continue
		}
		key, problem, err := e.readKey(ctx, target.Namespace, u.Password)
		if err != nil {
			errs = append(errs, fmt.Errorf("user %q: %w", u.Username, err))
			continue
		}
		if problem != "" {
			e.problems.Store(problemKey(target, u.Username), problem)
			if err := e.deleteKeySecret(ctx, target, u.Username); err != nil {
				errs = append(errs, fmt.Errorf("user %q: %w", u.Username, err))
			}
			continue
		}
		e.problems.Delete(problemKey(target, u.Username))
		if err := e.writeKeySecret(ctx, target, owner, u.Username, key); err != nil {
			errs = append(errs, fmt.Errorf("user %q: %w", u.Username, err))
		}
	}
	return errors.Join(errs...)
}

// readKey returns the user's API key, or a problem that keeps the user from
// having one.
func (e *Engine) readKey(ctx context.Context, namespace string, ref engine.SecretKeyRef) (key, problem string, err error) {
	secret, err := e.kube.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return "", fmt.Sprintf("API key Secret %q not found", ref.Name), nil
	}
	if err != nil {
		return "", "", fmt.Errorf("get API key secret: %w", err)
	}
	key = string(secret.Data[ref.Key])
	if key == "" {
		return "", fmt.Sprintf("API key Secret %q has no %q key", ref.Name, ref.Key), nil
	}
	if err := e.ValidateKey(key); err != nil {
		return "", fmt.Sprintf("API key in Secret %q is not usable: %v", ref.Name, err), nil
	}
	return key, "", nil
}

func (e *Engine) writeKeySecret(ctx context.Context, target engine.Target, owner metav1.OwnerReference, username, key string) error {
	secrets := e.kube.CoreV1().Secrets(target.Namespace)
	name := keySecretName(target, username)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	found := err == nil
	if err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("get key secret: %w", err)
	}
	if found && existing.Labels[managedByLabel] != managedBy {
		return fmt.Errorf("secret %q exists and is not managed by the plugin", name)
	}

	desired := &corev1.Secret{}
	if found {
		desired = existing.DeepCopy()
	}
	desired.Name, desired.Namespace, desired.Type = name, target.Namespace, corev1.SecretTypeOpaque
	if desired.Labels == nil {
		desired.Labels = map[string]string{}
	}
	maps.Copy(desired.Labels, map[string]string{
		keyLabel:       "true",
		ownerKindLabel: ownerKindUser,
		ownerNameLabel: username,
		managedByLabel: managedBy,
		instanceLabel:  target.Instance,
	})
	if desired.Annotations == nil {
		desired.Annotations = map[string]string{}
	}
	// Kept across key rotations, so a user keeps its metrics series.
	if !validKeyID.MatchString(desired.Annotations[keyIDAnnotation]) {
		id, err := newKeyID()
		if err != nil {
			return err
		}
		desired.Annotations[keyIDAnnotation] = id
	}
	desired.Annotations[modelsAnnotation] = modelRef(target)
	desired.Data = map[string][]byte{keyDataKey: []byte(key)}
	desired.OwnerReferences = []metav1.OwnerReference{owner}

	if !found {
		if _, err := secrets.Create(ctx, desired, metav1.CreateOptions{}); err != nil {
			return fmt.Errorf("create key secret: %w", err)
		}
		return nil
	}
	if sameKeySecret(existing, desired) {
		return nil
	}
	if _, err := secrets.Update(ctx, desired, metav1.UpdateOptions{}); err != nil {
		return fmt.Errorf("update key secret: %w", err)
	}
	return nil
}

func sameKeySecret(a, b *corev1.Secret) bool {
	return maps.Equal(a.Labels, b.Labels) &&
		maps.Equal(a.Annotations, b.Annotations) &&
		len(a.Data) == len(b.Data) && string(a.Data[keyDataKey]) == string(b.Data[keyDataKey]) &&
		len(a.OwnerReferences) == 1 && a.OwnerReferences[0] == b.OwnerReferences[0]
}

func (e *Engine) deleteKeySecret(ctx context.Context, target engine.Target, username string) error {
	secrets := e.kube.CoreV1().Secrets(target.Namespace)
	name := keySecretName(target, username)
	existing, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get key secret: %w", err)
	}
	if existing.Labels[managedByLabel] != managedBy {
		return nil
	}
	if err := secrets.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
		return fmt.Errorf("delete key secret: %w", err)
	}
	return nil
}

// ListUsers implements engine.Engine with the key Secrets that may call the
// model: the provider's own key is a system user named after the Instance,
// keys the plugin wrote are its users, and keys anyone else wrote are listed
// so the plugin never declares, and takes over, the same owner.
func (e *Engine) ListUsers(ctx context.Context, target engine.Target) ([]engine.User, error) {
	if _, err := e.resolve(ctx, target); err != nil {
		return nil, err
	}
	list, err := e.kube.CoreV1().Secrets(target.Namespace).List(ctx, metav1.ListOptions{LabelSelector: keyLabel + "=true"})
	if err != nil {
		return nil, fmt.Errorf("list key secrets: %w", err)
	}
	ref := modelRef(target)
	users := []engine.User{}
	for _, s := range list.Items {
		if !allowsModel(s.Annotations[modelsAnnotation], ref) {
			continue
		}
		kind, owner := s.Labels[ownerKindLabel], s.Labels[ownerNameLabel]
		switch {
		case kind == ownerKindInstance && owner == target.Instance:
			users = append(users, engine.User{Username: target.Instance, Roles: invokeGrants(), System: true})
		case owner != "":
			users = append(users, engine.User{Username: owner, Roles: invokeGrants()})
		default:
			users = append(users, engine.User{Username: s.Name, Roles: invokeGrants()})
		}
	}
	return users, nil
}

func allowsModel(annotation, ref string) bool {
	for _, r := range strings.Split(annotation, ",") {
		if strings.TrimSpace(r) == ref {
			return true
		}
	}
	return false
}

// DropUser implements engine.Engine. The key stops working once the gateway
// auth renderer drops it, within seconds.
func (e *Engine) DropUser(ctx context.Context, target engine.Target, username string) error {
	e.problems.Delete(problemKey(target, username))
	return e.deleteKeySecret(ctx, target, username)
}

// PendingReason implements engine.PendingReasoner. Without a problem, the key
// Secret was just written and shows up on the next reconcile.
func (e *Engine) PendingReason(_ context.Context, target engine.Target, username string) string {
	if v, ok := e.problems.Load(problemKey(target, username)); ok {
		return v.(string)
	}
	return "applying the API key to the Envoy AI Gateway"
}

// ConnectionURI implements engine.Engine with the Gateway's base URL, which
// OpenAI-compatible clients take as base_url. The key goes in a header.
func (e *Engine) ConnectionURI(ctx context.Context, target engine.Target, _, _ string) (string, error) {
	m, err := e.resolve(ctx, target)
	if err != nil {
		return "", err
	}
	if m.uri == "" {
		return "", fmt.Errorf("%w: the model has no gateway address yet", engine.ErrNotReady)
	}
	return m.uri, nil
}

// ConnectionProperties implements engine.ConnectionPropertiesProvider with
// the model name clients send in the request's "model" field.
func (e *Engine) ConnectionProperties(ctx context.Context, target engine.Target) (map[string]string, error) {
	m, err := e.resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	if m.name == "" {
		return nil, nil
	}
	return map[string]string{"model": m.name}, nil
}
