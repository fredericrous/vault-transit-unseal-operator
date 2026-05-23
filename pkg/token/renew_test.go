package token

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

func newScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	require.NoError(t, vaultv1alpha1.AddToScheme(scheme))
	require.NoError(t, corev1.AddToScheme(scheme))
	return scheme
}

func TestNumberSeconds(t *testing.T) {
	cases := []struct {
		name    string
		in      any
		want    int64
		wantErr bool
	}{
		{"json.Number", json.Number("3600"), 3600, false},
		{"float64", float64(3600), 3600, false},
		{"int64", int64(3600), 3600, false},
		{"int", 3600, 3600, false},
		{"nil", nil, 0, true},
		{"string", "3600", 0, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := numberSeconds(tc.in)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected error, got nil (value=%v)", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("got %d, want %d", got, tc.want)
			}
		})
	}
}

// TestRenewIfNeeded_NoOpPaths verifies the early-return guards that
// don't require a Vault server (auto-renew disabled, no secret, no
// token bytes, placeholder token). The on-the-wire renewal path is
// covered by the operator's integration suite.
func TestRenewIfNeeded_NoOpPaths(t *testing.T) {
	scheme := newScheme(t)
	mkVTU := func(autoRenew bool) *vaultv1alpha1.VaultTransitUnseal {
		return &vaultv1alpha1.VaultTransitUnseal{
			ObjectMeta: metav1.ObjectMeta{Name: "vault-homelab", Namespace: "vault"},
			Spec: vaultv1alpha1.VaultTransitUnsealSpec{
				VaultPod: vaultv1alpha1.VaultPodSpec{Namespace: "vault"},
				Initialization: vaultv1alpha1.InitializationSpec{
					SecretNames: vaultv1alpha1.SecretNamesSpec{AdminToken: "vault-admin-token"},
				},
				TokenManagement: &vaultv1alpha1.TokenManagementSpec{
					AutoRenew: autoRenew,
					TTL:       "168h",
				},
			},
		}
	}

	t.Run("auto-renew disabled returns nil", func(t *testing.T) {
		m := &SimpleManager{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			Log:    testr.New(t),
			Scheme: scheme,
		}
		if err := m.RenewIfNeeded(context.Background(), mkVTU(false), nil); err != nil {
			t.Fatalf("expected nil, got %v", err)
		}
	})

	t.Run("missing secret returns nil", func(t *testing.T) {
		m := &SimpleManager{
			Client: fake.NewClientBuilder().WithScheme(scheme).Build(),
			Log:    testr.New(t),
			Scheme: scheme,
		}
		if err := m.RenewIfNeeded(context.Background(), mkVTU(true), nil); err != nil {
			t.Fatalf("expected nil for missing secret, got %v", err)
		}
	})

	t.Run("empty token data returns nil", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "vault-admin-token", Namespace: "vault"},
			Data:       map[string][]byte{"token": []byte("")},
		}
		m := &SimpleManager{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build(),
			Log:    testr.New(t),
			Scheme: scheme,
		}
		if err := m.RenewIfNeeded(context.Background(), mkVTU(true), nil); err != nil {
			t.Fatalf("expected nil for empty token, got %v", err)
		}
	})

	t.Run("placeholder token returns nil", func(t *testing.T) {
		secret := &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "vault-admin-token", Namespace: "vault"},
			Data:       map[string][]byte{"token": []byte("hvs.PLACEHOLDER-x-NEEDS-MANUAL-GENERATION")},
		}
		m := &SimpleManager{
			Client: fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build(),
			Log:    testr.New(t),
			Scheme: scheme,
		}
		if err := m.RenewIfNeeded(context.Background(), mkVTU(true), nil); err != nil {
			t.Fatalf("expected nil for placeholder token, got %v", err)
		}
	})
}
