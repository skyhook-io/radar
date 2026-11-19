package health

import (
	"encoding/json"
	"os"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSubstrateWorkerPoolVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/substrate_workerpool.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name   string
		Object unstructured.Unstructured
		Level  Level
		Text   string
		// Now pins the clock for vectors that depend on elapsed time; others
		// are judged shortly after their conditions were recorded.
		Now string
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tc.Now)
			if tc.Now == "" {
				now, err = time.Parse(time.RFC3339, defaultVectorNow)
			}
			if err != nil {
				t.Fatal(err)
			}
			v := SubstrateWorkerPool(&tc.Object, now)
			if v.Level != tc.Level || v.Reason != tc.Text {
				t.Fatalf("got %+v, want level=%s text=%s", v, tc.Level, tc.Text)
			}
		})
	}
}

// defaultVectorNow is five minutes after the vectors' recorded transitions,
// inside workerPoolProvisionGrace. The UI's test uses the same default.
const defaultVectorNow = "2026-11-14T10:05:00Z"
