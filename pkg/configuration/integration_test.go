package configuration_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/go-logr/logr/testr"
	vaultapi "github.com/hashicorp/vault/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/configuration"
)

func TestConfiguration(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Configuration Integration Suite")
}

var _ = Describe("Configurator Integration Tests", func() {
	var (
		ctx          context.Context
		configurator *configuration.Configurator
		vaultServer  *MockVaultServer
		vaultClient  *vaultapi.Client
	)

	BeforeEach(func() {
		ctx = context.Background()
		configurator = configuration.NewConfigurator(testr.NewWithOptions(&testing.T{}, testr.Options{}))

		// Create mock vault server with realistic behavior
		vaultServer = NewMockVaultServer()
		vaultServer.Start()

		// Create real Vault API client pointing to mock server
		vaultConfig := vaultapi.DefaultConfig()
		vaultConfig.Address = vaultServer.URL
		var err error
		vaultClient, err = vaultapi.NewClient(vaultConfig)
		Expect(err).NotTo(HaveOccurred())
		vaultClient.SetToken("mock-root-token")
	})

	AfterEach(func() {
		if vaultServer != nil {
			vaultServer.Close()
		}
	})

	Context("KV Engine Configuration", func() {
		It("should enable KV v2 engine with default settings", func() {
			config := vaultv1alpha1.PostUnsealConfig{
				EnableKV: true,
				KVConfig: vaultv1alpha1.KVConfig{
					Path:    "secret",
					Version: 2,
				},
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).NotTo(HaveOccurred())

			Expect(status.KVConfigured).To(BeTrue())
			Expect(status.KVConfiguredTime).NotTo(BeEmpty())

			// Verify the mount was created
			Expect(vaultServer.HasMount("secret")).To(BeTrue())
			mount := vaultServer.GetMount("secret")
			Expect(mount["type"]).To(Equal("kv"))
			Expect(mount["options"].(map[string]interface{})["version"]).To(Equal("2"))
		})

		It("should skip KV configuration if already configured", func() {
			// Pre-configure KV engine
			vaultServer.AddMount("secret", map[string]interface{}{
				"type": "kv",
				"options": map[string]interface{}{
					"version": "2",
				},
			})

			config := vaultv1alpha1.PostUnsealConfig{
				EnableKV: true,
				KVConfig: vaultv1alpha1.KVConfig{
					Path:    "secret",
					Version: 2,
				},
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).NotTo(HaveOccurred())

			Expect(status.KVConfigured).To(BeTrue())
		})

		It("should configure KV with custom path and settings", func() {
			config := vaultv1alpha1.PostUnsealConfig{
				EnableKV: true,
				KVConfig: vaultv1alpha1.KVConfig{
					Path:               "custom-secret",
					Version:            2,
					MaxVersions:        10,
					DeleteVersionAfter: "30d",
				},
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).NotTo(HaveOccurred())

			Expect(status.KVConfigured).To(BeTrue())
			Expect(vaultServer.HasMount("custom-secret")).To(BeTrue())
		})

		It("should handle mount conflicts gracefully", func() {
			// Pre-configure different engine at same path
			vaultServer.AddMount("secret", map[string]interface{}{
				"type": "generic",
			})

			config := vaultv1alpha1.PostUnsealConfig{
				EnableKV: true,
				KVConfig: vaultv1alpha1.KVConfig{
					Path:    "secret",
					Version: 2,
				},
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already exists with different type"))
		})
	})

	Context("External Secrets Operator Configuration", func() {
		It("should configure ESO with default settings", func() {
			config := vaultv1alpha1.PostUnsealConfig{
				EnableExternalSecretsOperator: true,
				ExternalSecretsOperatorConfig: vaultv1alpha1.ExternalSecretsOperatorConfig{
					PolicyName: "admin",
					KubernetesAuth: vaultv1alpha1.KubernetesAuthConfig{
						RoleName: "external-secrets-operator",
						ServiceAccounts: []vaultv1alpha1.ServiceAccountRef{
							{
								Name:      "external-secrets",
								Namespace: "external-secrets",
							},
						},
					},
				},
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).NotTo(HaveOccurred())

			Expect(status.ExternalSecretsOperatorConfigured).To(BeTrue())
			Expect(status.ExternalSecretsOperatorConfiguredTime).NotTo(BeEmpty())

			// Verify policy was created
			Expect(vaultServer.HasPolicy("admin")).To(BeTrue())
			policy := vaultServer.GetPolicy("admin")
			Expect(policy).To(ContainSubstring("secret/data/*"))

			// Verify Kubernetes auth was enabled
			Expect(vaultServer.HasAuth("kubernetes")).To(BeTrue())

			// Verify role was created
			Expect(vaultServer.HasRole("external-secrets-operator")).To(BeTrue())
		})

		It("should skip auth setup if already enabled", func() {
			// Pre-enable Kubernetes auth
			vaultServer.AddAuth("kubernetes", map[string]interface{}{
				"type": "kubernetes",
			})

			config := vaultv1alpha1.PostUnsealConfig{
				EnableExternalSecretsOperator: true,
				ExternalSecretsOperatorConfig: vaultv1alpha1.ExternalSecretsOperatorConfig{
					PolicyName: "custom-policy",
					KubernetesAuth: vaultv1alpha1.KubernetesAuthConfig{
						RoleName: "custom-role",
					},
				},
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).NotTo(HaveOccurred())

			Expect(status.ExternalSecretsOperatorConfigured).To(BeTrue())
		})

		It("should configure multiple service accounts", func() {
			config := vaultv1alpha1.PostUnsealConfig{
				EnableExternalSecretsOperator: true,
				ExternalSecretsOperatorConfig: vaultv1alpha1.ExternalSecretsOperatorConfig{
					PolicyName: "multi-sa-policy",
					KubernetesAuth: vaultv1alpha1.KubernetesAuthConfig{
						RoleName: "multi-sa-role",
						ServiceAccounts: []vaultv1alpha1.ServiceAccountRef{
							{Name: "sa1", Namespace: "ns1"},
							{Name: "sa2", Namespace: "ns2"},
							{Name: "sa3", Namespace: "ns3"},
						},
						TTL:    "1h",
						MaxTTL: "24h",
					},
				},
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).NotTo(HaveOccurred())

			// Verify role configuration
			role := vaultServer.GetRole("multi-sa-role")
			Expect(role["bound_service_account_names"]).To(Equal([]interface{}{"sa1", "sa2", "sa3"}))
			Expect(role["bound_service_account_namespaces"]).To(Equal([]interface{}{"ns1", "ns2", "ns3"}))
			Expect(role["ttl"]).To(Equal("1h"))
			Expect(role["max_ttl"]).To(Equal("24h"))
		})
	})

	Context("Combined Configuration", func() {
		It("should configure both KV and ESO", func() {
			config := vaultv1alpha1.PostUnsealConfig{
				EnableKV:                      true,
				EnableExternalSecretsOperator: true,
				KVConfig: vaultv1alpha1.KVConfig{
					Path:    "secrets",
					Version: 2,
				},
				ExternalSecretsOperatorConfig: vaultv1alpha1.ExternalSecretsOperatorConfig{
					PolicyName: "combined-policy",
					KubernetesAuth: vaultv1alpha1.KubernetesAuthConfig{
						RoleName: "combined-role",
					},
				},
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).NotTo(HaveOccurred())

			Expect(status.KVConfigured).To(BeTrue())
			Expect(status.ExternalSecretsOperatorConfigured).To(BeTrue())
			Expect(vaultServer.HasMount("secrets")).To(BeTrue())
			Expect(vaultServer.HasPolicy("combined-policy")).To(BeTrue())
			Expect(vaultServer.HasAuth("kubernetes")).To(BeTrue())
		})

		It("should handle partial configuration gracefully", func() {
			config := vaultv1alpha1.PostUnsealConfig{
				EnableKV:                      true,
				EnableExternalSecretsOperator: true,
				KVConfig: vaultv1alpha1.KVConfig{
					Path: "partial",
				},
				ExternalSecretsOperatorConfig: vaultv1alpha1.ExternalSecretsOperatorConfig{
					// PolicyName will use default
					KubernetesAuth: vaultv1alpha1.KubernetesAuthConfig{
						// RoleName will use default
						// ServiceAccounts will use default
					},
				},
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).NotTo(HaveOccurred())

			// Verify defaults were applied
			Expect(vaultServer.HasMount("partial")).To(BeTrue())
			Expect(vaultServer.HasPolicy("external-secrets-operator")).To(BeTrue())
			Expect(vaultServer.HasRole("external-secrets-operator")).To(BeTrue())
		})
	})

	Context("Idempotency", func() {
		It("should be safe to run configuration multiple times", func() {
			config := vaultv1alpha1.PostUnsealConfig{
				EnableKV:                      true,
				EnableExternalSecretsOperator: true,
				KVConfig: vaultv1alpha1.KVConfig{
					Path: "idempotent",
				},
				ExternalSecretsOperatorConfig: vaultv1alpha1.ExternalSecretsOperatorConfig{
					PolicyName: "idempotent-policy",
					KubernetesAuth: vaultv1alpha1.KubernetesAuthConfig{
						RoleName: "idempotent-role",
					},
				},
			}

			// Run configuration first time
			status1 := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status1)
			Expect(err).NotTo(HaveOccurred())

			// Mark as configured
			status1.KVConfigured = true
			status1.ExternalSecretsOperatorConfigured = true

			// Run configuration second time - should be no-op
			status2 := &vaultv1alpha1.ConfigurationStatus{}
			status2.KVConfigured = true
			status2.ExternalSecretsOperatorConfigured = true
			err = configurator.Configure(ctx, vaultClient, config, status2)
			Expect(err).NotTo(HaveOccurred())

			// Verify mounts and configs still exist
			Expect(vaultServer.HasMount("idempotent")).To(BeTrue())
			Expect(vaultServer.HasPolicy("idempotent-policy")).To(BeTrue())
		})
	})

	Context("Error Handling", func() {
		It("should handle vault connection failures", func() {
			// Close the server to simulate connection failure
			vaultServer.Close()

			config := vaultv1alpha1.PostUnsealConfig{
				EnableKV: true,
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err := configurator.Configure(ctx, vaultClient, config, status)
			Expect(err).To(HaveOccurred())
			Expect(status.KVConfigured).To(BeFalse())
		})

		It("should handle invalid vault responses", func() {
			// Create a server that returns invalid responses
			invalidServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusInternalServerError)
				fmt.Fprint(w, "Internal Server Error")
			}))
			defer invalidServer.Close()

			invalidConfig := vaultapi.DefaultConfig()
			invalidConfig.Address = invalidServer.URL
			invalidClient, err := vaultapi.NewClient(invalidConfig)
			Expect(err).NotTo(HaveOccurred())

			config := vaultv1alpha1.PostUnsealConfig{
				EnableKV: true,
			}

			status := &vaultv1alpha1.ConfigurationStatus{}
			err = configurator.Configure(ctx, invalidClient, config, status)
			Expect(err).To(HaveOccurred())
		})
	})
})

// MockVaultServer simulates a Vault server for testing
type MockVaultServer struct {
	server *httptest.Server
	URL    string
	mu     sync.RWMutex

	mounts   map[string]map[string]interface{}
	auths    map[string]map[string]interface{}
	policies map[string]string
	roles    map[string]map[string]interface{}
	config   map[string]map[string]interface{}
}

func NewMockVaultServer() *MockVaultServer {
	mock := &MockVaultServer{
		mounts:   make(map[string]map[string]interface{}),
		auths:    make(map[string]map[string]interface{}),
		policies: make(map[string]string),
		roles:    make(map[string]map[string]interface{}),
		config:   make(map[string]map[string]interface{}),
	}
	return mock
}

func (m *MockVaultServer) Start() {
	m.server = httptest.NewServer(http.HandlerFunc(m.handleRequest))
	m.URL = m.server.URL
}

func (m *MockVaultServer) Close() {
	if m.server != nil {
		m.server.Close()
	}
}

func (m *MockVaultServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	path := r.URL.Path

	switch {
	case path == "/v1/sys/mounts":
		if r.Method == "GET" {
			mountList := make(map[string]interface{})
			for path, mount := range m.mounts {
				mountList[path+"/"] = mount
			}
			response := map[string]interface{}{
				"data": mountList,
			}
			json.NewEncoder(w).Encode(response)
		}

	case strings.HasPrefix(path, "/v1/sys/mounts/"):
		mountPath := strings.TrimPrefix(path, "/v1/sys/mounts/")
		if r.Method == "POST" {
			var mount map[string]interface{}
			json.NewDecoder(r.Body).Decode(&mount)
			m.mounts[mountPath] = mount
			w.WriteHeader(http.StatusNoContent)
		}

	case path == "/v1/sys/auth":
		if r.Method == "GET" {
			authList := make(map[string]interface{})
			for path, auth := range m.auths {
				authList[path+"/"] = auth
			}
			response := map[string]interface{}{
				"data": authList,
			}
			json.NewEncoder(w).Encode(response)
		}

	case strings.HasPrefix(path, "/v1/sys/auth/"):
		authPath := strings.TrimPrefix(path, "/v1/sys/auth/")
		if r.Method == "POST" {
			var auth map[string]interface{}
			json.NewDecoder(r.Body).Decode(&auth)
			m.auths[authPath] = auth
			w.WriteHeader(http.StatusNoContent)
		}

	case strings.HasPrefix(path, "/v1/sys/policies/acl/"):
		policyName := strings.TrimPrefix(path, "/v1/sys/policies/acl/")
		if r.Method == "PUT" {
			var policy map[string]interface{}
			json.NewDecoder(r.Body).Decode(&policy)
			m.policies[policyName] = policy["policy"].(string)
			w.WriteHeader(http.StatusNoContent)
		}

	case strings.HasPrefix(path, "/v1/auth/kubernetes/"):
		subPath := strings.TrimPrefix(path, "/v1/auth/kubernetes/")
		if r.Method == "POST" || r.Method == "PUT" {
			var data map[string]interface{}
			json.NewDecoder(r.Body).Decode(&data)

			if subPath == "config" {
				m.config["kubernetes"] = data
			} else if strings.HasPrefix(subPath, "role/") {
				roleName := strings.TrimPrefix(subPath, "role/")
				m.roles[roleName] = data
			}
			w.WriteHeader(http.StatusNoContent)
		}

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (m *MockVaultServer) HasMount(path string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.mounts[path]
	return exists
}

func (m *MockVaultServer) GetMount(path string) map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.mounts[path]
}

func (m *MockVaultServer) AddMount(path string, mount map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.mounts[path] = mount
}

func (m *MockVaultServer) HasAuth(path string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.auths[path]
	return exists
}

func (m *MockVaultServer) AddAuth(path string, auth map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auths[path] = auth
}

func (m *MockVaultServer) HasPolicy(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.policies[name]
	return exists
}

func (m *MockVaultServer) GetPolicy(name string) string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.policies[name]
}

func (m *MockVaultServer) HasRole(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.roles[name]
	return exists
}

func (m *MockVaultServer) GetRole(name string) map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.roles[name]
}
