package connections

import (
	"sync"

	"github.com/skyhook-io/radar/internal/config"
)

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
