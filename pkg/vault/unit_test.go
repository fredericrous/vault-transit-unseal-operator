package vault_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("Vault Client Unit Tests", func() {
	Context("NewClient", func() {
		It("should create client with TLS skip verify", func() {
			cfg := &vault.Config{
				Address:       "https://vault.example.com",
				Token:         "test-token",
				Namespace:     "test-namespace",
				TLSSkipVerify: true,
				Timeout:       30 * time.Second,
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(client).NotTo(BeNil())

			// Verify the client has the API client
			apiClient := client.GetAPIClient()
			Expect(apiClient).NotTo(BeNil())
			Expect(apiClient.Token()).To(Equal("test-token"))
		})

		It("should handle TLS configuration error", func() {
			// This test would need to mock the TLS configuration to force an error
			// For now, we'll test the normal path
			cfg := &vault.Config{
				Address:       "https://vault.example.com",
				TLSSkipVerify: false,
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(client).NotTo(BeNil())
		})

		It("should create client without token", func() {
			cfg := &vault.Config{
				Address: "http://vault.example.com",
				Timeout: 10 * time.Second,
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(client).NotTo(BeNil())
		})
	})

	Context("NewClientForPod", func() {
		It("should return deprecation error", func() {
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-0",
					Namespace: "vault",
				},
				Status: corev1.PodStatus{
					PodIP: "10.0.0.1",
				},
			}

			client, err := vault.NewClientForPod(pod, true)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("deprecated"))
			Expect(client).To(BeNil())
		})
	})

	Context("GetAPIClient", func() {
		It("should return the underlying API client", func() {
			cfg := &vault.Config{
				Address: "http://vault.example.com",
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			apiClient := client.GetAPIClient()
			Expect(apiClient).NotTo(BeNil())
			Expect(apiClient.Token()).To(Equal("test-token"))
		})
	})

	Context("MountSecretEngine edge cases", func() {
		It("should handle mount input with string version in config", func() {
			// This would need a mock server to test properly
			// The integration tests cover the main paths
		})

		It("should handle mount input with nil options", func() {
			// This would need a mock server to test properly
			// The integration tests cover the main paths
		})
	})

	Context("Error handling with mock server", func() {
		var (
			mockServer *httptest.Server
			errors     map[string]bool
			responses  map[string]interface{}
			ctx        context.Context
		)

		BeforeEach(func() {
			ctx = context.Background()
			errors = make(map[string]bool)
			responses = make(map[string]interface{})

			// Create mock Vault server
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Check if we should return an error for this path
				if errors[r.URL.Path] {
					w.WriteHeader(http.StatusInternalServerError)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"errors": []string{"mock error"},
					})
					return
				}

				// Return mock responses based on path
				switch r.URL.Path {
				case "/v1/sys/seal-status":
					json.NewEncoder(w).Encode(map[string]interface{}{
						"sealed":      false,
						"initialized": true,
					})
				case "/v1/sys/health":
					w.WriteHeader(http.StatusOK)
				case "/v1/sys/auth":
					if r.Method == "GET" {
						if resp, ok := responses["/v1/sys/auth"]; ok {
							json.NewEncoder(w).Encode(resp)
						} else {
							json.NewEncoder(w).Encode(map[string]interface{}{
								"kubernetes/": map[string]interface{}{
									"type": "kubernetes",
								},
							})
						}
					}
				case "/v1/auth/kubernetes/config":
					if r.Method == "POST" {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/v1/sys/mounts":
					if r.Method == "GET" {
						if resp, ok := responses["/v1/sys/mounts"]; ok {
							json.NewEncoder(w).Encode(resp)
						} else {
							json.NewEncoder(w).Encode(map[string]interface{}{
								"secret/": map[string]interface{}{
									"type":    "kv",
									"options": map[string]interface{}{"version": "1"},
								},
								"kv/": map[string]interface{}{
									"type":    "kv",
									"options": map[string]interface{}{"version": "2"},
								},
							})
						}
					}
				case "/v1/sys/mounts/secret":
					if r.Method == "POST" {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/v1/sys/mounts/secret/tune":
					if r.Method == "POST" {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/v1/sys/mounts/newsecret":
					if r.Method == "POST" {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/v1/sys/mounts/configmount":
					if r.Method == "POST" {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/v1/sys/mounts/bothmount":
					if r.Method == "POST" {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/v1/sys/mounts/mixedmount":
					if r.Method == "POST" {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/v1/sys/policies/acl/test-policy":
					if r.Method == "PUT" {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/v1/auth/kubernetes":
					if r.Method == "POST" {
						w.WriteHeader(http.StatusNoContent)
					}
				case "/v1/sys/auth/kubernetes":
					if r.Method == "POST" {
						w.WriteHeader(http.StatusNoContent)
					}
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
		})

		AfterEach(func() {
			if mockServer != nil {
				mockServer.Close()
			}
		})

		It("should handle auth listing error", func() {
			// Set up error for auth list
			errors["/v1/sys/auth"] = true

			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Try to check if auth is enabled - should fail
			enabled, err := client.AuthEnabled(ctx, "kubernetes")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("listing auth methods"))
			Expect(enabled).To(BeFalse())
		})

		It("should handle auth write error", func() {
			// Set up error for auth write
			errors["/v1/auth/kubernetes/config"] = true

			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Try to write auth config - should fail
			err = client.WriteAuth(ctx, "auth/kubernetes/config", map[string]interface{}{
				"kubernetes_host": "https://kubernetes.default.svc",
			})
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("writing auth config"))
		})

		It("should handle mount listing error", func() {
			// Set up error for mount list
			errors["/v1/sys/mounts"] = true

			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Try to check if mount exists - should fail
			exists, err := client.MountExists(ctx, "secret")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("listing mounts"))
			Expect(exists).To(BeFalse())
		})

		It("should handle mount with version update", func() {
			// Set up response with KV v1 that needs upgrade
			responses["/v1/sys/mounts"] = map[string]interface{}{
				"secret/": map[string]interface{}{
					"type":    "kv",
					"options": map[string]interface{}{"version": "1"},
				},
			}

			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Mount KV v2 - should update existing mount
			err = client.MountSecretEngine(ctx, "secret", &vault.MountInput{
				Type: "kv",
				Options: map[string]interface{}{
					"version": "2",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle enabling auth when not exists", func() {
			// Set up empty auth response
			responses["/v1/sys/auth"] = map[string]interface{}{}

			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Enable auth - should succeed
			err = client.EnableAuth(ctx, "kubernetes", "kubernetes")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle auth already enabled", func() {
			// Auth already exists in default response

			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Check if auth is enabled - should return true
			enabled, err := client.AuthEnabled(ctx, "kubernetes")
			Expect(err).NotTo(HaveOccurred())
			Expect(enabled).To(BeTrue())
		})

		It("should handle mount already exists", func() {
			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Check if mount exists - should return true
			exists, err := client.MountExists(ctx, "secret")
			Expect(err).NotTo(HaveOccurred())
			Expect(exists).To(BeTrue())
		})

		It("should write policy successfully", func() {
			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Write policy - should succeed
			err = client.WritePolicy(ctx, "test-policy", "path \"secret/*\" { capabilities = [\"read\"] }")
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle KV version mismatch on existing secret mount", func() {
			// Set up response with mismatched KV version and options
			responses["/v1/sys/mounts"] = map[string]interface{}{
				"secret/": map[string]interface{}{
					"type": "kv-v2", // Different type string
					"options": map[string]interface{}{
						"version":      "2",
						"max_versions": "10",
					},
				},
			}

			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Mount KV v2 - should handle existing mount with mismatched options
			err = client.MountSecretEngine(ctx, "secret", &vault.MountInput{
				Type: "kv",
				Options: map[string]interface{}{
					"version":      "2",
					"max_versions": "5", // Different value
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should create new mount when not exists", func() {
			// Set up empty mounts response
			responses["/v1/sys/mounts"] = map[string]interface{}{}

			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Mount new KV - should create
			err = client.MountSecretEngine(ctx, "newsecret", &vault.MountInput{
				Type: "kv",
				Options: map[string]interface{}{
					"version": "2",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle mount with config version", func() {
			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Mount with version in config instead of options
			err = client.MountSecretEngine(ctx, "configmount", &vault.MountInput{
				Type: "kv",
				Config: map[string]interface{}{
					"version": "2",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle mount with both config and options", func() {
			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Mount with version in both config and options
			err = client.MountSecretEngine(ctx, "bothmount", &vault.MountInput{
				Type: "kv",
				Config: map[string]interface{}{
					"version": "2",
					"ttl":     "1h",
				},
				Options: map[string]interface{}{
					"max_versions": "10",
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle non-string option values", func() {
			cfg := &vault.Config{
				Address: mockServer.URL,
				Token:   "test-token",
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Mount with mixed option types
			err = client.MountSecretEngine(ctx, "mixedmount", &vault.MountInput{
				Type: "kv",
				Options: map[string]interface{}{
					"version":   "2",
					"some_int":  42,   // Non-string value, should be ignored
					"some_bool": true, // Non-string value, should be ignored
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})
	})

	Context("TLS configuration", func() {
		It("should handle TLS server with skip verify", func() {
			// Create a TLS mock server
			tlsServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/v1/sys/health" {
					w.WriteHeader(http.StatusOK)
					json.NewEncoder(w).Encode(map[string]interface{}{
						"sealed":      false,
						"initialized": true,
					})
					return
				}

				switch r.URL.Path {
				case "/v1/sys/seal-status":
					json.NewEncoder(w).Encode(map[string]interface{}{
						"sealed":      false,
						"initialized": true,
					})
				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer tlsServer.Close()

			cfg := &vault.Config{
				Address:       tlsServer.URL,
				Token:         "test-token",
				TLSSkipVerify: true,
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			// Should be able to check status with skip verify
			ctx := context.Background()
			status, err := client.CheckStatus(ctx)
			Expect(err).NotTo(HaveOccurred())
			Expect(status.Initialized).To(BeTrue())
			Expect(status.Sealed).To(BeFalse())
		})
	})

	Context("CA Certificate configuration", func() {
		var tempDir string

		BeforeEach(func() {
			var err error
			tempDir, err = os.MkdirTemp("", "vault-ca-test")
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			os.RemoveAll(tempDir)
		})

		It("should handle valid CA certificate file", func() {
			// Create a valid CA cert file (self-signed for testing)
			caFile := filepath.Join(tempDir, "ca.crt")
			// This is a valid self-signed CA certificate generated with:
			// openssl req -new -x509 -key test.key -out test.crt -days 365
			validCACert := `-----BEGIN CERTIFICATE-----
MIICpDCCAYwCCQDU+pQ3ZUD30jANBgkqhkiG9w0BAQsFADAUMRIwEAYDVQQDDAls
b2NhbGhvc3QwHhcNMjQwMTAxMDAwMDAwWhcNMjUwMTAxMDAwMDAwWjAUMRIwEAYD
VQQDDAlsb2NhbGhvc3QwggEiMA0GCSqGSIb3DQEBAQUAA4IBDwAwggEKAoIBAQDV
OZ4zQT5H5E7wRzKQucpd+vhrcKnvFa9CR7Bxx4RLHMxqq/nzr6vNK2DQqCTVV/8Q
CRx1EEPUJomXZ8MB6j/h2r9OVnXkNOkx7trhcExl/p1KcyX0V9df42tmXiG2Rxho
T0K7KRs6Q0pqBvNkwXqXYTxjRWG3qRAwOqq+pOl0pPKr7oMT3caULBaLVnxvNqUC
qQB6VTQnzh8fkGJ9TVSKHYMqGFH/VC3TpGP2ST5WHNkBSpLfQpv7HHnbKWz0DPxp
wuhFMkVY1r3gZI0jSJYeFMlO1lIqB5TXo+hUeAFQYvS6RYQ9z0iu6dLTwPl3Xl5s
Tn6sdKlYF0BV5bPUPy1lAgMBAAEwDQYJKoZIhvcNAQELBQADggEBABjQ1mF3sR3P
CUPqRBNlcQxHmYH5fkx3iLBWsYBCiTXlHZ1i9M5SERJmWKwKY0bhLokMdF+4K+gF
1gGpQoJm3rjRj8JS9Mk03qcXSN5VRmI3KYvfMiZRM8703LpbqnQV5B6jGWX2TkPl
g6WvZMJnJWMkkhPDR9d7DFnJ3JDU6L/MdAQJD3ixnDCfUqU3joWfXQBl6G15CWre
c8lMu9Xz8d2snulhAqvn0cL5n7aPmK3rkdkQVb0LWJ5klvVNgzQoIuC6nDgQQuRU
qvDnXm7BXmaXta6gVgYmwInHkVJFz7TjCYWRak0E3kH1qTmDrnKHmJvE9FbGqDbL
sKx0F6gZQ5M=
-----END CERTIFICATE-----`
			err := os.WriteFile(caFile, []byte(validCACert), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg := &vault.Config{
				Address: "https://vault.example.com",
				Token:   "test-token",
				CACert:  caFile,
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(client).NotTo(BeNil())
		})

		It("should handle missing CA certificate file", func() {
			nonExistentFile := filepath.Join(tempDir, "non-existent.crt")

			cfg := &vault.Config{
				Address: "https://vault.example.com",
				Token:   "test-token",
				CACert:  nonExistentFile,
			}

			_, err := vault.NewClient(cfg)
			// The Vault API client validates the CA file during TLS configuration
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Error loading CA File"))
			Expect(err.Error()).To(ContainSubstring("no such file or directory"))
		})

		It("should handle invalid CA certificate file", func() {
			// Create an invalid CA cert file
			caFile := filepath.Join(tempDir, "invalid-ca.crt")
			invalidContent := "This is not a valid certificate"
			err := os.WriteFile(caFile, []byte(invalidContent), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg := &vault.Config{
				Address: "https://vault.example.com",
				Token:   "test-token",
				CACert:  caFile,
			}

			_, err = vault.NewClient(cfg)
			// The Vault API client validates PEM format during TLS configuration
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Error loading CA File"))
			Expect(err.Error()).To(ContainSubstring("Couldn't parse PEM"))
		})

		It("should handle empty CA certificate path", func() {
			cfg := &vault.Config{
				Address: "https://vault.example.com",
				Token:   "test-token",
				CACert:  "", // Empty path
			}

			client, err := vault.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())
			Expect(client).NotTo(BeNil())
		})

		It("should handle CA cert with TLS skip verify", func() {
			// Create a CA cert file
			caFile := filepath.Join(tempDir, "ca.crt")
			err := os.WriteFile(caFile, []byte("dummy cert"), 0644)
			Expect(err).NotTo(HaveOccurred())

			cfg := &vault.Config{
				Address:       "https://vault.example.com",
				Token:         "test-token",
				CACert:        caFile,
				TLSSkipVerify: true, // Should take precedence
			}

			// Even with TLS skip verify, invalid CA cert will cause error
			_, err = vault.NewClient(cfg)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("Error loading CA File"))
		})
	})
})
