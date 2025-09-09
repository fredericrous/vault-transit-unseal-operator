package vault_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

func TestVault(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Vault Integration Suite")
}

var _ = Describe("Vault Client Integration Tests", func() {
	var (
		ctx         context.Context
		vaultServer *MockVaultServer
		client      vault.Client
	)

	BeforeEach(func() {
		ctx = context.Background()
		vaultServer = NewMockVaultServer()
		vaultServer.Start()

		var err error
		client, err = vault.NewClient(&vault.Config{
			Address: vaultServer.URL,
			Token:   "mock-root-token",
		})
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if vaultServer != nil {
			vaultServer.Close()
		}
	})

	Context("Status Checking", func() {
		It("should check vault status correctly", func() {
			// Set vault as initialized and unsealed
			vaultServer.SetSealed(false)
			vaultServer.SetInitialized(true)

			status, err := client.CheckStatus(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Initialized).To(BeTrue())
			Expect(status.Sealed).To(BeFalse())
		})

		It("should detect sealed vault", func() {
			vaultServer.SetSealed(true)
			vaultServer.SetInitialized(true)

			status, err := client.CheckStatus(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Initialized).To(BeTrue())
			Expect(status.Sealed).To(BeTrue())
		})

		It("should detect uninitialized vault", func() {
			vaultServer.SetInitialized(false)
			vaultServer.SetSealed(true)

			status, err := client.CheckStatus(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Initialized).To(BeFalse())
			Expect(status.Sealed).To(BeTrue())
		})

		It("should report unhealthy when vault returns error", func() {
			vaultServer.SetHealthy(false)
			isHealthy := client.IsHealthy(ctx)
			Expect(isHealthy).To(BeFalse())
		})

		It("should report healthy when vault is operational", func() {
			vaultServer.SetHealthy(true)
			vaultServer.SetSealed(false)
			vaultServer.SetInitialized(true)

			isHealthy := client.IsHealthy(ctx)
			Expect(isHealthy).To(BeTrue())
		})
	})

	Context("Vault Initialization", func() {
		It("should initialize vault with recovery keys", func() {
			vaultServer.SetInitialized(false)

			req := &vault.InitRequest{
				RecoveryShares:    5,
				RecoveryThreshold: 3,
			}

			resp, err := client.Initialize(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(resp).NotTo(BeNil())
			Expect(resp.RootToken).NotTo(BeEmpty())
			Expect(len(resp.RecoveryKeysB64)).To(Equal(5))

			// Verify vault is now marked as initialized
			status, err := client.CheckStatus(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Initialized).To(BeTrue())
		})

		It("should handle initialization with custom parameters", func() {
			vaultServer.SetInitialized(false)

			req := &vault.InitRequest{
				RecoveryShares:    7,
				RecoveryThreshold: 4,
			}

			resp, err := client.Initialize(ctx, req)
			Expect(err).NotTo(HaveOccurred())
			Expect(len(resp.RecoveryKeysB64)).To(Equal(7))

			// Verify initialization parameters were used
			initData := vaultServer.GetInitializationData()
			Expect(initData["recovery_shares"]).To(Equal(float64(7)))
			Expect(initData["recovery_threshold"]).To(Equal(float64(4)))
		})

		It("should fail to initialize already initialized vault", func() {
			vaultServer.SetInitialized(true)

			req := &vault.InitRequest{
				RecoveryShares:    3,
				RecoveryThreshold: 2,
			}

			_, err := client.Initialize(ctx, req)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("already initialized"))
		})
	})

	Context("Auth Methods", func() {
		It("should enable kubernetes auth method", func() {
			err := client.EnableAuth(ctx, "kubernetes", "kubernetes")
			Expect(err).NotTo(HaveOccurred())

			enabled, err := client.AuthEnabled(ctx, "kubernetes")
			Expect(err).NotTo(HaveOccurred())
			Expect(enabled).To(BeTrue())
		})

		It("should detect existing auth methods", func() {
			// Pre-enable kubernetes auth
			vaultServer.AddAuth("kubernetes", map[string]interface{}{
				"type": "kubernetes",
			})

			enabled, err := client.AuthEnabled(ctx, "kubernetes")
			Expect(err).NotTo(HaveOccurred())
			Expect(enabled).To(BeTrue())
		})

		It("should not enable already enabled auth method", func() {
			// Enable it first
			err := client.EnableAuth(ctx, "kubernetes", "kubernetes")
			Expect(err).NotTo(HaveOccurred())

			// Try to enable again - should not error
			err = client.EnableAuth(ctx, "kubernetes", "kubernetes")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should write auth configuration", func() {
			// Enable auth first
			err := client.EnableAuth(ctx, "kubernetes", "kubernetes")
			Expect(err).NotTo(HaveOccurred())

			config := map[string]interface{}{
				"kubernetes_host": "https://kubernetes.default.svc:443",
				"issuer":          "kubernetes/serviceaccount",
			}

			err = client.WriteAuth(ctx, "kubernetes/config", config)
			Expect(err).NotTo(HaveOccurred())

			// Verify config was written
			storedConfig := vaultServer.GetAuthConfig("kubernetes", "config")
			Expect(storedConfig["kubernetes_host"]).To(Equal("https://kubernetes.default.svc:443"))
			Expect(storedConfig["issuer"]).To(Equal("kubernetes/serviceaccount"))
		})
	})

	Context("Secret Engines", func() {
		It("should mount KV v2 secret engine", func() {
			input := &vault.MountInput{
				Type: "kv",
				Options: map[string]interface{}{
					"version": "2",
				},
			}

			err := client.MountSecretEngine(ctx, "secret", input)
			Expect(err).NotTo(HaveOccurred())

			exists, err := client.MountExists(ctx, "secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue())
		})

		It("should detect existing mounts", func() {
			// Pre-mount a secret engine
			vaultServer.AddMount("existing", map[string]interface{}{
				"type": "kv",
				"options": map[string]interface{}{
					"version": "2",
				},
			})

			exists, err := client.MountExists(ctx, "existing")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue())
		})

		It("should mount custom secret engines", func() {
			input := &vault.MountInput{
				Type: "database",
				Config: map[string]interface{}{
					"max_lease_ttl": "1h",
				},
			}

			err := client.MountSecretEngine(ctx, "database", input)
			Expect(err).NotTo(HaveOccurred())

			mount := vaultServer.GetMount("database")
			Expect(mount["type"]).To(Equal("database"))
		})
	})

	Context("Policy Management", func() {
		It("should write ACL policies", func() {
			policyHCL := `
path "secret/data/*" {
  capabilities = ["read", "list"]
}

path "secret/metadata/*" {
  capabilities = ["read", "list"]
}
`

			err := client.WritePolicy(ctx, "read-only", policyHCL)
			Expect(err).NotTo(HaveOccurred())

			policy := vaultServer.GetPolicy("read-only")
			Expect(policy).To(ContainSubstring("secret/data/*"))
			Expect(policy).To(ContainSubstring("read"))
		})

		It("should handle complex policies", func() {
			policyHCL := `
# Admin policy for external secrets operator
path "secret/*" {
  capabilities = ["create", "read", "update", "delete", "list"]
}

path "sys/mounts" {
  capabilities = ["read"]
}

path "auth/token/lookup-self" {
  capabilities = ["read"]
}
`

			err := client.WritePolicy(ctx, "admin", policyHCL)
			Expect(err).NotTo(HaveOccurred())

			policy := vaultServer.GetPolicy("admin")
			Expect(policy).To(ContainSubstring("secret/*"))
			Expect(policy).To(ContainSubstring("sys/mounts"))
			Expect(policy).To(ContainSubstring("auth/token/lookup-self"))
		})
	})

	Context("Integration Scenarios", func() {
		It("should support complete ESO setup workflow", func() {
			By("Checking initial status")
			status, err := client.CheckStatus(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Initialized).To(BeTrue())
			Expect(status.Sealed).To(BeFalse())

			By("Enabling Kubernetes auth")
			err = client.EnableAuth(ctx, "kubernetes", "kubernetes")
			Expect(err).NotTo(HaveOccurred())

			By("Configuring Kubernetes auth")
			authConfig := map[string]interface{}{
				"kubernetes_host": "https://kubernetes.default.svc:443",
			}
			err = client.WriteAuth(ctx, "kubernetes/config", authConfig)
			Expect(err).NotTo(HaveOccurred())

			By("Creating policy for ESO")
			policyHCL := `
path "secret/data/*" {
  capabilities = ["read"]
}
path "auth/token/lookup-self" {
  capabilities = ["read"]
}
`
			err = client.WritePolicy(ctx, "external-secrets", policyHCL)
			Expect(err).NotTo(HaveOccurred())

			By("Creating role for service accounts")
			roleConfig := map[string]interface{}{
				"bound_service_account_names":      []string{"external-secrets"},
				"bound_service_account_namespaces": []string{"external-secrets"},
				"policies":                         []string{"external-secrets"},
				"ttl":                              "1h",
			}
			err = client.WriteAuth(ctx, "kubernetes/role/external-secrets", roleConfig)
			Expect(err).NotTo(HaveOccurred())

			By("Mounting KV engine")
			mountInput := &vault.MountInput{
				Type: "kv",
				Options: map[string]interface{}{
					"version": "2",
				},
			}
			err = client.MountSecretEngine(ctx, "secret", mountInput)
			Expect(err).NotTo(HaveOccurred())

			// Verify complete setup
			Expect(vaultServer.HasAuth("kubernetes")).To(BeTrue(), "kubernetes auth should be enabled")
			Expect(vaultServer.HasPolicy("external-secrets")).To(BeTrue(), "external-secrets policy should exist")
			Expect(vaultServer.HasMount("secret")).To(BeTrue(), "secret mount should exist")

			Expect(vaultServer.HasRole("external-secrets")).To(BeTrue(), "external-secrets role should exist")
		})

		It("should handle idempotent operations", func() {
			// Perform setup twice to test idempotency
			for i := 0; i < 2; i++ {
				By(fmt.Sprintf("Running setup iteration %d", i+1))

				err := client.EnableAuth(ctx, "kubernetes", "kubernetes")
				Expect(err).NotTo(HaveOccurred())

				exists, err := client.MountExists(ctx, "secret")
				if !exists {
					err = client.MountSecretEngine(ctx, "secret", &vault.MountInput{
						Type:    "kv",
						Options: map[string]interface{}{"version": "2"},
					})
					Expect(err).NotTo(HaveOccurred())
				}

				err = client.WritePolicy(ctx, "test-policy", `path "secret/*" { capabilities = ["read"] }`)
				Expect(err).NotTo(HaveOccurred())
			}

			// Verify final state is correct
			enabled, err := client.AuthEnabled(ctx, "kubernetes")
			Expect(err).NotTo(HaveOccurred())
			Expect(enabled).To(BeTrue())

			exists, err := client.MountExists(ctx, "secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue())

			Expect(vaultServer.HasPolicy("test-policy")).To(BeTrue())
		})
	})

	Context("Error Handling", func() {
		It("should handle connection failures", func() {
			vaultServer.Close()

			_, err := client.CheckStatus(ctx)
			Expect(err).To(HaveOccurred())
		})

		It("should handle API errors", func() {
			vaultServer.SetErrorMode(true)

			_, err := client.CheckStatus(ctx)
			Expect(err).To(HaveOccurred())
		})

		It("should handle malformed responses", func() {
			vaultServer.SetMalformedResponse(true)

			_, err := client.CheckStatus(ctx)
			Expect(err).To(HaveOccurred())
		})
	})
})

// MockVaultServer simulates a Vault server for integration testing
type MockVaultServer struct {
	server             *httptest.Server
	URL                string
	mu                 sync.RWMutex
	initialized        bool
	sealed             bool
	healthy            bool
	errorMode          bool
	malformedResponse  bool
	initializationData map[string]interface{}
	mounts             map[string]map[string]interface{}
	auths              map[string]map[string]interface{}
	policies           map[string]string
	authConfigs        map[string]map[string]map[string]interface{}
}

func NewMockVaultServer() *MockVaultServer {
	return &MockVaultServer{
		initialized:        true, // Default to initialized for most tests
		sealed:             false,
		healthy:            true,
		initializationData: make(map[string]interface{}),
		mounts:             make(map[string]map[string]interface{}),
		auths:              make(map[string]map[string]interface{}),
		policies:           make(map[string]string),
		authConfigs:        make(map[string]map[string]map[string]interface{}),
	}
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

func (m *MockVaultServer) SetInitialized(initialized bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initialized = initialized
}

func (m *MockVaultServer) SetSealed(sealed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sealed = sealed
}

func (m *MockVaultServer) SetHealthy(healthy bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthy = healthy
}

func (m *MockVaultServer) SetErrorMode(errorMode bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.errorMode = errorMode
}

func (m *MockVaultServer) SetMalformedResponse(malformed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.malformedResponse = malformed
}

func (m *MockVaultServer) GetInitializationData() map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.initializationData
}

func (m *MockVaultServer) AddAuth(path string, config map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.auths[path] = config
}

func (m *MockVaultServer) HasAuth(path string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.auths[path]
	return exists
}

func (m *MockVaultServer) AddMount(path string, config map[string]interface{}) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Ensure config has proper structure
	if _, hasConfig := config["config"]; !hasConfig {
		config["config"] = map[string]interface{}{
			"default_lease_ttl": 0,
			"max_lease_ttl":     0,
		}
	}
	m.mounts[path] = config
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
	if m.authConfigs["kubernetes"] == nil {
		return false
	}
	_, exists := m.authConfigs["kubernetes"]["role/"+name]
	return exists
}

func (m *MockVaultServer) GetAuthConfig(authMethod, configPath string) map[string]interface{} {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if method, exists := m.authConfigs[authMethod]; exists {
		return method[configPath]
	}
	return nil
}

func (m *MockVaultServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.errorMode {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}

	if m.malformedResponse {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "invalid json response")
		return
	}

	path := r.URL.Path

	switch {
	case path == "/v1/sys/health":
		// Parse query params to check for special status code requests
		sealedCode := r.URL.Query().Get("sealedcode")
		uninitCode := r.URL.Query().Get("uninitcode")

		status := http.StatusOK
		if !m.healthy {
			status = http.StatusServiceUnavailable
		} else if !m.initialized && uninitCode != "" {
			// Use uninit code if specified
			if code, err := strconv.Atoi(uninitCode); err == nil {
				status = code
			} else {
				status = http.StatusServiceUnavailable
			}
		} else if m.sealed && sealedCode != "" {
			// Use sealed code if specified
			if code, err := strconv.Atoi(sealedCode); err == nil {
				status = code
			} else {
				status = http.StatusServiceUnavailable
			}
		}

		w.WriteHeader(status)
		response := map[string]interface{}{
			"initialized": m.initialized,
			"sealed":      m.sealed,
			"standby":     false,
		}
		json.NewEncoder(w).Encode(response)

	case path == "/v1/sys/seal-status":
		w.WriteHeader(http.StatusOK)
		response := map[string]interface{}{
			"sealed":      m.sealed,
			"initialized": m.initialized,
		}
		json.NewEncoder(w).Encode(response)

	case path == "/v1/sys/init":
		if r.Method == "PUT" {
			if m.initialized {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": []string{"Vault is already initialized"},
				})
				return
			}

			var initReq map[string]interface{}
			json.NewDecoder(r.Body).Decode(&initReq)
			m.initializationData = initReq

			shares := int(initReq["recovery_shares"].(float64))
			keys := make([]string, shares)
			for i := 0; i < shares; i++ {
				keys[i] = base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("recovery-key-%d", i)))
			}

			response := map[string]interface{}{
				"root_token":           "mock-root-token-" + fmt.Sprintf("%d", time.Now().Unix()),
				"recovery_keys_base64": keys,
			}

			m.initialized = true
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(response)
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
			var authReq map[string]interface{}
			json.NewDecoder(r.Body).Decode(&authReq)
			// Store auth with proper structure
			m.auths[authPath] = map[string]interface{}{
				"type":        authReq["type"],
				"description": authReq["description"],
				"config": map[string]interface{}{
					"default_lease_ttl": 0,
					"max_lease_ttl":     0,
				},
			}
			w.WriteHeader(http.StatusNoContent)
		}

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
			var mountReq map[string]interface{}
			json.NewDecoder(r.Body).Decode(&mountReq)
			// Store mount with proper structure
			mountConfig := map[string]interface{}{
				"type":        mountReq["type"],
				"description": mountReq["description"],
				"config": map[string]interface{}{
					"default_lease_ttl": 0,
					"max_lease_ttl":     0,
				},
			}
			// Copy options if present
			if opts, ok := mountReq["options"]; ok {
				mountConfig["options"] = opts
			}
			m.mounts[mountPath] = mountConfig
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

	case strings.HasPrefix(path, "/v1/auth/"):
		// Handle auth configuration endpoints like /v1/auth/kubernetes/config
		authPath := strings.TrimPrefix(path, "/v1/auth/")
		// Split to get auth method and config path
		parts := strings.SplitN(authPath, "/", 2)
		if len(parts) >= 1 {
			authMethod := parts[0]
			configPath := ""
			if len(parts) > 1 {
				configPath = parts[1]
			}

			if r.Method == "POST" || r.Method == "PUT" {
				var config map[string]interface{}
				if err := json.NewDecoder(r.Body).Decode(&config); err != nil {
					w.WriteHeader(http.StatusBadRequest)
					return
				}

				if m.authConfigs[authMethod] == nil {
					m.authConfigs[authMethod] = make(map[string]map[string]interface{})
				}
				m.authConfigs[authMethod][configPath] = config
				w.WriteHeader(http.StatusNoContent)
			} else if r.Method == "GET" {
				if m.authConfigs[authMethod] != nil && m.authConfigs[authMethod][configPath] != nil {
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"data": m.authConfigs[authMethod][configPath],
					})
				} else {
					w.WriteHeader(http.StatusNotFound)
				}
			}
		}

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}
