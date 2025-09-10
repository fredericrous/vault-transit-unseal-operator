package transit_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
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
				logger,
			)
			Expect(err).NotTo(HaveOccurred())
			Expect(client).NotTo(BeNil())
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
