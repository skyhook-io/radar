package issues

import (
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/longhorn"
)

const (
	ReasonLonghornVolumeFaulted  = "LonghornVolumeFaulted"
	ReasonLonghornVolumeDegraded = "LonghornVolumeDegraded"
)

// detectLonghornVolumeIssues reports faulted volumes (critical: no usable
// replica) and degraded ones (warning: fewer healthy replicas than requested,
// which Longhorn reports whether or not it can place a new one, so the Issue
// claims no rebuild). A detached volume is not a problem. The onset of a
// degraded volume is status.lastDegradedAt when Longhorn recorded it.
func detectLonghornVolumeIssues(gvr schema.GroupVersionResource, kind string, items []*unstructured.Unstructured) []Issue {
	var out []Issue
	for _, u := range items {
		switch longhorn.VolumeHealth(u) {
		case longhorn.HealthFaulted:
			out = append(out, newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), SeverityCritical,
				ReasonLonghornVolumeFaulted,
				"No replica of this volume is usable. Longhorn can't serve its data until a replica is salvaged or the volume is restored from a backup.",
				time.Time{}, false, "", u.GetCreationTimestamp().Time))
		case longhorn.HealthDegraded:
			raw, _, _ := unstructured.NestedString(u.Object, "status", "lastDegradedAt")
			at, err := time.Parse(time.RFC3339, raw)
			out = append(out, newConditionIssue(gvr, kind, u.GetNamespace(), u.GetName(), SeverityWarning,
				ReasonLonghornVolumeDegraded,
				"Fewer healthy replicas than requested. Longhorn rebuilds them when it can place a replica, which is routine after a node drain or restart; if it lasts, check whether a replica can be scheduled (node disks, scheduling settings).",
				at, err == nil, "", u.GetCreationTimestamp().Time))
		}
	}
	return out
}

// longhornSuppressesGenericConditions: Longhorn copies the Kubernetes node's
// state into its own Node's Ready condition (reasons KubernetesNode*). The
// core Node already has that Issue; Longhorn's own reasons (its manager pod
// down, no mount propagation) are kept.
func longhornSuppressesGenericConditions(group, kind string, u *unstructured.Unstructured) bool {
	if group != longhorn.Group || kind != "Node" {
		return false
	}
	conds, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, c := range conds {
		m, _ := c.(map[string]any)
		if m["type"] == "Ready" && m["status"] == "False" {
			reason, _ := m["reason"].(string)
			return strings.HasPrefix(reason, "KubernetesNode")
		}
	}
	return false
}
