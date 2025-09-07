package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestNewRecorder(t *testing.T) {
	// Create a new registry for testing to avoid conflicts
	registry := prometheus.NewRegistry()

	// Temporarily replace the global registry
	oldRegistry := prometheus.DefaultRegisterer
	prometheus.DefaultRegisterer = registry
	defer func() {
		prometheus.DefaultRegisterer = oldRegistry
	}()

	recorder := NewRecorder()
	assert.NotNil(t, recorder)
	assert.NotNil(t, recorder.reconciliationDuration)
	assert.NotNil(t, recorder.reconciliationTotal)
	assert.NotNil(t, recorder.vaultStatus)
	assert.NotNil(t, recorder.initializationTotal)
}

func TestRecordReconciliation(t *testing.T) {
	tests := []struct {
		name     string
		duration time.Duration
		success  bool
		want     float64
	}{
		{
			name:     "successful reconciliation",
			duration: 100 * time.Millisecond,
			success:  true,
			want:     1,
		},
		{
			name:     "failed reconciliation",
			duration: 200 * time.Millisecond,
			success:  false,
			want:     1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &Recorder{
				reconciliationDuration: prometheus.NewHistogramVec(
					prometheus.HistogramOpts{
						Name: "test_reconciliation_duration",
						Help: "Test duration",
					},
					[]string{"success"},
				),
				reconciliationTotal: prometheus.NewCounterVec(
					prometheus.CounterOpts{
						Name: "test_reconciliation_total",
						Help: "Test total",
					},
					[]string{"success"},
				),
			}

			recorder.RecordReconciliation(tt.duration, tt.success)

			successLabel := "false"
			if tt.success {
				successLabel = "true"
			}

			// Check counter was incremented
			counter := recorder.reconciliationTotal.WithLabelValues(successLabel)
			assert.Equal(t, tt.want, testutil.ToFloat64(counter))

			// Check histogram was recorded (just verify it exists)
			histogram := recorder.reconciliationDuration.WithLabelValues(successLabel)
			assert.NotNil(t, histogram)
		})
	}
}

func TestRecordVaultStatus(t *testing.T) {
	tests := []struct {
		name        string
		initialized bool
		sealed      bool
	}{
		{
			name:        "initialized and unsealed",
			initialized: true,
			sealed:      false,
		},
		{
			name:        "not initialized and sealed",
			initialized: false,
			sealed:      true,
		},
		{
			name:        "initialized and sealed",
			initialized: true,
			sealed:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &Recorder{
				vaultStatus: prometheus.NewGaugeVec(
					prometheus.GaugeOpts{
						Name: "test_vault_status",
						Help: "Test status",
					},
					[]string{"status"},
				),
			}

			recorder.RecordVaultStatus(tt.initialized, tt.sealed)

			// Check initialized status
			initializedGauge := recorder.vaultStatus.WithLabelValues("initialized")
			expectedInitialized := float64(0)
			if tt.initialized {
				expectedInitialized = 1
			}
			assert.Equal(t, expectedInitialized, testutil.ToFloat64(initializedGauge))

			// Check sealed status
			sealedGauge := recorder.vaultStatus.WithLabelValues("sealed")
			expectedSealed := float64(0)
			if tt.sealed {
				expectedSealed = 1
			}
			assert.Equal(t, expectedSealed, testutil.ToFloat64(sealedGauge))
		})
	}
}

func TestRecordInitialization(t *testing.T) {
	tests := []struct {
		name    string
		success bool
		want    float64
	}{
		{
			name:    "successful initialization",
			success: true,
			want:    1,
		},
		{
			name:    "failed initialization",
			success: false,
			want:    1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &Recorder{
				initializationTotal: prometheus.NewCounterVec(
					prometheus.CounterOpts{
						Name: "test_initialization_total",
						Help: "Test total",
					},
					[]string{"success"},
				),
			}

			recorder.RecordInitialization(tt.success)

			successLabel := "false"
			if tt.success {
				successLabel = "true"
			}

			counter := recorder.initializationTotal.WithLabelValues(successLabel)
			assert.Equal(t, tt.want, testutil.ToFloat64(counter))
		})
	}
}

func TestRecorderIntegration(t *testing.T) {
	// Create a new registry for this test
	registry := prometheus.NewRegistry()

	// Create recorder with custom registry
	recorder := &Recorder{
		reconciliationDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "vault_operator_reconciliation_duration_seconds",
				Help:    "Duration of reconciliation in seconds",
				Buckets: prometheus.ExponentialBuckets(0.001, 2, 15),
			},
			[]string{"success"},
		),
		reconciliationTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "vault_operator_reconciliation_total",
				Help: "Total number of reconciliations",
			},
			[]string{"success"},
		),
		vaultStatus: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "vault_operator_vault_status",
				Help: "Vault status (1 = true, 0 = false)",
			},
			[]string{"status"},
		),
		initializationTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "vault_operator_initialization_total",
				Help: "Total number of Vault initializations",
			},
			[]string{"success"},
		),
	}

	// Register metrics with the test registry
	require.NoError(t, registry.Register(recorder.reconciliationDuration))
	require.NoError(t, registry.Register(recorder.reconciliationTotal))
	require.NoError(t, registry.Register(recorder.vaultStatus))
	require.NoError(t, registry.Register(recorder.initializationTotal))

	// Record various metrics
	recorder.RecordReconciliation(100*time.Millisecond, true)
	recorder.RecordReconciliation(200*time.Millisecond, false)
	recorder.RecordVaultStatus(true, false)
	recorder.RecordInitialization(true)
	recorder.RecordInitialization(false)

	// Verify metrics were recorded
	metricFamilies, err := registry.Gather()
	require.NoError(t, err)

	// Should have 4 metric families
	assert.Len(t, metricFamilies, 4)

	// Verify each metric family exists
	metricNames := make(map[string]bool)
	for _, mf := range metricFamilies {
		metricNames[mf.GetName()] = true
	}

	assert.True(t, metricNames["vault_operator_reconciliation_duration_seconds"])
	assert.True(t, metricNames["vault_operator_reconciliation_total"])
	assert.True(t, metricNames["vault_operator_vault_status"])
	assert.True(t, metricNames["vault_operator_initialization_total"])
}
