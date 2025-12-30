package secrets

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

func TestCleanupMixedStateSecret(t *testing.T) {
	tests := []struct {
		name              string
		secret            *corev1.Secret
		expectUpdate      bool
		expectData        map[string][]byte
		expectAnnotations map[string]string
	}{
		{
			name: "clean mixed state with valid token",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-admin-token",
					Namespace: "vault",
					Annotations: map[string]string{
						"vault.homelab.io/incomplete":        "true",
						"vault.homelab.io/incomplete-reason": "some reason",
						"vault.homelab.io/recovery-required": "true",
					},
				},
				Data: map[string][]byte{
					"token":       []byte("hvs.validtoken123456"),
					"placeholder": []byte("RECOVERY-REQUIRED: old message"),
				},
			},
			expectUpdate: true,
			expectData: map[string][]byte{
				"token": []byte("hvs.validtoken123456"),
			},
			expectAnnotations: map[string]string{},
		},
		{
			name: "skip cleanup for placeholder token",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-admin-token",
					Namespace: "vault",
					Annotations: map[string]string{
						"vault.homelab.io/incomplete": "true",
					},
				},
				Data: map[string][]byte{
					"token":       []byte("placeholder-123"),
					"placeholder": []byte("RECOVERY-REQUIRED: message"),
				},
			},
			expectUpdate: false,
			expectData: map[string][]byte{
				"token":       []byte("placeholder-123"),
				"placeholder": []byte("RECOVERY-REQUIRED: message"),
			},
			expectAnnotations: map[string]string{
				"vault.homelab.io/incomplete": "true",
			},
		},
		{
			name: "clean only annotations when no placeholder data",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-admin-token",
					Namespace: "vault",
					Annotations: map[string]string{
						"vault.homelab.io/incomplete":        "true",
						"vault.homelab.io/incomplete-reason": "some reason",
					},
				},
				Data: map[string][]byte{
					"token": []byte("hvs.validtoken123456"),
				},
			},
			expectUpdate: true,
			expectData: map[string][]byte{
				"token": []byte("hvs.validtoken123456"),
			},
			expectAnnotations: map[string]string{},
		},
		{
			name: "no update needed for clean secret",
			secret: &corev1.Secret{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "vault-admin-token",
					Namespace: "vault",
				},
				Data: map[string][]byte{
					"token": []byte("hvs.validtoken123456"),
				},
			},
			expectUpdate: false,
			expectData: map[string][]byte{
				"token": []byte("hvs.validtoken123456"),
			},
			expectAnnotations: map[string]string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create scheme
			scheme := runtime.NewScheme()
			_ = corev1.AddToScheme(scheme)

			// Create fake client with the secret
			client := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(tt.secret).
				Build()

			// Create recovery manager
			rm := &RecoveryManager{
				Client: client,
				Log:    zap.New(zap.UseDevMode(true)),
			}

			// Run cleanup
			err := rm.cleanupMixedStateSecret(context.Background(), tt.secret)
			require.NoError(t, err)

			// Get the secret from the client
			updatedSecret := &corev1.Secret{}
			err = client.Get(context.Background(), types.NamespacedName{
				Name:      tt.secret.Name,
				Namespace: tt.secret.Namespace,
			}, updatedSecret)
			require.NoError(t, err)

			// Check data
			assert.Equal(t, tt.expectData, updatedSecret.Data)

			// Check annotations
			if tt.expectAnnotations == nil || len(tt.expectAnnotations) == 0 {
				// Expect no annotations or empty map
				assert.True(t, updatedSecret.Annotations == nil || len(updatedSecret.Annotations) == 0)
			} else {
				assert.Equal(t, tt.expectAnnotations, updatedSecret.Annotations)
			}
		})
	}
}
