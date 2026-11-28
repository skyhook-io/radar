package rook

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type statusCase struct {
	Name   string         `json:"name"`
	Want   string         `json:"want"`
	Object map[string]any `json:"object"`
}

type statusFile struct {
	Now      time.Time    `json:"now"`
	Clusters []statusCase `json:"clusters"`
	Pools    []statusCase `json:"pools"`
}

func loadStatusCases(t *testing.T) statusFile {
	t.Helper()
	raw, err := os.ReadFile("testdata/status.json")
	if err != nil {
		t.Fatal(err)
	}
	var file statusFile
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	return file
}

func caseObject(kind string, c statusCase) *unstructured.Unstructured {
	obj := map[string]any{"apiVersion": "ceph.rook.io/v1", "kind": kind}
	for k, v := range c.Object {
		obj[k] = v
	}
	metadata, _ := obj["metadata"].(map[string]any)
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["name"], metadata["namespace"] = c.Name, "rook-ceph"
	obj["metadata"] = metadata
	return &unstructured.Unstructured{Object: obj}
}

func TestStatusFixtures(t *testing.T) {
	file := loadStatusCases(t)
	if len(file.Clusters) == 0 || len(file.Pools) == 0 {
		t.Fatal("no cases")
	}
	for _, c := range file.Clusters {
		if got := ClusterStatus(caseObject("CephCluster", c), file.Now); got != c.Want {
			t.Errorf("cluster %s: got %q, want %q", c.Name, got, c.Want)
		}
	}
	for _, c := range file.Pools {
		if got := BlockPoolStatus(caseObject("CephBlockPool", c)); got != c.Want {
			t.Errorf("pool %s: got %q, want %q", c.Name, got, c.Want)
		}
	}
}

func fixture(t *testing.T, kind, name string) (*unstructured.Unstructured, time.Time) {
	t.Helper()
	file := loadStatusCases(t)
	cases := file.Clusters
	if kind == "CephBlockPool" {
		cases = file.Pools
	}
	for _, c := range cases {
		if c.Name == name {
			return caseObject(kind, c), file.Now
		}
	}
	t.Fatalf("no %s case %q", kind, name)
	return nil, time.Time{}
}

func TestClusterHealthChecksOrderErrorsFirst(t *testing.T) {
	obj, now := fixture(t, "CephCluster", "error")
	h := ClusterHealth(obj, now)
	if !h.Current() || h.Status != "HEALTH_ERR" {
		t.Fatalf("got %+v", h)
	}
	if len(h.Checks) != 2 || h.Checks[0].Code != "OSD_FULL" || h.Checks[1].Code != "POOL_FULL" || h.Checks[0].Message != "1 full osd(s)" {
		t.Fatalf("checks = %+v", h.Checks)
	}
}

func TestClusterHealthTimes(t *testing.T) {
	obj, now := fixture(t, "CephCluster", "warning")
	h := ClusterHealth(obj, now)
	if !h.LastChanged.Equal(time.Date(2026, 11, 20, 11, 40, 0, 0, time.UTC)) || h.Previous != "HEALTH_OK" {
		t.Fatalf("got %+v", h)
	}
	if !h.LastChecked.Equal(time.Date(2026, 11, 20, 11, 59, 30, 0, time.UTC)) {
		t.Fatalf("lastChecked = %v", h.LastChecked)
	}
}

func TestStaleAfter(t *testing.T) {
	for _, tc := range []struct {
		interval any
		want     time.Duration
	}{
		{nil, 10 * time.Minute},
		{"60s", 10 * time.Minute},
		{"30m0s", 90 * time.Minute},
		{"not a duration", 10 * time.Minute},
		{"-5m", 10 * time.Minute},
	} {
		obj := &unstructured.Unstructured{Object: map[string]any{}}
		if tc.interval != nil {
			_ = unstructured.SetNestedField(obj.Object, tc.interval, "spec", "healthCheck", "daemonHealth", "status", "interval")
		}
		if got := StaleAfter(obj); got != tc.want {
			t.Errorf("interval %v: got %v, want %v", tc.interval, got, tc.want)
		}
	}
}

func TestClusterReconcileFailure(t *testing.T) {
	obj, _ := fixture(t, "CephCluster", "reconcile-failed-health-ok")
	f, failed := ClusterReconcileFailure(obj)
	if !failed || f.Message != "failed to create cluster: failed to start ceph monitors" {
		t.Fatalf("got %+v %v", f, failed)
	}
	for _, name := range []string{"healthy", "reconciling-health-ok", "creating", "no-status"} {
		obj, _ := fixture(t, "CephCluster", name)
		if _, failed := ClusterReconcileFailure(obj); failed {
			t.Errorf("%s: reported a failed reconcile", name)
		}
	}
}

func TestBlockPoolDeletionBlocked(t *testing.T) {
	obj, _ := fixture(t, "CephBlockPool", "deletion-blocked")
	got := BlockPoolDeletionBlocked(obj)
	if len(got) != 1 || got[0] != `pool "replicapool" contains images and/or snapshots` {
		t.Fatalf("got %q", got)
	}
}

func TestBlockPoolProtection(t *testing.T) {
	for _, tc := range []struct {
		spec map[string]any
		want string
	}{
		{map[string]any{"replicated": map[string]any{"size": int64(3)}}, "Replicated, size 3"},
		{map[string]any{"erasureCoded": map[string]any{"dataChunks": int64(2), "codingChunks": int64(1)}}, "Erasure coded, 2+1"},
		// An erasure-coded pool still carries the defaulted replicated block.
		{map[string]any{"replicated": map[string]any{"size": int64(0)}, "erasureCoded": map[string]any{"dataChunks": int64(4), "codingChunks": int64(2)}}, "Erasure coded, 4+2"},
		{map[string]any{}, ""},
	} {
		obj := &unstructured.Unstructured{Object: map[string]any{"spec": tc.spec}}
		if got := BlockPoolProtection(obj); got != tc.want {
			t.Errorf("spec %v: got %q, want %q", tc.spec, got, tc.want)
		}
	}
}
