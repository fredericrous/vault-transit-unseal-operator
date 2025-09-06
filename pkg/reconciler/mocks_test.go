package reconciler

import (
	"context"
	"fmt"
	"time"

	"github.com/fredericrous/homelab/vault-operator/pkg/vault"
	corev1 "k8s.io/api/core/v1"
)

// Mock implementations for testing

type mockVaultClient struct {
	status      *vault.Status
	statusErr   error
	initResp    *vault.InitResponse
	initErr     error
	healthy     bool
	initCalled  int
	statusCalls int
}

func (m *mockVaultClient) CheckStatus(ctx context.Context) (*vault.Status, error) {
	m.statusCalls++
	return m.status, m.statusErr
}

func (m *mockVaultClient) Initialize(ctx context.Context, shares, threshold int) (*vault.InitResponse, error) {
	m.initCalled++
	return m.initResp, m.initErr
}

func (m *mockVaultClient) IsHealthy(ctx context.Context) bool {
	return m.healthy
}

type mockVaultClientFactory struct {
	client    vault.Client
	createErr error
	podsSeen  []string
}

func (m *mockVaultClientFactory) NewClientForPod(pod *corev1.Pod) (vault.Client, error) {
	m.podsSeen = append(m.podsSeen, pod.Name)
	return m.client, m.createErr
}

type mockSecretManager struct {
	secrets   map[string]map[string][]byte
	getErr    error
	createErr error
	calls     []string
}

func newMockSecretManager() *mockSecretManager {
	return &mockSecretManager{
		secrets: make(map[string]map[string][]byte),
		calls:   []string{},
	}
}

func (m *mockSecretManager) CreateOrUpdate(ctx context.Context, namespace, name string, data map[string][]byte) error {
	m.calls = append(m.calls, fmt.Sprintf("CreateOrUpdate:%s/%s", namespace, name))
	if m.createErr != nil {
		return m.createErr
	}
	if m.secrets[namespace] == nil {
		m.secrets[namespace] = make(map[string][]byte)
	}
	key := fmt.Sprintf("%s/%s", namespace, name)
	m.secrets[key] = data
	return nil
}

func (m *mockSecretManager) Get(ctx context.Context, namespace, name, key string) ([]byte, error) {
	m.calls = append(m.calls, fmt.Sprintf("Get:%s/%s/%s", namespace, name, key))
	if m.getErr != nil {
		return nil, m.getErr
	}
	secretKey := fmt.Sprintf("%s/%s", namespace, name)
	if secret, ok := m.secrets[secretKey]; ok {
		if val, ok := secret[key]; ok {
			return val, nil
		}
	}
	return nil, fmt.Errorf("key not found")
}

func (m *mockSecretManager) setSecret(namespace, name string, data map[string][]byte) {
	key := fmt.Sprintf("%s/%s", namespace, name)
	m.secrets[key] = data
}

type mockMetricsRecorder struct {
	reconciliations []reconciliationRecord
	vaultStatuses   []vaultStatusRecord
	initializations []bool
}

type reconciliationRecord struct {
	duration time.Duration
	success  bool
}

type vaultStatusRecord struct {
	initialized bool
	sealed      bool
}

func (m *mockMetricsRecorder) RecordReconciliation(duration time.Duration, success bool) {
	m.reconciliations = append(m.reconciliations, reconciliationRecord{
		duration: duration,
		success:  success,
	})
}

func (m *mockMetricsRecorder) RecordVaultStatus(initialized, sealed bool) {
	m.vaultStatuses = append(m.vaultStatuses, vaultStatusRecord{
		initialized: initialized,
		sealed:      sealed,
	})
}

func (m *mockMetricsRecorder) RecordInitialization(success bool) {
	m.initializations = append(m.initializations, success)
}
