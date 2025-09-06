package health

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
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-operator/pkg/vault"
)

type mockVaultClient struct {
	healthy bool
	err     error
}

func (m *mockVaultClient) CheckStatus(ctx context.Context) (*vault.Status, error) {
	return &vault.Status{Initialized: true, Sealed: false}, nil
}

func (m *mockVaultClient) Initialize(ctx context.Context, shares, threshold int) (*vault.InitResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockVaultClient) IsHealthy(ctx context.Context) bool {
	return m.healthy
}

type mockVaultClientFactory struct {
	clients map[string]*mockVaultClient
	err     error
}

func (m *mockVaultClientFactory) NewClientForPod(pod *corev1.Pod) (vault.Client, error) {
	if m.err != nil {
		return nil, m.err
	}
	if client, ok := m.clients[pod.Name]; ok {
		return client, nil
	}
	return &mockVaultClient{healthy: true}, nil
}

func setupTest(t *testing.T) (*Checker, *runtime.Scheme) {
	s := scheme.Scheme
	require.NoError(t, vaultv1alpha1.AddToScheme(s))

	return &Checker{
		log: testr.New(t),
	}, s
}

func TestLiveness(t *testing.T) {
	checker, s := setupTest(t)

	tests := []struct {
		name    string
		objects []runtime.Object
		wantErr bool
	}{
		{
			name:    "can list resources",
			objects: []runtime.Object{},
			wantErr: false,
		},
		{
			name: "with existing resources",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().
				WithScheme(s).
				WithRuntimeObjects(tt.objects...).
				Build()
			checker.client = client

			err := checker.Liveness(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestReadiness(t *testing.T) {
	checker, s := setupTest(t)

	tests := []struct {
		name         string
		objects      []runtime.Object
		vaultFactory VaultClientFactory
		wantReady    bool
		wantMessage  string
	}{
		{
			name:         "no resources to manage",
			objects:      []runtime.Object{},
			vaultFactory: &mockVaultClientFactory{},
			wantReady:    true,
			wantMessage:  "no VaultTransitUnseal resources to manage",
		},
		{
			name: "no vault pods found",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
					Spec: vaultv1alpha1.VaultTransitUnsealSpec{
						VaultPod: vaultv1alpha1.VaultPodSpec{
							Namespace: "vault",
							Selector:  map[string]string{"app": "vault"},
						},
					},
				},
			},
			vaultFactory: &mockVaultClientFactory{},
			wantReady:    false,
			wantMessage:  "no Vault pods found",
		},
		{
			name: "all vault pods healthy",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
					Spec: vaultv1alpha1.VaultTransitUnsealSpec{
						VaultPod: vaultv1alpha1.VaultPodSpec{
							Namespace: "vault",
							Selector:  map[string]string{"app": "vault"},
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-0",
						Namespace: "vault",
						Labels:    map[string]string{"app": "vault"},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "vault", Ready: true},
						},
					},
				},
			},
			vaultFactory: &mockVaultClientFactory{
				clients: map[string]*mockVaultClient{
					"vault-0": {healthy: true},
				},
			},
			wantReady:   true,
			wantMessage: "all 1 Vault pods are healthy",
		},
		{
			name: "some vault pods unhealthy",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
					Spec: vaultv1alpha1.VaultTransitUnsealSpec{
						VaultPod: vaultv1alpha1.VaultPodSpec{
							Namespace: "vault",
							Selector:  map[string]string{"app": "vault"},
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-0",
						Namespace: "vault",
						Labels:    map[string]string{"app": "vault"},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "vault", Ready: true},
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-1",
						Namespace: "vault",
						Labels:    map[string]string{"app": "vault"},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "vault", Ready: true},
						},
					},
				},
			},
			vaultFactory: &mockVaultClientFactory{
				clients: map[string]*mockVaultClient{
					"vault-0": {healthy: true},
					"vault-1": {healthy: false},
				},
			},
			wantReady:   true,
			wantMessage: "1/2 Vault pods are healthy",
		},
		{
			name: "no healthy vault pods",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
					Spec: vaultv1alpha1.VaultTransitUnsealSpec{
						VaultPod: vaultv1alpha1.VaultPodSpec{
							Namespace: "vault",
							Selector:  map[string]string{"app": "vault"},
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-0",
						Namespace: "vault",
						Labels:    map[string]string{"app": "vault"},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "vault", Ready: true},
						},
					},
				},
			},
			vaultFactory: &mockVaultClientFactory{
				clients: map[string]*mockVaultClient{
					"vault-0": {healthy: false},
				},
			},
			wantReady:   false,
			wantMessage: "no healthy Vault pods (0/1)",
		},
		{
			name: "pod not running",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
					Spec: vaultv1alpha1.VaultTransitUnsealSpec{
						VaultPod: vaultv1alpha1.VaultPodSpec{
							Namespace: "vault",
							Selector:  map[string]string{"app": "vault"},
						},
					},
				},
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
			vaultFactory: &mockVaultClientFactory{},
			wantReady:    false,
			wantMessage:  "no healthy Vault pods (0/1)",
		},
		{
			name: "container not ready",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
					Spec: vaultv1alpha1.VaultTransitUnsealSpec{
						VaultPod: vaultv1alpha1.VaultPodSpec{
							Namespace: "vault",
							Selector:  map[string]string{"app": "vault"},
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "vault-0",
						Namespace: "vault",
						Labels:    map[string]string{"app": "vault"},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{
							{Name: "vault", Ready: false},
						},
					},
				},
			},
			vaultFactory: &mockVaultClientFactory{},
			wantReady:    false,
			wantMessage:  "no healthy Vault pods (0/1)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().
				WithScheme(s).
				WithRuntimeObjects(tt.objects...).
				Build()

			checker.client = client
			checker.vaultFactory = tt.vaultFactory

			err := checker.Readiness(context.Background())

			if tt.wantReady {
				assert.NoError(t, err)
			} else {
				assert.Error(t, err)
			}

			// Check last status
			result, checkTime := checker.GetLastStatus()
			assert.Equal(t, tt.wantReady, result.Ready)
			assert.Equal(t, tt.wantMessage, result.Message)
			assert.WithinDuration(t, time.Now(), checkTime, time.Second)
		})
	}
}

func TestGetLastStatus(t *testing.T) {
	checker, _ := setupTest(t)

	// Initially empty
	result, checkTime := checker.GetLastStatus()
	assert.False(t, result.Healthy)
	assert.False(t, result.Ready)
	assert.Empty(t, result.Message)
	assert.True(t, checkTime.IsZero())

	// Set status
	checker.mu.Lock()
	checker.lastStatus = CheckResult{
		Healthy: true,
		Ready:   true,
		Message: "test message",
	}
	checker.lastCheckTime = time.Now()
	checker.mu.Unlock()

	result, checkTime = checker.GetLastStatus()
	assert.True(t, result.Healthy)
	assert.True(t, result.Ready)
	assert.Equal(t, "test message", result.Message)
	assert.WithinDuration(t, time.Now(), checkTime, time.Second)
}
