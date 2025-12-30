package secrets

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// ExpectedSecret represents a secret that should exist in the cluster
type ExpectedSecret struct {
	Name      string
	Namespace string
	Keys      []string
	Optional  bool
}

// VerificationResult represents the result of secret verification
type VerificationResult struct {
	Missing      []MissingSecret
	Incomplete   []IncompleteSecret
	AllPresent   bool
	RecoveryPlan []RecoveryAction
}

// MissingSecret represents a completely missing secret
type MissingSecret struct {
	Name      string
	Namespace string
	Optional  bool
}

// IncompleteSecret represents a secret missing some keys
type IncompleteSecret struct {
	Name        string
	Namespace   string
	MissingKeys []string
}

// RecoveryAction represents an action to recover a missing secret
type RecoveryAction struct {
	Type        string // "create", "update", "restore"
	SecretName  string
	Namespace   string
	Description string
}

// Verifier verifies that expected secrets exist
type Verifier struct {
	client.Client
	Log logr.Logger
}

// NewVerifier creates a new secret verifier
func NewVerifier(client client.Client, log logr.Logger) *Verifier {
	return &Verifier{
		Client: client,
		Log:    log,
	}
}

// VerifyExpectedSecrets verifies that all expected secrets exist
func (v *Verifier) VerifyExpectedSecrets(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) (*VerificationResult, error) {
	expected := v.getExpectedSecrets(vtu)
	result := &VerificationResult{
		AllPresent: true,
	}

	for _, exp := range expected {
		secret := &corev1.Secret{}
		err := v.Get(ctx, types.NamespacedName{
			Namespace: exp.Namespace,
			Name:      exp.Name,
		}, secret)

		if err != nil {
			if errors.IsNotFound(err) {
				v.Log.Info("Secret not found",
					"namespace", exp.Namespace,
					"name", exp.Name,
					"optional", exp.Optional)

				if !exp.Optional {
					result.AllPresent = false
					result.Missing = append(result.Missing, MissingSecret{
						Name:      exp.Name,
						Namespace: exp.Namespace,
						Optional:  exp.Optional,
					})

					// Add recovery action
					result.RecoveryPlan = append(result.RecoveryPlan, RecoveryAction{
						Type:        "create",
						SecretName:  exp.Name,
						Namespace:   exp.Namespace,
						Description: fmt.Sprintf("Create missing secret %s/%s", exp.Namespace, exp.Name),
					})
				}
			} else {
				return nil, fmt.Errorf("error checking secret %s/%s: %w", exp.Namespace, exp.Name, err)
			}
			continue
		}

		// Check if all expected keys exist
		missingKeys := []string{}
		for _, key := range exp.Keys {
			if _, exists := secret.Data[key]; !exists {
				missingKeys = append(missingKeys, key)
			}
		}

		// Special handling for admin token - check if it's a placeholder or incomplete
		if exp.Name == vtu.Spec.Initialization.SecretNames.AdminToken && !vtu.Spec.Initialization.SecretNames.SkipAdminTokenCreation {
			tokenData, hasToken := secret.Data["token"]
			if hasToken && !v.isUsableToken(secret, string(tokenData)) {
				v.Log.Info("Admin token is not usable (placeholder or incomplete)",
					"namespace", exp.Namespace,
					"name", exp.Name)

				// Treat as missing for recovery purposes
				result.AllPresent = false
				result.Missing = append(result.Missing, MissingSecret{
					Name:      exp.Name,
					Namespace: exp.Namespace,
					Optional:  false,
				})

				// Add recovery action
				result.RecoveryPlan = append(result.RecoveryPlan, RecoveryAction{
					Type:        "restore",
					SecretName:  exp.Name,
					Namespace:   exp.Namespace,
					Description: fmt.Sprintf("Restore admin token for %s/%s (current token is placeholder or incomplete)", exp.Namespace, exp.Name),
				})
				continue
			}
		}

		if len(missingKeys) > 0 {
			v.Log.Info("Secret missing keys",
				"namespace", exp.Namespace,
				"name", exp.Name,
				"missingKeys", missingKeys)

			result.AllPresent = false
			result.Incomplete = append(result.Incomplete, IncompleteSecret{
				Name:        exp.Name,
				Namespace:   exp.Namespace,
				MissingKeys: missingKeys,
			})

			// Add recovery action
			result.RecoveryPlan = append(result.RecoveryPlan, RecoveryAction{
				Type:        "update",
				SecretName:  exp.Name,
				Namespace:   exp.Namespace,
				Description: fmt.Sprintf("Update secret %s/%s to add missing keys: %v", exp.Namespace, exp.Name, missingKeys),
			})
		}
	}

	// Log verification summary
	if result.AllPresent {
		v.Log.V(1).Info("All expected secrets are present")
	} else {
		v.Log.Info("Secret verification found issues",
			"missingSecrets", len(result.Missing),
			"incompleteSecrets", len(result.Incomplete),
			"recoveryActions", len(result.RecoveryPlan))
	}

	return result, nil
}

// getExpectedSecrets returns the list of secrets that should exist for a VaultTransitUnseal
func (v *Verifier) getExpectedSecrets(vtu *vaultv1alpha1.VaultTransitUnseal) []ExpectedSecret {
	namespace := vtu.Spec.VaultPod.Namespace
	secrets := []ExpectedSecret{
		// Transit token is always required
		{
			Name:      vtu.Spec.TransitVault.SecretRef.Name,
			Namespace: namespace,
			Keys:      []string{vtu.Spec.TransitVault.SecretRef.Key},
			Optional:  false,
		},
	}

	// Admin token is only expected if not skipped
	if !vtu.Spec.Initialization.SecretNames.SkipAdminTokenCreation {
		secrets = append(secrets, ExpectedSecret{
			Name:      vtu.Spec.Initialization.SecretNames.AdminToken,
			Namespace: namespace,
			Keys:      []string{"token"},
			Optional:  false, // Required after initialization
		})
	}

	// Recovery keys are optional based on configuration
	if vtu.Spec.Initialization.SecretNames.StoreRecoveryKeys {
		secrets = append(secrets, ExpectedSecret{
			Name:      vtu.Spec.Initialization.SecretNames.RecoveryKeys,
			Namespace: namespace,
			Keys:      v.getRecoveryKeyNames(vtu.Spec.Initialization.RecoveryShares),
			Optional:  false,
		})
	}

	// Add any post-unseal configuration secrets
	if vtu.Spec.PostUnsealConfig.EnableExternalSecretsOperator {
		// External secrets operator might need specific secrets
		secrets = append(secrets, ExpectedSecret{
			Name:      "vault-external-secrets-token",
			Namespace: namespace,
			Keys:      []string{"token"},
			Optional:  true,
		})
	}

	return secrets
}

// getRecoveryKeyNames generates the expected recovery key names
func (v *Verifier) getRecoveryKeyNames(recoveryShares int) []string {
	keys := []string{"root-token"}
	for i := 0; i < recoveryShares; i++ {
		keys = append(keys, fmt.Sprintf("recovery-key-%d", i))
	}
	return keys
}

// LogMissingSecrets logs detailed information about missing secrets
func (v *Verifier) LogMissingSecrets(result *VerificationResult) {
	if result.AllPresent {
		return
	}

	v.Log.Info("=== Secret Verification Report ===")

	if len(result.Missing) > 0 {
		v.Log.Info("Missing secrets:")
		for _, missing := range result.Missing {
			status := "REQUIRED"
			if missing.Optional {
				status = "OPTIONAL"
			}
			v.Log.Info(fmt.Sprintf("  - %s/%s [%s]", missing.Namespace, missing.Name, status))
		}
	}

	if len(result.Incomplete) > 0 {
		v.Log.Info("Incomplete secrets:")
		for _, incomplete := range result.Incomplete {
			v.Log.Info(fmt.Sprintf("  - %s/%s missing keys: %v",
				incomplete.Namespace, incomplete.Name, incomplete.MissingKeys))
		}
	}

	if len(result.RecoveryPlan) > 0 {
		v.Log.Info("Recovery plan:")
		for _, action := range result.RecoveryPlan {
			v.Log.Info(fmt.Sprintf("  - [%s] %s", action.Type, action.Description))
		}
	}

	v.Log.Info("=================================")
}

// isUsableToken checks if a token is usable (not a placeholder or incomplete)
func (v *Verifier) isUsableToken(secret *corev1.Secret, token string) bool {
	// Check annotations
	if secret.Annotations != nil {
		if _, hasRecoveryRequired := secret.Annotations["vault.homelab.io/recovery-required"]; hasRecoveryRequired {
			return false
		}
		if _, hasIncomplete := secret.Annotations["vault.homelab.io/incomplete"]; hasIncomplete {
			return false
		}
		if rootToken, hasRootToken := secret.Annotations["vault.homelab.io/root-token"]; hasRootToken && rootToken == "true" {
			// Root tokens should be replaced with scoped tokens
			return false
		}
	}

	// Check for common placeholder patterns
	if strings.HasPrefix(token, "placeholder-") ||
		strings.HasPrefix(token, "temp-") ||
		strings.HasPrefix(token, "incomplete-") ||
		token == "" ||
		token == "null" ||
		token == "undefined" ||
		len(token) < 10 { // Vault tokens are typically much longer
		return false
	}

	return true
}
