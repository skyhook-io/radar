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
		Name   string
		Object unstructured.Unstructured
		Level  Level
		Text   string
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			v := SubstrateWorkerPool(&tc.Object)
			if v.Level != tc.Level || v.Reason != tc.Text {
				t.Fatalf("got %+v, want level=%s text=%s", v, tc.Level, tc.Text)
			}
		})
	}
}
