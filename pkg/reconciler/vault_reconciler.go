package reconciler

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/retry"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/configuration"
	operrors "github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/errors"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/transit"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

// VaultReconciler handles the main reconciliation logic
type VaultReconciler struct {
	client.Client
	Log             logr.Logger
	Recorder        record.EventRecorder
	VaultFactory    VaultClientFactory
	SecretManager   SecretManager
	MetricsRecorder MetricsRecorder
	Configurator    *configuration.Configurator
}

// VaultClientFactory creates Vault clients
type VaultClientFactory interface {
	NewClientForPod(pod *corev1.Pod) (vault.Client, error)
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
	if !r.isPodReady(pod) {
		return fmt.Errorf("pod not ready")
	}

	// Create Vault client for this pod
	vaultClient, err := r.VaultFactory.NewClientForPod(pod)
	if err != nil {
		return fmt.Errorf("creating vault client: %w", err)
	}

	// Check status with timeout
	statusCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	status, err := vaultClient.CheckStatus(statusCtx)
	if err != nil {
		return fmt.Errorf("checking vault status: %w", err)
	}

	// Update metrics
	r.MetricsRecorder.RecordVaultStatus(status.Initialized, status.Sealed)

	// Handle initialization if needed
	if !status.Initialized {
		if err := r.InitializeVault(ctx, vaultClient, pod, vtu); err != nil {
			r.MetricsRecorder.RecordInitialization(false)
			return fmt.Errorf("initializing vault: %w", err)
		}
		r.MetricsRecorder.RecordInitialization(true)
	}

	// Update conditions
	r.updateConditions(vtu, status)

	// If vault is sealed, attempt to unseal it
	if status.Sealed && status.Initialized {
		if err := r.UnsealVault(ctx, vaultClient, vtu); err != nil {
			return fmt.Errorf("unsealing vault: %w", err)
		}

		// Re-check status after unseal
		status, err = vaultClient.CheckStatus(ctx)
		if err != nil {
			return fmt.Errorf("checking vault status after unseal: %w", err)
		}

		// Update metrics after unseal
		r.MetricsRecorder.RecordVaultStatus(status.Initialized, status.Sealed)
	}

	// If vault is unsealed and initialized, apply post-unseal configuration
	if !status.Sealed && status.Initialized && (vtu.Spec.PostUnsealConfig.EnableKV || vtu.Spec.PostUnsealConfig.EnableExternalSecretsOperator) && r.Configurator != nil {
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

	// Store admin token with annotations if provided
	if err := r.SecretManager.CreateOrUpdateWithOptions(ctx, namespace,
		vtu.Spec.Initialization.SecretNames.AdminToken,
		map[string][]byte{"token": []byte(initResp.RootToken)},
		vtu.Spec.Initialization.SecretNames.AdminTokenAnnotations); err != nil {
		return fmt.Errorf("storing admin token: %w", err)
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

func (r *VaultReconciler) isPodReady(pod *corev1.Pod) bool {
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "vault" && cs.Ready {
			return true
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
