package context

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestUpgradePlanEnvValuesNeverReachAIContext(t *testing.T) {
	plan := func() *unstructured.Unstructured {
		envs := func() []any { return []any{map[string]any{"name": "MODE", "value": "ENV-SENTINEL"}} }
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "upgrade.cattle.io/v1", "kind": "Plan",
			"metadata": map[string]any{"name": "k3s-server", "namespace": "system-upgrade"},
			"spec": map[string]any{
				"version": "v1.32.1+k3s1",
				"upgrade": map[string]any{"image": "rancher/k3s-upgrade", "envs": envs()},
				"prepare": map[string]any{"image": "rancher/k3s-upgrade", "envs": envs()},
			},
		}}
	}
	for _, level := range []VerbosityLevel{LevelSummary, LevelDetail, LevelCompact} {
		raw, _ := json.Marshal(MinifyUnstructured(plan(), level))
		if strings.Contains(string(raw), "ENV-SENTINEL") {
			t.Errorf("level %d leaks env values: %s", level, raw)
		}
	}
	raw, _ := json.Marshal(MinifyUnstructured(plan(), LevelDetail))
	if !strings.Contains(string(raw), `"MODE"`) {
		t.Errorf("env names stay: %s", raw)
	}
	other := plan()
	other.SetAPIVersion("example.com/v1")
	raw, _ = json.Marshal(MinifyUnstructured(other, LevelDetail))
	if !strings.Contains(string(raw), "ENV-SENTINEL") {
		t.Errorf("a foreign Plan is not this redaction's to touch: %s", raw)
	}
}
