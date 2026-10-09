package health

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
)

func TestNodeLifecycleCrossLanguage(t *testing.T) {
	raw, err := os.ReadFile("testdata/node_lifecycle.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Now     time.Time `json:"now"`
		Vectors []struct {
			Name            string      `json:"name"`
			Node            corev1.Node `json:"node"`
			Label           string      `json:"label"`
			Level           Level       `json:"level"`
			ReadinessFailed bool        `json:"readinessFailed"`
			Delayed         bool        `json:"delayed"`
		} `json:"vectors"`
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	for _, v := range fixture.Vectors {
		t.Run(v.Name, func(t *testing.T) {
			got := NodeLifecycle(&v.Node, fixture.Now)
			if got.Label != v.Label || got.Level != v.Level || got.ReadinessFailed != v.ReadinessFailed || got.Delayed != v.Delayed {
				t.Fatalf("got %+v; want label=%s level=%s readinessFailed=%v delayed=%v", got, v.Label, v.Level, v.ReadinessFailed, v.Delayed)
			}
		})
	}
}
