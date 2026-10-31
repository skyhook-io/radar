package cnpg

import "time"

// HTTP integration tests live in another package and must exercise this timer.
func SetTestLogDiscoveryInterval(interval time.Duration) func() {
	previous := logDiscoveryInterval
	logDiscoveryInterval = interval
	return func() { logDiscoveryInterval = previous }
}
