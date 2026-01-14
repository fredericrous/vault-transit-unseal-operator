package secrets

import (
	"context"
	"fmt"
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

func TestVerifyExpectedSecrets(t *testing.T) {
	tests := []struct {
		name             string
		vtu              *vaultv1alpha1.VaultTransitUnseal
		existingSecrets  []runtime.Object
		expectAllPresent bool
		expectMissing    []string
		expectIncomplete []string
	}{
		{
			name: "all secrets present",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vtu",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "vault-transit-token",
							Key:  "token",
						},
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken: "vault-admin-token",
						},
					},
				},
			},
			existingSecrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-transit-token",
						Namespace: "vault",
					},
					Data: map[string][]byte{
						"token": []byte("hvs.test-transit-token"),
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-admin-token",
						Namespace: "vault",
					},
					Data: map[string][]byte{
						"token": []byte("hvs.test-admin-token"),
					},
				},
			},
			expectAllPresent: true,
			expectMissing:    []string{},
			expectIncomplete: []string{},
		},
		{
			name: "transit token missing",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vtu",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "vault-transit-token",
							Key:  "token",
						},
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken: "vault-admin-token",
						},
					},
				},
			},
			existingSecrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-admin-token",
						Namespace: "vault",
					},
					Data: map[string][]byte{
						"token": []byte("hvs.test-admin-token"),
					},
				},
			},
			expectAllPresent: false,
			expectMissing:    []string{"vault-transit-token"},
			expectIncomplete: []string{},
		},
		{
			name: "admin token has missing key",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vtu",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "vault-transit-token",
							Key:  "token",
						},
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken: "vault-admin-token",
						},
					},
				},
			},
			existingSecrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-transit-token",
						Namespace: "vault",
					},
					Data: map[string][]byte{
						"token": []byte("hvs.test-transit-token"),
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-admin-token",
						Namespace: "vault",
					},
					Data: map[string][]byte{
						// Missing "token" key
					},
				},
			},
			expectAllPresent: false,
			expectMissing:    []string{},
			expectIncomplete: []string{"vault-admin-token"},
		},
		{
			name: "recovery keys enabled and present",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vtu",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "vault-transit-token",
							Key:  "token",
						},
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						RecoveryShares:    3,
						RecoveryThreshold: 2,
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken:        "vault-admin-token",
							RecoveryKeys:      "vault-keys",
							StoreRecoveryKeys: true,
						},
					},
				},
			},
			existingSecrets: []runtime.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-transit-token",
						Namespace: "vault",
					},
					Data: map[string][]byte{
						"token": []byte("hvs.test-transit-token"),
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-admin-token",
						Namespace: "vault",
					},
					Data: map[string][]byte{
						"token": []byte("hvs.test-admin-token"),
					},
				},
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-keys",
						Namespace: "vault",
					},
					Data: map[string][]byte{
						"root-token":     []byte("hvs.root-token"),
						"recovery-key-0": []byte("key0"),
						"recovery-key-1": []byte("key1"),
						"recovery-key-2": []byte("key2"),
					},
				},
			},
			expectAllPresent: true,
			expectMissing:    []string{},
			expectIncomplete: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with existing secrets
			scheme := runtime.NewScheme()
			require.NoError(t, vaultv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.existingSecrets...).
				Build()

			// Create verifier
			logger := testr.New(t)
			verifier := NewVerifier(client, logger)

			// Verify secrets
			ctx := context.Background()
			result, err := verifier.VerifyExpectedSecrets(ctx, tt.vtu)
			require.NoError(t, err)

			// Check results
			assert.Equal(t, tt.expectAllPresent, result.AllPresent)

			// Check missing secrets
			missingNames := []string{}
			for _, m := range result.Missing {
				missingNames = append(missingNames, m.Name)
			}
			assert.ElementsMatch(t, tt.expectMissing, missingNames)

			// Check incomplete secrets
			incompleteNames := []string{}
			for _, i := range result.Incomplete {
				incompleteNames = append(incompleteNames, i.Name)
			}
			assert.ElementsMatch(t, tt.expectIncomplete, incompleteNames)

			// Verify recovery plan is generated
			if !result.AllPresent {
				assert.NotEmpty(t, result.RecoveryPlan)
			}
		})
	}
}

func TestGetExpectedSecrets(t *testing.T) {
	vtu := &vaultv1alpha1.VaultTransitUnseal{
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			VaultPod: vaultv1alpha1.VaultPodSpec{
				Namespace: "vault",
			},
			TransitVault: vaultv1alpha1.TransitVaultSpec{
				SecretRef: vaultv1alpha1.SecretReference{
					Name: "vault-transit-token",
					Key:  "token",
				},
			},
			Initialization: vaultv1alpha1.InitializationSpec{
				RecoveryShares: 5,
				SecretNames: vaultv1alpha1.SecretNamesSpec{
					AdminToken:        "vault-admin-token",
					RecoveryKeys:      "vault-keys",
					StoreRecoveryKeys: true,
				},
			},
			PostUnsealConfig: vaultv1alpha1.PostUnsealConfig{
				EnableExternalSecretsOperator: true,
			},
		},
	}

	logger := testr.New(t)
	verifier := NewVerifier(nil, logger)

	expected := verifier.getExpectedSecrets(vtu)

	// Should have 4 secrets: transit token, admin token, recovery keys, and ESO token
	assert.Len(t, expected, 4)

	// Check transit token
	assert.Contains(t, expected, ExpectedSecret{
		Name:      "vault-transit-token",
		Namespace: "vault",
		Keys:      []string{"token"},
		Optional:  false,
	})

	// Check admin token
	assert.Contains(t, expected, ExpectedSecret{
		Name:      "vault-admin-token",
		Namespace: "vault",
		Keys:      []string{"token"},
		Optional:  false,
	})

	// Check recovery keys
	recoveryKeysFound := false
	for _, exp := range expected {
		if exp.Name == "vault-keys" {
			recoveryKeysFound = true
			// Should have 5 recovery keys (root-token is stored in admin token secret)
			assert.Len(t, exp.Keys, 5)
			for i := 0; i < 5; i++ {
				assert.Contains(t, exp.Keys, fmt.Sprintf("recovery-key-%d", i))
			}
			break
		}
	}
	assert.True(t, recoveryKeysFound)

	// Check ESO token (optional)
	esoTokenFound := false
	for _, exp := range expected {
		if exp.Name == "vault-external-secrets-token" {
			esoTokenFound = true
			assert.True(t, exp.Optional)
			break
		}
	}
	assert.True(t, esoTokenFound)
}
