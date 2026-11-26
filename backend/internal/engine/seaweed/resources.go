package seaweed

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

const (
	ProviderName = "provider-seaweedfs"
	FieldManager = "plugin-user-management"

	managedByLabel = "app.kubernetes.io/managed-by"
	managedBy      = "plugin-user-management"
	instanceLabel  = "plugin-user-management.plugins.openeverest.io/instance"
	usernameLabel  = "plugin-user-management.plugins.openeverest.io/s3-user"

	identityAnnotation = "plugin-user-management.plugins.openeverest.io/s3-identity"
	usernameAnnotation = "plugin-user-management.plugins.openeverest.io/username"
	roleAnnotation     = "plugin-user-management.plugins.openeverest.io/s3-role"
	bucketAnnotation   = "plugin-user-management.plugins.openeverest.io/s3-bucket"

	maxPolicyNameLength   = 128
	maxResourceNameLength = 230
)

var (
	seaweedResource    = schema.GroupVersionResource{Group: seaweedv1.GroupVersion.Group, Version: seaweedv1.GroupVersion.Version, Resource: "seaweeds"}
	identityResource   = schema.GroupVersionResource{Group: seaweedv1.GroupVersion.Group, Version: seaweedv1.GroupVersion.Version, Resource: "s3identities"}
	credentialResource = schema.GroupVersionResource{
		Group: seaweedv1.GroupVersion.Group, Version: seaweedv1.GroupVersion.Version, Resource: "s3credentials",
	}
	policyResource  = schema.GroupVersionResource{Group: seaweedv1.GroupVersion.Group, Version: seaweedv1.GroupVersion.Version, Resource: "s3policies"}
	bucketResource  = schema.GroupVersionResource{Group: seaweedv1.GroupVersion.Group, Version: seaweedv1.GroupVersion.Version, Resource: "buckets"}
	bindingResource = schema.GroupVersionResource{
		Group: seaweedv1.GroupVersion.Group, Version: seaweedv1.GroupVersion.Version, Resource: "s3policybindings",
	}
)

var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

func identityName(target engine.Target, username string) string {
	return resourceName(maxResourceNameLength, target.Instance, "s3-user", username)
}

func credentialsName(target engine.Target, username string) string {
	return resourceName(maxResourceNameLength, target.Instance, "s3-user", username, "credentials")
}

func bucketName(target engine.Target, name string) string {
	return resourceName(maxResourceNameLength, target.Instance, "bucket", name)
}

func policyName(target engine.Target, username string, grant engine.RoleGrant) string {
	return resourceName(maxPolicyNameLength, target.Instance, "s3", username, grant.Role, grant.Bucket)
}

func bindingName(target engine.Target, username string, grant engine.RoleGrant) string {
	return policyName(target, username, grant) + "-binding"
}

func resourceName(limit int, parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	suffix := "-" + hex.EncodeToString(sum[:])[:10]
	stem := invalidNameChars.ReplaceAllString(strings.ToLower(strings.Join(parts, "-")), "-")
	stem = strings.Trim(stem, "-")
	maxStem := limit - len(suffix)
	if len(stem) > maxStem {
		stem = strings.TrimRight(stem[:maxStem], "-")
	}
	return strings.TrimRight(stem, "-") + suffix
}

func identityLabel(name string) string {
	sum := sha256.Sum256([]byte(name))
	return hex.EncodeToString(sum[:])[:12]
}

func ownerReference(obj metav1.Object, apiVersion, kind string) metav1.OwnerReference {
	return metav1.OwnerReference{
		APIVersion: apiVersion,
		Kind:       kind,
		Name:       obj.GetName(),
		UID:        obj.GetUID(),
	}
}

func managedLabels(target engine.Target, identityName string) map[string]string {
	labels := map[string]string{
		managedByLabel: managedBy,
		instanceLabel:  target.Instance,
	}
	if identityName != "" {
		labels[usernameLabel] = identityLabel(identityName)
	}
	return labels
}

func toUnstructured(obj runtime.Object) (*unstructured.Unstructured, error) {
	raw, err := runtime.DefaultUnstructuredConverter.ToUnstructured(obj)
	if err != nil {
		return nil, err
	}
	return &unstructured.Unstructured{Object: raw}, nil
}

func fromUnstructured(u *unstructured.Unstructured, out any) error {
	return runtime.DefaultUnstructuredConverter.FromUnstructured(u.Object, out)
}

func identityObject(target engine.Target, seaweed *seaweedv1.Seaweed, username string) *seaweedv1.S3Identity {
	name := identityName(target, username)
	return &seaweedv1.S3Identity{
		TypeMeta: metav1.TypeMeta{
			APIVersion: seaweedv1.GroupVersion.String(),
			Kind:       "S3Identity",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: target.Namespace,
			Labels:    managedLabels(target, name),
			Annotations: map[string]string{
				usernameAnnotation: username,
			},
			OwnerReferences: []metav1.OwnerReference{
				ownerReference(seaweed, seaweedv1.GroupVersion.String(), "Seaweed"),
			},
		},
		Spec: seaweedv1.S3IdentitySpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: target.Instance},
			Name:       name,
			Account:    &seaweedv1.S3Account{DisplayName: username},
		},
	}
}

func credentialsObject(
	target engine.Target,
	identity *seaweedv1.S3Identity,
	user engine.DesiredUser,
) *seaweedv1.S3Credentials {
	return &seaweedv1.S3Credentials{
		TypeMeta: metav1.TypeMeta{
			APIVersion: seaweedv1.GroupVersion.String(),
			Kind:       "S3Credentials",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      credentialsName(target, user.Username),
			Namespace: target.Namespace,
			Labels:    managedLabels(target, identity.Name),
			OwnerReferences: []metav1.OwnerReference{
				ownerReference(identity, seaweedv1.GroupVersion.String(), "S3Identity"),
			},
		},
		Spec: seaweedv1.S3CredentialsSpec{
			SeaweedRef:  seaweedv1.SeaweedReference{Name: target.Instance},
			IdentityRef: seaweedv1.S3IdentityRef{Name: identity.Name},
			SecretRef: seaweedv1.S3SecretRef{
				Name:           user.Password.Name,
				AccessKeyField: accessKeyField,
				SecretKeyField: user.Password.Key,
			},
		},
	}
}

func policyObject(
	target engine.Target,
	identity *seaweedv1.S3Identity,
	grant engine.RoleGrant,
) *seaweedv1.S3Policy {
	username := identityUsername(identity)
	name := policyName(target, username, grant)
	return &seaweedv1.S3Policy{
		TypeMeta: metav1.TypeMeta{
			APIVersion: seaweedv1.GroupVersion.String(),
			Kind:       "S3Policy",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: target.Namespace,
			Labels:    managedLabels(target, identity.Name),
			Annotations: map[string]string{
				identityAnnotation: identity.Name,
				roleAnnotation:     grant.Role,
				bucketAnnotation:   grant.Bucket,
			},
			OwnerReferences: []metav1.OwnerReference{
				ownerReference(identity, seaweedv1.GroupVersion.String(), "S3Identity"),
			},
		},
		Spec: seaweedv1.S3PolicySpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: target.Instance},
			Statements: policyStatements(grant),
		},
	}
}

func bindingObject(
	target engine.Target,
	identity *seaweedv1.S3Identity,
	grant engine.RoleGrant,
	policy *seaweedv1.S3Policy,
) *seaweedv1.S3PolicyBinding {
	username := identityUsername(identity)
	return &seaweedv1.S3PolicyBinding{
		TypeMeta: metav1.TypeMeta{
			APIVersion: seaweedv1.GroupVersion.String(),
			Kind:       "S3PolicyBinding",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      bindingName(target, username, grant),
			Namespace: target.Namespace,
			Labels:    managedLabels(target, identity.Name),
			Annotations: map[string]string{
				identityAnnotation: identity.Name,
				roleAnnotation:     grant.Role,
				bucketAnnotation:   grant.Bucket,
			},
			OwnerReferences: []metav1.OwnerReference{
				ownerReference(identity, seaweedv1.GroupVersion.String(), "S3Identity"),
			},
		},
		Spec: seaweedv1.S3PolicyBindingSpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: target.Instance},
			PolicyRef:  seaweedv1.S3PolicyRef{Name: policy.Name},
			Subjects: []seaweedv1.S3Subject{{
				Kind: seaweedv1.S3SubjectKindIdentity,
				Name: identity.Name,
			}},
		},
	}
}

func bucketObject(
	target engine.Target,
	seaweed *seaweedv1.Seaweed,
	bucket engine.DesiredBucket,
) *seaweedv1.Bucket {
	owner := ""
	if bucket.Owner != "" {
		owner = identityName(target, bucket.Owner)
	}
	return &seaweedv1.Bucket{
		TypeMeta: metav1.TypeMeta{
			APIVersion: seaweedv1.GroupVersion.String(),
			Kind:       "Bucket",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      bucketName(target, bucket.Name),
			Namespace: target.Namespace,
			Labels: map[string]string{
				managedByLabel: managedBy,
				instanceLabel:  target.Instance,
			},
			Annotations: map[string]string{
				usernameAnnotation: bucket.Owner,
			},
			OwnerReferences: []metav1.OwnerReference{
				ownerReference(seaweed, seaweedv1.GroupVersion.String(), "Seaweed"),
			},
		},
		Spec: seaweedv1.BucketSpec{
			ClusterRef:    &seaweedv1.SeaweedReference{Name: target.Instance},
			Name:          bucket.Name,
			ReclaimPolicy: seaweedv1.BucketReclaimRetain,
			Owner:         owner,
		},
	}
}

func identityUsername(identity *seaweedv1.S3Identity) string {
	if isManaged(identity) {
		if username := identity.Annotations[usernameAnnotation]; username != "" {
			return username
		}
	}
	if identity.Spec.Name != "" {
		return identity.Spec.Name
	}
	return identity.Name
}

func iamReadyProblem(kind, name string, generation, observed int64, phase seaweedv1.S3Phase, conditions []metav1.Condition) string {
	if observed != generation {
		return fmt.Sprintf("waiting for %s %q generation %d to be observed", kind, name, generation)
	}
	switch phase {
	case seaweedv1.S3PhaseReady:
		return ""
	case seaweedv1.S3PhaseFailed:
		message := ""
		for _, condition := range conditions {
			if condition.Type == seaweedv1.S3ConditionReady &&
				condition.Status == metav1.ConditionFalse &&
				condition.Message != "" {
				message = condition.Message
				break
			}
		}
		if message == "" {
			message = fmt.Sprintf("%s reported Failed", kind)
		}
		return message
	default:
		return fmt.Sprintf("waiting for %s %q to become Ready", kind, name)
	}
}

func identityReadyProblem(identity *seaweedv1.S3Identity) string {
	return iamReadyProblem(
		"S3Identity", identity.Name, identity.Generation, identity.Status.ObservedGeneration,
		identity.Status.Phase, identity.Status.Conditions,
	)
}

func credentialsReadyProblem(credentials *seaweedv1.S3Credentials) string {
	return iamReadyProblem(
		"S3Credentials", credentials.Name, credentials.Generation, credentials.Status.ObservedGeneration,
		credentials.Status.Phase, credentials.Status.Conditions,
	)
}

func policyReadyProblem(policy *seaweedv1.S3Policy) string {
	return iamReadyProblem(
		"S3Policy", policy.Name, policy.Generation, policy.Status.ObservedGeneration,
		policy.Status.Phase, policy.Status.Conditions,
	)
}

func bindingReadyProblem(binding *seaweedv1.S3PolicyBinding) string {
	return iamReadyProblem(
		"S3PolicyBinding", binding.Name, binding.Generation, binding.Status.ObservedGeneration,
		binding.Status.Phase, binding.Status.Conditions,
	)
}

func bucketReadyProblem(bucket *seaweedv1.Bucket) string {
	if bucket.Status.ObservedGeneration != bucket.Generation {
		return fmt.Sprintf("waiting for Bucket %q generation %d to be observed", bucket.Name, bucket.Generation)
	}
	switch bucket.Status.Phase {
	case seaweedv1.BucketPhaseReady:
		return ""
	case seaweedv1.BucketPhaseFailed:
		message := ""
		for _, condition := range bucket.Status.Conditions {
			if condition.Type == seaweedv1.BucketConditionReady &&
				condition.Status == metav1.ConditionFalse &&
				condition.Message != "" {
				message = condition.Message
				break
			}
		}
		if message == "" {
			message = "Bucket reported Failed"
		}
		return message
	default:
		return fmt.Sprintf("waiting for Bucket %q to become Ready", bucket.Name)
	}
}

func matchingIdentity(identity *seaweedv1.S3Identity, target engine.Target) bool {
	return identity.Spec.SeaweedRef.Name == target.Instance
}

func matchingBucket(bucket *seaweedv1.Bucket, target engine.Target) bool {
	ref := bucket.Spec.ClusterReference()
	namespace := ref.Namespace
	if namespace == "" {
		namespace = bucket.Namespace
	}
	return ref.Name == target.Instance && namespace == target.Namespace
}

func bucketAlreadyExists(bucket *seaweedv1.Bucket) bool {
	for _, condition := range bucket.Status.Conditions {
		if condition.Type == seaweedv1.BucketConditionBucketAlreadyExists &&
			condition.Status == metav1.ConditionTrue {
			return true
		}
	}
	return false
}

func isManaged(obj metav1.Object) bool {
	return obj.GetLabels()[managedByLabel] == managedBy
}
