package vault

import (
	"context"
	"fmt"
	"time"

	vaultapi "github.com/hashicorp/vault/api"
	corev1 "k8s.io/api/core/v1"
)

// Client defines the interface for Vault operations
type Client interface {
	CheckStatus(ctx context.Context) (*Status, error)
	Initialize(ctx context.Context, recoveryShares, recoveryThreshold int) (*InitResponse, error)
	IsHealthy(ctx context.Context) bool
}

// Status represents Vault server status
type Status struct {
	Initialized bool
	Sealed      bool
	Version     string
	ClusterID   string
}

// InitResponse contains initialization results
type InitResponse struct {
	RootToken       string
	RecoveryKeys    []string
	RecoveryKeysB64 []string
}

// client implements the Vault client interface
type client struct {
	api    *vaultapi.Client
	config *Config
}

// Config holds Vault client configuration
type Config struct {
	Address       string
	Token         string
	Namespace     string
	TLSSkipVerify bool
	Timeout       time.Duration
}

// NewClient creates a new Vault client
func NewClient(cfg *Config) (Client, error) {
	vaultConfig := vaultapi.DefaultConfig()
	vaultConfig.Address = cfg.Address
	vaultConfig.Timeout = cfg.Timeout

	if cfg.TLSSkipVerify {
		if err := vaultConfig.ConfigureTLS(&vaultapi.TLSConfig{
			Insecure: true,
		}); err != nil {
			return nil, fmt.Errorf("configuring TLS: %w", err)
		}
	}

	apiClient, err := vaultapi.NewClient(vaultConfig)
	if err != nil {
		return nil, fmt.Errorf("creating vault client: %w", err)
	}

	if cfg.Token != "" {
		apiClient.SetToken(cfg.Token)
	}

	if cfg.Namespace != "" {
		apiClient.SetNamespace(cfg.Namespace)
	}

	return &client{
		api:    apiClient,
		config: cfg,
	}, nil
}

// NewClientForPod creates a Vault client configured for a specific pod
func NewClientForPod(pod *corev1.Pod, tlsSkipVerify bool) (Client, error) {
	return NewClient(&Config{
		Address:       fmt.Sprintf("http://%s:8200", pod.Status.PodIP),
		TLSSkipVerify: tlsSkipVerify,
		Timeout:       10 * time.Second,
	})
}

func (c *client) CheckStatus(ctx context.Context) (*Status, error) {
	health, err := c.api.Sys().HealthWithContext(ctx)
	if err != nil {
		return nil, fmt.Errorf("checking vault health: %w", err)
	}

	return &Status{
		Initialized: health.Initialized,
		Sealed:      health.Sealed,
		Version:     health.Version,
		ClusterID:   health.ClusterID,
	}, nil
}

func (c *client) Initialize(ctx context.Context, recoveryShares, recoveryThreshold int) (*InitResponse, error) {
	initReq := &vaultapi.InitRequest{
		RecoveryShares:    recoveryShares,
		RecoveryThreshold: recoveryThreshold,
	}

	resp, err := c.api.Sys().InitWithContext(ctx, initReq)
	if err != nil {
		return nil, fmt.Errorf("initializing vault: %w", err)
	}

	return &InitResponse{
		RootToken:       resp.RootToken,
		RecoveryKeys:    resp.RecoveryKeys,
		RecoveryKeysB64: resp.RecoveryKeysB64,
	}, nil
}

func (c *client) IsHealthy(ctx context.Context) bool {
	_, err := c.api.Sys().HealthWithContext(ctx)
	return err == nil
}
