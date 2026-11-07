package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Deletion policies of an InstanceDatabase.
const (
	// DeletionPolicyRetain stops managing the database and keeps it, with its data.
	DeletionPolicyRetain = "Retain"
	// DeletionPolicyDelete drops the database and its data.
	DeletionPolicyDelete = "Delete"
)

// Who can connect to an InstanceDatabase.
const (
	// ConnectPublic lets every user connect, as PostgreSQL does by default.
	ConnectPublic = "Public"
	// ConnectGranted lets only the owner and users granted a role on the database connect.
	ConnectGranted = "Granted"
)

// InstanceUserReference names an InstanceUser in the InstanceDatabase's namespace.
type InstanceUserReference struct {
	// +kubebuilder:validation:MinLength=1
	Name string `json:"name"`
}

// DatabaseOwner is the user who owns a database.
type DatabaseOwner struct {
	// InstanceUserRef is an InstanceUser of the same instance; the database
	// is created once the user is Ready.
	InstanceUserRef InstanceUserReference `json:"instanceUserRef"`
}

// InstanceDatabaseSpec declares a database of an instance.
// +kubebuilder:validation:XValidation:rule="self.instanceRef == oldSelf.instanceRef",message="instanceRef is immutable"
// +kubebuilder:validation:XValidation:rule="self.name == oldSelf.name",message="name is immutable"
type InstanceDatabaseSpec struct {
	// InstanceRef is the Instance the database is created in.
	InstanceRef InstanceReference `json:"instanceRef"`
	// Name of the database: lowercase letters, digits and underscores,
	// starting with a letter or an underscore.
	// +kubebuilder:validation:Pattern=`^[a-z_][a-z0-9_]{0,62}$`
	Name string `json:"name"`
	// Owner of the database. Changing it changes the owner of the database,
	// not of the objects in it.
	Owner DatabaseOwner `json:"owner"`
	// DeletionPolicy tells what deleting the InstanceDatabase does to the
	// database: Retain stops managing it and keeps its data, Delete drops it.
	// +kubebuilder:validation:Enum=Retain;Delete
	// +kubebuilder:default=Retain
	// +optional
	DeletionPolicy string `json:"deletionPolicy,omitempty"`
	// Connect tells who can connect to the database. Public, PostgreSQL's
	// default, lets every user connect, though only the owner and the users
	// granted a role on the database can use its objects. Granted lets only
	// those connect, besides superusers and the operator's own roles that
	// connect to every database, such as its connection pooler's.
	// +kubebuilder:validation:Enum=Public;Granted
	// +kubebuilder:default=Public
	// +optional
	Connect string `json:"connect,omitempty"`
}

// InstanceDatabaseStatus is the observed state of an InstanceDatabase.
type InstanceDatabaseStatus struct {
	// +optional
	Phase string `json:"phase,omitempty"`
	// +optional
	Message string `json:"message,omitempty"`
	// Owner is the username of the owner the database was last declared with.
	// +optional
	Owner string `json:"owner,omitempty"`
	// Declared is set once the plugin has claimed the database name. A
	// database with the same name that exists before then is a conflict, so
	// an InstanceDatabase can't take over an existing database.
	// +optional
	Declared bool `json:"declared,omitempty"`
	// Available is set once the database and the roles that grant access to
	// it have been applied. Users can be granted roles on it from then on,
	// even while it's Pending again, e.g. during an owner change.
	// +optional
	Available bool `json:"available,omitempty"`
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// InstanceDatabase is a database of an OpenEverest instance.
//
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName=idb;idbs
// +kubebuilder:printcolumn:name="Instance",type=string,JSONPath=`.spec.instanceRef.name`
// +kubebuilder:printcolumn:name="Database",type=string,JSONPath=`.spec.name`
// +kubebuilder:printcolumn:name="Owner",type=string,JSONPath=`.status.owner`
// +kubebuilder:printcolumn:name="Phase",type=string,JSONPath=`.status.phase`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`
type InstanceDatabase struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   InstanceDatabaseSpec   `json:"spec"`
	Status InstanceDatabaseStatus `json:"status,omitempty"`
}

// InstanceDatabaseList is a list of InstanceDatabases.
//
// +kubebuilder:object:root=true
type InstanceDatabaseList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []InstanceDatabase `json:"items"`
}

func init() {
	SchemeBuilder.Register(&InstanceDatabase{}, &InstanceDatabaseList{})
}
