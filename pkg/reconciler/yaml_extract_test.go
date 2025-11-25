package reconciler

import (
	"testing"
)

func TestExtractYAMLValue(t *testing.T) {
	tests := []struct {
		name        string
		yamlContent string
		keyPath     string
		expected    string
		expectError bool
	}{
		{
			name:        "simple string value",
			yamlContent: "hello world",
			keyPath:     "",
			expected:    "hello world",
		},
		{
			name: "nested yaml extraction",
			yamlContent: `transit:
  address: "http://vault.vault.svc.cluster.local:8200"
  mountPath: "transit"
  keyName: "autounseal"`,
			keyPath:  "transit.address",
			expected: "http://vault.vault.svc.cluster.local:8200",
		},
		{
			name: "deeply nested value",
			yamlContent: `level1:
  level2:
    level3:
      value: "deep value"`,
			keyPath:  "level1.level2.level3.value",
			expected: "deep value",
		},
		{
			name: "missing key",
			yamlContent: `transit:
  address: "http://vault.vault.svc.cluster.local:8200"`,
			keyPath:     "transit.missing",
			expectError: true,
		},
		{
			name: "invalid path - not an object",
			yamlContent: `transit:
  address: "http://vault.vault.svc.cluster.local:8200"`,
			keyPath:     "transit.address.invalid",
			expectError: true,
		},
		{
			name: "boolean value",
			yamlContent: `config:
  enabled: true`,
			keyPath:  "config.enabled",
			expected: "true",
		},
		{
			name: "numeric value",
			yamlContent: `config:
  port: 8200`,
			keyPath:  "config.port",
			expected: "8200",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := extractYAMLValue(tt.yamlContent, tt.keyPath)

			if tt.expectError {
				if err == nil {
					t.Errorf("expected error but got none")
				}
			} else {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
					return
				}
				if result != tt.expected {
					t.Errorf("expected %q but got %q", tt.expected, result)
				}
			}
		})
	}
}
