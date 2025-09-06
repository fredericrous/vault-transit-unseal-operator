package controllers

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/manager"
	"sigs.k8s.io/controller-runtime/pkg/metrics/server"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
)

var _ = Describe("VaultTransitUnseal Integration Tests", func() {
	const (
		timeout  = time.Second * 30
		interval = time.Millisecond * 250
	)

	var (
		mockTransitVault *httptest.Server
		mockTargetVault  *httptest.Server
		mgr              manager.Manager
		ctx              context.Context
		cancel           context.CancelFunc
	)

	BeforeEach(func() {
		ctx, cancel = context.WithCancel(context.Background())

		// Create mock transit vault
		mockTransitVault = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/sys/health":
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"initialized":true,"sealed":false,"standby":false}`)
			case "/v1/transit/encrypt/unseal_key":
				// Mock encrypt endpoint
				if r.Method == "POST" {
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{"data":{"ciphertext":"vault:v1:encrypted_data"}}`)
				}
			case "/v1/transit/decrypt/unseal_key":
				// Mock decrypt endpoint
				if r.Method == "POST" {
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{"data":{"plaintext":"dGVzdC11bnNlYWwta2V5"}}`)
				}
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))

		// Create mock target vault
		mockTargetVault = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.URL.Path {
			case "/v1/sys/seal-status":
				w.WriteHeader(http.StatusOK)
				fmt.Fprint(w, `{"sealed":true,"t":2,"n":3,"progress":0,"version":"1.15.0"}`)
			case "/v1/sys/init":
				if r.Method == "PUT" {
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{
						"root_token":"test-root-token",
						"recovery_keys_base64":["key1","key2","key3"]
					}`)
				}
			case "/v1/sys/unseal":
				if r.Method == "PUT" {
					w.WriteHeader(http.StatusOK)
					fmt.Fprint(w, `{"sealed":false,"t":2,"n":3,"progress":1}`)
				}
			default:
				w.WriteHeader(http.StatusNotFound)
			}
		}))

		// Setup the controller manager
		var err error
		mgr, err = ctrl.NewManager(cfg, ctrl.Options{
			Scheme: k8sClient.Scheme(),
			Metrics: server.Options{
				BindAddress: "0", // Disable metrics server for tests
			},
		})
		Expect(err).ToNot(HaveOccurred())

		err = (&VaultTransitUnsealReconciler{
			Client: mgr.GetClient(),
			Scheme: mgr.GetScheme(),
			Log:    ctrl.Log.WithName("controllers").WithName("VaultTransitUnseal"),
		}).SetupWithManager(mgr)
		Expect(err).ToNot(HaveOccurred())

		// Start the manager
		go func() {
			defer GinkgoRecover()
			err := mgr.Start(ctx)
			Expect(err).ToNot(HaveOccurred())
		}()
	})

	AfterEach(func() {
		cancel()
		mockTransitVault.Close()
		mockTargetVault.Close()
	})

	Context("Transit Unseal Workflow", func() {
		It("should initialize and unseal vault using transit engine", func() {
			namespace := "test-transit-unseal"
			vaultNamespace := "vault-transit"
			resourceName := "test-vault-transit"

			By("Creating namespaces")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			vaultNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: vaultNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, vaultNs)).To(Succeed())

			By("Creating transit token secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "transit-token",
					Namespace: vaultNamespace,
				},
				Data: map[string][]byte{
					"token": []byte("test-transit-token"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("Creating a mock vault pod")
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-0",
					Namespace: vaultNamespace,
					Labels: map[string]string{
						"app.kubernetes.io/name":     "vault",
						"app.kubernetes.io/instance": "vault",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "vault",
							Image: "vault:1.15.0",
							Env: []corev1.EnvVar{
								{
									Name:  "VAULT_ADDR",
									Value: mockTargetVault.URL,
								},
							},
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodRunning,
					PodIP: "10.0.0.1",
					Conditions: []corev1.PodCondition{
						{
							Type:   corev1.PodReady,
							Status: corev1.ConditionTrue,
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())

			// Update pod status
			pod.Status.Phase = corev1.PodRunning
			pod.Status.PodIP = "10.0.0.1"
			Expect(k8sClient.Status().Update(ctx, pod)).To(Succeed())

			By("Creating VaultTransitUnseal resource")
			vtu := &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: vaultNamespace,
						Selector: map[string]string{
							"app.kubernetes.io/name": "vault",
						},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						Address: mockTransitVault.URL,
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "transit-token",
							Key:  "token",
						},
						KeyName:   "unseal_key",
						MountPath: "transit",
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						RecoveryShares:    3,
						RecoveryThreshold: 2,
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken:   "vault-admin-token",
							RecoveryKeys: "vault-recovery-keys",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "5s",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vtu)).To(Succeed())

			By("Waiting for VaultTransitUnseal to be processed")
			Eventually(func() bool {
				var updated vaultv1alpha1.VaultTransitUnseal
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, &updated)
				if err != nil {
					return false
				}
				// Check if status has been updated
				return len(updated.Status.Conditions) > 0
			}, timeout, interval).Should(BeTrue())

			By("Verifying admin token secret was created")
			Eventually(func() error {
				adminTokenSecret := &corev1.Secret{}
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "vault-admin-token",
					Namespace: vaultNamespace,
				}, adminTokenSecret)
			}, timeout, interval).Should(Succeed())

			By("Verifying recovery keys secret was created")
			Eventually(func() error {
				recoveryKeysSecret := &corev1.Secret{}
				return k8sClient.Get(ctx, types.NamespacedName{
					Name:      "vault-recovery-keys",
					Namespace: vaultNamespace,
				}, recoveryKeysSecret)
			}, timeout, interval).Should(Succeed())

			By("Checking status conditions")
			var vtuStatus vaultv1alpha1.VaultTransitUnseal
			Eventually(func() string {
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      resourceName,
					Namespace: namespace,
				}, &vtuStatus)
				if err != nil {
					return ""
				}
				if len(vtuStatus.Status.Conditions) > 0 {
					return string(vtuStatus.Status.Conditions[0].Type)
				}
				return ""
			}, timeout, interval).Should(Not(BeEmpty()))

			// Cleanup
			Expect(k8sClient.Delete(ctx, vtu)).To(Succeed())
			Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			Expect(k8sClient.Delete(ctx, vaultNs)).To(Succeed())
		})

		It("should handle multiple vault pods in statefulset", func() {
			namespace := "test-multi-vault"
			vaultNamespace := "vault-multi"

			By("Creating namespaces")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			vaultNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: vaultNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, vaultNs)).To(Succeed())

			By("Creating transit token secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "transit-token",
					Namespace: vaultNamespace,
				},
				Data: map[string][]byte{
					"token": []byte("test-transit-token"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("Creating multiple vault pods")
			for i := 0; i < 3; i++ {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("vault-%d", i),
						Namespace: vaultNamespace,
						Labels: map[string]string{
							"app.kubernetes.io/name":             "vault",
							"statefulset.kubernetes.io/pod-name": fmt.Sprintf("vault-%d", i),
						},
					},
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "vault",
								Image: "vault:1.15.0",
							},
						},
					},
					Status: corev1.PodStatus{
						Phase: corev1.PodRunning,
						PodIP: fmt.Sprintf("10.0.0.%d", i+1),
					},
				}
				Expect(k8sClient.Create(ctx, pod)).To(Succeed())
			}

			By("Creating VaultTransitUnseal resource")
			vtu := &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-multi-vault",
					Namespace: namespace,
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: vaultNamespace,
						Selector: map[string]string{
							"app.kubernetes.io/name": "vault",
						},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						Address: mockTransitVault.URL,
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "transit-token",
							Key:  "token",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "10s",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vtu)).To(Succeed())

			By("Waiting for processing")
			time.Sleep(2 * time.Second)

			// Cleanup
			Expect(k8sClient.Delete(ctx, vtu)).To(Succeed())
			for i := 0; i < 3; i++ {
				pod := &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      fmt.Sprintf("vault-%d", i),
						Namespace: vaultNamespace,
					},
				}
				Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
			}
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			Expect(k8sClient.Delete(ctx, vaultNs)).To(Succeed())
		})

		It("should handle transit vault connection failures", func() {
			namespace := "test-transit-failure"
			vaultNamespace := "vault-failure"

			By("Creating namespaces")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			vaultNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: vaultNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, vaultNs)).To(Succeed())

			By("Creating transit token secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "transit-token",
					Namespace: vaultNamespace,
				},
				Data: map[string][]byte{
					"token": []byte("test-transit-token"),
				},
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())

			By("Creating VaultTransitUnseal with invalid transit vault address")
			vtu := &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-transit-failure",
					Namespace: namespace,
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: vaultNamespace,
						Selector: map[string]string{
							"app": "vault",
						},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						Address: "http://invalid-transit-vault:8200",
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "transit-token",
							Key:  "token",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "30s",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vtu)).To(Succeed())

			By("Verifying error condition is set")
			Eventually(func() bool {
				var updated vaultv1alpha1.VaultTransitUnseal
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "test-transit-failure",
					Namespace: namespace,
				}, &updated)
				if err != nil {
					return false
				}
				// Check for error condition
				for _, cond := range updated.Status.Conditions {
					if cond.Status == "False" {
						return true
					}
				}
				return false
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, vtu)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			Expect(k8sClient.Delete(ctx, vaultNs)).To(Succeed())
		})
	})

	Context("Secret Management", func() {
		It("should not overwrite existing secrets", func() {
			namespace := "test-existing-secrets"
			vaultNamespace := "vault-secrets"

			By("Creating namespaces")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			vaultNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: vaultNamespace,
				},
			}
			Expect(k8sClient.Create(ctx, vaultNs)).To(Succeed())

			By("Creating existing admin token secret")
			existingSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-admin-token",
					Namespace: vaultNamespace,
				},
				Data: map[string][]byte{
					"token": []byte("existing-token"),
				},
			}
			Expect(k8sClient.Create(ctx, existingSecret)).To(Succeed())

			By("Creating transit token secret")
			transitSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "transit-token",
					Namespace: vaultNamespace,
				},
				Data: map[string][]byte{
					"token": []byte("test-transit-token"),
				},
			}
			Expect(k8sClient.Create(ctx, transitSecret)).To(Succeed())

			By("Creating vault pod")
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-0",
					Namespace: vaultNamespace,
					Labels: map[string]string{
						"app": "vault",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "vault",
							Image: "vault:1.15.0",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())

			By("Creating VaultTransitUnseal resource")
			vtu := &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "test-existing-secrets",
					Namespace: namespace,
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: vaultNamespace,
						Selector: map[string]string{
							"app": "vault",
						},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						Address: mockTransitVault.URL,
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "transit-token",
							Key:  "token",
						},
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken: "vault-admin-token",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "30s",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vtu)).To(Succeed())

			By("Waiting for reconciliation")
			time.Sleep(2 * time.Second)

			By("Verifying existing secret was not modified")
			secret := &corev1.Secret{}
			err := k8sClient.Get(ctx, types.NamespacedName{
				Name:      "vault-admin-token",
				Namespace: vaultNamespace,
			}, secret)
			Expect(err).NotTo(HaveOccurred())
			Expect(string(secret.Data["token"])).To(Equal("existing-token"))

			// Cleanup
			Expect(k8sClient.Delete(ctx, vtu)).To(Succeed())
			Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
			Expect(k8sClient.Delete(ctx, existingSecret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, transitSecret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			Expect(k8sClient.Delete(ctx, vaultNs)).To(Succeed())
		})
	})

	Context("Homelab Architecture Validation", func() {
		It("should support dual vault setup with transit token", func() {
			// This test validates the specific homelab architecture requirement
			// of having 2 vaults with transit token authentication

			namespace := "test-homelab"
			primaryVaultNs := "vault-primary"
			secondaryVaultNs := "vault-secondary"

			By("Creating namespaces for dual vault setup")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			Expect(k8sClient.Create(ctx, ns)).To(Succeed())

			primaryNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: primaryVaultNs,
				},
			}
			Expect(k8sClient.Create(ctx, primaryNs)).To(Succeed())

			secondaryNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: secondaryVaultNs,
				},
			}
			Expect(k8sClient.Create(ctx, secondaryNs)).To(Succeed())

			By("Creating transit token for QNAP vault (transit provider)")
			transitSecret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "qnap-vault-transit-token",
					Namespace: primaryVaultNs,
				},
				Data: map[string][]byte{
					"token": []byte("mock-qnap-vault-transit-token"),
				},
			}
			Expect(k8sClient.Create(ctx, transitSecret)).To(Succeed())

			By("Creating primary Kubernetes vault pod")
			primaryPod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-0",
					Namespace: primaryVaultNs,
					Labels: map[string]string{
						"app.kubernetes.io/name":     "vault",
						"app.kubernetes.io/instance": "vault-k8s",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "vault",
							Image: "vault:1.15.0",
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, primaryPod)).To(Succeed())

			By("Creating VaultTransitUnseal for K8s vault using QNAP transit")
			vtu := &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "k8s-vault-transit-unseal",
					Namespace: namespace,
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: primaryVaultNs,
						Selector: map[string]string{
							"app.kubernetes.io/instance": "vault-k8s",
						},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						Address: mockTransitVault.URL, // Simulating QNAP vault
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "qnap-vault-transit-token",
							Key:  "token",
						},
						KeyName:   "k8s-vault-unseal",
						MountPath: "transit",
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						RecoveryShares:    5,
						RecoveryThreshold: 3,
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken:   "vault-admin-token",
							RecoveryKeys: "vault-recovery-keys",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "30s",
					},
				},
			}
			Expect(k8sClient.Create(ctx, vtu)).To(Succeed())

			By("Verifying the setup supports homelab requirements")
			Eventually(func() bool {
				var updated vaultv1alpha1.VaultTransitUnseal
				err := k8sClient.Get(ctx, types.NamespacedName{
					Name:      "k8s-vault-transit-unseal",
					Namespace: namespace,
				}, &updated)
				if err != nil {
					return false
				}
				// Verify transit vault configuration
				return updated.Spec.TransitVault.KeyName == "k8s-vault-unseal" &&
					updated.Spec.TransitVault.MountPath == "transit"
			}, timeout, interval).Should(BeTrue())

			// Cleanup
			Expect(k8sClient.Delete(ctx, vtu)).To(Succeed())
			Expect(k8sClient.Delete(ctx, primaryPod)).To(Succeed())
			Expect(k8sClient.Delete(ctx, transitSecret)).To(Succeed())
			Expect(k8sClient.Delete(ctx, ns)).To(Succeed())
			Expect(k8sClient.Delete(ctx, primaryNs)).To(Succeed())
			Expect(k8sClient.Delete(ctx, secondaryNs)).To(Succeed())
		})
	})
})
