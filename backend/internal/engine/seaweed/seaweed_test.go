package seaweed

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	seaweedv1 "github.com/seaweedfs/seaweedfs-operator/api/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	kubefake "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var target = engine.Target{Namespace: "ns", Instance: "object-store"}

func mustUnstructured(obj runtime.Object) *unstructured.Unstructured {
	u, err := toUnstructured(obj)
	if err != nil {
		panic(err)
	}
	return u
}

func seaweed() *unstructured.Unstructured {
	return mustUnstructured(&seaweedv1.Seaweed{
		TypeMeta: metav1.TypeMeta{
			APIVersion: seaweedv1.GroupVersion.String(),
			Kind:       "Seaweed",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      target.Instance,
			Namespace: target.Namespace,
			UID:       "seaweed-uid",
		},
	})
}

func connectionSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: target.Instance + "-conn", Namespace: target.Namespace},
		Data: map[string][]byte{
			"type":              []byte("s3"),
			"endpointURL":       []byte("https://object-store-s3.ns.svc:8334"),
			"forcePathStyle":    []byte("true"),
			"region":            []byte("us-east-1"),
			"verifyTLS":         []byte("false"),
			"AWS_ACCESS_KEY_ID": []byte("admin-access-key"),
		},
	}
}

func credentialsSecret(accessKey string) *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "alice-credentials", Namespace: target.Namespace},
		Data: map[string][]byte{
			"username":     []byte("alice"),
			"password":     []byte("secret-access-key"),
			accessKeyField: []byte(accessKey),
		},
	}
}

func newEngine(objects []runtime.Object, secrets ...*corev1.Secret) *Engine {
	listKinds := map[schema.GroupVersionResource]string{
		seaweedResource:    "SeaweedList",
		bucketResource:     "BucketList",
		identityResource:   "S3IdentityList",
		credentialResource: "S3CredentialsList",
		policyResource:     "S3PolicyList",
		bindingResource:    "S3PolicyBindingList",
	}
	kubeObjects := make([]runtime.Object, len(secrets))
	for i := range secrets {
		kubeObjects[i] = secrets[i]
	}
	return New(
		kubefake.NewClientset(kubeObjects...),
		dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...),
	)
}

func desiredUser(grants ...engine.RoleGrant) engine.DesiredUser {
	return engine.DesiredUser{
		Username: "alice",
		Roles:    grants,
		Password: engine.SecretKeyRef{
			Name: "alice-credentials",
			Key:  "password",
		},
		ManagedCredentials: true,
	}
}

func getTyped[T any](t *testing.T, e *Engine, gvr schema.GroupVersionResource, name string, out *T) {
	t.Helper()
	obj, err := e.dyn.Resource(gvr).Namespace(target.Namespace).Get(context.Background(), name, metav1.GetOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if err := fromUnstructured(obj, out); err != nil {
		t.Fatal(err)
	}
}

func TestRoles(t *testing.T) {
	e := New(nil, nil)
	got, err := e.NormalizeRoles([]engine.RoleGrant{
		{Role: RoleRead, Bucket: "uploads"},
		{Role: RoleReadWrite, Bucket: "uploads"},
		{Role: RoleRead, Bucket: "backups"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []engine.RoleGrant{
		{Role: RoleReadWrite, Bucket: "uploads"},
		{Role: RoleRead, Bucket: "backups"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("NormalizeRoles = %+v, want %+v", got, want)
	}

	for name, grants := range map[string][]engine.RoleGrant{
		"none":           nil,
		"unknown role":   {{Role: "owner", Bucket: "uploads"}},
		"missing bucket": {{Role: RoleRead}},
		"database":       {{Role: RoleRead, Database: "uploads", Bucket: "uploads"}},
		"uppercase":      {{Role: RoleRead, Bucket: "Uploads"}},
		"ip address":     {{Role: RoleRead, Bucket: "192.168.0.1"}},
		"double dot":     {{Role: RoleRead, Bucket: "photo..archive"}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := e.NormalizeRoles(grants); !errors.Is(err, engine.ErrInvalid) {
				t.Fatalf("error = %v, want ErrInvalid", err)
			}
		})
	}
}

func TestCredentialKindAndAccessKey(t *testing.T) {
	e := New(nil, nil)
	if got := engine.CredentialKindOf(e); got != engine.CredentialS3 {
		t.Fatalf("CredentialKindOf = %q", got)
	}
	data := map[string][]byte{"password": []byte("secret")}
	if err := e.PrepareCredentialSecret(context.Background(), target, "alice", "secret", data); err != nil {
		t.Fatal(err)
	}
	key := data[accessKeyField]
	if len(key) != accessKeyIDLength {
		t.Fatalf("access key length = %d", len(key))
	}
	if err := e.PrepareCredentialSecret(context.Background(), target, "alice", "new-secret", data); err != nil {
		t.Fatal(err)
	}
	if got := data[accessKeyField]; !reflect.DeepEqual(got, key) {
		t.Fatalf("access key changed on secret rotation: %q -> %q", key, got)
	}
	if err := e.ValidateUser(engine.DesiredUser{
		Username:           "admin",
		Roles:              []engine.RoleGrant{{Role: RoleRead, Bucket: "uploads"}},
		ManagedCredentials: true,
	}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("reserved user error = %v", err)
	}
	if err := e.ValidateUser(engine.DesiredUser{
		Username:           "alice",
		Roles:              []engine.RoleGrant{{Role: RoleRead, Bucket: "uploads"}},
		ManagedCredentials: false,
	}); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("external credentials error = %v", err)
	}
	if err := e.ValidateUser(engine.DesiredUser{
		Username: "alice", ManagedCredentials: true,
	}); err != nil {
		t.Fatalf("bootstrap identity with temporarily withheld roles: %v", err)
	}
}

func TestSyncUsers(t *testing.T) {
	e := newEngine([]runtime.Object{seaweed()}, credentialsSecret("AKIAALICE00000000001"))
	user := desiredUser(engine.RoleGrant{Role: RoleReadWrite, Bucket: "uploads"})

	if err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{user}); err != nil {
		t.Fatal(err)
	}

	identity := &seaweedv1.S3Identity{}
	getTyped(t, e, identityResource, identityName(target, user.Username), identity)
	if identity.Spec.Name != identity.Name {
		t.Fatalf("identity name = %q", identity.Spec.Name)
	}
	if identity.Spec.Account == nil || identity.Spec.Account.DisplayName != user.Username {
		t.Fatalf("display name = %+v", identity.Spec.Account)
	}
	if identity.Labels[managedByLabel] != managedBy {
		t.Fatalf("identity labels = %v", identity.Labels)
	}

	credentials := &seaweedv1.S3Credentials{}
	getTyped(t, e, credentialResource, credentialsName(target, user.Username), credentials)
	if credentials.Spec.SecretRef.Name != user.Password.Name ||
		credentials.Spec.SecretRef.AccessKeyField != accessKeyField ||
		credentials.Spec.SecretRef.SecretKeyField != user.Password.Key {
		t.Fatalf("secretRef = %+v", credentials.Spec.SecretRef)
	}

	policy := &seaweedv1.S3Policy{}
	getTyped(t, e, policyResource, policyName(target, user.Username, user.Roles[0]), policy)
	if len(policy.Spec.Statements) != 2 {
		t.Fatalf("statements = %+v", policy.Spec.Statements)
	}
	if !reflect.DeepEqual(policy.Spec.Statements[0].Resources, []string{"uploads"}) {
		t.Fatalf("bucket resources = %v", policy.Spec.Statements[0].Resources)
	}

	binding := &seaweedv1.S3PolicyBinding{}
	getTyped(t, e, bindingResource, bindingName(target, user.Username, user.Roles[0]), binding)
	if len(binding.Spec.Subjects) != 1 ||
		binding.Spec.Subjects[0].Kind != seaweedv1.S3SubjectKindIdentity ||
		binding.Spec.Subjects[0].Name != identity.Name {
		t.Fatalf("binding subject = %+v", binding.Spec.Subjects)
	}

	users, err := e.ListUsers(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != user.Username ||
		!reflect.DeepEqual(users[0].Roles, user.Roles) {
		t.Fatalf("ListUsers = %+v", users)
	}
}

func TestSyncRejectsForeignIdentityResource(t *testing.T) {
	foreign := mustUnstructured(&seaweedv1.S3Identity{
		TypeMeta: metav1.TypeMeta{
			APIVersion: seaweedv1.GroupVersion.String(),
			Kind:       "S3Identity",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      identityName(target, "alice"),
			Namespace: target.Namespace,
		},
		Spec: seaweedv1.S3IdentitySpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: target.Instance},
			Name:       "alice",
		},
	})
	e := newEngine([]runtime.Object{seaweed(), foreign}, credentialsSecret("AKIAALICE00000000001"))
	err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{
		desiredUser(engine.RoleGrant{Role: RoleRead, Bucket: "uploads"}),
	})
	if err == nil || !strings.Contains(err.Error(), "not managed by the plugin") {
		t.Fatalf("SyncUsers error = %v", err)
	}
}

func TestDropUserWaitsForIdentityFinalizer(t *testing.T) {
	e := newEngine([]runtime.Object{seaweed()}, credentialsSecret("AKIAALICE00000000001"))
	user := desiredUser(engine.RoleGrant{Role: RoleRead, Bucket: "uploads"})
	if err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{user}); err != nil {
		t.Fatal(err)
	}
	err := e.DropUser(context.Background(), target, user.Username)
	if !errors.Is(err, engine.ErrInProgress) {
		t.Fatalf("DropUser error = %v, want ErrInProgress", err)
	}
	if err := e.DropUser(context.Background(), target, user.Username); err != nil {
		t.Fatalf("second DropUser: %v", err)
	}
}

func TestDropUserWaitsForOwnedBucket(t *testing.T) {
	e := newEngine([]runtime.Object{seaweed()})
	user := desiredUser(engine.RoleGrant{Role: RoleRead, Bucket: "uploads"})
	if err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{user}); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncBucket(context.Background(), target, engine.DesiredBucket{
		Name: "uploads", Owner: user.Username,
	}); !errors.Is(err, engine.ErrInProgress) {
		t.Fatalf("SyncBucket error = %v", err)
	}
	err := e.DropUser(context.Background(), target, user.Username)
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), `bucket "uploads"`) {
		t.Fatalf("DropUser error = %v", err)
	}
}

func TestDropUserWaitsForCrossNamespaceOwnedBucket(t *testing.T) {
	foreign := mustUnstructured(&seaweedv1.Bucket{
		TypeMeta: metav1.TypeMeta{
			APIVersion: seaweedv1.GroupVersion.String(),
			Kind:       "Bucket",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "foreign-uploads",
			Namespace: "apps",
		},
		Spec: seaweedv1.BucketSpec{
			ClusterRef: &seaweedv1.SeaweedReference{
				Name:      target.Instance,
				Namespace: target.Namespace,
			},
			Name:  "uploads",
			Owner: identityName(target, "alice"),
		},
	})
	e := newEngine([]runtime.Object{seaweed(), foreign})
	err := e.DropUser(context.Background(), target, "alice")
	if !errors.Is(err, engine.ErrInProgress) || !strings.Contains(err.Error(), `bucket "uploads"`) {
		t.Fatalf("DropUser error = %v", err)
	}
}

func TestConnectionAndNotReady(t *testing.T) {
	e := newEngine([]runtime.Object{seaweed()}, connectionSecret())
	uri, err := e.ConnectionURI(context.Background(), target, "alice", "secret")
	if err != nil || uri != "https://object-store-s3.ns.svc:8334" {
		t.Fatalf("ConnectionURI = %q, %v", uri, err)
	}
	props, err := e.ConnectionProperties(context.Background(), target)
	if err != nil || props["forcePathStyle"] != "true" || props["region"] != "us-east-1" {
		t.Fatalf("ConnectionProperties = %v, %v", props, err)
	}

	missing := newEngine(nil)
	_, err = missing.ListUsers(context.Background(), target)
	if !errors.Is(err, engine.ErrNotReady) {
		t.Fatalf("ListUsers error = %v, want ErrNotReady", err)
	}
}

func TestBucketLifecycle(t *testing.T) {
	e := newEngine([]runtime.Object{seaweed()})
	desired := engine.DesiredBucket{Name: "uploads", Owner: "alice"}
	if err := e.ValidateBucket(desired.Name); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncBucket(context.Background(), target, desired); !errors.Is(err, engine.ErrInProgress) {
		t.Fatalf("initial SyncBucket error = %v, want ErrInProgress", err)
	}
	bucket := &seaweedv1.Bucket{}
	getTyped(t, e, bucketResource, bucketName(target, desired.Name), bucket)
	if bucket.Spec.Owner != identityName(target, desired.Owner) {
		t.Fatalf("bucket owner = %q, want %q", bucket.Spec.Owner, identityName(target, desired.Owner))
	}
	bucket.Status = seaweedv1.BucketStatus{
		Phase:              seaweedv1.BucketPhaseReady,
		ObservedGeneration: bucket.Generation,
		OwnerIdentity:      bucket.Spec.Owner,
	}
	updated, err := toUnstructured(bucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.dyn.Resource(bucketResource).Namespace(target.Namespace).Update(
		context.Background(), updated, metav1.UpdateOptions{},
	); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncBucket(context.Background(), target, desired); err != nil {
		t.Fatalf("ready SyncBucket: %v", err)
	}
	buckets, err := e.ListBuckets(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(buckets) != 1 || buckets[0].Name != desired.Name {
		t.Fatalf("ListBuckets = %+v", buckets)
	}
	if err := e.ReleaseBucket(context.Background(), target, desired.Name, true); !errors.Is(err, engine.ErrInProgress) {
		t.Fatalf("ReleaseBucket error = %v, want ErrInProgress", err)
	}
	actions := e.dyn.(*dynamicfake.FakeDynamicClient).Actions()
	foundDeletePolicy := false
	for _, action := range actions {
		update, ok := action.(k8stesting.UpdateAction)
		if !ok || update.GetResource() != bucketResource {
			continue
		}
		value := update.GetObject().(*unstructured.Unstructured)
		updatedBucket := &seaweedv1.Bucket{}
		if err := fromUnstructured(value, updatedBucket); err != nil {
			t.Fatal(err)
		}
		if updatedBucket.Spec.ReclaimPolicy == seaweedv1.BucketReclaimDelete {
			foundDeletePolicy = true
		}
	}
	if !foundDeletePolicy {
		t.Fatal("ReleaseBucket did not update the native bucket reclaim policy to Delete")
	}
}

func TestBucketAlreadyExistsIsNotTakenOver(t *testing.T) {
	e := newEngine([]runtime.Object{seaweed()})
	desired := engine.DesiredBucket{Name: "uploads"}
	if err := e.SyncBucket(context.Background(), target, desired); !errors.Is(err, engine.ErrInProgress) {
		t.Fatalf("initial SyncBucket error = %v", err)
	}
	bucket := &seaweedv1.Bucket{}
	getTyped(t, e, bucketResource, bucketName(target, desired.Name), bucket)
	bucket.Status = seaweedv1.BucketStatus{
		Phase:              seaweedv1.BucketPhaseFailed,
		ObservedGeneration: bucket.Generation,
		Conditions: []metav1.Condition{{
			Type:    seaweedv1.BucketConditionBucketAlreadyExists,
			Status:  metav1.ConditionTrue,
			Message: "bucket already exists",
		}},
	}
	updated, err := toUnstructured(bucket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.dyn.Resource(bucketResource).Namespace(target.Namespace).Update(
		context.Background(), updated, metav1.UpdateOptions{},
	); err != nil {
		t.Fatal(err)
	}
	if err := e.SyncBucket(context.Background(), target, desired); !errors.Is(err, engine.ErrInvalid) {
		t.Fatalf("SyncBucket takeover error = %v, want ErrInvalid", err)
	}
}

func TestPendingReason(t *testing.T) {
	e := newEngine([]runtime.Object{seaweed()}, credentialsSecret("AKIAALICE00000000001"))
	user := desiredUser(engine.RoleGrant{Role: RoleRead, Bucket: "uploads"})
	if err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{user}); err != nil {
		t.Fatal(err)
	}
	if reason := e.UserProblem(target, user.Username); !strings.Contains(reason, "become Ready") {
		t.Fatalf("UserProblem = %q", reason)
	}
}

func TestPendingReasonIncludesPolicyFailureAndGeneration(t *testing.T) {
	e := newEngine([]runtime.Object{seaweed()}, credentialsSecret("AKIAALICE00000000001"))
	user := desiredUser(engine.RoleGrant{Role: RoleRead, Bucket: "uploads"})
	if err := e.SyncUsers(context.Background(), target, []engine.DesiredUser{user}); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct {
		gvr      schema.GroupVersionResource
		name     string
		setReady func(*unstructured.Unstructured)
	}{
		{identityResource, identityName(target, user.Username), func(obj *unstructured.Unstructured) {
			identity := &seaweedv1.S3Identity{}
			if err := fromUnstructured(obj, identity); err != nil {
				t.Fatal(err)
			}
			identity.Generation = 1
			identity.Status = seaweedv1.S3IdentityStatus{
				Phase: seaweedv1.S3PhaseReady, ObservedGeneration: 1,
			}
			ready, err := toUnstructured(identity)
			if err != nil {
				t.Fatal(err)
			}
			obj.Object = ready.Object
		}},
		{credentialResource, credentialsName(target, user.Username), func(obj *unstructured.Unstructured) {
			credentials := &seaweedv1.S3Credentials{}
			if err := fromUnstructured(obj, credentials); err != nil {
				t.Fatal(err)
			}
			credentials.Generation = 1
			credentials.Status = seaweedv1.S3CredentialsStatus{
				Phase: seaweedv1.S3PhaseReady, ObservedGeneration: 1,
			}
			ready, err := toUnstructured(credentials)
			if err != nil {
				t.Fatal(err)
			}
			obj.Object = ready.Object
		}},
		{bindingResource, bindingName(target, user.Username, user.Roles[0]), func(obj *unstructured.Unstructured) {
			binding := &seaweedv1.S3PolicyBinding{}
			if err := fromUnstructured(obj, binding); err != nil {
				t.Fatal(err)
			}
			binding.Generation = 1
			binding.Status = seaweedv1.S3PolicyBindingStatus{
				Phase: seaweedv1.S3PhaseReady, ObservedGeneration: 1,
			}
			ready, err := toUnstructured(binding)
			if err != nil {
				t.Fatal(err)
			}
			obj.Object = ready.Object
		}},
	} {
		obj, err := e.dyn.Resource(item.gvr).Namespace(target.Namespace).Get(
			context.Background(), item.name, metav1.GetOptions{},
		)
		if err != nil {
			t.Fatal(err)
		}
		item.setReady(obj)
		if _, err := e.dyn.Resource(item.gvr).Namespace(target.Namespace).Update(
			context.Background(), obj, metav1.UpdateOptions{},
		); err != nil {
			t.Fatal(err)
		}
	}
	policy := &seaweedv1.S3Policy{}
	getTyped(t, e, policyResource, policyName(target, user.Username, user.Roles[0]), policy)
	policy.Generation = 2
	policy.Status = seaweedv1.S3PolicyStatus{
		Phase:              seaweedv1.S3PhaseFailed,
		ObservedGeneration: 2,
		Conditions: []metav1.Condition{{
			Type:    seaweedv1.S3ConditionReady,
			Status:  metav1.ConditionFalse,
			Message: "IAM policy was rejected",
		}},
	}
	updated, err := toUnstructured(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.dyn.Resource(policyResource).Namespace(target.Namespace).Update(
		context.Background(), updated, metav1.UpdateOptions{},
	); err != nil {
		t.Fatal(err)
	}
	if reason := e.userProblem(context.Background(), target, user.Username); reason != "IAM policy was rejected" {
		t.Fatalf("policy failure = %q", reason)
	}

	policy.Status.Phase = seaweedv1.S3PhaseReady
	policy.Status.ObservedGeneration = 1
	policy.Status.Conditions = []metav1.Condition{{
		Type: seaweedv1.S3ConditionReady, Status: metav1.ConditionTrue,
	}}
	updated, err = toUnstructured(policy)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.dyn.Resource(policyResource).Namespace(target.Namespace).Update(
		context.Background(), updated, metav1.UpdateOptions{},
	); err != nil {
		t.Fatal(err)
	}
	if reason := e.userProblem(context.Background(), target, user.Username); !strings.Contains(reason, "generation 2 to be observed") {
		t.Fatalf("stale policy generation = %q", reason)
	}
}

func TestForeignIdentityIsListed(t *testing.T) {
	foreign := mustUnstructured(&seaweedv1.S3Identity{
		TypeMeta: metav1.TypeMeta{
			APIVersion: seaweedv1.GroupVersion.String(),
			Kind:       "S3Identity",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      "alice",
			Namespace: target.Namespace,
		},
		Spec: seaweedv1.S3IdentitySpec{
			SeaweedRef: seaweedv1.SeaweedReference{Name: target.Instance},
			Name:       "alice",
		},
	})
	e := newEngine([]runtime.Object{seaweed(), foreign})
	users, err := e.ListUsers(context.Background(), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(users) != 1 || users[0].Username != "alice" {
		t.Fatalf("ListUsers = %+v", users)
	}
}
