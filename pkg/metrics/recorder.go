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

	// Admin token rotation. Timestamps rather than ages, so an alert on
	// time() - x keeps moving when the operator stops observing.
	adminTokenCreated           prometheus.Gauge
	adminTokenLastObservation   prometheus.Gauge
	adminTokenRotationPeriod    prometheus.Gauge
	adminTokenAutoRotate        prometheus.Gauge
	adminTokenLedgerEntries     prometheus.Gauge
	adminTokenRevocationsPend   prometheus.Gauge
	adminTokenOldestDueRevoke   prometheus.Gauge
	adminTokenUnresolvedMints   prometheus.Gauge
	adminTokenRotationsTotal    *prometheus.CounterVec
	adminTokenBackupFailures    prometheus.Counter
	adminTokenRevokeSkippedLive prometheus.Counter
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
		adminTokenCreated: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vault_admin_token_created_timestamp_seconds",
			Help: "Vault creation_time of the current admin token, unix seconds. Set only on a successful lookup; 0 until the first one.",
		}),
		adminTokenLastObservation: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vault_admin_token_last_observation_timestamp_seconds",
			Help: "Unix time of the last successful lookup-self of the admin token. Stops moving when the check loop or Vault lookups stop, which freezes every other admin-token gauge.",
		}),
		adminTokenRotationPeriod: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vault_admin_token_rotation_period_seconds",
			Help: "Configured spec.tokenManagement.rotationPeriod, in seconds.",
		}),
		adminTokenAutoRotate: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vault_admin_token_auto_rotate",
			Help: "1 when scheduled rotation (spec.tokenManagement.autoRotate) is on, else 0.",
		}),
		adminTokenLedgerEntries: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vault_admin_token_ledger_entries",
			Help: "Entries in the admin Secret's revoke ledger: retired tokens awaiting revocation and mints whose swap is unresolved.",
		}),
		adminTokenRevocationsPend: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vault_admin_token_revocations_pending",
			Help: "Admin tokens the operator retired or minted and has not yet seen to its end (the revoke ledger's entries). Drain to 0 before downgrading to an operator without rotation.",
		}),
		adminTokenOldestDueRevoke: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vault_admin_token_oldest_due_revoke_timestamp_seconds",
			Help: "notBefore of the oldest scheduled revocation that is already due, unix seconds; 0 when none is due.",
		}),
		adminTokenUnresolvedMints: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "vault_admin_token_unresolved_mints",
			Help: "Minted admin tokens whose swap into the Secret has an unknown outcome. Nothing revokes them until a fence resolves it.",
		}),
		adminTokenRotationsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "vault_admin_token_rotations_total",
			Help: "Admin token rotation attempts by outcome. A failure is any step that stopped a rotation or a revocation: ledger, mint, swap, fence, revoke or verify.",
		}, []string{"result"}),
		adminTokenBackupFailures: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vault_admin_token_backup_failures_total",
			Help: "Transit backups of a freshly rotated admin token that failed. Recovery then falls back to Kubernetes-auth self-heal.",
		}),
		adminTokenRevokeSkippedLive: prometheus.NewCounter(prometheus.CounterOpts{
			Name: "vault_admin_token_revoke_skipped_current_total",
			Help: "Ledger entries dropped unrevoked because the token they retire is the one in the Secret again: a retired credential is live.",
		}),
	}
	// Both outcomes exist from the start so increase() sees the first one.
	r.adminTokenRotationsTotal.WithLabelValues("success")
	r.adminTokenRotationsTotal.WithLabelValues("failure")

	// Register metrics
	metrics.Registry.MustRegister(
		r.reconciliationDuration,
		r.reconciliationTotal,
		r.vaultStatus,
		r.initializationTotal,
		r.unsealAttemptsTotal,
		r.adminTokenTTL,
		r.adminTokenCreated,
		r.adminTokenLastObservation,
		r.adminTokenRotationPeriod,
		r.adminTokenAutoRotate,
		r.adminTokenLedgerEntries,
		r.adminTokenRevocationsPend,
		r.adminTokenOldestDueRevoke,
		r.adminTokenUnresolvedMints,
		r.adminTokenRotationsTotal,
		r.adminTokenBackupFailures,
		r.adminTokenRevokeSkippedLive,
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

// AdminTokenObservation is one successful look at the admin token.
type AdminTokenObservation struct {
	Created        time.Time
	ObservedAt     time.Time
	RotationPeriod time.Duration
	AutoRotate     bool
}

// RecordAdminTokenObservation publishes what a successful lookup-self saw.
func (r *Recorder) RecordAdminTokenObservation(o AdminTokenObservation) {
	r.adminTokenCreated.Set(float64(o.Created.Unix()))
	r.adminTokenLastObservation.Set(float64(o.ObservedAt.Unix()))
	r.adminTokenRotationPeriod.Set(o.RotationPeriod.Seconds())
	autoRotate := 0.0
	if o.AutoRotate {
		autoRotate = 1
	}
	r.adminTokenAutoRotate.Set(autoRotate)
}

// RecordAdminTokenLedger publishes the revoke ledger's state. oldestDue is
// zero when no scheduled revocation is due.
func (r *Recorder) RecordAdminTokenLedger(entries, unresolvedMints int, oldestDue time.Time) {
	r.adminTokenLedgerEntries.Set(float64(entries))
	r.adminTokenRevocationsPend.Set(float64(entries))
	r.adminTokenUnresolvedMints.Set(float64(unresolvedMints))
	due := 0.0
	if !oldestDue.IsZero() {
		due = float64(oldestDue.Unix())
	}
	r.adminTokenOldestDueRevoke.Set(due)
}

// RecordAdminTokenRotation counts one rotation outcome.
func (r *Recorder) RecordAdminTokenRotation(success bool) {
	result := "failure"
	if success {
		result = "success"
	}
	r.adminTokenRotationsTotal.WithLabelValues(result).Inc()
}

// RecordAdminTokenBackupFailure counts a failed transit backup of a rotated token.
func (r *Recorder) RecordAdminTokenBackupFailure() {
	r.adminTokenBackupFailures.Inc()
}

// RecordAdminTokenRevokeSkippedCurrent counts a retired token found live again.
func (r *Recorder) RecordAdminTokenRevokeSkippedCurrent() {
	r.adminTokenRevokeSkippedLive.Inc()
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
