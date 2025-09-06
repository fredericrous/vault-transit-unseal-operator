# Vault Transit Unseal Operator

A Kubernetes operator that automatically manages Vault initialization and unsealing using HashiCorp Vault's transit unseal feature.

## Features

- Automatically detects uninitialized Vault pods
- Initializes Vault with transit unseal configuration
- Stores admin tokens and recovery keys in Kubernetes secrets
- Monitors Vault pod status continuously
- Handles multiple Vault instances

## Architecture

The operator watches for `VaultTransitUnseal` custom resources and manages the corresponding Vault pods:

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
      app.kubernetes.io/name: vault
  transitVault:
    address: http://192.168.1.42:61200
    secretRef:
      name: vault-transit-token
      key: token
```

## Building and Deployment

### Prerequisites

- Go 1.21+
- Docker
- kubectl with cluster access
- make

### Build the Operator

```bash
# Build locally
make build

# Build Docker image
make docker-build IMG=ghcr.io/fredericrous/vault-operator:latest

# Push to registry
make docker-push IMG=ghcr.io/fredericrous/vault-operator:latest
```

### Deploy to Kubernetes

1. Create the transit token secret:
```bash
kubectl create secret generic vault-transit-token \
  -n vault \
  --from-literal=token=<YOUR_TRANSIT_TOKEN>
```

2. Deploy the operator:
```bash
kubectl apply -k manifests/core/vault-operator/
```

3. Create a VaultTransitUnseal resource:
```bash
kubectl apply -f manifests/core/vault/vault-transit-unseal.yaml
```

## Development

### Run locally
```bash
# Install CRDs
make install

# Run operator locally
make run
```

### Run tests
```bash
make test
```

## How It Works

1. The operator watches for `VaultTransitUnseal` resources
2. For each resource, it finds matching Vault pods using the provided selector
3. It checks if Vault is initialized and unsealed
4. If not initialized, it initializes Vault with transit unseal configuration
5. It stores the admin token and recovery keys in Kubernetes secrets
6. It continuously monitors the Vault pod status

## Troubleshooting

Check operator logs:
```bash
kubectl logs -n vault-operator deployment/vault-operator
```

Check the status of VaultTransitUnseal resources:
```bash
kubectl get vaulttransitunseal -A
kubectl describe vaulttransitunseal vault-main -n vault
```

## Security Considerations

- The transit token is stored as a Kubernetes secret
- Admin tokens and recovery keys are stored as Kubernetes secrets
- Use RBAC to restrict access to these secrets
- Consider using sealed secrets or external secret operators for additional security