package health

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes/scheme"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	vaultv1alpha1 "github.com/fredericrous/homelab/vault-transit-unseal-operator/api/v1alpha1"
)

func setupTest(t *testing.T) (*Checker, *runtime.Scheme) {
	s := scheme.Scheme
	require.NoError(t, vaultv1alpha1.AddToScheme(s))

	return &Checker{
		log: testr.New(t),
	}, s
}

func TestLiveness(t *testing.T) {
	checker, s := setupTest(t)

	tests := []struct {
		name    string
		objects []runtime.Object
		wantErr bool
	}{
		{
			name:    "can list resources",
			objects: []runtime.Object{},
			wantErr: false,
		},
		{
			name: "with existing resources",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
				},
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().
				WithScheme(s).
				WithRuntimeObjects(tt.objects...).
				Build()
			checker.client = client

			err := checker.Liveness(context.Background())
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestReadiness(t *testing.T) {
	checker, s := setupTest(t)

	tests := []struct {
		name          string
		objects       []runtime.Object
		clientWrapper func(ctrlclient.WithWatch) ctrlclient.WithWatch
		wantReady     bool
		wantMessage   string
	}{
		{
			name:        "no resources to manage",
			objects:     []runtime.Object{},
			wantReady:   true,
			wantMessage: "operator ready, managing 0 VaultTransitUnseal resource(s)",
		},
		{
			name: "one resource to manage",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test",
						Namespace: "default",
					},
					Spec: vaultv1alpha1.VaultTransitUnsealSpec{
						VaultPod: vaultv1alpha1.VaultPodSpec{
							Namespace: "vault",
							Selector:  map[string]string{"app": "vault"},
						},
					},
				},
			},
			wantReady:   true,
			wantMessage: "operator ready, managing 1 VaultTransitUnseal resource(s)",
		},
		{
			name: "multiple resources to manage",
			objects: []runtime.Object{
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test1",
						Namespace: "default",
					},
				},
				&vaultv1alpha1.VaultTransitUnseal{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "test2",
						Namespace: "default",
					},
				},
			},
			wantReady:   true,
			wantMessage: "operator ready, managing 2 VaultTransitUnseal resource(s)",
		},
		{
			name:    "list resources fails",
			objects: []runtime.Object{},
			clientWrapper: func(c ctrlclient.WithWatch) ctrlclient.WithWatch {
				return &errorListClient{WithWatch: c}
			},
			wantReady:   false,
			wantMessage: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewClientBuilder().
				WithScheme(s).
				WithRuntimeObjects(tt.objects...).
				Build()
			if tt.clientWrapper != nil {
				client = tt.clientWrapper(client)
			}

			checker.client = client

			err := checker.Readiness(context.Background())

			if tt.wantReady {
				assert.NoError(t, err)
				// Check last status
				result, checkTime := checker.GetLastStatus()
				assert.True(t, result.Ready)
				assert.True(t, result.Healthy)
				assert.Equal(t, tt.wantMessage, result.Message)
				assert.WithinDuration(t, time.Now(), checkTime, time.Second)
			} else {
				assert.Error(t, err)
			}
		})
	}
}

type errorListClient struct {
	ctrlclient.WithWatch
}

func (e *errorListClient) List(ctx context.Context, list ctrlclient.ObjectList, opts ...ctrlclient.ListOption) error {
	return fmt.Errorf("simulated failure")
}

func TestGetLastStatus(t *testing.T) {
	checker, _ := setupTest(t)

	// Initially empty
	result, checkTime := checker.GetLastStatus()
	assert.False(t, result.Healthy)
	assert.False(t, result.Ready)
	assert.Empty(t, result.Message)
	assert.True(t, checkTime.IsZero())

	// Set status
	checker.mu.Lock()
	checker.lastStatus = CheckResult{
		Healthy: true,
		Ready:   true,
		Message: "test message",
	}
	checker.lastCheckTime = time.Now()
	checker.mu.Unlock()

	result, checkTime = checker.GetLastStatus()
	assert.True(t, result.Healthy)
	assert.True(t, result.Ready)
	assert.Equal(t, "test message", result.Message)
	assert.WithinDuration(t, time.Now(), checkTime, time.Second)
}
