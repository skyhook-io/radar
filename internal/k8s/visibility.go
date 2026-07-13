package k8s

import (
	"slices"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// VisibilitySummary explains when Radar is connected but cannot see enough
// core workload resources to make dashboard/issue results complete.
type VisibilitySummary struct {
	State                string            `json:"state"`
	Scope                *VisibilityScope  `json:"scope,omitempty"`
	Core                 map[string]string `json:"core,omitempty"`
	MissingOptionalKinds []string          `json:"missingOptionalKinds,omitempty"`
	Impact               string            `json:"impact,omitempty"`
}

type VisibilityScope struct {
	Namespace string `json:"namespace,omitempty"`
}

// VisibilityNamespace returns the single namespace from the slice when the
// caller is scoped to exactly one, or "" when broader / unscoped. Used by
// both REST and MCP handlers to derive the namespace argument for
// BuildVisibilitySummary. Centralized here so the (namespaces → hint)
// mapping lives in one place across surfaces.
func VisibilityNamespace(namespaces []string) string {
	if len(namespaces) == 1 {
		return namespaces[0]
	}
	return ""
}

// BuildVisibilitySummary returns nil when visibility is normal. It is based on
// the existing startup permission probes; it does not perform live RBAC calls.
func BuildVisibilitySummary(result *PermissionCheckResult, namespace string) *VisibilitySummary {
	if result == nil {
		return nil
	}

	core := map[string]string{
		"pods":        visibilityStatus(result, k8score.Pods, namespace),
		"deployments": visibilityStatus(result, k8score.Deployments, namespace),
		"services":    visibilityStatus(result, k8score.Services, namespace),
	}

	podsVisible := core["pods"] == "allowed" || core["pods"] == "namespace_limited"
	deploymentsVisible := core["deployments"] == "allowed" || core["deployments"] == "namespace_limited"

	optionalKinds := []struct {
		key  string
		name string
	}{
		{k8score.Events, "events"},
		{k8score.ConfigMaps, "configMaps"},
	}

	missingOptional := make([]string, 0, len(optionalKinds))
	for _, kind := range optionalKinds {
		if visibilityStatus(result, kind.key, namespace) == "unavailable" {
			missingOptional = append(missingOptional, kind.name)
		}
	}

	state := "ok"
	impact := ""
	if !podsVisible && !deploymentsVisible {
		state = "degraded"
		impact = "Radar cannot read core workload resources for this scope; pod health, workload status, topology, and issue detection may be empty or misleading."
	} else if core["pods"] != "allowed" || core["deployments"] != "allowed" || core["services"] != "allowed" || len(missingOptional) > 0 {
		state = "limited"
		impact = "Some related resource types are unavailable; diagnostics may omit supporting context."
	}
	if state == "ok" {
		return nil
	}

	out := &VisibilitySummary{
		State:                state,
		Core:                 core,
		MissingOptionalKinds: missingOptional,
		Impact:               impact,
	}
	if namespace != "" {
		out.Scope = &VisibilityScope{Namespace: namespace}
	}
	return out
}

func visibilityStatus(result *PermissionCheckResult, key string, namespace string) string {
	scope, ok := result.Scopes[key]
	if !ok || !scope.Enabled {
		return "unavailable"
	}
	if scope.Namespace == "" {
		return "allowed"
	}
	if namespace == "" {
		return "namespace_limited"
	}
	if scope.Namespace == namespace {
		return "allowed"
	}
	// Multi-namespace kinds: scope.Namespace is only the first grant; the
	// full fanout lives in ScopeNamespaces.
	if slices.Contains(result.ScopeNamespaces[key], namespace) {
		return "allowed"
	}
	return "unavailable"
}
