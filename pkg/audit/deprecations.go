package audit

// DeprecationEntry describes a deprecated Kubernetes API version.
type DeprecationEntry struct {
	// GroupVersion is the deprecated API group/version (e.g. "extensions/v1beta1").
	GroupVersion string
	// Kind is the resource kind affected (empty = all kinds in this GV).
	Kind string
	// DeprecatedIn is the K8s version where this was deprecated (e.g. "1.14").
	DeprecatedIn string
	// RemovedIn is the K8s version where this was removed (e.g. "1.22").
	RemovedIn string
	// Replacement is the stable API to use instead (e.g. "networking.k8s.io/v1").
	Replacement string
}

// DeprecationCatalogReviewedThrough includes reviewed releases with no removed built-in API versions.
const DeprecationCatalogReviewedThrough = "1.36"

// DeprecationTable contains known deprecated K8s APIs from 1.16 through 1.32.
// Sources: https://kubernetes.io/docs/reference/using-api/deprecation-guide/
var DeprecationTable = []DeprecationEntry{
	// ── Removed in 1.22 ───────────────────────────────────────────────
	{GroupVersion: "extensions/v1beta1", Kind: "Ingress", DeprecatedIn: "1.14", RemovedIn: "1.22", Replacement: "networking.k8s.io/v1"},
	{GroupVersion: "networking.k8s.io/v1beta1", Kind: "Ingress", DeprecatedIn: "1.19", RemovedIn: "1.22", Replacement: "networking.k8s.io/v1"},
	{GroupVersion: "networking.k8s.io/v1beta1", Kind: "IngressClass", DeprecatedIn: "1.19", RemovedIn: "1.22", Replacement: "networking.k8s.io/v1"},
	{GroupVersion: "rbac.authorization.k8s.io/v1beta1", Kind: "", DeprecatedIn: "1.17", RemovedIn: "1.22", Replacement: "rbac.authorization.k8s.io/v1"},
	{GroupVersion: "coordination.k8s.io/v1beta1", Kind: "Lease", DeprecatedIn: "1.19", RemovedIn: "1.22", Replacement: "coordination.k8s.io/v1"},
	{GroupVersion: "extensions/v1beta1", Kind: "DaemonSet", DeprecatedIn: "1.14", RemovedIn: "1.22", Replacement: "apps/v1"},
	{GroupVersion: "extensions/v1beta1", Kind: "Deployment", DeprecatedIn: "1.14", RemovedIn: "1.22", Replacement: "apps/v1"},
	{GroupVersion: "extensions/v1beta1", Kind: "ReplicaSet", DeprecatedIn: "1.14", RemovedIn: "1.22", Replacement: "apps/v1"},

	// ── Removed in 1.25 ───────────────────────────────────────────────
	{GroupVersion: "batch/v1beta1", Kind: "CronJob", DeprecatedIn: "1.21", RemovedIn: "1.25", Replacement: "batch/v1"},
	{GroupVersion: "discovery.k8s.io/v1beta1", Kind: "EndpointSlice", DeprecatedIn: "1.21", RemovedIn: "1.25", Replacement: "discovery.k8s.io/v1"},
	{GroupVersion: "events.k8s.io/v1beta1", Kind: "Event", DeprecatedIn: "1.21", RemovedIn: "1.25", Replacement: "events.k8s.io/v1"},
	{GroupVersion: "policy/v1beta1", Kind: "PodDisruptionBudget", DeprecatedIn: "1.21", RemovedIn: "1.25", Replacement: "policy/v1"},
	{GroupVersion: "policy/v1beta1", Kind: "PodSecurityPolicy", DeprecatedIn: "1.21", RemovedIn: "1.25", Replacement: "(removed, use Pod Security Admission)"},
	{GroupVersion: "node.k8s.io/v1beta1", Kind: "RuntimeClass", DeprecatedIn: "1.22", RemovedIn: "1.25", Replacement: "node.k8s.io/v1"},

	// ── Removed in 1.26 ───────────────────────────────────────────────
	{GroupVersion: "autoscaling/v2beta2", Kind: "HorizontalPodAutoscaler", DeprecatedIn: "1.23", RemovedIn: "1.26", Replacement: "autoscaling/v2"},
	{GroupVersion: "flowcontrol.apiserver.k8s.io/v1beta1", Kind: "", DeprecatedIn: "1.23", RemovedIn: "1.26", Replacement: "flowcontrol.apiserver.k8s.io/v1beta3"},

	// ── Removed in 1.27 ───────────────────────────────────────────────
	{GroupVersion: "storage.k8s.io/v1beta1", Kind: "CSIStorageCapacity", DeprecatedIn: "1.24", RemovedIn: "1.27", Replacement: "storage.k8s.io/v1"},

	// ── Removed in 1.29 ───────────────────────────────────────────────
	{GroupVersion: "flowcontrol.apiserver.k8s.io/v1beta2", Kind: "", DeprecatedIn: "1.26", RemovedIn: "1.29", Replacement: "flowcontrol.apiserver.k8s.io/v1"},

	// ── Removed in 1.32 ───────────────────────────────────────────────
	{GroupVersion: "flowcontrol.apiserver.k8s.io/v1beta3", Kind: "", DeprecatedIn: "1.29", RemovedIn: "1.32", Replacement: "flowcontrol.apiserver.k8s.io/v1"},
}

// DeprecationsByGroupVersion indexes the deprecation table by group/version for fast lookup.
func DeprecationsByGroupVersion() map[string][]DeprecationEntry {
	m := make(map[string][]DeprecationEntry)
	for _, d := range DeprecationTable {
		m[d.GroupVersion] = append(m[d.GroupVersion], d)
	}
	return m
}
