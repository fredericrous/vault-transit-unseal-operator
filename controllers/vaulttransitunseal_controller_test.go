package controllers

import (
	"context"
	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-operator/api/v1alpha1"
)

var _ = Describe("VaultTransitUnseal Controller", func() {
	Context("When reconciling a resource", func() {
		const resourceName = "test-resource"
		const namespace = "vault"

		ctx := context.Background()

		typeNamespacedName := types.NamespacedName{
			Name:      resourceName,
			Namespace: namespace,
		}
		vaulttransitunseal := &vaultv1alpha1.VaultTransitUnseal{}

		BeforeEach(func() {
			By("creating the namespace")
			namespace := &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					Name: "vault",
				},
			}
			err := k8sClient.Create(ctx, namespace)
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
			err = k8sClient.Get(ctx, typeNamespacedName, vaulttransitunseal)
			if err != nil && errors.IsNotFound(err) {
				resource := &vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      resourceName,
						Namespace: "vault",
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
					},
				}
				Expect(k8sClient.Create(ctx, resource)).To(Succeed())
			}
		})

		AfterEach(func() {
			// TODO(user): Cleanup logic after each test, like removing the resource instance.
			resource := &vaultv1alpha1.VaultTransitUnseal{}
			err := k8sClient.Get(ctx, typeNamespacedName, resource)
			Expect(err).NotTo(HaveOccurred())

			By("Cleanup the specific resource instance VaultTransitUnseal")
			Expect(k8sClient.Delete(ctx, resource)).To(Succeed())
		})

		It("should successfully reconcile the resource", func() {
			By("Reconciling the created resource")
			controllerReconciler := &VaultTransitUnsealReconciler{
				Client: k8sClient,
				Scheme: k8sClient.Scheme(),
				Log:    ctrl.Log.WithName("controllers").WithName("VaultTransitUnseal"),
			}

			_, err := controllerReconciler.Reconcile(ctx, reconcile.Request{
				NamespacedName: typeNamespacedName,
			})
			Expect(err).NotTo(HaveOccurred())
			// TODO(user): Add more specific assertions depending on your controller's reconciliation logic.
			// Example: If you expect a certain status condition after reconciliation, verify it here.
		})
	})
})
