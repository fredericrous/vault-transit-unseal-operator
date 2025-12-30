package transit

import (
	"context"
	"encoding/json"
	"fmt"
	"path"
	"strings"

	"github.com/go-logr/logr"
	vaultapi "github.com/hashicorp/vault/api"
)

// KVClient provides KV v2 operations on the transit vault
type KVClient struct {
	vaultClient *vaultapi.Client
	log         logr.Logger
	kvMount     string // KV mount path (default: "secret")
}

// NewKVClient creates a new KV client for the transit vault
func NewKVClient(address, token string, log logr.Logger) (*KVClient, error) {
	config := vaultapi.DefaultConfig()
	config.Address = address
	
	if address == "" {
		return nil, fmt.Errorf("vault address is required")
	}
	
	if token == "" {
		return nil, fmt.Errorf("vault token is required")
	}
	
	client, err := vaultapi.NewClient(config)
	if err != nil {
		return nil, fmt.Errorf("creating vault client: %w", err)
	}
	
	client.SetToken(token)
	
	return &KVClient{
		vaultClient: client,
		log:         log,
		kvMount:     "secret", // Default KV v2 mount path
	}, nil
}

// WriteToken writes an admin token to the transit vault KV v2 engine
func (kv *KVClient) WriteToken(ctx context.Context, kvPath string, token string) error {
	// Ensure path is properly formatted for KV v2
	dataPath := kv.getDataPath(kvPath)
	
	data := map[string]interface{}{
		"data": map[string]interface{}{
			"token": token,
			"metadata": map[string]string{
				"created_by": "vault-transit-unseal-operator",
				"purpose":    "admin-token-backup",
			},
		},
	}
	
	kv.log.Info("Writing token to transit vault KV", "path", dataPath)
	
	_, err := kv.vaultClient.Logical().WriteWithContext(ctx, dataPath, data)
	if err != nil {
		return fmt.Errorf("writing token to KV: %w", err)
	}
	
	kv.log.Info("Successfully backed up token to transit vault")
	return nil
}

// ReadToken reads an admin token from the transit vault KV v2 engine
func (kv *KVClient) ReadToken(ctx context.Context, kvPath string) (string, error) {
	// Ensure path is properly formatted for KV v2
	dataPath := kv.getDataPath(kvPath)
	
	kv.log.Info("Reading token from transit vault KV", "path", dataPath)
	
	secret, err := kv.vaultClient.Logical().ReadWithContext(ctx, dataPath)
	if err != nil {
		return "", fmt.Errorf("reading token from KV: %w", err)
	}
	
	if secret == nil || secret.Data == nil {
		kv.log.Info("No token found at path", "path", dataPath)
		return "", nil
	}
	
	// KV v2 wraps the actual data in a "data" field
	data, ok := secret.Data["data"].(map[string]interface{})
	if !ok {
		return "", fmt.Errorf("unexpected KV response format")
	}
	
	token, ok := data["token"].(string)
	if !ok {
		return "", fmt.Errorf("token not found in KV data")
	}
	
	kv.log.Info("Successfully retrieved token from transit vault")
	return token, nil
}

// DeleteToken removes a token from the transit vault KV v2 engine
func (kv *KVClient) DeleteToken(ctx context.Context, kvPath string) error {
	// For KV v2, we use the metadata path for deletion
	metadataPath := kv.getMetadataPath(kvPath)
	
	kv.log.Info("Deleting token from transit vault KV", "path", metadataPath)
	
	_, err := kv.vaultClient.Logical().DeleteWithContext(ctx, metadataPath)
	if err != nil {
		return fmt.Errorf("deleting token from KV: %w", err)
	}
	
	return nil
}

// CheckKVEnabled verifies that KV v2 is enabled at the expected path
func (kv *KVClient) CheckKVEnabled(ctx context.Context) error {
	// List mounted secret engines
	mounts, err := kv.vaultClient.Sys().ListMountsWithContext(ctx)
	if err != nil {
		return fmt.Errorf("listing mounts: %w", err)
	}
	
	// Check if 'secret/' mount exists and is KV v2
	mount, exists := mounts["secret/"]
	if !exists {
		return fmt.Errorf("KV engine not mounted at 'secret/'")
	}
	
	if mount.Type != "kv" {
		return fmt.Errorf("mount at 'secret/' is not KV engine (found: %s)", mount.Type)
	}
	
	// Check if it's version 2
	if mount.Options == nil || mount.Options["version"] != "2" {
		// Try to read the mount config to verify
		config, err := kv.vaultClient.Logical().ReadWithContext(ctx, "sys/mounts/secret/tune")
		if err == nil && config != nil && config.Data != nil {
			if version, ok := config.Data["options"].(map[string]interface{})["version"].(string); ok && version == "2" {
				return nil
			}
		}
		return fmt.Errorf("KV engine at 'secret/' is not version 2")
	}
	
	return nil
}

// EnsureKVEnabled ensures that KV v2 is enabled at the expected path
func (kv *KVClient) EnsureKVEnabled(ctx context.Context) error {
	// First check if it's already enabled
	if err := kv.CheckKVEnabled(ctx); err == nil {
		kv.log.V(1).Info("KV v2 already enabled", "mount", kv.kvMount)
		return nil
	}
	
	kv.log.Info("Enabling KV v2", "mount", kv.kvMount)
	
	// Enable KV v2 at the mount path
	mountInput := &vaultapi.MountInput{
		Type: "kv",
		Options: map[string]string{
			"version": "2",
		},
		Description: "KV v2 backend for vault-transit-unseal-operator token backups",
	}
	
	if err := kv.vaultClient.Sys().Mount(kv.kvMount, mountInput); err != nil {
		return fmt.Errorf("enabling KV v2 at %s: %w", kv.kvMount, err)
	}
	
	kv.log.Info("Successfully enabled KV v2", "mount", kv.kvMount)
	return nil
}

// getDataPath converts a logical path to a KV v2 data path
func (kv *KVClient) getDataPath(logicalPath string) string {
	// Remove leading/trailing slashes
	logicalPath = strings.Trim(logicalPath, "/")
	
	// If it doesn't start with secret/, prepend it
	if !strings.HasPrefix(logicalPath, "secret/") {
		logicalPath = "secret/" + logicalPath
	}
	
	// For KV v2, we need to insert "data/" after the mount point
	parts := strings.SplitN(logicalPath, "/", 2)
	if len(parts) == 2 {
		return parts[0] + "/data/" + parts[1]
	}
	return logicalPath
}

// getMetadataPath converts a logical path to a KV v2 metadata path
func (kv *KVClient) getMetadataPath(logicalPath string) string {
	// Remove leading/trailing slashes
	logicalPath = strings.Trim(logicalPath, "/")
	
	// If it doesn't start with secret/, prepend it
	if !strings.HasPrefix(logicalPath, "secret/") {
		logicalPath = "secret/" + logicalPath
	}
	
	// For KV v2, we need to insert "metadata/" after the mount point
	parts := strings.SplitN(logicalPath, "/", 2)
	if len(parts) == 2 {
		return parts[0] + "/metadata/" + parts[1]
	}
	return logicalPath
}

// BuildTokenBackupPath generates the standard backup path for a token
func BuildTokenBackupPath(namespace, name string, customPath string) string {
	if customPath != "" {
		return customPath
	}
	return path.Join("vault-transit-unseal", namespace, name, "admin-token")
}

// KVMetadata represents metadata stored with the token
type KVMetadata struct {
	CreatedBy   string            `json:"created_by"`
	Purpose     string            `json:"purpose"`
	BackupTime  string            `json:"backup_time"`
	VaultPodUID string            `json:"vault_pod_uid,omitempty"`
	Labels      map[string]string `json:"labels,omitempty"`
}

// WriteTokenWithMetadata writes a token with additional metadata
func (kv *KVClient) WriteTokenWithMetadata(ctx context.Context, kvPath string, token string, metadata KVMetadata) error {
	// Ensure path is properly formatted for KV v2
	dataPath := kv.getDataPath(kvPath)
	
	// Marshal metadata to store it as a JSON string
	metadataJSON, err := json.Marshal(metadata)
	if err != nil {
		return fmt.Errorf("marshaling metadata: %w", err)
	}
	
	data := map[string]interface{}{
		"data": map[string]interface{}{
			"token":    token,
			"metadata": string(metadataJSON),
		},
	}
	
	kv.log.Info("Writing token with metadata to transit vault KV", "path", dataPath)
	
	_, err = kv.vaultClient.Logical().WriteWithContext(ctx, dataPath, data)
	if err != nil {
		return fmt.Errorf("writing token to KV: %w", err)
	}
	
	kv.log.Info("Successfully backed up token with metadata to transit vault")
	return nil
}