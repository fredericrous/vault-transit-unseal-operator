# Stored-key (Shamir) unseal mode

## Why a second mode exists

The operator was built around one shape of Vault: a **leaf** Vault, sealed with
`seal "transit"`, that unseals against some other Vault holding the transit key.
Recovery there is indirect. There is no API to "trigger" a transit unseal —
`POST /v1/sys/unseal` on a transit-sealed Vault fails with `'key' must be
specified` — so the only correct move is to restart the pod and let Vault unseal
from its seal stanza at boot.

That leaves the **root of trust** unautomated: the Vault at the bottom of the
chain, which by definition has no other Vault to unseal against. It is
Shamir-sealed, its key lives in a Kubernetes Secret in the same cluster, and
until now the thing that fed that key to `sys/unseal` was a CronJob
(`kubernetes/cloud/security/vault/vault-auto-unseal.yaml`, and its ancestor on
the NAS) running a curl script every two minutes.

`mode: stored-key` makes the operator the automation surface for that shape too,
so there is one thing to reason about, one place that reports status, and one
set of metrics — instead of an operator for some Vaults and a shell script in a
CronJob for the others.

## What the two modes actually do

| | `transit` (default) | `stored-key` |
| --- | --- | --- |
| Vault's seal | `seal "transit"` | Shamir |
| Credential | Transit token Secret | Key share(s) Secret |
| Recovery from sealed | Delete the pod; Vault unseals at boot | `PUT sys/unseal` on the running pod |
| Pod restarted? | Yes | **No** |
| Initializes Vault? | Yes | **Never** — see below |
| Trigger | Pod watch + `checkInterval` | Pod watch + Secret watch + `checkInterval` |

An absent or empty `spec.mode` is `transit`. Every resource written before this
field existed keeps its exact previous behaviour, and there is a test that says
so.

## The CR

```yaml
apiVersion: vault.homelab.io/v1alpha1
kind: VaultTransitUnseal
metadata:
  name: vault-stored-key
  namespace: vault
spec:
  mode: stored-key

  vaultPod:
    namespace: vault
    selector:
      app.kubernetes.io/name: vault
    headlessServiceName: vault-internal
    servicePort: 8200

  storedKey:
    secretRef:
      name: vault-unseal-keys     # default
      key: unseal-keys.txt        # default
      # namespace: vault          # defaults to vaultPod.namespace

  monitoring:
    checkInterval: 30s
```

`transitVault` is omitted; it is no longer a required property.

The full annotated version, with the TLS options and the Secret's shape, is
`config/samples/vault-stored-key.yaml`.

### Reaching a sealed pod

This is the setting people get wrong. **A sealed Vault pod is not ready**, so
the client-facing Service excludes it — and that is precisely the pod that needs
unsealing. Set `headlessServiceName` to the StatefulSet's governing headless
service, which publishes not-ready addresses, and the operator will address each
pod individually at
`<pod>.<headless>.<namespace>.svc.cluster.local:<servicePort>`.

`vaultAddress` overrides the whole thing if you need something else.

### TLS

`vaultPod.scheme` (`http` | `https`) picks the scheme for addresses the operator
builds itself. Unset means `http`, which is what those addresses were hard-coded
to before the field existed, so nothing changes for existing installs.

For `https`, give the operator the CA through the `VAULT_CACERT` environment
variable (chart: `extraEnvVars` plus `extraVolumes`/`extraVolumeMounts`). With
no CA and no `vaultTlsValidation: false`, verification uses the system roots.

### The key Secret

One share per line:

```
share-1
share-2
share-3
```

Blank lines are dropped, and `\r` and surrounding whitespace are stripped, so a
file with CRLF endings, a trailing newline, or no newline at all all parse the
same.

Shares are submitted in order until Vault reports itself unsealed — spare shares
beyond the threshold are never spent. A single-line file is the common case and
is exactly the format the CronJob used.

## Uninitialized Vault: the operator will not touch it

In stored-key mode the operator **never** calls `sys/init`, and there is no flag
to make it. Two reasons, both about blast radius:

1. Initialization produces the unseal shares and the root token. If the operator
   generated them, they would have to be written somewhere for a human to
   capture — and the natural somewhere is a Secret in the very cluster whose
   Vault they open. `bootstrap run <cluster> vault-setup` already owns this step
   and already seeds the key Secret from it.
2. A Vault reports `initialized: false` whenever its storage backend looks
   empty. That is true of a genuinely new Vault, and equally true of one whose
   PVC failed to attach or whose Raft peer list came up wrong. Initializing on
   that signal alone turns a recoverable outage into a permanent one: the old
   data is still there, but it is now sealed with keys nobody has.

So the operator reports and waits:

- condition `Initialized=False`, reason `AwaitingExternalInit`, with a message
  naming the bootstrap step;
- a Warning event `InitializationDeferred`.

Apply the CR before Vault is initialized if you like — it will sit in that state
harmlessly, and start unsealing as soon as the Secret appears, because it
watches for it.

(An `allowInit` flag was considered and deliberately dropped. The re-init flow
in `RE-INITIALIZATION.md` is about recovering a lost *admin token* on an
already-initialized Vault via the transit KV backup — a different problem, with
no transit Vault to lean on here. A flag that only ever adds risk is not worth
its knob.)

## What you can observe

### Conditions

| Type | Meaning |
| --- | --- |
| `Initialized` | Vault reports `initialized`. `False` with reason `AwaitingExternalInit` means it is waiting on the bootstrap step. |
| `Ready` | Vault is **unsealed**. `False` with reason `VaultSealed` while sealed, or `UnsealFailed` with the error when an unseal attempt failed. |
| `KeySecretPresent` | The key Secret exists and holds at least one share. `False` with reason `SecretUnreadable` or `NoShares`. |

### Status fields

- `unsealMode` — the resolved mode, so `kubectl get vaulttransitunseal -o yaml`
  shows which automation is in charge without you having to reason about an
  absent `spec.mode`.
- `lastUnsealTime` — when the operator last brought this Vault from sealed to
  unsealed. Only stored-key mode sets it; transit mode never unseals over the
  API.
- `sealed`, `initialized`, `lastCheckTime` — as before.

### Metrics

- `vault_operator_unseal_attempts_total{mode,result}` — `result` is `success` or
  `failure`. Only stored-key mode increments it.
- `vault_operator_vault_status{status="sealed"}` — the sealed gauge, unchanged.

A useful alert is the pair: sealed gauge stuck at 1 **and** the failure counter
rising means the stored key no longer opens this Vault, which is a page.

### Events

| Reason | Type | When |
| --- | --- | --- |
| `Unsealed` | Normal | Vault was unsealed from the stored key |
| `UnsealFailed` | Warning | `sys/unseal` rejected the share, or the threshold was not met |
| `UnsealKeyUnavailable` | Warning | Vault is sealed and the Secret holds no usable share |
| `InitializationDeferred` | Warning | Vault is uninitialized and this mode will not initialize it |
| `InvalidConfig` | Warning | The key Secret was unreadable at the top of reconcile |

### Key material never appears

No share reaches a log line, an error, an event, or a status condition. Vault's
own error text echoes the response body, not the request, and nothing in the
unseal path formats a key. There are tests asserting exactly this on every error
path — treat them as load-bearing when editing that code.

## Migrating a cluster off the CronJob

**Not done as part of this change** — the operator gained the capability, the
cluster trees still run their CronJobs. When you do the swap, per cluster:

### 1. Confirm the operator is deployed and reaches the CRD

```bash
kubectl get crd vaulttransitunseals.vault.homelab.io
kubectl -n vault-transit-unseal-system get deploy
```

The chart must be at a version whose CRD carries `spec.mode`; a CRD without it
rejects `mode: stored-key` at admission.

### 2. Apply the CR

Take `config/samples/vault-stored-key.yaml` and adjust three things: the pod
`selector`, the `headlessServiceName` (whatever the Vault StatefulSet's
governing service is called — `vault-internal` for the HashiCorp chart), and
`servicePort`.

The Secret does not move. `vault-unseal-keys` / `unseal-keys.txt` in namespace
`vault` is both the CronJob's source and this mode's default.

### 3. Verify it took over

```bash
kubectl -n vault get vaulttransitunseal vault-stored-key -o yaml
```

Expect `status.unsealMode: stored-key`, `KeySecretPresent=True`, and
`Ready=True`. Then prove it end to end by sealing Vault deliberately and
watching it come back:

```bash
kubectl -n vault exec vault-0 -- vault operator seal   # needs a token
kubectl -n vault get events --field-selector reason=Unsealed --watch
```

It should unseal within seconds — the pod watch fires on the readiness
transition rather than waiting for the next check interval. (The CronJob's floor
was two minutes.)

Leave the CronJob in place for this step. Both are idempotent: whichever gets
there first unseals, and the other finds Vault already unsealed and exits.

### 4. Remove the CronJob

Once you have seen the operator do the unseal, delete from the cluster tree:

- the `CronJob/vault-auto-unseal`
- its `ServiceAccount/vault-auto-unseal`

i.e. all of `kubernetes/cloud/security/vault/vault-auto-unseal.yaml`, and its
entry in the kustomization. The NAS equivalent lives at
`kubernetes/nas/apps/auto-unseal/` and reads its key from a GPG file on a QNAP
share rather than a Secret — that one needs the key materialised into a Secret
first, so treat it as a separate migration.

### Rolling back

Delete the VaultTransitUnseal and re-apply the CronJob. Nothing about the Secret
or Vault's own state changes between the two, so rollback is just swapping which
automation is present.
