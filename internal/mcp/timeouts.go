package mcp

import (
	"fmt"
	"time"
)

// boundedTimeout turns a seconds argument into a duration: zero means the
// default, and anything above max is refused rather than silently clamped.
func boundedTimeout(seconds int, def, max time.Duration) (time.Duration, error) {
	if seconds < 0 {
		return 0, fmt.Errorf("timeout_seconds must be positive")
	}
	if seconds == 0 {
		return def, nil
	}
	d := time.Duration(seconds) * time.Second
	if d > max {
		return 0, fmt.Errorf("timeout_seconds must be at most %d", int(max.Seconds()))
	}
	return d, nil
}
