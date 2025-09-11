# Vault Transit Unseal Operator

A Kubernetes operator that automatically manages HashiCorp Vault initialization and unsealing using transit unseal. This operator watches for uninitialized or sealed Vault instances in your cluster and handles the initialization and unsealing process automatically, storing the resulting tokens and keys securely in Kubernetes secrets.

> **⚠️ Breaking Change in v1.0.0**: The operator now uses command-line flags instead of environment variables for configuration. If you're upgrading from a previous version, please see the [migration guide](#migrating-from-v0x-to-v10) below.

## Features

- **Automatic Vault Initialization**: Detects uninitialized Vault pods and initializes them with transit unseal
- **Automatic Unsealing**: Monitors Vault pods and automatically unseals them when needed
- **Flexible Configuration**: Supports multiple ways to configure transit vault address (direct, ConfigMap, Secret)
- **YAML Path Extraction**: Extract nested values from structured YAML ConfigMaps using dot notation
- **Default Values**: Provides fallback values when ConfigMaps or Secrets are missing
- **Post-Unseal Configuration**: Automatically configures KV engine and External Secrets Operator access
- **Integration Support**: Works with Reflector (cross-namespace sync) and Reloader (pod restarts)
- **Security First**: Recovery keys are not stored by default (appear once in logs)

## Prerequisites

- Kubernetes 1.24+
- A transit Vault instance for unsealing (can be external)
- Vault pods deployed in your cluster
- Transit token with appropriate permissions

## Installation

### Step 1: Deploy the Operator

```bash
# Using kubectl with kustomize
kubectl apply -k github.com/fredericrous/vault-transit-unseal-operator/config/default

# Or using Helm (from source)
git clone https://github.com/fredericrous/vault-transit-unseal-operator.git
cd vault-transit-unseal-operator
helm install vault-transit-unseal-operator ./charts/vault-transit-unseal-operator \
  --namespace vault-transit-unseal-system \
  --create-namespace

# Or using Helm (when published to charts repository)
# Note: Chart publication pending
# helm repo add vault-transit-unseal https://fredericrous.github.io/charts
# helm install vault-transit-unseal-operator vault-transit-unseal/vault-transit-unseal-operator
```

### Step 2: Create Transit Token Secret

The operator needs a token to authenticate with your transit Vault:

```bash
kubectl create secret generic vault-transit-token \
  -n vault \
  --from-literal=token=<YOUR_TRANSIT_TOKEN>
```

### Step 3: Create VaultTransitUnseal Resource
   ```yaml
   apiVersion: vault.homelab.io/v1alpha1
   kind: VaultTransitUnseal
   metadata:
     name: vault-main
     namespace: vault
   spec:
     vaultPod:
       namespace: vault
       selector:
         app: vault
     transitVault:
       address: http://transit-vault:8200
       secretRef:
         name: vault-transit-token
     postUnsealConfig:
       enableKV: true
       enableExternalSecretsOperator: true  # Auto-configures ESO access
       externalSecretsOperatorConfig:
         policyName: "external-secrets-operator"
         kubernetesAuth:
           roleName: "external-secrets-operator"
           serviceAccounts:
           - name: external-secrets
             namespace: external-secrets
     initialization:
       secretNames:
         storeRecoveryKeys: false  # Set to true only for dev/testing
         adminTokenAnnotations:
           # Works with Reflector for cross-namespace secret sync
           reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
           reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "external-secrets,argocd"
           reflector.v1.k8s.emberstack.com/reflection-auto-enabled: "true"
           # Works with Reloader to restart pods when secrets change
           reloader.stakater.com/match: "true"
   ```

## Command-Line Flags

The operator supports the following command-line flags for configuration:

```bash
  -health-probe-bind-address string
        The address the probe endpoint binds to (default ":8081")
  -kubeconfig string
        Paths to a kubeconfig. Only required if out-of-cluster.
  -leader-elect
        Enable leader election for controller manager
  -leader-election-id string
        Leader election ID (default "vault-transit-unseal-operator")
  -max-concurrent-reconciles int
        Maximum number of concurrent reconciles (default 3)
  -metrics-bind-address string
        The address the metric endpoint binds to (default ":8080")
  -metrics-enabled
        Enable metrics endpoint (default true)
  -namespace string
        Namespace to watch for Vault pods (default "vault")
  -reconcile-timeout duration
        Timeout for each reconcile operation (default 5m0s)
  -skip-crd-install
        Skip CRD installation
  -vault-timeout duration
        Timeout for Vault API operations (default 30s)
  -vault-tls-validation
        Enable TLS certificate validation for Vault (default true)
  -zap-devel
        Enable development mode logging
  -zap-encoder string
        Zap log encoding (json or console) (default "json")
  -zap-log-level string
        Zap log level (debug, info, warn, error) (default "info")
  -zap-stacktrace-level string
        Zap level at which to log stack traces (default "error")
  -zap-time-encoding string
        Zap time encoding (default "iso8601")
```

### Using Flags with Helm

When deploying with Helm, you can configure these flags through values.yaml:

```yaml
# Namespace to watch for Vault pods (empty string means all namespaces)
watchNamespace: ""

# Operator features
skipCrdInstall: false
enableLeaderElection: false

# Operator configuration
maxConcurrentReconciles: 3
reconcileTimeout: 5m
vaultTimeout: 30s
vaultTlsValidation: true

# Additional args can be added to controllerManager.manager.args
controllerManager:
  manager:
    args:
    - --zap-log-level=debug
```

## Configuration Options

### Direct Address Configuration

The simplest way to configure the transit vault address is directly in the spec:

```yaml
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-main
  namespace: vault
spec:
  transitVault:
    address: "http://192.168.1.42:61200"  # Direct address
    secretRef:
      name: vault-transit-token
```

### ConfigMap Address Reference

For better configuration management, you can store the address in a ConfigMap:

```yaml
# ConfigMap with transit vault configuration
apiVersion: v1
kind: ConfigMap
metadata:
  name: vault-config
  namespace: vault
data:
  qnap.address: "http://192.168.1.42:61200"
---
# VaultTransitUnseal using the ConfigMap
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-main
  namespace: vault
spec:
  vaultPod:
    namespace: vault
    selector:
      app: vault
  transitVault:
    # Address loaded from ConfigMap
    addressFrom:
      configMapKeyRef:
        name: vault-config
        key: qnap.address
    secretRef:
      name: vault-transit-token
  # ... rest of spec
```

### Using Structured ConfigMap

You can use structured YAML in ConfigMaps and extract values using dot notation:

```yaml
# ConfigMap with structured configuration
apiVersion: v1
kind: ConfigMap
metadata:
  name: vault-config
  namespace: vault
data:
  config.yaml: |
    transit:
      address: "http://192.168.1.42:61200"
      mountPath: "transit"
      keyName: "autounseal"
---
# Extract nested values using dot notation
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-main
  namespace: vault
spec:
  transitVault:
    addressFrom:
      configMapKeyRef:
        name: vault-config
        key: config.yaml.transit.address  # Extract nested value
      default: "http://192.168.1.42:61200"  # Optional default
  # ... rest of spec
```

### Secret Address Reference

For sensitive configurations or when the address itself should be kept secret:

```yaml
# Secret with transit vault configuration
apiVersion: v1
kind: Secret
metadata:
  name: vault-secrets
  namespace: vault
type: Opaque
stringData:
  transit.address: "http://192.168.1.42:61200"
---
# VaultTransitUnseal using the Secret
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-main
  namespace: vault
spec:
  vaultPod:
    namespace: vault
    selector:
      app: vault
  transitVault:
    # Address loaded from Secret
    addressFrom:
      secretKeyRef:
        name: vault-secrets
        key: transit.address
    secretRef:
      name: vault-transit-token
  # ... rest of spec
```

## How It Works

### Initialization Process

1. **Pod Discovery**: The operator watches for Vault pods matching the configured selector
2. **Status Check**: For each pod, it checks if Vault is initialized
3. **Initialization**: If uninitialized, it initializes Vault with transit unseal configuration
4. **Secret Storage**: Admin tokens and (optionally) recovery keys are stored in Kubernetes secrets
5. **Post-Configuration**: After initialization, it configures KV engine and authentication methods

### Unsealing Process

1. **Health Monitoring**: The operator continuously monitors Vault pod health
2. **Seal Detection**: When a sealed Vault is detected, it attempts to unseal
3. **Automatic Recovery**: Uses the transit unseal mechanism to unseal without manual intervention
4. **Retry Logic**: Implements exponential backoff for transient failures

### Configuration Resolution

The operator resolves configuration in the following order:
1. Direct values in the spec
2. ConfigMap references (with YAML path extraction support)
3. Secret references
4. Default values if specified

## Advanced Features

### YAML Path Extraction

The operator supports extracting values from structured YAML in ConfigMaps using dot notation:

```yaml
# ConfigMap with structured YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: vault-config
data:
  config.yaml: |
    environments:
      production:
        transit:
          address: "https://vault.prod.example.com:8200"
          mountPath: "transit-prod"
      staging:
        transit:
          address: "https://vault.stage.example.com:8200"
          mountPath: "transit-stage"
---
# Extract specific environment configuration
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-production
spec:
  transitVault:
    addressFrom:
      configMapKeyRef:
        name: vault-config
        key: config.yaml.environments.production.transit.address
    mountPath: transit-prod
```

### Default Values

Provide fallback values to ensure resilience when ConfigMaps or Secrets are missing:

```yaml
transitVault:
  addressFrom:
    configMapKeyRef:
      name: vault-config
      key: transit.address
    default: "http://fallback-vault:8200"  # Used if ConfigMap is missing or key not found
```

### Post-Unseal Configuration

Automatically configure Vault after initialization:

```yaml
postUnsealConfig:
  enableKV: true
  kvConfig:
    path: "secret"      # Mount path for KV engine
    version: 2          # KV version (1 or 2)
  
  enableExternalSecretsOperator: true
  externalSecretsOperatorConfig:
    policyName: "external-secrets-operator"
    kubernetesAuth:
      roleName: "external-secrets-operator"
      serviceAccounts:
      - name: external-secrets
        namespace: external-secrets
      - name: external-secrets-webhook
        namespace: external-secrets
      ttl: "24h"
      maxTTL: "720h"
```

## Secrets Management

### Created Secrets

The operator creates these secrets in the same namespace as your Vault:

| Secret Name | Description | Default Behavior |
|------------|-------------|------------------|
| `vault-admin-token` | Root token for Vault admin access | Always created |
| `vault-keys` | Recovery keys for disaster recovery | Only created if `storeRecoveryKeys: true` |

### Security Considerations

**⚠️ Recovery Keys Storage**: By default, recovery keys are NOT stored in Kubernetes (`storeRecoveryKeys: false`). Instead, they appear once in the operator logs during initialization. This is a security best practice.

- **Production**: Keep `storeRecoveryKeys: false` and securely store keys from logs
- **Development**: Set `storeRecoveryKeys: true` for convenience
- **Key Rotation**: Regularly rotate your transit tokens and admin tokens

### Secret Annotations

Customize secret annotations for integration with other tools:

```yaml
initialization:
  secretNames:
    adminTokenAnnotations:
      # Reflector annotations for cross-namespace sync
      reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
      reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "app1,app2"
      
      # Reloader annotation for pod restarts on secret change
      reloader.stakater.com/match: "true"
      
      # Custom annotations
      my-company.com/owner: "platform-team"
```

## Troubleshooting

### Common Issues

#### Operator Not Finding Vault Pods

**Symptom**: Operator logs show no Vault pods found

**Solution**: Check your pod selector matches your Vault deployment labels:
```bash
# Check Vault pod labels
kubectl get pods -n vault --show-labels

# Verify selector in VaultTransitUnseal
kubectl get vaulttransitunseal -n vault -o yaml
```

#### Transit Authentication Failures

**Symptom**: "permission denied" errors in operator logs

**Solution**: Verify transit token permissions:
```bash
# Test token manually
export VAULT_ADDR=<transit-vault-address>
export VAULT_TOKEN=<your-transit-token>
vault write transit/keys/autounseal type=aes256-gcm96
```

#### ConfigMap Key Not Found

**Symptom**: "key not found in ConfigMap" errors

**Solution**: 
1. Check if using dot notation for nested YAML
2. Verify the ConfigMap exists and contains the expected structure
3. Consider adding a default value

### Debugging

Enable verbose logging:
```bash
# Edit operator deployment
kubectl edit deployment vault-transit-unseal-controller-manager -n vault-transit-unseal-system

# Add to container args:
# - --zap-log-level=2
```

View operator logs:
```bash
kubectl logs -n vault-transit-unseal-system deployment/vault-transit-unseal-controller-manager -f
```

## Best Practices

1. **Use Structured ConfigMaps**: Organize configuration in YAML format for better maintainability
2. **Set Default Values**: Always provide defaults for non-critical configuration
3. **Namespace Isolation**: Deploy VaultTransitUnseal in the same namespace as your Vault
4. **Monitor Logs**: Recovery keys appear only once in logs - have a process to capture them
5. **Regular Backups**: Backup your transit Vault regularly as it holds critical unseal keys
6. **Token Rotation**: Implement a process to rotate transit tokens periodically

## API Reference

For complete API documentation, see:
```bash
kubectl explain vaulttransitunseal.spec
kubectl explain vaulttransitunseal.spec.transitVault.addressFrom
```

## Helm Chart

A Helm chart is available in the `charts/` directory of this repository. The chart includes:

- CRD installation
- Operator deployment with configurable resources
- RBAC configuration
- Optional ServiceMonitor for Prometheus integration
- Optional PodDisruptionBudget and NetworkPolicy
- Configurable security contexts

### Installing from Source

```bash
git clone https://github.com/fredericrous/vault-transit-unseal-operator.git
cd vault-transit-unseal-operator
helm install vault-transit-unseal-operator ./charts/vault-transit-unseal-operator \
  --namespace vault-transit-unseal-system \
  --create-namespace
```

### Configuration

See [charts/vault-transit-unseal-operator/README.md](charts/vault-transit-unseal-operator/README.md) for all available configuration options.

### Publishing to Central Repository

The Helm chart is maintained in a separate repository. When releasing a new operator version:

1. Create a release in this repository
2. An automated workflow creates a PR in the charts repository
3. Review and merge the PR
4. Tag the chart release

#### Setting up Automated Chart Updates

To enable automatic chart updates when releasing new operator versions:

1. **Create a GitHub Personal Access Token**:
   - Go to GitHub Settings → Developer settings → Personal access tokens
   - For **Classic Token**: Select `repo` and `workflow` scopes
   - For **Fine-grained Token**: Grant write access to the charts repository
   
2. **Add Token to Repository Secrets**:
   - Go to this repository's Settings → Secrets and variables → Actions
   - Create a new secret named `CHARTS_REPO_TOKEN`
   - Paste your token as the value

3. **Release Process**:
   ```bash
   # Tag and push a new version
   git tag v0.2.0
   git push origin v0.2.0
   # Create a GitHub release → workflow triggers automatically
   ```

For detailed setup instructions, see [.github/workflows/HELM_CHART_RELEASE.md](.github/workflows/HELM_CHART_RELEASE.md).

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

## Complete Example

Here's a complete example showing all features working together:

```yaml
# ConfigMap with transit vault configuration
apiVersion: v1
kind: ConfigMap
metadata:
  name: vault-transit-config
  namespace: vault
data:
  config.yaml: |
    transit:
      primary:
        address: "https://transit-vault.example.com:8200"
        mountPath: "transit"
        keyName: "autounseal-prod"
      backup:
        address: "https://backup-vault.example.com:8200"
        mountPath: "transit"
        keyName: "autounseal-prod"
---
# Transit token secret
apiVersion: v1
kind: Secret
metadata:
  name: vault-transit-token
  namespace: vault
type: Opaque
stringData:
  token: "hvs.YOUR_TRANSIT_TOKEN_HERE"
---
# VaultTransitUnseal configuration
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-production
  namespace: vault
spec:
  # Target Vault pods
  vaultPod:
    namespace: vault
    selector:
      app.kubernetes.io/name: vault
      app.kubernetes.io/instance: vault
  
  # Transit vault configuration
  transitVault:
    # Use primary address from ConfigMap with backup fallback
    addressFrom:
      configMapKeyRef:
        name: vault-transit-config
        key: config.yaml.transit.primary.address
      default: "https://backup-vault.example.com:8200"
    
    # Token reference
    secretRef:
      name: vault-transit-token
      key: token
    
    # Transit engine configuration
    keyName: autounseal-prod
    mountPath: transit
    tlsSkipVerify: false
  
  # Initialization settings
  initialization:
    recoveryShares: 5
    recoveryThreshold: 3
    secretNames:
      adminToken: vault-root-token
      recoveryKeys: vault-recovery-keys
      storeRecoveryKeys: false  # Don't store in K8s for production
      adminTokenAnnotations:
        # Enable cross-namespace reflection
        reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
        reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "external-secrets,argocd,monitoring"
        # Enable automatic pod restarts
        reloader.stakater.com/match: "true"
  
  # Monitoring configuration
  monitoring:
    checkInterval: "30s"
    retryInterval: "10s"
    maxRetries: 5
  
  # Post-initialization configuration
  postUnsealConfig:
    enableKV: true
    kvConfig:
      path: "secret"
      version: 2
      description: "Main KV store for applications"
    
    enableExternalSecretsOperator: true
    externalSecretsOperatorConfig:
      policyName: "eso-policy"
      kubernetesAuth:
        roleName: "external-secrets"
        serviceAccounts:
        - name: external-secrets
          namespace: external-secrets
        - name: external-secrets-webhook
          namespace: external-secrets
        - name: external-secrets-cert-controller
          namespace: external-secrets
        ttl: "1h"
        maxTTL: "24h"
        policies:
        - "eso-policy"
        boundServiceAccountNamespaces:
        - "external-secrets"
```

## License

MIT


## Migrating from v0.x to v1.0

Version 1.0.0 introduces a breaking change: the operator now uses command-line flags instead of environment variables for configuration.

### What Changed

- **Environment variables removed**: All configuration via environment variables has been removed
- **Command-line flags added**: Configuration is now done via command-line flags
- **Helm chart updated**: The Helm chart has been updated to use the new flags

### Migration Steps

1. **Update your Helm values**:
   
   If you were using environment variables:
   ```yaml
   # Old (v0.x)
   extraEnvVars:
   - name: METRICS_ADDR
     value: ":9090"
   - name: NAMESPACE
     value: "my-vault"
   - name: ENABLE_LEADER_ELECTION
     value: "true"
   ```
   
   Change to:
   ```yaml
   # New (v1.0)
   watchNamespace: "my-vault"
   enableLeaderElection: true
   controllerManager:
     manager:
       args:
       - --metrics-bind-address=:9090
   ```

2. **Update any custom deployments**:
   
   If you're deploying without Helm:
   ```yaml
   # Old (v0.x)
   env:
   - name: NAMESPACE
     value: "my-vault"
   - name: ENABLE_METRICS
     value: "true"
   ```
   
   Change to:
   ```yaml
   # New (v1.0)
   args:
   - --namespace=my-vault
   - --metrics-enabled
   ```

3. **Review new configuration options**: Check the [Command-Line Flags](#command-line-flags) section for all available options.

### Environment Variables Still Supported

The following environment variables are still supported as they are not configuration parameters:
- `ARGOCD_MANAGED`: Set to "true" to indicate ArgoCD management
- `POD_NAMESPACE`: Injected via downward API for pod information
- `POD_NAME`: Injected via downward API for pod information
EOF < /dev/null