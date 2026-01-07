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
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/discovery"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

// VaultClientFactory creates properly configured Vault clients
type VaultClientFactory interface {
	NewClientForPod(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) (vault.Client, error)
}

// SimpleManager handles only initial token creation
type SimpleManager struct {
	client.Client
	Log              logr.Logger
	Scheme           *runtime.Scheme
	ServiceDiscovery *discovery.ServiceDiscovery
	VaultFactory     VaultClientFactory // Factory for creating properly configured Vault clients
}

// NewSimpleManager creates a new simplified token manager
func NewSimpleManager(client client.Client, log logr.Logger, scheme *runtime.Scheme, serviceDiscovery *discovery.ServiceDiscovery, vaultFactory VaultClientFactory) *SimpleManager {
	return &SimpleManager{
		Client:           client,
		Log:              log,
		Scheme:           scheme,
		ServiceDiscovery: serviceDiscovery,
		VaultFactory:     vaultFactory,
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
	exists, needsReplacement, err := m.checkTokenStatus(ctx, vtu)
	if err != nil {
		return err
	}

	if exists && !needsReplacement {
		// Token already exists and doesn't need replacement - Kyverno will handle lifecycle
		m.Log.V(1).Info("Admin token already exists, lifecycle managed by Kyverno")
		return nil
	}

	// If token needs replacement (root token stored during init), create scoped token
	if needsReplacement {
		m.Log.Info("Root token found, creating scoped replacement")
		return m.replaceRootToken(ctx, vtu, vaultClient)
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
	policyName := "vault-admin" // Default policy name
	if vtu.Spec.TokenManagement != nil && vtu.Spec.TokenManagement.PolicyName != "" {
		policyName = vtu.Spec.TokenManagement.PolicyName
	}

	// Default vault-admin policy
	policy := `
# Admin access to most paths (includes sudo for auth/mount management)
path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
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

// CreateScopedTokenFromRoot creates a scoped admin token from a root token and revokes the root token
func (m *SimpleManager) CreateScopedTokenFromRoot(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, rootToken string) (string, error) {
	m.Log.Info("Creating scoped admin token from root token")

	// Check if TokenManagement is configured and enabled
	if vtu.Spec.TokenManagement == nil || !vtu.Spec.TokenManagement.Enabled {
		m.Log.Info("TokenManagement not enabled, returning root token")
		return rootToken, nil
	}

	// Get the first Vault pod for creating the client
	pods, err := m.getVaultPods(ctx, &vtu.Spec.VaultPod)
	if err != nil {
		return "", fmt.Errorf("getting vault pods: %w", err)
	}
	if len(pods) == 0 {
		return "", fmt.Errorf("no vault pods found")
	}

	// Use the VaultClientFactory to create a properly configured client (with TLS settings)
	vaultClient, err := m.VaultFactory.NewClientForPod(ctx, &pods[0], vtu)
	if err != nil {
		return "", fmt.Errorf("creating vault client with factory: %w", err)
	}

	// Get the API client and set the root token
	apiClient := vaultClient.GetAPIClient()
	if apiClient == nil {
		return "", fmt.Errorf("vault client has no API client")
	}
	apiClient.SetToken(rootToken)

	// Ensure policy exists first
	if err := m.ensurePolicy(ctx, vtu, apiClient); err != nil {
		return "", fmt.Errorf("failed to ensure policy: %w", err)
	}

	// Create the scoped token with defaults if needed
	ttl := 24 * time.Hour // Default TTL
	if vtu.Spec.TokenManagement.TTL != "" {
		if parsedTTL, err := time.ParseDuration(vtu.Spec.TokenManagement.TTL); err == nil {
			ttl = parsedTTL
		}
	}

	policyName := "vault-admin" // Default policy
	if vtu.Spec.TokenManagement.PolicyName != "" {
		policyName = vtu.Spec.TokenManagement.PolicyName
	}

	renewable := true // Default to renewable
	tokenReq := &vaultapi.TokenCreateRequest{
		Policies:  []string{policyName},
		TTL:       ttl.String(),
		Renewable: &renewable,
		Metadata: map[string]string{
			"created_by": "vault-transit-unseal-operator",
			"purpose":    "admin-token-recovery",
			"vtu":        fmt.Sprintf("%s/%s", vtu.Namespace, vtu.Name),
			"source":     "root-token-recovery",
		},
	}

	resp, err := apiClient.Auth().Token().Create(tokenReq)
	if err != nil {
		return "", fmt.Errorf("failed to create scoped token: %w", err)
	}

	newToken := resp.Auth.ClientToken

	// Revoke the root token now that we have the scoped token
	if err := apiClient.Auth().Token().RevokeSelf(rootToken); err != nil {
		// Log the error but don't fail - we have the new token
		m.Log.Error(err, "Failed to revoke root token after creating scoped token")
	} else {
		m.Log.Info("Successfully revoked root token after creating scoped token")
	}

	m.Log.Info("Successfully created scoped admin token",
		"accessor", resp.Auth.Accessor,
		"ttl", ttl,
		"policy", policyName)

	return newToken, nil
}

// checkTokenStatus checks if the admin token exists and if it needs replacement
func (m *SimpleManager) checkTokenStatus(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) (exists bool, needsReplacement bool, err error) {
	secret := &corev1.Secret{}
	err = m.Get(ctx, client.ObjectKey{
		Namespace: vtu.Spec.VaultPod.Namespace,
		Name:      vtu.Spec.Initialization.SecretNames.AdminToken,
	}, secret)

	if err != nil {
		if apierrors.IsNotFound(err) {
			return false, false, nil
		}
		return false, false, err
	}

	// Token exists
	exists = true

	// Check if it needs replacement
	if secret.Annotations != nil {
		if rootToken, ok := secret.Annotations["vault.homelab.io/root-token"]; ok && rootToken == "true" {
			needsReplacement = true
		}
		if needsRepl, ok := secret.Annotations["vault.homelab.io/needs-replacement"]; ok && needsRepl == "true" {
			needsReplacement = true
		}
	}

	return exists, needsReplacement, nil
}

// replaceRootToken replaces a root token with a scoped admin token
func (m *SimpleManager) replaceRootToken(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, vaultClient *vaultapi.Client) error {
	// Get the existing root token
	secret := &corev1.Secret{}
	err := m.Get(ctx, client.ObjectKey{
		Namespace: vtu.Spec.VaultPod.Namespace,
		Name:      vtu.Spec.Initialization.SecretNames.AdminToken,
	}, secret)

	if err != nil {
		return fmt.Errorf("failed to get admin token secret: %w", err)
	}

	rootToken := string(secret.Data["token"])
	if rootToken == "" {
		return fmt.Errorf("admin token secret has empty token")
	}

	// Create scoped token from root
	scopedToken, err := m.CreateScopedTokenFromRoot(ctx, vtu, rootToken)
	if err != nil {
		return fmt.Errorf("failed to create scoped token: %w", err)
	}

	// Update the secret with the scoped token
	secret.Data["token"] = []byte(scopedToken)

	// Update annotations
	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	delete(secret.Annotations, "vault.homelab.io/root-token")
	delete(secret.Annotations, "vault.homelab.io/needs-replacement")
	secret.Annotations["vault.homelab.io/scoped-token"] = "true"
	secret.Annotations["vault.homelab.io/replaced-at"] = time.Now().Format(time.RFC3339)

	// Add lifecycle annotations for Kyverno
	secret.Annotations["vault.homelab.io/token-created"] = time.Now().Format(time.RFC3339)
	secret.Annotations["vault.homelab.io/token-ttl"] = vtu.Spec.TokenManagement.TTL
	secret.Annotations["vault.homelab.io/token-policies"] = vtu.Spec.TokenManagement.PolicyName

	if vtu.Spec.TokenManagement.AutoRenew {
		secret.Annotations["vault.homelab.io/auto-renew"] = "true"
	}

	if vtu.Spec.TokenManagement.AutoRotate {
		secret.Annotations["vault.homelab.io/auto-rotate"] = "true"
		secret.Annotations["vault.homelab.io/rotation-period"] = vtu.Spec.TokenManagement.RotationPeriod
	}

	// Update the secret
	if err := m.Update(ctx, secret); err != nil {
		return fmt.Errorf("failed to update admin token secret: %w", err)
	}

	m.Log.Info("Successfully replaced root token with scoped token")

	// Update status
	vtu.Status.TokenStatus.State = vaultv1alpha1.TokenStateActive
	vtu.Status.TokenStatus.LastRenewedAt = time.Now().Format(time.RFC3339)

	return nil
}
