package cnpg

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

var databaseRoleResource = schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "databaseroles"}

const (
	// FieldManager owns the DatabaseRoles the plugin declares.
	FieldManager = "plugin-user-management"
	// instanceLabel marks the DatabaseRoles the plugin declares, with the
	// instance they belong to, like the DatabaseUsers they come from.
	instanceLabel = "plugin-user-management.plugins.openeverest.io/instance"
	// reloadLabel makes CloudNativePG apply password Secret changes right away.
	reloadLabel = "cnpg.io/reload"

	reclaimRetain     = "retain"
	reclaimDelete     = "delete"
	roleComment       = "Managed by OpenEverest"
	maxNameStemLength = 200
)

var invalidNameChars = regexp.MustCompile(`[^a-z0-9-]+`)

// databaseRoleName derives a valid, collision-free DatabaseRole name from the
// username, so the DatabaseRole can be found from the username alone.
func databaseRoleName(instance, username string) string {
	sum := sha256.Sum256([]byte(username))
	stem := instance + "-" + strings.Trim(invalidNameChars.ReplaceAllString(strings.ToLower(username), "-"), "-")
	if len(stem) > maxNameStemLength {
		stem = stem[:maxNameStemLength]
	}
	return strings.Trim(stem, "-") + "-" + hex.EncodeToString(sum[:])[:6]
}

// databaseRoleApplyConfiguration declares the user as a login role that is a
// member of its granted roles. The role is retained when the DatabaseRole
// goes away, unless DropUser asked for it to be dropped; the Cluster owns the
// DatabaseRole so it doesn't outlive it.
func databaseRoleApplyConfiguration(c cluster, user engine.DesiredUser) *unstructured.Unstructured {
	inRoles := make([]any, 0, len(user.Roles))
	for _, g := range user.Roles {
		inRoles = append(inRoles, g.Role)
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": databaseRoleResource.GroupVersion().String(),
		"kind":       "DatabaseRole",
		"metadata": map[string]any{
			"name":      databaseRoleName(c.name, user.Username),
			"namespace": c.namespace,
			"labels":    map[string]any{instanceLabel: c.name},
			"ownerReferences": []any{map[string]any{
				"apiVersion": clusterResource.GroupVersion().String(),
				"kind":       "Cluster",
				"name":       c.name,
				"uid":        string(c.uid),
			}},
		},
		"spec": map[string]any{
			"cluster":                   map[string]any{"name": c.name},
			"name":                      user.Username,
			"comment":                   roleComment,
			"login":                     true,
			"inRoles":                   inRoles,
			"passwordSecret":            map[string]any{"name": user.Password.Name},
			"databaseRoleReclaimPolicy": reclaimRetain,
		},
	}}
}

func (e *Engine) applyDatabaseRole(ctx context.Context, obj *unstructured.Unstructured) error {
	_, err := e.dyn.Resource(databaseRoleResource).Namespace(obj.GetNamespace()).
		Apply(ctx, obj.GetName(), obj, metav1.ApplyOptions{FieldManager: FieldManager, Force: true})
	if err != nil {
		return fmt.Errorf("apply DatabaseRole: %w", err)
	}
	return nil
}

// ensureReloadLabel labels the password Secret so CloudNativePG applies
// password changes as soon as the Secret is edited.
func (e *Engine) ensureReloadLabel(ctx context.Context, namespace, name string) error {
	secrets := e.kube.CoreV1().Secrets(namespace)
	secret, err := secrets.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		// CloudNativePG reports the missing Secret on the DatabaseRole.
		return nil
	}
	if err != nil {
		return fmt.Errorf("get password secret %q: %w", name, err)
	}
	if secret.Labels[reloadLabel] == "true" {
		return nil
	}
	patch := fmt.Appendf(nil, `{"metadata":{"labels":{%q:"true"}}}`, reloadLabel)
	if _, err := secrets.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: FieldManager}); err != nil {
		return fmt.Errorf("label password secret %q: %w", name, err)
	}
	return nil
}

// dropDatabaseRole has CloudNativePG drop the user's role by deleting its
// DatabaseRole with the delete reclaim policy, and reports ErrInProgress until
// the DatabaseRole is gone.
func (e *Engine) dropDatabaseRole(ctx context.Context, target engine.Target, username string) error {
	roles := e.dyn.Resource(databaseRoleResource).Namespace(target.Namespace)
	name := databaseRoleName(target.Instance, username)
	role, err := roles.Get(ctx, name, metav1.GetOptions{})
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get DatabaseRole: %w", err)
	}
	if role.GetDeletionTimestamp() == nil {
		if policy, _, _ := unstructured.NestedString(role.Object, "spec", "databaseRoleReclaimPolicy"); policy != reclaimDelete {
			patch := fmt.Appendf(nil, `{"spec":{"databaseRoleReclaimPolicy":%q}}`, reclaimDelete)
			if _, err := roles.Patch(ctx, name, types.MergePatchType, patch, metav1.PatchOptions{FieldManager: FieldManager}); err != nil {
				return fmt.Errorf("set DatabaseRole reclaim policy: %w", err)
			}
		}
		if err := roles.Delete(ctx, name, metav1.DeleteOptions{}); err != nil && !apierrors.IsNotFound(err) {
			return fmt.Errorf("delete DatabaseRole: %w", err)
		}
		return fmt.Errorf("%w: CloudNativePG is dropping the role", engine.ErrInProgress)
	}
	if reason := failureReason(role); reason != "" {
		return fmt.Errorf("%w: CloudNativePG can't drop the role: %s", engine.ErrInProgress, reason)
	}
	return fmt.Errorf("%w: CloudNativePG is dropping the role", engine.ErrInProgress)
}

// failureReason is CloudNativePG's error from the last attempt to apply the
// DatabaseRole, if it failed.
func failureReason(role *unstructured.Unstructured) string {
	if applied, _, _ := unstructured.NestedBool(role.Object, "status", "applied"); applied {
		return ""
	}
	message, _, _ := unstructured.NestedString(role.Object, "status", "message")
	return message
}

// PendingReason implements engine.PendingReasoner with CloudNativePG's error
// for the user's DatabaseRole.
func (e *Engine) PendingReason(ctx context.Context, target engine.Target, username string) string {
	role, err := e.dyn.Resource(databaseRoleResource).Namespace(target.Namespace).Get(ctx, databaseRoleName(target.Instance, username), metav1.GetOptions{})
	if err != nil {
		return ""
	}
	if reason := failureReason(role); reason != "" {
		return "CloudNativePG can't apply the role: " + reason
	}
	return ""
}

// foreignRoles are the roles declared for the cluster by DatabaseRoles the
// plugin didn't create; they may not exist in the database yet.
func (e *Engine) foreignRoles(ctx context.Context, c cluster) ([]engine.User, error) {
	list, err := e.dyn.Resource(databaseRoleResource).Namespace(c.namespace).List(ctx, metav1.ListOptions{})
	if err != nil {
		return nil, fmt.Errorf("list DatabaseRoles: %w", err)
	}
	var users []engine.User
	for _, item := range list.Items {
		if _, ours := item.GetLabels()[instanceLabel]; ours {
			continue
		}
		if clusterName, _, _ := unstructured.NestedString(item.Object, "spec", "cluster", "name"); clusterName != c.name {
			continue
		}
		name, _, _ := unstructured.NestedString(item.Object, "spec", "name")
		inRoles, _, _ := unstructured.NestedStringSlice(item.Object, "spec", "inRoles")
		users = append(users, engine.User{Username: name, Roles: grantsOf(inRoles)})
	}
	return users, nil
}

func grantsOf(roles []string) []engine.RoleGrant {
	grants := make([]engine.RoleGrant, 0, len(roles))
	for _, r := range roles {
		grants = append(grants, engine.RoleGrant{Role: r})
	}
	return grants
}
