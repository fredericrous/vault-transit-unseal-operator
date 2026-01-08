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

	// Service name to connect to Vault (optional)
	// If not specified, the operator will auto-discover the service using the pod selector
	// +optional
	ServiceName string `json:"serviceName,omitempty"`

	// Service port to connect to Vault
	// +kubebuilder:default=8300
	// +optional
	ServicePort int32 `json:"servicePort,omitempty"`

	// Override the full Vault address (e.g., https://vault.example.com)
	// This takes precedence over service discovery
	// +optional
	VaultAddress string `json:"vaultAddress,omitempty"`
}

// TransitVaultSpec defines the transit Vault configuration
type TransitVaultSpec struct {
	// Transit Vault address (direct value)
	// +optional
	Address string `json:"address,omitempty"`

	// Reference to get the address from ConfigMap or Secret
	// +optional
	AddressFrom *AddressReference `json:"addressFrom,omitempty"`

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

// AddressReference allows getting the address from ConfigMap or Secret
type AddressReference struct {
	// Reference to a key in a ConfigMap
	// +optional
	ConfigMapKeyRef *ConfigMapKeyReference `json:"configMapKeyRef,omitempty"`

	// Reference to a key in a Secret
	// +optional
	SecretKeyRef *SecretKeyReference `json:"secretKeyRef,omitempty"`

	// Default value to use if the reference cannot be resolved
	// +optional
	Default string `json:"default,omitempty"`
}

// ConfigMapKeyReference references a key in a ConfigMap
type ConfigMapKeyReference struct {
	// Name of the ConfigMap
	Name string `json:"name"`

	// Key in the ConfigMap
	Key string `json:"key"`

	// Namespace of the ConfigMap (defaults to the namespace of the VaultTransitUnseal)
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// SecretKeyReference references a key in a Secret
type SecretKeyReference struct {
	// Name of the Secret
	Name string `json:"name"`

	// Key in the Secret
	Key string `json:"key"`

	// Namespace of the Secret (defaults to the namespace of the VaultTransitUnseal)
	// +optional
	Namespace string `json:"namespace,omitempty"`
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

	// ForceReinitialize will generate a new root token even if Vault is already initialized
	// This is useful when the admin token is lost but Vault is already initialized
	// +kubebuilder:default=false
	ForceReinitialize bool `json:"forceReinitialize,omitempty"`

	// Token recovery configuration for disaster recovery scenarios
	TokenRecovery TokenRecoverySpec `json:"tokenRecovery,omitempty"`
}

// TokenRecoverySpec defines token recovery configuration for disaster recovery
type TokenRecoverySpec struct {
	// Enable token recovery features
	// +kubebuilder:default=true
	Enabled bool `json:"enabled,omitempty"`

	// Backup admin tokens to transit vault KV for recovery
	// +kubebuilder:default=true
	BackupToTransit bool `json:"backupToTransit,omitempty"`

	// KV path in transit vault for token backup
	// Defaults to vault-transit-unseal/<namespace>/<name>/admin-token
	// +optional
	TransitKVPath string `json:"transitKVPath,omitempty"`
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

	// Skip creating admin token secret (for external token management)
	// When true, the operator will not create placeholder admin tokens
	// +kubebuilder:default=false
	SkipAdminTokenCreation bool `json:"skipAdminTokenCreation,omitempty"`

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

// TokenManagementSpec defines how admin tokens are managed
type TokenManagementSpec struct {
	// Enable automatic token management
	// +kubebuilder:default=true
	Enabled bool `json:"enabled"`

	// Token creation strategy
	// +kubebuilder:validation:Enum=immediate;delayed;external
	// +kubebuilder:default="delayed"
	Strategy TokenStrategy `json:"strategy,omitempty"`

	// Delay before creating token (for delayed strategy)
	// +kubebuilder:default="30s"
	CreationDelay string `json:"creationDelay,omitempty"`

	// Policy to attach to admin token
	// +kubebuilder:default="vault-admin"
	PolicyName string `json:"policyName,omitempty"`

	// Token TTL
	// +kubebuilder:default="24h"
	TTL string `json:"ttl,omitempty"`

	// Enable automatic token renewal
	// +kubebuilder:default=true
	AutoRenew bool `json:"autoRenew,omitempty"`

	// Enable automatic token rotation
	// +kubebuilder:default=true
	AutoRotate bool `json:"autoRotate,omitempty"`

	// Rotation period
	// +kubebuilder:default="720h"
	RotationPeriod string `json:"rotationPeriod,omitempty"`

	// Dependencies that must be ready before creating token
	Dependencies TokenDependencies `json:"dependencies,omitempty"`
}

type TokenStrategy string

const (
	// Create token immediately after Vault is initialized
	TokenStrategyImmediate TokenStrategy = "immediate"

	// Wait for dependencies before creating token
	TokenStrategyDelayed TokenStrategy = "delayed"

	// Don't create token, let external system handle it
	TokenStrategyExternal TokenStrategy = "external"
)

type TokenDependencies struct {
	// Wait for specific deployments to be ready
	Deployments []DeploymentDependency `json:"deployments,omitempty"`

	// Wait for specific jobs to complete
	Jobs []JobDependency `json:"jobs,omitempty"`

	// Custom conditions to wait for
	CustomConditions []CustomCondition `json:"customConditions,omitempty"`
}

type DeploymentDependency struct {
	// Name of the deployment (supports prefix matching)
	Name string `json:"name"`

	// Namespace of the deployment
	Namespace string `json:"namespace"`

	// Minimum ready replicas
	// +kubebuilder:default=1
	MinReadyReplicas int32 `json:"minReadyReplicas,omitempty"`
}

type JobDependency struct {
	// Name of the job
	Name string `json:"name"`

	// Namespace of the job
	Namespace string `json:"namespace"`

	// Wait for successful completion
	// +kubebuilder:default=true
	WaitForSuccess bool `json:"waitForSuccess,omitempty"`
}

type CustomCondition struct {
	// Type of resource to check
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	Name       string `json:"name"`
	Namespace  string `json:"namespace"`

	// JSONPath expression that should evaluate to true
	Condition string `json:"condition"`
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

	// Token management configuration
	TokenManagement *TokenManagementSpec `json:"tokenManagement,omitempty"`
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

	// Token management status
	TokenStatus TokenStatus `json:"tokenStatus,omitempty"`

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

// TokenStatus tracks the state of managed tokens
type TokenStatus struct {
	// Current state of the token
	State TokenState `json:"state,omitempty"`

	// When the token was created
	CreatedAt string `json:"createdAt,omitempty"`

	// When the token was last renewed
	LastRenewedAt string `json:"lastRenewedAt,omitempty"`

	// When the token will expire
	ExpiresAt string `json:"expiresAt,omitempty"`

	// When the token should be rotated
	NextRotationAt string `json:"nextRotationAt,omitempty"`

	// Token accessor for management operations
	Accessor string `json:"accessor,omitempty"`

	// Error message if token creation failed
	Error string `json:"error,omitempty"`

	// Indicates if initial token creation is complete (for hybrid approach)
	Initialized bool `json:"initialized,omitempty"`
}

type TokenState string

const (
	// Token not yet created
	TokenStatePending TokenState = "Pending"

	// Waiting for dependencies
	TokenStateWaiting TokenState = "Waiting"

	// Token is active and valid
	TokenStateActive TokenState = "Active"

	// Token needs renewal
	TokenStateRenewing TokenState = "Renewing"

	// Token needs rotation
	TokenStateRotating TokenState = "Rotating"

	// Token creation failed
	TokenStateFailed TokenState = "Failed"
)

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
