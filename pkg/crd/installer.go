package crd

import (
	"context"
	"embed"
	"fmt"

	apiextensionsv1 "k8s.io/apiextensions-apiserver/pkg/apis/apiextensions/v1"
	"k8s.io/apiextensions-apiserver/pkg/client/clientset/clientset"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

//go:embed crds/*.yaml
var crds embed.FS

// InstallCRDs installs or updates CRDs from embedded files
func InstallCRDs(ctx context.Context, cfg *rest.Config) error {
	logger := log.FromContext(ctx)

	// Create apiextensions client
	apiextClient, err := clientset.NewForConfig(cfg)
	if err != nil {
		return fmt.Errorf("failed to create apiextensions client: %w", err)
	}

	// Read all CRD files
	entries, err := crds.ReadDir("crds")
	if err != nil {
		return fmt.Errorf("failed to read crds directory: %w", err)
	}

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}

		data, err := crds.ReadFile("crds/" + entry.Name())
		if err != nil {
			return fmt.Errorf("failed to read CRD file %s: %w", entry.Name(), err)
		}

		// Decode CRD
		crd := &apiextensionsv1.CustomResourceDefinition{}
		if err := yaml.Unmarshal(data, crd); err != nil {
			return fmt.Errorf("failed to unmarshal CRD %s: %w", entry.Name(), err)
		}

		// Create or update CRD
		existing, err := apiextClient.ApiextensionsV1().CustomResourceDefinitions().Get(ctx, crd.Name, metav1.GetOptions{})
		if err != nil {
			if apierrors.IsNotFound(err) {
				// Create new CRD
				logger.Info("Creating CRD", "name", crd.Name)
				if _, err := apiextClient.ApiextensionsV1().CustomResourceDefinitions().Create(ctx, crd, metav1.CreateOptions{}); err != nil {
					return fmt.Errorf("failed to create CRD %s: %w", crd.Name, err)
				}
			} else {
				return fmt.Errorf("failed to get CRD %s: %w", crd.Name, err)
			}
		} else {
			// Update existing CRD
			logger.Info("Updating CRD", "name", crd.Name)
			crd.ResourceVersion = existing.ResourceVersion
			if _, err := apiextClient.ApiextensionsV1().CustomResourceDefinitions().Update(ctx, crd, metav1.UpdateOptions{}); err != nil {
				return fmt.Errorf("failed to update CRD %s: %w", crd.Name, err)
			}
		}
	}

	logger.Info("CRDs installed successfully")
	return nil
}
