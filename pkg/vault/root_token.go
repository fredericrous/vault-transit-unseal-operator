package vault

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"

	vaultapi "github.com/hashicorp/vault/api"
)

// GenerateRootTokenStatus represents the status of root token generation
type GenerateRootTokenStatus struct {
	Started  bool
	Nonce    string
	Progress int
	Required int
	Complete bool
	OTP      string
}

// GenerateRootToken generates a new root token using recovery keys
func GenerateRootToken(ctx context.Context, client *vaultapi.Client, recoveryKeys []string) (string, error) {
	// Step 1: Generate OTP for root token generation
	otp, err := generateOTP()
	if err != nil {
		return "", fmt.Errorf("generating OTP: %w", err)
	}

	// Step 2: Initialize root token generation
	initResp, err := client.Sys().GenerateRootInit(otp, "")
	if err != nil {
		return "", fmt.Errorf("initializing root token generation: %w", err)
	}

	if initResp == nil {
		return "", fmt.Errorf("nil response from generate root init")
	}

	// Check if already in progress
	if initResp.Started {
		// Cancel existing operation
		if err := client.Sys().GenerateRootCancel(); err != nil {
			return "", fmt.Errorf("canceling existing root generation: %w", err)
		}

		// Reinitialize
		initResp, err = client.Sys().GenerateRootInit(otp, "")
		if err != nil {
			return "", fmt.Errorf("reinitializing root token generation: %w", err)
		}
	}

	// Step 3: Provide recovery keys
	var updateResp *vaultapi.GenerateRootStatusResponse
	for i, key := range recoveryKeys {
		updateResp, err = client.Sys().GenerateRootUpdate(key, initResp.Nonce)
		if err != nil {
			return "", fmt.Errorf("providing recovery key %d: %w", i, err)
		}

		if updateResp.Complete {
			break
		}
	}

	if updateResp == nil || !updateResp.Complete {
		return "", fmt.Errorf("root token generation incomplete after providing %d keys", len(recoveryKeys))
	}

	// Step 4: Decode the encoded token
	if updateResp.EncodedToken == "" {
		return "", fmt.Errorf("no encoded token in response")
	}

	decodedTokenBytes, err := base64.StdEncoding.DecodeString(updateResp.EncodedToken)
	if err != nil {
		return "", fmt.Errorf("decoding encoded token: %w", err)
	}

	otpBytes, err := base64.StdEncoding.DecodeString(otp)
	if err != nil {
		return "", fmt.Errorf("decoding OTP: %w", err)
	}

	// XOR the encoded token with the OTP to get the final token
	tokenBytes := make([]byte, len(decodedTokenBytes))
	for i := range decodedTokenBytes {
		tokenBytes[i] = decodedTokenBytes[i] ^ otpBytes[i%len(otpBytes)]
	}

	rootToken := string(tokenBytes)

	// Verify the token works
	testClient, err := vaultapi.NewClient(client.CloneConfig())
	if err != nil {
		return "", fmt.Errorf("creating test client: %w", err)
	}
	testClient.SetToken(rootToken)

	tokenInfo, err := testClient.Auth().Token().LookupSelf()
	if err != nil {
		return "", fmt.Errorf("verifying generated root token: %w", err)
	}

	if tokenInfo == nil || tokenInfo.Data == nil {
		return "", fmt.Errorf("invalid root token generated")
	}

	// Check if it's actually a root token
	policies, ok := tokenInfo.Data["policies"].([]interface{})
	if !ok || len(policies) == 0 {
		return "", fmt.Errorf("generated token has no policies")
	}

	isRoot := false
	for _, p := range policies {
		if p.(string) == "root" {
			isRoot = true
			break
		}
	}

	if !isRoot {
		return "", fmt.Errorf("generated token is not a root token")
	}

	return rootToken, nil
}

// generateOTP generates a secure one-time password for root token generation
func generateOTP() (string, error) {
	// Generate 16 bytes (128 bits) of random data
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generating random bytes: %w", err)
	}

	// Encode as base64 for the OTP
	// Vault expects standard base64 encoding with padding
	return base64.StdEncoding.EncodeToString(bytes), nil
}

// GetRootGenerationStatus returns the current status of root token generation
func GetRootGenerationStatus(ctx context.Context, client *vaultapi.Client) (*GenerateRootTokenStatus, error) {
	resp, err := client.Sys().GenerateRootStatus()
	if err != nil {
		return nil, fmt.Errorf("getting root generation status: %w", err)
	}

	return &GenerateRootTokenStatus{
		Started:  resp.Started,
		Nonce:    resp.Nonce,
		Progress: resp.Progress,
		Required: resp.Required,
		Complete: resp.Complete,
		OTP:      resp.OTP,
	}, nil
}

// CancelRootGeneration cancels an in-progress root token generation
func CancelRootGeneration(ctx context.Context, client *vaultapi.Client) error {
	return client.Sys().GenerateRootCancel()
}
