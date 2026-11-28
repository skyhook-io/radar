package context

import (
	"fmt"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/rook"
)

func summarizeCephCluster(obj *unstructured.Unstructured) *ResourceSummary {
	now := time.Now()
	s := &ResourceSummary{
		Kind:      "CephCluster",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
		Status:    rook.ClusterStatus(obj, now),
	}
	s.Ready, _, _ = unstructured.NestedString(obj.Object, "status", "phase")
	s.Version, _, _ = unstructured.NestedString(obj.Object, "status", "version", "version")
	if external, _, _ := unstructured.NestedBool(obj.Object, "spec", "external", "enable"); external {
		s.Type = "External"
	}
	h := rook.ClusterHealth(obj, now)
	switch {
	case h.Current() && h.Status == "HEALTH_ERR":
		s.Issue = "CephHealthError"
	case h.Current() && h.Status == "HEALTH_WARN":
		s.Issue = "CephHealthWarning"
	}
	if _, failed := rook.ClusterReconcileFailure(obj); failed && s.Issue == "" {
		s.Issue = "CephClusterReconcileFailed"
	}
	// Rook keeps the last capacity when a check reads none, so it is only as
	// current as the health report.
	if h.Current() {
		used, _, _ := unstructured.NestedInt64(obj.Object, "status", "ceph", "capacity", "bytesUsed")
		total, _, _ := unstructured.NestedInt64(obj.Object, "status", "ceph", "capacity", "bytesTotal")
		if total > 0 && used >= 0 {
			s.Capacity = fmt.Sprintf("%s used of %s", cephBytes(used), cephBytes(total))
		}
	}
	return s
}

func summarizeCephBlockPool(obj *unstructured.Unstructured) *ResourceSummary {
	s := &ResourceSummary{
		Kind:      "CephBlockPool",
		Name:      obj.GetName(),
		Namespace: obj.GetNamespace(),
		Age:       age(obj.GetCreationTimestamp().Time),
		Status:    rook.BlockPoolStatus(obj),
		Type:      rook.BlockPoolProtection(obj),
	}
	// Rook resolves an unset failure domain to its default and records it in
	// status.info.
	domain, _, _ := unstructured.NestedString(obj.Object, "spec", "failureDomain")
	if domain == "" {
		domain, _, _ = unstructured.NestedString(obj.Object, "status", "info", "failureDomain")
	}
	if domain != "" {
		s.Strategy = "failureDomain=" + domain
	}
	if s.Status == "Failure" {
		s.Issue = "CephBlockPoolFailure"
	}
	return s
}

func cephBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	value, suffix := float64(n), ""
	for _, next := range []string{"KiB", "MiB", "GiB", "TiB", "PiB", "EiB"} {
		value /= unit
		suffix = next
		if value < unit {
			break
		}
	}
	return fmt.Sprintf("%.1f %s", value, suffix)
}
