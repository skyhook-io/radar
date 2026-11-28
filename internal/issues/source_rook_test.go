package issues

import (
	"strings"
	"testing"
	"time"

	issuesapi "github.com/skyhook-io/radar/pkg/issuesapi"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

var (
	cephClusterGVR   = schema.GroupVersionResource{Group: "ceph.rook.io", Version: "v1", Resource: "cephclusters"}
	cephBlockPoolGVR = schema.GroupVersionResource{Group: "ceph.rook.io", Version: "v1", Resource: "cephblockpools"}
	cephNow          = time.Date(2026, 11, 20, 12, 0, 0, 0, time.UTC)
)

func cephCluster(status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1", "kind": "CephCluster",
		"metadata": map[string]any{"name": "rook-ceph", "namespace": "rook-ceph", "creationTimestamp": "2026-11-01T10:00:00Z"},
		"status":   status,
	}}
}

// Shaped like what Rook writes after `ceph status` reports a down monitor.
func cephWarnStatus() map[string]any {
	return map[string]any{
		"phase":      "Ready",
		"conditions": []any{map[string]any{"type": "Ready", "status": "True", "reason": "ClusterCreated"}},
		"ceph": map[string]any{
			"health": "HEALTH_WARN", "previousHealth": "HEALTH_OK",
			"lastChecked": "2026-11-20T11:59:30Z", "lastChanged": "2026-11-20T11:40:00Z",
			"details": map[string]any{
				"MON_DOWN":           map[string]any{"severity": "HEALTH_WARN", "message": "1/3 mons down, quorum a,b"},
				"PG_DEGRADED":        map[string]any{"severity": "HEALTH_WARN", "message": "Degraded data redundancy: 12/360 objects degraded"},
				"RECENT_CRASH":       map[string]any{"severity": "HEALTH_WARN", "message": "1 daemons have recently crashed"},
				"OSD_DOWN":           map[string]any{"severity": "HEALTH_WARN", "message": "1 osds down"},
				"OSD_HOST_DOWN":      map[string]any{"severity": "HEALTH_WARN", "message": "1 host (1 osds) down"},
				"POOL_NO_REDUNDANCY": map[string]any{"severity": "HEALTH_WARN", "message": "1 pool(s) have no replicas configured"},
			},
		},
	}
}

func TestRookCephClusterHealthIssues(t *testing.T) {
	got := RookCephClusterIssues(cephClusterGVR, cephCluster(cephWarnStatus()), cephNow)
	if len(got) != 1 {
		t.Fatalf("got %d issues: %+v", len(got), got)
	}
	iss := got[0]
	if iss.Reason != ReasonCephHealthWarning || iss.Severity != SeverityWarning || iss.Kind != "CephCluster" || iss.Group != "ceph.rook.io" || iss.Namespace != "rook-ceph" {
		t.Fatalf("got %+v", iss)
	}
	if !strings.HasPrefix(iss.Message, "Ceph reports HEALTH_WARN: MON_DOWN (1/3 mons down, quorum a,b); OSD_DOWN (1 osds down)") || !strings.HasSuffix(iss.Message, "; and 1 more") {
		t.Fatalf("message = %q", iss.Message)
	}
	if iss.Category != issuesapi.CategoryOperatorConditionFail {
		t.Fatalf("category = %q", iss.Category)
	}
	if want := time.Date(2026, 11, 20, 11, 40, 0, 0, time.UTC); iss.OnsetUnknown || !iss.FirstSeen.Equal(want) {
		t.Fatalf("onset = %v (unknown %v), want lastChanged %v", iss.FirstSeen, iss.OnsetUnknown, want)
	}

	status := cephWarnStatus()
	ceph := status["ceph"].(map[string]any)
	ceph["health"] = "HEALTH_ERR"
	delete(ceph, "lastChanged")
	ceph["details"] = map[string]any{"error": map[string]any{"severity": "Urgent", "message": "failed to get status. . timed out"}}
	got = RookCephClusterIssues(cephClusterGVR, cephCluster(status), cephNow)
	if len(got) != 1 || got[0].Reason != ReasonCephHealthError || got[0].Severity != SeverityCritical || !got[0].OnsetUnknown || got[0].Message != "Ceph reports HEALTH_ERR: error (failed to get status. . timed out)" {
		t.Fatalf("got %+v", got)
	}
}

func TestRookCephClusterIssuesIgnoreNonCurrentHealth(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*unstructured.Unstructured)
	}{
		{"healthy", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "HEALTH_OK", "status", "ceph", "health")
		}},
		{"stale", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, "2026-11-20T11:00:00Z", "status", "ceph", "lastChecked")
		}},
		{"checks disabled", func(u *unstructured.Unstructured) {
			_ = unstructured.SetNestedField(u.Object, true, "spec", "healthCheck", "daemonHealth", "status", "disabled")
		}},
		{"no ceph status", func(u *unstructured.Unstructured) { unstructured.RemoveNestedField(u.Object, "status", "ceph") }},
		{"no status", func(u *unstructured.Unstructured) { unstructured.RemoveNestedField(u.Object, "status") }},
		{"terminating", func(u *unstructured.Unstructured) { now := metav1.Now(); u.SetDeletionTimestamp(&now) }},
		{"other kind", func(u *unstructured.Unstructured) { u.SetKind("CephFilesystem") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			u := cephCluster(cephWarnStatus())
			tc.change(u)
			if got := RookCephClusterIssues(cephClusterGVR, u, cephNow); len(got) != 0 {
				t.Fatalf("unexpected issues: %+v", got)
			}
		})
	}
}

func TestRookCephClusterReconcileFailure(t *testing.T) {
	status := map[string]any{
		"phase": "Progressing", "state": "Error", "message": "failed to create cluster: failed to start ceph monitors",
		"conditions": []any{
			map[string]any{"type": "Ready", "status": "True", "reason": "ClusterCreated", "message": "Cluster created successfully"},
			map[string]any{"type": "Progressing", "status": "False", "reason": "ClusterProgressing", "message": "failed to create cluster: failed to start ceph monitors", "lastTransitionTime": "2026-11-20T11:59:00Z"},
		},
		"ceph": map[string]any{"health": "HEALTH_OK", "lastChecked": "2026-11-20T11:59:30Z"},
	}
	got := RookCephClusterIssues(cephClusterGVR, cephCluster(status), cephNow)
	if len(got) != 1 {
		t.Fatalf("got %d issues: %+v", len(got), got)
	}
	status["ceph"].(map[string]any)["health"] = "HEALTH_WARN"
	both := RookCephClusterIssues(cephClusterGVR, cephCluster(status), cephNow)
	if len(both) != 2 || both[0].ID == both[1].ID {
		t.Fatalf("health and reconcile findings must keep distinct IDs: %+v", both)
	}
	status["ceph"].(map[string]any)["health"] = "HEALTH_OK"
	if iss := got[0]; iss.Reason != ReasonCephClusterReconcileFailed || iss.Severity != SeverityWarning || !iss.OnsetUnknown ||
		iss.Message != "Rook's last reconcile of this CephCluster failed; it keeps retrying: failed to create cluster: failed to start ceph monitors" {
		t.Fatalf("got %+v", iss)
	}

	// A running reconcile is not a failure.
	conditions := status["conditions"].([]any)
	conditions[1].(map[string]any)["status"] = "True"
	if got := RookCephClusterIssues(cephClusterGVR, cephCluster(status), cephNow); len(got) != 0 {
		t.Fatalf("unexpected issues: %+v", got)
	}
}

func cephBlockPool(status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "ceph.rook.io/v1", "kind": "CephBlockPool",
		"metadata": map[string]any{"name": "replicapool", "namespace": "rook-ceph"},
		"spec":     map[string]any{"failureDomain": "host", "replicated": map[string]any{"size": int64(3)}},
		"status":   status,
	}}
}

func TestRookCephBlockPoolIssues(t *testing.T) {
	failed := cephBlockPool(map[string]any{"phase": "Failure", "message": "Failed to create the block pool. See the operator log for details."})
	got := RookCephBlockPoolIssues(cephBlockPoolGVR, failed)
	if len(got) != 1 || got[0].Reason != ReasonCephBlockPoolFailure || got[0].Severity != SeverityWarning || !got[0].OnsetUnknown ||
		got[0].Message != "Rook failed to configure this block pool and keeps retrying: Failed to create the block pool. See the operator log for details." {
		t.Fatalf("got %+v", got)
	}
	for _, status := range []map[string]any{{"phase": "Ready"}, {"phase": "Progressing"}, nil} {
		if got := RookCephBlockPoolIssues(cephBlockPoolGVR, cephBlockPool(status)); len(got) != 0 {
			t.Fatalf("status %v: unexpected issues %+v", status, got)
		}
	}
	now := metav1.Now()
	failed.SetDeletionTimestamp(&now)
	if got := RookCephBlockPoolIssues(cephBlockPoolGVR, failed); len(got) != 0 {
		t.Fatalf("terminating pool: unexpected issues %+v", got)
	}
}

func TestGenericCRDPassRoutesRookKinds(t *testing.T) {
	// An Available=False condition the generic pass would report must not add a second finding.
	status := cephWarnStatus()
	status["conditions"] = []any{map[string]any{"type": "Available", "status": "False", "reason": "Unexpected"}}
	cluster := cephCluster(status)
	_ = unstructured.SetNestedField(cluster.Object, time.Now().UTC().Format(time.RFC3339), "status", "ceph", "lastChecked")
	pool := cephBlockPool(map[string]any{"phase": "Failure", "message": "Failed to create the block pool."})
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			cephClusterGVR:   {cluster},
			cephBlockPoolGVR: {pool},
		},
		kinds:      map[schema.GroupVersionResource]string{cephClusterGVR: "CephCluster", cephBlockPoolGVR: "CephBlockPool"},
		namespaced: map[schema.GroupVersionResource]bool{cephClusterGVR: true, cephBlockPoolGVR: true},
	}
	got := detectGenericCRDIssues(p, Filters{}, nil, nil)
	reasons := map[string]int{}
	for _, iss := range got {
		reasons[iss.Reason]++
	}
	if len(got) != 2 || reasons[ReasonCephHealthWarning] != 1 || reasons[ReasonCephBlockPoolFailure] != 1 {
		t.Fatalf("got %+v", got)
	}
}
