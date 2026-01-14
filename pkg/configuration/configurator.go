package configuration

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/go-logr/logr"
	vaultapi "github.com/hashicorp/vault/api"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// readFile reads the contents of a file (used for reading service account tokens and CA certs)
var readFile = os.ReadFile

// Configurator handles post-unseal Vault configuration
type Configurator struct {
	log logr.Logger
}

// NewConfigurator creates a new configurator
func NewConfigurator(log logr.Logger) *Configurator {
	return &Configurator{
		log: log.WithName("configurator"),
	}
}

// Configure applies post-unseal configuration
func (c *Configurator) Configure(ctx context.Context, vaultClient *vaultapi.Client, spec vaultv1alpha1.PostUnsealConfig, status *vaultv1alpha1.ConfigurationStatus) error {
	// Configure KV if enabled and not already configured
	if spec.EnableKV && !status.KVConfigured {
		c.log.Info("Configuring KV engine")
		if err := c.configureKV(ctx, vaultClient, spec.KVConfig); err != nil {
			return fmt.Errorf("failed to configure KV: %w", err)
		}
		status.KVConfigured = true
		status.KVConfiguredTime = time.Now().UTC().Format(time.RFC3339)
		c.log.Info("KV engine configured successfully")
	}

	// Configure ESO if enabled and not already configured
	if spec.EnableExternalSecretsOperator && !status.ExternalSecretsOperatorConfigured {
		c.log.Info("Configuring External Secrets Operator")
		if err := c.configureExternalSecretsOperator(ctx, vaultClient, spec.ExternalSecretsOperatorConfig); err != nil {
			return fmt.Errorf("failed to configure External Secrets Operator: %w", err)
		}
		status.ExternalSecretsOperatorConfigured = true
		status.ExternalSecretsOperatorConfiguredTime = time.Now().UTC().Format(time.RFC3339)
		c.log.Info("External Secrets Operator configured successfully")
	}

	// Bootstrap vault-config-operator if enabled (separate from ESO config)
	// This is the minimal bootstrap needed when vault-config-operator manages
	// all Vault configuration via GitOps CRDs
	if spec.EnableVaultConfigOperatorBootstrap && !status.VaultConfigOperatorBootstrapped {
		c.log.Info("Bootstrapping vault-config-operator (minimal kubernetes auth)")
		if err := c.configureVaultConfigOperatorBootstrap(ctx, vaultClient); err != nil {
			return fmt.Errorf("failed to bootstrap vault-config-operator: %w", err)
		}
		status.VaultConfigOperatorBootstrapped = true
		status.VaultConfigOperatorBootstrappedTime = time.Now().UTC().Format(time.RFC3339)
		c.log.Info("vault-config-operator bootstrap completed")
	}

	return nil
}

// configureKV configures the KV v2 secret engine
func (c *Configurator) configureKV(ctx context.Context, client *vaultapi.Client, config vaultv1alpha1.KVConfig) error {
	// Set defaults
	if config.Path == "" {
		config.Path = "secret"
	}
	if config.Version == 0 {
		config.Version = 2
	}

	// Check if already enabled
	mounts, err := client.Sys().ListMounts()
	if err != nil {
		return fmt.Errorf("failed to list mounts: %w", err)
	}

	mountPath := config.Path + "/"
	if mount, exists := mounts[mountPath]; exists {
		// Check if it's KV v2
		if mount.Type == "kv" && mount.Options["version"] == "2" {
			c.log.Info("KV v2 engine already enabled", "path", config.Path)
			return nil
		}
		return fmt.Errorf("mount point %s already exists with different type or version", config.Path)
	}

	// Enable KV v2
	mountInput := &vaultapi.MountInput{
		Type: "kv",
		Options: map[string]string{
			"version": "2",
		},
	}

	if err := client.Sys().Mount(config.Path, mountInput); err != nil {
		return fmt.Errorf("failed to mount KV engine: %w", err)
	}

	// Configure KV metadata if specified
	if config.MaxVersions > 0 || config.DeleteVersionAfter != "" {
		metadataPath := fmt.Sprintf("/%s/config", config.Path)
		configData := map[string]interface{}{}

		if config.MaxVersions > 0 {
			configData["max_versions"] = config.MaxVersions
		}
		if config.DeleteVersionAfter != "" {
			configData["delete_version_after"] = config.DeleteVersionAfter
		}

		if _, err := client.Logical().Write(metadataPath, configData); err != nil {
			c.log.Error(err, "Failed to configure KV metadata, continuing anyway", "path", metadataPath)
		}
	}

	c.log.Info("KV v2 engine enabled", "path", config.Path, "version", config.Version)
	return nil
}

// configureExternalSecretsOperator configures Vault for ESO
func (c *Configurator) configureExternalSecretsOperator(ctx context.Context, client *vaultapi.Client, config vaultv1alpha1.ExternalSecretsOperatorConfig) error {
	// Set defaults
	if config.PolicyName == "" {
		config.PolicyName = "external-secrets-operator"
	}

	// Create policy
	policyHCL := `
# Read all secrets
path "secret/data/*" {
  capabilities = ["read", "list"]
}

path "secret/metadata/*" {
  capabilities = ["read", "list"]
}

# Auth token lookup
path "auth/token/lookup-self" {
  capabilities = ["read"]
}
`

	if err := client.Sys().PutPolicy(config.PolicyName, policyHCL); err != nil {
		return fmt.Errorf("failed to create policy: %w", err)
	}
	c.log.Info("Created policy", "name", config.PolicyName)

	// Check if Kubernetes auth is already enabled
	auths, err := client.Sys().ListAuth()
	if err != nil {
		return fmt.Errorf("failed to list auth methods: %w", err)
	}

	if _, exists := auths["kubernetes/"]; !exists {
		// Enable Kubernetes auth
		if err := client.Sys().EnableAuthWithOptions("kubernetes", &vaultapi.EnableAuthOptions{
			Type: "kubernetes",
		}); err != nil {
			return fmt.Errorf("failed to enable Kubernetes auth: %w", err)
		}
		c.log.Info("Enabled Kubernetes auth method")
	} else {
		c.log.Info("Kubernetes auth method already enabled")
	}

	// Configure Kubernetes auth
	k8sConfig := map[string]interface{}{
		"kubernetes_host": "https://kubernetes.default.svc:443",
	}

	if _, err := client.Logical().Write("auth/kubernetes/config", k8sConfig); err != nil {
		return fmt.Errorf("failed to configure Kubernetes auth: %w", err)
	}

	// Configure role
	if config.KubernetesAuth.RoleName == "" {
		config.KubernetesAuth.RoleName = "external-secrets-operator"
	}

	// Set default service accounts if not specified
	if len(config.KubernetesAuth.ServiceAccounts) == 0 {
		config.KubernetesAuth.ServiceAccounts = []vaultv1alpha1.ServiceAccountRef{
			{Name: "external-secrets", Namespace: "external-secrets"},
			{Name: "external-secrets-operator", Namespace: "external-secrets"},
		}
	}

	// Build bound service account names and namespaces
	var saNames, saNamespaces []string
	for _, sa := range config.KubernetesAuth.ServiceAccounts {
		saNames = append(saNames, sa.Name)
		saNamespaces = append(saNamespaces, sa.Namespace)
	}

	roleData := map[string]interface{}{
		"bound_service_account_names":      saNames,
		"bound_service_account_namespaces": saNamespaces,
		"policies":                         []string{config.PolicyName},
		"ttl":                              config.KubernetesAuth.TTL,
		"max_ttl":                          config.KubernetesAuth.MaxTTL,
	}

	rolePath := fmt.Sprintf("auth/kubernetes/role/%s", config.KubernetesAuth.RoleName)
	if _, err := client.Logical().Write(rolePath, roleData); err != nil {
		return fmt.Errorf("failed to create Kubernetes auth role: %w", err)
	}

	c.log.Info("Configured Kubernetes auth role",
		"role", config.KubernetesAuth.RoleName,
		"serviceAccounts", saNames,
		"namespaces", saNamespaces)

	return nil
}

// configureVaultConfigOperatorBootstrap creates the minimal kubernetes auth setup
// for vault-config-operator to authenticate and manage Vault resources.
// This solves the chicken-and-egg problem where vault-config-operator
// needs kubernetes auth before it can configure its own access via GitOps CRDs.
func (c *Configurator) configureVaultConfigOperatorBootstrap(ctx context.Context, client *vaultapi.Client) error {
	const (
		policyName  = "vault-admin" // Match GitOps policy name
		roleName    = "controller-manager"
		saName      = "controller-manager"
		saNamespace = "vault-config-operator"
	)

	// Check if Kubernetes auth is already enabled
	auths, err := client.Sys().ListAuth()
	if err != nil {
		return fmt.Errorf("failed to list auth methods: %w", err)
	}

	if _, exists := auths["kubernetes/"]; !exists {
		// Enable Kubernetes auth
		if err := client.Sys().EnableAuthWithOptions("kubernetes", &vaultapi.EnableAuthOptions{
			Type: "kubernetes",
		}); err != nil {
			return fmt.Errorf("failed to enable Kubernetes auth: %w", err)
		}
		c.log.Info("Enabled Kubernetes auth method")
	} else {
		c.log.Info("Kubernetes auth method already enabled")
	}

	// Read token_reviewer_jwt and kubernetes_ca_cert from the operator's service account
	// These are mounted at standard Kubernetes paths
	tokenReviewerJWT, err := c.readServiceAccountToken()
	if err != nil {
		return fmt.Errorf("reading service account token: %w", err)
	}

	kubernetesCACert, err := c.readKubernetesCACert()
	if err != nil {
		return fmt.Errorf("reading Kubernetes CA cert: %w", err)
	}

	// Configure Kubernetes auth with proper reviewer token and CA
	// This mirrors kubernetes/homelab/security/configs/vault-config-operator/kubernetes-auth-config.yaml
	// but supplies the reviewer + CA needed for the first login
	k8sConfig := map[string]interface{}{
		"kubernetes_host":        "https://kubernetes.default.svc.cluster.local:443",
		"disable_iss_validation": true,
		"disable_local_ca_jwt":   true,
	}

	k8sConfig["token_reviewer_jwt"] = tokenReviewerJWT
	k8sConfig["kubernetes_ca_cert"] = kubernetesCACert

	if _, err := client.Logical().Write("auth/kubernetes/config", k8sConfig); err != nil {
		return fmt.Errorf("failed to configure Kubernetes auth: %w", err)
	}
	c.log.Info("Configured Kubernetes auth with reviewer JWT and CA cert")

	// Create admin policy for vault-config-operator
	// Uses vault-admin to match the GitOps policy that will be created later
	adminPolicyHCL := `
# Allow vault-config-operator to manage all Vault resources
path "*" {
  capabilities = ["create", "read", "update", "delete", "list", "sudo"]
}
`

	if err := client.Sys().PutPolicy(policyName, adminPolicyHCL); err != nil {
		return fmt.Errorf("failed to create vault-admin policy: %w", err)
	}
	c.log.Info("Created vault-admin policy", "name", policyName)

	// Create role for vault-config-operator
	roleData := map[string]interface{}{
		"bound_service_account_names":      []string{saName},
		"bound_service_account_namespaces": []string{saNamespace},
		"policies":                         []string{policyName},
		"ttl":                              "1h",
		"max_ttl":                          "24h",
	}

	rolePath := fmt.Sprintf("auth/kubernetes/role/%s", roleName)
	if _, err := client.Logical().Write(rolePath, roleData); err != nil {
		return fmt.Errorf("failed to create controller-manager role: %w", err)
	}

	c.log.Info("Created vault-config-operator bootstrap role",
		"role", roleName,
		"serviceAccount", saName,
		"namespace", saNamespace,
		"policy", policyName)

	return nil
}

// readServiceAccountToken reads the JWT token from the operator's service account
func (c *Configurator) readServiceAccountToken() (string, error) {
	const tokenPath = "/var/run/secrets/kubernetes.io/serviceaccount/token"
	data, err := readFile(tokenPath)
	if err != nil {
		return "", fmt.Errorf("reading service account token: %w", err)
	}
	return string(data), nil
}

// readKubernetesCACert reads the CA certificate from the operator's service account
func (c *Configurator) readKubernetesCACert() (string, error) {
	const caPath = "/var/run/secrets/kubernetes.io/serviceaccount/ca.crt"
	data, err := readFile(caPath)
	if err != nil {
		return "", fmt.Errorf("reading Kubernetes CA cert: %w", err)
	}
	return string(data), nil
}
