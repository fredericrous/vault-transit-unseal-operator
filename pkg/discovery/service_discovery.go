package discovery

import (
	"context"
	"fmt"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	"sigs.k8s.io/controller-runtime/pkg/client"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

// ServiceDiscovery handles discovering services for vault pods
type ServiceDiscovery struct {
	Client client.Client
	Log    logr.Logger
}

// DiscoverVaultService finds the appropriate service for connecting to Vault
func (d *ServiceDiscovery) DiscoverVaultService(ctx context.Context, vaultSpec *vaultv1alpha1.VaultPodSpec) (string, int32, error) {
	// 1. If explicit service name is provided, use it
	if vaultSpec.ServiceName != "" {
		port := vaultSpec.ServicePort
		if port == 0 {
			port = 8200
		}
		d.Log.V(1).Info("Using configured service", "name", vaultSpec.ServiceName, "port", port)
		return vaultSpec.ServiceName, port, nil
	}

	// 2. Auto-discover service using selector
	d.Log.V(1).Info("Auto-discovering service", "namespace", vaultSpec.Namespace, "selector", vaultSpec.Selector)
	
	// List all services in the namespace
	serviceList := &corev1.ServiceList{}
	if err := d.Client.List(ctx, serviceList, client.InNamespace(vaultSpec.Namespace)); err != nil {
		return "", 0, fmt.Errorf("failed to list services: %w", err)
	}

	// Find services that match the pod selector
	podSelector := labels.SelectorFromSet(vaultSpec.Selector)
	
	var matchingServices []corev1.Service
	for _, svc := range serviceList.Items {
		// Skip services without selectors
		if len(svc.Spec.Selector) == 0 {
			continue
		}

		// Check if service selector matches our pod selector
		serviceSelector := labels.SelectorFromSet(svc.Spec.Selector)
		if d.selectorsMatch(podSelector, serviceSelector, vaultSpec.Selector) {
			matchingServices = append(matchingServices, svc)
			d.Log.V(2).Info("Found matching service", "name", svc.Name, "selector", svc.Spec.Selector)
		}
	}

	if len(matchingServices) == 0 {
		return "", 0, fmt.Errorf("no service found matching selector %v in namespace %s", vaultSpec.Selector, vaultSpec.Namespace)
	}

	// Select the best matching service
	selectedService := d.selectBestService(matchingServices)
	
	// Find the appropriate port
	port := d.findVaultPort(selectedService)
	
	d.Log.Info("Auto-discovered service", "name", selectedService.Name, "port", port)
	return selectedService.Name, port, nil
}

// selectorsMatch checks if the service selector could match pods with the given pod selector
func (d *ServiceDiscovery) selectorsMatch(podSelector, serviceSelector labels.Selector, podLabels map[string]string) bool {
	// Check if all service selector requirements are satisfied by pod labels
	requirements, _ := serviceSelector.Requirements()
	for _, req := range requirements {
		value, exists := podLabels[req.Key()]
		if !exists {
			return false
		}
		// For simple equality, check if values match
		if req.Operator() == "=" && !req.Matches(labels.Set{req.Key(): value}) {
			return false
		}
	}
	return true
}

// selectBestService selects the most appropriate service from multiple matches
func (d *ServiceDiscovery) selectBestService(services []corev1.Service) corev1.Service {
	// Prefer services with common Vault naming patterns
	preferredNames := []string{"vault", "vault-active", "vault-standby", "vault-internal"}
	
	for _, preferred := range preferredNames {
		for _, svc := range services {
			if svc.Name == preferred {
				d.Log.V(1).Info("Selected preferred service", "name", svc.Name)
				return svc
			}
		}
	}

	// Return the first service if no preferred name found
	return services[0]
}

// findVaultPort finds the appropriate port for Vault connection
func (d *ServiceDiscovery) findVaultPort(service corev1.Service) int32 {
	// Common vault port names
	portNames := []string{"http", "vault", "api", ""}
	
	// First, look for ports by common names
	for _, portName := range portNames {
		for _, port := range service.Spec.Ports {
			if port.Name == portName || (portName == "" && port.Port == 8200) {
				d.Log.V(2).Info("Found port", "name", port.Name, "port", port.Port)
				return port.Port
			}
		}
	}

	// If no standard port found, use the first port
	if len(service.Spec.Ports) > 0 {
		return service.Spec.Ports[0].Port
	}

	// Default to 8200
	return 8200
}

// GetVaultAddress constructs the full Vault address using service discovery
func (d *ServiceDiscovery) GetVaultAddress(ctx context.Context, vaultSpec *vaultv1alpha1.VaultPodSpec) (string, error) {
	// If full address override is specified, use it
	if vaultSpec.VaultAddress != "" {
		d.Log.V(1).Info("Using vault address override", "address", vaultSpec.VaultAddress)
		return vaultSpec.VaultAddress, nil
	}

	// Discover service
	serviceName, port, err := d.DiscoverVaultService(ctx, vaultSpec)
	if err != nil {
		return "", err
	}

	// Construct the address
	address := fmt.Sprintf("http://%s.%s.svc.cluster.local:%d", serviceName, vaultSpec.Namespace, port)
	d.Log.V(1).Info("Constructed vault address", "address", address)
	
	return address, nil
}