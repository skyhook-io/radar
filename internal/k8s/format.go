package k8s

import (
	"fmt"
	"strings"
	"time"
)

// FormatAge formats a duration into a human-readable age string (e.g., "5d", "3h").
func FormatAge(d time.Duration) string {
	if d < time.Minute {
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
	if d < time.Hour {
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

// Truncate trims a string to maxLen characters, appending "..." if truncated.
func Truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}

// ParseCPUToMillis parses CPU quantity strings like "250m", "1", "500n".
func ParseCPUToMillis(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if before, ok := strings.CutSuffix(s, "n"); ok {
		var val int64
		fmt.Sscanf(before, "%d", &val)
		return val / 1000000
	}
	if before, ok := strings.CutSuffix(s, "m"); ok {
		var val int64
		fmt.Sscanf(before, "%d", &val)
		return val
	}
	var val int64
	fmt.Sscanf(s, "%d", &val)
	return val * 1000
}

// ParseMemoryToBytes parses memory quantity strings like "1024Ki", "256Mi", "1Gi".
func ParseMemoryToBytes(s string) int64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	if before, ok := strings.CutSuffix(s, "Ki"); ok {
		var val int64
		fmt.Sscanf(before, "%d", &val)
		return val * 1024
	}
	if before, ok := strings.CutSuffix(s, "Mi"); ok {
		var val int64
		fmt.Sscanf(before, "%d", &val)
		return val * 1024 * 1024
	}
	if before, ok := strings.CutSuffix(s, "Gi"); ok {
		var val int64
		fmt.Sscanf(before, "%d", &val)
		return val * 1024 * 1024 * 1024
	}
	var val int64
	fmt.Sscanf(s, "%d", &val)
	return val
}
