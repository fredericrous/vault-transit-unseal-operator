package transit

import (
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/stretchr/testify/assert"
)

func TestBuildTokenBackupPath(t *testing.T) {
	tests := []struct {
		name       string
		namespace  string
		vtuName    string
		customPath string
		expected   string
	}{
		{
			name:       "default path",
			namespace:  "vault",
			vtuName:    "vault-homelab",
			customPath: "",
			expected:   "vault-transit-unseal/vault/vault-homelab/admin-token",
		},
		{
			name:       "custom path",
			namespace:  "vault",
			vtuName:    "vault-homelab",
			customPath: "custom/backup/path",
			expected:   "custom/backup/path",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildTokenBackupPath(tt.namespace, tt.vtuName, tt.customPath)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestKVClient_getDataPath(t *testing.T) {
	kv := &KVClient{log: testr.New(t)}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple path",
			input:    "my-secret",
			expected: "secret/data/my-secret",
		},
		{
			name:     "path with secret prefix",
			input:    "secret/my-secret",
			expected: "secret/data/my-secret",
		},
		{
			name:     "nested path",
			input:    "vault-transit-unseal/vault/my-vault/admin-token",
			expected: "secret/data/vault-transit-unseal/vault/my-vault/admin-token",
		},
		{
			name:     "path with leading slash",
			input:    "/my-secret",
			expected: "secret/data/my-secret",
		},
		{
			name:     "path with trailing slash",
			input:    "my-secret/",
			expected: "secret/data/my-secret",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := kv.getDataPath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestKVClient_getMetadataPath(t *testing.T) {
	kv := &KVClient{log: testr.New(t)}

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "simple path",
			input:    "my-secret",
			expected: "secret/metadata/my-secret",
		},
		{
			name:     "nested path",
			input:    "vault-transit-unseal/vault/my-vault/admin-token",
			expected: "secret/metadata/vault-transit-unseal/vault/my-vault/admin-token",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := kv.getMetadataPath(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
