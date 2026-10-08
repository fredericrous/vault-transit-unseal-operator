---
status: active
branch: feat/admin-token-rotation
repos: [vault-transit-unseal-operator, homelab]
adrs: []
---
# Real rotation of the Vault admin token

## Review panel

👉 **Decide:** none, so approve if you agree that the revocation ledger with a resourceVersion fence answers your five gaps. One release decision comes later: cut operator v2.8.0 after the merge.
📍 vault-transit-unseal-operator (canonical), homelab (pointer) · revised after your review, nothing built · next: Phase 1. Panel: backend, architect, go, python, typescript, platform, po, tui, unix.
**Changed by your review:**
- a ledger replaces the single pending slot;
- an uncertain write is resolved by a fence before any revoke, and the current accessor is never revoked;
- gating on `enabled`/`strategy`, timestamp gauges with staleness alerts, and 9 promtool cases.
📄 Full reviews: [2026-10-07-admin-token-rotation.reviews.md](2026-10-07-admin-token-rotation.reviews.md)
**Verdicts:** round 1 approve 1 (python) and approve-with-changes 8, no rework. Backend: 7 passes, the last one approving the final body.

Canonical file: `vault-transit-unseal-operator/docs/plans/2026-10-07-admin-token-rotation.md`. homelab
carries a pointer file of the same name (`work.plan-lives-in-the-repo`): the rotation behaviour is the
operator's decision.

## Context

`VaultTokenRotationFailed` can never fire: no `vault-token-rotation` Job exists. The Kyverno/root-based
rotation was replaced by a periodic orphan token that the operator only **renews**
(`pkg/token/renew.go` `RenewIfNeeded`). Renewal is not rotation. The live `vault/vault-admin-token` has
held the same value since **2026-06-04** (`display_name: token-admin-recovered-2026-06-04`, period 168h,
no `explicit_max_ttl`). A leaked copy stays valid for as long as the operator keeps renewing it, which has
no end date. `autoRotate` (CRD default `true`, set to `false` in homelab) only ever wrote annotations
(`manager_simple.go:383,825,1067`). The Secret still carries `auto-rotate: "true"` and
`rotation-period: 720h`, and `docs/operations/vault-token-rotation.md` describes a 30-day root re-mint that
cannot happen.

Outcome: the operator mints a fresh admin token every `rotationPeriod`, swaps it in the Secret, lets
consumers pick it up, and revokes the old token after a grace period. Every step is observable. A
rotation can be forced in response to a leak, even while scheduled rotation is off.

## Facts this plan is built on

- The `vault-admin` policy is `path "*"` with `create, read, update, delete, list, sudo`
  (`vault-admin-setup.yaml:85-88`). The live token has those capabilities on `auth/token/create-orphan`,
  `auth/token/create` (needed for `no_parent`, which takes sudo) and `auth/token/revoke-accessor`.
  Checked 2026-10-07; the `create` path is re-checked at implementation.
- Minting already exists: `tryRecoverViaK8sAuth` (`manager_simple.go:864-968`) posts `auth/token/create`
  with `NoParent: true` to get an orphan, periodic, renewable token. The `vtu-token-selfheal` role exists
  live and is bound to SA `vault-transit-unseal-operator`.
- `tryRecoverFromTransitBackup` validates a backup with `LookupSelf` before installing it (`:246-300`),
  so a stale backup of a revoked token is never reinstated.
- **Writers of `vault/vault-admin-token`**:
  - the operator;
  - `bootstrap recover homelab vault-admin-token` (`recover.go:376-433`), by hand;
  - **the `vault-admin-setup` Job** (`setup-jobs/vault-admin-setup.yaml:91-107`, Flux
    `force: enabled`). It runs `vault token create -policy=vault-admin` *with the current admin token*,
    which makes a **non-orphan child**, overwrites the Secret, and narrows its reflection annotation to
    one namespace. `revoke-accessor` revokes a token's children, so a run of this Job during a grace
    window would install a token that dies at the revoke.
  The live token is an orphan, so no such child is installed today. No other manifest mints tokens with
  the admin token: `nas-metrics` and `duro-pki` use NAS token roles. vault-config-operator CRs log in
  through Kubernetes auth (`kv-dynamic-engine-mount.yaml:20-24`), not with the admin token.
  PKI roles were read live on 2026-10-07: `pki/roles/client-cert` has `generate_lease=false`, and the four
  `pki-istio-*` mounts have **no roles** (they issue through root/intermediate endpoints, which create no
  lease). No PKI lease hangs off the admin token.
- **Readers.** The only long-running consumer is `vault-config-operator` (2 replicas, `VAULT_TOKEN` from
  env at pod start, `release.yaml:63-67`). It has required hostname anti-affinity
  (`release.yaml:38-45`), and `enableMonitoring: false` (`:109`). The other readers are Jobs and
  CronJobs that read the Secret at pod creation: pki-istio-*, vault-audit-setup and the setup-jobs in
  `vault`/`vault-config-operator`, plus openwebui's `vault-role-setup`. `bootstrap seed` reads it once
  per run (`seed.go:294`). Every ESO store uses `auth=kubernetes`. Reflected copies live in 7
  namespaces, and emberstack reflector propagates source updates. Reloader runs in homelab.
- **Operator internals.**
  - `SimpleManager` embeds only the cached client (`manager_simple.go:33-44`); there is no APIReader.
  - `renew_test.go` tests only early returns with a `nil` Vault client, and the wire path is left to the
    integration suite (`:59-62,87`). There is no fake Vault to reuse.
  - The controller watches only owned Secrets (`vaulttransitunseal_controller.go:127`), so an annotation
    edit is picked up at the next requeue (`checkInterval: 30s`).
  - The call site is `vault_reconciler.go:441`, once per Vault pod.
- **Rollout facts.**
  - The live token is about 125 days old, **already past 720h**: scheduled rotation turned on would fire
    on the first reconcile.
  - The VTU CR, the operator HelmRelease and the vault-config-operator HelmRelease are applied by
    different Flux Kustomizations, with no ordering between them.
  - Cloud's VTU has no `tokenManagement`, so the new path is a no-op there.
  - homelab runs operator **2.6.2**; origin/main is `v2.7.0-5`.

## Non-goals

- **Automating rotation of `vault-transit-token`** (see the dedicated section below).
- No change to renewal semantics or the recovery-keys path. There are two deliberate exceptions:
  - renewal reads through the APIReader (Phase 2);
  - **transit-backup recovery refuses a token whose accessor is in the revocation ledger** (Phase 2,
    step 0). Without that, recovery during a grace window could reinstall the token that is about to
    be revoked.
- No new RBAC *from this plan*: the ledger lives in the admin Secret, which the operator already
  updates, and consumer restarts go through Reloader. The release does ship 2.7.0's `delete-pods`
  ClusterRole rule.
- **Rotation does not contain an active admin-token compromise.** A holder of `vault-admin` can mint
  independent orphan tokens that rotation never touches. The runbook covers the manual response
  (Phase 4), and automating it is out of scope.

## Phases

### Phase 1 — CRD and wiring (structural) — operator
- `TokenManagementSpec`:
  - add `RotationGracePeriod string` (`+kubebuilder:default="1h"`);
  - rewrite the doc comments on `AutoRotate` and `RotationPeriod` to describe the real behaviour,
    including the gating on `enabled` and `strategy`;
  - the `AutoRotate` default stays `true`: no instance relies on it, and cloud has no
    `tokenManagement`.
- Add `APIReader client.Reader` to `SimpleManager`, wired from `mgr.GetAPIReader()` where the manager is
  built (`main.go` / `SetupWithManager`).
- `make manifests generate`; copy the regenerated CRD into `chart/vault-transit-unseal-operator/crds`.

### Phase 2 — `RotateIfDue` with a revocation ledger (behaviour) — operator
New `pkg/token/rotate.go` and `pkg/token/ledger.go`, called right after `RenewIfNeeded` at
`vault_reconciler.go:441`. `RenewIfNeeded` also switches its Secret read to the APIReader. Every read in
this path goes through the APIReader, and every write is an `Update` that carries the `resourceVersion`
it read.

**The ledger** is the annotation `vault.homelab.io/revoke-ledger` on the admin Secret, a JSON list of
entries `{accessor, notBefore, reason, state, swapID}`:
- `reason` is one of `superseded`, `unresolved-mint` or `forced`;
- `state` is `scheduled` or `unresolved`;
- `swapID` is a random ID shared by the two entries one rotation writes: the minted token's and the old
  token's.

The ledger is **capped at 16 entries**. At the cap, minting refuses with `TokenRotationSkipped`, and
`VaultAdminTokenLedgerFull` fires.

It replaces `previous-accessor` / `previous-revoke-after`. The Secret's `token-accessor` is never
trusted for a revoke decision. **Current accessor** always means the accessor that `LookupSelf` returns
for `.data.token`, read in the same call. **Steps 1–4 run only after that `LookupSelf` succeeded in this
call.** On a lookup error, including a timeout on a token that is still valid, the call does nothing
beyond publishing metrics. The observation timestamp is not advanced, and self-heal still handles a
token Vault rejects as invalid.

**Write outcome classification**, for every `Update` in this path. Each `Update` runs under a **2s
per-write context deadline**, so a write that hangs becomes an uncertain outcome instead of blocking
the reconcile:
- **Definite rejection**: `IsConflict`, `IsInvalid`, `IsBadRequest`, `IsForbidden`, `IsUnauthorized`,
  `IsNotFound`, `IsRequestEntityTooLarge`. The write did not and will not land.
- **Uncertain**: everything else, including `IsTimeout`, `IsServerTimeout`, `IsInternalError`, 429,
  connection errors and context deadlines. The write may still land later.

**Fence.** A fence resolves whether a swap whose outcome is unknown landed. It needs no stored RV:
1. Re-read the Secret through the APIReader. If the re-read fails, the outcome stays **unresolved**.
2. The swap removes the `unresolved-mint` entry in the same write that installs the token, so **the
   entry's absence from the re-read means the swap landed**. This needs no token value, and works after
   a crash. If the entry is still present, the swap has not landed at this version.
3. Otherwise, `Update` a fence annotation (`vault.homelab.io/rotation-fence=<ts>`) preconditioned on the
   **`resourceVersion` just read**.
   - Success: the swap **did not land**, and now never can. The pending swap was preconditioned on an
     earlier RV, RVs never repeat, and this write has moved the object past it.
   - Conflict: go back to step 1.
   - Uncertain: **unresolved**; keep everything and retry on the next reconcile.

The steps, in order:

0. **Gate.**
   - `TokenManagement == nil` → return.
   - Minting (step 4) requires `enabled: true` **and** `strategy != external`. Otherwise `rotate-now`
     emits `TokenRotationSkipped` naming the setting, and nothing is minted.
   - **The ledger drains (steps 1–2) whatever `enabled`, `strategy` or `autoRotate` say**: its
     entries are credentials the operator already retired, and leaving them alive is the unsafe
     default. Only an explicit cancel (below) removes an entry without revoking it.
   - `tryRecoverFromTransitBackup` gains a check: a backup whose accessor is any ledger entry is
     rejected, with `TokenRecoverySkipped`, and recovery falls through to k8s-auth self-heal. Self-heal
     keeps the ledger annotation (`manager_simple.go:928-949` edits only named keys), so draining
     survives a self-heal.
1. **Resolve unresolved entries.** For each `unresolved-mint` entry, run the fence:
   - **landed** → drop the minted entry. The `superseded`/`forced` entry with the same `swapID` stays
     scheduled.
   - **did not land** → set the minted entry to `scheduled, notBefore: now`, so the minted token is
     revoked in step 2. For the old entry with the same `swapID`, the outcome is decided by the
     **current accessor**, not by assumption. If the fence's re-read shows a `.data.token` different
     from the one this call ran `LookupSelf` on, stop the pass and keep both entries. Otherwise:
     - equal → that token is live, so drop the entry;
     - different → it was superseded by some other path, so keep the entry scheduled.
   - **unresolved** → keep both entries.
2. **Revoke due entries.** For each `scheduled` entry with `now ≥ notBefore`:
   - **guard**: if `entry.accessor == current accessor`, never revoke it. Drop the entry, emit the
     Warning `TokenRevokeSkippedCurrent`, and increment `vault_admin_token_revoke_skipped_current_total`.
     This catches any path that reinstalled a ledgered token.
   - otherwise call `revoke-accessor`, then **verify** with `lookup-accessor`, which must answer
     `invalid accessor`. Drop the entry only when that verification passes. On any other answer, keep
     the entry and count a failure.
3. **Due?**
   - Due means `autoRotate` is on and `now − creation_time ≥ rotationPeriod`, or `rotate-now` is
     present (`"true"` or `"immediate"`).
   - `rotate-now` is honoured while `superseded`/`forced` entries are pending: the ledger holds several,
     so a forced rotation never needs to clear one.
   - **No mint while any `unresolved-mint` entry exists** (`TokenRotationSkipped`: "previous swap
     unresolved").
   - **No mint inside the failure backoff**: 1m doubling to a cap of 1h after each failed rotation. It
     is held in memory and in the `vault.homelab.io/rotation-backoff-until` annotation, written
     best-effort.
   - With 3 pod passes every 30s, these two gates bound the mint rate to one per backoff window.
4. **Mint and swap.**
   1. Mint with `mintAdminToken(raw, vtu, "rotation")`. This is extracted from
      `tryRecoverViaK8sAuth`, holds only the create request and the `LookupSelf` validation, and
      leaves each caller to write its own annotations.
   2. **Write the ledger first.** Add, under a fresh `swapID`:
      - `{minted, unresolved-mint, unresolved}`;
      - `{old, superseded, scheduled, notBefore: now+grace}`, or for `"immediate"`:
        `notBefore: now, reason: forced`.

      Outcomes:
      - **Definite rejection, or uncertain** → `revoke-self` the minted token immediately, and stop.
        The swap is never sent on this path, so the token can never be installed, and revoking it is
        safe. If an uncertain ledger write lands later, step 1 fences its entries:
        - the minted entry resolves *did not land* → its revoke verifies `invalid` → dropped;
        - the old entry's accessor equals the current accessor → dropped.
   3. **Swap**, preconditioned on the RV the ledger write returned: `token=new`, `token-created`,
      `token-accessor`, `rotated-at`, the minted entry dropped, `rotate-now` removed.
      - Success → back up the new token to transit (best-effort; a failure raises `TokenBackupFailed`
        and counts in `vault_admin_token_backup_failures_total`).
      - Definite rejection → **did not land**. Resolve as in step 1 on this pass: the minted entry is
        scheduled now, and the old entry is decided by the current accessor.
      - Uncertain → leave the entry `unresolved`, start the backoff, and let step 1 fence it later.
        **A crash here is safe**: the entry carries everything the fence needs.

      **Nothing revokes the minted token while its swap outcome is unresolved.**
5. **Truthful annotations.** Write `auto-rotate`/`rotation-period` only while `autoRotate` is on, and
   delete them otherwise. Remove the never-honoured `next-rotation`.

**Cancel versus expedite**, both triggered by annotations on the admin Secret:
- `vault.homelab.io/cancel-revoke=<accessor>` drops that one entry **without revoking**, and emits
  `TokenRevokeCancelled`. Use it only when the old token must survive (a consumer that can't restart).
  It's logged because it leaves a valid credential behind.
- `vault.homelab.io/rotate-now=immediate` rotates and schedules the old token with `notBefore: now`:
  the incident response.
- There is no instruction anywhere to delete ledger entries by hand.

Events:
- `TokenRotated`, naming the old and new accessors;
- `TokenRotationSkipped`;
- `TokenRevoked`;
- `TokenRevokeSkippedCurrent`;
- `TokenRevokeCancelled`;
- `TokenRecoverySkipped`;
- `TokenRotationFailed`, whose message names the step (gate, ledger, mint, swap, fence, revoke or
  verify), the error, its classification (definite or uncertain), and what happened to the minted
  token.

Metrics. They are **timestamps rather than ages**, so an alert still progresses with `time()` when the
loop stops:
- `vault_admin_token_created_timestamp_seconds`;
- `vault_admin_token_last_observation_timestamp_seconds`, set only on a successful `LookupSelf` of the
  current token;
- `vault_admin_token_rotation_period_seconds`;
- `vault_admin_token_auto_rotate` (0/1);
- `vault_admin_token_revocations_pending` (the count of ledger entries);
- `vault_admin_token_oldest_due_revoke_timestamp_seconds` (0 when nothing is due);
- `vault_admin_token_unresolved_mints`;
- `vault_admin_token_ledger_entries`, compared against the cap of 16;
- `vault_admin_token_revoke_skipped_current_total`;
- `vault_admin_token_rotations_total{result}` and `vault_admin_token_backup_failures_total`, with every
  label initialised to 0.

### Phase 3 — tests and docs — operator
- A fake Vault in `pkg/token/fakevault_test.go`: an `httptest.Server` that serves `lookup-self`,
  `create`, `lookup-accessor`, `revoke-accessor` and `revoke-self`, keeps token state, and records each
  call. The fake k8s client uses `interceptor.Funcs` to inject definite and uncertain `Update` failures,
  including a **delayed commit**: the write returns a timeout and is then applied after a later
  `Get`. Every case asserts the exact recorded calls and the final ledger.
- `rotate_test.go` cases. Earlier cases carried over:
  - not due;
  - due by age;
  - `rotate-now` with `autoRotate=false`;
  - invalid current token → no mint;
  - renew after a swap reads the fresh Secret;
  - Secret replaced outside the operator while pending → only the ledgered accessor is revoked.
- New cases:
  - **uncertain swap whose write commits after the verification read** → the fence conflicts, the
    re-read sees the minted token, and it is *not* revoked;
  - uncertain swap that never commits → the fence succeeds, and the minted token is revoked in the next
    step 2;
  - an uncertain fence → the entry stays `unresolved`, and nothing is revoked;
  - a failed re-read → the minted token is intact and the entry `unresolved`;
  - the ledger write is rejected definitely → the minted token is revoked at once;
  - the ledger write is uncertain → no swap; the next pass finds both entries or neither;
  - a conflict on the swap → the fence path runs;
  - **A→B, the backup of B fails, B becomes invalid during the grace window, then recovery** → the
    transit backup (A) is refused because A is ledgered. k8s self-heal installs C. At the deadline A is
    revoked and C is untouched;
  - a ledgered accessor equal to the current accessor (any path) → not revoked;
    `TokenRevokeSkippedCurrent`;
  - revoke succeeds but `lookup-accessor` still answers valid → the entry is kept and a failure counted;
  - `rotate-now` while an entry is pending → rotates, and the ledger holds 2 entries;
  - `rotate-now=immediate` → the old entry has `notBefore=now`;
  - `cancel-revoke=<acc>` → the entry is dropped and nothing is revoked;
  - `enabled: false` with `rotate-now` → no mint, `TokenRotationSkipped`, and the ledger still drains;
  - `strategy: external` with `rotate-now` → same;
  - `autoRotate=false` with a pending entry → the revoke still runs;
  - a timeout from `LookupSelf` on a still-valid token → no revoke and no mint; `last_observation` is
    not updated;
  - **a crash between an uncertain swap and the fence** → a new manager instance fences from the ledger
    alone, and the outcome is correct whether the late write commits or not;
  - **B's fence uncertain, then `rotate-now` mints C, C's swap lands, then B resolves as did-not-land**
    → B is revoked, A's entries are kept (A ≠ current accessor C), and A is revoked at its `notBefore`;
  - an uncertain ledger write → `revoke-self(minted)` immediately; if the write lands later, both its
    entries resolve and drain;
  - an `unresolved-mint` entry present with `rotate-now` set → no mint; `TokenRotationSkipped`;
  - repeated swap failures → mints spaced by the backoff;
  - ledger at 16 → no mint; `LedgerFull`.
- `docs/hybrid-token-management.md`:
  - the lifecycle, the ledger and the fence;
  - the gating on `enabled` and `strategy`;
  - cancel versus expedite;
  - the invariant that **the operator is the only automated writer and the admin token mints only
    orphans**;
  - an **untracked orphan** is a minted token whose `revoke-self` failed, or a crash between mint and
    ledger write. Only the operator's memory ever held it and nobody can renew it, so it lives at most
    168h;
  - **"expires within 168h" holds only for a token nobody renews**. A holder of a renewable periodic
    token can keep it alive indefinitely, and only a revocation contains it.
- 🧑 decision: cut operator **v2.8.0** after merge (`work.release-on-request`, via `tag-release`).

### Phase 4 — homelab rollout PR (pointer plan) — rotation still off
- `controllers/vault-transit-unseal-operator.yaml`: bump the chart 2.6.2 → 2.8.0.
- `vault-transit-unseal.yaml`: `autoRotate: false` (unchanged), `rotationPeriod: "720h"`,
  `rotationGracePeriod: "1h"`. Rewrite the comment at :57-64 to say scheduled rotation is turned on in
  the closing PR.
- `vault-config-operator/release.yaml`: add the Reloader annotation
  `secret.reloader.stakater.com/reload: vault-admin-token` on the Deployment, plus a rolling update with
  `maxUnavailable: 1, maxSurge: 0`. Required anti-affinity on 2 replicas can't place a surge pod. Use
  chart values if v0.8.34 exposes them, otherwise a `postRenderers` patch.
- `setup-jobs/vault-admin-setup.yaml`: delete the token mint-and-store block (:91-107). Keep the policy
  and role setup.
- `token-rotation-alerts.yaml`. All alerts are warnings. Each description names the cause and the next
  command.
  - delete `VaultTokenRotationFailed`;
  - `VaultAdminTokenObservationStale`:
    `vault_admin_token_last_observation_timestamp_seconds > 0 and time() - vault_admin_token_last_observation_timestamp_seconds > 900`,
    for 5m. The loop or the
    lookup has stopped, so the TTL and creation gauges are frozen;
  - `VaultAdminTokenRotationOverdue`:
    `vault_admin_token_created_timestamp_seconds > 0 and time() - vault_admin_token_created_timestamp_seconds > vault_admin_token_rotation_period_seconds + 48*3600 and on() vault_admin_token_auto_rotate == 1`,
    for 1h;
  - `VaultAdminTokenRotationFailing`:
    `increase(vault_admin_token_rotations_total{result="failure"}[1h]) > 0`;
  - `VaultAdminTokenRevokeStuck`:
    `vault_admin_token_oldest_due_revoke_timestamp_seconds > 0 and time() - vault_admin_token_oldest_due_revoke_timestamp_seconds > 3600`;
  - `VaultAdminTokenMintUnresolved`: `vault_admin_token_unresolved_mints > 0`, for 30m;
  - `VaultAdminTokenLedgerFull`: `vault_admin_token_ledger_entries >= 16`;
  - `VaultAdminTokenRetiredCredentialLive`:
    `increase(vault_admin_token_revoke_skipped_current_total[1h]) > 0`. A ledgered (retired) token is
    in use again, so investigate which path reinstalled it;
  - `VaultAdminTokenTTLAbsent` keeps its `absent()` role for scrape loss only.
- `tests/prometheus/token-rotation.test.yaml`, run by `tests/prometheus/run.sh` through promtool:
  - lookup failures while the metrics endpoint stays healthy (observation frozen while `up` is 1) →
    `ObservationStale` fires, and `Overdue` fires once the frozen creation time is old enough;
  - `auto_rotate=0` with an old creation time → `Overdue` stays silent;
  - a stuck revocation (oldest due 2h ago) → `RevokeStuck` fires;
  - healthy (observation fresh, nothing due) → nothing fires;
  - one failure increment → `RotationFailing` fires;
  - every gauge at 0 (a fresh operator start) → `Overdue` and `ObservationStale` stay silent;
  - `unresolved_mints=1` for 30m → `MintUnresolved` fires;
  - `ledger_entries=16` → `LedgerFull` fires;
  - one `revoke_skipped_current_total` increment → `RetiredCredentialLive` fires.

  That's 9 cases in total.
- Docs:
  - `docs/operations/vault-token-rotation.md`:
    - the lifecycle and the ledger;
    - **forced rotation**:
      `kubectl --context homelab -n vault annotate secret vault-admin-token vault.homelab.io/rotate-now=true --overwrite`,
      picked up within 30s;
    - **incident response**: `rotate-now=immediate`, then verify that `lookup-accessor` on the old
      accessor answers invalid. Rotation alone is not containment: the holder could have minted orphan
      tokens. Find every token created by the compromised accessor in the Vault audit log
      (`vault-audit-setup`'s device) and revoke each one;
    - **cancel a revocation** (`cancel-revoke=<accessor>`), with the warning that it leaves a valid
      credential;
    - bootstrap recover/seed;
    - the one-writer invariant;
    - the 168h caveat;
    - removal of the Kyverno sections and issue 2.
  - Fix `vault-operations.md:71`.
  - Fix the pointer at `monitor-vault-auth-alerts.yaml:119`.
- `amont agents-md`, staged into the same commit.

### Phase 5 — first rotation by hand, then the closing PR — homelab
1. After the merge and the Flux reconcile, check that:
   - every PKI role still reads `generate_lease=false` and no `pki-istio-*` role has appeared. If any
     shows true, stop before `rotate-now`;
   - the operator runs 2.8.0;
   - the Reloader annotation and strategy are on the live `vault-config-operator` Deployment;
   - `vault-admin-setup` ran once after the merge (the Job is `force: enabled`, so a script change
     recreates it): its log has no `Creating vault-admin token`, and it leaves `token-accessor` unchanged.
2. Force the first rotation with `rotate-now`. Observe every row of Verification through the revoke.
3. **Closing PR** (`work.one-implementation-pr-per-repo-per-plan` allows one): `autoRotate: true`. The
   creation time was reset at step 2, so the first scheduled rotation lands about 30 days later.

Rollback trigger: any Vault 403 in the `vault-config-operator` logs, or `VaultAdminTokenRotationFailing`.
- Before the old entry's `notBefore`: `cancel-revoke=<old accessor>`, and keep `autoRotate: false`.
- After the revoke, the old token cannot be restored. Restart the consumer instead
  (`kubectl --context homelab -n vault-config-operator rollout restart deploy/vault-config-operator`).

## The transit token (`vault-transit-token`) — checked, not in scope

- Live lookup on the NAS Vault: orphan, periodic **720h**, policy `transit-unseal` only, issued
  **2026-10-06**, renewed by homelab Vault's seal while it runs.
- Rotation exists, done by hand: `bootstrap recover homelab transit-token --rotate --yes`. It mints the
  token on the NAS under the ADR-0021 session-root flow (`secrets.nas-vault-root`), rolls the 3 Vault
  pods one at a time with a quorum check, and revokes the old token only once every pod and the operator
  run on the new one. Vault reads the token from env (`vault.yaml:71-74`) and Flux substitutes it
  (`homelab-platform-foundation.yaml:30`), so a rotation is necessarily a rolling restart of Vault.
- Automating it would put NAS token-creation rights in the homelab cluster and let the operator roll
  Vault. That widens the blast radius more than rotation lowers it, so it stays manual: on suspected
  leak, or yearly.

## Rollback

- `autoRotate: false` stops scheduled rotation. `rotate-now` still works, and the ledger still drains.
  To keep an old token, `cancel-revoke` it before its `notBefore`.
- Chart back to 2.6.2, in **one commit** that also removes `rotationGracePeriod` from
  `vault-transit-unseal.yaml`. The HelmRelease runs `crds: CreateReplace`, so the old CRD comes back
  without that field. Check that the Kustomization holding the VTU CR is Ready after the downgrade.
- 2.6.2 ignores the ledger, so **ledgered accessors stop being revoked**. Before downgrading, drain the
  ledger: wait for `vault_admin_token_revocations_pending == 0`, or revoke each ledgered accessor by
  hand and verify it. 168h expiry is no safeguard for a token someone else may renew.
- The token in the Secret stays valid and is renewed. The removed `vault-admin-setup` block isn't needed
  for rollback.

## Verification

| Driven | Expected | Observed |
|---|---|---|
| `vault read <mount>/roles/<role>` for every PKI mount, before `rotate-now` | all `generate_lease=false` (2026-10-07: `pki/client-cert` false, `pki-istio-*` no roles) | |
| `make test lint` (operator) | green; each `rotate_test.go` case fails when its recorded-call or ledger assertion is inverted | |
| `tests/prometheus/run.sh` (homelab) | the 9 token-rotation rule tests pass; each fails when its expected alert is removed | |
| Operator from the branch vs `vault server -dev`, token older than `rotationPeriod: 1m`, grace 1m, 3 pod passes | exactly 1 `create`; old accessor revoked 1 min later and `lookup-accessor` invalid; 0 renewal conflicts | |
| Same, a proxy in front of the apiserver that holds the swap PUT 5s, then forwards, against the 2s per-write deadline | the swap is uncertain, then commits late; the fence conflicts; the re-read sees the minted token; not revoked; no `unresolved` left | |
| Fault matrix (dev Vault): a crash, and separately an uncertain result, injected after the ledger write, the swap and the fence | each run ends with the minted token installed xor revoked-and-verified; the ledger empty or scheduled-only | |
| 1h of forced swap failures (proxy returns 500 only on the PUT that changes `token`, so the ledger write passes) | 6 mints at about t=0/1/3/7/15/31 min; each fence succeeds; every minted accessor's `lookup-accessor` answers invalid; `ledger_entries` back to 0; `RotationFailing` fires | |
| Same, `autoRotate=false` + `rotate-now`; then `enabled: false` + `rotate-now` | 1 swap and `TokenRotated`; then `TokenRotationSkipped` and no `create` | |
| homelab after merge: Deployment annotations; `vault-admin-setup` Job log; `token-accessor` | reload annotation present; one Job run with no `Creating vault-admin token`; accessor unchanged | |
| homelab: forced `rotate-now` | `TokenRotated` event; ledger holds 1 `superseded` entry; new token `orphan: true`, period 168h | |
| hash of `.data.token` in the 7 reflected copies | all equal the source within 1 min | |
| `vault-config-operator` pods | restarted by Reloader before the entry's `notBefore`; ≥1 Ready throughout; 0 Vault 403 for 1h after the revoke | |
| `kubectl create job --from=cronjob/pki-istio-verify` after the revoke | Complete | |
| `vault token lookup -accessor <old>` after `notBefore` + 30s | `invalid accessor`; ledger empty | |
| Prometheus | creation timestamp ≈ now; observation fresh; `revocations_pending` 1 → 0; `rotations_total{result="success"}` +1; no alert | |

## Decision log

- Restarts go through Reloader rather than the operator: there's no new operator RBAC, and Reloader is
  already the cluster's mechanism.
- Fixed grace of 1h before revoke rather than consumer readiness tracking. Reloader restarts in seconds.
  Jobs are **alerted at 2 min, not bounded** (`TokenJobTakingTooLong` covers `vault`/`nas-integration`
  only). The first rotation is watched by a person (Phase 5).
- `revoke-accessor` takes the token tree, so the invariant is: the operator is the only automated writer,
  and the admin token mints only orphans. `vault-admin-setup`'s child-mint is removed for this reason.
- Rotation is turned on by a closing PR, after a forced first rotation: the live token is already past
  720h, and Flux doesn't order the Reloader annotation before the operator.
- `rotate-now` overrides `autoRotate=false`, but not `enabled: false` or `strategy: external`, which
  mean the operator does not own the credential.
- **A ledger replaces the single pending slot** (review by the person, 2026-10-08). A single slot forced
  "delete it to rotate again", which abandoned a possibly compromised token. The ledger also gives
  unresolved mints a durable home.
- **Write outcomes are classified, and an uncertain swap is resolved by a RV fence before any revoke.**
  A timed-out write can still commit after a verification read (apimachinery timeout semantics).
- **Revocation is verified** with `lookup-accessor`, and the current accessor is never revoked,
  whatever path put it back.
- **Alerts key on timestamps**, plus an observation-staleness alert. A registered gauge keeps its last
  value when lookups fail, so `absent()` alone never fires.
- Rotation is hygiene, not containment of an active compromise: the runbook's incident procedure covers
  orphan tokens minted by the holder.
- The transit token stays on manual rotation (section above).

<!-- panel: repos=vault-transit-unseal-operator,homelab reviewers=backend,architect,lang:go,lang:python,lang:typescript,platform,po,tui,unix body-sha=0416f8fdd071 -->
