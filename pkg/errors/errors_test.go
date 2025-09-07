package errors

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestOperatorError(t *testing.T) {
	tests := []struct {
		name        string
		err         *OperatorError
		wantMessage string
		wantRetry   bool
	}{
		{
			name: "transient error with wrapped error",
			err: &OperatorError{
				Type:    ErrorTypeTransient,
				Message: "connection failed",
				Err:     fmt.Errorf("timeout"),
			},
			wantMessage: "connection failed: timeout",
			wantRetry:   true,
		},
		{
			name: "permanent error without wrapped error",
			err: &OperatorError{
				Type:    ErrorTypePermanent,
				Message: "invalid configuration",
			},
			wantMessage: "invalid configuration",
			wantRetry:   false,
		},
		{
			name: "config error",
			err: &OperatorError{
				Type:    ErrorTypeConfig,
				Message: "missing required field",
				Err:     fmt.Errorf("field 'namespace' is required"),
			},
			wantMessage: "missing required field: field 'namespace' is required",
			wantRetry:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.wantMessage, tt.err.Error())
			assert.Equal(t, tt.wantRetry, tt.err.ShouldRetry())
		})
	}
}

func TestErrorHelpers(t *testing.T) {
	t.Run("NewTransientError", func(t *testing.T) {
		err := NewTransientError("network error", fmt.Errorf("connection refused"))
		assert.Equal(t, ErrorTypeTransient, err.Type)
		assert.True(t, err.ShouldRetry())
		assert.False(t, err.IsPermanent())
		assert.False(t, err.IsConfig())
	})

	t.Run("NewPermanentError", func(t *testing.T) {
		err := NewPermanentError("validation failed", nil)
		assert.Equal(t, ErrorTypePermanent, err.Type)
		assert.False(t, err.ShouldRetry())
		assert.True(t, err.IsPermanent())
		assert.False(t, err.IsConfig())
	})

	t.Run("NewConfigError", func(t *testing.T) {
		err := NewConfigError("invalid setting", nil)
		assert.Equal(t, ErrorTypeConfig, err.Type)
		assert.False(t, err.ShouldRetry())
		assert.False(t, err.IsPermanent())
		assert.True(t, err.IsConfig())
	})
}

func TestWithContext(t *testing.T) {
	err := NewTransientError("operation failed", nil)
	err = err.WithContext("pod", "vault-0").
		WithContext("namespace", "vault").
		WithContext("attempt", 3)

	assert.Len(t, err.Context, 3)
	assert.Equal(t, "vault-0", err.Context["pod"])
	assert.Equal(t, "vault", err.Context["namespace"])
	assert.Equal(t, 3, err.Context["attempt"])
}

func TestUnwrap(t *testing.T) {
	innerErr := fmt.Errorf("inner error")
	err := NewTransientError("outer error", innerErr)

	assert.True(t, errors.Is(err, innerErr))

	var target *OperatorError
	assert.True(t, errors.As(err, &target))
	assert.Equal(t, err, target)
}

func TestIsOperatorError(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "is operator error",
			err:  NewTransientError("test", nil),
			want: true,
		},
		{
			name: "regular error",
			err:  fmt.Errorf("regular error"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
		{
			name: "wrapped operator error",
			err:  fmt.Errorf("wrapped: %w", NewTransientError("test", nil)),
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := IsOperatorError(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestGetErrorType(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want ErrorType
	}{
		{
			name: "transient operator error",
			err:  NewTransientError("test", nil),
			want: ErrorTypeTransient,
		},
		{
			name: "permanent operator error",
			err:  NewPermanentError("test", nil),
			want: ErrorTypePermanent,
		},
		{
			name: "config operator error",
			err:  NewConfigError("test", nil),
			want: ErrorTypeConfig,
		},
		{
			name: "regular error defaults to transient",
			err:  fmt.Errorf("regular error"),
			want: ErrorTypeTransient,
		},
		{
			name: "wrapped operator error",
			err:  fmt.Errorf("wrapped: %w", NewPermanentError("test", nil)),
			want: ErrorTypePermanent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := GetErrorType(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestShouldRetry(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "transient error should retry",
			err:  NewTransientError("test", nil),
			want: true,
		},
		{
			name: "permanent error should not retry",
			err:  NewPermanentError("test", nil),
			want: false,
		},
		{
			name: "config error should not retry",
			err:  NewConfigError("test", nil),
			want: false,
		},
		{
			name: "regular error defaults to retry",
			err:  fmt.Errorf("regular error"),
			want: true,
		},
		{
			name: "nil error should retry",
			err:  nil,
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := ShouldRetry(tt.err)
			assert.Equal(t, tt.want, got)
		})
	}
}
