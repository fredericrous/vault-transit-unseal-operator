# Vault Transit Unseal Operator

**Never manually unseal Vault again!** This Kubernetes operator automatically initializes and unseals HashiCorp Vault instances using transit unseal, making Vault operations truly hands-free.

## Features

- 🚀 **Automatic Initialization & Unsealing** - No manual intervention required
- 🔧 **Flexible Configuration** - Direct values, ConfigMaps, Secrets with YAML path extraction
- 🔐 **Security First** - Recovery keys only in logs, never stored by default
- ⚡ **Post-Unseal Setup** - Auto-configures KV engine and External Secrets Operator
- 🔄 **Integration Ready** - Works with Reflector and Reloader out of the box
- 🔑 **Automatic Token Recovery** - Backs up and recovers admin tokens for disaster recovery (v1.6.0+)
- 🚨 **Self-Healing** - Automatically generates new tokens when missing using recovery keys

## Prerequisites

- Kubernetes 1.24+
- Transit Vault instance (can be external)
- Transit token with appropriate permissions
- Vault configured with transit seal (see [Vault Configuration](#vault-configuration))

## Vault Configuration

**Important:** This operator does NOT replace Vault's seal configuration. Your Vault instance must be configured to use transit unsealing in its configuration file.

Add the following to your Vault configuration (`vault.hcl`):

```hcl
# Transit auto-unseal configuration
seal "transit" {
  address         = "http://your-transit-vault:8200"  # Replace with your transit vault address
  disable_renewal = "false"
  key_name        = "autounseal"
  mount_path      = "transit"
  tls_skip_verify = "true"  # Only for development
  
  # The token can be injected via environment variable
  token = "your-transit-token"  # or use VAULT_SEAL_TRANSIT_TOKEN env var
}
```

The operator automates the unsealing process but requires Vault to be configured for transit unsealing. Without this configuration, Vault won't know how to decrypt its master key.

## Service Discovery

The operator automatically discovers Vault services in your cluster. You have two options:

### Automatic Service Discovery (Default)
The operator will find the appropriate service based on your pod selector:

```yaml
spec:
  vaultPod:
    namespace: vault
    selector:
      app.kubernetes.io/name: vault
    # Service is auto-discovered - no configuration needed
```

### Explicit Service Configuration
For precise control, specify the service name and port:

```yaml
spec:
  vaultPod:
    namespace: vault
    selector:
      app.kubernetes.io/name: vault
    serviceName: vault-http     # Your Vault service name
    servicePort: 8200           # Service port (not target port!)
```

**Important**: The `servicePort` is the port exposed by the Service (e.g., 8200), not the container's target port (e.g., 8300 for HTTP-in-mesh).

## Installation

### Using Helm (Recommended)

```bash
helm repo add fredericrous https://fredericrous.github.io/charts
helm repo update
helm install vault-transit-unseal-operator fredericrous/vault-transit-unseal-operator \
  --namespace vault-transit-unseal-system \
  --create-namespace
```

### Configure Transit Token

The operator needs a token to authenticate with your transit Vault:

```bash
kubectl create secret generic vault-transit-token \
  -n vault \
  --from-literal=token=<YOUR_TRANSIT_TOKEN>
```

### Deploy VaultTransitUnseal Resource

```yaml
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-main
  namespace: vault
spec:
  vaultPod:
    selector:
      app.kubernetes.io/name: vault
  transitVault:
    address: http://transit-vault:8200  # Transit vault address
    secretRef:
      name: vault-transit-token
  initialization:
    # Token recovery configuration (v1.6.0+)
    tokenRecovery:
      enabled: true         # Enable automatic recovery
      backupToTransit: true # Backup tokens to transit vault
      autoGenerate: true    # Generate new tokens if backup missing
  postUnsealConfig:
    enableKV: true
    enableExternalSecretsOperator: true
```

That's it! The operator will now automatically initialize and unseal your Vault pods.

### Complete Example

Here's how the Vault configuration and operator work together:

```yaml
# 1. Vault ConfigMap with transit seal configuration
apiVersion: v1
kind: ConfigMap
metadata:
  name: vault-config
  namespace: vault
data:
  vault.hcl: |
    seal "transit" {
      address = "http://transit-vault:8200"  # Transit vault endpoint
      key_name = "autounseal"
      mount_path = "transit"
      token = "TRANSIT_TOKEN_PLACEHOLDER"  # Will be replaced by init container
    }
    # ... rest of Vault config

---
# 2. Vault StatefulSet that uses the config
apiVersion: apps/v1
kind: StatefulSet
metadata:
  name: vault
  namespace: vault
spec:
  template:
    spec:
      initContainers:
      - name: config-templater
        # Replace token placeholder with actual secret
        command: ["sh", "-c", "sed -i 's/TRANSIT_TOKEN_PLACEHOLDER/'$TOKEN'/g' /vault/config/vault.hcl"]
        env:
        - name: TOKEN
          valueFrom:
            secretKeyRef:
              name: vault-transit-token
              key: token
      containers:
      - name: vault
        image: hashicorp/vault:1.20.1
        args: ["server", "-config=/vault/config/vault.hcl"]

---
# 3. VaultTransitUnseal CRD that manages unsealing
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-main
  namespace: vault
spec:
  vaultPod:
    selector:
      app.kubernetes.io/name: vault
  transitVault:
    address: http://transit-vault:8200  # Same as in vault.hcl
    secretRef:
      name: vault-transit-token  # Same secret as StatefulSet
```

The operator handles the unsealing lifecycle, but Vault must be configured to use transit sealing.

## Advanced Configuration

### Using ConfigMaps for Dynamic Configuration

Store transit vault addresses in ConfigMaps and even use nested YAML with dot notation:

```yaml
# ConfigMap with structured YAML
apiVersion: v1
kind: ConfigMap
metadata:
  name: vault-config
  namespace: vault
data:
  config.yaml: |
    environments:
      production:
        transit:
          address: "https://vault.prod.example.com:8200"  # Production vault endpoint
---
# Extract nested values
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
        key: config.yaml.environments.production.transit.address
      default: "http://fallback-vault:8200"  # Fallback transit vault address
    secretRef:
      name: vault-transit-token
```

### Production-Ready Example

```yaml
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-production
  namespace: vault
spec:
  vaultPod:
    selector:
      app.kubernetes.io/name: vault
  
  transitVault:
    addressFrom:
      configMapKeyRef:
        name: vault-config
        key: transit.address
      default: "https://backup-vault:8200"  # Backup transit vault address
    secretRef:
      name: vault-transit-token
    keyName: autounseal-prod
    mountPath: transit
  
  initialization:
    recoveryShares: 5
    recoveryThreshold: 3
    # Token recovery configuration (v1.6.0+)
    tokenRecovery:
      enabled: true         # Enable automatic recovery
      backupToTransit: true # Backup tokens to transit vault
      autoGenerate: true    # Generate new tokens if backup missing
    secretNames:
      storeRecoveryKeys: false  # Production: keys only in logs
      adminTokenAnnotations:
        # Cross-namespace secret sync
        reflector.v1.k8s.emberstack.com/reflection-allowed: "true"
        reflector.v1.k8s.emberstack.com/reflection-allowed-namespaces: "external-secrets,argocd"
        # Automatic pod restarts on secret change
        reloader.stakater.com/match: "true"
  
  postUnsealConfig:
    enableKV: true
    enableExternalSecretsOperator: true
    externalSecretsOperatorConfig:
      kubernetesAuth:
        roleName: "external-secrets"
        serviceAccounts:
        - name: external-secrets
          namespace: external-secrets
```

## How It Works

1. **Watches** for Vault pods in your cluster
2. **Detects** uninitialized or sealed instances
3. **Initializes** Vault with transit unseal configuration
4. **Stores** admin tokens in Kubernetes secrets
5. **Configures** KV engine and authentication post-initialization
6. **Monitors** continuously and unseals automatically when needed

## Security Best Practices

- **Recovery Keys**: Not stored by default - appear once in operator logs during initialization
- **Production**: Keep `storeRecoveryKeys: false` and capture keys from logs
- **Development**: Set `storeRecoveryKeys: true` for convenience
- **Token Rotation**: Implement regular transit token rotation

### Created Secrets

- `vault-admin-token`: Root token for Vault admin access
- `vault-keys`: Recovery keys (only if `storeRecoveryKeys: true`)

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

View operator logs:
```bash
kubectl logs -n vault-transit-unseal-system deployment/vault-transit-unseal-controller-manager -f
```

Enable debug logging via Helm:
```yaml
controllerManager:
  manager:
    args:
    - --zap-log-level=debug
```

## API Reference

For detailed configuration options:
```bash
kubectl explain vaulttransitunseal.spec
```

## Release Process

New releases automatically update the Helm chart:

```bash
# Tag and push a new version
git tag v0.2.0
git push origin v0.2.0
# Create a GitHub release → chart updates automatically
```

## Contributing

Contributions are welcome! Please feel free to submit a Pull Request.

### Development

#### Running Tests

To run all tests including controller integration tests:
```bash
make test
```

This will automatically download required test binaries (etcd, kube-apiserver) on first run.

For unit tests only:
```bash
make test-unit
```

#### Installation from Source

To install from source:
```bash
git clone https://github.com/fredericrous/vault-transit-unseal-operator.git
cd vault-transit-unseal-operator
helm install vault-transit-unseal-operator ./charts/vault-transit-unseal-operator \
  --namespace vault-transit-unseal-system \
  --create-namespace
```

For complete command-line flags documentation, see `vault-transit-unseal-operator --help`.


## License

MIT