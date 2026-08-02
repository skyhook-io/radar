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
	configMapGuardTotalBytes  = 16 * 1024
	configMapGuardValueBytes  = 8 * 1024
	maxMultiPodLogBundleBytes = 32 * 1024
	// configMapGuardValueFloor bounds how far the per-value cap tightens when
	// many medium values blow the total budget — below this, values stop
	// being readable at all.
	configMapGuardValueFloor = 512
)

type multiPodLogBundleCap struct {
	Truncated             bool
	ShownLines            int
	TotalLines            int
	ShownPods             int
	TotalPods             int
	FirstOmittedPod       string
	FirstOmittedContainer string
	FirstOmittedBundle    int
}

// capMultiPodLogBundles keeps aggregate log responses from consuming the
// agent's context window. Whole lines are selected breadth-first across
// pod/container streams so one noisy pod cannot monopolize the budget.
func capMultiPodLogBundles(bundles ...[]podLogEntry) ([][]podLogEntry, multiPodLogBundleCap) {
	stats := multiPodLogBundleCap{FirstOmittedBundle: -1}
	totalBytes := 0
	totalPods := map[string]struct{}{}
	for _, bundle := range bundles {
		for _, entry := range bundle {
			if len(entry.Logs.Lines) > 0 {
				totalPods[entry.Pod] = struct{}{}
			}
			for _, line := range entry.Logs.Lines {
				stats.TotalLines++
				totalBytes += len(line) + 1
			}
		}
	}
	stats.TotalPods = len(totalPods)
	if totalBytes <= maxMultiPodLogBundleBytes {
		stats.ShownLines = stats.TotalLines
		stats.ShownPods = stats.TotalPods
		return bundles, stats
	}

	capped := make([][]podLogEntry, len(bundles))
	blocked := make([][]bool, len(bundles))
	for bundleIndex, bundle := range bundles {
		capped[bundleIndex] = make([]podLogEntry, len(bundle))
		blocked[bundleIndex] = make([]bool, len(bundle))
		copy(capped[bundleIndex], bundle)
		for entryIndex := range capped[bundleIndex] {
			if len(bundle[entryIndex].Logs.Lines) > 0 {
				capped[bundleIndex][entryIndex].Logs.Lines = make([]string, 0, len(bundle[entryIndex].Logs.Lines))
			}
		}
	}

	usedBytes := 0
	shownPods := map[string]struct{}{}
	for lineIndex := 0; ; lineIndex++ {
		found := false
		for bundleIndex, bundle := range bundles {
			for entryIndex, entry := range bundle {
				if blocked[bundleIndex][entryIndex] || lineIndex >= len(entry.Logs.Lines) {
					continue
				}
				found = true
				line := entry.Logs.Lines[lineIndex]
				lineBytes := len(line) + 1
				if usedBytes+lineBytes > maxMultiPodLogBundleBytes {
					blocked[bundleIndex][entryIndex] = true
					if stats.FirstOmittedBundle < 0 {
						stats.FirstOmittedPod = entry.Pod
						stats.FirstOmittedContainer = entry.Container
						stats.FirstOmittedBundle = bundleIndex
					}
					continue
				}
				capped[bundleIndex][entryIndex].Logs.Lines = append(capped[bundleIndex][entryIndex].Logs.Lines, line)
				usedBytes += lineBytes
				stats.ShownLines++
				shownPods[entry.Pod] = struct{}{}
			}
		}
		if !found {
			break
		}
	}

	stats.Truncated = stats.ShownLines < stats.TotalLines
	stats.ShownPods = len(shownPods)
	return capped, stats
}

func multiPodLogBundleNarrowHint(namespace string, stats multiPodLogBundleCap, previous bool) string {
	scope := fmt.Sprintf(
		"showing logs from %d of %d pods / %d of %d lines",
		stats.ShownPods, stats.TotalPods, stats.ShownLines, stats.TotalLines,
	)
	if stats.ShownPods == stats.TotalPods {
		scope = fmt.Sprintf(
			"showing %d of %d lines across %d pods",
			stats.ShownLines, stats.TotalLines, stats.TotalPods,
		)
	}
	return fmt.Sprintf(
		"log bundle truncated: %s (%d KiB aggregate log-content cap reached) — use `get_pod_logs namespace=%q name=%q container=%q previous=%t` to inspect the omitted stream, or narrow with since= or grep=",
		scope, maxMultiPodLogBundleBytes/1024, namespace, stats.FirstOmittedPod, stats.FirstOmittedContainer, previous,
	)
}

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
	total, valueCount := 0, 0
	for _, section := range sections {
		for _, v := range section {
			if s, ok := v.(string); ok {
				total += len(s)
				valueCount++
			}
		}
	}
	if total <= configMapGuardTotalBytes {
		return resourceData, ""
	}

	// A swarm of medium values can blow the total budget without any single
	// value crossing the per-value cap — tighten the cap so the truncated
	// total lands near the budget. The floor means a huge key count can still
	// exceed the budget; accepted, the realistic bombs are few large values.
	valueCap := configMapGuardValueBytes
	if evenCap := configMapGuardTotalBytes / valueCount; evenCap < valueCap {
		valueCap = max(evenCap, configMapGuardValueFloor)
	}

	var truncatedKeys []string
	for _, section := range sections {
		for k, v := range section {
			s, ok := v.(string)
			if !ok || len(s) <= valueCap {
				continue
			}
			replacement := s[:valueCap] + fmt.Sprintf("\n…[truncated by radar: value is %d bytes, showing first %d]", len(s), valueCap)
			// Marker overhead can exceed the bytes saved for values only just
			// over the cap — never grow a value in the name of truncating it.
			if len(replacement) >= len(s) {
				continue
			}
			section[k] = replacement
			truncatedKeys = append(truncatedKeys, k)
		}
	}
	if len(truncatedKeys) == 0 {
		// Over budget but no value is large enough that truncating it would
		// shrink the payload (many small keys) — leave them intact, but the
		// size still deserves a warning.
		return resourceData, fmt.Sprintf(
			"large ConfigMap (%d bytes total across %d keys): values left intact — none large enough that truncation would reduce the payload",
			total, valueCount,
		)
	}
	sort.Strings(truncatedKeys)
	return resourceData, fmt.Sprintf(
		"large ConfigMap (%d bytes total): values truncated to %d bytes for keys %v",
		total, valueCap, truncatedKeys,
	)
}
