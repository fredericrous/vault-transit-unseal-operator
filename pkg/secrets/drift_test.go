package secrets

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

func TestHashRecoveryShare(t *testing.T) {
	t.Run("deterministic", func(t *testing.T) {
		a := HashRecoveryShare([]byte("key0"))
		b := HashRecoveryShare([]byte("key0"))
		if a != b {
			t.Fatalf("hash not deterministic: %s vs %s", a, b)
		}
		// SHA-256 hex is 64 chars.
		if len(a) != 64 {
			t.Fatalf("expected 64-char hex hash, got %d (%q)", len(a), a)
		}
	})

	t.Run("different inputs hash differently", func(t *testing.T) {
		a := HashRecoveryShare([]byte("idIB1BUgsX41FKUdI8OfOGRkpmjv81cH4EQTMHxBJUM="))
		b := HashRecoveryShare([]byte("3VC1HXyT+rlul+x7blwsHiEsEA5keH7RMkKMpDr8ypk="))
		if a == b {
			t.Fatal("two distinct shares should hash differently")
		}
	})

	t.Run("trailing whitespace trimmed", func(t *testing.T) {
		base := HashRecoveryShare([]byte("idIB1BUgsX41FKUdI8OfOGRkpmjv81cH4EQTMHxBJUM="))
		withNL := HashRecoveryShare([]byte("idIB1BUgsX41FKUdI8OfOGRkpmjv81cH4EQTMHxBJUM=\n"))
		withCRLF := HashRecoveryShare([]byte("idIB1BUgsX41FKUdI8OfOGRkpmjv81cH4EQTMHxBJUM=\r\n"))
		withTrailingSpaces := HashRecoveryShare([]byte("idIB1BUgsX41FKUdI8OfOGRkpmjv81cH4EQTMHxBJUM=  "))
		if base != withNL || base != withCRLF || base != withTrailingSpaces {
			t.Fatalf("trailing-whitespace variants produced different hashes (base=%s, NL=%s, CRLF=%s, sp=%s)",
				base, withNL, withCRLF, withTrailingSpaces)
		}
	})

	t.Run("interior whitespace not trimmed", func(t *testing.T) {
		// A leading character or whitespace inside the share is a
		// real difference and should hash differently.
		a := HashRecoveryShare([]byte("abc"))
		b := HashRecoveryShare([]byte(" abc"))
		if a == b {
			t.Fatal("hashes for 'abc' and ' abc' must differ")
		}
	})
}

func newDriftScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	s := runtime.NewScheme()
	require.NoError(t, vaultv1alpha1.AddToScheme(s))
	require.NoError(t, corev1.AddToScheme(s))
	return s
}

func driftVTU(expectedHash string) *vaultv1alpha1.VaultTransitUnseal {
	return &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{Name: "vault-homelab", Namespace: "vault"},
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			VaultPod: vaultv1alpha1.VaultPodSpec{Namespace: "vault"},
			Initialization: vaultv1alpha1.InitializationSpec{
				SecretNames: vaultv1alpha1.SecretNamesSpec{
					RecoveryKeys:      "vault-keys",
					StoreRecoveryKeys: true,
				},
			},
		},
		Status: vaultv1alpha1.VaultTransitUnsealStatus{
			RecoveryKeysHash: expectedHash,
		},
	}
}

func TestCheckRecoveryKeyDrift(t *testing.T) {
	scheme := newDriftScheme(t)
	share := "idIB1BUgsX41FKUdI8OfOGRkpmjv81cH4EQTMHxBJUM="
	canonicalHash := HashRecoveryShare([]byte(share))

	mkSecret := func(value string) *corev1.Secret {
		return &corev1.Secret{
			ObjectMeta: metav1.ObjectMeta{Name: "vault-keys", Namespace: "vault"},
			Data:       map[string][]byte{"recovery-key-0": []byte(value)},
		}
	}

	t.Run("match returns Drifted=false", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mkSecret(share)).Build()
		res, err := CheckRecoveryKeyDrift(context.Background(), c, driftVTU(canonicalHash))
		require.NoError(t, err)
		require.True(t, res.Checked)
		require.False(t, res.Drifted)
	})

	t.Run("mismatch returns Drifted=true", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).
			WithObjects(mkSecret("3VC1HXyT+rlul+x7blwsHiEsEA5keH7RMkKMpDr8ypk=")).Build()
		res, err := CheckRecoveryKeyDrift(context.Background(), c, driftVTU(canonicalHash))
		require.NoError(t, err)
		require.True(t, res.Checked)
		require.True(t, res.Drifted)
		require.NotEqual(t, res.ExpectedHash, res.ObservedHash)
	})

	t.Run("no expected hash on status returns Checked=false", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mkSecret(share)).Build()
		res, err := CheckRecoveryKeyDrift(context.Background(), c, driftVTU(""))
		require.NoError(t, err)
		require.False(t, res.Checked)
		require.False(t, res.Drifted)
	})

	t.Run("missing secret returns Checked=false (covered by missing-secrets verifier)", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).Build()
		res, err := CheckRecoveryKeyDrift(context.Background(), c, driftVTU(canonicalHash))
		require.NoError(t, err)
		require.False(t, res.Checked)
	})

	t.Run("trailing newline does not trigger drift", func(t *testing.T) {
		c := fake.NewClientBuilder().WithScheme(scheme).WithObjects(mkSecret(share + "\n")).Build()
		res, err := CheckRecoveryKeyDrift(context.Background(), c, driftVTU(canonicalHash))
		require.NoError(t, err)
		require.True(t, res.Checked)
		require.False(t, res.Drifted, "trailing whitespace should be normalised away")
	})
}
