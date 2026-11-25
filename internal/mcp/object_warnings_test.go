package mcp

import (
	"slices"
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
	if got := resourceContextWarnings(obj, nil, ""); len(got) != 1 {
		t.Fatalf("warnings without a context = %q; want the metadata warning", got)
	}
	if got := resourceContextWarnings(obj, &resourcecontext.ResourceContext{Manager: &resourcecontext.ManagerContext{State: "claimed"}}, ""); len(got) != 0 {
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
	if w := relationshipPreviewWarning(rc, ""); w != "" {
		t.Fatalf("unclipped relationships: %q", w)
	}
	for _, tc := range []struct {
		fields []string
		want   string
		first  string
	}{
		{[]string{"exposes"}, "exposes", "exposes"},
		{[]string{"exposes", "backends"}, "backends", "backends"},
		{[]string{"exposedBy", "exposes"}, "exposedBy", "exposedBy"},
		{[]string{"exposedBy", "exposes", "backends"}, "backends, exposedBy", "backends"},
	} {
		t.Run(tc.want, func(t *testing.T) {
			clipped := resourcecontext.ResourceContext{Omitted: slices.Clone(rc.Omitted)}
			for _, field := range tc.fields {
				clipped.Omitted = append(clipped.Omitted, resourcecontext.OmittedField{Field: field, Reason: resourcecontext.OmittedBudgetExceeded})
			}
			want := "Partial preview of " + tc.want + ". Radar reached its display or access-check limit. The total is unknown. " +
				"To list up to 200 of one, call get_resource for this resource with relationship=\"" + tc.first + "\"."
			if got := relationshipPreviewWarning(&clipped, ""); got != want {
				t.Fatalf("warning = %q, want %q", got, want)
			}
		})
	}
}

func TestExpandedRelationshipPreviewWarning(t *testing.T) {
	clipped := func(fields ...string) *resourcecontext.ResourceContext {
		rc := &resourcecontext.ResourceContext{}
		for _, f := range fields {
			rc.Omitted = append(rc.Omitted, resourcecontext.OmittedField{Field: f, Reason: resourcecontext.OmittedBudgetExceeded})
		}
		return rc
	}
	const exhausted = "dependents is still partial after expansion: Radar reached its 200-entry limit or its access-check limit. The total is unknown and get_resource can't list more."
	for _, tc := range []struct {
		name     string
		rc       *resourcecontext.ResourceContext
		expanded string
		want     string
	}{
		{"complete after expansion", clipped("dependencies"), "dependents",
			"Partial preview of dependencies. Radar reached its display or access-check limit. The total is unknown. To list up to 200 of one, call get_resource for this resource with relationship=\"dependencies\"."},
		{"clipped again", clipped("dependents"), "dependents", exhausted},
		{"clipped again beside another", clipped("dependents", "monitors"), "dependents", exhausted +
			" Partial preview of monitors. Radar reached its display or access-check limit. The total is unknown. To list up to 200 of one, call get_resource for this resource with relationship=\"monitors\"."},
		{"routing union", clipped("backends", "exposes"), "exposes",
			"exposes is still partial after expansion: Radar reached its 200-entry limit or its access-check limit. The total is unknown and get_resource can't list more."},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := relationshipPreviewWarning(tc.rc, tc.expanded); got != tc.want {
				t.Fatalf("warning = %q, want %q", got, tc.want)
			}
		})
	}
}
