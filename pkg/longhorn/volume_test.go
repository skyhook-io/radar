package longhorn

import (
	"encoding/json"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestVolumeHealthFixtures(t *testing.T) {
	raw, err := os.ReadFile("testdata/volumes.json")
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Cases []struct {
			Name   string         `json:"name"`
			Status map[string]any `json:"status"`
			Want   Health         `json:"want"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(raw, &file); err != nil {
		t.Fatal(err)
	}
	for _, c := range file.Cases {
		obj := map[string]any{"apiVersion": "longhorn.io/v1beta2", "kind": "Volume"}
		if c.Status != nil {
			obj["status"] = c.Status
		}
		if got := VolumeHealth(&unstructured.Unstructured{Object: obj}); got != c.Want {
			t.Errorf("%s: VolumeHealth = %q, want %q", c.Name, got, c.Want)
		}
	}
}

func engine(status map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": "longhorn.io/v1beta2", "kind": "Engine", "status": status}}
}

func TestHealthyReplicas(t *testing.T) {
	n, ok := HealthyReplicas(engine(map[string]any{"replicaModeMap": map[string]any{"r-1": "RW", "r-2": "WO", "r-3": "ERR", "r-4": "RW"}}))
	if !ok || n != 2 {
		t.Errorf("HealthyReplicas = %d, %v; want 2, true (only RW counts)", n, ok)
	}
	if _, ok := HealthyReplicas(nil); ok {
		t.Error("nil engine: ok = true, want unknown")
	}
	if _, ok := HealthyReplicas(engine(map[string]any{"currentState": "running"})); ok {
		t.Error("engine without replicaModeMap: ok = true, want unknown")
	}
}

func TestRebuild(t *testing.T) {
	got := Rebuild(engine(map[string]any{"rebuildStatus": map[string]any{
		"tcp://10.0.0.2:10000": map[string]any{"isRebuilding": true, "progress": int64(42), "state": "in_progress", "fromReplicaAddress": "tcp://10.0.0.1:10000"},
		"tcp://10.0.0.3:10000": map[string]any{"isRebuilding": false, "progress": int64(100), "state": "complete"},
		"tcp://10.0.0.4:10000": map[string]any{"isRebuilding": false, "state": "error", "error": "disk full"},
	}}))
	want := []RebuildProgress{
		{Replica: "tcp://10.0.0.2:10000", Progress: 42, State: "in_progress"},
		{Replica: "tcp://10.0.0.4:10000", State: "error", Error: "disk full"},
	}
	if len(got) != len(want) {
		t.Fatalf("Rebuild = %+v, want %+v (in-progress and failed rebuilds only, by replica)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("Rebuild[%d] = %+v, want %+v", i, got[i], want[i])
		}
	}
	if Rebuild(nil) != nil {
		t.Error("nil engine: want no rebuilds")
	}
}
