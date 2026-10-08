# Reviews — Real rotation of the Vault admin token

## Full reviews (reference)

- **python**, approve (37k, 26 s): no Python is touched. It lists the bootstrap recover and seed commands as a writer and a reader, and adds a test where the Secret is replaced while a revoke is pending. Folded in.
- **tui**, approve-with-changes (31k, 27 s): it asks for runbook commands with `--context homelab -n vault`, an exact abort command that clears both annotations, a `TokenRotationSkipped` event naming the reason, alert descriptions that give the next command, and a step-naming `TokenRotationFailed` message. All folded in.
- **po**, approve-with-changes (37k, 32 s):
  - the first rotation would fire unsupervised at rollout → rotation ships off and is forced by hand;
  - there was no signal for vault-config-operator → added a rollback trigger and Verification rows;
  - a failure alert was missing → added `VaultAdminTokenRotationFailing`;
  - the claim that Jobs are bounded was wrong → reworded.
- **lang:typescript**, approve-with-changes (42k, 38 s): renewal read a stale cache after the swap (it now reads through the APIReader); the docs pointers `vault-operations.md:71` and `monitor-vault-auth-alerts.yaml:119` needed fixing; the age gauge needed an absent check; the Jobs wording needed fixing. All folded in.
- **lang:go**, approve-with-changes (59k, 57 s):
  - there was no fake-Vault pattern to reuse → added an `httptest` fake and `interceptor.Funcs`;
  - the plan had no APIReader field → wired one from `mgr.GetAPIReader()`;
  - the mint posts to `auth/token/create`, not `create-orphan` → the capability is re-checked;
  - a non-conflict `Update` error could revoke an installed token → the Secret is re-read first;
  - the counter labels started missing → initialised to 0;
  - `rotate-now` is picked up on the 30s requeue → noted in the runbook.
- **architect**, approve-with-changes (46k, 62 s): `vault-admin-setup` was a second writer minting a child token → its mint block is removed. The rollout is split, the fake Vault and the APIReader were missing, and the mint helper now holds only the request while each caller writes its own annotations. All folded in.
- **unix**, approve-with-changes (38k, 49 s): the first rotation on deploy, ordering the Reloader annotation first, `rotate-now` overriding `autoRotate`, the Overdue alert firing during a rollback (now gated by an `auto_rotate` gauge), namespaced commands, and updating `token-accessor` on swap. All folded in.
- **platform**, approve-with-changes (50k, 72 s):
  - the second writer and the split rollout (shared with the architect);
  - required anti-affinity can't place a surge pod → `maxUnavailable: 1, maxSurge: 0`;
  - 2.7.0's `delete-pods` RBAC → named in the Non-goals.
- **backend**:
  - round 1, approve-with-changes (50k, 69 s): rollout order; the revoke takes child tokens and leases; a failure alert; renewal reading a stale cache; Verification rows.
  - round 2, approve-with-changes (53k, 44 s): every round-1 finding resolved; the `vault-admin-setup` run that the merge itself starts; "only automated writer" wording; evidence for PKI leases; the period gauge.
  - delta 1, approve-with-changes (63k, 79 s): the PKI check had no gate → it is now a live fact (`pki-istio-*` mounts have no roles; client-cert has `generate_lease=false`) plus a Phase 5 gate.
  - delta 2, approve (66k, 120 s): bound sha `5eeb0167`.
  - delta 3, a fresh agent so the hook could record it, approve-with-changes (36k, 29 s): a downgrade to 2.6.2 with `crds: CreateReplace` drops `rotationGracePeriod` → the rollback is one commit that also removes the field; after the revoke the old token can't be restored → restart the consumer instead.
  - delta 4, approve (37k, 38 s): bound sha `42d6caa3`. Its low finding, naming the VTU Kustomization in the rollback check, is deferred to the runbook in Phase 4.
- **The person's review (2026-10-08), request changes, with five gaps:**
  1. revoking after a failed write and a re-read is unsafe;
  2. backup recovery could restore the token awaiting revocation;
  3. forced rotation could abandon a compromised token;
  4. `enabled` and `strategy` were ignored;
  5. `absent()` can't see a stalled loop.

  Answered with the revocation ledger, write classification and a fence, a never-revoke-current guard
  with verified revokes, cancel versus expedite, gating, and timestamp gauges with promtool tests.
- **backend, delta 5**, approve-with-changes (72k, 140 s): `swapRV` couldn't be stored, so the fence now
  uses the RV it just read. Entries are tied by `swapID`, and the old entry is decided by the current
  accessor. Revokes are gated on a successful lookup. Added the mint-loop gates, `revoke-self` on an
  uncertain ledger write, a real per-write deadline in the proxy test, and zero-guards.
- **backend, delta 6**, approve-with-changes (43k, 48 s): the fence and the revoke-on-uncertain logic
  are sound. Landed is now tested by the entry's absence, a token change stops the pass, the failure row
  is aimed at the right path, and there are 9 promtool cases plus the untracked-orphan note.
- **backend, delta 7**, approve (28k, 26 s): bound sha `0416f8fd`.
