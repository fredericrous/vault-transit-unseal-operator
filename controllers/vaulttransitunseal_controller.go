package controllers

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	vaultapi "github.com/hashicorp/vault/api"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
)

// VaultTransitUnsealReconciler reconciles a VaultTransitUnseal object
type VaultTransitUnsealReconciler struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=vault.homelab.io,resources=vaulttransitunseals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vault.homelab.io,resources=vaulttransitunseals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=pods/exec,verbs=create
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch

func (r *VaultTransitUnsealReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("vaulttransitunseal", req.NamespacedName)

	// Fetch the VaultTransitUnseal instance
	vtu := &vaultv1alpha1.VaultTransitUnseal{}
	if err := r.Get(ctx, req.NamespacedName, vtu); err != nil {
		if errors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, err
	}

	// Get Vault pods
	pods := &corev1.PodList{}
	if err := r.List(ctx, pods, client.InNamespace(vtu.Spec.VaultPod.Namespace), client.MatchingLabels(vtu.Spec.VaultPod.Selector)); err != nil {
		log.Error(err, "Failed to list Vault pods")
		return ctrl.Result{}, err
	}

	if len(pods.Items) == 0 {
		log.Info("No Vault pods found")
		return ctrl.Result{RequeueAfter: 30 * time.Second}, nil
	}

	// Process each Vault pod
	for _, pod := range pods.Items {
		if pod.Status.Phase != corev1.PodRunning {
			log.Info("Vault pod not running", "pod", pod.Name, "phase", pod.Status.Phase)
			continue
		}

		// Check if vault container is ready
		vaultReady := false
		for _, cs := range pod.Status.ContainerStatuses {
			if cs.Name == "vault" && cs.Ready {
				vaultReady = true
				break
			}
		}

		if !vaultReady {
			log.Info("Vault container not ready", "pod", pod.Name)
			continue
		}

		// Check Vault status
		status, err := r.checkVaultStatus(ctx, &pod, vtu)
		if err != nil {
			log.Error(err, "Failed to check Vault status", "pod", pod.Name)
			continue
		}

		// Update status
		vtu.Status.Initialized = status.Initialized
		vtu.Status.Sealed = status.Sealed
		vtu.Status.LastCheckTime = metav1.Now().Format(time.RFC3339)

		// Handle initialization
		if !status.Initialized {
			log.Info("Vault not initialized, initializing", "pod", pod.Name)
			if err := r.initializeVault(ctx, &pod, vtu); err != nil {
				log.Error(err, "Failed to initialize Vault", "pod", pod.Name)
				r.updateCondition(vtu, "Initialization", "False", "InitializationFailed", err.Error())
			} else {
				r.updateCondition(vtu, "Initialization", "True", "Initialized", "Vault initialized successfully")
				vtu.Status.Initialized = true
			}
		}

		// Ensure unsealed (transit unseal should handle this automatically)
		if status.Sealed {
			log.Info("Vault is sealed, this shouldn't happen with transit unseal", "pod", pod.Name)
			r.updateCondition(vtu, "Unsealed", "False", "Sealed", "Vault is sealed despite transit unseal")
		} else {
			r.updateCondition(vtu, "Unsealed", "True", "Unsealed", "Vault is unsealed")
		}
	}

	// Update status
	if err := r.Status().Update(ctx, vtu); err != nil {
		log.Error(err, "Failed to update VaultTransitUnseal status")
		return ctrl.Result{}, err
	}

	// Requeue after check interval
	checkInterval, _ := time.ParseDuration(vtu.Spec.Monitoring.CheckInterval)
	return ctrl.Result{RequeueAfter: checkInterval}, nil
}

type vaultStatus struct {
	Initialized bool
	Sealed      bool
}

func (r *VaultTransitUnsealReconciler) checkVaultStatus(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) (*vaultStatus, error) {
	// Get transit token - not used for status check, but verify it exists
	_, err := r.getTransitToken(ctx, vtu)
	if err != nil {
		return nil, fmt.Errorf("failed to get transit token: %w", err)
	}

	// Create Vault client
	config := vaultapi.DefaultConfig()
	config.Address = fmt.Sprintf("http://%s.%s.svc.cluster.local:8200", pod.Name, pod.Namespace)

	client, err := vaultapi.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("failed to create vault client: %w", err)
	}

	// Check health
	health, err := client.Sys().Health()
	if err != nil {
		return nil, fmt.Errorf("failed to check vault health: %w", err)
	}

	return &vaultStatus{
		Initialized: health.Initialized,
		Sealed:      health.Sealed,
	}, nil
}

func (r *VaultTransitUnsealReconciler) initializeVault(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	// Create Vault client
	config := vaultapi.DefaultConfig()
	config.Address = fmt.Sprintf("http://%s.%s.svc.cluster.local:8200", pod.Name, pod.Namespace)

	client, err := vaultapi.NewClient(config)
	if err != nil {
		return fmt.Errorf("failed to create vault client: %w", err)
	}

	// Initialize with recovery keys for transit unseal
	initReq := &vaultapi.InitRequest{
		RecoveryShares:    vtu.Spec.Initialization.RecoveryShares,
		RecoveryThreshold: vtu.Spec.Initialization.RecoveryThreshold,
	}

	initResp, err := client.Sys().Init(initReq)
	if err != nil {
		return fmt.Errorf("failed to initialize vault: %w", err)
	}

	// Store admin token
	adminSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vtu.Spec.Initialization.SecretNames.AdminToken,
			Namespace: vtu.Spec.VaultPod.Namespace,
		},
		Data: map[string][]byte{
			"token": []byte(initResp.RootToken),
		},
	}

	if err := r.createOrUpdateSecret(ctx, adminSecret); err != nil {
		return fmt.Errorf("failed to store admin token: %w", err)
	}

	// Store recovery keys
	keysData := map[string][]byte{
		"root-token": []byte(initResp.RootToken),
	}
	for i, key := range initResp.RecoveryKeysB64 {
		keysData[fmt.Sprintf("recovery-key-%d", i)] = []byte(key)
	}

	keysSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      vtu.Spec.Initialization.SecretNames.RecoveryKeys,
			Namespace: vtu.Spec.VaultPod.Namespace,
		},
		Data: keysData,
	}

	if err := r.createOrUpdateSecret(ctx, keysSecret); err != nil {
		return fmt.Errorf("failed to store recovery keys: %w", err)
	}

	r.Log.Info("Vault initialized successfully", "pod", pod.Name)
	return nil
}

func (r *VaultTransitUnsealReconciler) getTransitToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) (string, error) {
	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Name:      vtu.Spec.TransitVault.SecretRef.Name,
		Namespace: vtu.Namespace,
	}, secret); err != nil {
		return "", fmt.Errorf("failed to get transit token secret: %w", err)
	}

	token, ok := secret.Data[vtu.Spec.TransitVault.SecretRef.Key]
	if !ok {
		return "", fmt.Errorf("transit token key %s not found in secret", vtu.Spec.TransitVault.SecretRef.Key)
	}

	return string(token), nil
}

func (r *VaultTransitUnsealReconciler) createOrUpdateSecret(ctx context.Context, secret *corev1.Secret) error {
	existing := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace}, existing)
	if err != nil && !errors.IsNotFound(err) {
		return err
	}

	if errors.IsNotFound(err) {
		return r.Create(ctx, secret)
	}

	existing.Data = secret.Data
	return r.Update(ctx, existing)
}

func (r *VaultTransitUnsealReconciler) updateCondition(vtu *vaultv1alpha1.VaultTransitUnseal, condType, status, reason, message string) {
	// Implementation of condition update logic
	for i := range vtu.Status.Conditions {
		if vtu.Status.Conditions[i].Type == condType {
			vtu.Status.Conditions[i].Status = status
			vtu.Status.Conditions[i].Reason = reason
			vtu.Status.Conditions[i].Message = message
			vtu.Status.Conditions[i].LastTransitionTime = metav1.Now().Format(time.RFC3339)
			return
		}
	}

	// Condition not found, add it
	vtu.Status.Conditions = append(vtu.Status.Conditions, vaultv1alpha1.Condition{
		Type:               condType,
		Status:             status,
		Reason:             reason,
		Message:            message,
		LastTransitionTime: metav1.Now().Format(time.RFC3339),
	})
}

func (r *VaultTransitUnsealReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&vaultv1alpha1.VaultTransitUnseal{}).
		Owns(&corev1.Secret{}).
		Complete(r)
}
