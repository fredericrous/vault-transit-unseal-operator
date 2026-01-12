package health

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

// Checker provides health and readiness checks
type Checker struct {
	client client.Client
	log    logr.Logger

	mu            sync.RWMutex
	lastCheckTime time.Time
	lastStatus    CheckResult
}

// CheckResult contains health check results
type CheckResult struct {
	Healthy       bool
	Ready         bool
	Message       string
	VaultPods     int
	HealthyVaults int
}

// VaultClientFactory creates Vault clients for pods
// Note: This interface is kept for API compatibility but is no longer used
// by health probes. Health probes are now lightweight and don't call Vault.
type VaultClientFactory interface {
	NewClientForPod(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) (vault.Client, error)
}

// NewChecker creates a new health checker
// Note: vaultFactory parameter is kept for API compatibility but is no longer used.
// Health probes are now lightweight K8s API calls that don't connect to Vault.
func NewChecker(client client.Client, vaultFactory VaultClientFactory, log logr.Logger) *Checker {
	return &Checker{
		client: client,
		log:    log,
	}
}

// Liveness checks if the operator is alive
// This is a lightweight check that verifies the operator process is responsive
func (h *Checker) Liveness(ctx context.Context) error {
	// Simple liveness: can we list VaultTransitUnseal resources?
	// This verifies API connectivity without calling Vault
	list := &vaultv1alpha1.VaultTransitUnsealList{}
	if err := h.client.List(ctx, list); err != nil {
		return fmt.Errorf("cannot list VaultTransitUnseal resources: %w", err)
	}
	return nil
}

// Readiness checks if the operator is ready to handle requests
// This is a lightweight check that does NOT call Vault - it only verifies
// the operator can communicate with the Kubernetes API
func (h *Checker) Readiness(ctx context.Context) error {
	// Simple readiness: can we list resources?
	// We intentionally do NOT check Vault connectivity here because:
	// 1. Vault may be sealed (which is expected during bootstrap)
	// 2. Health probes have short timeouts that can't accommodate Vault calls over mesh
	// 3. The operator's job is to unseal Vault, so it should be ready even when Vault isn't
	list := &vaultv1alpha1.VaultTransitUnsealList{}
	if err := h.client.List(ctx, list); err != nil {
		return fmt.Errorf("cannot list VaultTransitUnseal resources: %w", err)
	}

	// Update last status for informational purposes (without Vault checks)
	h.mu.Lock()
	h.lastCheckTime = time.Now()
	h.lastStatus = CheckResult{
		Ready:   true,
		Healthy: true,
		Message: fmt.Sprintf("operator ready, managing %d VaultTransitUnseal resource(s)", len(list.Items)),
	}
	h.mu.Unlock()

	return nil
}

// GetLastStatus returns the last health check status
func (h *Checker) GetLastStatus() (CheckResult, time.Time) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastStatus, h.lastCheckTime
}
