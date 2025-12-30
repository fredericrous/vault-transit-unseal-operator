package secrets

import (
	"context"
	"testing"

	"github.com/go-logr/logr/testr"
	vaultapi "github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

type mockVaultClient struct {
	initialized bool
	sealed      bool
	shouldError bool
}

func (m *mockVaultClient) CheckStatus(ctx context.Context) (*vault.Status, error) {
	if m.shouldError {
		return nil, assert.AnError
	}
	return &vault.Status{
		Initialized: m.initialized,
		Sealed:      m.sealed,
		Version:     "1.15.0",
	}, nil
}

func (m *mockVaultClient) Initialize(ctx context.Context, req *vault.InitRequest) (*vault.InitResponse, error) {
	if m.shouldError {
		return nil, assert.AnError
	}
	return &vault.InitResponse{
		RootToken:       "hvs.new-root-token",
		RecoveryKeysB64: []string{"key1", "key2", "key3"},
	}, nil
}

func (m *mockVaultClient) GetAPIClient() *vaultapi.Client {
	return nil
}

func (m *mockVaultClient) IsHealthy(ctx context.Context) bool {
	return !m.shouldError && !m.sealed
}

func (m *mockVaultClient) EnableAuth(ctx context.Context, path, authType string) error {
	if m.shouldError {
		return assert.AnError
	}
	return nil
}

func (m *mockVaultClient) AuthEnabled(ctx context.Context, path string) (bool, error) {
	if m.shouldError {
		return false, assert.AnError
	}
	return true, nil
}

func (m *mockVaultClient) WriteAuth(ctx context.Context, path string, data map[string]interface{}) error {
	if m.shouldError {
		return assert.AnError
	}
	return nil
}

func (m *mockVaultClient) MountExists(ctx context.Context, path string) (bool, error) {
	if m.shouldError {
		return false, assert.AnError
	}
	return true, nil
}

func (m *mockVaultClient) MountSecretEngine(ctx context.Context, path string, input *vault.MountInput) error {
	if m.shouldError {
		return assert.AnError
	}
	return nil
}

func (m *mockVaultClient) WritePolicy(ctx context.Context, name, policy string) error {
	if m.shouldError {
		return assert.AnError
	}
	return nil
}

func TestRecoverSecrets(t *testing.T) {
	tests := []struct {
		name               string
		vtu                *vaultv1alpha1.VaultTransitUnseal
		verificationResult *VerificationResult
		vaultClient        *mockVaultClient
		existingSecrets    []runtime.Object
		expectError        bool
		checkSecret        string
		checkNamespace     string
	}{
		{
			name: "no recovery needed",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vtu",
					Namespace: "default",
				},
			},
			verificationResult: &VerificationResult{
				AllPresent: true,
			},
			vaultClient: &mockVaultClient{
				initialized: true,
				sealed:      false,
			},
			expectError: false,
		},
		{
			name: "recover missing transit token - should create placeholder",
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
				},
			},
			verificationResult: &VerificationResult{
				AllPresent: false,
				Missing: []MissingSecret{
					{
						Name:      "vault-transit-token",
						Namespace: "vault",
					},
				},
				RecoveryPlan: []RecoveryAction{
					{
						Type:        "create",
						SecretName:  "vault-transit-token",
						Namespace:   "vault",
						Description: "Create missing secret vault/vault-transit-token",
					},
				},
			},
			vaultClient: &mockVaultClient{
				initialized: true,
				sealed:      false,
			},
			expectError:    true, // Transit token returns error after creating placeholder
			checkSecret:    "vault-transit-token",
			checkNamespace: "vault",
		},
		{
			name: "recover missing admin token without recovery keys",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vtu",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken: "vault-admin-token",
						},
					},
				},
			},
			verificationResult: &VerificationResult{
				AllPresent: false,
				Missing: []MissingSecret{
					{
						Name:      "vault-admin-token",
						Namespace: "vault",
					},
				},
				RecoveryPlan: []RecoveryAction{
					{
						Type:        "create",
						SecretName:  "vault-admin-token",
						Namespace:   "vault",
						Description: "Create missing secret vault/vault-admin-token",
					},
				},
			},
			vaultClient: &mockVaultClient{
				initialized: true,
				sealed:      false,
			},
			expectError:    false, // Admin token creates placeholder, no error returned
			checkSecret:    "vault-admin-token",
			checkNamespace: "vault",
		},
		{
			name: "vault not initialized - cannot recover admin token",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vtu",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken: "vault-admin-token",
						},
					},
				},
			},
			verificationResult: &VerificationResult{
				AllPresent: false,
				Missing: []MissingSecret{
					{
						Name:      "vault-admin-token",
						Namespace: "vault",
					},
				},
				RecoveryPlan: []RecoveryAction{
					{
						Type:        "create",
						SecretName:  "vault-admin-token",
						Namespace:   "vault",
						Description: "Create missing secret vault/vault-admin-token",
					},
				},
			},
			vaultClient: &mockVaultClient{
				initialized: false,
				sealed:      true,
			},
			expectError: false, // Creates placeholder, no error
		},
		{
			name: "recover missing recovery keys - should create placeholder",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-vtu",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							RecoveryKeys: "vault-recovery-keys",
						},
					},
				},
			},
			verificationResult: &VerificationResult{
				AllPresent: false,
				Missing: []MissingSecret{
					{
						Name:      "vault-recovery-keys",
						Namespace: "vault",
					},
				},
				RecoveryPlan: []RecoveryAction{
					{
						Type:        "create",
						SecretName:  "vault-recovery-keys",
						Namespace:   "vault",
						Description: "Create missing secret vault/vault-recovery-keys",
					},
				},
			},
			vaultClient: &mockVaultClient{
				initialized: true,
				sealed:      false,
			},
			expectError:    false, // Recovery keys create placeholder, no error
			checkSecret:    "vault-recovery-keys",
			checkNamespace: "vault",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client
			scheme := runtime.NewScheme()
			require.NoError(t, vaultv1alpha1.AddToScheme(scheme))
			require.NoError(t, corev1.AddToScheme(scheme))

			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithRuntimeObjects(tt.existingSecrets...).
				Build()

			// Create recovery manager
			logger := testr.New(t)
			recorder := record.NewFakeRecorder(100)
			secretMgr := NewManager(client, logger.WithName("secrets"))
			manager := NewRecoveryManager(client, logger, recorder, scheme, secretMgr, nil) // nil token manager for tests

			// Attempt recovery
			ctx := context.Background()
			err := manager.RecoverSecrets(ctx, tt.vtu, tt.verificationResult, tt.vaultClient)

			if tt.expectError {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			// Check if placeholder secret was created when expected
			if tt.checkSecret != "" {
				secret := &corev1.Secret{}
				err := client.Get(ctx, types.NamespacedName{
					Name:      tt.checkSecret,
					Namespace: tt.checkNamespace,
				}, secret)

				// Should have created a placeholder (unless it's the transit token which returns an error)
				if !tt.expectError || tt.checkSecret == "vault-transit-token" {
					assert.NoError(t, err, "Expected placeholder secret to be created")
					assert.Contains(t, secret.Annotations, "vault.homelab.io/recovery-required")
					assert.Equal(t, "true", secret.Annotations["vault.homelab.io/recovery-required"])
				}
			}
		})
	}
}

func TestGenerateSecureToken(t *testing.T) {
	// Test token generation
	token1, err := GenerateSecureToken(32)
	require.NoError(t, err)
	assert.NotEmpty(t, token1)

	// Generate another token - should be different
	token2, err := GenerateSecureToken(32)
	require.NoError(t, err)
	assert.NotEmpty(t, token2)
	assert.NotEqual(t, token1, token2)

	// Test different lengths
	shortToken, err := GenerateSecureToken(16)
	require.NoError(t, err)
	assert.NotEmpty(t, shortToken)

	longToken, err := GenerateSecureToken(64)
	require.NoError(t, err)
	assert.NotEmpty(t, longToken)
}
