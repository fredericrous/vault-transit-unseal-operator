package controllers

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/builder"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/predicate"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/config"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/configuration"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/discovery"
	operrors "github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/errors"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/health"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/metrics"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/reconciler"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/secrets"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/token"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/vault"
)

// VaultTransitUnsealReconciler reconciles a VaultTransitUnseal object
type VaultTransitUnsealReconciler struct {
	client.Client
	Log             logr.Logger
	Scheme          *runtime.Scheme
	Recorder        record.EventRecorder
	Config          *config.OperatorConfig
	VaultReconciler *reconciler.VaultReconciler
	HealthChecker   *health.Checker
}

// SetupWithManager sets up the controller with the Manager.
func (r *VaultTransitUnsealReconciler) SetupWithManager(mgr ctrl.Manager) error {
	// Initialize config if not set
	if r.Config == nil {
		r.Config = config.NewDefaultConfig()
	}

	// Create metrics recorder
	metricsRecorder := metrics.NewRecorder()

	// Create service discovery
	serviceDiscovery := &discovery.ServiceDiscovery{
		Client: r.Client,
		Log:    r.Log.WithName("discovery"),
	}

	// Create vault client factory
	vaultFactory := &vaultClientFactory{
		tlsSkipVerify: !r.Config.EnableTLSValidation,
		caCert:        os.Getenv("VAULT_CACERT"),
		timeout:       r.Config.DefaultVaultTimeout,
		discovery:     serviceDiscovery,
	}

	// Create secret manager
	secretMgr := &secretManager{
		client: r.Client,
		log:    r.Log.WithName("secrets"),
	}

	// Create configurator
	configurator := configuration.NewConfigurator(r.Log.WithName("configurator"))

	// Create secret verifier
	secretVerifier := secrets.NewVerifier(r.Client, r.Log.WithName("secret-verifier"))

	// Create simplified token manager for hybrid approach
	tokenManager := token.NewSimpleManager(r.Client, r.Log.WithName("token-manager"), r.Scheme, serviceDiscovery)

	// Create recovery manager
	recoveryManager := secrets.NewRecoveryManager(r.Client, r.Log.WithName("recovery-manager"), r.Recorder, r.Scheme, secretMgr, tokenManager, os.Getenv("TRANSIT_VAULT_CACERT"))

	// Create vault reconciler with all dependencies
	r.VaultReconciler = &reconciler.VaultReconciler{
		Client:             r.Client,
		Log:                r.Log.WithName("vault-reconciler"),
		Recorder:           r.Recorder,
		VaultFactory:       vaultFactory,
		SecretManager:      secretMgr,
		MetricsRecorder:    metricsRecorder,
		Configurator:       configurator,
		SecretVerifier:     secretVerifier,
		RecoveryManager:    recoveryManager,
		TokenManager:       tokenManager,
		TransitVaultCACert: os.Getenv("TRANSIT_VAULT_CACERT"),
	}

	// Create health checker with simple factory wrapper
	healthFactory := &healthVaultFactory{inner: vaultFactory}
	r.HealthChecker = health.NewChecker(r.Client, healthFactory, r.Log.WithName("health"))

	// Configure controller options
	opts := controller.Options{
		MaxConcurrentReconciles: r.Config.MaxConcurrentReconciles,
	}

	return ctrl.NewControllerManagedBy(mgr).
		For(&vaultv1alpha1.VaultTransitUnseal{}).
		Owns(&corev1.Secret{}).
		Watches(
			&corev1.ConfigMap{},
			r.enqueueRequestsForConfigMap(),
			builder.WithPredicates(
				predicate.ResourceVersionChangedPredicate{},
			),
		).
		Watches(
			&corev1.Secret{},
			r.enqueueRequestsForSecret(),
			builder.WithPredicates(
				predicate.ResourceVersionChangedPredicate{},
			),
		).
		WithOptions(opts).
		Complete(r)
}

// Reconcile handles the reconciliation loop
// +kubebuilder:rbac:groups=vault.homelab.io,resources=vaulttransitunseals,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=vault.homelab.io,resources=vaulttransitunseals/status,verbs=get;update;patch
// +kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=secrets,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch
// +kubebuilder:rbac:groups=coordination.k8s.io,resources=leases,verbs=get;list;create;update
// +kubebuilder:rbac:groups="",resources=configmaps,verbs=get;list;watch;create;update;patch;delete

func (r *VaultTransitUnsealReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.Log.WithValues("resource", req.NamespacedName, "trace_id", generateTraceID())
	ctx = logr.NewContext(ctx, log)

	log.V(1).Info("Starting reconciliation")

	// Fetch the VaultTransitUnseal instance
	vtu := &vaultv1alpha1.VaultTransitUnseal{}
	if err := r.Get(ctx, req.NamespacedName, vtu); err != nil {
		if errors.IsNotFound(err) {
			log.V(1).Info("Resource not found, likely deleted")
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, operrors.NewTransientError("failed to get VaultTransitUnseal", err).
			WithContext("resource", req.NamespacedName)
	}

	// Delegate to vault reconciler
	result := r.VaultReconciler.Reconcile(ctx, vtu)

	// Handle result
	if result.Error != nil {
		log.Error(result.Error, "Reconciliation failed")

		// Check if we should retry
		if operrors.ShouldRetry(result.Error) {
			// Use exponential backoff for transient errors
			return ctrl.Result{
				RequeueAfter: calculateBackoff(vtu, result.RequeueAfter),
			}, nil
		}

		// Don't retry permanent errors
		return ctrl.Result{}, result.Error
	}

	log.V(1).Info("Reconciliation completed successfully", "requeueAfter", result.RequeueAfter)
	return ctrl.Result{RequeueAfter: result.RequeueAfter}, nil
}

// vaultClientFactory implements reconciler.VaultClientFactory
type vaultClientFactory struct {
	tlsSkipVerify bool
	caCert        string
	timeout       time.Duration
	discovery     *discovery.ServiceDiscovery
}

func (f *vaultClientFactory) NewClientForPod(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) (vault.Client, error) {
	// Always use service discovery, no fallback to pod IP
	address, err := f.discovery.GetVaultServiceEndpoint(ctx, &vtu.Spec.VaultPod, pod)
	if err != nil {
		return nil, operrors.NewConfigError("service discovery failed", err).
			WithContext("pod", pod.Name).
			WithContext("namespace", pod.Namespace)
	}

	return vault.NewClient(&vault.Config{
		Address:       address,
		TLSSkipVerify: f.tlsSkipVerify,
		CACert:        f.caCert,
		Timeout:       f.timeout,
	})
}

// secretManager implements reconciler.SecretManager
type secretManager struct {
	client client.Client
	log    logr.Logger
}

func (s *secretManager) CreateOrUpdate(ctx context.Context, namespace, name string, data map[string][]byte) error {
	return s.CreateOrUpdateWithOptions(ctx, namespace, name, data, nil)
}

func (s *secretManager) CreateOrUpdateWithOptions(ctx context.Context, namespace, name string, data map[string][]byte, annotations map[string]string) error {
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
	}

	op, err := controllerutil.CreateOrUpdate(ctx, s.client, secret, func() error {
		// Preserve existing annotations
		if secret.Annotations == nil {
			secret.Annotations = make(map[string]string)
		}

		// Apply provided annotations
		for k, v := range annotations {
			secret.Annotations[k] = v
		}

		// Set data
		secret.Data = data
		return nil
	})

	if err != nil {
		return operrors.NewTransientError("failed to create/update secret", err).
			WithContext("namespace", namespace).
			WithContext("name", name)
	}

	s.log.V(1).Info("Secret operation completed",
		"operation", op,
		"namespace", namespace,
		"name", name,
		"annotationCount", len(annotations))
	return nil
}

func (s *secretManager) Get(ctx context.Context, namespace, name, key string) ([]byte, error) {
	secret := &corev1.Secret{}
	if err := s.client.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		if errors.IsNotFound(err) {
			return nil, operrors.NewConfigError("secret not found", err).
				WithContext("namespace", namespace).
				WithContext("name", name)
		}
		return nil, operrors.NewTransientError("failed to get secret", err).
			WithContext("namespace", namespace).
			WithContext("name", name)
	}

	value, ok := secret.Data[key]
	if !ok {
		return nil, operrors.NewConfigError("key not found in secret", nil).
			WithContext("namespace", namespace).
			WithContext("name", name).
			WithContext("key", key)
	}

	return value, nil
}

// healthVaultFactory wraps vaultClientFactory for health checks
type healthVaultFactory struct {
	inner *vaultClientFactory
}

func (h *healthVaultFactory) NewClientForPod(ctx context.Context, pod *corev1.Pod, vtu *vaultv1alpha1.VaultTransitUnseal) (vault.Client, error) {
	// Delegate to the inner factory which uses service discovery
	return h.inner.NewClientForPod(ctx, pod, vtu)
}

// Helper functions

func calculateBackoff(vtu *vaultv1alpha1.VaultTransitUnseal, defaultDuration time.Duration) time.Duration {
	// Simple exponential backoff based on failure count
	// In production, you'd track failure count in status
	baseInterval := defaultDuration
	if baseInterval == 0 {
		baseInterval = 30 * time.Second
	}

	// Cap at 5 minutes
	if baseInterval > 5*time.Minute {
		return 5 * time.Minute
	}

	return baseInterval
}

func generateTraceID() string {
	// Simple trace ID generation
	// In production, integrate with distributed tracing
	return fmt.Sprintf("%d", time.Now().UnixNano())
}

// RegisterHealthChecks registers health check endpoints
func RegisterHealthChecks(mgr manager.Manager, checker *health.Checker) error {
	// Wrap the functions to match the expected interface
	livenessCheck := func(req *http.Request) error {
		return checker.Liveness(req.Context())
	}

	readinessCheck := func(req *http.Request) error {
		return checker.Readiness(req.Context())
	}

	if err := mgr.AddHealthzCheck("operator-health", livenessCheck); err != nil {
		return fmt.Errorf("failed to add liveness check: %w", err)
	}

	if err := mgr.AddReadyzCheck("operator-ready", readinessCheck); err != nil {
		return fmt.Errorf("failed to add readiness check: %w", err)
	}

	return nil
}

// enqueueRequestsForConfigMap returns a handler that enqueues VaultTransitUnseal objects
// that reference the given ConfigMap in their addressFrom field
func (r *VaultTransitUnsealReconciler) enqueueRequestsForConfigMap() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		cm, ok := obj.(*corev1.ConfigMap)
		if !ok {
			return nil
		}

		// List all VaultTransitUnseal resources
		vtuList := &vaultv1alpha1.VaultTransitUnsealList{}
		if err := r.List(ctx, vtuList); err != nil {
			r.Log.Error(err, "Failed to list VaultTransitUnseal resources")
			return nil
		}

		var requests []reconcile.Request
		for _, vtu := range vtuList.Items {
			// Check if this VTU references the ConfigMap
			if vtu.Spec.TransitVault.AddressFrom != nil &&
				vtu.Spec.TransitVault.AddressFrom.ConfigMapKeyRef != nil {

				ref := vtu.Spec.TransitVault.AddressFrom.ConfigMapKeyRef
				namespace := ref.Namespace
				if namespace == "" {
					namespace = vtu.Namespace
				}

				// If the ConfigMap matches, enqueue the VTU
				if cm.Name == ref.Name && cm.Namespace == namespace {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      vtu.Name,
							Namespace: vtu.Namespace,
						},
					})
				}
			}
		}

		if len(requests) > 0 {
			r.Log.V(1).Info("Enqueuing VaultTransitUnseal resources due to ConfigMap change",
				"configMap", types.NamespacedName{Name: cm.Name, Namespace: cm.Namespace},
				"count", len(requests))
		}

		return requests
	})
}

// enqueueRequestsForSecret returns a handler that enqueues VaultTransitUnseal objects
// that reference the given Secret in their addressFrom field
func (r *VaultTransitUnsealReconciler) enqueueRequestsForSecret() handler.EventHandler {
	return handler.EnqueueRequestsFromMapFunc(func(ctx context.Context, obj client.Object) []reconcile.Request {
		secret, ok := obj.(*corev1.Secret)
		if !ok {
			return nil
		}

		// List all VaultTransitUnseal resources
		vtuList := &vaultv1alpha1.VaultTransitUnsealList{}
		if err := r.List(ctx, vtuList); err != nil {
			r.Log.Error(err, "Failed to list VaultTransitUnseal resources")
			return nil
		}

		var requests []reconcile.Request
		for _, vtu := range vtuList.Items {
			// Check if this VTU references the Secret for address
			if vtu.Spec.TransitVault.AddressFrom != nil &&
				vtu.Spec.TransitVault.AddressFrom.SecretKeyRef != nil {

				ref := vtu.Spec.TransitVault.AddressFrom.SecretKeyRef
				namespace := ref.Namespace
				if namespace == "" {
					namespace = vtu.Namespace
				}

				// If the Secret matches, enqueue the VTU
				if secret.Name == ref.Name && secret.Namespace == namespace {
					requests = append(requests, reconcile.Request{
						NamespacedName: types.NamespacedName{
							Name:      vtu.Name,
							Namespace: vtu.Namespace,
						},
					})
				}
			}

			// Also check transit token secret references
			if secret.Name == vtu.Spec.TransitVault.SecretRef.Name &&
				secret.Namespace == vtu.Spec.VaultPod.Namespace {
				requests = append(requests, reconcile.Request{
					NamespacedName: types.NamespacedName{
						Name:      vtu.Name,
						Namespace: vtu.Namespace,
					},
				})
			}
		}

		if len(requests) > 0 {
			r.Log.V(1).Info("Enqueuing VaultTransitUnseal resources due to Secret change",
				"secret", types.NamespacedName{Name: secret.Name, Namespace: secret.Namespace},
				"count", len(requests))
		}

		return requests
	})
}
