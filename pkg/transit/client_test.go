package transit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"time"

	"github.com/go-logr/logr"
	vaultapi "github.com/hashicorp/vault/api"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/transit"
)

var _ = Describe("Transit Client Unit Tests", func() {
	var (
		ctx    context.Context
		logger logr.Logger
	)

	BeforeEach(func() {
		ctx = context.Background()
		logger = logr.Discard()
	})

	Context("NewClient", func() {
		It("should create client successfully", func() {
			client, err := transit.NewClient(
				"http://vault.example.com",
				"test-token",
				"unseal-key",
				"transit",
				false,
				"",
				logger,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(client).NotTo(BeNil())
		})

		It("should fail with empty token", func() {
			_, err := transit.NewClient(
				"http://vault.example.com",
				"",
				"unseal-key",
				"transit",
				false,
				"",
				logger,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("transit token cannot be empty"))
		})

		It("should fail with empty address", func() {
			_, err := transit.NewClient(
				"",
				"test-token",
				"unseal-key",
				"transit",
				false,
				"",
				logger,
			)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("vault address cannot be empty"))
		})

		It("should create client with TLS skip verify", func() {
			client, err := transit.NewClient(
				"https://vault.example.com",
				"test-token",
				"unseal-key",
				"transit",
				true,
				"",
				logger,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(client).NotTo(BeNil())
		})

		Context("CA Certificate handling", func() {
			var tempDir string

			BeforeEach(func() {
				var err error
				tempDir, err = os.MkdirTemp("", "transit-ca-test")
				Expect(err).NotTo(HaveOccurred())
			})

			AfterEach(func() {
				os.RemoveAll(tempDir)
			})

			It("should create client with valid CA certificate", func() {
				// Create a valid CA cert file
				caFile := filepath.Join(tempDir, "ca.crt")
				// This is a valid self-signed CA certificate
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

				client, err := transit.NewClient(
					"https://vault.example.com",
					"test-token",
					"unseal-key",
					"transit",
					false,
					caFile,
					logger,
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(client).NotTo(BeNil())
			})

			It("should create client with missing CA certificate file", func() {
				nonExistentFile := filepath.Join(tempDir, "non-existent.crt")

				_, err := transit.NewClient(
					"https://vault.example.com",
					"test-token",
					"unseal-key",
					"transit",
					false,
					nonExistentFile,
					logger,
				)
				// Vault client validates CA file during TLS configuration
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Error loading CA File"))
				Expect(err.Error()).To(ContainSubstring("no such file or directory"))
			})

			It("should create client with empty CA certificate path", func() {
				client, err := transit.NewClient(
					"https://vault.example.com",
					"test-token",
					"unseal-key",
					"transit",
					false,
					"",
					logger,
				)
				Expect(err).NotTo(HaveOccurred())
				Expect(client).NotTo(BeNil())
			})

			It("should handle CA cert with TLS skip verify", func() {
				caFile := filepath.Join(tempDir, "ca.crt")
				err := os.WriteFile(caFile, []byte("dummy cert"), 0644)
				Expect(err).NotTo(HaveOccurred())

				// Even with TLS skip verify, invalid CA cert will cause error
				_, err = transit.NewClient(
					"https://vault.example.com",
					"test-token",
					"unseal-key",
					"transit",
					true, // TLS skip verify should take precedence
					caFile,
					logger,
				)
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Error loading CA File"))
			})

			It("should handle invalid CA certificate content", func() {
				caFile := filepath.Join(tempDir, "invalid-ca.crt")
				invalidContent := "This is not a valid certificate"
				err := os.WriteFile(caFile, []byte(invalidContent), 0644)
				Expect(err).NotTo(HaveOccurred())

				_, err = transit.NewClient(
					"https://vault.example.com",
					"test-token",
					"unseal-key",
					"transit",
					false,
					caFile,
					logger,
				)
				// Vault client validates PEM format during TLS configuration
				Expect(err).To(HaveOccurred())
				Expect(err.Error()).To(ContainSubstring("Error loading CA File"))
				Expect(err.Error()).To(ContainSubstring("Couldn't parse PEM"))
			})
		})
	})

	Context("UnsealVault", func() {
		var (
			mockServer *httptest.Server
			client     *transit.Client
			sealed     bool
			unsealErr  error
		)

		BeforeEach(func() {
			sealed = true
			unsealErr = nil

			// Create a mock vault server
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/v1/sys/seal-status":
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"sealed":      sealed,
						"initialized": true,
					})

				case "/v1/sys/unseal":
					if unsealErr != nil {
						w.WriteHeader(http.StatusInternalServerError)
						json.NewEncoder(w).Encode(map[string]interface{}{
							"errors": []string{unsealErr.Error()},
						})
						return
					}
					// Simulate unsealing after the request
					go func() {
						time.Sleep(100 * time.Millisecond)
						sealed = false
					}()
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(map[string]interface{}{
						"sealed": true, // Still sealed immediately after request
					})

				default:
					w.WriteHeader(http.StatusNotFound)
				}
			}))

			var err error
			client, err = transit.NewClient(
				"http://transit.example.com", // Transit vault (not used in UnsealVault)
				"transit-token",
				"unseal-key",
				"transit",
				false,
				"",
				logger,
			)
			Expect(err).NotTo(HaveOccurred())
		})

		AfterEach(func() {
			if mockServer != nil {
				mockServer.Close()
			}
		})

		It("should skip unsealing if vault is already unsealed", func() {
			sealed = false

			// Create target client pointing to our mock server
			cfg := vaultapi.DefaultConfig()
			cfg.Address = mockServer.URL
			targetClient, err := vaultapi.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			err = client.UnsealVault(ctx, targetClient)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should successfully unseal a sealed vault", func() {
			// Create target client pointing to our mock server
			cfg := vaultapi.DefaultConfig()
			cfg.Address = mockServer.URL
			targetClient, err := vaultapi.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			err = client.UnsealVault(ctx, targetClient)
			Expect(err).NotTo(HaveOccurred())
			Expect(sealed).To(BeFalse())
		})

		It("should handle seal status check error", func() {
			// Close the server to cause connection error
			mockServer.Close()

			cfg := vaultapi.DefaultConfig()
			cfg.Address = mockServer.URL
			targetClient, err := vaultapi.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			err = client.UnsealVault(ctx, targetClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("checking seal status"))
		})

		It("should handle unseal trigger error", func() {
			unsealErr = fmt.Errorf("unseal operation failed")

			cfg := vaultapi.DefaultConfig()
			cfg.Address = mockServer.URL
			targetClient, err := vaultapi.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			err = client.UnsealVault(ctx, targetClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("triggering transit unseal"))
		})

		It("should timeout if vault doesn't unseal", func() {
			// Don't change sealed status to simulate timeout
			mockServer = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				json.NewEncoder(w).Encode(map[string]interface{}{
					"sealed":      true, // Always return sealed
					"initialized": true,
				})
			}))
			defer mockServer.Close()

			cfg := vaultapi.DefaultConfig()
			cfg.Address = mockServer.URL
			targetClient, err := vaultapi.NewClient(cfg)
			Expect(err).NotTo(HaveOccurred())

			err = client.UnsealVault(ctx, targetClient)
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("vault did not unseal within timeout"))
		})
	})

	Context("EncryptData edge cases", func() {
		var client *transit.Client

		BeforeEach(func() {
			var err error
			client, err = transit.NewClient(
				"http://vault.example.com",
				"test-token",
				"key",
				"transit",
				false,
				"",
				logger,
			)
			Expect(err).NotTo(HaveOccurred())
		})

		It("should fail with empty plaintext", func() {
			_, err := client.EncryptData(ctx, "")
			Expect(err).To(HaveOccurred())
			Expect(err.Error()).To(ContainSubstring("plaintext cannot be empty"))
		})
	})
})
