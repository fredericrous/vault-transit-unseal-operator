package vault

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
)

// MockVaultServer creates a test HTTP server that mocks Vault API responses
type MockVaultServer struct {
	*httptest.Server
	Sealed           bool
	Initialized      bool
	TransitAvailable bool
	UnsealProgress   int
	UnsealThreshold  int
	RootToken        string
	RecoveryKeys     []string
	EncryptedKeys    map[string]string
}

// NewMockVaultServer creates a new mock Vault server for testing
func NewMockVaultServer() *MockVaultServer {
	m := &MockVaultServer{
		Sealed:           true,
		Initialized:      false,
		TransitAvailable: true,
		UnsealProgress:   0,
		UnsealThreshold:  3,
		RootToken:        "test-root-token",
		RecoveryKeys:     []string{"key1", "key2", "key3", "key4", "key5"},
		EncryptedKeys:    make(map[string]string),
	}

	m.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/sys/health":
			m.handleHealth(w, r)
		case "/v1/sys/seal-status":
			m.handleSealStatus(w, r)
		case "/v1/sys/init":
			m.handleInit(w, r)
		case "/v1/sys/unseal":
			m.handleUnseal(w, r)
		case "/v1/transit/encrypt/unseal_key":
			m.handleTransitEncrypt(w, r)
		case "/v1/transit/decrypt/unseal_key":
			m.handleTransitDecrypt(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))

	return m
}

func (m *MockVaultServer) handleHealth(w http.ResponseWriter, r *http.Request) {
	status := http.StatusOK
	if m.Sealed {
		status = http.StatusServiceUnavailable
	}
	w.WriteHeader(status)
	fmt.Fprintf(w, `{
		"initialized": %t,
		"sealed": %t,
		"standby": false,
		"performance_standby": false,
		"replication_performance_mode": "disabled",
		"replication_dr_mode": "disabled",
		"server_time_utc": %d,
		"version": "1.15.0",
		"cluster_name": "vault-cluster-test",
		"cluster_id": "test-cluster-id"
	}`, m.Initialized, m.Sealed, time.Now().Unix())
}

func (m *MockVaultServer) handleSealStatus(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{
		"type": "shamir",
		"initialized": %t,
		"sealed": %t,
		"t": %d,
		"n": 5,
		"progress": %d,
		"version": "1.15.0",
		"cluster_name": "vault-cluster-test",
		"cluster_id": "test-cluster-id"
	}`, m.Initialized, m.Sealed, m.UnsealThreshold, m.UnsealProgress)
}

func (m *MockVaultServer) handleInit(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if m.Initialized {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"errors":["Vault is already initialized"]}`)
		return
	}

	m.Initialized = true
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{
		"root_token": "%s",
		"recovery_keys_base64": ["%s", "%s", "%s", "%s", "%s"]
	}`, m.RootToken, m.RecoveryKeys[0], m.RecoveryKeys[1], m.RecoveryKeys[2], m.RecoveryKeys[3], m.RecoveryKeys[4])
}

func (m *MockVaultServer) handleUnseal(w http.ResponseWriter, r *http.Request) {
	if r.Method != "PUT" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if !m.Initialized {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"errors":["Vault is not initialized"]}`)
		return
	}

	m.UnsealProgress++
	if m.UnsealProgress >= m.UnsealThreshold {
		m.Sealed = false
		m.UnsealProgress = 0
	}

	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, `{
		"sealed": %t,
		"t": %d,
		"n": 5,
		"progress": %d
	}`, m.Sealed, m.UnsealThreshold, m.UnsealProgress)
}

func (m *MockVaultServer) handleTransitEncrypt(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if !m.TransitAvailable {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"errors":["transit engine not mounted"]}`)
		return
	}

	// Mock encryption - in real tests you might want to parse the request
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"data":{"ciphertext":"vault:v1:mock_encrypted_data"}}`)
}

func (m *MockVaultServer) handleTransitDecrypt(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}

	if !m.TransitAvailable {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"errors":["transit engine not mounted"]}`)
		return
	}

	// Mock decryption - return a base64 encoded test key
	w.WriteHeader(http.StatusOK)
	fmt.Fprint(w, `{"data":{"plaintext":"dGVzdC11bnNlYWwta2V5"}}`)
}

// TestClientBuilder helps create test clients for unit testing
type TestClientBuilder struct {
	scheme  *runtime.Scheme
	objects []ctrlclient.Object
}

// NewTestClientBuilder creates a new test client builder
func NewTestClientBuilder(scheme *runtime.Scheme) *TestClientBuilder {
	return &TestClientBuilder{
		scheme:  scheme,
		objects: []ctrlclient.Object{},
	}
}

// WithObjects adds objects to the test client
func (b *TestClientBuilder) WithObjects(objs ...ctrlclient.Object) *TestClientBuilder {
	b.objects = append(b.objects, objs...)
	return b
}

// Build creates the fake client
func (b *TestClientBuilder) Build() ctrlclient.Client {
	return fake.NewClientBuilder().
		WithScheme(b.scheme).
		WithObjects(b.objects...).
		Build()
}

// MockVaultClient provides a mock implementation of VaultClient for testing
type MockVaultClient struct {
	InitCalled    bool
	UnsealCalled  bool
	IsSealed      bool
	IsInitialized bool
	ShouldError   bool
	ErrorMessage  string
}

// NewMockVaultClient creates a new mock vault client
func NewMockVaultClient() *MockVaultClient {
	return &MockVaultClient{
		IsSealed:      true,
		IsInitialized: false,
		ShouldError:   false,
	}
}

// Init mocks the vault initialization
func (m *MockVaultClient) Init(ctx context.Context, shares, threshold int) (*vaultapi.InitResponse, error) {
	m.InitCalled = true
	if m.ShouldError {
		return nil, fmt.Errorf(m.ErrorMessage)
	}

	m.IsInitialized = true
	return &vaultapi.InitResponse{
		RootToken: "mock-root-token",
		Keys: []string{
			"mock-recovery-key-1",
			"mock-recovery-key-2",
			"mock-recovery-key-3",
		},
	}, nil
}

// Unseal mocks the vault unseal operation
func (m *MockVaultClient) Unseal(ctx context.Context, key string) (*vaultapi.SealStatusResponse, error) {
	m.UnsealCalled = true
	if m.ShouldError {
		return nil, fmt.Errorf(m.ErrorMessage)
	}

	m.IsSealed = false
	return &vaultapi.SealStatusResponse{
		Sealed:   false,
		T:        3,
		N:        5,
		Progress: 3,
	}, nil
}

// SealStatus mocks checking vault seal status
func (m *MockVaultClient) SealStatus(ctx context.Context) (*vaultapi.SealStatusResponse, error) {
	if m.ShouldError {
		return nil, fmt.Errorf(m.ErrorMessage)
	}

	return &vaultapi.SealStatusResponse{
		Sealed:      m.IsSealed,
		T:           3,
		N:           5,
		Progress:    0,
		Initialized: m.IsInitialized,
	}, nil
}

// CreateTestVaultTransitUnseal creates a test VaultTransitUnseal resource
func CreateTestVaultTransitUnseal(name, namespace string) *vaultv1alpha1.VaultTransitUnseal {
	return &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			VaultPod: vaultv1alpha1.VaultPodSpec{
				Namespace: "vault",
				Selector: map[string]string{
					"app.kubernetes.io/name": "vault",
				},
			},
			TransitVault: vaultv1alpha1.TransitVaultSpec{
				Address: "http://transit-vault:8200",
				SecretRef: vaultv1alpha1.SecretReference{
					Name: "vault-transit-token",
					Key:  "token",
				},
				KeyName:   "unseal_key",
				MountPath: "transit",
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
	}
}

// AssertVaultHealthy checks if the vault mock server reports as healthy
func AssertVaultHealthy(mockServer *MockVaultServer) bool {
	return mockServer.Initialized && !mockServer.Sealed
}

// WaitForVaultReady simulates waiting for vault to be ready
func WaitForVaultReady(ctx context.Context, mockServer *MockVaultServer, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)

	for time.Now().Before(deadline) {
		if AssertVaultHealthy(mockServer) {
			return nil
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
			continue
		}
	}

	return fmt.Errorf("vault not ready after %v", timeout)
}
