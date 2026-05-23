package token

import (
	"context"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// AdminTokenBackup persists / recovers the homelab vault admin token
// to/from the transit Vault (NAS Vault in homelab). Implementations
// live next to the transit-KV plumbing (see pkg/reconciler) since
// they need the transit-vault address resolver + CA cert; the token
// manager calls into this interface so the SimpleManager itself
// stays free of transit-vault dependencies.
//
// The backup feature gates on
// VaultTransitUnseal.Spec.Initialization.TokenRecovery.Enabled and
// .BackupToTransit; implementations should no-op gracefully when
// either is false. Errors are non-fatal in the token mint path —
// the in-cluster Secret is still the primary source of truth — but
// the recover path's success is what makes "operator self-recover
// when authelia is down" work.
type AdminTokenBackup interface {
	// Backup writes the current admin token to the transit Vault.
	// Called after every successful mint (createInitialToken,
	// replaceRootToken). Idempotent — last write wins.
	Backup(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal, token string) error

	// Recover reads the backed-up admin token from the transit
	// Vault and returns it, or returns ("", err) when no backup
	// exists / network failure / token absent. Callers must
	// validate the returned token works against homelab Vault
	// before storing it — the backup might be stale (e.g. a manual
	// rotation happened without a fresh backup).
	Recover(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) (string, error)
}
