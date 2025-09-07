package vault

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNewClient(t *testing.T) {
	tests := []struct {
		name    string
		config  *Config
		wantErr bool
	}{
		{
			name: "valid config",
			config: &Config{
				Address:       "http://localhost:8200",
				Token:         "test-token",
				Namespace:     "test-ns",
				TLSSkipVerify: true,
				Timeout:       10 * time.Second,
			},
			wantErr: false,
		},
		{
			name: "minimal config",
			config: &Config{
				Address: "http://localhost:8200",
			},
			wantErr: false,
		},
		{
			name: "config without token",
			config: &Config{
				Address:   "http://localhost:8200",
				Namespace: "test-ns",
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient(tt.config)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, client)
			} else {
				assert.NoError(t, err)
				assert.NotNil(t, client)
			}
		})
	}
}

func TestNewClientForPod(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "vault-0",
			Namespace: "vault",
		},
		Status: corev1.PodStatus{
			PodIP: "10.0.0.1",
		},
	}

	client, err := NewClientForPod(pod, true)
	require.NoError(t, err)
	assert.NotNil(t, client)
}

func TestCheckStatus(t *testing.T) {
	tests := []struct {
		name         string
		serverResp   string
		serverStatus int
		wantStatus   *Status
		wantErr      bool
	}{
		{
			name: "healthy initialized unsealed vault",
			serverResp: `{
				"initialized": true,
				"sealed": false,
				"standby": false,
				"version": "1.15.0",
				"cluster_id": "test-cluster-123"
			}`,
			serverStatus: http.StatusOK,
			wantStatus: &Status{
				Initialized: true,
				Sealed:      false,
				Version:     "1.15.0",
				ClusterID:   "test-cluster-123",
			},
			wantErr: false,
		},
		{
			name: "uninitialized vault",
			serverResp: `{
				"initialized": false,
				"sealed": true,
				"standby": false,
				"version": "1.15.0",
				"cluster_id": ""
			}`,
			serverStatus: http.StatusOK,
			wantStatus: &Status{
				Initialized: false,
				Sealed:      true,
				Version:     "1.15.0",
				ClusterID:   "",
			},
			wantErr: false,
		},
		{
			name:         "server error",
			serverResp:   `{"errors":["internal server error"]}`,
			serverStatus: http.StatusInternalServerError,
			wantStatus:   nil,
			wantErr:      true,
		},
		{
			name:         "invalid json response",
			serverResp:   `{invalid json`,
			serverStatus: http.StatusOK,
			wantStatus:   nil,
			wantErr:      true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v1/sys/health", r.URL.Path)
				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResp))
			}))
			defer server.Close()

			client, err := NewClient(&Config{
				Address: server.URL,
				Timeout: 5 * time.Second,
			})
			require.NoError(t, err)

			status, err := client.CheckStatus(context.Background())

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, status)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantStatus, status)
			}
		})
	}
}

func TestInitialize(t *testing.T) {
	tests := []struct {
		name              string
		serverResp        string
		serverStatus      int
		recoveryShares    int
		recoveryThreshold int
		wantResp          *InitResponse
		wantErr           bool
	}{
		{
			name: "successful initialization",
			serverResp: `{
				"recovery_keys": ["key1", "key2", "key3"],
				"recovery_keys_base64": ["a2V5MQ==", "a2V5Mg==", "a2V5Mw=="],
				"root_token": "root-token-123"
			}`,
			serverStatus:      http.StatusOK,
			recoveryShares:    3,
			recoveryThreshold: 2,
			wantResp: &InitResponse{
				RootToken:       "root-token-123",
				RecoveryKeys:    []string{"key1", "key2", "key3"},
				RecoveryKeysB64: []string{"a2V5MQ==", "a2V5Mg==", "a2V5Mw=="},
			},
			wantErr: false,
		},
		{
			name: "vault already initialized",
			serverResp: `{
				"errors": ["Vault is already initialized"]
			}`,
			serverStatus:      http.StatusBadRequest,
			recoveryShares:    3,
			recoveryThreshold: 2,
			wantResp:          nil,
			wantErr:           true,
		},
		{
			name:              "server error",
			serverResp:        `{"errors":["internal server error"]}`,
			serverStatus:      http.StatusInternalServerError,
			recoveryShares:    3,
			recoveryThreshold: 2,
			wantResp:          nil,
			wantErr:           true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/v1/sys/init", r.URL.Path)
				assert.Equal(t, "PUT", r.Method)
				w.WriteHeader(tt.serverStatus)
				_, _ = w.Write([]byte(tt.serverResp))
			}))
			defer server.Close()

			client, err := NewClient(&Config{
				Address: server.URL,
				Timeout: 5 * time.Second,
			})
			require.NoError(t, err)

			resp, err := client.Initialize(context.Background(), tt.recoveryShares, tt.recoveryThreshold)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, resp)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantResp, resp)
			}
		})
	}
}

func TestIsHealthy(t *testing.T) {
	tests := []struct {
		name         string
		serverStatus int
		want         bool
	}{
		{
			name:         "healthy vault",
			serverStatus: http.StatusOK,
			want:         true,
		},
		{
			name:         "unhealthy vault",
			serverStatus: http.StatusServiceUnavailable,
			want:         false,
		},
		{
			name:         "server error",
			serverStatus: http.StatusInternalServerError,
			want:         false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.serverStatus)
				if tt.serverStatus == http.StatusOK {
					_, _ = w.Write([]byte(`{"initialized":true,"sealed":false,"version":"1.15.0"}`))
				}
			}))
			defer server.Close()

			client, err := NewClient(&Config{
				Address: server.URL,
				Timeout: 5 * time.Second,
			})
			require.NoError(t, err)

			got := client.IsHealthy(context.Background())
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClientWithToken(t *testing.T) {
	expectedToken := "test-token-12345"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify token is set in header
		assert.Equal(t, expectedToken, r.Header.Get("X-Vault-Token"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false}`))
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Address: server.URL,
		Token:   expectedToken,
		Timeout: 5 * time.Second,
	})
	require.NoError(t, err)

	// Make a request that should include the token
	status, err := client.CheckStatus(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, status)
}

func TestClientWithNamespace(t *testing.T) {
	expectedNamespace := "test-namespace"

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify namespace is set in header
		assert.Equal(t, expectedNamespace, r.Header.Get("X-Vault-Namespace"))
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"initialized":true,"sealed":false}`))
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Address:   server.URL,
		Namespace: expectedNamespace,
		Timeout:   5 * time.Second,
	})
	require.NoError(t, err)

	// Make a request that should include the namespace
	status, err := client.CheckStatus(context.Background())
	assert.NoError(t, err)
	assert.NotNil(t, status)
}

func TestClientTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Sleep longer than client timeout
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	client, err := NewClient(&Config{
		Address: server.URL,
		Timeout: 50 * time.Millisecond,
	})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err = client.CheckStatus(ctx)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "context deadline exceeded")
}
