package reconciler

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

func TestResolveTransitVaultAddress(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)
	_ = vaultv1alpha1.AddToScheme(scheme)

	tests := []struct {
		name          string
		vtu           *vaultv1alpha1.VaultTransitUnseal
		objects       []client.Object
		expectedAddr  string
		expectError   bool
		errorContains string
	}{
		{
			name: "direct address",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						Address: "http://192.168.1.42:61200",
					},
				},
			},
			expectedAddr: "http://192.168.1.42:61200",
			expectError:  false,
		},
		{
			name: "address from configmap",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							ConfigMapKeyRef: &vaultv1alpha1.ConfigMapKeyReference{
								Name: "vault-config",
								Key:  "qnap.address",
							},
						},
					},
				},
			},
			objects: []client.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-config",
						Namespace: "vault",
					},
					Data: map[string]string{
						"qnap.address": "http://nas.local:61200",
					},
				},
			},
			expectedAddr: "http://nas.local:61200",
			expectError:  false,
		},
		{
			name: "address from structured configmap key",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							ConfigMapKeyRef: &vaultv1alpha1.ConfigMapKeyReference{
								Name: "vault-config",
								Key:  "config.yaml.environments.production.transit.address",
							},
						},
					},
				},
			},
			objects: []client.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-config",
						Namespace: "vault",
					},
					Data: map[string]string{
						"config.yaml": `environments:
  production:
    transit:
      address: "https://vault.vault.mesh:8200"`,
					},
				},
			},
			expectedAddr: "https://vault.vault.mesh:8200",
			expectError:  false,
		},
		{
			name: "address from configmap in different namespace",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							ConfigMapKeyRef: &vaultv1alpha1.ConfigMapKeyReference{
								Name:      "vault-config",
								Key:       "qnap.address",
								Namespace: "config",
							},
						},
					},
				},
			},
			objects: []client.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-config",
						Namespace: "config",
					},
					Data: map[string]string{
						"qnap.address": "http://nas.local:61200",
					},
				},
			},
			expectedAddr: "http://nas.local:61200",
			expectError:  false,
		},
		{
			name: "address from secret",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							SecretKeyRef: &vaultv1alpha1.SecretKeyReference{
								Name: "vault-secrets",
								Key:  "transit-address",
							},
						},
					},
				},
			},
			objects: []client.Object{
				&corev1.Secret{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-secrets",
						Namespace: "vault",
					},
					Data: map[string][]byte{
						"transit-address": []byte("http://secure.vault:8200"),
					},
				},
			},
			expectedAddr: "http://secure.vault:8200",
			expectError:  false,
		},
		{
			name: "no address specified",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{},
				},
			},
			expectError:   true,
			errorContains: "address not specified",
		},
		{
			name: "configmap not found",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							ConfigMapKeyRef: &vaultv1alpha1.ConfigMapKeyReference{
								Name: "missing-config",
								Key:  "address",
							},
						},
					},
				},
			},
			expectError:   true,
			errorContains: "ConfigMap vault/missing-config not found",
		},
		{
			name: "key not found in configmap",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							ConfigMapKeyRef: &vaultv1alpha1.ConfigMapKeyReference{
								Name: "vault-config",
								Key:  "missing-key",
							},
						},
					},
				},
			},
			objects: []client.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-config",
						Namespace: "vault",
					},
					Data: map[string]string{
						"other-key": "value",
					},
				},
			},
			expectError:   true,
			errorContains: "key missing-key not found",
		},
		{
			name: "empty value in configmap",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							ConfigMapKeyRef: &vaultv1alpha1.ConfigMapKeyReference{
								Name: "vault-config",
								Key:  "empty-address",
							},
						},
					},
				},
			},
			objects: []client.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-config",
						Namespace: "vault",
					},
					Data: map[string]string{
						"empty-address": "",
					},
				},
			},
			expectError:   true,
			errorContains: "is empty",
		},
		{
			name: "configmap not found with default",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							ConfigMapKeyRef: &vaultv1alpha1.ConfigMapKeyReference{
								Name: "missing-config",
								Key:  "address",
							},
							Default: "http://default.vault:8200",
						},
					},
				},
			},
			expectedAddr: "http://default.vault:8200",
			expectError:  false,
		},
		{
			name: "empty configmap value with default",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							ConfigMapKeyRef: &vaultv1alpha1.ConfigMapKeyReference{
								Name: "vault-config",
								Key:  "empty-address",
							},
							Default: "http://fallback.vault:8200",
						},
					},
				},
			},
			objects: []client.Object{
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-config",
						Namespace: "vault",
					},
					Data: map[string]string{
						"empty-address": "",
					},
				},
			},
			expectedAddr: "http://fallback.vault:8200",
			expectError:  false,
		},
		{
			name: "secret not found with default",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "vault",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						AddressFrom: &vaultv1alpha1.AddressReference{
							SecretKeyRef: &vaultv1alpha1.SecretKeyReference{
								Name: "missing-secret",
								Key:  "address",
							},
							Default: "http://secret-default.vault:8200",
						},
					},
				},
			},
			expectedAddr: "http://secret-default.vault:8200",
			expectError:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client with test objects
			fakeClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.objects...).
				Build()

			// Create test logger
			log := logr.Discard()

			// Test the function
			addr, err := ResolveTransitVaultAddress(context.Background(), fakeClient, log, tt.vtu)

			// Check error expectation
			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
					return
				}
				if tt.errorContains != "" && !contains(err.Error(), tt.errorContains) {
					t.Errorf("expected error to contain %q but got %q", tt.errorContains, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return
				}
				if addr != tt.expectedAddr {
					t.Errorf("expected address %q but got %q", tt.expectedAddr, addr)
				}
			}
		})
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && s[0:len(s)] != "" && s[0:len(substr)] == substr || len(s) > len(substr) && contains(s[1:], substr)
}
