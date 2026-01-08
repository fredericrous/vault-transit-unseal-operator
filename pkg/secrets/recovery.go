package secrets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"strings"
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
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/transit"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

// TokenManager interface for creating scoped tokens
type TokenManager interface {
	CreateScopedTokenFromRoot(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, rootToken string) (string, error)
}

// RecoveryManager handles recovery of missing secrets
type RecoveryManager struct {
	client.Client
	Log                logr.Logger
	Recorder           record.EventRecorder
	Scheme             *runtime.Scheme
	secretManager      Manager
	tokenManager       TokenManager
	TransitVaultCACert string // Path to CA certificate for transit vault
}

// NewRecoveryManager creates a new recovery manager
func NewRecoveryManager(client client.Client, log logr.Logger, recorder record.EventRecorder, scheme *runtime.Scheme, secretManager Manager, tokenManager TokenManager, transitVaultCACert string) *RecoveryManager {
	return &RecoveryManager{
		Client:             client,
		Log:                log,
		Recorder:           recorder,
		Scheme:             scheme,
		secretManager:      secretManager,
		tokenManager:       tokenManager,
		TransitVaultCACert: transitVaultCACert,
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
		case "restore":
			// Handle replacing existing placeholder/incomplete admin tokens
			if err := r.recoverMissingSecret(ctx, vtu, action, vaultClient); err != nil {
				r.Log.Error(err, "Failed to restore secret", "secret", action.SecretName)
				if r.Recorder != nil {
					r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "SecretRestoreFailed",
						"Failed to restore secret %s: %v", action.SecretName, err)
				}
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
		// Check if admin token creation is skipped
		if vtu.Spec.Initialization.SecretNames.SkipAdminTokenCreation {
			r.Log.Info("Admin token creation is disabled, skipping recovery",
				"namespace", action.Namespace,
				"name", action.SecretName)
			return nil
		}
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

	// Only set controller reference if in the same namespace to avoid cross-namespace ownership
	if vtu.Namespace == secret.Namespace {
		if err := controllerutil.SetControllerReference(vtu, secret, r.Scheme); err != nil {
			return fmt.Errorf("setting controller reference: %w", err)
		}
	} else {
		// Add annotation to indicate ownership when we can't use owner references
		secret.Annotations["vault.homelab.io/managed-by"] = fmt.Sprintf("%s/%s", vtu.Namespace, vtu.Name)
	}

	if err := r.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating placeholder secret: %w", err)
	}

	return fmt.Errorf("transit token requires manual recovery - placeholder created")
}

// recoverAdminToken attempts to recover the admin token
func (r *RecoveryManager) recoverAdminToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction, vaultClient vault.Client) error {
	r.Log.Info("Admin token is missing, attempting recovery")

	// Check if Vault is initialized and unsealed
	status, err := vaultClient.CheckStatus(ctx)
	if err != nil {
		return fmt.Errorf("checking vault status: %w", err)
	}

	if !status.Initialized || status.Sealed {
		return r.createRecoveryPlaceholder(ctx, vtu, action,
			"Cannot recover admin token: vault is not initialized or is sealed")
	}

	// Step 1: Try to recover from transit vault backup if enabled
	if vtu.Spec.Initialization.TokenRecovery.Enabled && vtu.Spec.Initialization.TokenRecovery.BackupToTransit {
		r.Log.Info("Attempting to recover token from transit vault backup")

		token, err := r.recoverTokenFromTransit(ctx, vtu)
		if err != nil {
			r.Log.Error(err, "Failed to recover token from transit vault")
		} else if token != "" {
			// Successfully recovered token from backup
			r.Log.Info("Successfully recovered admin token from transit vault backup")

			// Check if this is a root token and TokenManagement is enabled
			if r.tokenManager != nil && vtu.Spec.TokenManagement != nil && vtu.Spec.TokenManagement.Enabled {
				// Check if the recovered token is a root token by looking it up
				isRoot, err := r.isRootToken(vaultClient, token)
				if err != nil {
					r.Log.Error(err, "Failed to check if token is root, assuming it is for safety")
					isRoot = true
				}
				if isRoot {
					r.Log.Info("Recovered token is root, creating scoped token")
					scopedToken, err := r.tokenManager.CreateScopedTokenFromRoot(ctx, vtu, token)
					if err != nil {
						r.Log.Error(err, "Failed to create scoped token from recovered root token")
						// Fall back to using the root token
					} else {
						r.Log.Info("Successfully created scoped token from recovered root token")
						token = scopedToken
					}
				}
			}

			return r.restoreAdminToken(ctx, vtu, action, token)
		}
	}

	// Step 2: Fall back to placeholder with detailed instructions
	r.Log.Info("Admin token missing, manual recovery required")

	// Create detailed recovery instructions
	recoveryInstructions := fmt.Sprintf(`Admin token is missing and requires manual recovery.

Follow these steps to recover the admin token:

1. Get the recovery keys (if stored in Kubernetes):
   kubectl get secret %s -n %s -o jsonpath='{.data}' | jq -r 'to_entries[] | select(.key | startswith("recovery-key-")) | .value' | base64 -d

2. Generate a new root token:
   # First, get a Vault pod name
   export VAULT_POD=$(kubectl get pods -n %s -l %s -o jsonpath='{.items[0].metadata.name}')
   
   # Start root token generation
   kubectl exec -n %s $VAULT_POD -- vault operator generate-root -init
   # Note the OTP and Nonce values from the output

   # Provide recovery keys (repeat for each key until threshold is met)
   kubectl exec -n %s $VAULT_POD -- vault operator generate-root -nonce=<NONCE> <RECOVERY_KEY>

   # Decode the final token
   kubectl exec -n %s $VAULT_POD -- vault operator generate-root -decode=<ENCODED_TOKEN> -otp=<OTP>

3. Create a scoped admin token (recommended instead of using root directly):
   # Set the root token
   export VAULT_TOKEN=<ROOT_TOKEN_FROM_STEP_2>
   
   # Create admin policy if not exists
   kubectl exec -n %s $VAULT_POD -- vault policy write vault-admin - <<EOF
path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}
EOF

   # Create scoped admin token
   kubectl exec -n %s $VAULT_POD -- vault token create -policy=vault-admin -ttl=24h

4. Update the admin token secret:
   kubectl create secret generic %s -n %s --from-literal=token=<ADMIN_TOKEN> --dry-run=client -o yaml | kubectl apply -f -

5. IMPORTANT: Revoke the root token after creating the admin token:
   kubectl exec -n %s $VAULT_POD -- vault token revoke <ROOT_TOKEN>

If recovery keys are not available in Kubernetes, you will need to retrieve them from your secure storage.`,
		vtu.Spec.Initialization.SecretNames.RecoveryKeys,
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.VaultPod.Namespace,
		labelSelectorString(vtu.Spec.VaultPod.Selector),
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.Initialization.SecretNames.AdminToken,
		vtu.Spec.VaultPod.Namespace,
		vtu.Spec.VaultPod.Namespace,
	)

	// Record an event with clear instructions
	if r.Recorder != nil {
		r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "ManualRecoveryRequired",
			"Admin token is missing. See secret %s/%s annotations for detailed recovery instructions",
			action.Namespace, action.SecretName)
	}

	// Create placeholder with detailed instructions
	return r.createRecoveryPlaceholder(ctx, vtu, action, recoveryInstructions)
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

// updateIncompleteSecret handles a secret that's missing some keys
func (r *RecoveryManager) updateIncompleteSecret(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction) error {
	r.Log.Info("Secret is incomplete, manual intervention required",
		"namespace", action.Namespace,
		"name", action.SecretName)

	// Get the existing secret to preserve any valid data
	existingSecret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: action.Namespace,
		Name:      action.SecretName,
	}, existingSecret); err != nil {
		return fmt.Errorf("getting existing secret: %w", err)
	}

	// Add annotations to indicate the secret is incomplete
	if existingSecret.Annotations == nil {
		existingSecret.Annotations = make(map[string]string)
	}
	existingSecret.Annotations["vault.homelab.io/incomplete"] = "true"
	existingSecret.Annotations["vault.homelab.io/incomplete-reason"] = action.Description
	existingSecret.Annotations["vault.homelab.io/incomplete-time"] = time.Now().Format(time.RFC3339)

	// Update the secret with annotations
	if err := r.Update(ctx, existingSecret); err != nil {
		return fmt.Errorf("updating incomplete secret annotations: %w", err)
	}

	// Record an event
	if r.Recorder != nil {
		r.Recorder.Eventf(vtu, corev1.EventTypeWarning, "IncompleteSecret",
			"Secret %s/%s is incomplete: %s. Manual intervention required.",
			action.Namespace, action.SecretName, action.Description)
	}

	// Don't return an error - just log the warning
	// This allows the operator to continue functioning with partial secrets
	r.Log.Info("Marked secret as incomplete, continuing with partial functionality",
		"secret", fmt.Sprintf("%s/%s", action.Namespace, action.SecretName))

	return nil
}

// createRecoveryPlaceholder creates a placeholder secret with recovery instructions
func (r *RecoveryManager) createRecoveryPlaceholder(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction, reason string) error {
	// For admin token, include the full reason as the token value for better visibility
	data := map[string][]byte{
		"placeholder": []byte(fmt.Sprintf("RECOVERY-REQUIRED: See recovery-instructions.txt")),
	}
	
	if action.SecretName == vtu.Spec.Initialization.SecretNames.AdminToken {
		data["token"] = []byte("PLACEHOLDER-MANUAL-RECOVERY-REQUIRED")
		data["recovery-instructions.txt"] = []byte(reason)
	}

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      action.SecretName,
			Namespace: action.Namespace,
			Annotations: map[string]string{
				"vault.homelab.io/recovery-required": "true",
				"vault.homelab.io/recovery-reason":   "Manual intervention required - see recovery-instructions.txt in secret data",
				"vault.homelab.io/recovery-time":     time.Now().Format(time.RFC3339),
			},
		},
		Data: data,
	}

	// Only set controller reference if in the same namespace to avoid cross-namespace ownership
	if vtu.Namespace == secret.Namespace {
		if err := controllerutil.SetControllerReference(vtu, secret, r.Scheme); err != nil {
			return fmt.Errorf("setting controller reference: %w", err)
		}
	} else {
		// Add annotation to indicate ownership when we can't use owner references
		secret.Annotations["vault.homelab.io/managed-by"] = fmt.Sprintf("%s/%s", vtu.Namespace, vtu.Name)
	}

	if err := r.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating placeholder secret: %w", err)
	}

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

// recoverTokenFromTransit attempts to recover admin token from transit vault backup
func (r *RecoveryManager) recoverTokenFromTransit(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) (string, error) {
	// Resolve transit vault address
	resolver := transit.NewAddressResolver(r.Client, r.Log.WithName("transit-resolver"))
	address, err := resolver.ResolveAddress(ctx, &vtu.Spec.TransitVault, vtu.Namespace)
	if err != nil {
		return "", fmt.Errorf("resolving transit vault address: %w", err)
	}

	// Get transit vault token - from the VTU namespace
	secretNamespace := vtu.Namespace

	transitToken, err := r.secretManager.Get(ctx,
		secretNamespace,
		vtu.Spec.TransitVault.SecretRef.Name,
		vtu.Spec.TransitVault.SecretRef.Key)
	if err != nil {
		return "", fmt.Errorf("getting transit token: %w", err)
	}

	// Create KV client for transit vault
	kvClient, err := transit.NewKVClient(
		address,
		string(transitToken),
		vtu.Spec.TransitVault.TLSSkipVerify,
		r.TransitVaultCACert,
		r.Log.WithName("transit-kv"))
	if err != nil {
		return "", fmt.Errorf("creating transit KV client: %w", err)
	}

	// Ensure KV v2 is enabled (will create if needed)
	if err := kvClient.EnsureKVEnabled(ctx); err != nil {
		// Log warning but don't fail recovery - reading might still work
		r.Log.Error(err, "Failed to ensure KV v2 on transit vault during recovery")
	}

	// Build backup path
	backupPath := transit.BuildTokenBackupPath(
		vtu.Namespace,
		vtu.Name,
		vtu.Spec.Initialization.TokenRecovery.TransitKVPath)

	// Read token from backup
	token, err := kvClient.ReadToken(ctx, backupPath)
	if err != nil {
		return "", fmt.Errorf("reading token from transit KV: %w", err)
	}

	return token, nil
}

// cleanupMixedStateSecret handles secrets that have both valid data and placeholder/incomplete markers
func (r *RecoveryManager) cleanupMixedStateSecret(ctx context.Context, secret *corev1.Secret) error {
	r.Log.Info("Cleaning up mixed-state secret",
		"namespace", secret.Namespace,
		"name", secret.Name)

	updated := false

	// Remove placeholder data if a valid token exists
	if tokenData, hasToken := secret.Data["token"]; hasToken && len(tokenData) > 0 {
		// Only remove placeholder if the token is not itself a placeholder
		tokenStr := string(tokenData)
		if !strings.HasPrefix(tokenStr, "placeholder-") &&
			!strings.HasPrefix(tokenStr, "RECOVERY-REQUIRED:") &&
			len(tokenStr) > 10 {
			if _, hasPlaceholder := secret.Data["placeholder"]; hasPlaceholder {
				delete(secret.Data, "placeholder")
				updated = true
				r.Log.Info("Removed placeholder data from secret with valid token")
			}
		}
	}

	// Clean up incorrect annotations if the token is valid
	if secret.Annotations != nil {
		// Remove incomplete markers if we have valid token data
		if tokenData, hasToken := secret.Data["token"]; hasToken && len(tokenData) > 0 &&
			!strings.HasPrefix(string(tokenData), "placeholder-") &&
			!strings.HasPrefix(string(tokenData), "RECOVERY-REQUIRED:") {

			if _, hasIncomplete := secret.Annotations["vault.homelab.io/incomplete"]; hasIncomplete {
				delete(secret.Annotations, "vault.homelab.io/incomplete")
				delete(secret.Annotations, "vault.homelab.io/incomplete-reason")
				delete(secret.Annotations, "vault.homelab.io/incomplete-time")
				updated = true
				r.Log.Info("Removed incomplete annotations from secret with valid token")
			}

			if _, hasRecoveryRequired := secret.Annotations["vault.homelab.io/recovery-required"]; hasRecoveryRequired {
				delete(secret.Annotations, "vault.homelab.io/recovery-required")
				updated = true
				r.Log.Info("Removed recovery-required annotation from secret with valid token")
			}
		}
	}

	if updated {
		if err := r.Update(ctx, secret); err != nil {
			return fmt.Errorf("updating cleaned secret: %w", err)
		}
		r.Log.Info("Successfully cleaned up mixed-state secret")
	}

	return nil
}

// restoreAdminToken restores the recovered admin token to Kubernetes secret
func (r *RecoveryManager) restoreAdminToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, action RecoveryAction, token string) error {
	// Check if the secret already exists
	existingSecret := &corev1.Secret{}
	err := r.Get(ctx, types.NamespacedName{
		Namespace: action.Namespace,
		Name:      action.SecretName,
	}, existingSecret)

	secretExists := err == nil

	// If secret exists, try to clean it up first
	if secretExists {
		// Check if it's a mixed-state secret that can be cleaned
		if tokenData, hasToken := existingSecret.Data["token"]; hasToken && len(tokenData) > 0 {
			// Decode and check if the existing token is valid
			existingToken := string(tokenData)
			if !strings.HasPrefix(existingToken, "placeholder-") &&
				!strings.HasPrefix(existingToken, "RECOVERY-REQUIRED:") &&
				len(existingToken) > 10 {
				// Existing token looks valid, just clean up the secret
				if err := r.cleanupMixedStateSecret(ctx, existingSecret); err != nil {
					r.Log.Error(err, "Failed to cleanup mixed-state secret")
				}
				r.Log.Info("Existing secret has valid token, skipping replacement")
				return nil
			}
		}

		// Otherwise, delete and replace
		r.Log.Info("Existing admin token secret found, deleting to replace",
			"namespace", action.Namespace,
			"name", action.SecretName)

		if err := r.Delete(ctx, existingSecret); err != nil {
			return fmt.Errorf("deleting existing admin token secret: %w", err)
		}

		// Wait a moment for deletion to propagate
		time.Sleep(100 * time.Millisecond)
	}

	// Create new secret with recovered token
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        action.SecretName,
			Namespace:   action.Namespace,
			Annotations: vtu.Spec.Initialization.SecretNames.AdminTokenAnnotations,
		},
		Data: map[string][]byte{"token": []byte(token)},
	}

	// Add recovery annotation
	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	secret.Annotations["vault.homelab.io/recovered"] = "true"
	secret.Annotations["vault.homelab.io/recovery-time"] = time.Now().Format(time.RFC3339)

	// Only set controller reference if in the same namespace
	if vtu.Namespace == secret.Namespace {
		if err := controllerutil.SetControllerReference(vtu, secret, r.Scheme); err != nil {
			return fmt.Errorf("setting controller reference: %w", err)
		}
	}

	if err := r.Create(ctx, secret); err != nil {
		return fmt.Errorf("creating admin token secret: %w", err)
	}

	if r.Recorder != nil {
		action := "recovered"
		if secretExists {
			action = "replaced"
		}
		r.Recorder.Eventf(vtu, corev1.EventTypeNormal, "TokenRecovered",
			"Admin token successfully %s", action)
	}

	return nil
}

// backupTokenToTransit backs up token to transit vault
func (r *RecoveryManager) backupTokenToTransit(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, token string) error {
	// Resolve transit vault address
	resolver := transit.NewAddressResolver(r.Client, r.Log.WithName("transit-resolver"))
	address, err := resolver.ResolveAddress(ctx, &vtu.Spec.TransitVault, vtu.Namespace)
	if err != nil {
		return fmt.Errorf("resolving transit vault address: %w", err)
	}

	// Get transit vault token - from the VTU namespace
	secretNamespace := vtu.Namespace

	transitToken, err := r.secretManager.Get(ctx,
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
		Purpose:    "admin-token-backup-recovery",
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

// isRootToken checks if a token is a root token by looking it up and checking policies.
func (r *RecoveryManager) isRootToken(vaultClient vault.Client, token string) (bool, error) {
	apiClient := vaultClient.GetAPIClient()
	if apiClient == nil {
		return false, fmt.Errorf("vault client does not support API access")
	}

	// Set the token temporarily
	oldToken := apiClient.Token()
	apiClient.SetToken(token)
	defer apiClient.SetToken(oldToken)

	// Look up the token
	secret, err := apiClient.Logical().Write("auth/token/lookup-self", nil)
	if err != nil {
		return false, fmt.Errorf("looking up token: %w", err)
	}

	// Check if it has the root policy
	if secret != nil && secret.Data != nil {
		if policies, ok := secret.Data["policies"].([]interface{}); ok {
			for _, policy := range policies {
				if policyStr, ok := policy.(string); ok && policyStr == "root" {
					return true, nil
				}
			}
		}
	}

	return false, nil
}

// labelSelectorString converts a map of labels to a label selector string
func labelSelectorString(labels map[string]string) string {
	var parts []string
	for k, v := range labels {
		parts = append(parts, fmt.Sprintf("%s=%s", k, v))
	}
	return strings.Join(parts, ",")
}
