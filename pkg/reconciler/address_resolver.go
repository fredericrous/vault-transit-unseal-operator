package reconciler

import (
	"context"
	"fmt"
	"strings"

	"github.com/go-logr/logr"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// ResolveTransitVaultAddress resolves the transit vault address from the spec
func ResolveTransitVaultAddress(ctx context.Context, c client.Client, log logr.Logger, vtu *vaultv1alpha1.VaultTransitUnseal) (string, error) {
	// Direct address takes precedence
	if vtu.Spec.TransitVault.Address != "" {
		log.V(1).Info("Using direct transit vault address", "address", vtu.Spec.TransitVault.Address)
		return vtu.Spec.TransitVault.Address, nil
	}

	// Check if addressFrom is specified
	if vtu.Spec.TransitVault.AddressFrom == nil {
		return "", fmt.Errorf("transit vault address not specified: either 'address' or 'addressFrom' must be set")
	}

	// Resolve from ConfigMap
	if vtu.Spec.TransitVault.AddressFrom.ConfigMapKeyRef != nil {
		address, err := resolveFromConfigMap(ctx, c, log, vtu.Namespace, vtu.Spec.TransitVault.AddressFrom.ConfigMapKeyRef, vtu.Spec.TransitVault.AddressFrom.Default)
		if err != nil {
			return "", fmt.Errorf("failed to resolve address from ConfigMap: %w", err)
		}
		log.Info("Resolved transit vault address from ConfigMap",
			"configMap", vtu.Spec.TransitVault.AddressFrom.ConfigMapKeyRef.Name,
			"key", vtu.Spec.TransitVault.AddressFrom.ConfigMapKeyRef.Key,
			"address", address)
		return address, nil
	}

	// Resolve from Secret
	if vtu.Spec.TransitVault.AddressFrom.SecretKeyRef != nil {
		address, err := resolveFromSecret(ctx, c, log, vtu.Namespace, vtu.Spec.TransitVault.AddressFrom.SecretKeyRef, vtu.Spec.TransitVault.AddressFrom.Default)
		if err != nil {
			return "", fmt.Errorf("failed to resolve address from Secret: %w", err)
		}
		log.Info("Resolved transit vault address from Secret",
			"secret", vtu.Spec.TransitVault.AddressFrom.SecretKeyRef.Name,
			"key", vtu.Spec.TransitVault.AddressFrom.SecretKeyRef.Key)
		return address, nil
	}

	return "", fmt.Errorf("addressFrom specified but neither configMapKeyRef nor secretKeyRef is set")
}

// resolveFromConfigMap resolves a value from a ConfigMap
func resolveFromConfigMap(ctx context.Context, c client.Client, log logr.Logger, defaultNamespace string, ref *vaultv1alpha1.ConfigMapKeyReference, defaultValue string) (string, error) {
	namespace := ref.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}

	configMap := &corev1.ConfigMap{}
	key := types.NamespacedName{
		Name:      ref.Name,
		Namespace: namespace,
	}

	if err := c.Get(ctx, key, configMap); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return "", fmt.Errorf("failed to get ConfigMap %s/%s: %w", namespace, ref.Name, err)
		}
		// ConfigMap not found, use default if available
		if defaultValue != "" {
			log.V(1).Info("ConfigMap not found, using default value",
				"configMap", fmt.Sprintf("%s/%s", namespace, ref.Name),
				"default", defaultValue)
			return defaultValue, nil
		}
		return "", fmt.Errorf("ConfigMap %s/%s not found and no default value provided", namespace, ref.Name)
	}

	// First try to get the value with the full key (in case it contains dots)
	value, exists := configMap.Data[ref.Key]
	if !exists {
		// If the key doesn't exist as-is and contains dots, try to parse as YAML path
		if strings.Contains(ref.Key, ".") {
			// Split into base key and path
			parts := strings.SplitN(ref.Key, ".", 2)
			baseKey := parts[0]
			yamlPath := parts[1]

			// Try to get the base key
			if yamlValue, ok := configMap.Data[baseKey]; ok {
				// Try to extract from YAML
				extractedValue, err := extractYAMLValue(yamlValue, yamlPath)
				if err == nil {
					return extractedValue, nil
				}
				// If extraction failed, log but continue to default handling
				log.V(1).Info("Failed to extract YAML value",
					"key", ref.Key,
					"baseKey", baseKey,
					"path", yamlPath,
					"error", err)
			}
		}

		// Key not found
		if defaultValue != "" {
			log.V(1).Info("Key not found in ConfigMap, using default value",
				"key", ref.Key,
				"configMap", fmt.Sprintf("%s/%s", namespace, ref.Name),
				"default", defaultValue)
			return defaultValue, nil
		}
		return "", fmt.Errorf("key %s not found in ConfigMap %s/%s", ref.Key, namespace, ref.Name)
	}

	if value == "" {
		if defaultValue != "" {
			log.V(1).Info("Key in ConfigMap is empty, using default value",
				"key", ref.Key,
				"configMap", fmt.Sprintf("%s/%s", namespace, ref.Name),
				"default", defaultValue)
			return defaultValue, nil
		}
		return "", fmt.Errorf("key %s in ConfigMap %s/%s is empty", ref.Key, namespace, ref.Name)
	}

	return value, nil
}

// resolveFromSecret resolves a value from a Secret
func resolveFromSecret(ctx context.Context, c client.Client, log logr.Logger, defaultNamespace string, ref *vaultv1alpha1.SecretKeyReference, defaultValue string) (string, error) {
	namespace := ref.Namespace
	if namespace == "" {
		namespace = defaultNamespace
	}

	secret := &corev1.Secret{}
	key := types.NamespacedName{
		Name:      ref.Name,
		Namespace: namespace,
	}

	if err := c.Get(ctx, key, secret); err != nil {
		if client.IgnoreNotFound(err) != nil {
			return "", fmt.Errorf("failed to get Secret %s/%s: %w", namespace, ref.Name, err)
		}
		// Secret not found, use default if available
		if defaultValue != "" {
			log.V(1).Info("Secret not found, using default value",
				"secret", fmt.Sprintf("%s/%s", namespace, ref.Name),
				"default", defaultValue)
			return defaultValue, nil
		}
		return "", fmt.Errorf("Secret %s/%s not found and no default value provided", namespace, ref.Name)
	}

	valueBytes, exists := secret.Data[ref.Key]
	if !exists {
		if defaultValue != "" {
			log.V(1).Info("Key not found in Secret, using default value",
				"key", ref.Key,
				"secret", fmt.Sprintf("%s/%s", namespace, ref.Name),
				"default", defaultValue)
			return defaultValue, nil
		}
		return "", fmt.Errorf("key %s not found in Secret %s/%s", ref.Key, namespace, ref.Name)
	}

	value := string(valueBytes)
	if value == "" {
		if defaultValue != "" {
			log.V(1).Info("Key in Secret is empty, using default value",
				"key", ref.Key,
				"secret", fmt.Sprintf("%s/%s", namespace, ref.Name),
				"default", defaultValue)
			return defaultValue, nil
		}
		return "", fmt.Errorf("key %s in Secret %s/%s is empty", ref.Key, namespace, ref.Name)
	}

	return value, nil
}

// extractYAMLValue extracts a value from a YAML string using a dot-separated path
// For example, "transit.address" would extract the address field from a transit object
func extractYAMLValue(yamlContent string, keyPath string) (string, error) {
	// If the key path doesn't contain dots, return the content as-is
	if !strings.Contains(keyPath, ".") {
		return yamlContent, nil
	}

	// Parse the YAML
	var data map[interface{}]interface{}
	if err := yaml.Unmarshal([]byte(yamlContent), &data); err != nil {
		return "", fmt.Errorf("failed to parse YAML: %w", err)
	}

	// Split the key path and navigate through the structure
	keys := strings.Split(keyPath, ".")
	var current interface{} = data

	for i, key := range keys {
		switch v := current.(type) {
		case map[interface{}]interface{}:
			value, exists := v[key]
			if !exists {
				return "", fmt.Errorf("key %s not found at path %s", key, strings.Join(keys[:i+1], "."))
			}
			current = value
		case map[string]interface{}:
			value, exists := v[key]
			if !exists {
				return "", fmt.Errorf("key %s not found at path %s", key, strings.Join(keys[:i+1], "."))
			}
			current = value
		default:
			// If we're not at the last key and current is not a map, path is invalid
			if i < len(keys)-1 {
				return "", fmt.Errorf("invalid path: %s is not an object", strings.Join(keys[:i+1], "."))
			}
			// If we're at the last key but the value isn't what we're looking for
			return "", fmt.Errorf("cannot traverse further: %s is a %T", strings.Join(keys[:i], "."), v)
		}
	}

	// Convert final value to string
	switch v := current.(type) {
	case string:
		return v, nil
	case int, int32, int64, float32, float64, bool:
		return fmt.Sprintf("%v", v), nil
	default:
		return "", fmt.Errorf("value at path %s is not a scalar value", keyPath)
	}
}
