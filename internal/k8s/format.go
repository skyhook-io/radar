package k8s

import (
	"strings"

	"k8s.io/apimachinery/pkg/api/resource"
)

// Truncate trims a string to maxLen characters, appending "..." if truncated.
func Truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// ParseCPUToMillis parses a CPU quantity such as "250m", "1" or "500n" into
// millicores, rounding up. metrics-server writes each value in its shortest
// exact form, so the same reading can arrive as "98753000n" or "98753u".
// Unparseable input reads as 0.
func ParseCPUToMillis(s string) int64 {
	q, err := resource.ParseQuantity(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return q.MilliValue()
}

// ParseMemoryToBytes parses a memory quantity such as "1024Ki", "256Mi" or
// "1G" into bytes, rounding up. Unparseable input reads as 0.
func ParseMemoryToBytes(s string) int64 {
	q, err := resource.ParseQuantity(strings.TrimSpace(s))
	if err != nil {
		return 0
	}
	return q.Value()
}
