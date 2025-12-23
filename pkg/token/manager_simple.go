package token

import (
	"context"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	vaultapi "github.com/hashicorp/vault/api"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// SimpleManager handles only initial token creation
type SimpleManager struct {
	client.Client
	Log    logr.Logger
	Scheme *runtime.Scheme
}

// NewSimpleManager creates a new simplified token manager
func NewSimpleManager(client client.Client, log logr.Logger, scheme *runtime.Scheme) *SimpleManager {
	return &SimpleManager{
		Client: client,
		Log:    log,
		Scheme: scheme,
	}
}

// ReconcileInitialToken creates the initial admin token if needed
func (m *SimpleManager) ReconcileInitialToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, vaultClient *vaultapi.Client) error {
	// Check if token management is enabled
	if !vtu.Spec.TokenManagement.Enabled {
		m.Log.Info("Token management is disabled")
		return nil
	}

	// For hybrid approach, we only handle initial creation
	if vtu.Spec.TokenManagement.Strategy == vaultv1alpha1.TokenStrategyExternal {
		m.Log.Info("Token management strategy is external, skipping")
		vtu.Status.TokenStatus.State = vaultv1alpha1.TokenStatePending
		return nil
	}

	// Check if initial token already exists
	exists, err := m.tokenExists(ctx, vtu)
	if err != nil {
		return err
	}

	if exists {
		// Token already exists - Kyverno will handle lifecycle
		m.Log.V(1).Info("Admin token already exists, lifecycle managed by Kyverno")
		return nil
	}

	// Check dependencies before creating
	if vtu.Spec.TokenManagement.Strategy == vaultv1alpha1.TokenStrategyDelayed {
		ready, err := m.checkDependencies(ctx, vtu)
		if err != nil {
			return err
		}

		if !ready {
			m.Log.Info("Dependencies not yet met, waiting")
			vtu.Status.TokenStatus.State = vaultv1alpha1.TokenStateWaiting
			return nil
		}

		// Apply creation delay if transitioning from waiting
		if vtu.Status.TokenStatus.State == vaultv1alpha1.TokenStateWaiting {
			delay, _ := time.ParseDuration(vtu.Spec.TokenManagement.CreationDelay)
			m.Log.Info("Dependencies met, applying creation delay", "delay", delay)
			time.Sleep(delay)
		}
	}

	// Create the initial token
	return m.createInitialToken(ctx, vtu, vaultClient)
}

// createInitialToken creates the admin token with lifecycle annotations
func (m *SimpleManager) createInitialToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, vaultClient *vaultapi.Client) error {
	m.Log.Info("Creating initial admin token")

	// Ensure policy exists
	if err := m.ensurePolicy(ctx, vtu, vaultClient); err != nil {
		return fmt.Errorf("failed to ensure policy: %w", err)
	}

	// Parse TTL
	ttl, err := time.ParseDuration(vtu.Spec.TokenManagement.TTL)
	if err != nil {
		return fmt.Errorf("invalid TTL: %w", err)
	}

	// Create token
	tokenReq := &vaultapi.TokenCreateRequest{
		Policies:  []string{vtu.Spec.TokenManagement.PolicyName},
		TTL:       ttl.String(),
		Renewable: &vtu.Spec.TokenManagement.AutoRenew,
		Metadata: map[string]string{
			"created_by": "vault-transit-unseal-operator",
			"purpose":    "admin-token",
			"vtu":        fmt.Sprintf("%s/%s", vtu.Namespace, vtu.Name),
		},
	}

	resp, err := vaultClient.Auth().Token().Create(tokenReq)
	if err != nil {
		vtu.Status.TokenStatus.State = vaultv1alpha1.TokenStateFailed
		vtu.Status.TokenStatus.Error = err.Error()
		return fmt.Errorf("failed to create token: %w", err)
	}

	// Create secret with lifecycle annotations for Kyverno
	now := time.Now()
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:        vtu.Spec.Initialization.SecretNames.AdminToken,
			Namespace:   vtu.Spec.VaultPod.Namespace,
			Annotations: m.buildLifecycleAnnotations(vtu, resp.Auth, now),
		},
		Type: corev1.SecretTypeOpaque,
		Data: map[string][]byte{
			"token": []byte(resp.Auth.ClientToken),
		},
	}

	// Set owner reference
	if err := controllerutil.SetControllerReference(vtu, secret, m.Scheme); err != nil {
		return fmt.Errorf("failed to set owner reference: %w", err)
	}

	// Create the secret
	if err := m.Create(ctx, secret); err != nil {
		return fmt.Errorf("failed to create secret: %w", err)
	}

	// Update status
	vtu.Status.TokenStatus = vaultv1alpha1.TokenStatus{
		State:         vaultv1alpha1.TokenStateActive,
		CreatedAt:     now.Format(time.RFC3339),
		LastRenewedAt: now.Format(time.RFC3339),
		Accessor:      resp.Auth.Accessor,
		Initialized:   true, // Mark as initialized for hybrid approach
	}

	if resp.Auth.LeaseDuration > 0 {
		expiresAt := now.Add(time.Duration(resp.Auth.LeaseDuration) * time.Second)
		vtu.Status.TokenStatus.ExpiresAt = expiresAt.Format(time.RFC3339)
	}

	m.Log.Info("Initial admin token created successfully",
		"accessor", resp.Auth.Accessor,
		"ttl", ttl,
		"kyverno_managed", true)

	return nil
}

// buildLifecycleAnnotations creates annotations for Kyverno lifecycle management
func (m *SimpleManager) buildLifecycleAnnotations(vtu *vaultv1alpha1.VaultTransitUnseal, resp *vaultapi.SecretAuth, now time.Time) map[string]string {
	annotations := make(map[string]string)

	// Copy existing annotations
	if vtu.Spec.Initialization.SecretNames.AdminTokenAnnotations != nil {
		for k, v := range vtu.Spec.Initialization.SecretNames.AdminTokenAnnotations {
			annotations[k] = v
		}
	}

	// Add lifecycle annotations for Kyverno
	annotations["vault.homelab.io/token-created"] = now.Format(time.RFC3339)
	annotations["vault.homelab.io/token-accessor"] = resp.Accessor
	annotations["vault.homelab.io/token-ttl"] = vtu.Spec.TokenManagement.TTL
	annotations["vault.homelab.io/token-policies"] = vtu.Spec.TokenManagement.PolicyName

	if vtu.Spec.TokenManagement.AutoRenew {
		annotations["vault.homelab.io/auto-renew"] = "true"
	}

	if vtu.Spec.TokenManagement.AutoRotate {
		annotations["vault.homelab.io/auto-rotate"] = "true"
		annotations["vault.homelab.io/rotation-period"] = vtu.Spec.TokenManagement.RotationPeriod

		// Calculate next rotation
		rotationPeriod, _ := time.ParseDuration(vtu.Spec.TokenManagement.RotationPeriod)
		nextRotation := now.Add(rotationPeriod)
		annotations["vault.homelab.io/next-rotation"] = nextRotation.Format(time.RFC3339)
	}

	if resp.LeaseDuration > 0 {
		expiresAt := now.Add(time.Duration(resp.LeaseDuration) * time.Second)
		annotations["vault.homelab.io/token-expires"] = expiresAt.Format(time.RFC3339)
	}

	// Mark as managed by hybrid approach
	annotations["vault.homelab.io/lifecycle-manager"] = "kyverno"
	annotations["vault.homelab.io/initial-creation"] = "vault-transit-unseal-operator"

	return annotations
}

// Simplified helper methods (reuse from original manager)

func (m *SimpleManager) tokenExists(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) (bool, error) {
	secret := &corev1.Secret{}
	err := m.Get(ctx, client.ObjectKey{
		Namespace: vtu.Spec.VaultPod.Namespace,
		Name:      vtu.Spec.Initialization.SecretNames.AdminToken,
	}, secret)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, nil
		}
		return false, err
	}

	// Check if it has actual token data
	if _, hasToken := secret.Data["token"]; hasToken {
		// Also check if it's marked as initialized (not a placeholder)
		if initialized, ok := secret.Annotations["vault.homelab.io/token-created"]; ok && initialized != "" {
			return true, nil
		}
	}

	return false, nil
}

func (m *SimpleManager) ensurePolicy(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, vaultClient *vaultapi.Client) error {
	policyName := vtu.Spec.TokenManagement.PolicyName

	// Default vault-admin policy
	policy := `
# Admin access to most paths
path "*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

# Deny root token creation for safety
path "auth/token/create-orphan" {
  capabilities = ["deny"]
}

path "auth/token/create/root" {
  capabilities = ["deny"]
}

# Allow managing own token
path "auth/token/self" {
  capabilities = ["read"]
}

path "auth/token/renew-self" {
  capabilities = ["update"]
}`

	if err := vaultClient.Sys().PutPolicy(policyName, policy); err != nil {
		return fmt.Errorf("failed to create policy %s: %w", policyName, err)
	}

	m.Log.Info("Ensured Vault policy exists", "policy", policyName)
	return nil
}

// checkDependencies is simplified - just check if deployments exist
func (m *SimpleManager) checkDependencies(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) (bool, error) {
	// For hybrid approach, we only need basic dependency checking
	// Kyverno will handle more complex dependency scenarios

	for _, dep := range vtu.Spec.TokenManagement.Dependencies.Deployments {
		// Just check if deployment exists (don't need complex ready checks)
		deployment := &appsv1.Deployment{}
		err := m.Get(ctx, client.ObjectKey{
			Namespace: dep.Namespace,
			Name:      dep.Name,
		}, deployment)

		if err != nil {
			if apierrors.IsNotFound(err) {
				m.Log.Info("Deployment dependency not found",
					"namespace", dep.Namespace,
					"name", dep.Name)
				return false, nil
			}
			return false, err
		}

		if deployment.Status.ReadyReplicas < dep.MinReadyReplicas {
			m.Log.Info("Deployment not ready",
				"namespace", dep.Namespace,
				"name", dep.Name,
				"ready", deployment.Status.ReadyReplicas,
				"required", dep.MinReadyReplicas)
			return false, nil
		}
	}

	return true, nil
}
