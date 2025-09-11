package config

import (
	"fmt"
	"time"
)

// OperatorConfig holds all operator configuration
type OperatorConfig struct {
	// Feature flags
	EnableMetrics        bool
	EnableLeaderElection bool
	SkipCRDInstall       bool

	// Operational settings
	ReconcileTimeout        time.Duration
	MaxConcurrentReconciles int
	Namespace               string

	// Vault settings
	DefaultVaultTimeout time.Duration
	EnableTLSValidation bool

	// Server settings
	MetricsAddr      string
	ProbeAddr        string
	LeaderElectionID string
}

// NewDefaultConfig creates a new config with default values
// This is primarily used for testing
func NewDefaultConfig() *OperatorConfig {
	return &OperatorConfig{
		EnableMetrics:           true,
		EnableLeaderElection:    false,
		SkipCRDInstall:          false,
		ReconcileTimeout:        5 * time.Minute,
		MaxConcurrentReconciles: 3,
		Namespace:               "vault",
		DefaultVaultTimeout:     30 * time.Second,
		EnableTLSValidation:     true,
		MetricsAddr:             ":8080",
		ProbeAddr:               ":8081",
		LeaderElectionID:        "vault-transit-unseal-operator",
	}
}

// Validate checks if the configuration is valid
func (c *OperatorConfig) Validate() error {
	if c.ReconcileTimeout <= 0 {
		return fmt.Errorf("reconcile timeout must be positive")
	}

	if c.MaxConcurrentReconciles < 1 {
		return fmt.Errorf("max concurrent reconciles must be at least 1")
	}

	if c.DefaultVaultTimeout <= 0 {
		return fmt.Errorf("vault timeout must be positive")
	}

	if c.Namespace == "" {
		return fmt.Errorf("namespace cannot be empty")
	}

	if c.MetricsAddr == "" {
		return fmt.Errorf("metrics address cannot be empty")
	}

	if c.ProbeAddr == "" {
		return fmt.Errorf("probe address cannot be empty")
	}

	return nil
}
