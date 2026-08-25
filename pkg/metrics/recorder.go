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
	unsealAttemptsTotal    *prometheus.CounterVec
	adminTokenTTL          prometheus.Gauge
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
		unsealAttemptsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "vault_operator_unseal_attempts_total",
				Help: "Total unseal attempts the operator drove over the Vault API, by unseal mode and outcome. Only stored-key mode increments it: transit mode has no API-driven unseal, it restarts the pod. Failures are result=\"failure\"; pair with vault_operator_vault_status{status=\"sealed\"} to alert on a Vault that stays sealed across attempts.",
			},
			[]string{"mode", "result"},
		),
		adminTokenTTL: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "vault_admin_token_ttl_seconds",
				Help: "Remaining TTL of the Vault admin token in seconds, observed on each renewal check. Drives VaultTokenExpiring* alerts directly instead of via the Secret resource_version proxy.",
			},
		),
	}

	// Register metrics
	metrics.Registry.MustRegister(
		r.reconciliationDuration,
		r.reconciliationTotal,
		r.vaultStatus,
		r.initializationTotal,
		r.unsealAttemptsTotal,
		r.adminTokenTTL,
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

// RecordAdminTokenTTL records the admin token's remaining TTL in seconds.
func (r *Recorder) RecordAdminTokenTTL(seconds float64) {
	r.adminTokenTTL.Set(seconds)
}

// RecordUnsealAttempt records one API-driven unseal attempt for a mode.
func (r *Recorder) RecordUnsealAttempt(mode string, success bool) {
	result := "failure"
	if success {
		result = "success"
	}

	r.unsealAttemptsTotal.WithLabelValues(mode, result).Inc()
}

// RecordInitialization records initialization metrics
func (r *Recorder) RecordInitialization(success bool) {
	successLabel := "false"
	if success {
		successLabel = "true"
	}

	r.initializationTotal.WithLabelValues(successLabel).Inc()
}
