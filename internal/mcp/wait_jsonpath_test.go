package mcp

import (
	"reflect"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestSplitJSONPathInput(t *testing.T) {
	// Cases from kubectl v0.37.1's wait condition parsing, restricted to status.
	for _, tc := range []struct {
		input string
		want  []string
	}{
		{`{.status.phase}=Running`, []string{`{.status.phase}`, `Running`}},
		{`'{.status.phase}'="Running"`, []string{`'{.status.phase}'`, `"Running"`}},
		{`{.status.conditions[?(@.type=="Ready")].status}=True`, []string{`{.status.conditions[?(@.type=="Ready")].status}`, `True`}},
		{`{.status.phase}`, []string{`{.status.phase}`}},
		{`{.status.phase}=`, []string{`{.status.phase}`, ``}},
		{`{.status.phase}='test=wrong'`, []string{`{.status.phase}`, `'test`, `wrong'`}},
		{`a.b.c====d.e.f===g.h.i===`, []string{`a.b.c====d.e.f==`, `g.h.i==`, ``}},
	} {
		if got := splitJSONPathInput(tc.input); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: %q != %q", tc.input, got, tc.want)
		}
	}
}

func TestWaitJSONPath(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{"status": map[string]any{
		"health":         map[string]any{"status": "Healthy"},
		"conditions":     []any{map[string]any{"type": "Ready", "status": "True"}, map[string]any{"type": "Available", "status": "True"}},
		"secretSentinel": map[string]any{"value": "DO-NOT-LEAK-SENTINEL"},
	}, "spec": map[string]any{"secret": "DO-NOT-LEAK-SPEC"}}}
	for _, tc := range []struct {
		expression            string
		met, missing, invalid bool
	}{
		{`{.status.health.status}=Healthy`, true, false, false},
		{`'{.status.health.status}'="Healthy"`, true, false, false},
		{`.status.health.status=Healthy`, true, false, false},
		{`{.status.health.status}`, true, false, false},
		{`{.status.health.status}=Progressing`, false, false, false},
		{`{.status.conditions[?(@.type=="Ready")].status}=True`, true, false, false},
		{`{.status.noSuchField}=True`, false, true, false},
		{`{.spec.secret}=x`, false, false, true},
		{`{.statusExtra}=x`, false, false, true},
		{`{.status.health}={}`, false, false, true},
		{`{.status.conditions[*].status}=True`, false, false, true},
		{`{.status.secretSentinel[?(@>1)]}=x`, false, false, true},
		{`{.status.health.status}=`, false, false, true},
		{`{.status.health.status}='test=wrong'`, false, false, true},
		{`{.status.conditions[?(@.type=="Failed"||@.type=="Complete")].status}=True`, false, false, true},
		{`{.status.health.status}{.spec.secret}=x`, false, false, true},
		{`{.status[}=x`, false, false, true},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			j, err := parseWaitJSONPath(tc.expression)
			met, missing := false, false
			if err == nil {
				met, _, missing, err = j.evaluate(obj)
			}
			if (err != nil) != tc.invalid || met != tc.met || missing != tc.missing {
				t.Fatalf("met=%v missing=%v err=%v", met, missing, err)
			}
			if err != nil && (strings.Contains(err.Error(), "DO-NOT-LEAK") || strings.Contains(err.Error(), "map[")) {
				t.Fatalf("unsafe error: %v", err)
			}
		})
	}
}
