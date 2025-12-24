# Hybrid Token Management Architecture

## Overview

The hybrid approach splits token management responsibilities between the vault-transit-unseal-operator and Kyverno policies, providing the best balance of reliability and maintainability.

## Architecture

```
┌─────────────────────────────────┐
│  vault-transit-unseal-operator  │
├─────────────────────────────────┤
│ Responsibilities:               │
│ • Vault initialization          │
│ • Auto-unsealing               │
│ • Initial token creation       │
│ • Dependency checking          │
└──────────────┬──────────────────┘
               │ Creates initial token
               │ with lifecycle annotations
               ▼
┌─────────────────────────────────┐
│      vault-admin-token          │
│         (Secret)                │
├─────────────────────────────────┤
│ Annotations:                    │
│ • token-created                 │
│ • token-expires                 │
│ • token-accessor                │
│ • auto-renew: true             │
│ • auto-rotate: true            │
│ • lifecycle-manager: kyverno   │
└──────────────┬──────────────────┘
               │ Watched by
               ▼
┌─────────────────────────────────┐
│      Kyverno Policies           │
├─────────────────────────────────┤
│ Responsibilities:               │
│ • Token renewal (< 7 days)     │
│ • Token rotation (> 25 days)   │
│ • Health monitoring            │
│ • Compliance checking          │
│ • Event logging               │
└─────────────────────────────────┘
```

## Component Details

### 1. Operator (Simplified)

The operator now only handles initial token creation:

```go
// SimpleManager in pkg/token/manager_simple.go
type SimpleManager struct {
    client.Client
    Log    logr.Logger
    Scheme *runtime.Scheme
}

// Only creates initial token with lifecycle annotations
func (m *SimpleManager) ReconcileInitialToken(ctx context.Context, vtu *VaultTransitUnseal, vaultClient *vault.Client) error {
    // Check dependencies
    // Create token once
    // Add Kyverno annotations
    // Mark as initialized
}
```

**Benefits:**
- Removes ~400 lines of lifecycle code
- No complex state machines
- Easier to test and maintain
- Clear single responsibility

### 2. Kyverno Policies

Three main policies handle ongoing lifecycle:

#### a. Token Renewal Policy
- **Schedule**: Every 30 minutes
- **Trigger**: Token expires in < 7 days
- **Action**: Create job to renew token
- **Safety**: Checks last renewal to prevent loops

#### b. Token Rotation Policy  
- **Schedule**: Every 4 hours
- **Trigger**: Token age > 25 days OR next rotation time passed
- **Action**: Create new token, backup old, schedule revocation
- **Safety**: Grace period for old token, rollback on failure

#### c. Token Monitoring Policy
- **Type**: Audit/Alert only
- **Checks**: Expiration warnings, rotation compliance
- **Output**: Metrics ConfigMap, Kubernetes Events
- **Integration**: Can trigger alerts via Prometheus

### 3. Token Manager Container

Small Go program that Kyverno jobs execute:

```dockerfile
FROM alpine:3.19
COPY vault-token-manager /bin/
ENTRYPOINT ["/bin/vault-token-manager"]
```

**Commands:**
- `renew`: Extends token TTL
- `rotate`: Creates new token, manages transition

## Implementation Guide

### Step 1: Deploy Enhanced Operator

```bash
# Update CRD with tokenManagement spec
kubectl apply -f config/crd/bases/vault.homelab.io_vaulttransitunseals.yaml

# Deploy operator with simplified token manager
kubectl set image deployment/vault-transit-unseal-operator-controller-manager \
  manager=ghcr.io/fredericrous/vault-transit-unseal-operator:latest \
  -n vault
```

### Step 2: Update VaultTransitUnseal Resource

```yaml
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-homelab
spec:
  tokenManagement:
    enabled: true
    strategy: "delayed"
    dependencies:
      deployments:
      - name: "vault-config-operator"
        namespace: "vault-config-operator"
        minReadyReplicas: 1
```

### Step 3: Token Manager Image

The vault-token-manager image is automatically built and pushed by GitHub Actions when changes are made to the vault-token-manager directory. No manual build required!

### Step 4: Deploy Kyverno Policies

```bash
kubectl apply -k kubernetes/homelab/security/policies/vault-token-lifecycle/
```

### Step 5: Verify

```bash
# Check initial token creation
kubectl get vaulttransitunseal -n vault vault-homelab -o jsonpath='{.status.tokenStatus}'

# Check token annotations
kubectl get secret vault-admin-token -n vault -o jsonpath='{.metadata.annotations}' | jq .

# Monitor Kyverno policies
kubectl get cpol | grep vault-token

# Check metrics
kubectl get cm vault-token-metrics -n vault -o yaml
```

## Monitoring

### Metrics to Track

1. **Token Age**: `vault_token_age_days`
2. **Time Until Expiry**: `vault_token_expiry_days` 
3. **Rotation Compliance**: `vault_token_rotation_overdue`
4. **Renewal Failures**: `vault_token_renewal_failed_total`

### Alerts to Configure

```yaml
# Prometheus rules
groups:
- name: vault-tokens
  rules:
  - alert: VaultTokenExpiringSoon
    expr: vault_token_expiry_days < 3
    annotations:
      summary: "Vault admin token expires in {{ $value }} days"
      runbook_url: ".../runbooks/token-rotation-failure.md"
      
  - alert: VaultTokenRotationOverdue  
    expr: vault_token_age_days > 30 AND vault_token_auto_rotate == 1
    annotations:
      summary: "Token rotation overdue by {{ $value - 30 }} days"
```

## Advantages Over Full Operator Approach

1. **Maintainability**: Token lifecycle logic in YAML, not Go
2. **Flexibility**: Easy to adjust schedules, thresholds
3. **Observability**: Kyverno provides built-in policy reports
4. **Debugging**: Each rotation creates a job with logs
5. **GitOps**: Everything in declarative YAML

## Advantages Over Pure Kyverno Approach

1. **Reliability**: Operator ensures initial token exists
2. **Dependencies**: Operator handles complex startup sequencing  
3. **Integration**: Single owner for Vault lifecycle
4. **State**: CRD status tracks initialization

## Troubleshooting

### Token Not Created
```bash
# Check operator logs
kubectl logs -n vault deployment/vault-transit-unseal-operator

# Check VTU status  
kubectl get vtu -n vault vault-homelab -o yaml | grep -A10 tokenStatus
```

### Rotation Not Happening
```bash
# Check Kyverno policy status
kubectl get cpol vault-token-rotation -o yaml | grep -A10 status

# Check for rotation jobs
kubectl get jobs -n vault -l kyverno.io/policy=vault-token-rotation

# Check policy reports
kubectl get polr -n vault
```

### Manual Intervention
See [Token Rotation Failure Runbook](./runbooks/token-rotation-failure.md)

## Migration Path

From existing setup:
1. Deploy new operator version
2. Apply Kyverno policies  
3. Operator detects existing token, marks initialized
4. Kyverno takes over lifecycle
5. Monitor for 30+ days to verify rotation

No downtime or token recreation required!