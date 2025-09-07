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

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
	operrors "github.com/fredericrous/homelab/vault-operator/pkg/errors"
	"github.com/fredericrous/homelab/vault-operator/pkg/vault"
)

// VaultReconciler handles the main reconciliation logic
type VaultReconciler struct {
	client.Client
	Log             logr.Logger
	Recorder        record.EventRecorder
	VaultFactory    VaultClientFactory
	SecretManager   SecretManager
	MetricsRecorder MetricsRecorder
}

// VaultClientFactory creates Vault clients
type VaultClientFactory interface {
	NewClientForPod(pod *corev1.Pod) (vault.Client, error)
}

// SecretManager handles K8s secret operations
type SecretManager interface {
	CreateOrUpdate(ctx context.Context, namespace, name string, data map[string][]byte) error
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
	if err := r.validateTransitToken(ctx, vtu); err != nil {
		if r.Recorder != nil {
			r.Recorder.Event(vtu, corev1.EventTypeWarning, "InvalidConfig", err.Error())
		}
		result.Error = operrors.NewConfigError("transit token validation failed", err).
			WithContext("resource", client.ObjectKeyFromObject(vtu))
		return result
	}

	// Find Vault pods
	pods, err := r.findVaultPods(ctx, vtu)
	if err != nil {
		result.Error = operrors.NewTransientError("failed to find vault pods", err).
			WithContext("namespace", vtu.Spec.VaultPod.Namespace).
			WithContext("selector", vtu.Spec.VaultPod.Selector)
		return result
	}

	if len(pods) == 0 {
		log.Info("No Vault pods found, requeueing")
		result.RequeueAfter = 30 * time.Second
		return result
	}

	// Process each pod
	allHealthy := true
	for _, pod := range pods {
		if err := r.processPod(ctx, &pod, vtu); err != nil {
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

func (r *VaultReconciler) processPod(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) error {
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
		if err := r.initializeVault(ctx, vaultClient, pod, vtu); err != nil {
			r.MetricsRecorder.RecordInitialization(false)
			return fmt.Errorf("initializing vault: %w", err)
		}
		r.MetricsRecorder.RecordInitialization(true)
	}

	// Update conditions
	r.updateConditions(vtu, status)

	return nil
}

func (r *VaultReconciler) initializeVault(ctx context.Context, vaultClient vault.Client, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	log := r.Log.WithValues("pod", pod.Name)

	// Use retry for idempotency
	return retry.RetryOnConflict(retry.DefaultRetry, func() error {
		log.Info("Initializing Vault")

		initResp, err := vaultClient.Initialize(ctx,
			vtu.Spec.Initialization.RecoveryShares,
			vtu.Spec.Initialization.RecoveryThreshold)
		if err != nil {
			return err
		}

		// Store secrets
		if err := r.storeSecrets(ctx, vtu, initResp); err != nil {
			return fmt.Errorf("storing secrets: %w", err)
		}

		if r.Recorder != nil {
			r.Recorder.Event(vtu, corev1.EventTypeNormal, "Initialized",
				fmt.Sprintf("Vault pod %s initialized successfully", pod.Name))
		}

		return nil
	})
}

func (r *VaultReconciler) storeSecrets(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, initResp *vault.InitResponse) error {
	namespace := vtu.Spec.VaultPod.Namespace

	// Store admin token
	if err := r.SecretManager.CreateOrUpdate(ctx, namespace,
		vtu.Spec.Initialization.SecretNames.AdminToken,
		map[string][]byte{"token": []byte(initResp.RootToken)}); err != nil {
		return fmt.Errorf("storing admin token: %w", err)
	}

	// Store recovery keys
	keysData := map[string][]byte{
		"root-token": []byte(initResp.RootToken),
	}
	for i, key := range initResp.RecoveryKeysB64 {
		keysData[fmt.Sprintf("recovery-key-%d", i)] = []byte(key)
	}

	if err := r.SecretManager.CreateOrUpdate(ctx, namespace,
		vtu.Spec.Initialization.SecretNames.RecoveryKeys,
		keysData); err != nil {
		return fmt.Errorf("storing recovery keys: %w", err)
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

func (r *VaultReconciler) findVaultPods(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) ([]corev1.Pod, error) {
	podList := &corev1.PodList{}
	if err := r.List(ctx, podList,
		client.InNamespace(vtu.Spec.VaultPod.Namespace),
		client.MatchingLabels(vtu.Spec.VaultPod.Selector)); err != nil {
		return nil, err
	}

	return podList.Items, nil
}

func (r *VaultReconciler) validateTransitToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) error {
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
	// Use SSA for conflict-free updates
	patch := &vaultv1alpha1.VaultTransitUnseal{
		TypeMeta: metav1.TypeMeta{
			APIVersion: vaultv1alpha1.GroupVersion.String(),
			Kind:       "VaultTransitUnseal",
		},
		ObjectMeta: metav1.ObjectMeta{
			Name:      vtu.Name,
			Namespace: vtu.Namespace,
		},
		Status: vtu.Status,
	}

	patch.Status.LastCheckTime = metav1.Now().Format(time.RFC3339)

	return r.Client.Status().Patch(ctx, patch, client.Apply,
		client.FieldOwner("vault-operator"),
		client.ForceOwnership)
}
