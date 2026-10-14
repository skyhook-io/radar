package server

import (
	"context"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestApplicationControllerFactsStayWithinReadableNamespaces(t *testing.T) {
	visible := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"namespace": "app", "name": "visible"},
		"spec":     map[string]any{"path": "billing/overlays/dev"},
		"status":   map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "app_web_apps_Deployment"}}}},
	}}
	hidden := visible.DeepCopy()
	hidden.SetNamespace("hidden")
	hidden.SetName("private-manager")
	hidden.Object["spec"] = map[string]any{"path": "private/overlays/prod"}
	hidden.Object["status"] = map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "app_other_apps_Deployment"}}}}
	for _, allowed := range [][]string{{"app"}, {}} {
		facts := namespaceScopedResourceLister{resourceLister: &stubLister{items: []*unstructured.Unstructured{visible, hidden}}, namespaces: allowed}
		paths := fluxKustomizationFacts(context.Background(), facts)
		if paths["hidden/private-manager"] != "" || paths["private-manager"] != "" {
			t.Fatalf("hidden source path leaked: %+v", paths)
		}
		sources := map[string][]appSourceRef{}
		addFluxKustomizationManagedSourceRefs(context.Background(), facts, sources)
		if len(sources[managedWorkloadKey("apps", "Deployment", "app", "other")]) != 0 {
			t.Fatalf("hidden inventory source leaked: %+v", sources)
		}
		if len(allowed) == 0 && (len(paths) != 0 || len(sources) != 0) {
			t.Fatalf("no-access facts were not empty: %+v %+v", paths, sources)
		}
	}
	argoVisible := argoAppInNamespace("app", "visible", map[string]any{"source": map[string]any{"path": "billing/overlays/dev"}})
	argoHidden := argoAppInNamespace("hidden", "private-manager", map[string]any{"source": map[string]any{"path": "private/overlays/prod"}})
	paths, _, items := argoApplicationFacts(context.Background(), namespaceScopedResourceLister{resourceLister: &stubLister{items: []*unstructured.Unstructured{argoVisible, argoHidden}}, namespaces: []string{"app"}})
	if len(items) != 1 || items[0].GetNamespace() != "app" || paths["hidden/private-manager"] != "" || paths["private-manager"] != "" {
		t.Fatalf("hidden Argo facts leaked: %+v %+v", paths, items)
	}
}
