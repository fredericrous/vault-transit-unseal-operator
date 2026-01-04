package health

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	vaultapi "github.com/hashicorp/vault/api"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

type mockVaultClient struct {
	healthy bool
}

func (m *mockVaultClient) CheckStatus(ctx context.Context) (*vault.Status, error) {
	return &vault.Status{Initialized: true, Sealed: false}, nil
}

func (m *mockVaultClient) Initialize(ctx context.Context, req *vault.InitRequest) (*vault.InitResponse, error) {
	return nil, fmt.Errorf("not implemented")
}

func (m *mockVaultClient) EnableAuth(ctx context.Context, path, authType string) error {
	return fmt.Errorf("not implemented")
}

func (m *mockVaultClient) AuthEnabled(ctx context.Context, path string) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func (m *mockVaultClient) WriteAuth(ctx context.Context, path string, data map[string]interface{}) error {
	return fmt.Errorf("not implemented")
}

func (m *mockVaultClient) MountExists(ctx context.Context, path string) (bool, error) {
	return false, fmt.Errorf("not implemented")
}

func (m *mockVaultClient) MountSecretEngine(ctx context.Context, path string, input *vault.MountInput) error {
	return fmt.Errorf("not implemented")
}

func (m *mockVaultClient) WritePolicy(ctx context.Context, name, policy string) error {
	return fmt.Errorf("not implemented")
}

func (m *mockVaultClient) IsHealthy(ctx context.Context) bool {
	return m.healthy
}

func (m *mockVaultClient) GetAPIClient() *vaultapi.Client {
	return nil
}

type mockVaultClientFactory struct {
	clients map[string]*mockVaultClient
	err     error
}

func (m *mockVaultClientFactory) NewClientForPod(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) (vault.Client, error) {
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
		name          string
		objects       []runtime.Object
		vaultFactory  VaultClientFactory
		clientWrapper func(ctrlclient.WithWatch) ctrlclient.WithWatch
		wantReady     bool
		wantHealthy   bool
		wantMessage   string
	}{
		{
			name:         "no resources to manage",
			objects:      []runtime.Object{},
			vaultFactory: &mockVaultClientFactory{},
			wantReady:    true,
			wantHealthy:  true,
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
			wantReady:    true,
			wantHealthy:  false,
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
			wantHealthy: true,
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
			wantHealthy: true,
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
			wantReady:   true,
			wantHealthy: false,
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
			wantReady:    true,
			wantHealthy:  false,
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
			wantReady:    true,
			wantHealthy:  false,
			wantMessage:  "no healthy Vault pods (0/1)",
		},
		{
			name:    "list resources fails",
			objects: []runtime.Object{},
			vaultFactory: &mockVaultClientFactory{
				clients: map[string]*mockVaultClient{},
			},
			clientWrapper: func(c ctrlclient.WithWatch) ctrlclient.WithWatch {
				return &errorListClient{WithWatch: c}
			},
			wantReady:   false,
			wantHealthy: false,
			wantMessage: "failed to list VaultTransitUnseal resources: simulated failure",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().
				WithScheme(s).
				WithRuntimeObjects(tt.objects...).
				Build()
			if tt.clientWrapper != nil {
				client = tt.clientWrapper(client)
			}

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
			assert.Equal(t, tt.wantHealthy, result.Healthy)
			assert.Equal(t, tt.wantMessage, result.Message)
			assert.WithinDuration(t, time.Now(), checkTime, time.Second)
		})
	}
}

type errorListClient struct {
	ctrlclient.WithWatch
}

func (e *errorListClient) List(ctx context.Context, list ctrlclient.ObjectList, opts ...ctrlclient.ListOption) error {
	return fmt.Errorf("simulated failure")
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
