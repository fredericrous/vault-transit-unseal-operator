package config

import (
	"os"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    *OperatorConfig
		wantErr bool
	}{
		{
			name: "default configuration",
			env:  map[string]string{},
			want: &OperatorConfig{
				EnableMetrics:           true,
				EnableLeaderElection:    false,
				ReconcileTimeout:        5 * time.Minute,
				MaxConcurrentReconciles: 3,
				Namespace:               "vault",
				DefaultVaultTimeout:     30 * time.Second,
				EnableTLSValidation:     true,
				MetricsAddr:             ":8080",
				ProbeAddr:               ":8081",
				LeaderElectionID:        "vault-operator",
			},
			wantErr: false,
		},
		{
			name: "custom configuration",
			env: map[string]string{
				"ENABLE_METRICS":            "false",
				"ENABLE_LEADER_ELECTION":    "true",
				"RECONCILE_TIMEOUT":         "10m",
				"MAX_CONCURRENT_RECONCILES": "5",
				"NAMESPACE":                 "custom-vault",
				"VAULT_TIMEOUT":             "1m",
				"VAULT_TLS_VALIDATION":      "false",
				"METRICS_ADDR":              ":9090",
				"PROBE_ADDR":                ":9091",
				"LEADER_ELECTION_ID":        "custom-operator",
			},
			want: &OperatorConfig{
				EnableMetrics:           false,
				EnableLeaderElection:    true,
				ReconcileTimeout:        10 * time.Minute,
				MaxConcurrentReconciles: 5,
				Namespace:               "custom-vault",
				DefaultVaultTimeout:     1 * time.Minute,
				EnableTLSValidation:     false,
				MetricsAddr:             ":9090",
				ProbeAddr:               ":9091",
				LeaderElectionID:        "custom-operator",
			},
			wantErr: false,
		},
		{
			name: "invalid reconcile timeout",
			env: map[string]string{
				"RECONCILE_TIMEOUT": "-5m",
			},
			wantErr: true,
		},
		{
			name: "invalid max concurrent reconciles",
			env: map[string]string{
				"MAX_CONCURRENT_RECONCILES": "0",
			},
			wantErr: true,
		},
		{
			name: "empty namespace",
			env: map[string]string{
				"NAMESPACE": "",
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Save and restore environment
			oldEnv := make(map[string]string)
			for k := range tt.env {
				oldEnv[k] = os.Getenv(k)
				require.NoError(t, os.Setenv(k, tt.env[k]))
			}
			defer func() {
				for k, v := range oldEnv {
					if v == "" {
						require.NoError(t, os.Unsetenv(k))
					} else {
						require.NoError(t, os.Setenv(k, v))
					}
				}
			}()

			got, err := LoadConfig()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.want, got)
			}
		})
	}
}

func TestOperatorConfig_Validate(t *testing.T) {
	tests := []struct {
		name    string
		config  *OperatorConfig
		wantErr string
	}{
		{
			name: "valid config",
			config: &OperatorConfig{
				ReconcileTimeout:        5 * time.Minute,
				MaxConcurrentReconciles: 3,
				DefaultVaultTimeout:     30 * time.Second,
				Namespace:               "vault",
			},
			wantErr: "",
		},
		{
			name: "negative reconcile timeout",
			config: &OperatorConfig{
				ReconcileTimeout:        -1 * time.Minute,
				MaxConcurrentReconciles: 3,
				DefaultVaultTimeout:     30 * time.Second,
				Namespace:               "vault",
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
			},
			wantErr: "namespace cannot be empty",
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

func TestGetEnvHelpers(t *testing.T) {
	t.Run("getEnvString", func(t *testing.T) {
		require.NoError(t, os.Setenv("TEST_STRING", "value"))
		defer func() { require.NoError(t, os.Unsetenv("TEST_STRING")) }()

		assert.Equal(t, "value", getEnvString("TEST_STRING", "default"))
		assert.Equal(t, "default", getEnvString("NON_EXISTENT", "default"))
	})

	t.Run("getEnvBool", func(t *testing.T) {
		require.NoError(t, os.Setenv("TEST_TRUE", "true"))
		require.NoError(t, os.Setenv("TEST_FALSE", "false"))
		require.NoError(t, os.Setenv("TEST_INVALID", "invalid"))
		defer func() {
			require.NoError(t, os.Unsetenv("TEST_TRUE"))
			_ = os.Unsetenv("TEST_FALSE")
			_ = os.Unsetenv("TEST_INVALID")
		}()

		assert.True(t, getEnvBool("TEST_TRUE", false))
		assert.False(t, getEnvBool("TEST_FALSE", true))
		assert.True(t, getEnvBool("TEST_INVALID", true)) // defaults on invalid
		assert.False(t, getEnvBool("NON_EXISTENT", false))
	})

	t.Run("getEnvInt", func(t *testing.T) {
		require.NoError(t, os.Setenv("TEST_INT", "42"))
		require.NoError(t, os.Setenv("TEST_INVALID", "invalid"))
		defer func() {
			_ = os.Unsetenv("TEST_INT")
			_ = os.Unsetenv("TEST_INVALID")
		}()

		assert.Equal(t, 42, getEnvInt("TEST_INT", 10))
		assert.Equal(t, 10, getEnvInt("TEST_INVALID", 10)) // defaults on invalid
		assert.Equal(t, 10, getEnvInt("NON_EXISTENT", 10))
	})

	t.Run("getEnvDuration", func(t *testing.T) {
		require.NoError(t, os.Setenv("TEST_DURATION", "5m"))
		require.NoError(t, os.Setenv("TEST_INVALID", "invalid"))
		defer func() {
			_ = os.Unsetenv("TEST_DURATION")
			_ = os.Unsetenv("TEST_INVALID")
		}()

		assert.Equal(t, 5*time.Minute, getEnvDuration("TEST_DURATION", time.Hour))
		assert.Equal(t, time.Hour, getEnvDuration("TEST_INVALID", time.Hour)) // defaults on invalid
		assert.Equal(t, time.Hour, getEnvDuration("NON_EXISTENT", time.Hour))
	})
}
