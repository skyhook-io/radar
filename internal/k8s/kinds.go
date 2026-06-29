package k8s

import "strings"

// ClusterOnlyKindInfo carries the canonical (group, resource) tuple for a
// cluster-scoped kind, used by SubjectAccessReview lookups.
type ClusterOnlyKindInfo struct {
	Group    string
	Resource string
}

// clusterOnlyKinds is the static catalogue of cluster-scoped kinds keyed by
// lowercase kind (plural REST form, singular, and common short forms). The
// value is the canonical (group, resource) for SAR lookups.
//
// "namespaces" is included so cluster-scoped SAR gates have its GVR, but
// IsClusterOnlyKind() returns false for it — namespace-restricted users
// still get a filtered list of their allowed namespaces rather than being
// blocked outright.
var clusterOnlyKinds = map[string]ClusterOnlyKindInfo{
	"nodes":                           {"", "nodes"},
	"node":                            {"", "nodes"},
	"no":                              {"", "nodes"},
	"persistentvolumes":               {"", "persistentvolumes"},
	"persistentvolume":                {"", "persistentvolumes"},
	"pv":                              {"", "persistentvolumes"},
	"namespaces":                      {"", "namespaces"},
	"namespace":                       {"", "namespaces"},
	"ns":                              {"", "namespaces"},
	"storageclasses":                  {"storage.k8s.io", "storageclasses"},
	"storageclass":                    {"storage.k8s.io", "storageclasses"},
	"sc":                              {"storage.k8s.io", "storageclasses"},
	"ingressclasses":                  {"networking.k8s.io", "ingressclasses"},
	"ingressclass":                    {"networking.k8s.io", "ingressclasses"},
	"clusterroles":                    {"rbac.authorization.k8s.io", "clusterroles"},
	"clusterrole":                     {"rbac.authorization.k8s.io", "clusterroles"},
	"clusterrolebindings":             {"rbac.authorization.k8s.io", "clusterrolebindings"},
	"clusterrolebinding":              {"rbac.authorization.k8s.io", "clusterrolebindings"},
	"priorityclasses":                 {"scheduling.k8s.io", "priorityclasses"},
	"priorityclass":                   {"scheduling.k8s.io", "priorityclasses"},
	"runtimeclasses":                  {"node.k8s.io", "runtimeclasses"},
	"runtimeclass":                    {"node.k8s.io", "runtimeclasses"},
	"mutatingwebhookconfigurations":   {"admissionregistration.k8s.io", "mutatingwebhookconfigurations"},
	"mutatingwebhookconfiguration":    {"admissionregistration.k8s.io", "mutatingwebhookconfigurations"},
	"validatingwebhookconfigurations": {"admissionregistration.k8s.io", "validatingwebhookconfigurations"},
	"validatingwebhookconfiguration":  {"admissionregistration.k8s.io", "validatingwebhookconfigurations"},
	"customresourcedefinitions":       {"apiextensions.k8s.io", "customresourcedefinitions"},
	"customresourcedefinition":        {"apiextensions.k8s.io", "customresourcedefinitions"},
	"crd":                             {"apiextensions.k8s.io", "customresourcedefinitions"},
}

// IsClusterOnlyKind reports whether the kind is cluster-scoped AND should
// be hidden from namespace-restricted users. "namespaces" is cluster-scoped
// at the K8s level but is exposed as a filtered list, so it returns false.
func IsClusterOnlyKind(kind string) bool {
	k := strings.ToLower(kind)
	if k == "namespaces" || k == "namespace" || k == "ns" {
		return false
	}
	_, ok := clusterOnlyKinds[k]
	return ok
}

// ClusterOnlyKindGVR returns the (group, resource) tuple for a known
// cluster-scoped kind. The bool is true when the kind is recognized; false
// for dynamic / unknown kinds (callers should use discovery in that case).
func ClusterOnlyKindGVR(kind string) (group, resource string, ok bool) {
	info, exists := clusterOnlyKinds[strings.ToLower(kind)]
	if !exists {
		return "", "", false
	}
	return info.Group, info.Resource, true
}

// ClassifyKindScope reports whether the given (kind, group) is cluster-
// scoped and, if so, returns the canonical (group, resource) for a SAR.
// Tries the static cluster-only catalogue first, then resource discovery.
//
// When group is non-empty, discovery is restricted to that group —
// disambiguates colliding kinds (e.g. Knative Service vs core Service, CAPI
// Cluster vs CNPG Cluster). Pass "" when the caller doesn't know the group;
// the static catalogue still wins on core kinds.
//
// Returns (false, "", "") for namespaced kinds, unknown kinds, or when
// discovery isn't available.
func ClassifyKindScope(kind, group string) (clusterScoped bool, gvrGroup, gvrResource string) {
	if g, r, ok := ClusterOnlyKindGVR(kind); ok {
		return true, g, r
	}
	disc := GetResourceDiscovery()
	if disc == nil {
		return false, "", ""
	}
	if group != "" {
		if ar, ok := disc.GetResourceWithGroup(kind, group); ok && !ar.Namespaced {
			return true, ar.Group, ar.Name
		}
		return false, "", ""
	}
	if ar, ok := disc.GetResource(kind); ok && !ar.Namespaced {
		return true, ar.Group, ar.Name
	}
	return false, "", ""
}
