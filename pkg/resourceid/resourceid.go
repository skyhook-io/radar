// Package resourceid holds the neutral, dependency-free resource-identity
// primitives shared across the platform: the canonical index key and the
// built-in Kind→Group table. It is a leaf package (no internal/ or other pkg/
// imports), so the identity foundations (pkg/subject, internal/issues) and the
// audit suite can all depend on it WITHOUT depending on each other — audit must
// not be the identity foundation for the product.
package resourceid

import "fmt"

// ResourceKey returns the index key for a resource:
// "group|Kind|namespace|name". Group goes first because both group and
// namespace can legitimately be empty independently — encoding group last would
// leave a cluster-scoped CRD key ambiguous with a namespaced core-group key
// under any 3-part parse. "|" is a safe delimiter — Kubernetes API groups follow
// DNS subdomain rules and can't contain it.
func ResourceKey(group, kind, namespace, name string) string {
	return fmt.Sprintf("%s|%s|%s|%s", group, kind, namespace, name)
}

// BuiltinGroup returns the canonical API group for a built-in Kubernetes Kind.
// The boolean distinguishes core-group kinds from unrecognized custom kinds.
func BuiltinGroup(kind string) (string, bool) {
	switch kind {
	case "Pod", "Service", "ConfigMap", "Secret", "Node", "Namespace",
		"PersistentVolume", "PersistentVolumeClaim", "ServiceAccount", "Event",
		"LimitRange", "ResourceQuota", "Endpoints":
		return "", true
	case "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet":
		return "apps", true
	case "Job", "CronJob":
		return "batch", true
	case "HorizontalPodAutoscaler":
		return "autoscaling", true
	case "Ingress", "IngressClass", "NetworkPolicy":
		return "networking.k8s.io", true
	case "PodDisruptionBudget":
		return "policy", true
	case "StorageClass", "VolumeAttachment":
		return "storage.k8s.io", true
	case "EndpointSlice":
		return "discovery.k8s.io", true
	case "Role", "ClusterRole", "RoleBinding", "ClusterRoleBinding":
		return "rbac.authorization.k8s.io", true
	case "PriorityClass":
		return "scheduling.k8s.io", true
	case "RuntimeClass":
		return "node.k8s.io", true
	case "Lease":
		return "coordination.k8s.io", true
	case "MutatingWebhookConfiguration", "ValidatingWebhookConfiguration":
		return "admissionregistration.k8s.io", true
	}
	return "", false
}

// GroupForBuiltinKind maps a built-in Kubernetes Kind to its API group. Returns
// "" for both core-group built-ins and unrecognized kinds; callers that must
// distinguish those cases should use BuiltinGroup.
func GroupForBuiltinKind(kind string) string {
	group, _ := BuiltinGroup(kind)
	return group
}

// BuiltinAPIVersion returns the stable API version used by Radar's typed
// informer for a built-in Kind.
func BuiltinAPIVersion(kind string) (string, bool) {
	group, ok := BuiltinGroup(kind)
	if !ok {
		return "", false
	}
	version := "v1"
	if kind == "HorizontalPodAutoscaler" {
		version = "v2"
	}
	if group == "" {
		return version, true
	}
	return group + "/" + version, true
}
