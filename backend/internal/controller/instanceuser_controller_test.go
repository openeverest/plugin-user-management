package controller

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"github.com/openeverest/plugin-user-management/backend/api/v1alpha1"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

const ns = "ns"

type fakeEngine struct {
	dbUsers       []engine.User
	listErr       error
	dropErr       error
	pendingReason string
	syncErr       error
	synced        [][]engine.DesiredUser
	dropped       []string
	uriCalls      int
}

func (f *fakeEngine) Roles() []engine.Role { return nil }
func (f *fakeEngine) AuthDatabase() string { return "admin" }

func (f *fakeEngine) NormalizeRoles(grants []engine.RoleGrant) ([]engine.RoleGrant, error) {
	for _, g := range grants {
		if g.Role == "root" {
			return nil, errors.Join(engine.ErrInvalid, errors.New("role \"root\" can't be granted"))
		}
	}
	return grants, nil
}

func (f *fakeEngine) ValidateUser(user engine.DesiredUser) error {
	if user.Username == "reserved" {
		return errors.Join(engine.ErrInvalid, errors.New("user \"reserved\" is reserved"))
	}
	return nil
}

func (f *fakeEngine) PendingReason(context.Context, engine.Target, string) string {
	return f.pendingReason
}

func (f *fakeEngine) SyncUsers(_ context.Context, _ engine.Target, users []engine.DesiredUser) error {
	f.synced = append(f.synced, users)
	return f.syncErr
}

func (f *fakeEngine) ListUsers(context.Context, engine.Target) ([]engine.User, error) {
	return f.dbUsers, f.listErr
}

func (f *fakeEngine) DropUser(_ context.Context, _ engine.Target, username string) error {
	f.dropped = append(f.dropped, username)
	return f.dropErr
}

func (f *fakeEngine) ConnectionURI(_ context.Context, _ engine.Target, username, password string) (string, error) {
	f.uriCalls++
	return "mongodb://" + username + ":" + password + "@host/", nil
}

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	if err := v1alpha1.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	s.AddKnownTypeWithName(InstanceGVK, &unstructured.Unstructured{})
	s.AddKnownTypeWithName(InstanceGVK.GroupVersion().WithKind("InstanceList"), &unstructured.UnstructuredList{})
	return s
}

func instance(provider string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"providerRef": map[string]any{"name": provider}},
	}}
	u.SetGroupVersionKind(InstanceGVK)
	u.SetNamespace(ns)
	u.SetName("mongo")
	u.SetUID("instance-uid")
	return u
}

var created = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

func dbUser(name, username string, roles ...v1alpha1.RoleGrant) *v1alpha1.InstanceUser {
	created = created.Add(time.Minute)
	return &v1alpha1.InstanceUser{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns, CreationTimestamp: metav1.NewTime(created)},
		Spec: v1alpha1.InstanceUserSpec{
			InstanceRef: v1alpha1.InstanceReference{Name: "mongo"},
			Username:    username,
			Roles:       roles,
		},
	}
}

var readShop = v1alpha1.RoleGrant{Role: "read", Database: "shop"}

type harness struct {
	t      *testing.T
	client client.Client
	engine *fakeEngine
	r      *Reconciler
}

func newHarness(t *testing.T, objs ...client.Object) *harness {
	c := fake.NewClientBuilder().
		WithScheme(newScheme(t)).
		WithObjects(objs...).
		WithStatusSubresource(&v1alpha1.InstanceUser{}).
		WithIndex(&v1alpha1.InstanceUser{}, InstanceIndex, func(obj client.Object) []string {
			return []string{obj.(*v1alpha1.InstanceUser).Spec.InstanceRef.Name}
		}).
		Build()
	eng := &fakeEngine{}
	return &harness{t: t, client: c, engine: eng, r: &Reconciler{Client: c, Engines: map[string]engine.Engine{"percona-server-mongodb": eng}}}
}

func (h *harness) reconcile() ctrl.Result {
	h.t.Helper()
	res, err := h.r.Reconcile(context.Background(), ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "mongo"}})
	if err != nil {
		h.t.Fatalf("reconcile: %v", err)
	}
	return res
}

func (h *harness) get(name string) *v1alpha1.InstanceUser {
	h.t.Helper()
	u := &v1alpha1.InstanceUser{}
	if err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, u); err != nil {
		h.t.Fatalf("get %s: %v", name, err)
	}
	return u
}

func (h *harness) secret(name string) *corev1.Secret {
	h.t.Helper()
	s := &corev1.Secret{}
	if err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: name}, s); err != nil {
		h.t.Fatalf("get secret %s: %v", name, err)
	}
	return s
}

func TestNewUserLifecycle(t *testing.T) {
	h := newHarness(t, instance("percona-server-mongodb"), dbUser("app", "app", readShop))

	if res := h.reconcile(); res.RequeueAfter == 0 {
		t.Error("pending user must be requeued")
	}
	u := h.get("app")
	if u.Status.Phase != v1alpha1.PhasePending || !u.Status.Declared || u.Status.CredentialsSecret != "app-credentials" {
		t.Errorf("status = %+v", u.Status)
	}
	if len(u.Finalizers) != 1 || u.Labels[InstanceLabel] != "mongo" || len(u.OwnerReferences) != 1 || u.OwnerReferences[0].UID != "instance-uid" {
		t.Errorf("metadata = finalizers %v labels %v owners %v", u.Finalizers, u.Labels, u.OwnerReferences)
	}

	secret := h.secret("app-credentials")
	pwd := string(secret.Data["password"])
	if len(pwd) < 16 || string(secret.Data["username"]) != "app" || string(secret.Data["uri"]) != "mongodb://app:"+pwd+"@host/" {
		t.Errorf("secret data = %v", secret.Data)
	}
	if len(secret.OwnerReferences) != 1 || secret.OwnerReferences[0].Name != "app" {
		t.Errorf("secret owners = %v", secret.OwnerReferences)
	}
	want := []engine.DesiredUser{{
		Username:           "app",
		Roles:              []engine.RoleGrant{{Role: "read", Database: "shop"}},
		Password:           engine.SecretKeyRef{Name: "app-credentials", Key: "password"},
		ManagedCredentials: true,
	}}
	if len(h.engine.synced) != 1 || !reflect.DeepEqual(h.engine.synced[0], want) {
		t.Fatalf("synced = %+v", h.engine.synced)
	}

	// The operator created the user.
	h.engine.dbUsers = []engine.User{{Username: "app", Database: "admin", Roles: []engine.RoleGrant{{Role: "read", Database: "shop"}}}}
	if res := h.reconcile(); res.RequeueAfter != 0 {
		t.Error("ready user must not be requeued")
	}
	if u := h.get("app"); u.Status.Phase != v1alpha1.PhaseReady || u.Status.Conditions[0].Status != metav1.ConditionTrue {
		t.Errorf("status = %+v", u.Status)
	}
	if got := string(h.secret("app-credentials").Data["password"]); got != pwd {
		t.Error("password changed on a later reconcile")
	}

	// Deleting the InstanceUser drops it from the declared users and the database.
	if err := h.client.Delete(context.Background(), h.get("app")); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	if last := h.engine.synced[len(h.engine.synced)-1]; len(last) != 0 {
		t.Errorf("deleted user still declared: %+v", last)
	}
	if !reflect.DeepEqual(h.engine.dropped, []string{"app"}) {
		t.Errorf("dropped = %v", h.engine.dropped)
	}
	err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "app"}, &v1alpha1.InstanceUser{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("InstanceUser not released: %v", err)
	}
}

func TestRolesChangeUntilApplied(t *testing.T) {
	u := dbUser("app", "app", v1alpha1.RoleGrant{Role: "readWrite", Database: "shop"})
	u.Status.Declared = true
	h := newHarness(t, instance("percona-server-mongodb"), u)
	h.engine.dbUsers = []engine.User{{Username: "app", Database: "admin", Roles: []engine.RoleGrant{{Role: "read", Database: "shop"}}}}
	h.reconcile()
	if s := h.get("app").Status; s.Phase != v1alpha1.PhasePending || s.Message != "waiting for the operator to apply the user" {
		t.Errorf("status = %+v, want Pending while roles differ", s)
	}

	h.engine.pendingReason = "secret has no username"
	h.reconcile()
	if s := h.get("app").Status; s.Phase != v1alpha1.PhasePending || s.Message != "secret has no username" {
		t.Errorf("status = %+v, want the operator's reason", s)
	}
}

type problemEngine struct {
	*fakeEngine
	problem string
}

func (p *problemEngine) UserProblem(engine.Target, string) string { return p.problem }

func TestUserProblemKeepsMatchingUserPending(t *testing.T) {
	u := dbUser("app", "app", readShop)
	u.Status.Declared = true
	h := newHarness(t, instance("percona-server-mongodb"), u)
	eng := &problemEngine{fakeEngine: h.engine, problem: "password rejected"}
	h.r.Engines["percona-server-mongodb"] = eng
	h.engine.dbUsers = []engine.User{{Username: "app", Database: "admin", Roles: []engine.RoleGrant{{Role: "read", Database: "shop"}}}}
	h.reconcile()
	if s := h.get("app").Status; s.Phase != v1alpha1.PhasePending || s.Message != "password rejected" {
		t.Errorf("status = %+v, want Pending with the engine's problem", s)
	}

	eng.problem = ""
	h.reconcile()
	if s := h.get("app").Status; s.Phase != v1alpha1.PhaseReady {
		t.Errorf("status = %+v, want Ready once applied", s)
	}
}

func TestKeepsDeletingUserUntilDropped(t *testing.T) {
	deleted := dbUser("old", "old", readShop)
	deleted.Status.Declared = true
	deleted.Finalizers = []string{Finalizer}
	h := newHarness(t, instance("percona-server-mongodb"), deleted)
	if err := h.client.Delete(context.Background(), h.get("old")); err != nil {
		t.Fatal(err)
	}
	h.engine.dropErr = errors.Join(engine.ErrInProgress, errors.New("role owns objects"))

	if res := h.reconcile(); res.RequeueAfter == 0 {
		t.Error("drop in progress must be requeued")
	}
	u := h.get("old")
	if len(u.Finalizers) != 1 || u.Status.Phase != v1alpha1.PhaseDeleting || u.Status.Message != "in progress\nrole owns objects" {
		t.Errorf("finalizers %v, status %+v", u.Finalizers, u.Status)
	}

	h.engine.dropErr = nil
	h.reconcile()
	err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "old"}, &v1alpha1.InstanceUser{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("InstanceUser not released once dropped: %v", err)
	}
}

func TestRejectsTakeoverAndInvalidUsers(t *testing.T) {
	h := newHarness(t, instance("percona-server-mongodb"),
		dbUser("existing", "boss", readShop),
		dbUser("system", "userAdmin", readShop),
		dbUser("root", "x", v1alpha1.RoleGrant{Role: "root"}),
		dbUser("reserved", "reserved", readShop),
		dbUser("first", "dup", readShop),
		dbUser("second", "dup", readShop),
	)
	h.engine.dbUsers = []engine.User{
		{Username: "boss", Database: "admin"},
		{Username: "userAdmin", Database: "admin", System: true},
	}
	h.reconcile()

	for name, want := range map[string]string{
		"existing": `user "boss" already exists in the database and isn't managed by this InstanceUser`,
		"system":   `user "userAdmin" is reserved for the operator`,
		"reserved": "invalid user\nuser \"reserved\" is reserved",
		"second":   `another InstanceUser already declares user "dup" on this instance`,
	} {
		if s := h.get(name).Status; s.Phase != v1alpha1.PhaseFailed || s.Message != want || s.Declared {
			t.Errorf("%s: status = %+v", name, s)
		}
	}
	if s := h.get("root").Status; s.Phase != v1alpha1.PhaseFailed {
		t.Errorf("root: status = %+v", s)
	}
	if got := h.engine.synced[0]; len(got) != 1 || got[0].Username != "dup" {
		t.Errorf("synced = %+v", got)
	}
	if s := h.get("first").Status; s.Phase != v1alpha1.PhasePending {
		t.Errorf("first: status = %+v", s)
	}
}

func TestRefreshesURIWhenPasswordEdited(t *testing.T) {
	h := newHarness(t, instance("percona-server-mongodb"), dbUser("app", "app", readShop))
	h.reconcile()
	secret := h.secret("app-credentials")
	orig := secret.DeepCopy()
	secret.Data["password"] = []byte("edited-password")
	if err := h.client.Patch(context.Background(), secret, client.MergeFrom(orig)); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	if got := string(h.secret("app-credentials").Data["uri"]); got != "mongodb://app:edited-password@host/" {
		t.Errorf("uri = %s", got)
	}
}

func TestKeepsChosenPassword(t *testing.T) {
	// The API writes the Secret with the user's chosen password before the InstanceUser.
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "app-credentials", Namespace: ns},
		Data:       map[string][]byte{"password": []byte("chosen-password")},
	}
	h := newHarness(t, instance("percona-server-mongodb"), dbUser("app", "app", readShop), secret)
	h.reconcile()
	got := h.secret("app-credentials")
	if string(got.Data["password"]) != "chosen-password" || len(got.OwnerReferences) != 1 {
		t.Errorf("secret = %+v", got)
	}
}

func TestOwnPasswordSecretIsNotTouched(t *testing.T) {
	u := dbUser("app", "app", readShop)
	u.Spec.PasswordSecretRef = &v1alpha1.SecretKeySelector{Name: "my-secret", Key: "pw"}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "my-secret", Namespace: ns},
		Data:       map[string][]byte{"pw": []byte("secret")},
	}
	h := newHarness(t, instance("percona-server-mongodb"), u, secret)
	h.reconcile()
	if got := h.engine.synced[0][0].Password; got != (engine.SecretKeyRef{Name: "my-secret", Key: "pw"}) {
		t.Errorf("password ref = %+v", got)
	}
	if h.get("app").Status.CredentialsSecret != "my-secret" {
		t.Error("credentialsSecret should point to the user's Secret")
	}
	err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "app-credentials"}, &corev1.Secret{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("generated a Secret for a user with its own: %v", err)
	}
	live := h.secret("my-secret")
	if live.Labels[InstanceLabel] != "mongo" {
		t.Errorf("external credentials Secret labels = %+v", live.Labels)
	}
	if len(live.OwnerReferences) != 0 || string(live.Data["pw"]) != "secret" {
		t.Errorf("external credentials Secret was claimed or changed: %+v", live)
	}
}

func TestOwnPasswordSecretCannotBelongToAnotherInstance(t *testing.T) {
	u := dbUser("app", "app", readShop)
	u.Spec.PasswordSecretRef = &v1alpha1.SecretKeySelector{Name: "my-secret"}
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "my-secret",
			Namespace: ns,
			Labels:    map[string]string{InstanceLabel: "another-instance"},
		},
	}
	h := newHarness(t, instance("percona-server-mongodb"), u, secret)
	h.reconcile()

	if got := h.get("app").Status; got.Phase != v1alpha1.PhaseFailed ||
		!strings.Contains(got.Message, `already belongs to instance "another-instance"`) {
		t.Fatalf("status = %+v", got)
	}
	if len(h.engine.synced) != 1 || len(h.engine.synced[0]) != 0 {
		t.Fatalf("synced users = %+v", h.engine.synced)
	}
}

func TestDatabaseNotReady(t *testing.T) {
	deleted := dbUser("old", "old", readShop)
	deleted.Status.Declared = true
	deleted.Finalizers = []string{Finalizer}
	h := newHarness(t, instance("percona-server-mongodb"), dbUser("app", "app", readShop), deleted)
	if err := h.client.Delete(context.Background(), h.get("old")); err != nil {
		t.Fatal(err)
	}
	h.engine.listErr = engine.ErrNotReady

	if res := h.reconcile(); res.RequeueAfter == 0 {
		t.Error("expected requeue")
	}
	if s := h.get("app").Status; s.Phase != v1alpha1.PhasePending {
		t.Errorf("status = %+v", s)
	}
	if len(h.engine.dropped) != 0 || len(h.get("old").Finalizers) != 1 {
		t.Error("deleted user released before it could be dropped")
	}
}

func TestDatabaseNotReadyReleasesUndeclaredUser(t *testing.T) {
	never := dbUser("never", "never", readShop)
	never.Finalizers = []string{Finalizer}
	h := newHarness(t, instance("percona-server-mongodb"), never)
	if err := h.client.Delete(context.Background(), h.get("never")); err != nil {
		t.Fatal(err)
	}
	h.engine.listErr = engine.ErrNotReady

	h.reconcile()
	err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "never"}, &v1alpha1.InstanceUser{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("undeclared user not released: %v", err)
	}
	if len(h.engine.dropped) != 0 {
		t.Errorf("dropped = %v", h.engine.dropped)
	}
}

func TestInstanceGone(t *testing.T) {
	deleted := dbUser("old", "old", readShop)
	deleted.Status.Declared = true
	deleted.Finalizers = []string{Finalizer}
	h := newHarness(t, dbUser("app", "app", readShop), deleted)
	if err := h.client.Delete(context.Background(), h.get("old")); err != nil {
		t.Fatal(err)
	}
	h.reconcile()

	if s := h.get("app").Status; s.Phase != v1alpha1.PhaseFailed || s.Message != `instance "mongo" not found` {
		t.Errorf("status = %+v", s)
	}
	err := h.client.Get(context.Background(), types.NamespacedName{Namespace: ns, Name: "old"}, &v1alpha1.InstanceUser{})
	if !apierrors.IsNotFound(err) {
		t.Errorf("deleted user not released: %v", err)
	}
}

func TestUnsupportedProvider(t *testing.T) {
	h := newHarness(t, instance("mssql"), dbUser("app", "app", readShop))
	h.reconcile()
	if s := h.get("app").Status; s.Phase != v1alpha1.PhaseFailed || s.Message != `user management is not supported for provider "mssql"` {
		t.Errorf("status = %+v", s)
	}
}

// A user is claimed before it's declared, so one whose declaration was
// interrupted before its status was written is still dropped when deleted.
func TestClaimsUserBeforeDeclaring(t *testing.T) {
	h := newHarness(t, instance("percona-server-mongodb"), dbUser("app", "app", readShop))
	h.engine.syncErr = errors.New("plugin stopped")
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: ns, Name: "mongo"}}
	if _, err := h.r.Reconcile(context.Background(), req); err == nil {
		t.Fatal("expected the sync error")
	}
	if !h.get("app").Status.Declared {
		t.Fatal("user declared in the database without being claimed")
	}

	h.engine.syncErr = nil
	h.engine.dbUsers = []engine.User{{Username: "app", Database: "admin"}}
	h.reconcile()
	if s := h.get("app").Status; s.Phase == v1alpha1.PhaseFailed {
		t.Errorf("claimed user failed as a takeover: %+v", s)
	}
	if err := h.client.Delete(context.Background(), h.get("app")); err != nil {
		t.Fatal(err)
	}
	h.reconcile()
	if !reflect.DeepEqual(h.engine.dropped, []string{"app"}) {
		t.Errorf("dropped = %v", h.engine.dropped)
	}
}

// The cache can lag behind the claim: a user that looks unclaimed there is
// neither failed as a takeover nor released without being dropped.
func TestClaimNotInCacheYet(t *testing.T) {
	stale := func(name string, deleting bool) (*v1alpha1.InstanceUser, *v1alpha1.InstanceUser) {
		cached := dbUser(name, name, readShop)
		cached.UID = types.UID(name)
		if deleting {
			cached.Finalizers = []string{Finalizer}
		}
		live := cached.DeepCopy()
		live.Status.Declared = true
		return cached, live
	}
	app, liveApp := stale("app", false)
	old, liveOld := stale("old", true)
	h := newHarness(t, instance("percona-server-mongodb"), app, old)
	h.r.Reader = fake.NewClientBuilder().WithScheme(newScheme(t)).WithObjects(liveApp, liveOld).Build()
	if err := h.client.Delete(context.Background(), h.get("old")); err != nil {
		t.Fatal(err)
	}
	h.engine.dbUsers = []engine.User{
		{Username: "app", Database: "admin", Roles: []engine.RoleGrant{{Role: "read", Database: "shop"}}},
		{Username: "old", Database: "admin"},
	}
	h.reconcile()

	if s := h.get("app").Status; s.Phase != v1alpha1.PhaseReady {
		t.Errorf("app status = %+v", s)
	}
	if !reflect.DeepEqual(h.engine.dropped, []string{"old"}) {
		t.Errorf("dropped = %v", h.engine.dropped)
	}
}
