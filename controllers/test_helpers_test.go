package controllers

import (
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/config"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/discovery"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/health"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/metrics"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/reconciler"
)

// createTestReconciler creates a test reconciler with mock dependencies
func createTestReconciler(c client.Client) *VaultTransitUnsealReconciler {
	cfg := config.NewDefaultConfig()
	metricsRecorder := metrics.NewRecorder()

	// Create service discovery
	serviceDiscovery := &discovery.ServiceDiscovery{
		Client: c,
		Log:    ctrl.Log.WithName("discovery"),
	}

	// Create vault client factory
	vaultFactory := &vaultClientFactory{
		tlsSkipVerify: !cfg.EnableTLSValidation,
		timeout:       cfg.DefaultVaultTimeout,
		discovery:     serviceDiscovery,
	}

	// Create secret manager
	secretMgr := &secretManager{
		client: c,
		log:    ctrl.Log.WithName("secrets"),
	}

	// Create health checker with wrapper
	healthFactory := &healthVaultFactory{inner: vaultFactory}
	healthChecker := health.NewChecker(c, healthFactory, ctrl.Log.WithName("health"))

	// Create vault reconciler with all dependencies
	vaultReconciler := &reconciler.VaultReconciler{
		Client:          c,
		Log:             ctrl.Log.WithName("vault-reconciler"),
		Recorder:        nil, // Event recorder not needed for tests
		VaultFactory:    vaultFactory,
		SecretManager:   secretMgr,
		MetricsRecorder: metricsRecorder,
	}

	return &VaultTransitUnsealReconciler{
		Client:          c,
		Scheme:          c.Scheme(),
		Log:             ctrl.Log.WithName("controllers").WithName("VaultTransitUnseal"),
		Config:          cfg,
		VaultReconciler: vaultReconciler,
		HealthChecker:   healthChecker,
	}
}
