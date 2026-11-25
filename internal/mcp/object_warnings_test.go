package mcp

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// With a manager answer in the context, the metadata-only manager warning is
// left out: the answer weighs those claims against the managers' lists.
func TestObjectWarningsDeferToTheManagerAnswer(t *testing.T) {
	obj := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "v1", "kind": "ConfigMap",
		"metadata": map[string]any{"namespace": "shop", "name": "settings", "annotations": map[string]any{"meta.helm.sh/release-name": "web"}},
	}}
	if got := objectWarnings(obj, nil); len(got) != 1 {
		t.Fatalf("warnings without a context = %q; want the metadata warning", got)
	}
	if got := objectWarnings(obj, &resourcecontext.ResourceContext{Manager: &resourcecontext.ManagerContext{State: "claimed"}}); len(got) != 0 {
		t.Fatalf("warnings with a manager answer = %q; want none", got)
	}
}

func TestResourceRelationshipPreviewWarning(t *testing.T) {
	rc := &resourcecontext.ResourceContext{Omitted: []resourcecontext.OmittedField{
		{Field: "dependents", Reason: resourcecontext.OmittedRBACDenied},
		{Field: "backends", Reason: resourcecontext.OmittedUnavailable},
		{Field: "exposes", Reason: resourcecontext.OmittedCacheCold},
		{Field: "serviceAccountSummary.secretRefs", Reason: resourcecontext.OmittedBudgetExceeded},
		{Field: "reflection.mirrors", Reason: resourcecontext.OmittedBudgetExceeded},
	}}
	if w := relationshipPreviewWarning(rc); w != "" {
		t.Fatalf("unclipped relationships: %q", w)
	}
	rc.Omitted = append(rc.Omitted, resourcecontext.OmittedField{Field: "exposes", Reason: resourcecontext.OmittedBudgetExceeded}, resourcecontext.OmittedField{Field: "backends", Reason: resourcecontext.OmittedBudgetExceeded})
	if w := relationshipPreviewWarning(rc); w != "Partial preview of backends, exposes. Radar reached its display or access-check limit. The total is unknown. To list more, use search with kind:<Kind> and a CEL filter on the field that refers to this object." {
		t.Fatalf("empty clipped preview: %q", w)
	}
}
