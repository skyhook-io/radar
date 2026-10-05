package context

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
	"testing"
)

func TestDatumSecretsRedactedAtEveryLevel(t *testing.T) {
	for _, kind := range []string{"Domain", "Connector", "Instance", "Workload"} {
		group := "networking.datumapis.com"
		spec, status := map[string]any{}, map[string]any{}
		switch kind {
		case "Domain":
			status["verification"] = map[string]any{"httpToken": map[string]any{"body": "sensitive-challenge"}}
			status["registration"] = map[string]any{"contacts": []any{map[string]any{"email": "sensitive-contact"}}, "abuse": map[string]any{"email": "sensitive-abuse"}}
		case "Connector":
			status["connectionDetails"] = map[string]any{"privateKey": "sensitive-key"}
		default:
			group = "compute.datumapis.com"
			runtime := map[string]any{"sandbox": map[string]any{"containers": []any{map[string]any{"name": "web", "env": []any{map[string]any{"name": "API_TOKEN", "value": "sensitive-env"}, map[string]any{"name": "MODE", "value": "production"}, map[string]any{"name": "DB_PASSWORD", "valueFrom": map[string]any{"secretKeyRef": map[string]any{"name": "database", "key": "password"}}}}}}}}
			spec["runtime"] = runtime
			if kind == "Workload" {
				spec = map[string]any{"template": map[string]any{"spec": spec}}
			}
		}
		u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v1alpha", "kind": kind, "metadata": map[string]any{"name": "fixture"}, "spec": spec, "status": status}}
		before := mustJSON(u.Object)
		for _, level := range []VerbosityLevel{LevelSummary, LevelCompact, LevelDetail} {
			b, _ := json.Marshal(MinifyUnstructured(u, level))
			for _, secret := range []string{"sensitive-env", "sensitive-challenge", "sensitive-key", "sensitive-contact", "sensitive-abuse"} {
				if strings.Contains(string(b), secret) {
					t.Errorf("%s level %d leaked %s", kind, level, secret)
				}
			}
			if level == LevelDetail && (kind == "Instance" || kind == "Workload") && (!strings.Contains(string(b), "database") || !strings.Contains(string(b), "production")) {
				t.Errorf("redaction lost ordinary environment values or secret references: %s", b)
			}
		}
		if mustJSON(u.Object) != before {
			t.Fatal("redaction mutated input")
		}
	}
}
func mustJSON(v any) string { b, _ := json.Marshal(v); return string(b) }
