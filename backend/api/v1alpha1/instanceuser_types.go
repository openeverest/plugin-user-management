package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Phases of an InstanceUser.
const (
	// PhasePending: the user is being created or changed in the database.
	PhasePending = "Pending"
	// PhaseReady: the user exists in the database with the declared roles.
	PhaseReady = "Ready"
	// PhaseFailed: the user can't be reconciled; see the message.
	PhaseFailed = "Failed"
	// PhaseDeleting: the user is being removed from the database.
	PhaseDeleting = "Deleting"
)

// ConditionReady tells whether the user matches its spec in the database.
const ConditionReady = "Ready"

// Credentials Secret keys.
const (
	SecretKeyUsername = "username"
	SecretKeyPassword = "password"
	SecretKeyURI      = "uri"
)

// InstanceReference names an OpenEverest Instance in the InstanceUser's namespace.
type InstanceReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// SecretKeySelector selects a key of a Secret in the InstanceUser's namespace.
type SecretKeySelector struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
	// +kubebuilder:default=password
	Key string `json:"key,omitempty"`
}

// RoleGrant grants a role, on a database for database-scoped roles.
type RoleGrant struct {
	// +kubebuilder:validation:MinLength=1
	Role string `json:"role"`
	// Database the role is granted on. Required for database-scoped roles,
	// ignored for roles that apply to the whole instance.
	Database string `json:"database,omitempty"`
}

// InstanceUserSpec declares a database user.
// +kubebuilder:validation:XValidation:rule="self.instanceRef == oldSelf.instanceRef",message="instanceRef is immutable"
// +kubebuilder:validation:XValidation:rule="self.username == oldSelf.username",message="username is immutable"
type InstanceUserSpec struct {
	// InstanceRef is the Instance whose database the user is created in.
	InstanceRef InstanceReference `json:"instanceRef"`
	// Username in the database.
	// +kubebuilder:validation:Pattern=`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,63}$`
	Username string `json:"username"`
	// Roles granted to the user. Only roles the plugin offers for the
	// instance's engine are accepted.
	// +kubebuilder:validation:MinItems=1
	// +listType=atomic
	Roles []RoleGrant `json:"roles"`
	// PasswordSecretRef points to the user's password. When omitted, the
	// plugin generates a password and keeps it, with a connection string, in
	// the Secret named in status.credentialsSecret.
	// +optional
	PasswordSecretRef *SecretKeySelector `json:"passwordSecretRef,omitempty"`
}

// InstanceUserStatus is the observed state of an InstanceUser.
type InstanceUserStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// CredentialsSecret is the Secret holding the user's password, and for
	// generated passwords also its username and connection string.
	// +optional
	CredentialsSecret string `json:"credentialsSecret,omitempty"`
	// Declared is set once the plugin has declared the user for the
	// database. A database user with the same name that exists before then
	// is a conflict, so an InstanceUser can't take over an existing account.
	// +optional
	Declared bool `json:"declared,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// InstanceUser is a user of an OpenEverest instance's database.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=iuser;iusers
// +kubebuilder:printcolumn:name="Instance",type=string,JSONPath=`.spec.instanceRef.name`
// +kubebuilder:printcolumn:name="Username",type=string,JSONPath=`.spec.username`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type InstanceUser struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceUserSpec   `json:"spec"`
	Status InstanceUserStatus `json:"status,omitempty"`
}

// InstanceUserList is a list of InstanceUsers.
//
// +kubebuilder:object:root=true
type InstanceUserList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceUser `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InstanceUser{}, &InstanceUserList{})
}
