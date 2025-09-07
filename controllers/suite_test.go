package controllers

import (
	"context"
	"path/filepath"
	"testing"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment

// Integration test variables (defined in vaulttransitunseal_integration_test.go)
var integrationMgr manager.Manager
var integrationCtx context.Context
var integrationCancel context.CancelFunc
var managerStarted bool
var controllerSetup bool

func TestControllers(t *testing.T) {
	RegisterFailHandler(Fail)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: false,
	}

	var err error
	// cfg is defined in this file globally.
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = vaultv1alpha1.AddToScheme(scheme.Scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme.Scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	// Setup integration test manager and controller
	integrationCtx, integrationCancel = context.WithCancel(context.Background())

	integrationMgr, err = ctrl.NewManager(cfg, ctrl.Options{
		Scheme: scheme.Scheme,
		Metrics: server.Options{
			BindAddress: "0", // Disable metrics server for tests
		},
	})
	Expect(err).NotTo(HaveOccurred())

	// Start the manager in the background
	go func() {
		defer GinkgoRecover()
		err := integrationMgr.Start(integrationCtx)
		if err != nil {
			panic(err)
		}
	}()
	managerStarted = true
})

var _ = AfterSuite(func() {
	By("stopping the integration manager")
	if integrationCancel != nil {
		integrationCancel()
	}

	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})
