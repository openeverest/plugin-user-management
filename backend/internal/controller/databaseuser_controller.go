// Package controller reconciles DatabaseUsers into their instances' databases.
package controller

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/openeverest/plugin-user-management/backend/api/v1alpha1"
	"github.com/openeverest/plugin-user-management/backend/internal/engine"
	"github.com/openeverest/plugin-user-management/backend/internal/password"
)

const (
	// Finalizer drops the user from the database before the DatabaseUser goes away.
	Finalizer = "plugin-user-management.plugins.openeverest.io/drop-user"
	// InstanceIndex indexes DatabaseUsers by spec.instanceRef.name.
	InstanceIndex = "spec.instanceRef.name"
	// InstanceLabel lets kubectl select an instance's DatabaseUsers.
	InstanceLabel = "plugin-user-management.plugins.openeverest.io/instance"

	defaultPasswordKey = "password"
	pendingRequeue     = 10 * time.Second
)

// InstanceGVK is the OpenEverest Instance kind.
var InstanceGVK = schema.GroupVersionKind{Group: "core.openeverest.io", Version: "v1alpha1", Kind: "Instance"}

// CredentialsSecretName is the Secret holding a generated password.
func CredentialsSecretName(u *v1alpha1.DatabaseUser) string {
	return u.Name + "-credentials"
}

// PasswordRef returns where the user's password is kept.
func PasswordRef(u *v1alpha1.DatabaseUser) engine.SecretKeyRef {
	if ref := u.Spec.PasswordSecretRef; ref != nil {
		key := ref.Key
		if key == "" {
			key = defaultPasswordKey
		}
		return engine.SecretKeyRef{Name: ref.Name, Key: key}
	}
	return engine.SecretKeyRef{Name: CredentialsSecretName(u), Key: v1alpha1.SecretKeyPassword}
}

// ToEngineGrants converts API role grants to engine grants.
func ToEngineGrants(grants []v1alpha1.RoleGrant) []engine.RoleGrant {
	out := make([]engine.RoleGrant, 0, len(grants))
	for _, g := range grants {
		out = append(out, engine.RoleGrant{Role: g.Role, Database: g.Database})
	}
	return out
}

// Reconciler reconciles all DatabaseUsers of an instance at once: engines
// declare an instance's managed users as a whole (e.g. PerconaServerMongoDB
// spec.users), so requests are keyed by instance, not by DatabaseUser.
type Reconciler struct {
	Client client.Client
	// Engines by provider name (Instance spec.providerRef.name).
	Engines map[string]engine.Engine
}

// IndexInstance registers the InstanceIndex field index.
func IndexInstance(ctx context.Context, mgr ctrl.Manager) error {
	return mgr.GetFieldIndexer().IndexField(ctx, &v1alpha1.DatabaseUser{}, InstanceIndex, func(obj client.Object) []string {
		u, ok := obj.(*v1alpha1.DatabaseUser)
		if !ok {
			return nil
		}
		return []string{u.Spec.InstanceRef.Name}
	})
}

// SetupWithManager registers the reconciler.
func (r *Reconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		Named("databaseuser").
		Watches(&v1alpha1.DatabaseUser{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []reconcile.Request {
				u, ok := obj.(*v1alpha1.DatabaseUser)
				if !ok {
					return nil
				}
				return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: u.Namespace, Name: u.Spec.InstanceRef.Name}}}
			})).
		// A password edited in a credentials Secret needs its connection string refreshed.
		Watches(&corev1.Secret{}, handler.EnqueueRequestsFromMapFunc(
			func(_ context.Context, obj client.Object) []reconcile.Request {
				instance := obj.GetLabels()[InstanceLabel]
				if instance == "" {
					return nil
				}
				return []reconcile.Request{{NamespacedName: types.NamespacedName{Namespace: obj.GetNamespace(), Name: instance}}}
			})).
		Complete(r)
}

// userState is the outcome of reconciling one DatabaseUser.
type userState struct {
	user     *v1alpha1.DatabaseUser
	roles    []engine.RoleGrant
	password engine.SecretKeyRef
	phase    string
	message  string
	declared bool
}

func (s *userState) fail(message string) {
	s.phase, s.message = v1alpha1.PhaseFailed, message
}

func (s *userState) pending(message string) {
	s.phase, s.message = v1alpha1.PhasePending, message
}

// Reconcile reconciles the DatabaseUsers of the instance named by req.
func (r *Reconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	var list v1alpha1.DatabaseUserList
	if err := r.Client.List(ctx, &list, client.InNamespace(req.Namespace), client.MatchingFields{InstanceIndex: req.Name}); err != nil {
		return ctrl.Result{}, err
	}
	if len(list.Items) == 0 {
		return ctrl.Result{}, nil
	}
	// Oldest first: it wins when two DatabaseUsers declare the same username.
	sort.Slice(list.Items, func(i, j int) bool {
		a, b := list.Items[i], list.Items[j]
		if !a.CreationTimestamp.Equal(&b.CreationTimestamp) {
			return a.CreationTimestamp.Before(&b.CreationTimestamp)
		}
		return a.Name < b.Name
	})

	var live, deleting []*v1alpha1.DatabaseUser
	for i := range list.Items {
		if list.Items[i].DeletionTimestamp.IsZero() {
			live = append(live, &list.Items[i])
		} else {
			deleting = append(deleting, &list.Items[i])
		}
	}

	instance := &unstructured.Unstructured{}
	instance.SetGroupVersionKind(InstanceGVK)
	if err := r.Client.Get(ctx, req.NamespacedName, instance); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, err
		}
		// The database went away with the instance; nothing left to drop.
		return ctrl.Result{}, r.abandon(ctx, live, deleting, fmt.Sprintf("instance %q not found", req.Name))
	}
	if !instance.GetDeletionTimestamp().IsZero() {
		return ctrl.Result{}, r.abandon(ctx, live, deleting, fmt.Sprintf("instance %q is being deleted", req.Name))
	}
	provider, _, _ := unstructured.NestedString(instance.Object, "spec", "providerRef", "name")
	eng, ok := r.Engines[provider]
	if !ok {
		return ctrl.Result{}, r.abandon(ctx, live, deleting, fmt.Sprintf("user management is not supported for provider %q", provider))
	}
	target := engine.Target{Namespace: req.Namespace, Instance: req.Name}

	states := make([]*userState, 0, len(live))
	usernames := map[string]bool{}
	for _, u := range live {
		if err := r.ensureMetadata(ctx, u, instance); err != nil {
			return ctrl.Result{}, err
		}
		s := &userState{user: u, declared: u.Status.Declared, password: PasswordRef(u)}
		states = append(states, s)

		roles, err := eng.NormalizeRoles(ToEngineGrants(u.Spec.Roles))
		if err != nil {
			s.fail(err.Error())
			continue
		}
		s.roles = roles
		if err := eng.ValidateUser(engine.DesiredUser{Username: u.Spec.Username, Roles: roles, Password: s.password}); err != nil {
			s.fail(err.Error())
			continue
		}
		if usernames[u.Spec.Username] {
			s.fail(fmt.Sprintf("another DatabaseUser already declares user %q on this instance", u.Spec.Username))
			continue
		}
		usernames[u.Spec.Username] = true
		if u.Spec.PasswordSecretRef == nil {
			if err := r.ensureCredentials(ctx, eng, target, u); err != nil {
				return ctrl.Result{}, err
			}
		}
	}

	result, err := r.syncDatabase(ctx, eng, target, states, deleting)
	if err != nil {
		return ctrl.Result{}, err
	}
	for _, s := range states {
		if err := r.writeStatus(ctx, s); err != nil {
			return ctrl.Result{}, err
		}
		if s.phase == v1alpha1.PhasePending {
			result.RequeueAfter = pendingRequeue
		}
	}
	return result, nil
}

// syncDatabase declares the valid users, drops deleted ones and works out each user's phase.
func (r *Reconciler) syncDatabase(ctx context.Context, eng engine.Engine, target engine.Target, states []*userState, deleting []*v1alpha1.DatabaseUser) (ctrl.Result, error) {
	dbUsers, err := eng.ListUsers(ctx, target)
	if err != nil {
		if !errors.Is(err, engine.ErrNotReady) {
			return ctrl.Result{}, err
		}
		for _, s := range states {
			if s.phase == "" {
				s.pending(err.Error())
			}
		}
		// Deleted users stay until the database can be reached to drop them.
		return ctrl.Result{RequeueAfter: pendingRequeue}, nil
	}
	existing := map[string]engine.User{}
	for _, u := range dbUsers {
		if u.Database == eng.AuthDatabase() {
			existing[u.Username] = u
		}
	}

	var desired []engine.DesiredUser
	for _, s := range states {
		if s.phase != "" {
			continue
		}
		if dbUser, found := existing[s.user.Spec.Username]; found && !s.declared {
			if dbUser.System {
				s.fail(fmt.Sprintf("user %q is reserved for the operator", s.user.Spec.Username))
			} else {
				s.fail(fmt.Sprintf("user %q already exists in the database and isn't managed by this DatabaseUser", s.user.Spec.Username))
			}
			continue
		}
		desired = append(desired, engine.DesiredUser{Username: s.user.Spec.Username, Roles: s.roles, Password: s.password})
	}

	if err := eng.SyncUsers(ctx, target, desired); err != nil {
		return ctrl.Result{}, err
	}
	for _, s := range states {
		if s.phase != "" {
			continue
		}
		s.declared = true
		if dbUser, found := existing[s.user.Spec.Username]; found && engine.SameRoles(dbUser.Roles, s.roles) {
			s.phase = v1alpha1.PhaseReady
		} else {
			s.pending(pendingMessage(ctx, eng, target, s.user.Spec.Username))
		}
	}

	var result ctrl.Result
	for _, u := range deleting {
		if u.Status.Declared && !usernameDeclared(desired, u.Spec.Username) {
			err := eng.DropUser(ctx, target, u.Spec.Username)
			if errors.Is(err, engine.ErrInProgress) {
				if err := r.writeStatus(ctx, &userState{user: u, declared: true, phase: v1alpha1.PhaseDeleting, message: err.Error()}); err != nil {
					return ctrl.Result{}, err
				}
				result.RequeueAfter = pendingRequeue
				continue
			}
			if err != nil {
				return ctrl.Result{}, fmt.Errorf("drop user %q: %w", u.Spec.Username, err)
			}
			log.FromContext(ctx).Info("dropped database user", "user", u.Spec.Username, "databaseUser", u.Name)
		}
		if err := r.removeFinalizer(ctx, u); err != nil {
			return ctrl.Result{}, err
		}
	}
	return result, nil
}

// pendingMessage tells why a declared user isn't applied, with the
// operator's reason when the engine reports one.
func pendingMessage(ctx context.Context, eng engine.Engine, target engine.Target, username string) string {
	if reasoner, ok := eng.(engine.PendingReasoner); ok {
		if reason := reasoner.PendingReason(ctx, target, username); reason != "" {
			return reason
		}
	}
	return "waiting for the operator to apply the user"
}

func usernameDeclared(users []engine.DesiredUser, username string) bool {
	for _, u := range users {
		if u.Username == username {
			return true
		}
	}
	return false
}

// abandon fails live users and releases deleted ones when the instance's
// database can't be managed.
func (r *Reconciler) abandon(ctx context.Context, live, deleting []*v1alpha1.DatabaseUser, message string) error {
	for _, u := range deleting {
		if err := r.removeFinalizer(ctx, u); err != nil {
			return err
		}
	}
	for _, u := range live {
		s := &userState{user: u, declared: u.Status.Declared}
		s.fail(message)
		if err := r.writeStatus(ctx, s); err != nil {
			return err
		}
	}
	return nil
}

// ensureMetadata adds the finalizer, the instance label and an owner
// reference to the Instance, so deleting the instance deletes its users.
func (r *Reconciler) ensureMetadata(ctx context.Context, u *v1alpha1.DatabaseUser, instance *unstructured.Unstructured) error {
	orig := u.DeepCopy()
	controllerutil.AddFinalizer(u, Finalizer)
	labels := u.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[InstanceLabel] = instance.GetName()
	u.SetLabels(labels)
	if !hasOwner(u, instance.GetUID()) {
		// Not blockOwnerDeletion: that would need update on instances/finalizers.
		u.OwnerReferences = append(u.OwnerReferences, metav1.OwnerReference{
			APIVersion: InstanceGVK.GroupVersion().String(),
			Kind:       InstanceGVK.Kind,
			Name:       instance.GetName(),
			UID:        instance.GetUID(),
		})
	}
	if equalMetadata(orig, u) {
		return nil
	}
	return r.Client.Patch(ctx, u, client.MergeFrom(orig))
}

func hasOwner(obj metav1.Object, uid types.UID) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

func equalMetadata(a, b *v1alpha1.DatabaseUser) bool {
	return len(a.Finalizers) == len(b.Finalizers) &&
		a.Labels[InstanceLabel] == b.Labels[InstanceLabel] &&
		len(a.OwnerReferences) == len(b.OwnerReferences)
}

func (r *Reconciler) removeFinalizer(ctx context.Context, u *v1alpha1.DatabaseUser) error {
	if !controllerutil.ContainsFinalizer(u, Finalizer) {
		return nil
	}
	orig := u.DeepCopy()
	controllerutil.RemoveFinalizer(u, Finalizer)
	return client.IgnoreNotFound(r.Client.Patch(ctx, u, client.MergeFrom(orig)))
}

// ensureCredentials keeps the generated-password Secret: creates it with a
// random password, or adopts one the API created with a chosen password,
// and keeps its username and connection string current.
func (r *Reconciler) ensureCredentials(ctx context.Context, eng engine.Engine, target engine.Target, u *v1alpha1.DatabaseUser) error {
	key := types.NamespacedName{Namespace: u.Namespace, Name: CredentialsSecretName(u)}
	secret := &corev1.Secret{}
	err := r.Client.Get(ctx, key, secret)
	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}
	exists := err == nil
	orig := secret.DeepCopy()
	if !exists {
		secret.Name, secret.Namespace = key.Name, key.Namespace
	}
	if secret.Data == nil {
		secret.Data = map[string][]byte{}
	}
	if len(secret.Data[v1alpha1.SecretKeyPassword]) == 0 {
		pwd, err := password.Generate()
		if err != nil {
			return err
		}
		secret.Data[v1alpha1.SecretKeyPassword] = []byte(pwd)
	}
	secret.Data[v1alpha1.SecretKeyUsername] = []byte(u.Spec.Username)
	// Unknown until the database is reachable; filled in on a later reconcile.
	if uri, err := eng.ConnectionURI(ctx, target, u.Spec.Username, string(secret.Data[v1alpha1.SecretKeyPassword])); err == nil {
		secret.Data[v1alpha1.SecretKeyURI] = []byte(uri)
	}
	labels := secret.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}
	labels[InstanceLabel] = target.Instance
	secret.SetLabels(labels)
	if err := controllerutil.SetControllerReference(u, secret, r.Client.Scheme()); err != nil {
		return fmt.Errorf("own credentials secret: %w", err)
	}

	if !exists {
		err := r.Client.Create(ctx, secret)
		if apierrors.IsAlreadyExists(err) {
			// Just created by the API and not cached yet; its watch event requeues.
			return nil
		}
		return err
	}
	return r.Client.Patch(ctx, secret, client.MergeFrom(orig))
}

func (r *Reconciler) writeStatus(ctx context.Context, s *userState) error {
	u := s.user
	orig := u.DeepCopy()
	u.Status.Phase = s.phase
	u.Status.Message = s.message
	u.Status.Declared = s.declared
	u.Status.ObservedGeneration = u.Generation
	if s.password.Name != "" {
		u.Status.CredentialsSecret = s.password.Name
	}
	condition := metav1.Condition{
		Type:               v1alpha1.ConditionReady,
		Status:             metav1.ConditionFalse,
		Reason:             s.phase,
		Message:            s.message,
		ObservedGeneration: u.Generation,
	}
	if s.phase == v1alpha1.PhaseReady {
		condition.Status = metav1.ConditionTrue
	}
	meta.SetStatusCondition(&u.Status.Conditions, condition)
	return client.IgnoreNotFound(r.Client.Status().Patch(ctx, u, client.MergeFrom(orig)))
}
