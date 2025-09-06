package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VaultPodSpec defines the Vault pods to manage
type VaultPodSpec struct {
	// Namespace where Vault is deployed
	Namespace string `json:"namespace"`

	// Label selector for Vault pods
	Selector map[string]string `json:"selector"`
}

// TransitVaultSpec defines the transit Vault configuration
type TransitVaultSpec struct {
	// Transit Vault address
	Address string `json:"address"`

	// Secret containing transit token
	SecretRef SecretReference `json:"secretRef"`

	// Transit key name
	// +kubebuilder:default=autounseal
	KeyName string `json:"keyName,omitempty"`

	// Transit mount path
	// +kubebuilder:default=transit
	MountPath string `json:"mountPath,omitempty"`

	// Skip TLS verification
	// +kubebuilder:default=true
	TLSSkipVerify bool `json:"tlsSkipVerify,omitempty"`
}

// SecretReference references a secret
type SecretReference struct {
	// Name of the secret
	Name string `json:"name"`

	// Key in the secret
	// +kubebuilder:default=token
	Key string `json:"key,omitempty"`
}

// InitializationSpec defines initialization parameters
type InitializationSpec struct {
	// Number of recovery key shares
	// +kubebuilder:default=1
	RecoveryShares int `json:"recoveryShares,omitempty"`

	// Recovery key threshold
	// +kubebuilder:default=1
	RecoveryThreshold int `json:"recoveryThreshold,omitempty"`

	// Names for created secrets
	SecretNames SecretNamesSpec `json:"secretNames,omitempty"`
}

// SecretNamesSpec defines secret names
type SecretNamesSpec struct {
	// Name for admin token secret
	// +kubebuilder:default=vault-admin-token
	AdminToken string `json:"adminToken,omitempty"`

	// Name for recovery keys secret
	// +kubebuilder:default=vault-keys
	RecoveryKeys string `json:"recoveryKeys,omitempty"`
}

// MonitoringSpec defines monitoring parameters
type MonitoringSpec struct {
	// How often to check Vault status
	// +kubebuilder:default="30s"
	CheckInterval string `json:"checkInterval,omitempty"`

	// Retry interval for failed operations
	// +kubebuilder:default="10s"
	RetryInterval string `json:"retryInterval,omitempty"`
}

// VaultTransitUnsealSpec defines the desired state of VaultTransitUnseal
type VaultTransitUnsealSpec struct {
	// Vault pods to manage
	VaultPod VaultPodSpec `json:"vaultPod"`

	// Transit Vault configuration
	TransitVault TransitVaultSpec `json:"transitVault"`

	// Initialization parameters
	Initialization InitializationSpec `json:"initialization,omitempty"`

	// Monitoring parameters
	Monitoring MonitoringSpec `json:"monitoring,omitempty"`
}

// Condition represents the state of a VaultTransitUnseal at a certain point
type Condition struct {
	// Type of condition
	Type string `json:"type"`

	// Status of the condition, one of True, False, Unknown
	Status string `json:"status"`

	// Reason for the condition's last transition
	Reason string `json:"reason"`

	// Human-readable message
	Message string `json:"message"`

	// Last time the condition transitioned
	LastTransitionTime string `json:"lastTransitionTime"`
}

// VaultTransitUnsealStatus defines the observed state of VaultTransitUnseal
type VaultTransitUnsealStatus struct {
	// Whether Vault is initialized
	Initialized bool `json:"initialized,omitempty"`

	// Whether Vault is sealed
	Sealed bool `json:"sealed,omitempty"`

	// Last time Vault status was checked
	LastCheckTime string `json:"lastCheckTime,omitempty"`

	// Current conditions
	Conditions []Condition `json:"conditions,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status

// VaultTransitUnseal is the Schema for the vaulttransitunseals API
type VaultTransitUnseal struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   VaultTransitUnsealSpec   `json:"spec,omitempty"`
	Status VaultTransitUnsealStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// VaultTransitUnsealList contains a list of VaultTransitUnseal
type VaultTransitUnsealList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VaultTransitUnseal `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VaultTransitUnseal{}, &VaultTransitUnsealList{})
}
