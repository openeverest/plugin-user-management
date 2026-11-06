package controller

import (
	"context"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// ensureInstanceMetadata adds the finalizer, the instance label and an owner
// reference to the Instance, so deleting the instance deletes the object.
func ensureInstanceMetadata(ctx context.Context, c client.Client, obj client.Object, instance *unstructured.Unstructured, finalizer string) error {
	orig, ok := obj.DeepCopyObject().(client.Object)
	if !ok {
		return nil
	}
	changed := controllerutil.AddFinalizer(obj, finalizer)
	labels := obj.GetLabels()
	if labels[InstanceLabel] != instance.GetName() {
		if labels == nil {
			labels = map[string]string{}
		}
		labels[InstanceLabel] = instance.GetName()
		obj.SetLabels(labels)
		changed = true
	}
	if !hasOwner(obj, instance.GetUID()) {
		// Not blockOwnerDeletion: that would need update on instances/finalizers.
		obj.SetOwnerReferences(append(slices.Clone(obj.GetOwnerReferences()), metav1.OwnerReference{
			APIVersion: InstanceGVK.GroupVersion().String(),
			Kind:       InstanceGVK.Kind,
			Name:       instance.GetName(),
			UID:        instance.GetUID(),
		}))
		changed = true
	}
	if !changed {
		return nil
	}
	return c.Patch(ctx, obj, client.MergeFrom(orig))
}

func hasOwner(obj metav1.Object, uid types.UID) bool {
	for _, ref := range obj.GetOwnerReferences() {
		if ref.UID == uid {
			return true
		}
	}
	return false
}

func removeFinalizer(ctx context.Context, c client.Client, obj client.Object, finalizer string) error {
	orig, ok := obj.DeepCopyObject().(client.Object)
	if !ok || !controllerutil.RemoveFinalizer(obj, finalizer) {
		return nil
	}
	return client.IgnoreNotFound(c.Patch(ctx, obj, client.MergeFrom(orig)))
}
