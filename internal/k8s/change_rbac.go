package k8s

import "strings"

// namespacedBuiltinKinds maps the lowercase Kind of the well-known namespaced
// builtin resources to their canonical (group, resource) for SAR lookups. It is
// the namespaced counterpart of clusterOnlyKinds: together they let
// ResolveChangeGVR authorize every builtin kind without a discovery round-trip,
// which (a) avoids a cold-start window where authenticated users briefly see an
// empty timeline before discovery warms up, and (b) keeps the per-kind gate
// unit-testable. CRDs and anything not listed here fall through to discovery.
var namespacedBuiltinKinds = map[string]ClusterOnlyKindInfo{
	"pod":                     {"", "pods"},
	"service":                 {"", "services"},
	"configmap":               {"", "configmaps"},
	"secret":                  {"", "secrets"},
	"event":                   {"", "events"},
	"serviceaccount":          {"", "serviceaccounts"},
	"persistentvolumeclaim":   {"", "persistentvolumeclaims"},
	"limitrange":              {"", "limitranges"},
	"resourcequota":           {"", "resourcequotas"},
	"deployment":              {"apps", "deployments"},
	"daemonset":               {"apps", "daemonsets"},
	"statefulset":             {"apps", "statefulsets"},
	"replicaset":              {"apps", "replicasets"},
	"job":                     {"batch", "jobs"},
	"cronjob":                 {"batch", "cronjobs"},
	"ingress":                 {"networking.k8s.io", "ingresses"},
	"networkpolicy":           {"networking.k8s.io", "networkpolicies"},
	"endpointslice":           {"discovery.k8s.io", "endpointslices"},
	"horizontalpodautoscaler": {"autoscaling", "horizontalpodautoscalers"},
	"poddisruptionbudget":     {"policy", "poddisruptionbudgets"},
	"role":                    {"rbac.authorization.k8s.io", "roles"},
	"rolebinding":             {"rbac.authorization.k8s.io", "rolebindings"},
}

// namespacedBuiltinGVR returns the canonical (group, resource) for a well-known
// namespaced builtin kind. The bool is false for CRDs / unknown kinds (callers
// fall through to discovery).
func namespacedBuiltinGVR(kind string) (group, resource string, ok bool) {
	info, exists := namespacedBuiltinKinds[strings.ToLower(kind)]
	if !exists {
		return "", "", false
	}
	return info.Group, info.Resource, true
}

// This file holds the single, shared per-kind authorization decision for
// timeline/change/diff/drop data. Every surface that returns such data — REST
// handlers (internal/server) and MCP tools (internal/mcp) — routes through
// ChangeReadAllowed so the RBAC rule is defined once and can't drift or be
// forgotten. Each caller supplies its own authorize callback bound to its auth
// context (REST: canReadUser; MCP: canReadInNamespace); the resolve + scope +
// fail-closed logic lives here.

// ResolveChangeGVR resolves the (group, resource-plural, cluster-scoped) for a
// change/timeline event so per-kind SAR gates can authorize it. `group` is the
// caller's known group hint — a ResourceChange's group (dynamic cache) or the
// group parsed from an apiVersion — and disambiguates CRD kind collisions.
// Returns ok=false when neither the cluster-only catalogue nor discovery can
// resolve it (callers fail closed).
//
// The cluster-scoped case is delegated to ClassifyKindScope, which owns the
// builtin-catalogue-vs-group-hint collision guard; namespaced kinds are then
// resolved from discovery. clusterScoped drives the SAR namespace — cluster-
// scoped kinds must authorize at namespace "" even when the event row carries a
// namespace (a K8s Event about a Node stores the Event's own namespace, not the
// cluster-scoped involved object's).
func ResolveChangeGVR(kind, group string) (gvrGroup, gvrResource string, clusterScoped, ok bool) {
	if kind == "" {
		return "", "", false, false
	}
	if cs, g, r := ClassifyKindScope(kind, group); cs {
		return g, r, true, true
	}
	// Namespaced builtins resolve from the static catalogue — discovery-free and
	// collision-guarded (a disagreeing group hint means a CRD collision → fall
	// through to discovery for the real GVR).
	if g, r, found := namespacedBuiltinGVR(kind); found && (group == "" || group == g) {
		return g, r, false, true
	}
	disc := GetResourceDiscovery()
	if disc == nil {
		return "", "", false, false
	}
	if group != "" {
		// Hint present: resolve within that exact group, or fail closed — a
		// group-less fallback here would re-admit the builtin-collision leak.
		if ar, found := disc.GetResourceWithGroup(kind, group); found {
			return ar.Group, ar.Name, !ar.Namespaced, true
		}
		return "", "", false, false
	}
	if ar, found := disc.GetResource(kind); found {
		return ar.Group, ar.Name, !ar.Namespaced, true
	}
	return "", "", false, false
}

// GroupFromAPIVersion extracts the API group from an apiVersion string
// ("apps/v1" → "apps", "v1" → "", "cluster.x-k8s.io/v1beta1" → "cluster.x-k8s.io").
func GroupFromAPIVersion(apiVersion string) string {
	if i := strings.IndexByte(apiVersion, '/'); i >= 0 {
		return apiVersion[:i]
	}
	return ""
}

// ChangeReadAllowed reports whether a change/diff/drop row for (kind, apiVersion,
// namespace) may be shown to a caller whose per-kind read authority is `authorize`
// (returns true iff the caller may "list" that group/resource in that namespace).
//
// It is the one place the timeline RBAC rule lives: resolve the GVR (apiVersion
// disambiguates CRD collisions), authorize cluster-scoped kinds at namespace ""
// (their row's namespace is not the object's), and fail closed when the kind
// can't be resolved — during discovery warmup an authenticated caller briefly
// sees fewer rows rather than unauthorized ones.
func ChangeReadAllowed(kind, apiVersion, namespace string, authorize func(group, resource, namespace string) bool) bool {
	group, resource, clusterScoped, ok := ResolveChangeGVR(kind, GroupFromAPIVersion(apiVersion))
	if !ok {
		return false
	}
	if clusterScoped {
		namespace = ""
	}
	return authorize(group, resource, namespace)
}
