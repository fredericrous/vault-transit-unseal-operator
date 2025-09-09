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
	Initialize(ctx context.Context, req *InitRequest) (*InitResponse, error)
	IsHealthy(ctx context.Context) bool
	GetAPIClient() *vaultapi.Client
	EnableAuth(ctx context.Context, path, authType string) error
	AuthEnabled(ctx context.Context, path string) (bool, error)
	WriteAuth(ctx context.Context, path string, data map[string]interface{}) error
	MountExists(ctx context.Context, path string) (bool, error)
	MountSecretEngine(ctx context.Context, path string, input *MountInput) error
	WritePolicy(ctx context.Context, name, policy string) error
}

// Status represents Vault server status
type Status struct {
	Initialized bool
	Sealed      bool
	Version     string
	ClusterID   string
}

// InitRequest holds initialization parameters
type InitRequest struct {
	RecoveryShares    int
	RecoveryThreshold int
}

// InitResponse contains initialization results
type InitResponse struct {
	RootToken       string
	RecoveryKeys    []string
	RecoveryKeysB64 []string
}

// MountInput holds mount configuration
type MountInput struct {
	Type    string
	Config  map[string]interface{}
	Options map[string]interface{}
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

func (c *client) Initialize(ctx context.Context, req *InitRequest) (*InitResponse, error) {
	initReq := &vaultapi.InitRequest{
		RecoveryShares:    req.RecoveryShares,
		RecoveryThreshold: req.RecoveryThreshold,
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

func (c *client) GetAPIClient() *vaultapi.Client {
	return c.api
}

func (c *client) EnableAuth(ctx context.Context, path, authType string) error {
	return c.api.Sys().EnableAuthWithOptionsWithContext(ctx, path, &vaultapi.EnableAuthOptions{
		Type: authType,
	})
}

func (c *client) AuthEnabled(ctx context.Context, path string) (bool, error) {
	auths, err := c.api.Sys().ListAuthWithContext(ctx)
	if err != nil {
		return false, fmt.Errorf("listing auth methods: %w", err)
	}

	_, exists := auths[path+"/"]
	return exists, nil
}

func (c *client) WriteAuth(ctx context.Context, path string, data map[string]interface{}) error {
	_, err := c.api.Logical().WriteWithContext(ctx, "auth/"+path, data)
	if err != nil {
		return fmt.Errorf("writing auth config: %w", err)
	}
	return nil
}

func (c *client) MountExists(ctx context.Context, path string) (bool, error) {
	mounts, err := c.api.Sys().ListMountsWithContext(ctx)
	if err != nil {
		return false, fmt.Errorf("listing mounts: %w", err)
	}

	_, exists := mounts[path+"/"]
	return exists, nil
}

func (c *client) MountSecretEngine(ctx context.Context, path string, input *MountInput) error {
	mountInput := &vaultapi.MountInput{
		Type:   input.Type,
		Config: vaultapi.MountConfigInput{},
	}

	// Convert Options from map[string]interface{} to map[string]string
	if input.Options != nil {
		options := make(map[string]string)
		for k, v := range input.Options {
			if str, ok := v.(string); ok {
				options[k] = str
			}
		}
		mountInput.Options = options
	}

	if input.Config != nil {
		if v, ok := input.Config["version"]; ok {
			if version, ok := v.(string); ok {
				if mountInput.Options == nil {
					mountInput.Options = make(map[string]string)
				}
				mountInput.Options["version"] = version
			}
		}
	}

	return c.api.Sys().MountWithContext(ctx, path, mountInput)
}

func (c *client) WritePolicy(ctx context.Context, name, policy string) error {
	return c.api.Sys().PutPolicyWithContext(ctx, name, policy)
}
