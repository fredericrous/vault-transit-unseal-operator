package secrets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

// RecoveryManager handles recovery of missing secrets
type RecoveryManager struct {
	client.Client
	Log      logr.Logger
	Recorder record.EventRecorder
}

// NewRecoveryManager creates a new recovery manager
func NewRecoveryManager(client client.Client, log logr.Logger, recorder record.EventRecorder) *RecoveryManager {
	return &RecoveryManager{
		Client:   client,
		Log:      log,
		Recorder: recorder,
	}
}

// RecoverSecrets attempts to recover missing secrets
func (r *RecoveryManager) RecoverSecrets(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, result *VerificationResult, vaultClient vault.Client) error {
	if result.AllPresent {
		return nil
	}

	r.Log.Info("Starting secret recovery process",
		"missingCount", len(result.Missing),
		"incompleteCount", len(result.Incomplete))

	for _, action := range result.RecoveryPlan {
		switch action.Type {
		case "create":
			if err := r.recoverMissingSecret(ctx, vtu, action, vaultClient); err != nil {
				r.Log.Error(err, "Failed to recover secret", "secret", action.SecretName)
				if r.Recorder != nil {
					r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "SecretRecoveryFailed",
						"Failed to recover secret %s: %v", action.SecretName, err)
				}
				return err
			}
		case "update":
			if err := r.updateIncompleteSecret(ctx, vtu, action); err != nil {
				r.Log.Error(err, "Failed to update secret", "secret", action.SecretName)
				return err
			}
		}
	}

	if r.Recorder != nil {
		r.Recorder.Event(vtu, corev1.EventTypeNormal, "SecretsRecovered",
			fmt.Sprintf("Successfully recovered %d secrets", len(result.RecoveryPlan)))
	}

	return nil
}

// recoverMissingSecret attempts to recover a completely missing secret
func (r *RecoveryManager) recoverMissingSecret(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction, vaultClient vault.Client) error {
	r.Log.Info("Attempting to recover missing secret",
		"namespace", action.Namespace,
		"name", action.SecretName)

	// Check what type of secret we're recovering
	switch action.SecretName {
	case vtu.Spec.TransitVault.SecretRef.Name:
		return r.recoverTransitToken(ctx, vtu, action)

	case vtu.Spec.Initialization.SecretNames.AdminToken:
		return r.recoverAdminToken(ctx, vtu, action, vaultClient)

	case vtu.Spec.Initialization.SecretNames.RecoveryKeys:
		return r.recoverRecoveryKeys(ctx, vtu, action)

	default:
		return fmt.Errorf("unknown secret type for recovery: %s", action.SecretName)
	}
}

// recoverTransitToken attempts to recover the transit token
func (r *RecoveryManager) recoverTransitToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction) error {
	// Transit token recovery requires manual intervention or backup
	r.Log.Error(nil, "Transit token is missing and cannot be automatically recovered",
		"namespace", action.Namespace,
		"name", action.SecretName,
		"action", "Manual intervention required")

	// Create a placeholder secret with instructions
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      action.SecretName,
			Namespace: action.Namespace,
			Annotations: map[string]string{
				"vault.homelab.io/recovery-required": "true",
				"vault.homelab.io/recovery-reason":   "Transit token missing - manual recovery required",
				"vault.homelab.io/recovery-time":     time.Now().Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{
			vtu.Spec.TransitVault.SecretRef.Key: []byte("PLACEHOLDER-REQUIRES-MANUAL-RECOVERY"),
		},
	}

	if err := controllerutil.SetControllerReference(vtu, secret, r.Scheme()); err != nil {
		return fmt.Errorf("setting controller reference: %w", err)
	}

	if err := r.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating placeholder secret: %w", err)
	}

	return fmt.Errorf("transit token requires manual recovery - placeholder created")
}

// recoverAdminToken attempts to recover the admin token
func (r *RecoveryManager) recoverAdminToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction, vaultClient vault.Client) error {
	r.Log.Info("Attempting to recover admin token")

	// Check if Vault is initialized and unsealed
	status, err := vaultClient.CheckStatus(ctx)
	if err != nil {
		return fmt.Errorf("checking vault status: %w", err)
	}

	if !status.Initialized || status.Sealed {
		return fmt.Errorf("cannot recover admin token: vault is not initialized or is sealed")
	}

	// Try to generate a new root token if we have recovery keys
	recoveryKeys, err := r.getRecoveryKeys(ctx, vtu)
	if err != nil {
		r.Log.Error(err, "Cannot recover admin token without recovery keys")
		return r.createRecoveryPlaceholder(ctx, vtu, action, "Admin token missing - requires recovery keys or manual intervention")
	}

	// Generate new root token using recovery keys
	newToken, err := r.generateRootToken(ctx, vaultClient, recoveryKeys)
	if err != nil {
		r.Log.Error(err, "Failed to generate new root token")
		return r.createRecoveryPlaceholder(ctx, vtu, action, fmt.Sprintf("Failed to generate root token: %v", err))
	}

	// Store the new admin token
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      action.SecretName,
			Namespace: action.Namespace,
			Annotations: map[string]string{
				"vault.homelab.io/recovered":     "true",
				"vault.homelab.io/recovery-time": time.Now().Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{
			"token": []byte(newToken),
		},
	}

	// Apply any configured annotations
	for k, v := range vtu.Spec.Initialization.SecretNames.AdminTokenAnnotations {
		secret.Annotations[k] = v
	}

	if err := controllerutil.SetControllerReference(vtu, secret, r.Scheme()); err != nil {
		return fmt.Errorf("setting controller reference: %w", err)
	}

	if err := r.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating recovered admin token secret: %w", err)
	}

	r.Log.Info("Successfully recovered admin token")
	return nil
}

// recoverRecoveryKeys attempts to recover recovery keys
func (r *RecoveryManager) recoverRecoveryKeys(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction) error {
	// Recovery keys cannot be recovered if lost
	r.Log.Error(nil, "Recovery keys are missing and cannot be automatically recovered",
		"namespace", action.Namespace,
		"name", action.SecretName,
		"action", "Manual intervention or re-initialization required")

	return r.createRecoveryPlaceholder(ctx, vtu, action, "Recovery keys missing - requires re-initialization or restore from backup")
}

// updateIncompleteSecret updates a secret that's missing some keys
func (r *RecoveryManager) updateIncompleteSecret(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction) error {
	r.Log.Info("Updating incomplete secret",
		"namespace", action.Namespace,
		"name", action.SecretName)

	// This would need specific logic based on which keys are missing
	// For now, we'll log and create placeholders
	return fmt.Errorf("incomplete secret update not yet implemented for %s", action.SecretName)
}

// createRecoveryPlaceholder creates a placeholder secret with recovery instructions
func (r *RecoveryManager) createRecoveryPlaceholder(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction, reason string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      action.SecretName,
			Namespace: action.Namespace,
			Annotations: map[string]string{
				"vault.homelab.io/recovery-required": "true",
				"vault.homelab.io/recovery-reason":   reason,
				"vault.homelab.io/recovery-time":     time.Now().Format(time.RFC3339),
			},
		},
		Data: map[string][]byte{
			"placeholder": []byte(fmt.Sprintf("RECOVERY-REQUIRED: %s", reason)),
		},
	}

	if err := controllerutil.SetControllerReference(vtu, secret, r.Scheme()); err != nil {
		return fmt.Errorf("setting controller reference: %w", err)
	}

	if err := r.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating placeholder secret: %w", err)
	}

	return nil
}

// getRecoveryKeys retrieves recovery keys from secret storage
func (r *RecoveryManager) getRecoveryKeys(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) ([]string, error) {
	if !vtu.Spec.Initialization.SecretNames.StoreRecoveryKeys {
		return nil, fmt.Errorf("recovery keys storage is disabled")
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: vtu.Spec.VaultPod.Namespace,
		Name:      vtu.Spec.Initialization.SecretNames.RecoveryKeys,
	}, secret); err != nil {
		return nil, fmt.Errorf("getting recovery keys secret: %w", err)
	}

	var keys []string
	for i := 0; i < vtu.Spec.Initialization.RecoveryShares; i++ {
		key := fmt.Sprintf("recovery-key-%d", i)
		if data, exists := secret.Data[key]; exists {
			keys = append(keys, string(data))
		}
	}

	if len(keys) < vtu.Spec.Initialization.RecoveryThreshold {
		return nil, fmt.Errorf("insufficient recovery keys: have %d, need %d", len(keys), vtu.Spec.Initialization.RecoveryThreshold)
	}

	return keys, nil
}

// generateRootToken generates a new root token using recovery keys
func (r *RecoveryManager) generateRootToken(ctx context.Context, vaultClient vault.Client, recoveryKeys []string) (string, error) {
	// This is a simplified version - actual implementation would use Vault's
	// generate-root API with the recovery keys

	// For now, return an error indicating manual intervention is needed
	return "", fmt.Errorf("automatic root token generation not yet implemented - use vault operator generate-root")
}

// Scheme returns the scheme from the embedded client
func (r *RecoveryManager) Scheme() *runtime.Scheme {
	// This would need to be passed in or retrieved from the client
	// For now, return nil as it would be set during initialization
	return nil
}

// GenerateSecureToken generates a cryptographically secure random token
func GenerateSecureToken(length int) (string, error) {
	bytes := make([]byte, length)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}
	return base64.URLEncoding.EncodeToString(bytes), nil
}
