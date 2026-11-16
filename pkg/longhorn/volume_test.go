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
