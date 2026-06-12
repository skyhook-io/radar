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

// truncateLargeConfigMapData truncates oversized values in a minified
// ConfigMap payload (map[string]any with "data" / "binaryData" sections),
// mutating the payload in place. binaryData counts too — base64 blobs (cert
// bundles, jars) are routinely the largest values. Returns the payload and a
// warning note ("" when nothing changed).
func truncateLargeConfigMapData(resourceData any) (any, string) {
	m, ok := resourceData.(map[string]any)
	if !ok {
		return resourceData, ""
	}
	var sections []map[string]any
	for _, key := range []string{"data", "binaryData"} {
		if section, ok := m[key].(map[string]any); ok {
			sections = append(sections, section)
		}
	}
	total := 0
	for _, section := range sections {
		for _, v := range section {
			if s, ok := v.(string); ok {
				total += len(s)
			}
		}
	}
	if total <= configMapGuardTotalBytes {
		return resourceData, ""
	}

	var truncatedKeys []string
	for _, section := range sections {
		for k, v := range section {
			s, ok := v.(string)
			if !ok || len(s) <= configMapGuardValueBytes {
				continue
			}
			section[k] = s[:configMapGuardValueBytes] + fmt.Sprintf("\n…[truncated by radar: value is %d bytes, showing first %d]", len(s), configMapGuardValueBytes)
			truncatedKeys = append(truncatedKeys, k)
		}
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
