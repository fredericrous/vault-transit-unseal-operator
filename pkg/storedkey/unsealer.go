package storedkey

import (
	"context"
	"errors"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"
)

// ErrNoKeys is returned when the Secret exists but carries no usable share.
var ErrNoKeys = errors.New("no unseal key shares found in secret")

// Result summarises what a call to Unseal achieved. Deliberately carries no
// key material — it is safe to log, event, and put on a status condition.
type Result struct {
	// Sealed is Vault's seal state after the last accepted share.
	Sealed bool

	// Progress is how many shares Vault has accepted toward the current
	// unseal, as Vault itself reports it. Resets to 0 once Vault unseals.
	Progress int

	// Threshold is how many shares Vault requires (the "t" of t-of-n).
	Threshold int

	// SharesSubmitted is how many shares this call handed to Vault.
	SharesSubmitted int
}

// Unseal submits shares to sys/unseal until Vault reports itself unsealed or
// the shares run out.
//
// Vault's unseal is stateful: each accepted share bumps a server-side progress
// counter and only the threshold-th share actually opens the vault. So a
// partial attempt leaves the server mid-unseal. If a share is rejected after
// others were accepted, this function resets that progress before returning,
// so the next reconcile starts from a clean slate instead of stacking a second
// partial attempt on top of a stale one.
//
// The share itself never appears in the returned error: Vault's own error text
// echoes the response, not the request body, and nothing here formats a key.
func Unseal(ctx context.Context, api *vaultapi.Client, keys []string) (*Result, error) {
	if api == nil {
		return nil, errors.New("unseal: vault client is nil")
	}
	if len(keys) == 0 {
		return nil, ErrNoKeys
	}

	result := &Result{Sealed: true}

	for i, key := range keys {
		status, err := api.Sys().UnsealWithContext(ctx, key)
		if err != nil {
			if result.SharesSubmitted > 0 {
				// Abandon the half-finished attempt; best effort, the
				// next reconcile retries either way.
				_, _ = api.Sys().ResetUnsealProcessWithContext(ctx)
				result.Progress = 0
			}
			return result, fmt.Errorf("submitting share %d of %d to sys/unseal: %w", i+1, len(keys), err)
		}

		result.SharesSubmitted++
		result.Sealed = status.Sealed
		result.Progress = status.Progress
		result.Threshold = status.T

		if !status.Sealed {
			return result, nil
		}
	}

	return result, fmt.Errorf(
		"vault still sealed after submitting %d share(s): %d of %d accepted — the secret holds fewer shares than the unseal threshold",
		result.SharesSubmitted, result.Progress, result.Threshold)
}
