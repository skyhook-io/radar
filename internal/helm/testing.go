package helm

import "sync"

// ResetTestClient clears the process settings between independent test fixtures.
func ResetTestClient() {
	globalClient = nil
	clientOnce = sync.Once{}
}
