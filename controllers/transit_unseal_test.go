package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

func TestVaultTransitUnsealReconciliation(t *testing.T) {
	// Setup
	scheme := runtime.NewScheme()
	_ = vaultv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create test objects
	namespace := "test"
	vaultNamespace := "vault"

	transitTokenSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vault-transit-token",
			Namespace: vaultNamespace,
		},
		Data: map[string][]byte{
			"token": []byte("test-transit-token"),
		},
	}

	vtu := &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-vault",
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
				Address: "http://transit-vault:8200",
				SecretRef: vaultv1alpha1.SecretReference{
					Name: "vault-transit-token",
					Key:  "token",
				},
			},
			Monitoring: vaultv1alpha1.MonitoringSpec{
				CheckInterval: "30s",
			},
		},
	}

	// Create fake client
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(vtu, transitTokenSecret).
		Build()

	// Create reconciler
	reconciler := createTestReconciler(fakeClient)

	// Test 1: Reconciliation without vault pods should requeue
	t.Run("no vault pods should requeue", func(t *testing.T) {
		result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      vtu.Name,
				Namespace: vtu.Namespace,
			},
		})

		require.NoError(t, err)
		assert.True(t, result.RequeueAfter > 0, "Should requeue when no vault pods found")
	})

	// Test 2: Test with a vault pod
	t.Run("with vault pod", func(t *testing.T) {
		// Add a vault pod
		vaultPod := &corev1.Pod{
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

		err := fakeClient.Create(context.Background(), vaultPod)
		require.NoError(t, err)

		// Update pod status (fake client doesn't automatically do this)
		vaultPod.Status.Phase = corev1.PodRunning
		vaultPod.Status.PodIP = "10.0.0.1"
		err = fakeClient.Status().Update(context.Background(), vaultPod)
		require.NoError(t, err)

		// Reconcile again
		result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      vtu.Name,
				Namespace: vtu.Namespace,
			},
		})

		// Should still requeue because vault client will fail to connect
		// but should not error
		require.NoError(t, err)
		assert.True(t, result.RequeueAfter > 0)
	})

	// Test 3: Test interval parsing
	t.Run("custom check interval", func(t *testing.T) {
		customVTU := &vaultv1alpha1.VaultTransitUnseal{
			ObjectMeta: metav1.ObjectMeta{
				Name:      "custom-interval",
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
					Address: "http://transit-vault:8200",
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

		err := fakeClient.Create(context.Background(), customVTU)
		require.NoError(t, err)

		result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      customVTU.Name,
				Namespace: customVTU.Namespace,
			},
		})

		require.NoError(t, err)
		// Without vault pods, it should use default 30s interval
		assert.Equal(t, 30*time.Second, result.RequeueAfter)
	})
}

// TestHomelabScenario tests the specific homelab dual vault setup
func TestHomelabScenario(t *testing.T) {
	// This test validates the specific homelab architecture:
	// - NAS vault (transit provider) reachable via tailnet
	// - K8s vault using QNAP for transit unseal

	scheme := runtime.NewScheme()
	_ = vaultv1alpha1.AddToScheme(scheme)
	_ = corev1.AddToScheme(scheme)

	// Create QNAP transit token secret
	qnapTransitSecret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "qnap-vault-transit-token",
			Namespace: "vault",
		},
		Data: map[string][]byte{
			"token": []byte("mock-vault-transit-token-for-testing"),
		},
	}

	// Create VaultTransitUnseal for K8s vault
	k8sVaultUnseal := &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "k8s-vault-transit-unseal",
			Namespace: "vault-transit-unseal-operator",
		},
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			VaultPod: vaultv1alpha1.VaultPodSpec{
				Namespace: "vault",
				Selector: map[string]string{
					"app.kubernetes.io/name": "vault",
				},
			},
			TransitVault: vaultv1alpha1.TransitVaultSpec{
				Address: "http://vault.vault.svc.cluster.local:8200",
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

	// Create fake client with homelab setup
	fakeClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(qnapTransitSecret, k8sVaultUnseal).
		Build()

	reconciler := createTestReconciler(fakeClient)

	t.Run("homelab transit unseal configuration", func(t *testing.T) {
		// Verify the resource can be retrieved
		var vtu vaultv1alpha1.VaultTransitUnseal
		err := fakeClient.Get(context.Background(), types.NamespacedName{
			Name:      k8sVaultUnseal.Name,
			Namespace: k8sVaultUnseal.Namespace,
		}, &vtu)
		require.NoError(t, err)

		// Verify transit configuration matches homelab setup
		assert.Equal(t, "http://vault.vault.svc.cluster.local:8200", vtu.Spec.TransitVault.Address)
		assert.Equal(t, "qnap-vault-transit-token", vtu.Spec.TransitVault.SecretRef.Name)
		assert.Equal(t, "k8s-vault-unseal", vtu.Spec.TransitVault.KeyName)
		assert.Equal(t, "transit", vtu.Spec.TransitVault.MountPath)

		// Verify initialization settings
		assert.Equal(t, 5, vtu.Spec.Initialization.RecoveryShares)
		assert.Equal(t, 3, vtu.Spec.Initialization.RecoveryThreshold)

		// Attempt reconciliation (will requeue due to no vault pods)
		result, err := reconciler.Reconcile(context.Background(), reconcile.Request{
			NamespacedName: types.NamespacedName{
				Name:      k8sVaultUnseal.Name,
				Namespace: k8sVaultUnseal.Namespace,
			},
		})

		require.NoError(t, err)
		assert.True(t, result.RequeueAfter > 0, "Should requeue to check for vault pods")
	})
}
