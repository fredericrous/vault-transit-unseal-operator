package secrets

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// HashRecoveryShare returns the hex-encoded SHA-256 of a recovery
// share's bytes. We intentionally use a plain cryptographic hash
// rather than an HMAC: the goal is integrity detection (did the
// Secret value change since we wrote it?), not authentication.
// The hash is stored on the VaultTransitUnseal CR status and is
// recomputed from the in-cluster Secret on every reconcile.
//
// Whitespace can sneak into Secret data via `kubectl edit` or hand
// edits — trim trailing newlines before hashing so the same logical
// value with/without a stray '\n' compares equal.
func HashRecoveryShare(share []byte) string {
	// Trim trailing whitespace bytes (newline, carriage return,
	// space, tab) — Vault accepts shares with or without them, so
	// they shouldn't trigger a drift alert.
	for len(share) > 0 {
		last := share[len(share)-1]
		if last != '\n' && last != '\r' && last != ' ' && last != '\t' {
			break
		}
		share = share[:len(share)-1]
	}
	sum := sha256.Sum256(share)
	return hex.EncodeToString(sum[:])
}

// DriftCheckResult captures the outcome of a drift comparison.
type DriftCheckResult struct {
	// Checked is true if both the expected hash and the in-cluster
	// Secret value were available. If false, drift detection was
	// skipped (e.g. status hash not yet recorded after fresh init).
	Checked bool

	// Drifted is true when expected != observed. Only meaningful
	// when Checked is true.
	Drifted bool

	// ExpectedHash is the hash recorded on the CR status at init.
	ExpectedHash string

	// ObservedHash is the hash recomputed from the Secret's
	// current recovery-key-0 bytes.
	ObservedHash string
}

// CheckRecoveryKeyDrift reads the in-cluster recovery-keys Secret,
// hashes recovery-key-0, and compares to the canonical hash on the
// CR status. Returns Checked=false (and no error) when either side
// of the comparison is unavailable — the caller should treat that
// as "not yet observable" rather than "drift detected", so a fresh
// install or post-restore reconcile doesn't spuriously fire the
// alert before StoreSecrets has populated the status.
func CheckRecoveryKeyDrift(ctx context.Context, c client.Client, vtu *vaultv1alpha1.VaultTransitUnseal) (DriftCheckResult, error) {
	res := DriftCheckResult{
		ExpectedHash: vtu.Status.RecoveryKeysHash,
	}

	// Nothing to compare against yet (fresh install, pre-StoreSecrets,
	// or a restored CR whose status was lost).
	if res.ExpectedHash == "" {
		return res, nil
	}

	// Recovery keys not stored in Kubernetes (operator config says
	// they live outside the cluster) — there's no in-cluster value
	// to drift, so skip.
	if vtu.Spec.Initialization.SecretNames.RecoveryKeys == "" ||
		!vtu.Spec.Initialization.SecretNames.StoreRecoveryKeys {
		return res, nil
	}

	secret := &corev1.Secret{}
	err := c.Get(ctx, client.ObjectKey{
		Namespace: vtu.Spec.VaultPod.Namespace,
		Name:      vtu.Spec.Initialization.SecretNames.RecoveryKeys,
	}, secret)
	if err != nil {
		if apierrors.IsNotFound(err) {
			// Secret has been deleted — that's its own kind of
			// drift, but the missing-secrets verifier handles it
			// separately. Skip here to avoid duplicate alerting.
			return res, nil
		}
		return res, fmt.Errorf("get recovery-keys secret: %w", err)
	}

	share, ok := secret.Data["recovery-key-0"]
	if !ok || len(share) == 0 {
		// Same as IsNotFound: handled by the missing-secrets path.
		return res, nil
	}

	res.ObservedHash = HashRecoveryShare(share)
	res.Checked = true
	res.Drifted = res.ObservedHash != res.ExpectedHash
	return res, nil
}
