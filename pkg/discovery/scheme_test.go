package discovery

import (
	"context"
	"testing"

	"github.com/go-logr/logr"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// TestAddressScheme pins the compatibility promise of vaultPod.scheme: unset
// means http, which is what every address the operator built was hard-coded to
// before the field existed.
func TestAddressScheme(t *testing.T) {
	d := &ServiceDiscovery{Log: logr.Discard()}
	ctx := context.Background()

	tests := []struct {
		name   string
		scheme string
		want   string
	}{
		{
			name:   "unset scheme keeps the historical http",
			scheme: "",
			want:   "http://vault-0.vault-internal.vault.svc.cluster.local:8300",
		},
		{
			name:   "explicit http",
			scheme: "http",
			want:   "http://vault-0.vault-internal.vault.svc.cluster.local:8300",
		},
		{
			name:   "https for a Vault that terminates TLS itself",
			scheme: "https",
			want:   "https://vault-0.vault-internal.vault.svc.cluster.local:8300",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec := &vaultv1alpha1.VaultPodSpec{
				Namespace:           "vault",
				HeadlessServiceName: "vault-internal",
				Scheme:              tt.scheme,
			}

			got, err := d.GetVaultServiceEndpoint(ctx, spec, pod("vault-0"))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestVaultAddressOverrideIgnoresScheme(t *testing.T) {
	d := &ServiceDiscovery{Log: logr.Discard()}

	spec := &vaultv1alpha1.VaultPodSpec{
		Namespace:    "vault",
		Scheme:       "http",
		VaultAddress: "https://vault.example.com:8200",
	}

	got, err := d.GetVaultServiceEndpoint(context.Background(), spec, pod("vault-0"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "https://vault.example.com:8200" {
		t.Fatalf("override should carry its own scheme, got %q", got)
	}
}
