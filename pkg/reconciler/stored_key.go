package reconciler

import (
	"context"
	"fmt"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	operrors "github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/errors"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/storedkey"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

// Condition types and event reasons owned by stored-key mode.
const (
	// ConditionKeySecretPresent reports whether the Secret named by
	// spec.storedKey.secretRef exists and carries at least one share.
	ConditionKeySecretPresent = "KeySecretPresent"

	// ReasonAwaitingExternalInit is the Initialized condition's reason when
	// Vault has never been initialized and this mode refuses to do it.
	ReasonAwaitingExternalInit = "AwaitingExternalInit"
)

// awaitingInitMessage explains, on the CR itself, why the operator is standing
// still in front of an uninitialized Vault. Initialization in stored-key mode
// belongs to the `bootstrap run <cluster> vault-setup` CLI step, which is also
// what seeds the unseal-key Secret. An operator that initialized on its own
// would mint shares nobody captured, and would happily "initialize" a Vault
// that only LOOKS empty because its storage backend came up wrong — turning a
// recoverable outage into a permanent one.
const awaitingInitMessage = "Vault is not initialized; stored-key mode never initializes. Run the bootstrap vault-setup step, which initializes Vault and seeds the unseal-key Secret."

// ValidateStoredKeySecret checks that the unseal-key Secret exists and holds at
// least one share, and records the answer on the KeySecretPresent condition.
// Returns the parsed shares so a caller that is about to unseal does not have
// to read the Secret twice.
func (r *VaultReconciler) ValidateStoredKeySecret(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) ([]string, error) {
	namespace, name, key := vtu.Spec.StoredKeySecretRef()
	conditions := NewConditionManager(vtu)

	raw, err := r.SecretManager.Get(ctx, namespace, name, key)
	if err != nil {
		conditions.SetCondition(ConditionKeySecretPresent, metav1.ConditionFalse, "SecretUnreadable",
			fmt.Sprintf("cannot read key %q of secret %s/%s", key, namespace, name))
		return nil, fmt.Errorf("reading unseal key secret %s/%s: %w", namespace, name, err)
	}

	keys := storedkey.ParseKeys(raw)
	if len(keys) == 0 {
		conditions.SetCondition(ConditionKeySecretPresent, metav1.ConditionFalse, "NoShares",
			fmt.Sprintf("key %q of secret %s/%s holds no unseal key share", key, namespace, name))
		return nil, fmt.Errorf("unseal key secret %s/%s: %w", namespace, name, storedkey.ErrNoKeys)
	}

	conditions.SetCondition(ConditionKeySecretPresent, metav1.ConditionTrue, "SharesAvailable",
		fmt.Sprintf("%d unseal key share(s) available from %s/%s", len(keys), namespace, name))

	return keys, nil
}

// handleUninitializedStoredKey reports an uninitialized Vault without touching
// it. See awaitingInitMessage for why this mode never initializes.
func (r *VaultReconciler) handleUninitializedStoredKey(vtu *vaultv1alpha1.VaultTransitUnseal, pod *corev1.Pod) {
	r.Log.WithValues("pod", pod.Name).Info("Vault is not initialized; stored-key mode leaves initialization to the bootstrap CLI")

	NewConditionManager(vtu).SetCondition("Initialized", metav1.ConditionFalse, ReasonAwaitingExternalInit, awaitingInitMessage)

	if r.Recorder != nil {
		r.Recorder.Event(vtu, corev1.EventTypeWarning, "InitializationDeferred", awaitingInitMessage)
	}
}

// UnsealWithStoredKey submits the stored share(s) to a sealed Vault pod.
//
// Unlike transit mode — where a sealed pod can only be recovered by restarting
// it so Vault re-runs its seal stanza — a Shamir-sealed Vault unseals over the
// API on the running process, so nothing is restarted and no request is
// interrupted.
func (r *VaultReconciler) UnsealWithStoredKey(ctx context.Context, vaultClient vault.Client, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) error {
	log := r.Log.WithValues("pod", pod.Name)

	keys, err := r.ValidateStoredKeySecret(ctx, vtu)
	if err != nil {
		namespace, name, _ := vtu.Spec.StoredKeySecretRef()
		if r.Recorder != nil {
			r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "UnsealKeyUnavailable",
				"Vault pod %s is sealed but secret %s/%s holds no usable unseal key share", pod.Name, namespace, name)
		}
		r.MetricsRecorder.RecordUnsealAttempt(string(vaultv1alpha1.UnsealModeStoredKey), false)
		return operrors.NewConfigError("unseal key unavailable", err)
	}

	log.Info("Vault is sealed; submitting stored unseal key share(s)", "shares", len(keys))

	result, err := storedkey.Unseal(ctx, vaultClient.GetAPIClient(), keys)
	if err != nil {
		r.MetricsRecorder.RecordUnsealAttempt(string(vaultv1alpha1.UnsealModeStoredKey), false)
		NewConditionManager(vtu).SetCondition("Ready", metav1.ConditionFalse, "UnsealFailed", err.Error())
		if r.Recorder != nil {
			r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "UnsealFailed",
				"Failed to unseal pod %s from stored key: %v", pod.Name, err)
		}
		return fmt.Errorf("unsealing %s from stored key: %w", pod.Name, err)
	}

	r.MetricsRecorder.RecordUnsealAttempt(string(vaultv1alpha1.UnsealModeStoredKey), true)
	r.MetricsRecorder.RecordVaultStatus(true, false)

	vtu.Status.Sealed = false
	vtu.Status.LastUnsealTime = metav1.Now().Format(time.RFC3339)
	NewConditionManager(vtu).SetCondition("Ready", metav1.ConditionTrue, "VaultReady", "Vault is unsealed and ready")

	log.Info("Vault unsealed from stored key", "sharesSubmitted", result.SharesSubmitted, "threshold", result.Threshold)
	if r.Recorder != nil {
		r.Recorder.Eventf(vtu, corev1.EventTypeNormal, "Unsealed",
			"Unsealed pod %s with %d stored key share(s)", pod.Name, result.SharesSubmitted)
	}

	return nil
}
