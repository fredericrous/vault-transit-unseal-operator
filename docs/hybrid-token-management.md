# Admin token lifecycle

The operator owns the Vault admin token in the admin Secret
(`spec.initialization.secretNames.adminToken`, usually `vault/vault-admin-token`):
it creates it, renews it, rotates it, and revokes the tokens it retires. Earlier
versions of this document described a Kyverno-driven design; it was never the
running one, and nothing outside the operator takes part.

## The token

A periodic, renewable, orphan token with `spec.tokenManagement.policyName`,
minted with `period = spec.tokenManagement.ttl` and no `explicit_max_ttl`.
Being an orphan matters: revoking a token revokes its children, so the admin
token must never be the parent of a token someone else depends on.

**Invariant: the operator is the only automated writer of the admin Secret,
and the admin token mints only orphans.** A person may still install a token
by hand (`bootstrap recover homelab vault-admin-token`).

## Every reconcile, per Vault pod

1. **Initial token** (`ReconcileInitialToken`): create one if none exists,
   replace a root token, or self-heal an invalid one — transit backup first,
   then Kubernetes auth with the operator's own ServiceAccount
   (`vtu-token-selfheal` role).
2. **Renew** (`RenewIfNeeded`): `renew-self` once less than a third of the
   period remains.
3. **Rotate** (`RotateIfDue`), described below.

Steps 2 and 3 read the Secret uncached, so a swap made one pass earlier is
never mistaken for the old token.

## Rotation

| Field | Default | Meaning |
|---|---|---|
| `autoRotate` | `true` | rotate when the token is `rotationPeriod` old (Vault's `creation_time`) |
| `rotationPeriod` | `720h` | age that triggers a scheduled rotation |
| `rotationGracePeriod` | `1h` | how long a retired token stays valid before it is revoked |

Minting needs `enabled: true` and a `strategy` other than `external`: under
either setting the operator does not own the credential and never replaces it.

A rotation mints the new token, records it in the **revoke ledger**, swaps it
into the Secret, and schedules the old token's revocation after the grace
period. Consumers that read the Secret only at start (a Deployment's env)
restart in that window — in homelab, Reloader does it.

### The revoke ledger

The annotation `vault.homelab.io/revoke-ledger` holds every admin token the
operator has retired or minted and not yet seen to its end, as JSON entries:

| Field | Values |
|---|---|
| `accessor` | the token's accessor |
| `reason` | `superseded` (scheduled rotation), `forced` (`rotate-now=immediate`), `unresolved-mint` (a minted token) |
| `state` | `scheduled` (revoke at `notBefore`) or `unresolved` (a minted token whose swap outcome is unknown) |
| `notBefore` | when a scheduled entry is revoked |
| `swapID` | ties together the two entries one rotation writes |

The ledger drains whatever `autoRotate`, `enabled` or `strategy` say: its
entries are credentials the operator already retired. A rotation needs two
slots until its swap lands, so minting stops at 15 entries (the cap is 16),
with a `TokenRotationSkipped` event on every refused pass.

### What each pass does

Each pass first looks up the token in the Secret. Only the accessor that
lookup returns is trusted for a revoke decision, and when the lookup fails —
even with a timeout on a valid token — the pass does nothing.

1. **Settle unresolved swaps.** For each `unresolved-mint` entry, fence the
   Secret (below). Landed: the minted token is live. Not landed: the minted
   token is scheduled for revocation now, and the entry for the token it would
   have replaced is dropped only if that token is still the live one.
2. **Revoke due entries.** `revoke-accessor`, then `lookup-accessor` must answer
   `invalid accessor` before the entry is dropped. An entry whose accessor is
   the live token's is never revoked: it is dropped with the Warning
   `TokenRevokeSkippedCurrent`, because a path reinstalled a retired token.
3. **Rotate when due.** No mint while a swap is unresolved, while the ledger is
   full, or during the backoff after a failure (1 min, doubling to 1 h).
   1. Mint, and validate the new token.
   2. Write both ledger entries. If this write fails or its outcome is unknown,
      revoke the minted token at once: the swap is never sent on this path, so
      that token can never be installed.
   3. Swap the token, preconditioned on the ledger write's resourceVersion.
      The swap removes the minted token's entry in the same write.
   4. Back up the new token to transit (best effort; `TokenBackupFailed`).

### Write outcomes and the fence

Every write is preconditioned on the resourceVersion it read and bounded by a
2 s deadline. A failed write is either **definitely rejected** (conflict,
invalid, bad request, forbidden, unauthorized, not found, too large) or
**uncertain** (timeouts, 5xx, 429, a dropped connection): an uncertain write
may still be applied by the apiserver later.

Nothing revokes a minted token while its swap outcome is uncertain. The fence
settles it, and needs no stored resourceVersion:

1. Re-read the Secret. A failed read leaves the outcome unknown.
2. The minted entry is absent: the swap landed.
3. Otherwise write a `vault.homelab.io/rotation-fence` annotation
   preconditioned on the version just read. Success means the swap never
   landed and never will: it was preconditioned on an earlier version, and
   versions do not repeat. A conflict means something wrote meanwhile: go back
   to 1.

### Forcing, cancelling, and incidents

| Annotation on the admin Secret | Effect |
|---|---|
| `vault.homelab.io/rotate-now=true` | rotate on the next pass (≤ 30 s), whatever `autoRotate` says; the old token is revoked after the grace period |
| `vault.homelab.io/rotate-now=immediate` | the same, and the old token is revoked on the following pass: **incident response** |
| `vault.homelab.io/cancel-revoke=<accessor>` | drop that scheduled entry **without revoking**: the token stays valid and untracked (`TokenRevokeCancelled`) |

`rotate-now` works while other entries are pending: a forced rotation never
needs an entry removed. Never edit the ledger by hand.

**Rotation does not contain an active compromise.** A holder of the admin
token can mint orphan tokens that rotation never touches. After
`rotate-now=immediate`, find every token the compromised accessor created in
the Vault audit log and revoke each one.

**"Expires within the period" holds only for a token nobody renews.** A holder
of a periodic token can renew it indefinitely; only a revocation contains it.
The one token that is ever left to expiry is an **untracked orphan**: a minted
token whose immediate revocation failed, or one minted just before the
operator crashed and before its ledger write. Only the operator's memory ever
held it, nobody can renew it, so it lives at most one period.

## Events

| Reason | Type | When |
|---|---|---|
| `TokenRotated` | Normal | a swap landed; names the old and new accessors and the revoke time |
| `TokenRevoked` | Normal | a retired token is revoked and verified |
| `TokenRotationSkipped` | Warning | a forced rotation cannot run now, and why |
| `TokenRotationFailed` | Warning | a step failed (ledger, mint, swap, fence, revoke, verify), its class, and what happened to the minted token |
| `TokenRevokeSkippedCurrent` | Warning | a retired token is the live one again |
| `TokenRevokeCancelled` | Warning | `cancel-revoke` dropped an entry |
| `TokenRecoverySkipped` | Warning | the transit backup holds a retired token; self-heal is used instead |
| `TokenBackupFailed` | Warning | the rotated token is not in the transit backup |

## Metrics

Timestamps rather than ages, so an alert on `time() - x` keeps moving when the
operator stops observing.

| Metric | Meaning |
|---|---|
| `vault_admin_token_ttl_seconds` | remaining TTL |
| `vault_admin_token_created_timestamp_seconds` | `creation_time` of the live token |
| `vault_admin_token_last_observation_timestamp_seconds` | last successful lookup; stops when the loop or Vault stops |
| `vault_admin_token_rotation_period_seconds`, `vault_admin_token_auto_rotate` | the configuration in force |
| `vault_admin_token_ledger_entries`, `vault_admin_token_revocations_pending` | ledger size (the same value; minting stops at 15) |
| `vault_admin_token_unresolved_mints` | swaps with an unknown outcome |
| `vault_admin_token_oldest_due_revoke_timestamp_seconds` | the oldest overdue revocation, 0 when none |
| `vault_admin_token_rotations_total{result}` | rotation outcomes |
| `vault_admin_token_backup_failures_total` | transit backups of a rotated token that failed |
| `vault_admin_token_revoke_skipped_current_total` | retired tokens found live again |

## Rolling back to an operator without rotation

An older operator ignores the ledger, so the tokens in it stop being revoked.
Drain it first: wait for `vault_admin_token_revocations_pending` to reach 0, or
revoke each ledgered accessor by hand and check `lookup-accessor` answers
`invalid accessor`.
