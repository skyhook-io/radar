package issues

import (
	"fmt"
	"strings"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/rook"
)

const (
	ReasonCephHealthError            = "CephHealthError"
	ReasonCephHealthWarning          = "CephHealthWarning"
	ReasonCephClusterReconcileFailed = "CephClusterReconcileFailed"
	ReasonCephBlockPoolFailure       = "CephBlockPoolFailure"
)

// One CephCluster can carry both findings under the same category; distinct
// fingerprints keep them apart in the Issue ID. Ceph health keeps one
// fingerprint across HEALTH_WARN and HEALTH_ERR, so a worsening cluster stays
// one Issue.
const (
	cephHealthFingerprint    = "ceph-health"
	rookReconcileFingerprint = "rook-reconcile"
)

// maxCephHealthChecks bounds how many Ceph health checks one message quotes.
const maxCephHealthChecks = 5

// RookCephClusterIssues reports the Ceph health Rook last copied to a
// CephCluster, and a failed reconcile. Only a current report counts: a stale
// one (Rook stopped refreshing it) or one left behind after health checks
// were disabled says nothing about now, and the drawer shows it as such.
// HEALTH_ERR is critical; HEALTH_WARN is a warning, since Ceph raises it for
// routine states such as a rebalance as well as for real risk. The onset is
// status.ceph.lastChanged, when Rook saw the health change to its current
// value; Rook doesn't record it for the first report.
//
// A failed reconcile is reported separately: Ceph can be healthy while Rook
// can't apply the spec. Rook rewrites the condition on every retry, so its
// time is not the onset.
func RookCephClusterIssues(gvr schema.GroupVersionResource, u *unstructured.Unstructured, now time.Time) []Issue {
	if u == nil || u.GetKind() != "CephCluster" || u.GetDeletionTimestamp() != nil {
		return nil
	}
	var out []Issue
	created := u.GetCreationTimestamp().Time
	if h := rook.ClusterHealth(u, now); h.Current() && (h.Status == "HEALTH_ERR" || h.Status == "HEALTH_WARN") {
		severity, reason := SeverityWarning, ReasonCephHealthWarning
		if h.Status == "HEALTH_ERR" {
			severity, reason = SeverityCritical, ReasonCephHealthError
		}
		out = append(out, newConditionIssue(gvr, "CephCluster", u.GetNamespace(), u.GetName(), severity, reason,
			cephHealthMessage(h), h.LastChanged, !h.LastChanged.IsZero(), cephHealthFingerprint, created))
	}
	if f, failed := rook.ClusterReconcileFailure(u); failed {
		message := "Rook's last reconcile of this CephCluster failed; it keeps retrying"
		if f.Message != "" {
			message += ": " + f.Message
		}
		out = append(out, newConditionIssue(gvr, "CephCluster", u.GetNamespace(), u.GetName(), SeverityWarning,
			ReasonCephClusterReconcileFailed, message, time.Time{}, false, rookReconcileFingerprint, created))
	}
	out, _ = filterShapedIssues(out, Filters{})
	return out
}

func cephHealthMessage(h rook.Health) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Ceph reports %s", h.Status)
	if len(h.Checks) == 0 {
		return b.String()
	}
	b.WriteString(": ")
	for i, check := range h.Checks {
		if i == maxCephHealthChecks {
			fmt.Fprintf(&b, "; and %d more", len(h.Checks)-maxCephHealthChecks)
			break
		}
		if i > 0 {
			b.WriteString("; ")
		}
		b.WriteString(check.Code)
		if check.Message != "" {
			b.WriteString(" (" + check.Message + ")")
		}
	}
	return b.String()
}

// RookCephBlockPoolIssues reports a CephBlockPool Rook failed to configure
// (phase Failure). Rook records no time with the phase, so the onset is
// unknown. Placement-group health is the CephCluster's, not the pool's.
func RookCephBlockPoolIssues(gvr schema.GroupVersionResource, u *unstructured.Unstructured) []Issue {
	if u == nil || u.GetKind() != "CephBlockPool" || u.GetDeletionTimestamp() != nil {
		return nil
	}
	if phase, _, _ := unstructured.NestedString(u.Object, "status", "phase"); phase != "Failure" {
		return nil
	}
	message := "Rook failed to configure this block pool and keeps retrying"
	if detail, _, _ := unstructured.NestedString(u.Object, "status", "message"); strings.TrimSpace(detail) != "" {
		message += ": " + strings.TrimSpace(detail)
	}
	out, _ := filterShapedIssues([]Issue{newConditionIssue(gvr, "CephBlockPool", u.GetNamespace(), u.GetName(), SeverityWarning,
		ReasonCephBlockPoolFailure, message, time.Time{}, false, "", u.GetCreationTimestamp().Time)}, Filters{})
	return out
}
