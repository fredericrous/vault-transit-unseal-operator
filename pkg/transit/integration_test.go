package transit_test

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/transit"
)

func TestTransit(t *testing.T) {
	RegisterFailHandler(Fail)
	RunSpecs(t, "Transit Integration Suite")
}

var _ = Describe("Transit Client Integration Tests", func() {
	var (
		ctx           context.Context
		transitServer *MockTransitVaultServer
		targetServer  *MockTargetVaultServer
		client        *transit.Client
	)

	BeforeEach(func() {
		ctx = context.Background()

		// Create mock transit vault server (QNAP vault)
		transitServer = NewMockTransitVaultServer()
		transitServer.Start()

		// Create mock target vault server (K8s vault)
		targetServer = NewMockTargetVaultServer()
		targetServer.Start()

		var err error
		client, err = transit.NewClient(
			transitServer.URL,
			"mock-transit-token",
			"k8s-vault-unseal",
			"transit",
			false, // tlsSkipVerify
			"",
			testr.NewWithOptions(&testing.T{}, testr.Options{}),
		)
		Expect(err).NotTo(HaveOccurred())
	})

	AfterEach(func() {
		if transitServer != nil {
			transitServer.Close()
		}
		if targetServer != nil {
			targetServer.Close()
		}
	})

	Context("Transit Encryption Operations", func() {
		It("should encrypt unseal keys using transit engine", func() {
			plaintext := "test-unseal-key-data"

			ciphertext, err := client.EncryptData(ctx, plaintext)
			Expect(err).NotTo(HaveOccurred())
			Expect(ciphertext).To(HavePrefix("vault:v1:"))
			Expect(ciphertext).NotTo(Equal(plaintext))

			// Verify the encryption was recorded
			Expect(transitServer.GetEncryptionCount()).To(BeNumerically(">", 0))
		})

		It("should decrypt ciphertext back to plaintext", func() {
			originalText := "recovery-key-to-encrypt"

			// First encrypt
			ciphertext, err := client.EncryptData(ctx, originalText)
			Expect(err).NotTo(HaveOccurred())

			// Then decrypt
			decrypted, err := client.DecryptData(ctx, ciphertext)
			Expect(err).NotTo(HaveOccurred())
			Expect(decrypted).To(Equal(originalText))

			// Verify both operations were recorded
			Expect(transitServer.GetEncryptionCount()).To(BeNumerically(">", 0))
			Expect(transitServer.GetDecryptionCount()).To(BeNumerically(">", 0))
		})

		It("should handle base64 encoded data", func() {
			originalData := []byte("binary-unseal-key-data")
			base64Data := base64.StdEncoding.EncodeToString(originalData)

			ciphertext, err := client.EncryptData(ctx, base64Data)
			Expect(err).NotTo(HaveOccurred())

			decrypted, err := client.DecryptData(ctx, ciphertext)
			Expect(err).NotTo(HaveOccurred())
			Expect(decrypted).To(Equal(base64Data))

			// Verify we can decode back to original
			decodedData, err := base64.StdEncoding.DecodeString(decrypted)
			Expect(err).NotTo(HaveOccurred())
			Expect(decodedData).To(Equal(originalData))
		})

		It("should handle empty data gracefully", func() {
			_, err := client.EncryptData(ctx, "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("plaintext cannot be empty"))
		})

		It("should handle large data", func() {
			// Create large data (simulate large unseal key)
			largeData := strings.Repeat("unseal-key-segment-", 100)

			ciphertext, err := client.EncryptData(ctx, largeData)
			Expect(err).NotTo(HaveOccurred())

			decrypted, err := client.DecryptData(ctx, ciphertext)
			Expect(err).NotTo(HaveOccurred())
			Expect(decrypted).To(Equal(largeData))
		})
	})

	Context("Vault Unsealing Workflow", func() {
		It("should complete full unseal workflow for single vault", func() {
			// Setup target vault as sealed and initialized
			targetServer.SetSealed(true)
			targetServer.SetInitialized(true)
			targetServer.AddRecoveryKey("test-recovery-key-1")

			By("Checking target vault status")
			status, err := targetServer.CheckStatus()
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Sealed).To(BeTrue())
			Expect(status.Initialized).To(BeTrue())

			By("Encrypting recovery key with transit vault")
			recoveryKey := "test-recovery-key-1"
			encryptedKey, err := client.EncryptData(ctx, recoveryKey)
			Expect(err).NotTo(HaveOccurred())

			By("Simulating storage of encrypted key")
			// In real scenario, this would be stored in Kubernetes secret
			storedEncryptedKey := encryptedKey

			By("Decrypting key for unseal operation")
			decryptedKey, err := client.DecryptData(ctx, storedEncryptedKey)
			Expect(err).NotTo(HaveOccurred())
			Expect(decryptedKey).To(Equal(recoveryKey))

			By("Using decrypted key to unseal vault")
			unsealed, err := targetServer.Unseal(decryptedKey)
			Expect(err).NotTo(HaveOccurred())
			Expect(unsealed).To(BeTrue())

			// Verify final state
			status, err = targetServer.CheckStatus()
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Sealed).To(BeFalse())
		})

		It("should handle multiple recovery keys for threshold unsealing", func() {
			// Setup target vault with threshold unsealing (3 keys, threshold 2)
			targetServer.SetSealed(true)
			targetServer.SetInitialized(true)
			targetServer.SetUnsealThreshold(2)
			targetServer.AddRecoveryKey("recovery-key-1")
			targetServer.AddRecoveryKey("recovery-key-2")
			targetServer.AddRecoveryKey("recovery-key-3")

			recoveryKeys := []string{"recovery-key-1", "recovery-key-2", "recovery-key-3"}

			By("Encrypting all recovery keys")
			var encryptedKeys []string
			for _, key := range recoveryKeys {
				encrypted, err := client.EncryptData(ctx, key)
				Expect(err).NotTo(HaveOccurred())
				encryptedKeys = append(encryptedKeys, encrypted)
			}

			By("Using threshold number of keys to unseal")
			for i := 0; i < 2; i++ { // Use 2 out of 3 keys (threshold)
				decrypted, err := client.DecryptData(ctx, encryptedKeys[i])
				Expect(err).NotTo(HaveOccurred())

				unsealed, err := targetServer.Unseal(decrypted)
				if i == 1 { // Second key should complete unsealing
					Expect(err).NotTo(HaveOccurred())
					Expect(unsealed).To(BeTrue())
				} else { // First key shouldn't complete unsealing yet
					Expect(err).NotTo(HaveOccurred())
					Expect(unsealed).To(BeFalse())
				}
			}

			// Verify vault is now unsealed
			status, err := targetServer.CheckStatus()
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Sealed).To(BeFalse())
		})

		It("should handle homelab dual-vault architecture", func() {
			By("Setting up QNAP vault as transit provider")
			// Transit server is already configured to act as QNAP vault
			Expect(transitServer.URL).NotTo(BeEmpty())

			By("Setting up K8s vault as target")
			targetServer.SetSealed(true)
			targetServer.SetInitialized(true)
			targetServer.AddRecoveryKey("k8s-vault-recovery-key")

			By("Simulating K8s vault operator encrypting its recovery key")
			recoveryKey := "k8s-vault-recovery-key"
			encryptedKey, err := client.EncryptData(ctx, recoveryKey)
			Expect(err).NotTo(HaveOccurred())

			By("Verifying encrypted key is stored securely")
			// In real scenario: kubectl get secret vault-keys -o yaml
			Expect(encryptedKey).To(HavePrefix("vault:v1:"))

			By("Simulating automatic unseal on vault restart")
			// Operator would decrypt and use key automatically
			decryptedKey, err := client.DecryptData(ctx, encryptedKey)
			Expect(err).NotTo(HaveOccurred())

			unsealed, err := targetServer.Unseal(decryptedKey)
			Expect(err).NotTo(HaveOccurred())
			Expect(unsealed).To(BeTrue())

			By("Verifying dual-vault setup is operational")
			// Both vaults should be accessible
			Expect(transitServer.IsHealthy()).To(BeTrue())

			targetStatus, err := targetServer.CheckStatus()
			Expect(err).NotTo(HaveOccurred())
			Expect(targetStatus.Sealed).To(BeFalse())
		})
	})

	Context("Error Handling and Recovery", func() {
		It("should handle transit vault connection failures", func() {
			transitServer.Close()

			_, err := client.EncryptData(ctx, "test-data")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("connection"))
		})

		It("should handle invalid ciphertext", func() {
			_, err := client.DecryptData(ctx, "invalid-ciphertext")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid ciphertext"))
		})

		It("should handle transit vault authentication errors", func() {
			// Create client with invalid token
			invalidClient, err := transit.NewClient(
				transitServer.URL,
				"invalid-token",
				"test-key",
				"transit",
				false,
				"",
				testr.NewWithOptions(&testing.T{}, testr.Options{}),
			)
			Expect(err).NotTo(HaveOccurred())

			transitServer.SetRequireValidAuth(true)

			_, err = invalidClient.EncryptData(ctx, "test-data")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("permission denied"))
		})

		It("should handle missing transit key", func() {
			// Create client with non-existent key
			missingKeyClient, err := transit.NewClient(
				transitServer.URL,
				"mock-transit-token",
				"non-existent-key",
				"transit",
				false,
				"",
				testr.NewWithOptions(&testing.T{}, testr.Options{}),
			)
			Expect(err).NotTo(HaveOccurred())

			_, err = missingKeyClient.EncryptData(ctx, "test-data")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("key not found"))
		})

		It("should handle target vault unsealing failures", func() {
			targetServer.SetSealed(true)
			targetServer.SetInitialized(true)
			targetServer.AddRecoveryKey("correct-key")

			By("Trying to unseal with wrong key")
			wrongKey := "wrong-recovery-key"
			encryptedWrongKey, err := client.EncryptData(ctx, wrongKey)
			Expect(err).NotTo(HaveOccurred())

			decryptedWrongKey, err := client.DecryptData(ctx, encryptedWrongKey)
			Expect(err).NotTo(HaveOccurred())

			unsealed, err := targetServer.Unseal(decryptedWrongKey)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("invalid recovery key"))
			Expect(unsealed).To(BeFalse())

			By("Verifying vault remains sealed")
			status, err := targetServer.CheckStatus()
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Sealed).To(BeTrue())
		})
	})

	Context("Performance and Reliability", func() {
		It("should handle multiple concurrent encryption operations", func() {
			const numOperations = 50
			results := make(chan error, numOperations)

			By("Running concurrent encryptions")
			for i := 0; i < numOperations; i++ {
				go func(index int) {
					data := fmt.Sprintf("concurrent-test-data-%d", index)
					ciphertext, err := client.EncryptData(ctx, data)
					if err != nil {
						results <- err
						return
					}

					// Decrypt to verify
					decrypted, err := client.DecryptData(ctx, ciphertext)
					if err != nil {
						results <- err
						return
					}

					if decrypted != data {
						results <- fmt.Errorf("data mismatch: expected %s, got %s", data, decrypted)
						return
					}

					results <- nil
				}(i)
			}

			By("Verifying all operations completed successfully")
			for i := 0; i < numOperations; i++ {
				select {
				case err := <-results:
					Expect(err).NotTo(HaveOccurred())
				case <-time.After(10 * time.Second):
					Fail("Operation timed out")
				}
			}

			// Verify operation counts
			Expect(transitServer.GetEncryptionCount()).To(Equal(numOperations))
			Expect(transitServer.GetDecryptionCount()).To(Equal(numOperations))
		})

		It("should maintain consistency across multiple unseal operations", func() {
			// Setup multiple target vaults (simulating StatefulSet)
			var targetServers []*MockTargetVaultServer
			const numVaults = 3

			for i := 0; i < numVaults; i++ {
				server := NewMockTargetVaultServer()
				server.Start()
				server.SetSealed(true)
				server.SetInitialized(true)
				server.AddRecoveryKey("shared-recovery-key")
				targetServers = append(targetServers, server)
			}

			defer func() {
				for _, server := range targetServers {
					server.Close()
				}
			}()

			By("Encrypting shared recovery key once")
			sharedKey := "shared-recovery-key"
			encryptedKey, err := client.EncryptData(ctx, sharedKey)
			Expect(err).NotTo(HaveOccurred())

			By("Using same encrypted key to unseal all vaults")
			for i, server := range targetServers {
				decryptedKey, err := client.DecryptData(ctx, encryptedKey)
				Expect(err).NotTo(HaveOccurred())

				unsealed, err := server.Unseal(decryptedKey)
				Expect(err).NotTo(HaveOccurred())
				Expect(unsealed).To(BeTrue(), "Vault %d should be unsealed", i)

				status, err := server.CheckStatus()
				Expect(err).NotTo(HaveOccurred())
				Expect(status.Sealed).To(BeFalse(), "Vault %d should not be sealed", i)
			}
		})
	})
})

// MockTransitVaultServer simulates the QNAP Vault acting as transit provider
type MockTransitVaultServer struct {
	server           *httptest.Server
	URL              string
	mu               sync.RWMutex
	encryptionCount  int
	decryptionCount  int
	requireValidAuth bool
	healthy          bool
}

func NewMockTransitVaultServer() *MockTransitVaultServer {
	return &MockTransitVaultServer{
		healthy: true,
	}
}

func (m *MockTransitVaultServer) Start() {
	m.server = httptest.NewServer(http.HandlerFunc(m.handleRequest))
	m.URL = m.server.URL
}

func (m *MockTransitVaultServer) Close() {
	if m.server != nil {
		m.server.Close()
	}
}

func (m *MockTransitVaultServer) SetRequireValidAuth(require bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requireValidAuth = require
}

func (m *MockTransitVaultServer) GetEncryptionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.encryptionCount
}

func (m *MockTransitVaultServer) GetDecryptionCount() int {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.decryptionCount
}

func (m *MockTransitVaultServer) IsHealthy() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.healthy
}

func (m *MockTransitVaultServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check authentication if required
	if m.requireValidAuth {
		token := r.Header.Get("X-Vault-Token")
		if token != "mock-transit-token" {
			w.WriteHeader(http.StatusForbidden)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []string{"permission denied"},
			})
			return
		}
	}

	path := r.URL.Path

	switch {
	case path == "/v1/sys/health":
		if m.healthy {
			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"initialized": true,
				"sealed":      false,
				"standby":     false,
			})
		} else {
			w.WriteHeader(http.StatusServiceUnavailable)
		}

	case strings.HasPrefix(path, "/v1/transit/encrypt/"):
		keyName := strings.TrimPrefix(path, "/v1/transit/encrypt/")
		if keyName != "k8s-vault-unseal" && keyName != "test-key" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []string{"key not found"},
			})
			return
		}

		if r.Method == "POST" || r.Method == "PUT" {
			var req map[string]interface{}
			json.NewDecoder(r.Body).Decode(&req)

			plaintextB64, ok := req["plaintext"].(string)
			if !ok || plaintextB64 == "" {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": []string{"plaintext cannot be empty"},
				})
				return
			}

			// The client sends base64-encoded plaintext, so we need to decode it
			decodedPlaintext, err := base64.StdEncoding.DecodeString(plaintextB64)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": []string{"invalid base64 plaintext"},
				})
				return
			}

			// Check if the actual plaintext is empty
			if len(decodedPlaintext) == 0 {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": []string{"plaintext cannot be empty"},
				})
				return
			}

			// Simulate encryption by re-encoding and adding vault prefix
			// In real Vault, this would be actual encryption
			ciphertext := fmt.Sprintf("vault:v1:%s", base64.StdEncoding.EncodeToString(decodedPlaintext))

			m.encryptionCount++

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"ciphertext": ciphertext,
				},
			})
		}

	case strings.HasPrefix(path, "/v1/transit/decrypt/"):
		keyName := strings.TrimPrefix(path, "/v1/transit/decrypt/")
		if keyName != "k8s-vault-unseal" && keyName != "test-key" {
			w.WriteHeader(http.StatusNotFound)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"errors": []string{"key not found"},
			})
			return
		}

		if r.Method == "POST" || r.Method == "PUT" {
			var req map[string]interface{}
			json.NewDecoder(r.Body).Decode(&req)

			ciphertext, ok := req["ciphertext"].(string)
			if !ok {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": []string{"invalid ciphertext"},
				})
				return
			}

			// Validate ciphertext format
			if !strings.HasPrefix(ciphertext, "vault:v1:") {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": []string{"invalid ciphertext format"},
				})
				return
			}

			// Decode the base64 part
			encoded := strings.TrimPrefix(ciphertext, "vault:v1:")
			decoded, err := base64.StdEncoding.DecodeString(encoded)
			if err != nil {
				w.WriteHeader(http.StatusBadRequest)
				json.NewEncoder(w).Encode(map[string]interface{}{
					"errors": []string{"invalid ciphertext"},
				})
				return
			}

			m.decryptionCount++

			// Return base64 encoded plaintext (as Vault does)
			plaintext := base64.StdEncoding.EncodeToString(decoded)

			w.WriteHeader(http.StatusOK)
			json.NewEncoder(w).Encode(map[string]interface{}{
				"data": map[string]interface{}{
					"plaintext": plaintext,
				},
			})
		}

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

// MockTargetVaultServer simulates the K8s Vault being unsealed
type MockTargetVaultServer struct {
	server          *httptest.Server
	URL             string
	mu              sync.RWMutex
	sealed          bool
	initialized     bool
	recoveryKeys    []string
	unsealProgress  int
	unsealThreshold int
}

func NewMockTargetVaultServer() *MockTargetVaultServer {
	return &MockTargetVaultServer{
		sealed:          true,
		initialized:     false,
		unsealThreshold: 1, // Default to single key unsealing
	}
}

func (m *MockTargetVaultServer) Start() {
	m.server = httptest.NewServer(http.HandlerFunc(m.handleRequest))
	m.URL = m.server.URL
}

func (m *MockTargetVaultServer) Close() {
	if m.server != nil {
		m.server.Close()
	}
}

func (m *MockTargetVaultServer) SetSealed(sealed bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sealed = sealed
	if !sealed {
		m.unsealProgress = m.unsealThreshold
	} else {
		m.unsealProgress = 0
	}
}

func (m *MockTargetVaultServer) SetInitialized(initialized bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.initialized = initialized
}

func (m *MockTargetVaultServer) SetUnsealThreshold(threshold int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.unsealThreshold = threshold
}

func (m *MockTargetVaultServer) AddRecoveryKey(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.recoveryKeys = append(m.recoveryKeys, key)
}

func (m *MockTargetVaultServer) CheckStatus() (*VaultStatus, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return &VaultStatus{
		Sealed:      m.sealed,
		Initialized: m.initialized,
		Progress:    m.unsealProgress,
		Threshold:   m.unsealThreshold,
	}, nil
}

func (m *MockTargetVaultServer) Unseal(key string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Check if key is valid
	validKey := false
	for _, recoveryKey := range m.recoveryKeys {
		if recoveryKey == key {
			validKey = true
			break
		}
	}

	if !validKey {
		return false, fmt.Errorf("invalid recovery key")
	}

	if !m.sealed {
		return true, nil // Already unsealed
	}

	m.unsealProgress++
	if m.unsealProgress >= m.unsealThreshold {
		m.sealed = false
		return true, nil
	}

	return false, nil // Need more keys
}

func (m *MockTargetVaultServer) handleRequest(w http.ResponseWriter, r *http.Request) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	path := r.URL.Path

	switch path {
	case "/v1/sys/health":
		status := http.StatusOK
		if m.sealed {
			status = http.StatusServiceUnavailable
		}
		w.WriteHeader(status)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"initialized": m.initialized,
			"sealed":      m.sealed,
			"standby":     false,
		})

	case "/v1/sys/seal-status":
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]interface{}{
			"sealed":      m.sealed,
			"initialized": m.initialized,
			"progress":    m.unsealProgress,
			"threshold":   m.unsealThreshold,
		})

	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

type VaultStatus struct {
	Sealed      bool
	Initialized bool
	Progress    int
	Threshold   int
}
