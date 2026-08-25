package v1alpha1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestEffectiveUnsealMode(t *testing.T) {
	tests := []struct {
		name string
		mode UnsealMode
		want UnsealMode
	}{
		{
			// The load-bearing case: every resource that existed before
			// spec.mode did has no mode at all, and must keep behaving
			// exactly as it did.
			name: "zero value is transit",
			mode: "",
			want: UnsealModeTransit,
		},
		{
			name: "explicit transit",
			mode: UnsealModeTransit,
			want: UnsealModeTransit,
		},
		{
			name: "explicit stored-key",
			mode: UnsealModeStoredKey,
			want: UnsealModeStoredKey,
		},
		{
			// The CRD enum rejects this at admission; if one ever slips
			// through, failing closed onto transit is the safe answer.
			name: "unknown value falls back to transit",
			mode: "shamir",
			want: UnsealModeTransit,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &VaultTransitUnsealSpec{Mode: tt.mode}
			assert.Equal(t, tt.want, spec.EffectiveUnsealMode())

			vtu := &VaultTransitUnseal{Spec: *spec}
			assert.Equal(t, tt.want == UnsealModeStoredKey, vtu.IsStoredKeyMode())
		})
	}
}

// TestAbsentModeUnmarshalsAsTransit guards the same invariant one level down:
// a resource stored before the field existed — so with no `mode` key at all in
// what the API server hands back — must deserialize to transit, not to some
// marker the operator would misread.
func TestAbsentModeUnmarshalsAsTransit(t *testing.T) {
	stored := []byte(`{
		"vaultPod": {"namespace": "vault", "selector": {"app.kubernetes.io/name": "vault"}},
		"transitVault": {"address": "http://transit-vault:8200", "secretRef": {"name": "vault-transit-token"}}
	}`)

	var spec VaultTransitUnsealSpec
	assert.NoError(t, json.Unmarshal(stored, &spec))

	assert.Equal(t, UnsealMode(""), spec.Mode, "mode should be absent, not defaulted client-side")
	assert.Equal(t, UnsealModeTransit, spec.EffectiveUnsealMode())
	assert.Nil(t, spec.StoredKey)
}

func TestStoredKeySecretRef(t *testing.T) {
	tests := []struct {
		name          string
		spec          VaultTransitUnsealSpec
		wantNamespace string
		wantName      string
		wantKey       string
	}{
		{
			// Defaults must land exactly on the CronJob's Secret so a
			// cluster can swap automation without moving key material.
			name: "storedKey absent falls back to the CronJob's secret",
			spec: VaultTransitUnsealSpec{
				VaultPod: VaultPodSpec{Namespace: "vault"},
			},
			wantNamespace: "vault",
			wantName:      "vault-unseal-keys",
			wantKey:       "unseal-keys.txt",
		},
		{
			name: "empty secretRef fields fall back to defaults",
			spec: VaultTransitUnsealSpec{
				VaultPod:  VaultPodSpec{Namespace: "vault"},
				StoredKey: &StoredKeySpec{},
			},
			wantNamespace: "vault",
			wantName:      "vault-unseal-keys",
			wantKey:       "unseal-keys.txt",
		},
		{
			name: "explicit values win",
			spec: VaultTransitUnsealSpec{
				VaultPod: VaultPodSpec{Namespace: "vault"},
				StoredKey: &StoredKeySpec{
					SecretRef: StoredKeySecretRef{
						Name:      "root-shamir-shares",
						Key:       "shares",
						Namespace: "vault-keys",
					},
				},
			},
			wantNamespace: "vault-keys",
			wantName:      "root-shamir-shares",
			wantKey:       "shares",
		},
		{
			name: "namespace defaults to the vault pod namespace",
			spec: VaultTransitUnsealSpec{
				VaultPod: VaultPodSpec{Namespace: "security"},
				StoredKey: &StoredKeySpec{
					SecretRef: StoredKeySecretRef{Name: "custom"},
				},
			},
			wantNamespace: "security",
			wantName:      "custom",
			wantKey:       "unseal-keys.txt",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			namespace, name, key := tt.spec.StoredKeySecretRef()
			assert.Equal(t, tt.wantNamespace, namespace)
			assert.Equal(t, tt.wantName, name)
			assert.Equal(t, tt.wantKey, key)
		})
	}
}
