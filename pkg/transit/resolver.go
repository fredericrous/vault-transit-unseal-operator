package transit

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// AddressResolver resolves transit vault address from various sources
type AddressResolver struct {
	client.Client
	log logr.Logger
}

// NewAddressResolver creates a new address resolver
func NewAddressResolver(client client.Client, log logr.Logger) *AddressResolver {
	return &AddressResolver{
		Client: client,
		log:    log,
	}
}

// ResolveAddress resolves the transit vault address from the spec
func (r *AddressResolver) ResolveAddress(ctx context.Context, spec *vaultv1alpha1.TransitVaultSpec, namespace string) (string, error) {
	// If direct address is specified, use it
	if spec.Address != "" {
		r.log.V(1).Info("Using direct transit vault address", "address", spec.Address)
		return spec.Address, nil
	}

	// If AddressFrom is specified, resolve it
	if spec.AddressFrom != nil {
		return r.resolveAddressFrom(ctx, spec.AddressFrom, namespace)
	}

	return "", fmt.Errorf("no transit vault address specified")
}

// resolveAddressFrom resolves address from ConfigMap or Secret
func (r *AddressResolver) resolveAddressFrom(ctx context.Context, ref *vaultv1alpha1.AddressReference, namespace string) (string, error) {
	// ConfigMap reference
	if ref.ConfigMapKeyRef != nil {
		return r.resolveFromConfigMap(ctx, ref.ConfigMapKeyRef, namespace)
	}

	// Secret reference
	if ref.SecretKeyRef != nil {
		return r.resolveFromSecret(ctx, ref.SecretKeyRef, namespace)
	}

	return "", fmt.Errorf("addressFrom specified but no valid reference found")
}

// resolveFromConfigMap resolves address from ConfigMap
func (r *AddressResolver) resolveFromConfigMap(ctx context.Context, ref *vaultv1alpha1.ConfigMapKeyReference, defaultNamespace string) (string, error) {
	namespace := ref.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}

	configMap := &corev1.ConfigMap{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      ref.Name,
	}, configMap); err != nil {
		return "", fmt.Errorf("getting configmap %s/%s: %w", namespace, ref.Name, err)
	}

	value, exists := configMap.Data[ref.Key]
	if !exists {
		return "", fmt.Errorf("key %s not found in configmap %s/%s", ref.Key, namespace, ref.Name)
	}

	return value, nil
}

// resolveFromSecret resolves address from Secret
func (r *AddressResolver) resolveFromSecret(ctx context.Context, ref *vaultv1alpha1.SecretKeyReference, defaultNamespace string) (string, error) {
	namespace := ref.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}

	secret := &corev1.Secret{}
	if err := r.Get(ctx, types.NamespacedName{
		Namespace: namespace,
		Name:      ref.Name,
	}, secret); err != nil {
		return "", fmt.Errorf("getting secret %s/%s: %w", namespace, ref.Name, err)
	}

	value, exists := secret.Data[ref.Key]
	if !exists {
		return "", fmt.Errorf("key %s not found in secret %s/%s", ref.Key, namespace, ref.Name)
	}

	return string(value), nil
}

// ResolveTLSConfig resolves TLS configuration from spec
func (r *AddressResolver) ResolveTLSConfig(spec *vaultv1alpha1.TransitVaultSpec) bool {
	// For now, TLS config is not in the spec
	// TODO: Add TLSConfig to TransitVaultSpec when needed
	return false
}