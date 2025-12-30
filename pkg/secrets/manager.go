package secrets

import (
	"context"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// Manager handles Kubernetes secret operations
type Manager interface {
	CreateOrUpdate(ctx context.Context, namespace, name string, data map[string][]byte) error
	CreateOrUpdateWithOptions(ctx context.Context, namespace, name string, data map[string][]byte, annotations map[string]string) error
	Get(ctx context.Context, namespace, name, key string) ([]byte, error)
}

// DefaultManager implements Manager interface
type DefaultManager struct {
	client.Client
	log logr.Logger
}

// NewManager creates a new secret manager
func NewManager(client client.Client, log logr.Logger) Manager {
	return &DefaultManager{
		Client: client,
		log:    log,
	}
}

// CreateOrUpdate creates or updates a secret
func (m *DefaultManager) CreateOrUpdate(ctx context.Context, namespace, name string, data map[string][]byte) error {
	return m.CreateOrUpdateWithOptions(ctx, namespace, name, data, nil)
}

// CreateOrUpdateWithOptions creates or updates a secret with optional annotations
func (m *DefaultManager) CreateOrUpdateWithOptions(ctx context.Context, namespace, name string, data map[string][]byte, annotations map[string]string) error {
	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, secret)

	if err != nil && !apierrors.IsNotFound(err) {
		return err
	}

	secret.Namespace = namespace
	secret.Name = name
	secret.Type = corev1.SecretTypeOpaque
	secret.Data = data

	if secret.Annotations == nil {
		secret.Annotations = make(map[string]string)
	}

	for k, v := range annotations {
		secret.Annotations[k] = v
	}

	if apierrors.IsNotFound(err) {
		return m.Client.Create(ctx, secret)
	}

	return m.Client.Update(ctx, secret)
}

// Get retrieves a specific key from a secret
func (m *DefaultManager) Get(ctx context.Context, namespace, name, key string) ([]byte, error) {
	secret := &corev1.Secret{}
	err := m.Client.Get(ctx, client.ObjectKey{
		Namespace: namespace,
		Name:      name,
	}, secret)

	if err != nil {
		return nil, err
	}

	value, ok := secret.Data[key]
	if !ok {
		return nil, apierrors.NewNotFound(corev1.Resource("secret"), name)
	}

	return value, nil
}
