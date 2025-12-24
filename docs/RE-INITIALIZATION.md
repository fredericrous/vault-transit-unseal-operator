# Vault Re-initialization Support

## Overview

The vault-transit-unseal-operator now supports re-initialization of Vault instances when the admin token is lost but Vault is already initialized. This feature helps recover from scenarios where:

- The admin token secret was deleted
- The bootstrap process was interrupted
- Manual intervention corrupted the admin token

## How It Works

When `forceReinitialize: true` is set in the VaultTransitUnseal spec:

1. The operator checks if Vault is already initialized
2. If initialized and unsealed, it creates a placeholder admin token
3. The placeholder includes instructions for manual root token generation
4. The `forceReinitialize` flag is automatically cleared after execution

## Usage

### Enable Re-initialization

Add or update the `forceReinitialize` field in your VaultTransitUnseal resource:

```yaml
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-transit-unseal
  namespace: vault
spec:
  initialization:
    forceReinitialize: true  # Trigger re-initialization
    # ... rest of initialization config
```

### Apply the Changes

```bash
kubectl apply -f your-vault-transit-unseal.yaml
```

### Manual Steps After Re-initialization

1. Get the placeholder token:
   ```bash
   kubectl get secret vault-admin-token -n vault -o jsonpath='{.data.token}' | base64 -d
   ```

2. If recovery keys are stored (not recommended for production):
   ```bash
   kubectl get secret vault-recovery-keys -n vault -o yaml
   ```

3. Generate a new root token using recovery keys:
   ```bash
   kubectl exec -it vault-0 -n vault -- vault operator generate-root \
     -init \
     -recovery-key-shares=5 \
     -recovery-key-threshold=3
   ```

4. Follow the prompts to enter recovery keys and generate the new root token

5. Update the admin token secret with the real token:
   ```bash
   kubectl create secret generic vault-admin-token \
     --from-literal=token=<new-root-token> \
     --namespace vault \
     --dry-run=client -o yaml | kubectl apply -f -
   ```

## Security Considerations

- The re-initialization feature creates placeholder tokens as a safety mechanism
- Real root token generation requires manual intervention with recovery keys
- Recovery keys should be stored securely outside of Kubernetes in production
- The `forceReinitialize` flag is automatically cleared after use to prevent accidental re-triggers

## Troubleshooting

### Issue: Placeholder token created but no recovery keys available

**Solution**: Recovery keys must be provided manually. If lost, Vault must be completely re-initialized.

### Issue: Re-initialization not triggering

**Symptoms**: The `forceReinitialize` flag is set but nothing happens

**Check**:
1. Vault must be initialized (`vault status` shows `Initialized: true`)
2. Vault must be unsealed
3. Check operator logs: `kubectl logs -n vault-transit-unseal-operator <operator-pod>`

### Issue: Flag keeps getting reset

**Cause**: The operator automatically clears the flag after processing

**Solution**: This is expected behavior to prevent loops. Set the flag again only if needed.