// Package errors provides structured error types with codes for the Explorer backend.
// These error types enable consistent error handling, logging, and API responses.
package errors

import (
	"errors"
	"fmt"
)

// ErrorCode represents a unique identifier for error types.
// Codes are organized by category:
//   - 1xxx: K8s/cluster errors
//   - 2xxx: Server/HTTP errors
//   - 3xxx: Cache errors
//   - 4xxx: Timeline/storage errors
//   - 5xxx: Helm errors
type ErrorCode int

const (
	// K8s/Cluster errors (1xxx)
	ErrK8sClientNotInitialized ErrorCode = 1001
	ErrK8sContextSwitch        ErrorCode = 1002
	ErrK8sResourceNotFound     ErrorCode = 1003
	ErrK8sAPIError             ErrorCode = 1004
	ErrK8sClusterUnreachable   ErrorCode = 1005

	// Server/HTTP errors (2xxx)
	ErrBadRequest         ErrorCode = 2001
	ErrNotFound           ErrorCode = 2002
	ErrInternalServer     ErrorCode = 2003
	ErrValidation         ErrorCode = 2004
	ErrServiceUnavailable ErrorCode = 2005
	ErrMarshalFailed      ErrorCode = 2006

	// Cache errors (3xxx)
	ErrCacheNotInitialized  ErrorCode = 3001
	ErrCacheSyncFailed      ErrorCode = 3002
	ErrCacheHandlerFailed   ErrorCode = 3003
	ErrCacheDynamicNotFound ErrorCode = 3004

	// Timeline/storage errors (4xxx)
	ErrTimelineStoreNotInit ErrorCode = 4001
	ErrTimelineWriteFailed  ErrorCode = 4002
	ErrTimelineQueryFailed  ErrorCode = 4003
	ErrTimelineEventDropped ErrorCode = 4004

	// Helm errors (5xxx)
	ErrHelmClientNotInit   ErrorCode = 5001
	ErrHelmReleaseNotFound ErrorCode = 5002
	ErrHelmOperationFailed ErrorCode = 5003
)

// String returns a human-readable code identifier.
func (c ErrorCode) String() string {
	switch c {
	// K8s errors
	case ErrK8sClientNotInitialized:
		return "K8S_CLIENT_NOT_INITIALIZED"
	case ErrK8sContextSwitch:
		return "K8S_CONTEXT_SWITCH_FAILED"
	case ErrK8sResourceNotFound:
		return "K8S_RESOURCE_NOT_FOUND"
	case ErrK8sAPIError:
		return "K8S_API_ERROR"
	case ErrK8sClusterUnreachable:
		return "K8S_CLUSTER_UNREACHABLE"
	// Server errors
	case ErrBadRequest:
		return "BAD_REQUEST"
	case ErrNotFound:
		return "NOT_FOUND"
	case ErrInternalServer:
		return "INTERNAL_SERVER_ERROR"
	case ErrValidation:
		return "VALIDATION_ERROR"
	case ErrServiceUnavailable:
		return "SERVICE_UNAVAILABLE"
	case ErrMarshalFailed:
		return "MARSHAL_FAILED"
	// Cache errors
	case ErrCacheNotInitialized:
		return "CACHE_NOT_INITIALIZED"
	case ErrCacheSyncFailed:
		return "CACHE_SYNC_FAILED"
	case ErrCacheHandlerFailed:
		return "CACHE_HANDLER_FAILED"
	case ErrCacheDynamicNotFound:
		return "CACHE_DYNAMIC_NOT_FOUND"
	// Timeline errors
	case ErrTimelineStoreNotInit:
		return "TIMELINE_STORE_NOT_INITIALIZED"
	case ErrTimelineWriteFailed:
		return "TIMELINE_WRITE_FAILED"
	case ErrTimelineQueryFailed:
		return "TIMELINE_QUERY_FAILED"
	case ErrTimelineEventDropped:
		return "TIMELINE_EVENT_DROPPED"
	// Helm errors
	case ErrHelmClientNotInit:
		return "HELM_CLIENT_NOT_INITIALIZED"
	case ErrHelmReleaseNotFound:
		return "HELM_RELEASE_NOT_FOUND"
	case ErrHelmOperationFailed:
		return "HELM_OPERATION_FAILED"
	default:
		return fmt.Sprintf("UNKNOWN_ERROR_%d", c)
	}
}

// ExplorerError is a structured error type with error codes.
type ExplorerError struct {
	Code    ErrorCode
	Message string
	Cause   error
	Details map[string]any // Additional context for debugging
}

// Error implements the error interface.
func (e *ExplorerError) Error() string {
	if e.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", e.Code.String(), e.Message, e.Cause)
	}
	return fmt.Sprintf("[%s] %s", e.Code.String(), e.Message)
}

// Unwrap returns the underlying error for errors.Is/As support.
func (e *ExplorerError) Unwrap() error {
	return e.Cause
}

// New creates a new ExplorerError with the given code and message.
func New(code ErrorCode, message string) *ExplorerError {
	return &ExplorerError{
		Code:    code,
		Message: message,
	}
}

// Wrap wraps an existing error with an ExplorerError.
func Wrap(code ErrorCode, message string, cause error) *ExplorerError {
	return &ExplorerError{
		Code:    code,
		Message: message,
		Cause:   cause,
	}
}

// WithDetails adds contextual details to the error.
func (e *ExplorerError) WithDetails(details map[string]any) *ExplorerError {
	e.Details = details
	return e
}

// WithDetail adds a single detail key-value pair.
func (e *ExplorerError) WithDetail(key string, value any) *ExplorerError {
	if e.Details == nil {
		e.Details = make(map[string]any)
	}
	e.Details[key] = value
	return e
}

// GetCode extracts the error code from an error if it's an ExplorerError.
// Returns 0 if the error is not an ExplorerError.
func GetCode(err error) ErrorCode {
	var explorerErr *ExplorerError
	if errors.As(err, &explorerErr) {
		return explorerErr.Code
	}
	return 0
}

// IsCode checks if an error has the specified error code.
func IsCode(err error, code ErrorCode) bool {
	return GetCode(err) == code
}

// --- Convenience constructors for common errors ---

// K8sClientNotInitialized returns an error for when the K8s client isn't ready.
func K8sClientNotInitialized() *ExplorerError {
	return New(ErrK8sClientNotInitialized, "K8s client not initialized")
}

// K8sResourceNotFound returns an error when a K8s resource can't be found.
func K8sResourceNotFound(kind, namespace, name string) *ExplorerError {
	return New(ErrK8sResourceNotFound, fmt.Sprintf("%s %s/%s not found", kind, namespace, name)).
		WithDetail("kind", kind).
		WithDetail("namespace", namespace).
		WithDetail("name", name)
}

// CacheNotInitialized returns an error when the resource cache isn't available.
func CacheNotInitialized() *ExplorerError {
	return New(ErrCacheNotInitialized, "resource cache not initialized")
}

// ValidationError returns an error for invalid input.
func ValidationError(message string) *ExplorerError {
	return New(ErrValidation, message)
}

// InternalError wraps an internal error with additional context.
func InternalError(message string, cause error) *ExplorerError {
	return Wrap(ErrInternalServer, message, cause)
}

// MarshalError returns an error for JSON marshaling failures.
func MarshalError(cause error) *ExplorerError {
	return Wrap(ErrMarshalFailed, "failed to marshal data", cause)
}
