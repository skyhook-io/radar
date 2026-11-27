package prom

import "errors"

// ErrQueryRefused marks a query refused as bad input, by the metrics backend
// or by a layer in front of it that scopes queries, as opposed to a backend
// that failed. Callers answer it as the caller's error (HTTP 400, an MCP tool
// error), not as an unavailable backend.
var ErrQueryRefused = errors.New("query refused")

// RefusedError is a refused query. Message, when set, is what the caller is
// shown; otherwise Err's text is. It unwraps to ErrQueryRefused and Err.
type RefusedError struct {
	Message string
	Err     error
}

func (e *RefusedError) Error() string {
	if e.Message != "" {
		return e.Message
	}
	if e.Err != nil {
		return e.Err.Error()
	}
	return ErrQueryRefused.Error()
}

func (e *RefusedError) Unwrap() []error {
	if e.Err == nil {
		return []error{ErrQueryRefused}
	}
	return []error{ErrQueryRefused, e.Err}
}
