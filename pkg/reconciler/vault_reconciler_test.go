package reconciler

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/tools/record"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-operator/pkg/vault"
)

func TestVaultReconciler_Reconcile(t *testing.T) {
	// Register scheme
	s := scheme.Scheme
	require.NoError(t, vaultv1alpha1.AddToScheme(s))

	tests := []struct {
		name            string
		vtu             *vaultv1alpha1.VaultTransitUnseal
		existingPods    []runtime.Object
		mockVault       *mockVaultClient
		mockSecrets     *mockSecretManager
		expectRequeue   bool
		expectError     bool
		expectedMetrics int
	}{
		{
			name: "successful reconciliation with initialized vault",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
						Selector:  map[string]string{"app": "vault"},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "transit-token",
							Key:  "token",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "30s",
					},
				},
			},
			existingPods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-0",
						Namespace: "vault",
						Labels:    map[string]string{"app": "vault"},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{
								Name:  "vault",
								Ready: true,
							},
						},
						PodIP: "10.0.0.1",
					},
				},
			},
			mockVault: &mockVaultClient{
				status: &vault.Status{
					Initialized: true,
					Sealed:      false,
					Version:     "1.15.0",
				},
			},
			mockSecrets: func() *mockSecretManager {
				m := newMockSecretManager()
				m.setSecret("vault", "transit-token", map[string][]byte{
					"token": []byte("test-transit-token"),
				})
				return m
			}(),
			expectRequeue:   true,
			expectError:     false,
			expectedMetrics: 1,
		},
		{
			name: "vault needs initialization",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
						Selector:  map[string]string{"app": "vault"},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "transit-token",
							Key:  "token",
						},
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						RecoveryShares:    3,
						RecoveryThreshold: 2,
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken:   "vault-admin-token",
							RecoveryKeys: "vault-keys",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "30s",
					},
				},
			},
			existingPods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-0",
						Namespace: "vault",
						Labels:    map[string]string{"app": "vault"},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{
								Name:  "vault",
								Ready: true,
							},
						},
						PodIP: "10.0.0.1",
					},
				},
			},
			mockVault: &mockVaultClient{
				status: &vault.Status{
					Initialized: false,
					Sealed:      true,
				},
				initResp: &vault.InitResponse{
					RootToken:       "root-token-123",
					RecoveryKeysB64: []string{"key1", "key2", "key3"},
				},
			},
			mockSecrets: func() *mockSecretManager {
				m := newMockSecretManager()
				m.setSecret("vault", "transit-token", map[string][]byte{
					"token": []byte("test-transit-token"),
				})
				return m
			}(),
			expectRequeue:   true,
			expectError:     false,
			expectedMetrics: 2, // status + initialization
		},
		{
			name: "no vault pods found",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
						Selector:  map[string]string{"app": "vault"},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "transit-token",
							Key:  "token",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "30s",
					},
				},
			},
			existingPods: []runtime.Object{},
			mockSecrets: func() *mockSecretManager {
				m := newMockSecretManager()
				m.setSecret("vault", "transit-token", map[string][]byte{
					"token": []byte("test-transit-token"),
				})
				return m
			}(),
			expectRequeue: true,
			expectError:   false,
		},
		{
			name: "transit token not found",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
						Selector:  map[string]string{"app": "vault"},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "transit-token",
							Key:  "token",
						},
					},
				},
			},
			mockSecrets: newMockSecretManager(),
			expectError: true,
		},
		{
			name: "pod not ready",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test",
					Namespace: "default",
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
						Selector:  map[string]string{"app": "vault"},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "transit-token",
							Key:  "token",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "30s",
					},
				},
			},
			existingPods: []runtime.Object{
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-0",
						Namespace: "vault",
						Labels:    map[string]string{"app": "vault"},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodPending,
					},
				},
			},
			mockSecrets: func() *mockSecretManager {
				m := newMockSecretManager()
				m.setSecret("vault", "transit-token", map[string][]byte{
					"token": []byte("test-transit-token"),
				})
				return m
			}(),
			expectRequeue: true,
			expectError:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create fake client
			objects := append([]runtime.Object{tt.vtu}, tt.existingPods...)
			fakeClient := fake.NewClientBuilder().
				WithScheme(s).
				WithRuntimeObjects(objects...).
				WithStatusSubresource(&vaultv1alpha1.VaultTransitUnseal{}).
				Build()

			// Create metrics recorder
			metrics := &mockMetricsRecorder{}

			// Create reconciler
			r := &VaultReconciler{
				Client:   fakeClient,
				Log:      testr.New(t),
				Recorder: record.NewFakeRecorder(10),
				VaultFactory: &mockVaultClientFactory{
					client: tt.mockVault,
				},
				SecretManager:   tt.mockSecrets,
				MetricsRecorder: metrics,
			}

			// Run reconcile
			result := r.Reconcile(context.Background(), tt.vtu)

			// Check results
			if tt.expectError {
				assert.NotNil(t, result.Error)
			} else {
				assert.Nil(t, result.Error)
			}

			if tt.expectRequeue {
				assert.Greater(t, result.RequeueAfter, time.Duration(0))
			}

			// Verify metrics were recorded
			if tt.name == "vault needs initialization" {
				assert.Len(t, metrics.vaultStatuses, 1)
				assert.Len(t, metrics.initializations, 1)
			} else {
				assert.Len(t, metrics.vaultStatuses, tt.expectedMetrics)
			}
		})
	}
}

func TestVaultReconciler_initializeVault(t *testing.T) {
	// Register scheme
	s := scheme.Scheme
	require.NoError(t, vaultv1alpha1.AddToScheme(s))

	tests := []struct {
		name        string
		vtu         *vaultv1alpha1.VaultTransitUnseal
		mockClient  *mockVaultClient
		mockSecrets *mockSecretManager
		wantErr     bool
	}{
		{
			name: "successful initialization",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						RecoveryShares:    3,
						RecoveryThreshold: 2,
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken:   "vault-admin-token",
							RecoveryKeys: "vault-keys",
						},
					},
				},
			},
			mockClient: &mockVaultClient{
				initResp: &vault.InitResponse{
					RootToken:       "root-token-123",
					RecoveryKeysB64: []string{"key1", "key2", "key3"},
				},
			},
			mockSecrets: newMockSecretManager(),
			wantErr:     false,
		},
		{
			name: "initialization error",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					Initialization: vaultv1alpha1.InitializationSpec{
						RecoveryShares:    3,
						RecoveryThreshold: 2,
					},
				},
			},
			mockClient: &mockVaultClient{
				initErr: fmt.Errorf("vault already initialized"),
			},
			mockSecrets: newMockSecretManager(),
			wantErr:     true,
		},
		{
			name: "secret storage error",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						RecoveryShares:    3,
						RecoveryThreshold: 2,
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken:   "vault-admin-token",
							RecoveryKeys: "vault-keys",
						},
					},
				},
			},
			mockClient: &mockVaultClient{
				initResp: &vault.InitResponse{
					RootToken:       "root-token-123",
					RecoveryKeysB64: []string{"key1", "key2", "key3"},
				},
			},
			mockSecrets: &mockSecretManager{
				createErr: fmt.Errorf("permission denied"),
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vault-0",
				},
			}

			r := &VaultReconciler{
				Log:           testr.New(t),
				Recorder:      record.NewFakeRecorder(10),
				SecretManager: tt.mockSecrets,
			}

			err := r.initializeVault(context.Background(), tt.mockClient, pod, tt.vtu)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				// Verify secrets were created
				assert.Greater(t, len(tt.mockSecrets.calls), 0)
			}
		})
	}
}

func TestVaultReconciler_isPodReady(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "pod is ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "vault",
							Ready: true,
						},
					},
				},
			},
			want: true,
		},
		{
			name: "pod not running",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
				},
			},
			want: false,
		},
		{
			name: "container not ready",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "vault",
							Ready: false,
						},
					},
				},
			},
			want: false,
		},
		{
			name: "vault container not found",
			pod: &corev1.Pod{
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name:  "other-container",
							Ready: true,
						},
					},
				},
			},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &VaultReconciler{}
			got := r.isPodReady(tt.pod)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestVaultReconciler_updateConditions(t *testing.T) {
	tests := []struct {
		name          string
		status        *vault.Status
		expectedConds map[string]string
	}{
		{
			name: "initialized and unsealed",
			status: &vault.Status{
				Initialized: true,
				Sealed:      false,
			},
			expectedConds: map[string]string{
				"Initialized": string(metav1.ConditionTrue),
				"Ready":       string(metav1.ConditionTrue),
			},
		},
		{
			name: "not initialized",
			status: &vault.Status{
				Initialized: false,
				Sealed:      true,
			},
			expectedConds: map[string]string{
				"Initialized": string(metav1.ConditionFalse),
				"Ready":       string(metav1.ConditionFalse),
			},
		},
		{
			name: "initialized but sealed",
			status: &vault.Status{
				Initialized: true,
				Sealed:      true,
			},
			expectedConds: map[string]string{
				"Initialized": string(metav1.ConditionTrue),
				"Ready":       string(metav1.ConditionFalse),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			vtu := &vaultv1alpha1.VaultTransitUnseal{}
			r := &VaultReconciler{}

			r.updateConditions(vtu, tt.status)

			// Check conditions
			cm := NewConditionManager(vtu)
			for condType, expectedStatus := range tt.expectedConds {
				cond := cm.GetCondition(condType)
				require.NotNil(t, cond, "condition %s not found", condType)
				assert.Equal(t, expectedStatus, cond.Status)
			}
		})
	}
}

func TestVaultReconciler_storeSecrets(t *testing.T) {
	tests := []struct {
		name        string
		vtu         *vaultv1alpha1.VaultTransitUnseal
		initResp    *vault.InitResponse
		expectCalls int
		wantErr     bool
	}{
		{
			name: "successful storage",
			vtu: &vaultv1alpha1.VaultTransitUnseal{
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken:   "vault-admin-token",
							RecoveryKeys: "vault-keys",
						},
					},
				},
			},
			initResp: &vault.InitResponse{
				RootToken:       "root-123",
				RecoveryKeysB64: []string{"key1", "key2", "key3"},
			},
			expectCalls: 2, // admin token + recovery keys
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockSecrets := newMockSecretManager()
			r := &VaultReconciler{
				SecretManager: mockSecrets,
			}

			err := r.storeSecrets(context.Background(), tt.vtu, tt.initResp)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Len(t, mockSecrets.calls, tt.expectCalls)

				// Verify admin token was stored
				key := fmt.Sprintf("%s/%s", tt.vtu.Spec.VaultPod.Namespace, tt.vtu.Spec.Initialization.SecretNames.AdminToken)
				assert.Contains(t, mockSecrets.secrets[key], "token")

				// Verify recovery keys were stored
				key = fmt.Sprintf("%s/%s", tt.vtu.Spec.VaultPod.Namespace, tt.vtu.Spec.Initialization.SecretNames.RecoveryKeys)
				assert.Contains(t, mockSecrets.secrets[key], "root-token")
				assert.Contains(t, mockSecrets.secrets[key], "recovery-key-0")
			}
		})
	}
}
