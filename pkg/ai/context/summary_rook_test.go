package context

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func rookObject(apiVersion, kind string, spec, status map[string]any) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": "rook-ceph", "namespace": "rook-ceph"},
	}
	if spec != nil {
		obj["spec"] = spec
	}
	if status != nil {
		obj["status"] = status
	}
	return &unstructured.Unstructured{Object: obj}
}

func TestCephClusterSummary(t *testing.T) {
	checked := time.Now().Add(-30 * time.Second).UTC().Format(time.RFC3339)
	capacity := map[string]any{"bytesTotal": int64(3 << 40), "bytesUsed": int64(1 << 40), "bytesAvailable": int64(2 << 40)}
	for _, tc := range []struct {
		name                                string
		spec, status                        map[string]any
		wantStatus, wantIssue, wantCapacity string
	}{
		{"healthy", nil, map[string]any{"phase": "Ready", "ceph": map[string]any{"health": "HEALTH_OK", "lastChecked": checked, "capacity": capacity}},
			"HEALTH_OK", "", "1.0 TiB used of 3.0 TiB"},
		{"warning", nil, map[string]any{"phase": "Ready", "ceph": map[string]any{"health": "HEALTH_WARN", "lastChecked": checked}},
			"HEALTH_WARN", "CephHealthWarning", ""},
		{"error", nil, map[string]any{"phase": "Ready", "ceph": map[string]any{"health": "HEALTH_ERR", "lastChecked": checked}},
			"HEALTH_ERR", "CephHealthError", ""},
		{"reconcile failed", nil, map[string]any{"phase": "Progressing", "message": "failed to start ceph monitors",
			"conditions": []any{map[string]any{"type": "Progressing", "status": "False"}},
			"ceph":       map[string]any{"health": "HEALTH_OK", "lastChecked": checked}},
			"Reconcile failed", "CephClusterReconcileFailed", ""},
		// A stale report keeps neither its health hint nor its capacity.
		{"stale error", nil, map[string]any{"phase": "Ready", "ceph": map[string]any{"health": "HEALTH_ERR", "lastChecked": "2020-01-01T00:00:00Z", "capacity": capacity}},
			"Health stale", "", ""},
		{"checks disabled", map[string]any{"healthCheck": map[string]any{"daemonHealth": map[string]any{"status": map[string]any{"disabled": true}}}},
			map[string]any{"phase": "Ready", "ceph": map[string]any{"health": "HEALTH_WARN", "lastChecked": checked}},
			"Health checks disabled", "", ""},
		{"creating", nil, map[string]any{"phase": "Progressing"}, "Progressing", "", ""},
		{"no status", nil, nil, "Not reported", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := summarizeUnstructured(rookObject("ceph.rook.io/v1", "CephCluster", tc.spec, tc.status))
			if s.Kind != "CephCluster" || s.Status != tc.wantStatus || s.Issue != tc.wantIssue || s.Capacity != tc.wantCapacity {
				t.Fatalf("got status=%q issue=%q capacity=%q", s.Status, s.Issue, s.Capacity)
			}
		})
	}
}

func TestCephClusterSummaryFields(t *testing.T) {
	s := summarizeUnstructured(rookObject("ceph.rook.io/v1", "CephCluster",
		map[string]any{"external": map[string]any{"enable": true}},
		map[string]any{"phase": "Connected", "version": map[string]any{"version": "19.2.3-0", "image": "quay.io/ceph/ceph:v19.2.3"}}))
	if s.Ready != "Connected" || s.Version != "19.2.3-0" || s.Type != "External" || s.Status != "Connected" {
		t.Fatalf("got %+v", s)
	}
}

func TestCephBlockPoolSummary(t *testing.T) {
	replicated := rookObject("ceph.rook.io/v1", "CephBlockPool",
		map[string]any{"failureDomain": "zone", "replicated": map[string]any{"size": int64(3)}},
		map[string]any{"phase": "Ready", "info": map[string]any{"type": "Replicated", "failureDomain": "zone"}})
	s := summarizeUnstructured(replicated)
	if s.Kind != "CephBlockPool" || s.Status != "Ready" || s.Type != "Replicated, size 3" || s.Strategy != "failureDomain=zone" || s.Issue != "" {
		t.Fatalf("got %+v", s)
	}

	ec := rookObject("ceph.rook.io/v1", "CephBlockPool",
		map[string]any{"erasureCoded": map[string]any{"dataChunks": int64(2), "codingChunks": int64(1)}},
		map[string]any{"phase": "Failure", "message": "Failed to create the block pool.", "info": map[string]any{"failureDomain": "host"}})
	s = summarizeUnstructured(ec)
	if s.Status != "Failure" || s.Issue != "CephBlockPoolFailure" || s.Type != "Erasure coded, 2+1" || s.Strategy != "failureDomain=host" {
		t.Fatalf("got %+v", s)
	}

	if s := summarizeUnstructured(rookObject("ceph.rook.io/v1", "CephBlockPool", nil, nil)); s.Status != "Not reported" {
		t.Fatalf("got %+v", s)
	}
}

func TestRookSummaryClaimsOnlyItsGroup(t *testing.T) {
	status := map[string]any{"phase": "Failure", "ceph": map[string]any{"health": "HEALTH_ERR", "lastChecked": time.Now().UTC().Format(time.RFC3339)}}
	for _, apiVersion := range []string{"fake.ceph.rook.io/v1", "ceph.example.com/v1"} {
		for _, kind := range []string{"CephCluster", "CephBlockPool"} {
			s := summarizeUnstructured(rookObject(apiVersion, kind, nil, status))
			raw, _ := json.Marshal(s)
			if strings.Contains(string(raw), "CephHealthError") || strings.Contains(string(raw), "CephBlockPoolFailure") || s.Status == "HEALTH_ERR" {
				t.Fatalf("%s %s got a Rook summary: %s", apiVersion, kind, raw)
			}
		}
	}
}
