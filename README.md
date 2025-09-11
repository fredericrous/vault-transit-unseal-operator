# Vault Transit Unseal Operator

**Never manually unseal Vault again!** This Kubernetes operator automatically initializes and unseals HashiCorp Vault instances using transit unseal, making Vault operations truly hands-free.

## Features

- 🚀 **Automatic Initialization & Unsealing** - No manual intervention required
- 🔧 **Flexible Configuration** - Direct values, ConfigMaps, Secrets with YAML path extraction
- 🔐 **Security First** - Recovery keys only in logs, never stored by default
- ⚡ **Post-Unseal Setup** - Auto-configures KV engine and External Secrets Operator
- 🔄 **Integration Ready** - Works with Reflector and Reloader out of the box

## Prerequisites

- Kubernetes 1.24+
- Transit Vault instance (can be external)
- Transit token with appropriate permissions

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
      app: vault
  transitVault:
    address: http://transit-vault:8200
    secretRef:
      name: vault-transit-token
  postUnsealConfig:
    enableKV: true
    enableExternalSecretsOperator: true
```

That's it! The operator will now automatically initialize and unseal your Vault pods.

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
          address: "https://vault.prod.example.com:8200"
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
      default: "http://fallback-vault:8200"  # Fallback value
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
      default: "https://backup-vault:8200"
    secretRef:
      name: vault-transit-token
    keyName: autounseal-prod
    mountPath: transit
  
  initialization:
    recoveryShares: 5
    recoveryThreshold: 3
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