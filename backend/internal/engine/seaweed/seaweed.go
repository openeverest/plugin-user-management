// Package seaweed reconciles S3 users and bucket grants for
// provider-seaweedfs through the operator's native IAM CRDs.
package seaweed

import (
	"context"
	"crypto/rand"
	"fmt"
	"math/big"
	"reflect"
	"sort"
	"sync"
	"time"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/kubernetes"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

const (
	accessKeyField       = "AWS_ACCESS_KEY_ID"
	accessKeyIDLength    = 20
	accessKeyIDAlphabet  = "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	connectionSecretType = "s3"
)

// Engine implements engine.Engine for SeaweedFS instances.
type Engine struct {
	kube kubernetes.Interface
	dyn  dynamic.Interface

	problems sync.Map
}

// New returns an engine that manages the SeaweedFS operator's S3 IAM CRDs.
func New(kube kubernetes.Interface, dyn dynamic.Interface) *Engine {
	return &Engine{kube: kube, dyn: dyn}
}

func (e *Engine) Roles() []engine.Role {
	return roleCatalog
}

func (e *Engine) AuthDatabase() string {
	return ""
}

func (e *Engine) NormalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	return normalizeRoles(grants)
}

func (e *Engine) CredentialKind() engine.CredentialKind {
	return engine.CredentialS3
}

func (e *Engine) ValidateUser(user engine.DesiredUser) error {
	if user.Username == "admin" || user.Username == "anonymous" {
		return fmt.Errorf("%w: user %q is reserved by SeaweedFS", engine.ErrInvalid, user.Username)
	}
	if !user.ManagedCredentials {
		return fmt.Errorf("%w: SeaweedFS users currently require plugin-managed credentials", engine.ErrInvalid)
	}
	// The controller may temporarily withhold every role while a managed
	// InstanceBucket is still becoming available. The identity and key still
	// need to exist first so that it can own the bucket. The HTTP create path
	// already requires NormalizeRoles to receive at least one declared role.
	if len(user.Roles) == 0 {
		return nil
	}
	_, err := e.NormalizeRoles(user.Roles)
	return err
}

func (e *Engine) PrepareCredentialSecret(
	_ context.Context,
	_ engine.Target,
	_ string,
	password string,
	data map[string][]byte,
) error {
	if password == "" {
		return fmt.Errorf("secret access key is empty")
	}
	if len(data[accessKeyField]) > 0 {
		return nil
	}
	accessKey, err := randomAccessKey()
	if err != nil {
		return err
	}
	data[accessKeyField] = []byte(accessKey)
	return nil
}

func randomAccessKey() (string, error) {
	limit := big.NewInt(int64(len(accessKeyIDAlphabet)))
	out := make([]byte, accessKeyIDLength)
	for i := range out {
		n, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", fmt.Errorf("generate S3 access key: %w", err)
		}
		out[i] = accessKeyIDAlphabet[n.Int64()]
	}
	return string(out), nil
}

func (e *Engine) getSeaweed(ctx context.Context, target engine.Target) (*seaweedv1.Seaweed, error) {
	obj, err := e.dyn.Resource(seaweedResource).Namespace(target.Namespace).Get(
		ctx, target.Instance, metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: Seaweed cluster is not found", engine.ErrNotReady)
	}
	if err != nil {
		return nil, fmt.Errorf("get Seaweed cluster: %w", err)
	}
	seaweed := &seaweedv1.Seaweed{}
	if err := fromUnstructured(obj, seaweed); err != nil {
		return nil, fmt.Errorf("decode Seaweed cluster: %w", err)
	}
	return seaweed, nil
}

func (e *Engine) SyncUsers(ctx context.Context, target engine.Target, users []engine.DesiredUser) error {
	seaweed, err := e.getSeaweed(ctx, target)
	if err != nil {
		return err
	}

	desiredGrants := map[string]bool{}
	desiredIdentities := map[string]bool{}
	for _, user := range users {
		key := problemKey(target, user.Username)
		if err := e.ValidateUser(user); err != nil {
			e.problems.Store(key, err.Error())
			continue
		}
		secret, problem, err := e.credentialsSecret(ctx, target.Namespace, user.Password)
		if err != nil {
			return err
		}
		if problem != "" {
			e.problems.Store(key, problem)
			continue
		}
		if len(secret.Data[accessKeyField]) == 0 {
			e.problems.Store(key, fmt.Sprintf("credentials Secret %q has no %q key", user.Password.Name, accessKeyField))
			continue
		}
		e.problems.Delete(key)

		identity, err := e.applyIdentity(ctx, identityObject(target, seaweed, user.Username))
		if err != nil {
			return err
		}
		desiredIdentities[identity.Name] = true
		if _, err := e.applyCredentials(ctx, credentialsObject(target, identity, user)); err != nil {
			return err
		}

		for _, grant := range user.Roles {
			policy, err := e.applyPolicy(ctx, policyObject(target, identity, grant))
			if err != nil {
				return err
			}
			if _, err := e.applyBinding(ctx, bindingObject(target, identity, grant, policy)); err != nil {
				return err
			}
			desiredGrants[policy.Name] = true
			desiredGrants[bindingName(target, user.Username, grant)] = true
		}
		if problem := e.userProblem(ctx, target, user.Username); problem != "" {
			e.problems.Store(key, problem)
		} else {
			e.problems.Delete(key)
		}
	}

	for _, gvr := range []schema.GroupVersionResource{bindingResource, policyResource} {
		list, err := e.dyn.Resource(gvr).Namespace(target.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return fmt.Errorf("list %s: %w", gvr.Resource, err)
		}
		for i := range list.Items {
			obj := &list.Items[i]
			if !isManaged(obj) || obj.GetLabels()[instanceLabel] != target.Instance || desiredGrants[obj.GetName()] {
				continue
			}
			identity := obj.GetAnnotations()[identityAnnotation]
			if identity != "" && !desiredIdentities[identity] {
				// The identity owner will clean up these after DropUser.
				continue
			}
			if err := e.dyn.Resource(gvr).Namespace(target.Namespace).Delete(ctx, obj.GetName(), metav1.DeleteOptions{}); err != nil &&
				!apierrors.IsNotFound(err) {
				return fmt.Errorf("delete stale %s %q: %w", obj.GetKind(), obj.GetName(), err)
			}
		}
	}
	return nil
}

func (e *Engine) credentialsSecret(
	ctx context.Context,
	namespace string,
	ref engine.SecretKeyRef,
) (*corev1.Secret, string, error) {
	secret, err := e.kube.CoreV1().Secrets(namespace).Get(ctx, ref.Name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Sprintf("credentials Secret %q is not found", ref.Name), nil
	}
	if err != nil {
		return nil, "", fmt.Errorf("get credentials Secret %q: %w", ref.Name, err)
	}
	if len(secret.Data[ref.Key]) == 0 {
		return secret, fmt.Sprintf("credentials Secret %q has no %q key", ref.Name, ref.Key), nil
	}
	return secret, "", nil
}

func (e *Engine) apply(
	ctx context.Context,
	gvr schema.GroupVersionResource,
	obj runtime.Object,
) (*unstructured.Unstructured, error) {
	desired, err := toUnstructured(obj)
	if err != nil {
		return nil, fmt.Errorf("encode %T: %w", obj, err)
	}
	resource := e.dyn.Resource(gvr).Namespace(desired.GetNamespace())
	existing, err := resource.Get(ctx, desired.GetName(), metav1.GetOptions{})
	switch {
	case apierrors.IsNotFound(err):
	case err != nil:
		return nil, fmt.Errorf("get %s %q: %w", desired.GetKind(), desired.GetName(), err)
	case !isManaged(existing) || existing.GetLabels()[instanceLabel] != desired.GetLabels()[instanceLabel]:
		return nil, fmt.Errorf("%s %q exists and is not managed by the plugin", desired.GetKind(), desired.GetName())
	}
	applied, err := resource.Apply(ctx, desired.GetName(), desired, metav1.ApplyOptions{
		FieldManager: FieldManager,
		Force:        true,
	})
	if apierrors.IsNotFound(err) {
		return resource.Create(ctx, desired, metav1.CreateOptions{FieldManager: FieldManager})
	}
	if err != nil {
		return nil, fmt.Errorf("apply %s %q: %w", desired.GetKind(), desired.GetName(), err)
	}
	return applied, nil
}

func (e *Engine) applyIdentity(ctx context.Context, desired *seaweedv1.S3Identity) (*seaweedv1.S3Identity, error) {
	applied, err := e.apply(ctx, identityResource, desired)
	if err != nil {
		return nil, err
	}
	out := &seaweedv1.S3Identity{}
	if err := fromUnstructured(applied, out); err != nil {
		return nil, fmt.Errorf("decode S3Identity %q: %w", desired.Name, err)
	}
	return out, nil
}

func (e *Engine) applyCredentials(ctx context.Context, desired *seaweedv1.S3Credentials) (*seaweedv1.S3Credentials, error) {
	applied, err := e.apply(ctx, credentialResource, desired)
	if err != nil {
		return nil, err
	}
	out := &seaweedv1.S3Credentials{}
	if err := fromUnstructured(applied, out); err != nil {
		return nil, fmt.Errorf("decode S3Credentials %q: %w", desired.Name, err)
	}
	return out, nil
}

func (e *Engine) applyPolicy(ctx context.Context, desired *seaweedv1.S3Policy) (*seaweedv1.S3Policy, error) {
	applied, err := e.apply(ctx, policyResource, desired)
	if err != nil {
		return nil, err
	}
	out := &seaweedv1.S3Policy{}
	if err := fromUnstructured(applied, out); err != nil {
		return nil, fmt.Errorf("decode S3Policy %q: %w", desired.Name, err)
	}
	return out, nil
}

func (e *Engine) applyBinding(ctx context.Context, desired *seaweedv1.S3PolicyBinding) (*seaweedv1.S3PolicyBinding, error) {
	applied, err := e.apply(ctx, bindingResource, desired)
	if err != nil {
		return nil, err
	}
	out := &seaweedv1.S3PolicyBinding{}
	if err := fromUnstructured(applied, out); err != nil {
		return nil, fmt.Errorf("decode S3PolicyBinding %q: %w", desired.Name, err)
	}
	return out, nil
}

func (e *Engine) applyBucket(ctx context.Context, desired *seaweedv1.Bucket) (*seaweedv1.Bucket, error) {
	applied, err := e.apply(ctx, bucketResource, desired)
	if err != nil {
		return nil, err
	}
	out := &seaweedv1.Bucket{}
	if err := fromUnstructured(applied, out); err != nil {
		return nil, fmt.Errorf("decode Bucket %q: %w", desired.Name, err)
	}
	return out, nil
}

func (e *Engine) ListUsers(ctx context.Context, target engine.Target) ([]engine.User, error) {
	if _, err := e.getSeaweed(ctx, target); err != nil {
		return nil, err
	}

	list, err := e.dyn.Resource(identityResource).Namespace(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list S3Identity resources: %w", err)
	}
	grants, err := e.bucketGrants(ctx, target)
	if err != nil {
		return nil, err
	}
	users := make([]engine.User, 0, len(list.Items))
	for i := range list.Items {
		identity := &seaweedv1.S3Identity{}
		if err := fromUnstructured(&list.Items[i], identity); err != nil {
			return nil, fmt.Errorf("decode S3Identity: %w", err)
		}
		if !matchingIdentity(identity, target) {
			continue
		}
		username := identityUsername(identity)
		if username == "" {
			continue
		}
		users = append(users, engine.User{
			Username: username,
			Roles:    grants[identity.Name],
			System:   username == "admin" || username == "anonymous",
		})
	}
	sort.Slice(users, func(i, j int) bool { return users[i].Username < users[j].Username })
	return users, nil
}

// ValidateBucket implements engine.BucketEngine with the operator's S3 bucket
// naming contract.
func (e *Engine) ValidateBucket(name string) error {
	if !validBucketName(name) {
		return fmt.Errorf("%w: %q is not a valid S3 bucket name", engine.ErrInvalid, name)
	}
	return nil
}

// ListBuckets implements engine.BucketEngine. Buckets represented by native
// Bucket CRs are visible; buckets created directly in SeaweedFS without a CR
// are not discoverable through Kubernetes.
func (e *Engine) ListBuckets(ctx context.Context, target engine.Target) ([]engine.Bucket, error) {
	if _, err := e.getSeaweed(ctx, target); err != nil {
		return nil, err
	}
	list, err := e.dyn.Resource(bucketResource).Namespace(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list Bucket resources: %w", err)
	}
	owners, err := e.identityDisplayNames(ctx, target)
	if err != nil {
		return nil, err
	}
	buckets := make([]engine.Bucket, 0, len(list.Items))
	for i := range list.Items {
		bucket := &seaweedv1.Bucket{}
		if err := fromUnstructured(&list.Items[i], bucket); err != nil {
			return nil, fmt.Errorf("decode Bucket: %w", err)
		}
		if !matchingBucket(bucket, target) {
			continue
		}
		name := bucket.Spec.Name
		if name == "" {
			name = bucket.Name
		}
		owner := bucket.Status.OwnerIdentity
		if visible := owners[owner]; visible != "" {
			owner = visible
		}
		buckets = append(buckets, engine.Bucket{Name: name, Owner: owner})
	}
	sort.Slice(buckets, func(i, j int) bool { return buckets[i].Name < buckets[j].Name })
	return buckets, nil
}

func (e *Engine) identityDisplayNames(ctx context.Context, target engine.Target) (map[string]string, error) {
	list, err := e.dyn.Resource(identityResource).Namespace(target.Namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list S3Identity resources: %w", err)
	}
	owners := map[string]string{}
	for i := range list.Items {
		identity := &seaweedv1.S3Identity{}
		if err := fromUnstructured(&list.Items[i], identity); err != nil {
			return nil, fmt.Errorf("decode S3Identity: %w", err)
		}
		if !matchingIdentity(identity, target) {
			continue
		}
		iamName := identity.Spec.Name
		if iamName == "" {
			iamName = identity.Name
		}
		owners[iamName] = identityUsername(identity)
	}
	return owners, nil
}

// SyncBucket implements engine.BucketEngine using the SeaweedFS operator's
// native Bucket CR.
func (e *Engine) SyncBucket(ctx context.Context, target engine.Target, bucket engine.DesiredBucket) error {
	seaweed, err := e.getSeaweed(ctx, target)
	if err != nil {
		return err
	}
	desired := bucketObject(target, seaweed, bucket)
	resource := e.dyn.Resource(bucketResource).Namespace(target.Namespace)
	existingObj, err := resource.Get(ctx, desired.Name, metav1.GetOptions{})
	switch {
	case err == nil:
		if !isManaged(existingObj) || existingObj.GetLabels()[instanceLabel] != target.Instance {
			return fmt.Errorf("Bucket %q exists and is not managed by the plugin", desired.Name)
		}
		existing := &seaweedv1.Bucket{}
		if err := fromUnstructured(existingObj, existing); err != nil {
			return fmt.Errorf("decode Bucket %q: %w", desired.Name, err)
		}
		if reflect.DeepEqual(existing.Spec, desired.Spec) {
			if problem := bucketReadyProblem(existing); problem != "" {
				if bucketAlreadyExists(existing) {
					return fmt.Errorf("%w: %s", engine.ErrInvalid, problem)
				}
				return fmt.Errorf("%w: %s", engine.ErrInProgress, problem)
			}
			return nil
		}
	case apierrors.IsNotFound(err):
	case err != nil:
		return fmt.Errorf("get Bucket %q: %w", desired.Name, err)
	}
	applied, err := e.applyBucket(ctx, desired)
	if err != nil {
		return err
	}
	if problem := bucketReadyProblem(applied); problem != "" {
		if bucketAlreadyExists(applied) {
			return fmt.Errorf("%w: %s", engine.ErrInvalid, problem)
		}
		return fmt.Errorf("%w: %s", engine.ErrInProgress, problem)
	}
	return nil
}

// ReleaseBucket removes the native Bucket CR, retaining or deleting the
// underlying S3 bucket according to drop.
func (e *Engine) ReleaseBucket(ctx context.Context, target engine.Target, name string, drop bool) error {
	resource := e.dyn.Resource(bucketResource).Namespace(target.Namespace)
	resourceName := bucketName(target, name)
	obj, err := resource.Get(ctx, resourceName, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get Bucket %q: %w", resourceName, err)
	}
	if !isManaged(obj) || obj.GetLabels()[instanceLabel] != target.Instance {
		return fmt.Errorf("Bucket %q is not managed by the plugin", resourceName)
	}
	bucket := &seaweedv1.Bucket{}
	if err := fromUnstructured(obj, bucket); err != nil {
		return fmt.Errorf("decode Bucket %q: %w", resourceName, err)
	}
	policy := seaweedv1.BucketReclaimRetain
	if drop {
		policy = seaweedv1.BucketReclaimDelete
	}
	if bucket.Spec.ReclaimPolicy != policy && bucket.DeletionTimestamp == nil {
		bucket.Spec.ReclaimPolicy = policy
		updated, err := toUnstructured(bucket)
		if err != nil {
			return fmt.Errorf("encode Bucket %q: %w", resourceName, err)
		}
		obj, err = resource.Update(ctx, updated, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("set Bucket reclaim policy: %w", err)
		}
		if err := fromUnstructured(obj, bucket); err != nil {
			return fmt.Errorf("decode Bucket %q: %w", resourceName, err)
		}
	}
	if bucket.DeletionTimestamp == nil {
		if err := resource.Delete(ctx, resourceName, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete Bucket %q: %w", resourceName, err)
		}
	}
	return fmt.Errorf("%w: SeaweedFS is releasing bucket %q", engine.ErrInProgress, name)
}

func (e *Engine) bucketGrants(ctx context.Context, target engine.Target) (map[string][]engine.RoleGrant, error) {
	list, err := e.dyn.Resource(bindingResource).Namespace(target.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: managedByLabel + "=" + managedBy,
	})
	if err != nil {
		return nil, fmt.Errorf("list S3PolicyBindings: %w", err)
	}
	grants := map[string][]engine.RoleGrant{}
	for i := range list.Items {
		binding := &list.Items[i]
		if binding.GetLabels()[instanceLabel] != target.Instance {
			continue
		}
		annotations := binding.GetAnnotations()
		grant := engine.RoleGrant{Role: annotations[roleAnnotation], Bucket: annotations[bucketAnnotation]}
		identity := annotations[identityAnnotation]
		if grant.Role != "" && grant.Bucket != "" && identity != "" {
			grants[identity] = append(grants[identity], grant)
		}
	}
	for identity := range grants {
		sort.Slice(grants[identity], func(i, j int) bool {
			a, b := grants[identity][i], grants[identity][j]
			if a.Bucket != b.Bucket {
				return a.Bucket < b.Bucket
			}
			return a.Role < b.Role
		})
	}
	return grants, nil
}

func (e *Engine) DropUser(ctx context.Context, target engine.Target, username string) error {
	name := identityName(target, username)
	list, err := e.dyn.Resource(bucketResource).Namespace(metav1.NamespaceAll).List(
		ctx, metav1.ListOptions{},
	)
	if err != nil {
		return fmt.Errorf("list buckets before dropping S3 user: %w", err)
	}
	for i := range list.Items {
		bucket := &seaweedv1.Bucket{}
		if err := fromUnstructured(&list.Items[i], bucket); err != nil {
			return fmt.Errorf("decode Bucket: %w", err)
		}
		if !matchingBucket(bucket, target) {
			continue
		}
		if bucket.Spec.Owner == name {
			bucketName := bucket.Spec.Name
			if bucketName == "" {
				bucketName = bucket.Name
			}
			return fmt.Errorf("%w: bucket %q is still owned by user %q", engine.ErrInProgress, bucketName, username)
		}
	}
	resource := e.dyn.Resource(identityResource).Namespace(target.Namespace)
	obj, err := resource.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		e.problems.Delete(problemKey(target, username))
		return nil
	}
	if err != nil {
		return fmt.Errorf("get S3Identity %q: %w", name, err)
	}
	identity := &seaweedv1.S3Identity{}
	if err := fromUnstructured(obj, identity); err != nil {
		return fmt.Errorf("decode S3Identity %q: %w", name, err)
	}
	if !isManaged(identity) {
		return nil
	}
	if identity.DeletionTimestamp == nil {
		if err := resource.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete S3Identity %q: %w", name, err)
		}
	}
	return fmt.Errorf("%w: SeaweedFS is deleting IAM user %q", engine.ErrInProgress, username)
}

func (e *Engine) ConnectionURI(ctx context.Context, target engine.Target, _, _ string) (string, error) {
	connection, err := e.connection(ctx, target)
	if err != nil {
		return "", err
	}
	return connection["endpointURL"], nil
}

func (e *Engine) ConnectionProperties(ctx context.Context, target engine.Target) (map[string]string, error) {
	return e.connection(ctx, target)
}

func (e *Engine) connection(ctx context.Context, target engine.Target) (map[string]string, error) {
	secret, err := e.kube.CoreV1().Secrets(target.Namespace).Get(ctx, target.Instance+"-conn", metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil, fmt.Errorf("%w: S3 connection details are not ready", engine.ErrNotReady)
	}
	if err != nil {
		return nil, fmt.Errorf("get S3 connection Secret: %w", err)
	}
	if string(secret.Data["type"]) != connectionSecretType {
		return nil, fmt.Errorf("%w: the instance has no S3 connection details", engine.ErrNotReady)
	}
	properties := map[string]string{}
	for _, key := range []string{
		"endpointURL",
		"externalEndpointURL",
		"forcePathStyle",
		"verifyTLS",
		"externalVerifyTLS",
		"region",
	} {
		if len(secret.Data[key]) > 0 {
			properties[key] = string(secret.Data[key])
		}
	}
	if properties["endpointURL"] == "" {
		return nil, fmt.Errorf("%w: the S3 endpoint is not ready", engine.ErrNotReady)
	}
	return properties, nil
}

func problemKey(target engine.Target, username string) string {
	return target.Namespace + "/" + target.Instance + "/" + username
}

func (e *Engine) UserProblem(target engine.Target, username string) string {
	if value, ok := e.problems.Load(problemKey(target, username)); ok {
		return value.(string)
	}
	return ""
}

func (e *Engine) userProblem(ctx context.Context, target engine.Target, username string) string {
	identityObj, err := e.dyn.Resource(identityResource).Namespace(target.Namespace).Get(
		ctx, identityName(target, username), metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return fmt.Sprintf("waiting for %s %q to be created", identityResource.Resource, identityName(target, username))
	}
	if err == nil {
		identity := &seaweedv1.S3Identity{}
		if err := fromUnstructured(identityObj, identity); err == nil {
			if reason := identityReadyProblem(identity); reason != "" {
				return reason
			}
		}
	}

	credObj, err := e.dyn.Resource(credentialResource).Namespace(target.Namespace).Get(
		ctx, credentialsName(target, username), metav1.GetOptions{},
	)
	if apierrors.IsNotFound(err) {
		return fmt.Sprintf("waiting for %s %q to be created", credentialResource.Resource, credentialsName(target, username))
	}
	if err == nil {
		credentials := &seaweedv1.S3Credentials{}
		if err := fromUnstructured(credObj, credentials); err == nil {
			if reason := credentialsReadyProblem(credentials); reason != "" {
				return reason
			}
		}
	}

	policyList, err := e.dyn.Resource(policyResource).Namespace(target.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: usernameLabel + "=" + identityLabel(identityName(target, username)),
	})
	if err == nil {
		for i := range policyList.Items {
			policy := &seaweedv1.S3Policy{}
			if err := fromUnstructured(&policyList.Items[i], policy); err != nil {
				continue
			}
			if reason := policyReadyProblem(policy); reason != "" {
				return reason
			}
		}
	}

	bindingList, err := e.dyn.Resource(bindingResource).Namespace(target.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: usernameLabel + "=" + identityLabel(identityName(target, username)),
	})
	if err == nil {
		for i := range bindingList.Items {
			binding := &seaweedv1.S3PolicyBinding{}
			if err := fromUnstructured(&bindingList.Items[i], binding); err != nil {
				continue
			}
			if reason := bindingReadyProblem(binding); reason != "" {
				return reason
			}
		}
	}
	return ""
}

func (e *Engine) PendingReason(ctx context.Context, target engine.Target, username string) string {
	if reason := e.UserProblem(target, username); reason != "" {
		return reason
	}
	if reason := e.userProblem(ctx, target, username); reason != "" {
		return reason
	}
	return "waiting for SeaweedFS IAM to apply the user"
}

func (e *Engine) ResyncInterval() time.Duration {
	return 5 * time.Minute
}
