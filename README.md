# Vault Transit Unseal Operator

Automatically manages HashiCorp Vault initialization and unsealing using transit unseal in Kubernetes.

## Quick Start

1. **Create transit token secret**
   ```bash
   kubectl create secret generic vault-transit-token \
     -n vault \
     --from-literal=token=<YOUR_TRANSIT_TOKEN>
   ```

2. **Deploy the operator**
   ```bash
   kubectl apply -k github.com/fredericrous/vault-transit-unseal-operator/config/default
   ```

3. **Configure your Vault**
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

## What It Does

- Watches for uninitialized Vault pods
- Initializes them with transit unseal 
- Stores admin tokens in Kubernetes secrets with custom annotations
- Auto-configures External Secrets Operator access (policies, auth, roles)
- Sets up KV v2 engine at `/secret`
- Supports Reflector (cross-namespace sync) and Reloader (pod restarts)

## Secrets Created

The operator creates these secrets in the same namespace as your Vault:

- `vault-admin-token`: Contains the root token for Vault admin access
- `vault-keys`: Contains the recovery keys (only if `storeRecoveryKeys: true`)

**Security Note**: By default, recovery keys are NOT stored in Kubernetes (`storeRecoveryKeys: false`). Instead, they appear once in the operator logs during initialization - capture and store them securely elsewhere. Only set to `true` for dev/test environments.

## Requirements

- Kubernetes 1.24+
- A transit Vault instance for unsealing
- Vault pods deployed in your cluster

## License

MIT
