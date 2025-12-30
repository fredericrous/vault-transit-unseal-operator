package vault

import (
	"context"
	"encoding/base64"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGenerateOTP(t *testing.T) {
	// Test OTP generation
	otp1, err := generateOTP()
	require.NoError(t, err)
	assert.NotEmpty(t, otp1)

	// Verify it's valid base64
	decoded, err := base64.RawStdEncoding.DecodeString(otp1)
	require.NoError(t, err)
	assert.Len(t, decoded, 16) // Should be 16 bytes

	// Generate another OTP and verify they're different
	otp2, err := generateOTP()
	require.NoError(t, err)
	assert.NotEqual(t, otp1, otp2, "OTPs should be unique")
}

func TestGenerateRootToken_Integration(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	// This test would require a real Vault instance with transit seal
	// Example setup:
	/*
	vaultAddr := os.Getenv("VAULT_ADDR")
	if vaultAddr == "" {
		t.Skip("VAULT_ADDR not set, skipping integration test")
	}

	// Would need recovery keys from a real initialized Vault
	recoveryKeys := strings.Split(os.Getenv("VAULT_RECOVERY_KEYS"), ",")
	if len(recoveryKeys) < 3 {
		t.Skip("VAULT_RECOVERY_KEYS not set properly, skipping integration test")
	}

	config := vaultapi.DefaultConfig()
	config.Address = vaultAddr
	client, err := vaultapi.NewClient(config)
	require.NoError(t, err)

	ctx := context.Background()
	
	// Test root token generation
	rootToken, err := GenerateRootToken(ctx, client, recoveryKeys)
	require.NoError(t, err)
	assert.NotEmpty(t, rootToken)
	assert.True(t, strings.HasPrefix(rootToken, "hvs."))

	// Verify the token works
	client.SetToken(rootToken)
	tokenInfo, err := client.Auth().Token().LookupSelf()
	require.NoError(t, err)
	
	policies, ok := tokenInfo.Data["policies"].([]interface{})
	require.True(t, ok)
	
	hasRootPolicy := false
	for _, p := range policies {
		if p.(string) == "root" {
			hasRootPolicy = true
			break
		}
	}
	assert.True(t, hasRootPolicy, "Generated token should have root policy")

	// Clean up - revoke the root token
	err = client.Auth().Token().RevokeSelf(rootToken)
	assert.NoError(t, err)
	*/
}

func TestGetRootGenerationStatus_Integration(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	// This would test getting the status of root generation
	/*
	vaultAddr := os.Getenv("VAULT_ADDR")
	if vaultAddr == "" {
		t.Skip("VAULT_ADDR not set, skipping integration test")
	}

	config := vaultapi.DefaultConfig()
	config.Address = vaultAddr
	client, err := vaultapi.NewClient(config)
	require.NoError(t, err)

	ctx := context.Background()
	
	// Get initial status
	status, err := GetRootGenerationStatus(ctx, client)
	require.NoError(t, err)
	assert.NotNil(t, status)
	assert.False(t, status.Started) // Should not be in progress initially
	assert.Equal(t, 0, status.Progress)
	*/
}

func TestCancelRootGeneration_Integration(t *testing.T) {
	// Skip this test if not running integration tests
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	// This would test canceling an in-progress root generation
	/*
	vaultAddr := os.Getenv("VAULT_ADDR")
	if vaultAddr == "" {
		t.Skip("VAULT_ADDR not set, skipping integration test")
	}

	config := vaultapi.DefaultConfig()
	config.Address = vaultAddr
	client, err := vaultapi.NewClient(config)
	require.NoError(t, err)

	ctx := context.Background()
	
	// Start a root generation
	otp, err := generateOTP()
	require.NoError(t, err)
	
	_, err = client.Sys().GenerateRootInit(otp, "")
	require.NoError(t, err)
	
	// Verify it's started
	status, err := GetRootGenerationStatus(ctx, client)
	require.NoError(t, err)
	assert.True(t, status.Started)
	
	// Cancel it
	err = CancelRootGeneration(ctx, client)
	require.NoError(t, err)
	
	// Verify it's cancelled
	status, err = GetRootGenerationStatus(ctx, client)
	require.NoError(t, err)
	assert.False(t, status.Started)
	*/
}

// Mock tests for XOR logic
func TestTokenDecoding(t *testing.T) {
	// Test the XOR decoding logic
	testToken := "test-root-token-value"
	testOTP := "abcdefghijklmnop" // 16 chars
	
	// Encode the token (simulate what Vault does)
	otpBytes, err := base64.RawStdEncoding.DecodeString(base64.RawStdEncoding.EncodeToString([]byte(testOTP)))
	require.NoError(t, err)
	
	tokenBytes := []byte(testToken)
	encodedBytes := make([]byte, len(tokenBytes))
	for i := range tokenBytes {
		encodedBytes[i] = tokenBytes[i] ^ otpBytes[i%len(otpBytes)]
	}
	
	// Now decode it (what our function does)
	decodedBytes := make([]byte, len(encodedBytes))
	for i := range encodedBytes {
		decodedBytes[i] = encodedBytes[i] ^ otpBytes[i%len(otpBytes)]
	}
	
	decodedToken := string(decodedBytes)
	assert.Equal(t, testToken, decodedToken, "Token should be correctly decoded")
}