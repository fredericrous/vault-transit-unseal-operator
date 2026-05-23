package token

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// renewalThresholdFraction is the fraction of the configured TTL at
// which we trigger renewal. With TTL=168h (7d) and fraction=1/3 we
// renew when the remaining TTL drops below ~56h, i.e. every ~2.3 days.
// Picking 1/3 (rather than 1/2 or 2/3) means we get two renewal
// attempts per TTL window before the token expires — one buffer for a
// transient failure to retry on the next reconcile tick.
const renewalThresholdFraction = 3

// RenewIfNeeded inspects the current admin token's remaining TTL and
// renews it via `vault token renew-self` when it drops below TTL/3.
// It is a no-op when:
//   - TokenManagement is nil or AutoRenew is disabled,
//   - the admin Secret has no token / a placeholder / a token Vault
//     rejects as invalid (caller's ReconcileInitialToken path handles
//     those),
//   - Vault marks the token as non-renewable.
//
// On every successful check it writes observability annotations on the
// admin Secret:
//
//	vault.homelab.io/last-renewal-attempt  -- last time we tried
//	vault.homelab.io/last-renewal-success  -- last time we succeeded
//	vault.homelab.io/last-renewal-error    -- last error (cleared on success)
//	vault.homelab.io/token-expires         -- updated to now + new lease
//
// The Secret update also bumps the Secret's resource_version, which is
// the signal the VaultTokenExpiringSoon Prometheus alert keys off.
func (m *SimpleManager) RenewIfNeeded(
	ctx context.Context,
	vtu *vaultv1alpha1.VaultTransitUnseal,
	vaultClient *vaultapi.Client,
) error {
	if vtu.Spec.TokenManagement == nil || !vtu.Spec.TokenManagement.AutoRenew {
		return nil
	}

	secret := &corev1.Secret{}
	if err := m.Get(ctx, client.ObjectKey{
		Namespace: vtu.Spec.VaultPod.Namespace,
		Name:      vtu.Spec.Initialization.SecretNames.AdminToken,
	}, secret); err != nil {
		if apierrors.IsNotFound(err) {
			// Initial-token path will create it
			return nil
		}
		return fmt.Errorf("get admin token secret: %w", err)
	}

	tokenBytes, ok := secret.Data["token"]
	if !ok || len(tokenBytes) == 0 {
		return nil
	}
	tokenStr := strings.TrimSpace(string(tokenBytes))
	if m.isPlaceholderToken(secret, tokenStr) {
		return nil
	}

	// Use the admin token to inspect itself. SetToken/restore so we
	// don't leak it to other callers of the shared client.
	originalToken := vaultClient.Token()
	defer vaultClient.SetToken(originalToken)
	vaultClient.SetToken(tokenStr)

	info, err := vaultClient.Auth().Token().LookupSelf()
	if err != nil {
		// LookupSelf failure here is recoverable signal — either the
		// token is dead (covered by ReconcileInitialToken's path) or a
		// transient network blip. Don't double-report; let the
		// existing token-status check surface invalid-token state.
		return nil
	}
	if info == nil || info.Data == nil {
		return nil
	}

	ttlRemaining, err := numberSeconds(info.Data["ttl"])
	if err != nil {
		return fmt.Errorf("parse ttl from token lookup: %w", err)
	}
	if renewable, _ := info.Data["renewable"].(bool); !renewable {
		// Periodic / orphan tokens that are non-renewable: nothing to
		// do here; auto-rotate path will mint a new one when needed.
		return nil
	}

	configuredTTL, err := time.ParseDuration(vtu.Spec.TokenManagement.TTL)
	if err != nil {
		return fmt.Errorf("parse TokenManagement.TTL %q: %w", vtu.Spec.TokenManagement.TTL, err)
	}
	threshold := configuredTTL / renewalThresholdFraction
	remaining := time.Duration(ttlRemaining) * time.Second

	if remaining > threshold {
		// Not time yet. Don't touch the Secret — preserve the
		// 48h-no-update signal that the alert depends on.
		return nil
	}

	m.Log.Info("Renewing admin token",
		"remaining", remaining.String(),
		"threshold", threshold.String(),
		"configuredTTL", configuredTTL.String(),
	)

	renewResp, renewErr := vaultClient.Auth().Token().RenewSelf(int(configuredTTL.Seconds()))

	now := time.Now().UTC().Format(time.RFC3339)
	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}
	secret.Annotations["vault.homelab.io/last-renewal-attempt"] = now

	if renewErr != nil {
		secret.Annotations["vault.homelab.io/last-renewal-error"] = renewErr.Error()
		if updateErr := m.Update(ctx, secret); updateErr != nil {
			m.Log.Error(updateErr, "Failed to record renewal-error annotation")
		}
		return fmt.Errorf("renew-self: %w", renewErr)
	}

	if renewResp == nil || renewResp.Auth == nil {
		return fmt.Errorf("renew-self returned nil response")
	}

	newLease := time.Duration(renewResp.Auth.LeaseDuration) * time.Second
	if newLease <= 0 {
		// Defensive: Vault should always return a positive lease on
		// successful renewal. If not, log + don't pretend success.
		secret.Annotations["vault.homelab.io/last-renewal-error"] = "renew-self returned non-positive LeaseDuration"
		_ = m.Update(ctx, secret)
		return fmt.Errorf("renew-self returned non-positive LeaseDuration=%d", renewResp.Auth.LeaseDuration)
	}

	secret.Annotations["vault.homelab.io/last-renewal-success"] = now
	secret.Annotations["vault.homelab.io/token-expires"] = time.Now().UTC().Add(newLease).Format(time.RFC3339)
	delete(secret.Annotations, "vault.homelab.io/last-renewal-error")

	if err := m.Update(ctx, secret); err != nil {
		return fmt.Errorf("update admin secret after renewal: %w", err)
	}

	m.Log.Info("Admin token renewed",
		"newExpiry", secret.Annotations["vault.homelab.io/token-expires"],
		"newLease", newLease.String(),
	)
	return nil
}

// numberSeconds extracts a seconds value from a Vault API response field.
// Vault's JSON decoder uses json.Number for numeric fields, but tests
// and older code paths may hand back float64 or int64 — accept all three.
func numberSeconds(v any) (int64, error) {
	switch n := v.(type) {
	case json.Number:
		return n.Int64()
	case float64:
		return int64(n), nil
	case int64:
		return n, nil
	case int:
		return int64(n), nil
	case nil:
		return 0, fmt.Errorf("missing")
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}
