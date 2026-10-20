package mongodb

import (
	"context"
	"fmt"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/openeverest/plugin-user-management/backend/internal/engine"
)

// FieldManager owns spec.users of PerconaServerMongoDB resources. The
// provider applies the rest of the resource under its own field manager and
// never sets spec.users, so the two don't conflict.
const FieldManager = "plugin-user-management"

// psmdbUser is an entry of PerconaServerMongoDB spec.users.
type psmdbUser struct {
	Name              string         `json:"name"`
	DB                string         `json:"db"`
	PasswordSecretRef psmdbSecretRef `json:"passwordSecretRef"`
	Roles             []psmdbRole    `json:"roles"`
}

type psmdbSecretRef struct {
	Name string `json:"name"`
	Key  string `json:"key"`
}

type psmdbRole struct {
	Name string `json:"name"`
	DB   string `json:"db"`
}

// usersApplyConfiguration is the server-side apply configuration declaring
// exactly the given users. With no users it releases spec.users, which the
// API server then removes.
func usersApplyConfiguration(target engine.Target, users []psmdbUser) (*unstructured.Unstructured, error) {
	spec := map[string]any{}
	if len(users) > 0 {
		list := make([]any, 0, len(users))
		for _, u := range users {
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&u)
			if err != nil {
				return nil, err
			}
			list = append(list, m)
		}
		spec["users"] = list
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": psmdbResource.GroupVersion().String(),
		"kind":       "PerconaServerMongoDB",
		"metadata":   map[string]any{"name": target.Instance, "namespace": target.Namespace},
		"spec":       spec,
	}}, nil
}

func (r *clusterResolver) applyUsers(ctx context.Context, target engine.Target, users []psmdbUser) error {
	resource := r.dyn.Resource(psmdbResource).Namespace(target.Namespace)
	// Apply would create a missing resource; only ever add users to the provider's.
	if _, err := resource.Get(ctx, target.Instance, metav1.GetOptions{}); err != nil {
		if apierrors.IsNotFound(err) {
			return fmt.Errorf("%w: MongoDB cluster not found", engine.ErrNotReady)
		}
		return fmt.Errorf("get PerconaServerMongoDB: %w", err)
	}
	obj, err := usersApplyConfiguration(target, users)
	if err != nil {
		return err
	}
	if _, err := resource.Apply(ctx, target.Instance, obj, metav1.ApplyOptions{FieldManager: FieldManager, Force: true}); err != nil {
		return fmt.Errorf("apply PerconaServerMongoDB users: %w", err)
	}
	return nil
}
