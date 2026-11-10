package config

import (
	"fmt"
	"os"
	"strconv"
)

// A nil override preserves the listener/authentication-based server default.
func PprofOverrideFromEnv() (*bool, error) {
	if raw := os.Getenv("RADAR_PPROF"); raw != "" {
		enabled, err := strconv.ParseBool(raw)
		if err != nil {
			return nil, fmt.Errorf("RADAR_PPROF must be a boolean: %w", err)
		}
		return &enabled, nil
	}
	return nil, nil
}
