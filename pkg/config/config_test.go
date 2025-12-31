package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestNewDefaultConfig(t *testing.T) {
	cfg := NewDefaultConfig()

	assert.True(t, cfg.EnableMetrics)
	assert.False(t, cfg.EnableLeaderElection)
	assert.False(t, cfg.SkipCRDInstall)
	assert.Equal(t, 5*time.Minute, cfg.ReconcileTimeout)
	assert.Equal(t, 3, cfg.MaxConcurrentReconciles)
	assert.Equal(t, "vault", cfg.Namespace)
	assert.Equal(t, 120*time.Second, cfg.DefaultVaultTimeout)
	assert.True(t, cfg.EnableTLSValidation)
	assert.Equal(t, ":8080", cfg.MetricsAddr)
	assert.Equal(t, ":8081", cfg.ProbeAddr)
	assert.Equal(t, "vault-transit-unseal-operator", cfg.LeaderElectionID)
}

func TestOperatorConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *OperatorConfig
		wantErr string
	}{
		{
			name:    "valid config",
			config:  NewDefaultConfig(),
			wantErr: "",
		},
		{
			name: "negative reconcile timeout",
			config: &OperatorConfig{
				ReconcileTimeout:        -1 * time.Minute,
				MaxConcurrentReconciles: 3,
				DefaultVaultTimeout:     30 * time.Second,
				Namespace:               "vault",
				MetricsAddr:             ":8080",
				ProbeAddr:               ":8081",
			},
			wantErr: "reconcile timeout must be positive",
		},
		{
			name: "zero max concurrent reconciles",
			config: &OperatorConfig{
				ReconcileTimeout:        5 * time.Minute,
				MaxConcurrentReconciles: 0,
				DefaultVaultTimeout:     30 * time.Second,
				Namespace:               "vault",
				MetricsAddr:             ":8080",
				ProbeAddr:               ":8081",
			},
			wantErr: "max concurrent reconciles must be at least 1",
		},
		{
			name: "negative vault timeout",
			config: &OperatorConfig{
				ReconcileTimeout:        5 * time.Minute,
				MaxConcurrentReconciles: 3,
				DefaultVaultTimeout:     -30 * time.Second,
				Namespace:               "vault",
				MetricsAddr:             ":8080",
				ProbeAddr:               ":8081",
			},
			wantErr: "vault timeout must be positive",
		},
		{
			name: "empty namespace",
			config: &OperatorConfig{
				ReconcileTimeout:        5 * time.Minute,
				MaxConcurrentReconciles: 3,
				DefaultVaultTimeout:     30 * time.Second,
				Namespace:               "",
				MetricsAddr:             ":8080",
				ProbeAddr:               ":8081",
			},
			wantErr: "namespace cannot be empty",
		},
		{
			name: "empty metrics address",
			config: &OperatorConfig{
				ReconcileTimeout:        5 * time.Minute,
				MaxConcurrentReconciles: 3,
				DefaultVaultTimeout:     30 * time.Second,
				Namespace:               "vault",
				MetricsAddr:             "",
				ProbeAddr:               ":8081",
			},
			wantErr: "metrics address cannot be empty",
		},
		{
			name: "empty probe address",
			config: &OperatorConfig{
				ReconcileTimeout:        5 * time.Minute,
				MaxConcurrentReconciles: 3,
				DefaultVaultTimeout:     30 * time.Second,
				Namespace:               "vault",
				MetricsAddr:             ":8080",
				ProbeAddr:               "",
			},
			wantErr: "probe address cannot be empty",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.config.Validate()
			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}
