package gitops

import "strings"

// StringValue returns v as a string, or "" if v is not a string.
// Convenience helper for unstructured map[string]any access where typed
// assertions would otherwise litter the call sites.
func StringValue(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

// GroupFromAPIVersion extracts the API group from a Kubernetes apiVersion
// string. Returns "" for the core group ("v1" or empty input).
func GroupFromAPIVersion(apiVersion string) string {
	if apiVersion == "" || apiVersion == "v1" {
		return ""
	}
	if before, _, ok := strings.Cut(apiVersion, "/"); ok {
		return before
	}
	return apiVersion
}

// ParseFluxInventoryID parses Flux's namespace_name_group_kind inventory key
// from the right so resource names containing underscores remain intact.
func ParseFluxInventoryID(id string) (group, kind, namespace, name string, ok bool) {
	parts := strings.Split(id, "_")
	if len(parts) < 4 {
		return "", "", "", "", false
	}
	kind = parts[len(parts)-1]
	group = parts[len(parts)-2]
	namespace = parts[0]
	name = strings.Join(parts[1:len(parts)-2], "_")
	if kind == "" || name == "" {
		return "", "", "", "", false
	}
	if group == "core" {
		group = ""
	}
	return group, kind, namespace, name, true
}
