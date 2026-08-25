package controllers

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/event"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

func watchTestReconciler(t *testing.T, objs ...runtime.Object) *VaultTransitUnsealReconciler {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	require.NoError(t, vaultv1alpha1.AddToScheme(scheme))

	return &VaultTransitUnsealReconciler{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).Build(),
		Log:    logr.Discard(),
		Scheme: scheme,
	}
}

func vaultPod(name string, labels map[string]string, ready bool) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "vault", Labels: labels},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{Name: "vault", Ready: ready},
			},
		},
	}
}

func watchTestVTU(mode vaultv1alpha1.UnsealMode) *vaultv1alpha1.VaultTransitUnseal {
	return &vaultv1alpha1.VaultTransitUnseal{
		ObjectMeta: metav1.ObjectMeta{Name: "vault", Namespace: "vault"},
		Spec: vaultv1alpha1.VaultTransitUnsealSpec{
			Mode: mode,
			VaultPod: vaultv1alpha1.VaultPodSpec{
				Namespace: "vault",
				Selector:  map[string]string{"app.kubernetes.io/name": "vault"},
			},
			TransitVault: vaultv1alpha1.TransitVaultSpec{
				SecretRef: vaultv1alpha1.SecretReference{Name: "vault-transit-token", Key: "token"},
			},
		},
	}
}

// TestVaultPodRequests is what makes unsealing event-driven: a pod whose
// readiness flips must reach the reconciler at once rather than waiting out a
// check interval.
func TestVaultPodRequests(t *testing.T) {
	labels := map[string]string{"app.kubernetes.io/name": "vault"}
	vtu := watchTestVTU(vaultv1alpha1.UnsealModeStoredKey)
	r := watchTestReconciler(t, vtu)
	ctx := context.Background()

	t.Run("matching pod enqueues its resource", func(t *testing.T) {
		requests := r.vaultPodRequests(ctx, vaultPod("vault-0", labels, false))

		require.Len(t, requests, 1)
		assert.Equal(t, "vault", requests[0].Name)
		assert.Equal(t, "vault", requests[0].Namespace)
	})

	t.Run("pod with other labels is ignored", func(t *testing.T) {
		other := vaultPod("postgres-0", map[string]string{"app.kubernetes.io/name": "postgres"}, true)

		assert.Empty(t, r.vaultPodRequests(ctx, other))
	})

	t.Run("pod in another namespace is ignored", func(t *testing.T) {
		elsewhere := vaultPod("vault-0", labels, true)
		elsewhere.Namespace = "staging"

		assert.Empty(t, r.vaultPodRequests(ctx, elsewhere))
	})

	t.Run("non-pod objects are ignored", func(t *testing.T) {
		assert.Empty(t, r.vaultPodRequests(ctx, &corev1.Secret{}))
	})

	t.Run("transit resources are watched too", func(t *testing.T) {
		transit := watchTestVTU(vaultv1alpha1.UnsealModeTransit)
		transit.Name = "vault-transit"
		rt := watchTestReconciler(t, transit)

		assert.Len(t, rt.vaultPodRequests(ctx, vaultPod("vault-0", labels, false)), 1)
	})
}

// TestSecretRequests covers the credential each mode watches for. Seeding the
// unseal-key Secret is the last step of `bootstrap run <cluster> vault-setup`,
// and it should unseal Vault immediately rather than after a check interval.
func TestSecretRequests(t *testing.T) {
	ctx := context.Background()

	secret := func(name, namespace string) *corev1.Secret {
		return &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace}}
	}

	t.Run("stored-key resource watches its key secret", func(t *testing.T) {
		r := watchTestReconciler(t, watchTestVTU(vaultv1alpha1.UnsealModeStoredKey))

		assert.Len(t, r.secretRequests(ctx, secret("vault-unseal-keys", "vault")), 1)
	})

	t.Run("stored-key resource honours a custom secret ref", func(t *testing.T) {
		vtu := watchTestVTU(vaultv1alpha1.UnsealModeStoredKey)
		vtu.Spec.StoredKey = &vaultv1alpha1.StoredKeySpec{
			SecretRef: vaultv1alpha1.StoredKeySecretRef{Name: "root-shares", Namespace: "vault-keys"},
		}
		r := watchTestReconciler(t, vtu)

		assert.Len(t, r.secretRequests(ctx, secret("root-shares", "vault-keys")), 1)
		assert.Empty(t, r.secretRequests(ctx, secret("vault-unseal-keys", "vault")))
	})

	t.Run("transit resource ignores an unseal-key secret", func(t *testing.T) {
		r := watchTestReconciler(t, watchTestVTU(vaultv1alpha1.UnsealModeTransit))

		assert.Empty(t, r.secretRequests(ctx, secret("vault-unseal-keys", "vault")))
	})

	t.Run("transit token secret still enqueues", func(t *testing.T) {
		r := watchTestReconciler(t, watchTestVTU(vaultv1alpha1.UnsealModeTransit))

		assert.Len(t, r.secretRequests(ctx, secret("vault-transit-token", "vault")), 1)
	})

	t.Run("unrelated secret enqueues nothing", func(t *testing.T) {
		r := watchTestReconciler(t, watchTestVTU(vaultv1alpha1.UnsealModeStoredKey))

		assert.Empty(t, r.secretRequests(ctx, secret("postgres-credentials", "vault")))
	})
}

func TestVaultPodReadinessPredicate(t *testing.T) {
	labels := map[string]string{"app.kubernetes.io/name": "vault"}
	p := vaultPodReadinessPredicate()

	t.Run("creates and deletes always pass", func(t *testing.T) {
		assert.True(t, p.Create(event.CreateEvent{Object: vaultPod("vault-0", labels, true)}))
		assert.True(t, p.Delete(event.DeleteEvent{Object: vaultPod("vault-0", labels, true)}))
	})

	t.Run("readiness transition passes", func(t *testing.T) {
		// This is the sealing signal: a Vault that seals stops being ready.
		assert.True(t, p.Update(event.UpdateEvent{
			ObjectOld: vaultPod("vault-0", labels, true),
			ObjectNew: vaultPod("vault-0", labels, false),
		}))
	})

	t.Run("phase transition passes", func(t *testing.T) {
		old := vaultPod("vault-0", labels, true)
		updated := vaultPod("vault-0", labels, true)
		updated.Status.Phase = corev1.PodPending

		assert.True(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}))
	})

	t.Run("noise is filtered out", func(t *testing.T) {
		// Vault pods churn their status constantly; without this filter the
		// watch would reconcile on every probe timestamp.
		old := vaultPod("vault-0", labels, true)
		updated := vaultPod("vault-0", labels, true)
		updated.ResourceVersion = "999"
		updated.Status.PodIP = "10.0.0.7"

		assert.False(t, p.Update(event.UpdateEvent{ObjectOld: old, ObjectNew: updated}))
	})

	t.Run("non-pod objects are ignored", func(t *testing.T) {
		assert.False(t, p.Update(event.UpdateEvent{
			ObjectOld: &corev1.Secret{},
			ObjectNew: &corev1.Secret{},
		}))
		assert.False(t, p.Generic(event.GenericEvent{Object: vaultPod("vault-0", labels, true)}))
	})
}

func TestVaultContainerReady(t *testing.T) {
	labels := map[string]string{"app.kubernetes.io/name": "vault"}

	assert.True(t, vaultContainerReady(vaultPod("vault-0", labels, true)))
	assert.False(t, vaultContainerReady(vaultPod("vault-0", labels, false)))

	// A pod with no vault container at all (the injector sidecar pod) is
	// never "ready" for our purposes.
	noVault := vaultPod("vault-agent-injector", labels, true)
	noVault.Status.ContainerStatuses[0].Name = "sidecar-injector"
	assert.False(t, vaultContainerReady(noVault))
}
