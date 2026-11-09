package issues

import (
	"encoding/json"
	"os"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestStrimziSharedDisplayScenarios(t *testing.T) {
	raw, err := os.ReadFile("../../testdata/strimzi/connector-status.json")
	if err != nil {
		t.Fatal(err)
	}
	var cases []struct {
		Name      string
		Resource  json.RawMessage
		Issue     bool
		Recovered bool
	}
	if err := json.Unmarshal(raw, &cases); err != nil {
		t.Fatal(err)
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			var u unstructured.Unstructured
			if err := u.UnmarshalJSON(tc.Resource); err != nil {
				t.Fatal(err)
			}
			gvr := schema.GroupVersionResource{Group: "kafka.strimzi.io", Version: "v1", Resource: "kafkaconnectors"}
			if got := len(StrimziConnectorIssues(gvr, &u)) > 0; got != tc.Issue {
				t.Errorf("issue=%v, want %v", got, tc.Issue)
			}
			if got := StrimziConnectorRecoveryObserved(&u); got != tc.Recovered {
				t.Errorf("recovered=%v, want %v", got, tc.Recovered)
			}
		})
	}
}
