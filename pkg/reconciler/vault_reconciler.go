package reconciler

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/configuration"
	operrors "github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/errors"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/secrets"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/token"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/transit"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

// VaultReconciler handles the main reconciliation logic
type VaultReconciler struct {
	client.Client
	Log                logr.Logger
	Recorder           record.EventRecorder
	VaultFactory       VaultClientFactory
	SecretManager      SecretManager
	MetricsRecorder    MetricsRecorder
	Configurator       *configuration.Configurator
	SecretVerifier     *secrets.Verifier
	RecoveryManager    *secrets.RecoveryManager
	TokenManager       *token.SimpleManager
	TransitVaultCACert string // Path to CA certificate for transit vault
}

// VaultClientFactory creates Vault clients
type VaultClientFactory interface {
	NewClientForPod(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) (vault.Client, error)
}

// SecretManager handles K8s secret operations
type SecretManager interface {
	CreateOrUpdate(ctx context.Context, namespace, name string, data map[string][]byte) error
	CreateOrUpdateWithOptions(ctx context.Context, namespace, name string, data map[string][]byte, annotations map[string]string) error
	Get(ctx context.Context, namespace, name, key string) ([]byte, error)
}

// MetricsRecorder records operator metrics
type MetricsRecorder interface {
	RecordReconciliation(duration time.Duration, success bool)
	RecordVaultStatus(initialized, sealed bool)
	RecordInitialization(success bool)
}

// Result encapsulates the reconciliation result
type Result struct {
	RequeueAfter time.Duration
	Error        error
}

// Reconcile handles a VaultTransitUnseal resource
func (r *VaultReconciler) Reconcile(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) *Result {
	log := r.Log.WithValues("resource", client.ObjectKeyFromObject(vtu))

	// Record metrics
	start := time.Now()
	result := &Result{}
	defer func() {
		r.MetricsRecorder.RecordReconciliation(time.Since(start), result.Error == nil)
	}()

	// Verify expected secrets exist
	if r.SecretVerifier != nil {
		log.V(1).Info("Verifying expected secrets")
		verificationResult, err := r.SecretVerifier.VerifyExpectedSecrets(ctx, vtu)
		if err != nil {
			log.Error(err, "Failed to verify secrets")
			result.Error = operrors.NewTransientError("secret verification failed", err)
			return result
		}

		// Log detailed verification results
		r.SecretVerifier.LogMissingSecrets(verificationResult)

		// Attempt recovery if secrets are missing and recovery manager is available
		if !verificationResult.AllPresent && r.RecoveryManager != nil {
			log.Info("Attempting to recover missing secrets")
			// For recovery, we need a vault client - try to get one from the first available pod
			pods, err := r.FindVaultPods(ctx, vtu)
			if err == nil && len(pods) > 0 {
				vaultClient, err := r.VaultFactory.NewClientForPod(ctx, &pods[0], vtu)
				if err == nil {
					if recErr := r.RecoveryManager.RecoverSecrets(ctx, vtu, verificationResult, vaultClient); recErr != nil {
						log.Error(recErr, "Secret recovery failed")
						if r.Recorder != nil {
							r.Recorder.Event(vtu, corev1.EventTypeWarning, "SecretRecoveryFailed", recErr.Error())
						}
					}
				}
			}
		}
	}

	// Validate transit token exists
	if err := r.ValidateTransitToken(ctx, vtu); err != nil {
		if r.Recorder != nil {
			r.Recorder.Event(vtu, corev1.EventTypeWarning, "InvalidConfig", err.Error())
		}
		result.Error = operrors.NewConfigError("transit token validation failed", err).
			WithContext("resource", client.ObjectKeyFromObject(vtu))
		return result
	}

	// Find Vault pods
	pods, err := r.FindVaultPods(ctx, vtu)
	if err != nil {
		result.Error = operrors.NewTransientError("failed to find vault pods", err).
			WithContext("namespace", vtu.Spec.VaultPod.Namespace).
			WithContext("selector", vtu.Spec.VaultPod.Selector)
		return result
	}

	if len(pods) == 0 {
		log.Info("No Vault pods found, requeueing")
		checkInterval, _ := time.ParseDuration(vtu.Spec.Monitoring.CheckInterval)
		result.RequeueAfter = checkInterval
		return result
	}

	// Process each pod
	allHealthy := true
	for _, pod := range pods {
		if err := r.ProcessPod(ctx, &pod, vtu); err != nil {
			log.Error(err, "Failed to process pod", "pod", pod.Name)
			allHealthy = false
			if r.Recorder != nil {
				r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "ProcessingFailed",
					"Failed to process pod %s: %v", pod.Name, err)
			}
		}
	}

	// Update status
	if err := r.updateStatus(ctx, vtu); err != nil {
		log.Error(err, "Failed to update status")
		result.Error = operrors.NewTransientError("failed to update status", err).
			WithContext("resource", client.ObjectKeyFromObject(vtu))
		return result
	}

	// Determine requeue interval
	checkInterval, _ := time.ParseDuration(vtu.Spec.Monitoring.CheckInterval)
	if !allHealthy {
		// Faster requeue on errors
		checkInterval = checkInterval / 2
	}

	result.RequeueAfter = checkInterval
	return result
}

// ProcessPod handles processing of a single vault pod
func (r *VaultReconciler) ProcessPod(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	log := r.Log.WithValues("pod", pod.Name, "namespace", pod.Namespace)

	// Skip pods that don't have a vault container (e.g., vault-agent-injector)
	if !r.hasVaultContainer(pod) {
		log.V(1).Info("Skipping pod without vault container")
		return nil
	}

	if !r.isPodReady(pod) {
		log.V(1).Info("Pod not ready, waiting for vault container to start")
		return fmt.Errorf("pod not running (waiting for vault container to start)")
	}

	// Create Vault client for this pod
	log.V(1).Info("Creating vault client for pod")
	vaultClient, err := r.VaultFactory.NewClientForPod(ctx, pod, vtu)
	if err != nil {
		log.Error(err, "Failed to create vault client")
		return fmt.Errorf("creating vault client: %w", err)
	}

	// Check status with timeout
	statusCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	log.V(1).Info("Checking vault status")
	status, err := vaultClient.CheckStatus(statusCtx)
	if err != nil {
		log.Error(err, "Failed to check vault status")
		return fmt.Errorf("checking vault status: %w", err)
	}

	log.Info("Vault status",
		"initialized", status.Initialized,
		"sealed", status.Sealed,
		"version", status.Version)

	// Update metrics
	r.MetricsRecorder.RecordVaultStatus(status.Initialized, status.Sealed)

	// Handle initialization if needed
	if !status.Initialized {
		if err := r.InitializeVault(ctx, vaultClient, pod, vtu); err != nil {
			r.MetricsRecorder.RecordInitialization(false)
			return fmt.Errorf("initializing vault: %w", err)
		}
		r.MetricsRecorder.RecordInitialization(true)

		// Verify secrets were created after initialization
		if r.SecretVerifier != nil {
			log.Info("Verifying secrets after initialization")
			verificationResult, err := r.SecretVerifier.VerifyExpectedSecrets(ctx, vtu)
			if err != nil {
				log.Error(err, "Post-initialization secret verification failed")
			} else if !verificationResult.AllPresent {
				log.Error(nil, "Expected secrets missing after initialization",
					"missingCount", len(verificationResult.Missing),
					"incompleteCount", len(verificationResult.Incomplete))
				r.SecretVerifier.LogMissingSecrets(verificationResult)
			}
		}
	} else if vtu.Spec.Initialization.ForceReinitialize && status.Initialized && !status.Sealed {
		// Handle force re-initialization to generate a new root token
		log.Info("Force re-initialization requested for initialized Vault")
		if err := r.GenerateRootToken(ctx, vaultClient, pod, vtu); err != nil {
			return fmt.Errorf("generating root token: %w", err)
		}
	}

	// Update conditions
	r.updateConditions(vtu, status)

	// If vault is sealed, attempt to unseal it
	if status.Sealed && status.Initialized {
		log.Info("Vault is sealed, attempting to unseal")
		if err := r.UnsealVault(ctx, vaultClient, vtu); err != nil {
			log.Error(err, "Failed to unseal vault")
			return fmt.Errorf("unsealing vault: %w", err)
		}

		// Re-check status after unseal
		status, err = vaultClient.CheckStatus(ctx)
		if err != nil {
			return fmt.Errorf("checking vault status after unseal: %w", err)
		}

		log.Info("Vault unseal completed", "sealed", status.Sealed)

		// Update metrics after unseal
		r.MetricsRecorder.RecordVaultStatus(status.Initialized, status.Sealed)
	}

	// Periodically verify expected secrets exist when vault is initialized
	if status.Initialized && r.SecretVerifier != nil {
		log.V(1).Info("Running periodic secret verification")
		verificationResult, err := r.SecretVerifier.VerifyExpectedSecrets(ctx, vtu)
		if err != nil {
			log.Error(err, "Periodic secret verification failed")
		} else if !verificationResult.AllPresent {
			log.Info("Expected secrets missing during periodic check",
				"missingCount", len(verificationResult.Missing),
				"incompleteCount", len(verificationResult.Incomplete))
			r.SecretVerifier.LogMissingSecrets(verificationResult)

			// Record event for missing secrets
			if r.Recorder != nil {
				r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "MissingSecrets",
					"Missing %d secrets, %d incomplete secrets detected",
					len(verificationResult.Missing), len(verificationResult.Incomplete))
			}
		}
	}

	// If vault is unsealed and initialized, handle token management and post-unseal configuration
	if !status.Sealed && status.Initialized {
		// First, handle token management if enabled
		if r.TokenManager != nil && vtu.Spec.TokenManagement != nil {
			log.V(1).Info("Managing admin token")
			if err := r.TokenManager.ReconcileInitialToken(ctx, vtu, vaultClient.GetAPIClient()); err != nil {
				log.Error(err, "Failed to manage admin token")
				// Don't fail the reconciliation, but record the event
				if r.Recorder != nil {
					r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "TokenManagementFailed",
						"Failed to manage admin token: %v", err)
				}
			}
		}

		// Then apply post-unseal configuration if needed
		if (vtu.Spec.PostUnsealConfig.EnableKV || vtu.Spec.PostUnsealConfig.EnableExternalSecretsOperator || vtu.Spec.PostUnsealConfig.EnableVaultConfigOperatorBootstrap) && r.Configurator != nil {
			log := r.Log.WithValues("pod", pod.Name)
			log.V(1).Info("Applying post-unseal configuration")

			// Get the admin token if available
			adminToken, err := r.GetAdminToken(ctx, vtu)
			if err != nil {
				log.Error(err, "Failed to get admin token for configuration, skipping")
				return nil
			}

			// Create a new client with admin token
			apiClient := vaultClient.GetAPIClient()
			apiClient.SetToken(string(adminToken))

			// Apply configuration
			if err := r.Configurator.Configure(ctx, apiClient, vtu.Spec.PostUnsealConfig, &vtu.Status.ConfigurationStatus); err != nil {
				log.Error(err, "Failed to apply post-unseal configuration")
				// Don't fail the reconciliation for configuration errors
			}
		}
	}

	return nil
}

// InitializeVault initializes a new vault instance
func (r *VaultReconciler) InitializeVault(ctx context.Context, vaultClient vault.Client, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	log := r.Log.WithValues("pod", pod.Name)

	// Use retry for idempotency
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		log.Info("Initializing Vault")

		initResp, err := vaultClient.Initialize(ctx, &vault.InitRequest{
			RecoveryShares:    vtu.Spec.Initialization.RecoveryShares,
			RecoveryThreshold: vtu.Spec.Initialization.RecoveryThreshold,
		})
		if err != nil {
			return err
		}

		// Store secrets
		if err := r.StoreSecrets(ctx, vtu, initResp); err != nil {
			return fmt.Errorf("storing secrets: %w", err)
		}

		if r.Recorder != nil {
			r.Recorder.Event(vtu, corev1.EventTypeNormal, "Initialized",
				fmt.Sprintf("Vault pod %s initialized successfully", pod.Name))
		}

		return nil
	})
}

// StoreSecrets stores initialization secrets in Kubernetes
func (r *VaultReconciler) StoreSecrets(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, initResp *vault.InitResponse) error {
	namespace := vtu.Spec.VaultPod.Namespace

	// Prepare annotations for admin token
	annotations := make(map[string]string)
	if vtu.Spec.Initialization.SecretNames.AdminTokenAnnotations != nil {
		for k, v := range vtu.Spec.Initialization.SecretNames.AdminTokenAnnotations {
			annotations[k] = v
		}
	}

	// If TokenManagement is enabled, mark this as a root token that needs replacement
	if r.TokenManager != nil && vtu.Spec.TokenManagement != nil && vtu.Spec.TokenManagement.Enabled {
		annotations["vault.homelab.io/root-token"] = "true"
		annotations["vault.homelab.io/needs-replacement"] = "true"
		r.Log.Info("Storing root token with replacement annotation")
	}

	// Store admin token with annotations
	if err := r.SecretManager.CreateOrUpdateWithOptions(ctx, namespace,
		vtu.Spec.Initialization.SecretNames.AdminToken,
		map[string][]byte{"token": []byte(initResp.RootToken)},
		annotations); err != nil {
		return fmt.Errorf("storing admin token: %w", err)
	}

	// Backup token to transit vault if enabled
	if vtu.Spec.Initialization.TokenRecovery.Enabled && vtu.Spec.Initialization.TokenRecovery.BackupToTransit {
		if err := r.backupTokenToTransit(ctx, vtu, initResp.RootToken); err != nil {
			// Log error but don't fail initialization
			r.Log.Error(err, "Failed to backup token to transit vault")
			if r.Recorder != nil {
				r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "TokenBackupFailed",
					"Failed to backup admin token to transit vault: %v", err)
			}
		} else {
			r.Log.Info("Successfully backed up admin token to transit vault")
			if r.Recorder != nil {
				r.Recorder.Event(vtu, corev1.EventTypeNormal, "TokenBackupSuccess",
					"Admin token backed up to transit vault for recovery")
			}
		}
	}

	// Store recovery keys only if enabled
	if vtu.Spec.Initialization.SecretNames.StoreRecoveryKeys {
		keysData := map[string][]byte{
			"root-token": []byte(initResp.RootToken),
		}
		for i, key := range initResp.RecoveryKeysB64 {
			keysData[fmt.Sprintf("recovery-key-%d", i)] = []byte(key)
		}

		if err := r.SecretManager.CreateOrUpdateWithOptions(ctx, namespace,
			vtu.Spec.Initialization.SecretNames.RecoveryKeys,
			keysData,
			vtu.Spec.Initialization.SecretNames.RecoveryKeysAnnotations); err != nil {
			return fmt.Errorf("storing recovery keys: %w", err)
		}
	} else {
		r.Log.Info("Recovery keys storage disabled - keys must be recorded securely outside of Kubernetes")
		// Log recovery keys to operator logs (they'll appear once and can be captured)
		r.Log.Info("IMPORTANT: Recovery keys generated. Store them securely and delete these logs:",
			"recoveryKeys", initResp.RecoveryKeysB64)
	}

	return nil
}

// hasVaultContainer checks if a pod has a container named "vault"
func (r *VaultReconciler) hasVaultContainer(pod *corev1.Pod) bool {
	for _, c := range pod.Spec.Containers {
		if c.Name == "vault" {
			return true
		}
	}
	return false
}

func (r *VaultReconciler) isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "vault" {
			// For uninitialized Vault, container will be running but not ready
			// Check if container is started instead of ready
			return cs.Started != nil && *cs.Started
		}
	}

	return false
}

// FindVaultPods finds vault pods matching the selector
func (r *VaultReconciler) FindVaultPods(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(vtu.Spec.VaultPod.Namespace),
		client.MatchingLabels(vtu.Spec.VaultPod.Selector)); err != nil {
		return nil, err
	}

	return podList.Items, nil
}

// ValidateTransitToken validates the transit token exists and is not empty
func (r *VaultReconciler) ValidateTransitToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	token, err := r.SecretManager.Get(ctx,
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.TransitVault.SecretRef.Name,
		vtu.Spec.TransitVault.SecretRef.Key)

	if err != nil {
		return fmt.Errorf("transit token secret not found: %w", err)
	}

	if len(token) == 0 {
		return fmt.Errorf("transit token is empty")
	}

	return nil
}

func (r *VaultReconciler) updateConditions(vtu *vaultv1alpha1.VaultTransitUnseal, status *vault.Status) {
	// Use a helper to manage conditions properly
	conditions := NewConditionManager(vtu)

	if status.Initialized {
		conditions.SetCondition("Initialized", metav1.ConditionTrue, "VaultInitialized", "Vault is initialized")
	} else {
		conditions.SetCondition("Initialized", metav1.ConditionFalse, "NotInitialized", "Vault is not initialized")
	}

	if !status.Sealed {
		conditions.SetCondition("Ready", metav1.ConditionTrue, "VaultReady", "Vault is unsealed and ready")
	} else {
		conditions.SetCondition("Ready", metav1.ConditionFalse, "VaultSealed", "Vault is sealed")
	}
}

func (r *VaultReconciler) updateStatus(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	// Update the status with timestamp
	vtu.Status.LastCheckTime = metav1.Now().Format(time.RFC3339)

	// Use Status().Update() for status updates
	return r.Client.Status().Update(ctx, vtu)
}

// UnsealVault unseals a vault instance using transit unseal
func (r *VaultReconciler) UnsealVault(ctx context.Context, vaultClient vault.Client, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	log := r.Log.WithName("unseal")

	// Get transit token
	transitToken, err := r.SecretManager.Get(ctx,
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.TransitVault.SecretRef.Name,
		vtu.Spec.TransitVault.SecretRef.Key)
	if err != nil {
		return fmt.Errorf("getting transit token: %w", err)
	}

	// Resolve transit vault address
	transitAddress, err := ResolveTransitVaultAddress(ctx, r.Client, log, vtu)
	if err != nil {
		return fmt.Errorf("resolving transit vault address: %w", err)
	}

	// Create transit client
	transitClient, err := transit.NewClient(
		transitAddress,
		string(transitToken),
		vtu.Spec.TransitVault.KeyName,
		vtu.Spec.TransitVault.MountPath,
		vtu.Spec.TransitVault.TLSSkipVerify,
		r.getTransitVaultCACert(),
		log)
	if err != nil {
		return fmt.Errorf("creating transit client: %w", err)
	}

	// Attempt to unseal
	if err := transitClient.UnsealVault(ctx, vaultClient.GetAPIClient()); err != nil {
		return fmt.Errorf("unsealing vault: %w", err)
	}

	if r.Recorder != nil {
		r.Recorder.Event(vtu, corev1.EventTypeNormal, "Unsealed", "Vault unsealed successfully")
	}

	return nil
}

// GetAdminToken retrieves the admin token from secret storage
func (r *VaultReconciler) GetAdminToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) ([]byte, error) {
	// Try to get the admin token from the secret
	token, err := r.SecretManager.Get(ctx,
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.Initialization.SecretNames.AdminToken,
		"token")
	if err != nil {
		return nil, fmt.Errorf("getting admin token: %w", err)
	}

	return token, nil
}

// GenerateRootToken generates a new root token for an already initialized Vault
func (r *VaultReconciler) GenerateRootToken(ctx context.Context, vaultClient vault.Client, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	log := r.Log.WithValues("pod", pod.Name)
	log.Info("Generating new root token for initialized Vault")

	// For forceReinitialize, we'll use the same recovery flow
	// This ensures consistent behavior whether recovering from missing token or forcing new token

	// First try to use recovery manager if available
	if r.SecretVerifier != nil && vtu.Spec.Initialization.TokenRecovery.Enabled {
		// Create a fake verification result showing admin token is missing
		result := &secrets.VerificationResult{
			AllPresent: false,
			Missing: []secrets.MissingSecret{
				{
					Name:      vtu.Spec.Initialization.SecretNames.AdminToken,
					Namespace: vtu.Spec.VaultPod.Namespace,
				},
			},
			RecoveryPlan: []secrets.RecoveryAction{
				{
					Type:       "create",
					SecretName: vtu.Spec.Initialization.SecretNames.AdminToken,
					Namespace:  vtu.Spec.VaultPod.Namespace,
				},
			},
		}

		// Use recovery manager to handle token generation
		if r.RecoveryManager != nil {
			if err := r.RecoveryManager.RecoverSecrets(ctx, vtu, result, vaultClient); err != nil {
				log.Error(err, "Failed to recover admin token using recovery manager")
			} else {
				// Success - clear the force reinitialize flag and return
				if err := r.clearForceReinitializeFlag(ctx, vtu); err != nil {
					log.Error(err, "Failed to clear ForceReinitialize flag")
				}
				return nil
			}
		}
	}

	// Fallback to existing behavior if recovery not available
	recoveryKeys, err := r.getRecoveryKeys(ctx, vtu)
	if err != nil {
		// No recovery keys available, create a placeholder token
		log.Info("No recovery keys available, creating admin token placeholder")

		// Generate a temporary token that will need to be replaced manually
		// In a real scenario, you would need to use vault operator generate-root
		placeholder := map[string][]byte{
			"token": []byte("hvs.PLACEHOLDER-" + pod.Name + "-NEEDS-MANUAL-GENERATION"),
			"note":  []byte("This is a placeholder. Use 'vault operator generate-root' with recovery keys to generate a real token"),
		}

		if err := r.SecretManager.CreateOrUpdateWithOptions(ctx, vtu.Spec.VaultPod.Namespace,
			vtu.Spec.Initialization.SecretNames.AdminToken,
			placeholder,
			vtu.Spec.Initialization.SecretNames.AdminTokenAnnotations); err != nil {
			return fmt.Errorf("creating placeholder admin token: %w", err)
		}

		if r.Recorder != nil {
			r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "ManualInterventionRequired",
				"Admin token placeholder created. Run 'vault operator generate-root' manually to generate real token")
		}

		// Clear the ForceReinitialize flag by patching the resource
		if err := r.clearForceReinitializeFlag(ctx, vtu); err != nil {
			log.Error(err, "Failed to clear ForceReinitialize flag")
		}

		return nil
	}

	// Recovery keys are available, but root token generation is manual by design.
	// Create a placeholder and log instructions.
	log.Info("Recovery keys available", "count", len(recoveryKeys))

	placeholder := map[string][]byte{
		"token": []byte("hvs.PLACEHOLDER-" + pod.Name + "-USE-RECOVERY-KEYS"),
		"note":  []byte("Use 'vault operator generate-root' with recovery keys from " + vtu.Spec.Initialization.SecretNames.RecoveryKeys),
	}

	if err := r.SecretManager.CreateOrUpdateWithOptions(ctx, vtu.Spec.VaultPod.Namespace,
		vtu.Spec.Initialization.SecretNames.AdminToken,
		placeholder,
		vtu.Spec.Initialization.SecretNames.AdminTokenAnnotations); err != nil {
		return fmt.Errorf("creating placeholder admin token: %w", err)
	}

	if r.Recorder != nil {
		r.Recorder.Eventf(vtu, corev1.EventTypeNormal, "RootTokenGeneration",
			"Admin token placeholder created. Recovery keys available in secret %s/%s",
			vtu.Spec.VaultPod.Namespace, vtu.Spec.Initialization.SecretNames.RecoveryKeys)
	}

	// Clear the ForceReinitialize flag
	if err := r.clearForceReinitializeFlag(ctx, vtu); err != nil {
		log.Error(err, "Failed to clear ForceReinitialize flag")
	}

	return nil
}

// getRecoveryKeys retrieves recovery keys from the secret
func (r *VaultReconciler) getRecoveryKeys(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) ([]string, error) {
	// Try to get the recovery keys from the secret
	secret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: vtu.Spec.VaultPod.Namespace,
		Name:      vtu.Spec.Initialization.SecretNames.RecoveryKeys,
	}, secret)
	if err != nil {
		return nil, err
	}

	var keys []string
	for i := 0; ; i++ {
		key := fmt.Sprintf("recovery-key-%d", i)
		if data, exists := secret.Data[key]; exists {
			keys = append(keys, string(data))
		} else {
			break
		}
	}

	if len(keys) == 0 {
		return nil, fmt.Errorf("no recovery keys found in secret")
	}

	return keys, nil
}

// clearForceReinitializeFlag patches the VaultTransitUnseal resource to clear the flag
func (r *VaultReconciler) clearForceReinitializeFlag(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	patch := []byte(`{"spec":{"initialization":{"forceReinitialize":false}}}`)

	if err := r.Patch(ctx, vtu, client.RawPatch(types.MergePatchType, patch)); err != nil {
		return fmt.Errorf("patching VaultTransitUnseal to clear forceReinitialize: %w", err)
	}

	r.Log.Info("Cleared ForceReinitialize flag")
	return nil
}

// backupTokenToTransit backs up the admin token to transit vault KV
func (r *VaultReconciler) backupTokenToTransit(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, token string) error {
	// Resolve transit vault address
	resolver := transit.NewAddressResolver(r.Client, r.Log.WithName("transit-resolver"))
	address, err := resolver.ResolveAddress(ctx, &vtu.Spec.TransitVault, vtu.Namespace)
	if err != nil {
		return fmt.Errorf("resolving transit vault address: %w", err)
	}

	// Get transit vault token - from the VTU namespace
	secretNamespace := vtu.Namespace

	transitToken, err := r.SecretManager.Get(ctx,
		secretNamespace,
		vtu.Spec.TransitVault.SecretRef.Name,
		vtu.Spec.TransitVault.SecretRef.Key)
	if err != nil {
		return fmt.Errorf("getting transit token: %w", err)
	}

	// Create KV client for transit vault
	kvClient, err := transit.NewKVClient(
		address,
		string(transitToken),
		vtu.Spec.TransitVault.TLSSkipVerify,
		r.TransitVaultCACert,
		r.Log.WithName("transit-kv"))
	if err != nil {
		return fmt.Errorf("creating transit KV client: %w", err)
	}

	// Ensure KV v2 is enabled (will create if needed)
	if err := kvClient.EnsureKVEnabled(ctx); err != nil {
		return fmt.Errorf("ensuring KV v2 on transit vault: %w", err)
	}

	// Build backup path
	backupPath := transit.BuildTokenBackupPath(
		vtu.Namespace,
		vtu.Name,
		vtu.Spec.Initialization.TokenRecovery.TransitKVPath)

	// Write token with metadata
	metadata := transit.KVMetadata{
		CreatedBy:  "vault-transit-unseal-operator",
		Purpose:    "admin-token-backup",
		BackupTime: time.Now().Format(time.RFC3339),
		Labels: map[string]string{
			"vault.homelab.io/instance":  vtu.Name,
			"vault.homelab.io/namespace": vtu.Namespace,
		},
	}

	if err := kvClient.WriteTokenWithMetadata(ctx, backupPath, token, metadata); err != nil {
		return fmt.Errorf("writing token to transit KV: %w", err)
	}

	return nil
}

// getTransitVaultCACert returns the CA certificate path for the transit vault
func (r *VaultReconciler) getTransitVaultCACert() string {
	return r.TransitVaultCACert
}
