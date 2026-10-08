package token

import (
	"context"
	"fmt"
	"strings"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/metrics"
)

// Rotation backoff: the first failure waits backoffBase, each further one
// doubles it, up to backoffMax.
const (
	backoffBase = time.Minute
	backoffMax  = time.Hour
)

type backoffState struct {
	failures int
	until    time.Time
}

// RotateIfDue rotates the admin token and drives its revoke ledger.
//
// Each call looks the current token up first and acts only when Vault
// answered: the current accessor that lookup returns is the one thing a
// revoke decision trusts. Then, in order, it
//
//  1. resolves every minted token whose swap into the Secret has an
//     unknown outcome, by fencing the Secret's resourceVersion;
//  2. revokes each retired token whose grace period is over, verifies the
//     revocation, and never revokes the token the Secret holds;
//  3. mints a replacement when rotation is due, records both tokens in the
//     ledger, and only then swaps the Secret.
//
// Nothing revokes a minted token while its swap outcome is unknown, and
// the ledger drains whatever autoRotate, enabled or strategy say: its
// entries are credentials the operator already retired.
func (m *SimpleManager) RotateIfDue(
	ctx context.Context,
	vtu *vaultv1alpha1.VaultTransitUnseal,
	vaultClient *vaultapi.Client,
) error {
	tm := vtu.Spec.TokenManagement
	if tm == nil {
		return nil
	}

	secret, err := m.getAdminSecret(ctx, vtu)
	if apierrors.IsNotFound(err) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("get admin token secret: %w", err)
	}
	token := strings.TrimSpace(string(secret.Data["token"]))
	if token == "" || m.isPlaceholderToken(secret, token) {
		return nil
	}

	original := vaultClient.Token()
	defer vaultClient.SetToken(original)
	vaultClient.SetToken(token)

	info, err := vaultClient.Auth().Token().LookupSelf()
	if err != nil || info == nil || info.Data == nil {
		// Invalid is self-heal's job; a timeout leaves the current
		// accessor unknown, so no revoke decision can be made.
		reason := "empty lookup response"
		if err != nil {
			reason = err.Error()
		}
		m.Log.Info("Admin token lookup failed; rotation and revocation wait for the next pass", "reason", reason)
		return nil
	}
	accessor, _ := info.Data["accessor"].(string)
	if accessor == "" {
		return nil
	}
	createdSec, err := numberSeconds(info.Data["creation_time"])
	if err != nil {
		return fmt.Errorf("parse creation_time from token lookup: %w", err)
	}
	period, err := time.ParseDuration(orDefault(tm.RotationPeriod, "720h"))
	if err != nil {
		return fmt.Errorf("parse TokenManagement.RotationPeriod %q: %w", tm.RotationPeriod, err)
	}
	now := m.now()
	recorder := metrics.NewRecorder()
	recorder.RecordAdminTokenObservation(metrics.AdminTokenObservation{
		Created:        time.Unix(createdSec, 0),
		ObservedAt:     now,
		RotationPeriod: period,
		AutoRotate:     tm.AutoRotate,
	})

	ledger, err := readLedger(secret)
	if err != nil {
		m.event(vtu, corev1.EventTypeWarning, "TokenRotationFailed",
			"rotation stopped at ledger: %v; no token is minted or revoked until the annotation %s is valid JSON",
			err, ledgerAnnotation)
		recorder.RecordAdminTokenRotation(false)
		return err
	}

	p := &rotationPass{
		m:        m,
		ctx:      ctx,
		vtu:      vtu,
		vault:    vaultClient,
		recorder: recorder,
		secret:   secret,
		ledger:   ledger,
		token:    token,
		accessor: accessor,
		now:      now,
	}
	defer p.publishLedger()

	if stop := p.resolveUnresolved(); stop {
		return nil
	}
	p.revokeDue()
	return p.rotateIfDue(time.Unix(createdSec, 0), period)
}

// rotationPass is one RotateIfDue call's view: the Secret as last read or
// written, its ledger, and the token and accessor the lookup confirmed.
type rotationPass struct {
	m        *SimpleManager
	ctx      context.Context
	vtu      *vaultv1alpha1.VaultTransitUnseal
	vault    *vaultapi.Client
	recorder *metrics.Recorder

	secret *corev1.Secret
	ledger []LedgerEntry

	token    string
	accessor string
	now      time.Time
}

type fenceOutcome int

const (
	// fenceUnresolved: the swap's outcome is still unknown.
	fenceUnresolved fenceOutcome = iota
	// fenceLanded: the minted entry is gone from the Secret, so the swap
	// that removes it landed (or another pass already resolved it).
	fenceLanded
	// fenceNotLanded: the Secret moved past the resourceVersion the swap
	// was preconditioned on, so the swap never landed and never will.
	fenceNotLanded
)

// resolveUnresolved fences every minted token whose swap outcome is
// unknown. It returns true when the pass must stop: the Secret no longer
// holds the token this pass looked up, so its accessor is stale.
func (p *rotationPass) resolveUnresolved() bool {
	var swaps []string
	for _, e := range p.ledger {
		if e.Reason == reasonUnresolvedMint && e.State == stateUnresolved {
			swaps = append(swaps, e.SwapID)
		}
	}
	for _, swapID := range swaps {
		if stop := p.resolveSwap(swapID); stop {
			return true
		}
	}
	return false
}

// resolveSwap settles one swap. Landed: the minted token is installed and
// the pass stops. Not landed: the minted token is scheduled for revocation
// now, and the token it would have replaced leaves the ledger only if it
// is still the live one.
func (p *rotationPass) resolveSwap(swapID string) bool {
	outcome, fresh := p.fence(swapID)
	switch outcome {
	case fenceUnresolved:
		p.m.event(p.vtu, corev1.EventTypeWarning, "TokenRotationFailed",
			"rotation stopped at fence for swap %s: outcome still unknown; the minted token is kept until it resolves",
			swapID)
		p.recorder.RecordAdminTokenRotation(false)
		p.skipped("previous swap %s is unresolved; it is settled before any new mint", swapID)
		return true
	case fenceLanded:
		return true
	}

	if strings.TrimSpace(string(fresh.Data["token"])) != p.token {
		// Another writer changed the token since the lookup; the next
		// pass looks the new one up and fences again.
		return true
	}
	entries, err := readLedger(fresh)
	if err != nil {
		p.m.Log.Info("Revoke ledger unreadable while settling a swap; retrying next pass", "swap", swapID, "error", err.Error())
		return true
	}
	kept := entries[:0:0]
	for _, e := range entries {
		if e.SwapID == swapID {
			if e.Reason == reasonUnresolvedMint {
				e.State = stateScheduled
				e.NotBefore = p.now
			} else if e.Accessor == p.accessor {
				// The swap never replaced it: it is the live token.
				continue
			}
		}
		kept = append(kept, e)
	}
	writeLedger(fresh, kept)
	if err := p.update(fresh); err != nil {
		// The entry is still unresolved in the Secret; the next pass
		// fences it again and reaches the same answer.
		p.m.Log.Info("Could not record a resolved swap; retrying next pass",
			"swap", swapID, "class", writeClass(err), "error", err.Error())
		return true
	}
	p.secret, p.ledger = fresh, kept
	return false
}

// fence settles whether the swap of swapID landed. It needs no stored
// resourceVersion: a write preconditioned on the version just read moves
// the Secret past every earlier version, including the one the swap was
// preconditioned on, and versions never repeat.
func (p *rotationPass) fence(swapID string) (fenceOutcome, *corev1.Secret) {
	for attempt := 0; attempt < 3; attempt++ {
		fresh, err := p.m.getAdminSecret(p.ctx, p.vtu)
		if err != nil {
			p.m.Log.Info("Fence read failed; swap outcome stays unknown", "swap", swapID, "error", err.Error())
			return fenceUnresolved, nil
		}
		entries, err := readLedger(fresh)
		if err != nil {
			p.m.Log.Info("Revoke ledger unreadable at fence; swap outcome stays unknown", "swap", swapID, "error", err.Error())
			return fenceUnresolved, nil
		}
		if !hasUnresolvedMint(entries, swapID) {
			return fenceLanded, fresh
		}
		if fresh.Annotations == nil {
			fresh.Annotations = map[string]string{}
		}
		fresh.Annotations[fenceAnnotation] = p.now.UTC().Format(time.RFC3339Nano)
		err = p.update(fresh)
		if err == nil {
			return fenceNotLanded, fresh
		}
		if !apierrors.IsConflict(err) {
			p.m.Log.Info("Fence write failed; swap outcome stays unknown", "swap", swapID,
				"class", writeClass(err), "error", err.Error())
			return fenceUnresolved, nil
		}
	}
	return fenceUnresolved, nil
}

func hasUnresolvedMint(entries []LedgerEntry, swapID string) bool {
	for _, e := range entries {
		if e.SwapID == swapID && e.Reason == reasonUnresolvedMint && e.State == stateUnresolved {
			return true
		}
	}
	return false
}

// revokeDue applies a cancel-revoke request, revokes every scheduled entry
// whose grace period is over, and keeps the lifecycle annotations truthful,
// in one write.
func (p *rotationPass) revokeDue() {
	fresh := p.secret.DeepCopy()
	changed := false
	entries := append([]LedgerEntry(nil), p.ledger...)

	if cancel := strings.TrimSpace(fresh.Annotations[cancelRevokeAnnotation]); cancel != "" {
		dropped := false
		for i, e := range entries {
			if e.Accessor == cancel && e.State == stateScheduled {
				entries = append(entries[:i], entries[i+1:]...)
				dropped = true
				break
			}
		}
		if dropped {
			p.m.event(p.vtu, corev1.EventTypeWarning, "TokenRevokeCancelled",
				"revocation of accessor %s cancelled by %s: that token stays VALID and is no longer tracked",
				cancel, cancelRevokeAnnotation)
		} else {
			p.m.event(p.vtu, corev1.EventTypeWarning, "TokenRevokeCancelled",
				"%s=%s matches no scheduled revocation; nothing cancelled", cancelRevokeAnnotation, cancel)
		}
		delete(fresh.Annotations, cancelRevokeAnnotation)
		changed = true
	}

	kept := entries[:0:0]
	for _, e := range entries {
		if e.State != stateScheduled || p.now.Before(e.NotBefore) {
			kept = append(kept, e)
			continue
		}
		if e.Accessor == p.accessor {
			p.m.event(p.vtu, corev1.EventTypeWarning, "TokenRevokeSkippedCurrent",
				"accessor %s was retired (%s) but is the token the Secret holds again; not revoked. Find the path that reinstalled it.",
				e.Accessor, e.Reason)
			p.recorder.RecordAdminTokenRevokeSkippedCurrent()
			changed = true
			continue
		}
		if err := revokeAndVerify(p.vault, e.Accessor); err != nil {
			p.m.event(p.vtu, corev1.EventTypeWarning, "TokenRotationFailed",
				"revocation of accessor %s failed at revoke/verify: %v; kept in the ledger and retried", e.Accessor, err)
			p.recorder.RecordAdminTokenRotation(false)
			kept = append(kept, e)
			continue
		}
		p.m.event(p.vtu, corev1.EventTypeNormal, "TokenRevoked",
			"revoked retired admin token accessor %s (%s); lookup-accessor confirms it is gone", e.Accessor, e.Reason)
		changed = true
	}

	if p.applyLifecycleAnnotations(fresh) {
		changed = true
	}
	if !changed {
		return
	}
	writeLedger(fresh, kept)
	if err := p.update(fresh); err != nil {
		// Revocations already done are verified again next pass, where
		// Vault answers invalid accessor and the entries drop.
		p.m.Log.Info("Could not record revocations; retrying next pass",
			"class", writeClass(err), "error", err.Error())
		return
	}
	p.secret, p.ledger = fresh, kept
}

// applyLifecycleAnnotations writes the auto-rotate annotations only while
// scheduled rotation is on, and drops the never-honoured next-rotation.
func (p *rotationPass) applyLifecycleAnnotations(secret *corev1.Secret) bool {
	tm := p.vtu.Spec.TokenManagement
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	before := fmt.Sprint(secret.Annotations["vault.homelab.io/auto-rotate"],
		"|", secret.Annotations["vault.homelab.io/rotation-period"],
		"|", secret.Annotations["vault.homelab.io/next-rotation"])
	if tm.AutoRotate {
		secret.Annotations["vault.homelab.io/auto-rotate"] = "true"
		secret.Annotations["vault.homelab.io/rotation-period"] = tm.RotationPeriod
	} else {
		delete(secret.Annotations, "vault.homelab.io/auto-rotate")
		delete(secret.Annotations, "vault.homelab.io/rotation-period")
	}
	delete(secret.Annotations, "vault.homelab.io/next-rotation")
	after := fmt.Sprint(secret.Annotations["vault.homelab.io/auto-rotate"],
		"|", secret.Annotations["vault.homelab.io/rotation-period"],
		"|", secret.Annotations["vault.homelab.io/next-rotation"])
	return before != after
}

// rotateIfDue mints and swaps when the token is old enough or a rotation
// is forced, and the operator owns the credential.
func (p *rotationPass) rotateIfDue(created time.Time, period time.Duration) error {
	tm := p.vtu.Spec.TokenManagement
	rotateNow := strings.TrimSpace(p.secret.Annotations[rotateNowAnnotation])
	forced := rotateNow == "true" || rotateNow == "immediate"
	if rotateNow != "" && !forced {
		p.m.event(p.vtu, corev1.EventTypeWarning, "TokenRotationSkipped",
			"%s=%q is not a valid value; use \"true\" or \"immediate\"", rotateNowAnnotation, rotateNow)
	}
	if !forced && (!tm.AutoRotate || p.now.Sub(created) < period) {
		return nil
	}

	// Not owning the credential and backing off are expected states: an
	// event only when someone forced a rotation. A stuck ledger is a
	// fault: an event either way.
	quiet := func(format string, args ...any) error {
		if forced {
			p.skipped(format, args...)
		} else {
			p.m.Log.V(1).Info("Admin token rotation skipped", "reason", fmt.Sprintf(format, args...))
		}
		return nil
	}
	if !tm.Enabled {
		return quiet("token management is disabled (spec.tokenManagement.enabled=false): the operator does not own this credential")
	}
	if tm.Strategy == vaultv1alpha1.TokenStrategyExternal {
		return quiet("token strategy is external: the operator does not own this credential")
	}
	for _, e := range p.ledger {
		if e.State == stateUnresolved {
			p.skipped("previous swap %s is unresolved; it is settled before any new mint", e.SwapID)
			return nil
		}
	}
	if len(p.ledger) >= ledgerFull {
		p.skipped("revoke ledger holds %d entries (minting stops at %d): revocations are not completing", len(p.ledger), ledgerFull)
		return nil
	}
	if until := p.m.backoffUntil(p.vtu, p.secret); p.now.Before(until) {
		return quiet("backing off after a failed rotation until %s", until.UTC().Format(time.RFC3339))
	}

	grace, err := time.ParseDuration(orDefault(tm.RotationGracePeriod, "1h"))
	if err != nil {
		return fmt.Errorf("parse TokenManagement.RotationGracePeriod %q: %w", tm.RotationGracePeriod, err)
	}
	return p.mintAndSwap(rotateNow == "immediate", grace)
}

// mintAndSwap mints the replacement, records both tokens in the ledger,
// and only then swaps the Secret, preconditioned on the ledger write's
// resourceVersion. Every token is recorded before it can be lost.
func (p *rotationPass) mintAndSwap(immediate bool, grace time.Duration) error {
	minted, mintedAccessor, err := mintAdminToken(p.vault, p.vtu, "rotation")
	if err != nil {
		outcome := "no token minted"
		if mintedAccessor != "" {
			outcome = "minted token revoked"
			if rerr := revokeAndVerify(p.vault, mintedAccessor); rerr != nil {
				outcome = fmt.Sprintf("minted token %s NOT revoked (%v); nobody holds it and it expires unrenewed", mintedAccessor, rerr)
			}
		}
		return p.fail("mint", err, outcome)
	}

	swapID := newSwapID()
	old := LedgerEntry{
		Accessor:  p.accessor,
		NotBefore: p.now.Add(grace),
		Reason:    reasonSuperseded,
		State:     stateScheduled,
		SwapID:    swapID,
	}
	if immediate {
		old.NotBefore = p.now
		old.Reason = reasonForced
	}
	mint := LedgerEntry{
		Accessor: mintedAccessor,
		Reason:   reasonUnresolvedMint,
		State:    stateUnresolved,
		SwapID:   swapID,
	}

	withLedger := p.secret.DeepCopy()
	recorded := append(append([]LedgerEntry(nil), p.ledger...), mint, old)
	writeLedger(withLedger, recorded)
	if err := p.update(withLedger); err != nil {
		// The swap is never sent on this path, so the minted token can
		// never be installed: revoking it is safe whatever happened to
		// the ledger write. If that write lands later, the fence settles
		// its entries as not landed.
		outcome := "minted token revoked"
		if rerr := revokeAndVerify(p.vault, mintedAccessor); rerr != nil {
			outcome = fmt.Sprintf("minted token %s NOT revoked (%v); nobody holds it and it expires unrenewed", mintedAccessor, rerr)
		}
		return p.fail(fmt.Sprintf("ledger (%s)", writeClass(err)), err, outcome)
	}
	p.secret, p.ledger = withLedger, recorded

	swapped := withLedger.DeepCopy()
	if swapped.Data == nil {
		swapped.Data = map[string][]byte{}
	}
	swapped.Data["token"] = []byte(minted)
	stamp := p.now.UTC().Format(time.RFC3339)
	swapped.Annotations["vault.homelab.io/token-created"] = stamp
	swapped.Annotations["vault.homelab.io/token-accessor"] = mintedAccessor
	swapped.Annotations["vault.homelab.io/rotated-at"] = stamp
	delete(swapped.Annotations, rotateNowAnnotation)
	delete(swapped.Annotations, backoffAnnotation)
	installed := make([]LedgerEntry, 0, len(recorded)-1)
	for _, e := range recorded {
		if e.SwapID != swapID || e.Reason != reasonUnresolvedMint {
			installed = append(installed, e)
		}
	}
	writeLedger(swapped, installed)

	if err := p.update(swapped); err != nil {
		if writeDefinitelyRejected(err) {
			p.resolveSwap(swapID)
			return p.fail("swap (definite)", err, "minted token scheduled for revocation")
		}
		return p.fail("swap (uncertain)", err, "outcome unknown; minted token kept until a fence settles it")
	}
	p.secret, p.ledger = swapped, installed

	p.backupRotatedToken(minted)
	p.m.clearBackoff(p.vtu)
	p.recorder.RecordAdminTokenRotation(true)
	p.m.event(p.vtu, corev1.EventTypeNormal, "TokenRotated",
		"rotated admin token: accessor %s -> %s; the old token is revoked at %s",
		old.Accessor, mintedAccessor, old.NotBefore.UTC().Format(time.RFC3339))
	return nil
}

// skipped reports a rotation that is due but cannot run now.
func (p *rotationPass) skipped(format string, args ...any) {
	p.m.event(p.vtu, corev1.EventTypeWarning, "TokenRotationSkipped", format, args...)
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

// backupRotatedToken stores the new token in the transit backup. Best
// effort: without it, recovery falls back to Kubernetes-auth self-heal.
func (p *rotationPass) backupRotatedToken(token string) {
	init := p.vtu.Spec.Initialization
	if p.m.AdminTokenBackup == nil || !init.TokenRecovery.Enabled || !init.TokenRecovery.BackupToTransit {
		return
	}
	if err := p.m.AdminTokenBackup.Backup(p.ctx, p.vtu, token); err != nil {
		p.recorder.RecordAdminTokenBackupFailure()
		p.m.event(p.vtu, corev1.EventTypeWarning, "TokenBackupFailed",
			"transit backup of the rotated admin token failed: %v; recovery falls back to Kubernetes-auth self-heal", err)
	}
}

// fail records a failed rotation step and starts or extends the backoff.
func (p *rotationPass) fail(step string, err error, outcome string) error {
	p.recorder.RecordAdminTokenRotation(false)
	until := p.m.extendBackoff(p.vtu, p.now)
	p.m.event(p.vtu, corev1.EventTypeWarning, "TokenRotationFailed",
		"rotation failed at %s: %v; %s. Next attempt after %s",
		step, err, outcome, until.UTC().Format(time.RFC3339))
	p.m.persistBackoff(p.ctx, p.vtu, until, p.token)
	return fmt.Errorf("rotation failed at %s: %w", step, err)
}

// update writes the Secret with its resourceVersion as precondition,
// bounded by writeTimeout.
func (p *rotationPass) update(secret *corev1.Secret) error {
	ctx, cancel := context.WithTimeout(p.ctx, writeTimeout)
	defer cancel()
	return p.m.Update(ctx, secret)
}

// publishLedger exports the ledger's state as it stands after the pass.
func (p *rotationPass) publishLedger() {
	unresolved := 0
	var oldestDue time.Time
	for _, e := range p.ledger {
		if e.State == stateUnresolved {
			unresolved++
			continue
		}
		if !p.now.Before(e.NotBefore) && (oldestDue.IsZero() || e.NotBefore.Before(oldestDue)) {
			oldestDue = e.NotBefore
		}
	}
	p.recorder.RecordAdminTokenLedger(len(p.ledger), unresolved, oldestDue)
}

// getAdminSecret reads the admin Secret, uncached when an APIReader is wired.
func (m *SimpleManager) getAdminSecret(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) (*corev1.Secret, error) {
	secret := &corev1.Secret{}
	err := m.reader().Get(ctx, client.ObjectKey{
		Namespace: vtu.Spec.VaultPod.Namespace,
		Name:      vtu.Spec.Initialization.SecretNames.AdminToken,
	}, secret)
	return secret, err
}

func (m *SimpleManager) now() time.Time {
	if m.clock != nil {
		return m.clock()
	}
	return time.Now()
}

func (m *SimpleManager) event(vtu *vaultv1alpha1.VaultTransitUnseal, eventType, reason, format string, args ...any) {
	m.Log.Info(fmt.Sprintf(format, args...), "event", reason)
	if m.Events != nil {
		m.Events.Eventf(vtu, eventType, reason, format, args...)
	}
}

func backoffKey(vtu *vaultv1alpha1.VaultTransitUnseal) string {
	return vtu.Namespace + "/" + vtu.Name
}

// backoffUntil is the later of the in-memory backoff and the one recorded
// on the Secret by a previous process.
func (m *SimpleManager) backoffUntil(vtu *vaultv1alpha1.VaultTransitUnseal, secret *corev1.Secret) time.Time {
	m.backoffMu.Lock()
	until := m.rotationBackoff[backoffKey(vtu)].until
	m.backoffMu.Unlock()
	if recorded, err := time.Parse(time.RFC3339, secret.Annotations[backoffAnnotation]); err == nil && recorded.After(until) {
		until = recorded
	}
	return until
}

// extendBackoff counts a failure and returns when minting may resume:
// backoffBase after the first, doubling up to backoffMax.
func (m *SimpleManager) extendBackoff(vtu *vaultv1alpha1.VaultTransitUnseal, now time.Time) time.Time {
	m.backoffMu.Lock()
	defer m.backoffMu.Unlock()
	if m.rotationBackoff == nil {
		m.rotationBackoff = map[string]backoffState{}
	}
	state := m.rotationBackoff[backoffKey(vtu)]
	state.failures++
	wait := backoffBase
	for i := 1; i < state.failures && wait < backoffMax; i++ {
		wait *= 2
	}
	if wait > backoffMax {
		wait = backoffMax
	}
	state.until = now.Add(wait)
	m.rotationBackoff[backoffKey(vtu)] = state
	return state.until
}

func (m *SimpleManager) clearBackoff(vtu *vaultv1alpha1.VaultTransitUnseal) {
	m.backoffMu.Lock()
	defer m.backoffMu.Unlock()
	delete(m.rotationBackoff, backoffKey(vtu))
}

// persistBackoff records the backoff on the Secret so a restarted
// operator keeps it. Best effort: the in-memory backoff already holds.
// failedOn is the token the failed pass worked from; if the Secret no
// longer holds it, a swap landed late and there is nothing to back off.
func (m *SimpleManager) persistBackoff(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, until time.Time, failedOn string) {
	secret, err := m.getAdminSecret(ctx, vtu)
	if err != nil {
		m.Log.Info("Could not read the admin Secret to record the rotation backoff", "error", err.Error())
		return
	}
	if strings.TrimSpace(string(secret.Data["token"])) != failedOn {
		return
	}
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	secret.Annotations[backoffAnnotation] = until.UTC().Format(time.RFC3339)
	wctx, cancel := context.WithTimeout(ctx, writeTimeout)
	defer cancel()
	if err := m.Update(wctx, secret); err != nil {
		m.Log.Info("Could not record the rotation backoff; it holds in memory", "class", writeClass(err), "error", err.Error())
	}
}
