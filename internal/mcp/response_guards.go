package mcp

import (
	"fmt"
	"sort"
)

// Large ConfigMaps (init scripts, bundled JSON configs) can dominate a
// get_resource response with tens of KB the agent rarely needs in full.
// Values are truncated only when the total data size is genuinely large, and
// always with an explicit warning — silent truncation would read as "that's
// the whole value".
const (
	configMapGuardTotalBytes = 16 * 1024
	configMapGuardValueBytes = 8 * 1024
)

// truncateLargeConfigMapData truncates oversized data values in a minified
// ConfigMap payload (map[string]any with a "data" section). Returns the
// (possibly modified) payload and a warning note ("" when nothing changed).
func truncateLargeConfigMapData(resourceData any) (any, string) {
	m, ok := resourceData.(map[string]any)
	if !ok {
		return resourceData, ""
	}
	data, ok := m["data"].(map[string]any)
	if !ok {
		return resourceData, ""
	}
	total := 0
	for _, v := range data {
		if s, ok := v.(string); ok {
			total += len(s)
		}
	}
	if total <= configMapGuardTotalBytes {
		return resourceData, ""
	}

	var truncatedKeys []string
	for k, v := range data {
		s, ok := v.(string)
		if !ok || len(s) <= configMapGuardValueBytes {
			continue
		}
		data[k] = s[:configMapGuardValueBytes] + fmt.Sprintf("\n…[truncated by radar: value is %d bytes, showing first %d]", len(s), configMapGuardValueBytes)
		truncatedKeys = append(truncatedKeys, k)
	}
	if len(truncatedKeys) == 0 {
		return resourceData, ""
	}
	sort.Strings(truncatedKeys)
	return resourceData, fmt.Sprintf(
		"large ConfigMap (%d bytes total): values truncated to %dKB for keys %v",
		total, configMapGuardValueBytes/1024, truncatedKeys,
	)
}
