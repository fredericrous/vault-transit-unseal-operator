# Vault Re-initialization and Token Recovery Support

## Overview

The vault-transit-unseal-operator now supports automatic token recovery and re-initialization of Vault instances when the admin token is lost but Vault is already initialized. This feature helps recover from scenarios where:

- The admin token secret was deleted
- The bootstrap process was interrupted
- Manual intervention corrupted the admin token
- Disaster recovery of a cluster with existing Vault data

## Automatic Token Recovery (v1.6.0+)

The operator now includes automatic token recovery capabilities that can:

1. **Backup admin tokens** to the transit Vault's KV store during initialization
2. **Automatically recover** admin tokens from transit Vault backup
3. **Generate new root tokens** using recovery keys (when available)
4. **Create scoped admin tokens** instead of persisting root tokens

### Configuration

Add the `tokenRecovery` section to your VaultTransitUnseal resource:

```yaml
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-transit-unseal
  namespace: vault
spec:
  initialization:
    tokenRecovery:
      enabled: true                    # Enable automatic recovery
      backupToTransit: true           # Backup tokens to transit vault
      transitKVPath: ""               # Optional: custom KV path (default: vault-transit-unseal/<namespace>/<name>/admin-token)
      autoGenerate: true              # Auto-generate token if backup not found
```

### How Automatic Recovery Works

When the admin token is missing:

1. **Check Transit Backup**: If `backupToTransit` is enabled, the operator attempts to recover the token from the transit Vault's KV store
2. **Generate New Token**: If no backup is found and `autoGenerate` is enabled, the operator will:
   - Use recovery keys to generate a new root token
   - Create a scoped admin token with appropriate policies
   - Revoke the temporary root token
   - Backup the new admin token to transit Vault
3. **Fallback**: If automatic recovery fails, a placeholder is created with manual recovery instructions

### Prerequisites for Transit Vault Backup

The transit Vault must have:
- KV v2 secrets engine enabled at `secret/` mount
- The transit token must have permissions to read/write at the backup path

## Manual Re-initialization

When automatic recovery is not possible or `forceReinitialize: true` is set:

1. The operator checks if Vault is already initialized
2. If initialized and unsealed, it attempts automatic recovery first
3. If recovery fails, it creates a placeholder admin token
4. The placeholder includes instructions for manual root token generation
5. The `forceReinitialize` flag is automatically cleared after execution

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

### Token Recovery
- Admin tokens are backed up to the transit Vault's KV store for disaster recovery
- Only scoped admin tokens are persisted - root tokens are created transiently and immediately revoked
- The backup path in transit Vault should be properly secured with ACL policies
- Consider encrypting tokens before backup for additional security

### General Security
- The re-initialization feature creates placeholder tokens as a safety mechanism
- Real root token generation requires manual intervention with recovery keys
- Recovery keys should be stored securely outside of Kubernetes in production
- The `forceReinitialize` flag is automatically cleared after use to prevent accidental re-triggers
- Transit Vault tokens need appropriate permissions for KV operations

## Troubleshooting

### Token Recovery Issues

#### Issue: Token recovery from transit vault fails

**Symptoms**: Logs show "Failed to recover token from transit vault"

**Check**:
1. Transit vault is accessible and unsealed
2. Transit token has read permissions at the backup path
3. KV v2 engine is enabled at `secret/` mount on transit vault
4. Check operator logs for specific error: `kubectl logs -n vault-transit-unseal-operator <operator-pod>`

#### Issue: Automatic token generation fails

**Symptoms**: "Failed to generate new root token" in logs

**Check**:
1. Recovery keys are available in the configured secret
2. Sufficient recovery keys are present (meets threshold)
3. Vault is initialized and unsealed
4. Recovery keys are valid for the current Vault instance

#### Issue: Token backup to transit fails during initialization

**Symptoms**: Warning event "TokenBackupFailed"

**Check**:
1. Transit vault token has write permissions at the backup path
2. Transit vault KV v2 engine is properly configured
3. Network connectivity between Vaults

**Solution**: The initialization will continue, but manual backup may be needed

### General Issues

#### Issue: Placeholder token created but no recovery keys available

**Solution**: Recovery keys must be provided manually. If lost, Vault must be completely re-initialized.

#### Issue: Re-initialization not triggering

**Symptoms**: The `forceReinitialize` flag is set but nothing happens

**Check**:
1. Vault must be initialized (`vault status` shows `Initialized: true`)
2. Vault must be unsealed
3. Check operator logs: `kubectl logs -n vault-transit-unseal-operator <operator-pod>`

#### Issue: Flag keeps getting reset

**Cause**: The operator automatically clears the flag after processing

**Solution**: This is expected behavior to prevent loops. Set the flag again only if needed.

## Best Practices

1. **Enable Token Recovery**: Always enable `tokenRecovery` for production deployments
2. **Test Recovery**: Periodically test token recovery in non-production environments
3. **Monitor Backups**: Set up alerts for failed token backup operations
4. **Secure Transit Path**: Use Vault policies to restrict access to token backup paths
5. **Document Recovery Keys**: Keep recovery keys in secure, offline storage with clear documentation