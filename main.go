package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"strings"

	"go.uber.org/zap/zapcore"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/controllers"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/config"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/crd"
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	utilruntime.Must(vaultv1alpha1.AddToScheme(scheme))
}

func main() {
	// Parse flags (these can override environment variables)
	var configFile string
	flag.StringVar(&configFile, "config", "", "Path to configuration file")
	flag.Parse()

	// Setup logging with production settings
	opts := zap.Options{
		Development: false,
		Level:       zapcore.InfoLevel,
		Encoder: zapcore.NewJSONEncoder(zapcore.EncoderConfig{
			TimeKey:        "timestamp",
			LevelKey:       "level",
			NameKey:        "logger",
			CallerKey:      "caller",
			MessageKey:     "msg",
			StacktraceKey:  "stacktrace",
			LineEnding:     zapcore.DefaultLineEnding,
			EncodeLevel:    zapcore.LowercaseLevelEncoder,
			EncodeTime:     zapcore.ISO8601TimeEncoder,
			EncodeDuration: zapcore.SecondsDurationEncoder,
			EncodeCaller:   zapcore.ShortCallerEncoder,
		}),
	}

	// Allow debug mode via environment
	if os.Getenv("DEBUG") == "true" {
		opts.Development = true
		opts.Level = zapcore.DebugLevel
	}

	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))

	// Load configuration
	cfg, err := config.LoadConfig()
	if err != nil {
		setupLog.Error(err, "Failed to load configuration")
		os.Exit(1)
	}

	setupLog.Info("Starting vault-transit-unseal-operator",
		"namespace", cfg.Namespace,
		"metricsAddr", cfg.MetricsAddr,
		"probeAddr", cfg.ProbeAddr,
		"enableLeaderElection", cfg.EnableLeaderElection,
		"maxConcurrentReconciles", cfg.MaxConcurrentReconciles,
	)

	// Install/Update CRDs if not skipped
	skipCRDInstall := cfg.SkipCRDInstall

	// Check if we should skip CRD install based on environment detection
	if !skipCRDInstall && isRunningInArgoCD() {
		setupLog.Info("Detected ArgoCD environment, skipping CRD installation")
		skipCRDInstall = true
	}

	if !skipCRDInstall {
		setupLog.Info("Installing CRDs")
		restConfig := ctrl.GetConfigOrDie()
		if err := crd.InstallCRDs(context.Background(), restConfig); err != nil {
			// Check if it's a permission error
			if strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "cannot get resource") {
				setupLog.Info("CRD installation skipped due to permissions. Assuming CRDs are managed externally.")
			} else {
				setupLog.Error(err, "Failed to install CRDs")
				os.Exit(1)
			}
		}
	} else {
		setupLog.Info("CRD installation skipped")
	}

	// Create manager
	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		HealthProbeBindAddress: cfg.ProbeAddr,
		LeaderElection:         cfg.EnableLeaderElection,
		LeaderElectionID:       cfg.LeaderElectionID,
	})
	if err != nil {
		setupLog.Error(err, "Failed to create manager")
		os.Exit(1)
	}

	// Create the recorder for events
	recorder := mgr.GetEventRecorderFor("vault-transit-unseal-operator")

	// Setup controller with new architecture
	if err = setupController(mgr, cfg, recorder); err != nil {
		setupLog.Error(err, "Failed to setup controller")
		os.Exit(1)
	}

	// Start the manager
	setupLog.Info("Starting manager")
	ctx := ctrl.SetupSignalHandler()
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "Failed to run manager")
		os.Exit(1)
	}
}

func setupController(mgr manager.Manager, cfg *config.OperatorConfig, recorder record.EventRecorder) error {
	reconciler := &controllers.VaultTransitUnsealReconciler{
		Client:   mgr.GetClient(),
		Log:      ctrl.Log.WithName("controllers").WithName("VaultTransitUnseal"),
		Scheme:   mgr.GetScheme(),
		Recorder: recorder,
		Config:   cfg,
	}

	if err := reconciler.SetupWithManager(mgr); err != nil {
		return fmt.Errorf("failed to setup reconciler: %w", err)
	}

	// Register health checks
	if reconciler.HealthChecker != nil {
		if err := controllers.RegisterHealthChecks(mgr, reconciler.HealthChecker); err != nil {
			return fmt.Errorf("failed to register health checks: %w", err)
		}
	}

	// Add metrics endpoint
	if cfg.EnableMetrics {
		setupLog.Info("Metrics enabled", "addr", cfg.MetricsAddr)
	}

	return nil
}

// isRunningInArgoCD checks if the operator is running in an ArgoCD-managed environment
func isRunningInArgoCD() bool {
	ctx := context.Background()

	// First, check environment variables that ArgoCD might set
	// These are not standard but could be set by users
	if os.Getenv("ARGOCD_MANAGED") == "true" {
		return true
	}

	// Try to detect if we're running in a pod with ArgoCD labels
	namespace := os.Getenv("POD_NAMESPACE")
	podName := os.Getenv("POD_NAME")

	if namespace == "" || podName == "" {
		// Try to read from downward API files
		if nsBytes, err := os.ReadFile("/var/run/secrets/kubernetes.io/serviceaccount/namespace"); err == nil {
			namespace = strings.TrimSpace(string(nsBytes))
		}
		// For pod name, we'd need downward API volume mount
		if podName == "" {
			podName = os.Getenv("HOSTNAME") // Often the pod name
		}
	}

	// If we still don't have namespace/pod info, we can't check labels
	if namespace == "" || podName == "" {
		return false
	}

	// Create a minimal k8s client to check our own pod
	config, err := rest.InClusterConfig()
	if err != nil {
		setupLog.V(1).Info("Failed to get in-cluster config for ArgoCD detection", "error", err)
		return false
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		setupLog.V(1).Info("Failed to create k8s client for ArgoCD detection", "error", err)
		return false
	}

	// Check our own pod for ArgoCD labels
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, podName, metav1.GetOptions{})
	if err != nil {
		setupLog.V(1).Info("Failed to get pod for ArgoCD detection", "error", err)
		return false
	}

	// Check for common ArgoCD labels
	labels := pod.GetLabels()
	annotations := pod.GetAnnotations()

	// Standard ArgoCD managed resource labels
	if _, hasAppInstance := labels["app.kubernetes.io/instance"]; hasAppInstance {
		if _, hasArgocdApp := labels["argocd.argoproj.io/instance"]; hasArgocdApp {
			return true
		}
	}

	// Check annotations
	if _, hasManaged := annotations["argocd.argoproj.io/sync-options"]; hasManaged {
		return true
	}

	// Check if the namespace has ArgoCD application
	if apps, err := clientset.AppsV1().Deployments(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: "app.kubernetes.io/managed-by=argocd",
	}); err == nil && len(apps.Items) > 0 {
		return true
	}

	// Check if ArgoCD is installed in the cluster
	if _, err := clientset.CoreV1().Namespaces().Get(ctx, "argocd", metav1.GetOptions{}); err == nil {
		// ArgoCD namespace exists, check if we have any ArgoCD applications managing our namespace
		// This would require ArgoCD Application API access, which we might not have
		// For now, just note that ArgoCD is present
		setupLog.V(1).Info("ArgoCD namespace detected in cluster")
	}

	return false
}
