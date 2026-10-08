package token

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// Annotations on the admin Secret that drive and record rotation.
const (
	// ledgerAnnotation holds the revoke ledger: every admin token the
	// operator has retired or minted and not yet seen to its end.
	ledgerAnnotation = "vault.homelab.io/revoke-ledger"
	// rotateNowAnnotation forces a rotation. "true" schedules the old
	// token's revocation after the grace period; "immediate" revokes it
	// on the next pass. It is removed by the swap that honours it.
	rotateNowAnnotation = "vault.homelab.io/rotate-now"
	// cancelRevokeAnnotation names one scheduled accessor to drop from the
	// ledger WITHOUT revoking it, leaving that credential valid.
	cancelRevokeAnnotation = "vault.homelab.io/cancel-revoke"
	// fenceAnnotation is written only to move the Secret past a
	// resourceVersion, so a swap preconditioned on it can never land.
	fenceAnnotation = "vault.homelab.io/rotation-fence"
	// backoffAnnotation records when minting may resume after a failure.
	backoffAnnotation = "vault.homelab.io/rotation-backoff-until"
)

// Ledger entry reasons and states.
const (
	reasonSuperseded     = "superseded"
	reasonForced         = "forced"
	reasonUnresolvedMint = "unresolved-mint"

	stateScheduled  = "scheduled"
	stateUnresolved = "unresolved"
)

// ledgerCap bounds the ledger so a stuck revocation or a failing swap
// cannot grow the annotation towards the 256 KiB metadata limit. A
// rotation adds two entries, so minting stops at ledgerCap-1.
const ledgerCap = 16

// writeTimeout bounds every Secret write in the rotation path, so a
// write that hangs becomes an uncertain outcome instead of a stuck pass.
const writeTimeout = 2 * time.Second

// LedgerEntry is one admin token the operator must still see to its end:
// a retired token to revoke at NotBefore, or a minted token whose swap
// into the Secret has an unknown outcome.
type LedgerEntry struct {
	Accessor  string    `json:"accessor"`
	NotBefore time.Time `json:"notBefore"`
	Reason    string    `json:"reason"`
	State     string    `json:"state"`
	// SwapID ties together the two entries one rotation writes: the
	// minted token's and the token it replaces.
	SwapID string `json:"swapID"`
}

// readLedger parses the Secret's ledger. A missing annotation is an
// empty ledger; a malformed one is an error, and the caller must not act
// on a ledger it cannot read.
func readLedger(secret *corev1.Secret) ([]LedgerEntry, error) {
	raw := strings.TrimSpace(secret.Annotations[ledgerAnnotation])
	if raw == "" {
		return nil, nil
	}
	var entries []LedgerEntry
	if err := json.Unmarshal([]byte(raw), &entries); err != nil {
		return nil, fmt.Errorf("parse %s: %w", ledgerAnnotation, err)
	}
	return entries, nil
}

// writeLedger stores entries on the Secret object (not the apiserver).
func writeLedger(secret *corev1.Secret, entries []LedgerEntry) {
	if secret.Annotations == nil {
		secret.Annotations = map[string]string{}
	}
	if len(entries) == 0 {
		delete(secret.Annotations, ledgerAnnotation)
		return
	}
	raw, err := json.Marshal(entries)
	if err != nil {
		// LedgerEntry holds only strings and times; Marshal cannot fail.
		panic(fmt.Sprintf("marshal revoke ledger: %v", err))
	}
	secret.Annotations[ledgerAnnotation] = string(raw)
}

// ledgerHasAccessor reports whether any entry retires or tracks accessor.
func ledgerHasAccessor(entries []LedgerEntry, accessor string) bool {
	for _, e := range entries {
		if e.Accessor == accessor {
			return true
		}
	}
	return false
}

// newSwapID returns a random identifier for one rotation's entries.
func newSwapID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand does not fail on supported platforms.
		panic(fmt.Sprintf("read random swap id: %v", err))
	}
	return hex.EncodeToString(b)
}

// writeDefinitelyRejected reports whether a failed write is known not to
// have landed and never to land. Everything else — timeouts, 5xx, 429,
// a dropped connection — may still be applied by the apiserver later.
func writeDefinitelyRejected(err error) bool {
	return apierrors.IsConflict(err) ||
		apierrors.IsInvalid(err) ||
		apierrors.IsBadRequest(err) ||
		apierrors.IsForbidden(err) ||
		apierrors.IsUnauthorized(err) ||
		apierrors.IsNotFound(err) ||
		apierrors.IsRequestEntityTooLargeError(err)
}

// writeClass names a failed write's outcome for events and logs.
func writeClass(err error) string {
	if writeDefinitelyRejected(err) {
		return "definite"
	}
	return "uncertain"
}

// isInvalidAccessor reports whether Vault answered that an accessor does
// not exist: the token behind it is revoked or expired.
func isInvalidAccessor(err error) bool {
	var re *vaultapi.ResponseError
	if !errors.As(err, &re) {
		return false
	}
	if re.StatusCode != http.StatusBadRequest {
		return false
	}
	for _, msg := range re.Errors {
		if strings.Contains(msg, "invalid accessor") {
			return true
		}
	}
	return false
}

// revokeAndVerify revokes accessor with the client's current token, then
// confirms Vault no longer knows it. Only a confirmed revocation returns
// nil: the ledger entry may be dropped only then.
func revokeAndVerify(vaultClient *vaultapi.Client, accessor string) error {
	if err := vaultClient.Auth().Token().RevokeAccessor(accessor); err != nil && !isInvalidAccessor(err) {
		return fmt.Errorf("revoke-accessor: %w", err)
	}
	_, err := vaultClient.Auth().Token().LookupAccessor(accessor)
	if err == nil {
		return fmt.Errorf("accessor still valid after revoke")
	}
	if !isInvalidAccessor(err) {
		return fmt.Errorf("verify revocation: %w", err)
	}
	return nil
}
