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
	client       client.Client
	vaultFactory VaultClientFactory
	log          logr.Logger

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
type VaultClientFactory interface {
	NewClientForPod(pod *corev1.Pod) (vault.Client, error)
}

// NewChecker creates a new health checker
func NewChecker(client client.Client, vaultFactory VaultClientFactory, log logr.Logger) *Checker {
	return &Checker{
		client:       client,
		vaultFactory: vaultFactory,
		log:          log,
	}
}

// Liveness checks if the operator is alive
func (h *Checker) Liveness(ctx context.Context) error {
	// Basic liveness: can we list resources?
	list := &vaultv1alpha1.VaultTransitUnsealList{}
	if err := h.client.List(ctx, list); err != nil {
		return fmt.Errorf("cannot list VaultTransitUnseal resources: %w", err)
	}
	return nil
}

// Readiness checks if the operator is ready to handle requests
func (h *Checker) Readiness(ctx context.Context) error {
	result := h.performCheck(ctx)

	h.mu.Lock()
	h.lastCheckTime = time.Now()
	h.lastStatus = result
	h.mu.Unlock()

	if !result.Ready {
		return fmt.Errorf("%s", result.Message)
	}
	return nil
}

// GetLastStatus returns the last health check status
func (h *Checker) GetLastStatus() (CheckResult, time.Time) {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return h.lastStatus, h.lastCheckTime
}

func (h *Checker) performCheck(ctx context.Context) CheckResult {
	// List all VaultTransitUnseal resources
	vtuList := &vaultv1alpha1.VaultTransitUnsealList{}
	if err := h.client.List(ctx, vtuList); err != nil {
		return CheckResult{
			Ready:   false,
			Message: fmt.Sprintf("failed to list VaultTransitUnseal resources: %v", err),
		}
	}

	if len(vtuList.Items) == 0 {
		return CheckResult{
			Ready:   true,
			Healthy: true,
			Message: "no VaultTransitUnseal resources to manage",
		}
	}

	// Check Vault pods for each resource
	totalPods := 0
	healthyPods := 0

	for _, vtu := range vtuList.Items {
		pods, healthy := h.checkVaultPods(ctx, &vtu)
		totalPods += pods
		healthyPods += healthy
	}

	result := CheckResult{
		VaultPods:     totalPods,
		HealthyVaults: healthyPods,
		Healthy:       healthyPods > 0 || totalPods == 0,
		Ready:         true,
	}

	switch {
	case totalPods == 0:
		result.Healthy = false
		result.Message = "no Vault pods found"
	case healthyPods == 0:
		result.Healthy = false
		result.Message = fmt.Sprintf("no healthy Vault pods (0/%d)", totalPods)
	case healthyPods < totalPods:
		result.Message = fmt.Sprintf("%d/%d Vault pods are healthy", healthyPods, totalPods)
	default:
		result.Message = fmt.Sprintf("all %d Vault pods are healthy", totalPods)
	}

	return result
}

func (h *Checker) checkVaultPods(ctx context.Context, vtu *vaultv1alpha1.VaultTransitUnseal) (pods int, healthy int) {
	podList := &corev1.PodList{}
	if err := h.client.List(ctx, podList,
		client.InNamespace(vtu.Spec.VaultPod.Namespace),
		client.MatchingLabels(vtu.Spec.VaultPod.Selector)); err != nil {
		h.log.Error(err, "failed to list Vault pods",
			"namespace", vtu.Spec.VaultPod.Namespace,
			"selector", vtu.Spec.VaultPod.Selector)
		return 0, 0
	}

	pods = len(podList.Items)

	for _, pod := range podList.Items {
		if h.isPodHealthy(ctx, &pod) {
			healthy++
		}
	}

	return pods, healthy
}

func (h *Checker) isPodHealthy(ctx context.Context, pod *corev1.Pod) bool {
	// Check if pod is running
	if pod.Status.Phase != corev1.PodRunning {
		return false
	}

	// Check if vault container is ready
	for _, cs := range pod.Status.ContainerStatuses {
		if cs.Name == "vault" && !cs.Ready {
			return false
		}
	}

	// Try to connect to Vault
	vaultClient, err := h.vaultFactory.NewClientForPod(pod)
	if err != nil {
		h.log.V(1).Info("failed to create Vault client", "pod", pod.Name, "error", err)
		return false
	}

	// Use a short timeout for health checks
	checkCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()

	return vaultClient.IsHealthy(checkCtx)
}
