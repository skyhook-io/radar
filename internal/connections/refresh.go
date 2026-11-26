package connections

import (
	"github.com/skyhook-io/radar/internal/config"
)

// SettingsError is a failure of the current context's own integration settings
// (unreadable file, paused cluster identity, invalid entry or launch override),
// so callers can send users to Settings instead of blaming the backend.
type SettingsError struct {
	Kind   config.Integration
	Launch bool
	Err    error
}

func (e *SettingsError) Error() string { return e.Err.Error() }
func (e *SettingsError) Unwrap() error { return e.Err }
