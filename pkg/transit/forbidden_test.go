package transit

import (
	"fmt"
	"testing"

	vaultapi "github.com/hashicorp/vault/api"
)

func TestIsForbidden(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"nil", nil, false},
		{"ResponseError 403", &vaultapi.ResponseError{StatusCode: 403}, true},
		{"ResponseError 404", &vaultapi.ResponseError{StatusCode: 404}, false},
		{"ResponseError 500", &vaultapi.ResponseError{StatusCode: 500}, false},
		{"wrapped 403", fmt.Errorf("listing mounts: %w", &vaultapi.ResponseError{StatusCode: 403}), true},
		{"plain permission denied", fmt.Errorf("permission denied"), true},
		{"plain 403 in string", fmt.Errorf("got 403 from vault"), true},
		{"unrelated error", fmt.Errorf("connection refused"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isForbidden(tc.err); got != tc.want {
				t.Fatalf("isForbidden(%v) = %v, want %v", tc.err, got, tc.want)
			}
		})
	}
}
