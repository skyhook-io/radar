package health

import (
	"encoding/json"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSubstrateWorkerPoolVectors(t *testing.T) {
	raw, err := os.ReadFile("testdata/substrate_workerpool.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name     string
		Object   unstructured.Unstructured
		Level    Level
		Text     string
		Critical bool
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			r := ReadSubstrateWorkerPool(&tc.Object)
			if r.Verdict.Level != tc.Level || r.Verdict.Reason != tc.Text || r.Critical != tc.Critical {
				t.Fatalf("got %+v, want level=%s text=%s critical=%v", r, tc.Level, tc.Text, tc.Critical)
			}
		})
	}
}
