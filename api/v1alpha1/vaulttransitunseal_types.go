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

	// HeadlessServiceName, when set, makes the operator address each Vault pod
	// INDIVIDUALLY via its per-pod DNS record
	// (<pod-name>.<headlessServiceName>.<namespace>.svc.cluster.local) instead
	// of a shared, load-balanced service. This is required to unseal a SPECIFIC
	// sealed pod: a load-balanced service can only reach the pod the LB happens
	// to pick, and a client Service that excludes not-ready (sealed) pods can't
	// reach them at all. Use the StatefulSet's governing headless service, which
	// publishes not-ready addresses (e.g. "vault-vault-internal").
	// +optional
	HeadlessServiceName string `json:"headlessServiceName,omitempty"`

	// Service port to connect to Vault
	// +kubebuilder:default=8300
	// +optional
	ServicePort int32 `json:"servicePort,omitempty"`

	// Scheme to use when the operator builds the Vault address itself
	// (service discovery / per-pod DNS). Empty means "http", which is the
	// behaviour every existing install has. Set to "https" for a Vault that
	// terminates TLS itself; supply the CA to trust through the operator's
	// VAULT_CACERT environment variable (chart: extraEnvVars + extraVolumes),
	// or leave it unset to fall back to the operator's TLS-validation setting.
	// Ignored when VaultAddress is set — that override carries its own scheme.
	// +kubebuilder:validation:Enum=http;https
	// +optional
	Scheme string `json:"scheme,omitempty"`

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
	// This creates kubernetes auth, ESO policy and role
	// Set to false if vault-config-operator manages ESO via GitOps
	// +kubebuilder:default=true
	EnableExternalSecretsOperator bool `json:"enableExternalSecretsOperator,omitempty"`

	// Enable minimal bootstrap for vault-config-operator
	// This ONLY enables kubernetes auth and creates controller-manager role
	// Use this when vault-config-operator manages all other Vault config via GitOps
	// +kubebuilder:default=false
	EnableVaultConfigOperatorBootstrap bool `json:"enableVaultConfigOperatorBootstrap,omitempty"`

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

// UnsealMode selects how the operator recovers a sealed Vault.
//
// "transit" (the default, and what every pre-existing resource gets) drives a
// Vault whose seal stanza is `seal "transit"`: there is no API to trigger such
// an unseal, so recovery means restarting the pod and letting it unseal from
// its seal stanza at boot.
//
// "stored-key" drives a root-of-trust Vault that is Shamir-sealed and has no
// transit provider to lean on: the operator submits the key share(s) held in
// an in-cluster Secret to sys/unseal.
//
// +kubebuilder:validation:Enum=transit;stored-key
type UnsealMode string

const (
	// UnsealModeTransit unseals via the Vault transit seal (default).
	UnsealModeTransit UnsealMode = "transit"

	// UnsealModeStoredKey unseals by POSTing Shamir shares to sys/unseal.
	UnsealModeStoredKey UnsealMode = "stored-key"
)

// Defaults for the stored-key Secret. They match the vault-auto-unseal CronJob
// this mode replaces (Secret `vault-unseal-keys`, key `unseal-keys.txt`), so a
// cluster can swap the CronJob for a CR without touching the Secret.
const (
	DefaultStoredKeySecretName = "vault-unseal-keys"
	DefaultStoredKeySecretKey  = "unseal-keys.txt"
)

// StoredKeySpec configures stored-key (Shamir) unsealing.
type StoredKeySpec struct {
	// SecretRef points at the Secret holding the unseal key share(s), one
	// share per line. Defaults to vault-unseal-keys/unseal-keys.txt in the
	// Vault namespace.
	// +optional
	SecretRef StoredKeySecretRef `json:"secretRef,omitempty"`
}

// StoredKeySecretRef references the Secret carrying the unseal key shares.
type StoredKeySecretRef struct {
	// Name of the Secret
	// +kubebuilder:default=vault-unseal-keys
	// +optional
	Name string `json:"name,omitempty"`

	// Key inside the Secret. Its value is the share list, one per line.
	// +kubebuilder:default=unseal-keys.txt
	// +optional
	Key string `json:"key,omitempty"`

	// Namespace of the Secret. Defaults to vaultPod.namespace.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// VaultTransitUnsealSpec defines the desired state of VaultTransitUnseal
type VaultTransitUnsealSpec struct {
	// Mode selects how a sealed Vault is recovered. Absent or empty means
	// "transit", so existing resources keep their behaviour untouched.
	// +kubebuilder:default=transit
	// +optional
	Mode UnsealMode `json:"mode,omitempty"`

	// Vault pods to manage
	VaultPod VaultPodSpec `json:"vaultPod"`

	// Transit Vault configuration. Required in transit mode; ignored (and
	// safely omitted) in stored-key mode.
	// +optional
	TransitVault TransitVaultSpec `json:"transitVault,omitempty"`

	// StoredKey configures stored-key (Shamir) unsealing. Only read when
	// mode is "stored-key"; its own defaults apply when it is omitted.
	// +optional
	StoredKey *StoredKeySpec `json:"storedKey,omitempty"`

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

	// UnsealMode is the mode the operator last reconciled this resource in.
	// Echoed back so `kubectl get vaulttransitunseal -o yaml` shows which
	// automation is actually in charge, defaults resolved.
	// +optional
	UnsealMode string `json:"unsealMode,omitempty"`

	// LastUnsealTime is when the operator last brought this Vault from
	// sealed to unsealed. Only set by stored-key mode: transit mode never
	// unseals over the API, it restarts the pod and lets Vault do it.
	// +optional
	LastUnsealTime string `json:"lastUnsealTime,omitempty"`

	// Configuration status
	ConfigurationStatus ConfigurationStatus `json:"configurationStatus,omitempty"`

	// Token management status
	TokenStatus TokenStatus `json:"tokenStatus,omitempty"`

	// RecoveryKeysHash is the SHA-256 of the recovery-key-0 bytes the
	// operator wrote into the recovery-keys Secret at initialization.
	// On every subsequent reconcile, the operator recomputes the hash
	// from the in-cluster Secret value and compares: a mismatch means
	// the Secret has been overwritten with a value Vault will reject
	// (the failure mode that caused the 2026-05-23 admin-token
	// outage). Set once at init; never overwritten unless
	// ForceReinitialize is asserted.
	// +optional
	RecoveryKeysHash string `json:"recoveryKeysHash,omitempty"`

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

	// vault-config-operator bootstrap configured
	VaultConfigOperatorBootstrapped bool `json:"vaultConfigOperatorBootstrapped,omitempty"`

	// Last time vault-config-operator bootstrap was configured
	VaultConfigOperatorBootstrappedTime string `json:"vaultConfigOperatorBootstrappedTime,omitempty"`
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
