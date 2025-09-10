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

	// Store recovery keys in a secret
	// Set to false for production environments where recovery keys should be handled outside of Kubernetes
	// +kubebuilder:default=false
	StoreRecoveryKeys bool `json:"storeRecoveryKeys,omitempty"`

	// Annotations to add to the admin token secret
	// +optional
	AdminTokenAnnotations map[string]string `json:"adminTokenAnnotations,omitempty"`

	// Annotations to add to the recovery keys secret
	// +optional
	RecoveryKeysAnnotations map[string]string `json:"recoveryKeysAnnotations,omitempty"`
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

// PostUnsealConfig defines configuration to apply after unsealing
type PostUnsealConfig struct {
	// Enable KV v2 secret engine
	// +kubebuilder:default=true
	EnableKV bool `json:"enableKV,omitempty"`

	// Enable External Secrets Operator configuration
	// +kubebuilder:default=true
	EnableExternalSecretsOperator bool `json:"enableExternalSecretsOperator,omitempty"`

	// KV engine configuration
	KVConfig KVConfig `json:"kvConfig,omitempty"`

	// External Secrets Operator configuration
	ExternalSecretsOperatorConfig ExternalSecretsOperatorConfig `json:"externalSecretsOperatorConfig,omitempty"`
}

// KVConfig defines KV engine configuration
type KVConfig struct {
	// Mount path for KV engine
	// +kubebuilder:default="secret"
	Path string `json:"path,omitempty"`

	// KV version
	// +kubebuilder:default=2
	Version int `json:"version,omitempty"`

	// Max versions to keep
	// +kubebuilder:default=5
	MaxVersions int `json:"maxVersions,omitempty"`

	// Delete version after duration (e.g., "30d")
	// +kubebuilder:default="30d"
	DeleteVersionAfter string `json:"deleteVersionAfter,omitempty"`
}

// ExternalSecretsOperatorConfig defines ESO configuration
type ExternalSecretsOperatorConfig struct {
	// Policy name
	// +kubebuilder:default="external-secrets-operator"
	PolicyName string `json:"policyName,omitempty"`

	// Kubernetes auth configuration
	KubernetesAuth KubernetesAuthConfig `json:"kubernetesAuth,omitempty"`
}

// KubernetesAuthConfig defines Kubernetes auth configuration
type KubernetesAuthConfig struct {
	// Role name
	// +kubebuilder:default="external-secrets-operator"
	RoleName string `json:"roleName,omitempty"`

	// Service accounts that can authenticate
	ServiceAccounts []ServiceAccountRef `json:"serviceAccounts,omitempty"`

	// Default TTL
	// +kubebuilder:default="24h"
	TTL string `json:"ttl,omitempty"`

	// Default max TTL
	// +kubebuilder:default="24h"
	MaxTTL string `json:"maxTTL,omitempty"`
}

// ServiceAccountRef references a service account
type ServiceAccountRef struct {
	// Service account name
	Name string `json:"name"`

	// Namespace
	Namespace string `json:"namespace"`
}

// ArgoCDSpec defines ArgoCD integration settings
type ArgoCDSpec struct {
	// Enable ArgoCD integration
	// When enabled, CRDs will be managed by ArgoCD instead of the operator
	// +kubebuilder:default=false
	Enabled bool `json:"enabled,omitempty"`

	// ArgoCD namespace
	// +kubebuilder:default="argocd"
	Namespace string `json:"namespace,omitempty"`

	// Application name that manages this operator's CRDs
	// If specified, operator will wait for this application to be healthy
	ApplicationName string `json:"applicationName,omitempty"`

	// Skip CRD installation when ArgoCD integration is enabled
	// +kubebuilder:default=true
	SkipCRDInstall bool `json:"skipCRDInstall,omitempty"`

	// Wait timeout for ArgoCD application to be ready
	// +kubebuilder:default="5m"
	WaitTimeout string `json:"waitTimeout,omitempty"`
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

	// Post-unseal configuration
	PostUnsealConfig PostUnsealConfig `json:"postUnsealConfig,omitempty"`

	// ArgoCD integration settings
	ArgoCD *ArgoCDSpec `json:"argocd,omitempty"`
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

	// Configuration status
	ConfigurationStatus ConfigurationStatus `json:"configurationStatus,omitempty"`

	// Current conditions
	Conditions []Condition `json:"conditions,omitempty"`
}

// ConfigurationStatus tracks post-unseal configuration
type ConfigurationStatus struct {
	// KV engine configured
	KVConfigured bool `json:"kvConfigured,omitempty"`

	// Last time KV was configured
	KVConfiguredTime string `json:"kvConfiguredTime,omitempty"`

	// External Secrets Operator configured
	ExternalSecretsOperatorConfigured bool `json:"externalSecretsOperatorConfigured,omitempty"`

	// Last time ESO was configured
	ExternalSecretsOperatorConfiguredTime string `json:"externalSecretsOperatorConfiguredTime,omitempty"`
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
