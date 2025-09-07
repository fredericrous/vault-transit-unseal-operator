package main

import (
	"flag"
	"fmt"
	"os"

	"go.uber.org/zap/zapcore"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	_ "k8s.io/client-go/plugin/pkg/client/auth"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/controllers"
	"github.com/fredericrous/homelab/vault-transit-unseal-operator/pkg/config"
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

	setupLog.Info("Starting vault-operator",
		"namespace", cfg.Namespace,
		"metricsAddr", cfg.MetricsAddr,
		"probeAddr", cfg.ProbeAddr,
		"enableLeaderElection", cfg.EnableLeaderElection,
		"maxConcurrentReconciles", cfg.MaxConcurrentReconciles,
	)

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
	recorder := mgr.GetEventRecorderFor("vault-operator")

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
