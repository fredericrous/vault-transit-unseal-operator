package errors

import (
	"errors"
	"fmt"
)

// ErrorType categorizes errors for proper handling
type ErrorType int

const (
	// ErrorTypeTransient indicates a temporary error that should be retried
	ErrorTypeTransient ErrorType = iota
	// ErrorTypePermanent indicates an error that won't be fixed by retrying
	ErrorTypePermanent
	// ErrorTypeConfig indicates a configuration error
	ErrorTypeConfig
)

// OperatorError provides structured error information
type OperatorError struct {
	Type    ErrorType
	Message string
	Err     error
	Context map[string]interface{}
}

// Error implements the error interface
func (e *OperatorError) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %v", e.Message, e.Err)
	}
	return e.Message
}

// Unwrap allows errors.Is and errors.As to work
func (e *OperatorError) Unwrap() error {
	return e.Err
}

// ShouldRetry indicates if the operation should be retried
func (e *OperatorError) ShouldRetry() bool {
	return e.Type == ErrorTypeTransient
}

// IsPermanent indicates if the error is permanent
func (e *OperatorError) IsPermanent() bool {
	return e.Type == ErrorTypePermanent
}

// IsConfig indicates if the error is a configuration issue
func (e *OperatorError) IsConfig() bool {
	return e.Type == ErrorTypeConfig
}

// NewTransientError creates a transient error
func NewTransientError(message string, err error) *OperatorError {
	return &OperatorError{
		Type:    ErrorTypeTransient,
		Message: message,
		Err:     err,
	}
}

// NewPermanentError creates a permanent error
func NewPermanentError(message string, err error) *OperatorError {
	return &OperatorError{
		Type:    ErrorTypePermanent,
		Message: message,
		Err:     err,
	}
}

// NewConfigError creates a configuration error
func NewConfigError(message string, err error) *OperatorError {
	return &OperatorError{
		Type:    ErrorTypeConfig,
		Message: message,
		Err:     err,
	}
}

// WithContext adds context to an error
func (e *OperatorError) WithContext(key string, value interface{}) *OperatorError {
	if e.Context == nil {
		e.Context = make(map[string]interface{})
	}
	e.Context[key] = value
	return e
}

// IsOperatorError checks if an error is an OperatorError
func IsOperatorError(err error) bool {
	var opErr *OperatorError
	return errors.As(err, &opErr)
}

// GetErrorType returns the error type or defaults to transient
func GetErrorType(err error) ErrorType {
	var opErr *OperatorError
	if errors.As(err, &opErr) {
		return opErr.Type
	}
	// Default to transient for unknown errors
	return ErrorTypeTransient
}

// ShouldRetry checks if an error indicates a retry is appropriate
func ShouldRetry(err error) bool {
	var opErr *OperatorError
	if errors.As(err, &opErr) {
		return opErr.ShouldRetry()
	}
	// Default to retry for unknown errors
	return true
}
