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

// GroupForBuiltinKind maps a built-in Kubernetes Kind to its API group. Returns
// "" for kinds it doesn't recognize (core-group built-ins and unrecognized
// kinds both return ""). Keeps the Kind→Group mapping in one place rather than
// at every emission site; the audit suite, the issues classifier, and any other
// consumer share it so they can't drift.
func GroupForBuiltinKind(kind string) string {
	switch kind {
	case "Pod", "Service", "ConfigMap", "Secret", "Node", "Namespace",
		"PersistentVolume", "PersistentVolumeClaim", "ServiceAccount":
		return ""
	case "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet":
		return "apps"
	case "Job", "CronJob":
		return "batch"
	case "HorizontalPodAutoscaler":
		return "autoscaling"
	case "Ingress", "NetworkPolicy":
		return "networking.k8s.io"
	case "PodDisruptionBudget":
		return "policy"
	}
	return ""
}
