package context

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func k3sHelmObject(kind string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.cattle.io/v1", "kind": kind,
		"metadata": map[string]any{"name": "traefik", "namespace": "kube-system"},
		"spec": map[string]any{
			"chart":         "traefik",
			"valuesContent": "a: 1\npassword: VALUES-SENTINEL\n",
			"set":           map[string]any{"auth.token": "SET-SENTINEL"},
		},
	}}
}

func TestK3sHelmValuesNeverReachAIContext(t *testing.T) {
	for _, kind := range []string{"HelmChart", "HelmChartConfig"} {
		for _, level := range []VerbosityLevel{LevelSummary, LevelDetail, LevelCompact} {
			raw, err := json.Marshal(MinifyUnstructured(k3sHelmObject(kind), level))
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(raw), "SENTINEL") {
				t.Errorf("%s at level %d leaks values: %s", kind, level, raw)
			}
		}
	}
	detail, _ := MinifyUnstructured(k3sHelmObject("HelmChart"), LevelDetail).(map[string]any)
	spec, _ := detail["spec"].(map[string]any)
	if spec["valuesContent"] != "[2 lines omitted]" || !reflect.DeepEqual(spec["set"], map[string]any{"auth.token": "[omitted]"}) {
		t.Fatalf("detail spec = %#v", spec)
	}

	flux := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "source.toolkit.fluxcd.io/v1", "kind": "HelmChart",
		"metadata": map[string]any{"name": "x", "namespace": "flux-system"},
		"spec":     map[string]any{"chart": "x", "valuesFiles": []any{"values.yaml"}},
	}}
	got, _ := MinifyUnstructured(flux, LevelDetail).(map[string]any)
	if fspec, _ := got["spec"].(map[string]any); fspec["chart"] != "x" || fspec["valuesFiles"] == nil {
		t.Fatalf("a Flux HelmChart is not stripped: %#v", got)
	}
}
