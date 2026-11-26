package gitops

import (
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestMatchesArgoSyncSource(t *testing.T) {
	old, selected := strings.Repeat("a", 40), strings.Repeat("b", 40)
	matches := func(a *unstructured.Unstructured, id int64, previous, revision string) bool {
		pair, ok := RenderableArgoSyncPairs(a)[id]
		return ok && pair.Previous == previous && pair.Revision == revision
	}
	source := map[string]any{"repoURL": "https://example.test/git", "path": "a", "targetRevision": "main", "kustomize": map[string]any{"namePrefix": "demo-"}}
	app := &unstructured.Unstructured{Object: map[string]any{"spec": map[string]any{"source": source, "destination": map[string]any{"name": "in-cluster"}}, "status": map[string]any{"history": []any{
		map[string]any{"id": float64(4), "revision": old, "source": source},
		map[string]any{"id": int64(5), "revision": selected, "source": source},
	}}}}
	if !matches(app, 5, old, selected) {
		t.Fatal("eligible pair rejected")
	}
	overrides := app.DeepCopy()
	cur := overrides.Object["spec"].(map[string]any)["source"].(map[string]any)
	cur["targetRevision"], cur["chart"] = "other-branch", ""
	if !matches(overrides, 5, old, selected) {
		t.Error("targetRevision override / empty chart rejected")
	}
	for _, tc := range []struct {
		name               string
		id                 int64
		previous, revision string
	}{
		{"gap", 6, old, selected}, {"wrong order", 5, selected, old}, {"same commit", 5, selected, selected}, {"wrong revision", 5, old, "unknown"},
	} {
		if matches(app, tc.id, tc.previous, tc.revision) {
			t.Errorf("%s accepted", tc.name)
		}
	}
	for _, tc := range []struct {
		name   string
		mutate func(spec, first, second map[string]any)
	}{
		{"remote destination", func(spec, _, _ map[string]any) {
			spec["destination"] = map[string]any{"server": "https://other.cluster"}
		}},
		{"abbreviated SHA", func(_, entry, _ map[string]any) { entry["revision"] = "abc1234" }},
		{"changed source options", func(_, first, _ map[string]any) { delete(first["source"].(map[string]any), "kustomize") }},
		{"repurposed Application", func(spec, _, _ map[string]any) { spec["source"].(map[string]any)["path"] = "elsewhere" }},
		{"multi source Application", func(spec, _, _ map[string]any) { spec["sources"] = []any{source} }},
		{"historic multi source", func(_, _, second map[string]any) { second["sources"] = []any{source} }},
		{"historic multi revision", func(_, _, second map[string]any) { second["revisions"] = []any{"x"} }},
		{"source hydrator", func(spec, _, _ map[string]any) { spec["sourceHydrator"] = map[string]any{} }},
		{"chart version", func(spec, _, _ map[string]any) { spec["source"].(map[string]any)["chart"] = "demo" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			test := app.DeepCopy()
			entries := test.Object["status"].(map[string]any)["history"].([]any)
			tc.mutate(test.Object["spec"].(map[string]any), entries[0].(map[string]any), entries[1].(map[string]any))
			if matches(test, 5, old, selected) {
				t.Error("accepted ineligible pair")
			}
		})
	}
}
