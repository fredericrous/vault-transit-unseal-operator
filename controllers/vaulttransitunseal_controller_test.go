package controllers

import (
	"context"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
)

var _ = Describe("VaultTransitUnseal Controller", func() {
	Context("When reconciling a resource", func() {
		const (
			resourceName = "test-resource"
			namespace    = "default"
			timeout      = time.Second * 10
			interval     = time.Millisecond * 250
		)

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: namespace,
		}

		BeforeEach(func() {
			By("creating the namespace")
			ns := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: namespace,
				},
			}
			err := k8sClient.Create(ctx, ns)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			vaultNs := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vault",
				},
			}
			err = k8sClient.Create(ctx, vaultNs)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			By("creating the transit token secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-transit-token",
					Namespace: "vault",
				},
				Data: map[string][]byte{
					"token": []byte("test-token"),
				},
			}
			err = k8sClient.Create(ctx, secret)
			if err != nil && !errors.IsAlreadyExists(err) {
				Expect(err).NotTo(HaveOccurred())
			}

			By("creating the custom resource for the Kind VaultTransitUnseal")
			resource := &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      resourceName,
					Namespace: namespace,
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
						Selector: map[string]string{
							"app.kubernetes.io/name": "vault",
						},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						Address: "http://test-transit-vault:8200",
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "vault-transit-token",
							Key:  "token",
						},
					},
					Initialization: vaultv1alpha1.InitializationSpec{
						RecoveryShares:    3,
						RecoveryThreshold: 2,
						SecretNames: vaultv1alpha1.SecretNamesSpec{
							AdminToken:   "vault-admin-token",
							RecoveryKeys: "vault-keys",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "30s",
					},
				},
			}
			Expect(k8sClient.Create(ctx, resource)).To(Succeed())
		})

		AfterEach(func() {
			resource := &vaultv1alpha1.VaultTransitUnseal{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			if err == nil {
				By("Cleanup the specific resource instance VaultTransitUnseal")
				Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
			}
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := createTestReconciler(k8sClient)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should handle missing resources gracefully", func() {
			By("Reconciling a non-existent resource")
			controllerReconciler := createTestReconciler(k8sClient)

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "non-existent",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
		})

		It("should update status conditions", func() {
			Skip("Skipping test that requires mocked vault client")

			By("Getting the created resource")
			vtu := &vaultv1alpha1.VaultTransitUnseal{}
			Eventually(func() error {
				return k8sClient.Get(ctx, typeNamespacedName, vtu)
			}, timeout, interval).Should(Succeed())

			By("Checking that conditions are set")
			Eventually(func() int {
				err := k8sClient.Get(ctx, typeNamespacedName, vtu)
				if err != nil {
					return 0
				}
				return len(vtu.Status.Conditions)
			}, timeout, interval).Should(BeNumerically(">", 0))
		})

		It("should requeue when no pods are found", func() {
			By("Reconciling without vault pods")
			controllerReconciler := createTestReconciler(k8sClient)

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))
		})

		It("should handle vault pod creation", func() {
			By("Creating a vault pod")
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-0",
					Namespace: "vault",
					Labels: map[string]string{
						"app.kubernetes.io/name": "vault",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name:  "vault",
							Image: "vault:1.15.0",
							Ports: []corev1.ContainerPort{
								{
									Name:          "http",
									ContainerPort: 8200,
								},
							},
						},
					},
				},
			}
			Expect(k8sClient.Create(ctx, pod)).To(Succeed())

			By("Reconciling after pod creation")
			controllerReconciler := createTestReconciler(k8sClient)

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(BeNumerically(">", 0))

			By("Cleaning up the pod")
			Expect(k8sClient.Delete(ctx, pod)).To(Succeed())
		})

		It("should handle invalid transit token", func() {
			By("Deleting the transit token secret")
			secret := &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-transit-token",
					Namespace: "vault",
				},
			}
			Expect(k8sClient.Delete(ctx, secret)).To(Succeed())

			By("Reconciling without transit token")
			controllerReconciler := createTestReconciler(k8sClient)

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).To(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(time.Duration(0)))

			By("Recreating the transit token secret")
			secret.Data = map[string][]byte{
				"token": []byte("test-token"),
			}
			Expect(k8sClient.Create(ctx, secret)).To(Succeed())
		})

		It("should parse monitoring interval correctly", func() {
			By("Creating a resource with custom check interval")
			customResource := &vaultv1alpha1.VaultTransitUnseal{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "custom-interval",
					Namespace: namespace,
				},
				Spec: vaultv1alpha1.VaultTransitUnsealSpec{
					VaultPod: vaultv1alpha1.VaultPodSpec{
						Namespace: "vault",
						Selector: map[string]string{
							"app": "vault",
						},
					},
					TransitVault: vaultv1alpha1.TransitVaultSpec{
						Address: "http://test-transit-vault:8200",
						SecretRef: vaultv1alpha1.SecretReference{
							Name: "vault-transit-token",
							Key:  "token",
						},
					},
					Monitoring: vaultv1alpha1.MonitoringSpec{
						CheckInterval: "2m30s",
					},
				},
			}
			Expect(k8sClient.Create(ctx, customResource)).To(Succeed())

			By("Reconciling the custom resource")
			controllerReconciler := createTestReconciler(k8sClient)

			result, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name:      "custom-interval",
					Namespace: namespace,
				},
			})
			Expect(err).NotTo(HaveOccurred())
			Expect(result.RequeueAfter).To(Equal(2*time.Minute + 30*time.Second))

			By("Cleaning up custom resource")
			Expect(k8sClient.Delete(ctx, customResource)).To(Succeed())
		})
	})

	Context("Controller Setup", func() {
		It("should setup with manager", func() {
			Skip("Requires full manager setup")
			// This would test SetupWithManager in a real integration test
		})
	})
})
