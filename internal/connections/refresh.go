package connections

import (
	"sync"

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

var refreshMu sync.RWMutex
var refreshOperation func(config.Integration) error

func RegisterRefresh(fn func(config.Integration) error) {
	refreshMu.Lock()
	defer refreshMu.Unlock()
	refreshOperation = fn
}

func Refresh(kind config.Integration) error {
	refreshMu.RLock()
	fn := refreshOperation
	refreshMu.RUnlock()
	if fn != nil {
		return fn(kind)
	}
	return nil
}
