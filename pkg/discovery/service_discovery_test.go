package discovery

import (
	"context"
	"testing"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

func pod(name string) *corev1.Pod {
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: name}}
}

func TestGetVaultServiceEndpoint(t *testing.T) {
	d := &ServiceDiscovery{Log: logr.Discard()}
	ctx := context.Background()

	t.Run("per-pod DNS when HeadlessServiceName is set", func(t *testing.T) {
		spec := &vaultv1alpha1.VaultPodSpec{
			Namespace:           "vault",
			HeadlessServiceName: "vault-vault-internal",
			ServicePort:         8300,
		}
		got, err := d.GetVaultServiceEndpoint(ctx, spec, pod("vault-vault-2"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "http://vault-vault-2.vault-vault-internal.vault.svc.cluster.local:8300"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("per-pod address is pod-specific (not load-balanced)", func(t *testing.T) {
		spec := &vaultv1alpha1.VaultPodSpec{
			Namespace:           "vault",
			HeadlessServiceName: "vault-vault-internal",
			ServicePort:         8300,
		}
		a, _ := d.GetVaultServiceEndpoint(ctx, spec, pod("vault-vault-0"))
		b, _ := d.GetVaultServiceEndpoint(ctx, spec, pod("vault-vault-1"))
		if a == b {
			t.Fatalf("expected distinct per-pod addresses, both were %q", a)
		}
	})

	t.Run("defaults ServicePort to 8300", func(t *testing.T) {
		spec := &vaultv1alpha1.VaultPodSpec{
			Namespace:           "vault",
			HeadlessServiceName: "vault-vault-internal",
		}
		got, _ := d.GetVaultServiceEndpoint(ctx, spec, pod("vault-vault-0"))
		want := "http://vault-vault-0.vault-vault-internal.vault.svc.cluster.local:8300"
		if got != want {
			t.Fatalf("got %q, want %q", got, want)
		}
	})

	t.Run("VaultAddress override wins over per-pod", func(t *testing.T) {
		spec := &vaultv1alpha1.VaultPodSpec{
			Namespace:           "vault",
			HeadlessServiceName: "vault-vault-internal",
			VaultAddress:        "https://vault.example.com",
		}
		got, _ := d.GetVaultServiceEndpoint(ctx, spec, pod("vault-vault-0"))
		if got != "https://vault.example.com" {
			t.Fatalf("got %q, want override", got)
		}
	})

	t.Run("falls back to shared service when HeadlessServiceName unset", func(t *testing.T) {
		spec := &vaultv1alpha1.VaultPodSpec{
			Namespace:   "vault",
			ServiceName: "vault-http",
			ServicePort: 8200,
		}
		got, err := d.GetVaultServiceEndpoint(ctx, spec, pod("vault-vault-0"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		want := "http://vault-http.vault.svc.cluster.local:8200"
		if got != want {
			t.Fatalf("got %q, want %q (legacy shared-service path)", got, want)
		}
	})
}
