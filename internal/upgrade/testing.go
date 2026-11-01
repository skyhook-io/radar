package upgrade

import "time"

// HTTP integration tests live in another package and must exercise this timer.
func SetTestAPIMetricsCollectionTimeout(timeout time.Duration) func() {
	previous := upgradeAPIMetricsCollectionTimeout
	upgradeAPIMetricsCollectionTimeout = timeout
	return func() { upgradeAPIMetricsCollectionTimeout = previous }
}
