package context

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestLoggingOutputNeverReachesAIContext(t *testing.T) {
	out := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "logging.banzaicloud.io/v1beta1", "kind": "ClusterOutput",
		"metadata": map[string]any{"name": "loki", "namespace": "logging-system"},
		"spec": map[string]any{"loki": map[string]any{
			"url":               "https://user:pw@loki.example/push?token=abc",
			"password":          map[string]any{"value": "s3cret"},
			"tenant":            "acme",
			"bearer_token_file": "/var/run/x",
		}},
		"status": map[string]any{"active": true},
	}}
	for _, level := range []VerbosityLevel{LevelSummary, LevelDetail, LevelCompact} {
		b, err := json.Marshal(MinifyUnstructured(out, level))
		if err != nil {
			t.Fatal(err)
		}
		s := string(b)
		if !strings.Contains(s, "loki") {
			t.Errorf("level %v lost the output type: %s", level, s)
		}
		for _, leak := range []string{"s3cret", "user:pw", "token=abc", "acme", "bearer_token_file"} {
			if strings.Contains(s, leak) {
				t.Errorf("level %v leaks %q: %s", level, leak, s)
			}
		}
	}
	flow := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "logging.banzaicloud.io/v1beta1", "kind": "ClusterFlow",
		"metadata": map[string]any{"name": "optin", "namespace": "logging-system"},
		"spec":     map[string]any{"match": []any{map[string]any{"select": map[string]any{"labels": map[string]any{"app": "web"}}}}},
	}}
	if b, _ := json.Marshal(MinifyUnstructured(flow, LevelDetail)); !strings.Contains(string(b), `"match"`) {
		t.Errorf("Flow match must stay in detail: %s", b)
	}
}
