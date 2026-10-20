// Package v1alpha1 contains the plugin's API: database users declared as
// Kubernetes resources and reconciled into the instance's database.
//
// +kubebuilder:object:generate=true
// +groupName=plugin-user-management.plugins.openeverest.io
package v1alpha1

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/scheme"
)

var (
	// GroupVersion follows the plugin CRD group convention <plugin>.plugins.openeverest.io.
	GroupVersion = schema.GroupVersion{Group: "plugin-user-management.plugins.openeverest.io", Version: "v1alpha1"}

	SchemeBuilder = &scheme.Builder{GroupVersion: GroupVersion}

	AddToScheme = SchemeBuilder.AddToScheme
)
