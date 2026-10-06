package podlogs

import "strconv"

// ParseSinceSeconds parses sinceSeconds query parameter, returning nil if not set
func ParseSinceSeconds(str string) *int64 {
	if str == "" {
		return nil
	}
	if s, err := strconv.ParseInt(str, 10, 64); err == nil && s > 0 {
		return &s
	}
	return nil
}

// ParseTailLines parses tailLines query parameter with a default value
func ParseTailLines(str string, defaultVal int64) int64 {
	if str == "" {
		return defaultVal
	}
	if t, err := strconv.ParseInt(str, 10, 64); err == nil && t > 0 {
		return t
	}
	return defaultVal
}
