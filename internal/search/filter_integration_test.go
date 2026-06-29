package search

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/filter"
)

// Filter integration tests — exercise the full Search() walker with
// a compiled CEL filter installed, covering the silent-drop policy,
// eval-error stats, and the dynamic-CRD path.

func TestSearch_FilterDropsNonMatching(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"pods": {
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "prod-east", Name: "a"},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "x"}}},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "dev", Name: "b"},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "x"}}},
				},
			},
		},
	}
	f, err := filter.CompileObjectFilter(`metadata.namespace.startsWith("prod-")`)
	if err != nil {
		t.Fatal(err)
	}
	// No free tokens — pure filter-only query. Both Pods would otherwise
	// score 1 from the modifier-less match; the CEL filter is what
	// narrows to the prod-east one.
	res, err := Search(context.Background(), p, Parse(""), Options{Include: IncludeNone, Filter: f})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 || res.Hits[0].Name != "a" {
		t.Fatalf("expected only prod-east pod, got %+v", res.Hits)
	}
	if res.FilterErrors != 0 {
		t.Errorf("clean filter, expected no eval errors, got %d", res.FilterErrors)
	}
}

func TestSearch_FilterEvalError_DropsRowAndReportsCount(t *testing.T) {
	// Filter references a field that doesn't exist on the candidate's
	// concrete object — CEL emits an eval error per row; we want the
	// row dropped, the count surfaced, and no 500.
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"pods": {
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "p", Name: "a"},
					Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "c", Image: "x"}}},
				},
			},
		},
	}
	// `status.thing_that_isnt_there` is a runtime-only error path
	// (compiles against the dyn-typed status binding, fails at eval
	// because the field's absent). Wrap in `has()` would prevent the
	// error — the test deliberately doesn't.
	f, err := filter.CompileObjectFilter(`status.thing_that_isnt_there == "x"`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), p, Parse("a"), Options{Include: IncludeNone, Filter: f})
	if err != nil {
		t.Fatalf("Search returned error instead of dropping row: %v", err)
	}
	if len(res.Hits) != 0 {
		t.Fatalf("expected 0 hits (filter eval errored), got %+v", res.Hits)
	}
	if res.FilterErrors == 0 {
		t.Error("expected FilterErrors > 0 so agents can distinguish empty-result vs filter-broken")
	}
	if res.FilterErrorSample == "" {
		t.Error("expected FilterErrorSample to be populated")
	}
}

func TestSearch_FilterAgainstDynamicCRD(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "my-app", "namespace": "argocd"},
		"spec":       map[string]any{"project": "default"},
	}}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {app}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Application"},
	}
	// Filter exercises the unstructuredActivation path.
	f, err := filter.CompileObjectFilter(`spec.project == "default"`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), p, Parse(""), Options{Include: IncludeNone, Filter: f})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("expected 1 CRD hit, got %+v", res.Hits)
	}
}
