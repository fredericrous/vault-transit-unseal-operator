package transit

import (
	"context"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/go-logr/logr"
	vaultapi "github.com/hashicorp/vault/api"
)

// Client handles transit unseal operations
type Client struct {
	api       *vaultapi.Client
	keyName   string
	mountPath string
	log       logr.Logger
}

// NewClient creates a new transit client
func NewClient(address, token, keyName, mountPath string, tlsSkipVerify bool, log logr.Logger) (*Client, error) {
	if token == "" {
		return nil, fmt.Errorf("transit token cannot be empty")
	}

	if address == "" {
		return nil, fmt.Errorf("vault address cannot be empty")
	}

	config := vaultapi.DefaultConfig()
	config.Address = address
	config.Timeout = 30 * time.Second

	if tlsSkipVerify {
		if err := config.ConfigureTLS(&vaultapi.TLSConfig{
			Insecure: true,
		}); err != nil {
			return nil, fmt.Errorf("configuring TLS: %w", err)
		}
	}

	apiClient, err := vaultapi.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("creating vault client: %w", err)
	}

	apiClient.SetToken(token)

	return &Client{
		api:       apiClient,
		keyName:   keyName,
		mountPath: mountPath,
		log:       log.WithName("transit"),
	}, nil
}

// UnsealVault attempts to unseal the target vault using transit auto-unseal
func (c *Client) UnsealVault(ctx context.Context, targetClient *vaultapi.Client) error {
	// Check if already unsealed
	status, err := targetClient.Sys().SealStatusWithContext(ctx)
	if err != nil {
		return fmt.Errorf("checking seal status: %w", err)
	}

	if !status.Sealed {
		c.log.V(1).Info("Vault is already unsealed")
		return nil
	}

	c.log.Info("Vault is sealed, attempting to unseal")

	// For transit auto-unseal, we just need to trigger the unseal
	// The Vault server will automatically use the configured transit key
	_, err = targetClient.Sys().UnsealWithContext(ctx, "")
	if err != nil {
		return fmt.Errorf("triggering transit unseal: %w", err)
	}

	// Wait for unseal to complete
	for i := 0; i < 10; i++ {
		status, err := targetClient.Sys().SealStatusWithContext(ctx)
		if err != nil {
			return fmt.Errorf("checking seal status after unseal: %w", err)
		}

		if !status.Sealed {
			c.log.Info("Vault successfully unsealed")
			return nil
		}

		time.Sleep(time.Second)
	}

	return fmt.Errorf("vault did not unseal within timeout")
}

// EncryptData encrypts data using transit engine
func (c *Client) EncryptData(ctx context.Context, plaintext string) (string, error) {
	if plaintext == "" {
		return "", fmt.Errorf("plaintext cannot be empty")
	}

	path := fmt.Sprintf("%s/encrypt/%s", c.mountPath, c.keyName)

	data := map[string]interface{}{
		"plaintext": base64.StdEncoding.EncodeToString([]byte(plaintext)),
	}

	secret, err := c.api.Logical().WriteWithContext(ctx, path, data)
	if err != nil {
		return "", fmt.Errorf("encrypting data: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return "", fmt.Errorf("no data returned from transit engine")
	}

	ciphertext, ok := secret.Data["ciphertext"].(string)
	if !ok {
		return "", fmt.Errorf("ciphertext not found in response")
	}

	return ciphertext, nil
}

// DecryptData decrypts data using transit engine
func (c *Client) DecryptData(ctx context.Context, ciphertext string) (string, error) {
	path := fmt.Sprintf("%s/decrypt/%s", c.mountPath, c.keyName)

	data := map[string]interface{}{
		"ciphertext": ciphertext,
	}

	secret, err := c.api.Logical().WriteWithContext(ctx, path, data)
	if err != nil {
		return "", fmt.Errorf("decrypting data: %w", err)
	}

	if secret == nil || secret.Data == nil {
		return "", fmt.Errorf("no data returned from transit engine")
	}

	plaintextB64, ok := secret.Data["plaintext"].(string)
	if !ok {
		return "", fmt.Errorf("plaintext not found in response")
	}

	plaintext, err := base64.StdEncoding.DecodeString(plaintextB64)
	if err != nil {
		return "", fmt.Errorf("decoding plaintext: %w", err)
	}

	return string(plaintext), nil
}
