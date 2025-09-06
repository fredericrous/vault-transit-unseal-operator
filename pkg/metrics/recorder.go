package metrics

import (
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	once      sync.Once
	singleton *Recorder
)

// Recorder implements MetricsRecorder interface
type Recorder struct {
	reconciliationDuration *prometheus.HistogramVec
	reconciliationTotal    *prometheus.CounterVec
	vaultStatus            *prometheus.GaugeVec
	initializationTotal    *prometheus.CounterVec
}

// NewRecorder creates a new metrics recorder (singleton)
func NewRecorder() *Recorder {
	once.Do(func() {
		singleton = createRecorder()
	})
	return singleton
}

// createRecorder creates the actual recorder instance
func createRecorder() *Recorder {
	r := &Recorder{
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

	// Register metrics
	metrics.Registry.MustRegister(
		r.reconciliationDuration,
		r.reconciliationTotal,
		r.vaultStatus,
		r.initializationTotal,
	)

	return r
}

// RecordReconciliation records reconciliation metrics
func (r *Recorder) RecordReconciliation(duration time.Duration, success bool) {
	successLabel := "false"
	if success {
		successLabel = "true"
	}

	r.reconciliationDuration.WithLabelValues(successLabel).Observe(duration.Seconds())
	r.reconciliationTotal.WithLabelValues(successLabel).Inc()
}

// RecordVaultStatus records Vault status metrics
func (r *Recorder) RecordVaultStatus(initialized, sealed bool) {
	if initialized {
		r.vaultStatus.WithLabelValues("initialized").Set(1)
	} else {
		r.vaultStatus.WithLabelValues("initialized").Set(0)
	}

	if sealed {
		r.vaultStatus.WithLabelValues("sealed").Set(1)
	} else {
		r.vaultStatus.WithLabelValues("sealed").Set(0)
	}
}

// RecordInitialization records initialization metrics
func (r *Recorder) RecordInitialization(success bool) {
	successLabel := "false"
	if success {
		successLabel = "true"
	}

	r.initializationTotal.WithLabelValues(successLabel).Inc()
}
