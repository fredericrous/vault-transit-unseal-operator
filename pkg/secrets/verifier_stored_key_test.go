package secrets

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

func storedKeyVTU() *vaultv1alpha1.VaultTransitUnseal {
	return &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{Name: "vault", Namespace: "vault"},
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			Mode:     vaultv1alpha1.UnsealModeStoredKey,
			VaultPod: vaultv1alpha1.VaultPodSpec{Namespace: "vault"},
		},
	}
}

func unsealKeysSecret() *corev1.Secret {
	return &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "vault-unseal-keys", Namespace: "vault"},
		Data:       map[string][]byte{"unseal-keys.txt": []byte("share-one\n")},
	}
}

func newVerifier(t *testing.T, objs ...runtime.Object) *Verifier {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, vaultv1alpha1.AddToScheme(scheme))

	return NewVerifier(
		fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build(),
		testr.New(t),
	)
}

// TestStoredKeyExpectsOnlyTheKeySecret pins the smaller expectation set for
// stored-key mode. There is no transit token to hold, and — because this mode
// never initializes Vault — no admin token or recovery-key Secret the operator
// would have created. Demanding those would report a permanent, unactionable
// "missing secret" on every reconcile.
func TestStoredKeyExpectsOnlyTheKeySecret(t *testing.T) {
	v := newVerifier(t, unsealKeysSecret())

	result, err := v.VerifyExpectedSecrets(context.Background(), storedKeyVTU())

	require.NoError(t, err)
	assert.True(t, result.AllPresent)
	assert.Empty(t, result.Missing)
}

func TestStoredKeyReportsMissingKeySecret(t *testing.T) {
	v := newVerifier(t)

	result, err := v.VerifyExpectedSecrets(context.Background(), storedKeyVTU())

	require.NoError(t, err)
	assert.False(t, result.AllPresent)
	require.Len(t, result.Missing, 1)
	assert.Equal(t, "vault-unseal-keys", result.Missing[0].Name)
	assert.Equal(t, "vault", result.Missing[0].Namespace)
}

func TestStoredKeyHonoursCustomSecretRef(t *testing.T) {
	vtu := storedKeyVTU()
	vtu.Spec.StoredKey = &vaultv1alpha1.StoredKeySpec{
		SecretRef: vaultv1alpha1.StoredKeySecretRef{
			Name:      "root-shares",
			Key:       "shares",
			Namespace: "vault-keys",
		},
	}

	v := newVerifier(t, &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{Name: "root-shares", Namespace: "vault-keys"},
		Data:       map[string][]byte{"shares": []byte("a\nb\n")},
	})

	result, err := v.VerifyExpectedSecrets(context.Background(), vtu)

	require.NoError(t, err)
	assert.True(t, result.AllPresent)
}

func TestStoredKeyExpectsAdminTokenOnlyWhenManaged(t *testing.T) {
	vtu := storedKeyVTU()
	vtu.Spec.TokenManagement = &vaultv1alpha1.TokenManagementSpec{Enabled: true}
	vtu.Spec.Initialization.SecretNames.AdminToken = "vault-admin-token"

	v := newVerifier(t, unsealKeysSecret())

	result, err := v.VerifyExpectedSecrets(context.Background(), vtu)

	require.NoError(t, err)
	assert.False(t, result.AllPresent)
	require.Len(t, result.Missing, 1)
	assert.Equal(t, "vault-admin-token", result.Missing[0].Name)
}

// TestTransitExpectationsUnchanged is the regression guard for the other side
// of the branch: a transit resource must still be asked for its transit token.
func TestTransitExpectationsUnchanged(t *testing.T) {
	vtu := &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{Name: "vault", Namespace: "vault"},
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			VaultPod: vaultv1alpha1.VaultPodSpec{Namespace: "vault"},
			TransitVault: vaultv1alpha1.TransitVaultSpec{
				SecretRef: vaultv1alpha1.SecretReference{Name: "vault-transit-token", Key: "token"},
			},
			Initialization: vaultv1alpha1.InitializationSpec{
				SecretNames: vaultv1alpha1.SecretNamesSpec{AdminToken: "vault-admin-token"},
			},
		},
	}

	// The unseal-keys Secret exists but is irrelevant to transit mode.
	v := newVerifier(t, unsealKeysSecret())

	result, err := v.VerifyExpectedSecrets(context.Background(), vtu)

	require.NoError(t, err)
	assert.False(t, result.AllPresent)

	missing := map[string]bool{}
	for _, m := range result.Missing {
		missing[m.Name] = true
	}
	assert.True(t, missing["vault-transit-token"])
	assert.True(t, missing["vault-admin-token"])
	assert.False(t, missing["vault-unseal-keys"])
}
