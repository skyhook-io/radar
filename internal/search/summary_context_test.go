package search

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// TestSearch_SummaryBuilderAttached pins the wiring: when Options.SummaryBuilder
// is non-nil, the executor invokes it per kept hit and the result lands
// in Hit.SummaryContext.
func TestSearch_SummaryBuilderAttached(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"pods": {
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api-1"},
					Status: corev1.PodStatus{
						Phase:             corev1.PodRunning,
						ContainerStatuses: []corev1.ContainerStatus{{Ready: true}},
					},
				},
			},
		},
	}

	var calls int
	var gotGroup string
	builder := func(obj runtime.Object, u *unstructured.Unstructured, group, kind, namespace, name string) *resourcecontext.ResourceSummaryContext {
		calls++
		gotGroup = group
		return &resourcecontext.ResourceSummaryContext{
			ManagedBy:  &resourcecontext.ManagedByRef{Kind: "Deployment", Source: "native", Name: "api", Namespace: namespace},
			Health:     "healthy",
			IssueCount: 0,
		}
	}

	res, _ := Search(context.Background(), p, Parse("api-1"), Options{
		Include:        IncludeNone,
		SummaryBuilder: builder,
	})
	if calls != 1 {
		t.Fatalf("SummaryBuilder calls = %d, want 1", calls)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(res.Hits))
	}
	h := res.Hits[0]
	if h.SummaryContext == nil {
		t.Fatalf("SummaryContext not attached to hit: %+v", h)
	}
	if h.SummaryContext.Health != "healthy" {
		t.Errorf("Health = %q, want healthy", h.SummaryContext.Health)
	}
	if h.SummaryContext.ManagedBy == nil || h.SummaryContext.ManagedBy.Name != "api" {
		t.Errorf("ManagedBy mismatch: %+v", h.SummaryContext.ManagedBy)
	}
	// Pod is core-group — builder should see "" for group, threaded
	// through from candidate.Group (set on the typed walker via tk.Group).
	if gotGroup != "" {
		t.Errorf("builder saw group=%q for core-group Pod, want \"\"", gotGroup)
	}
}

// TestSearch_NoSummaryBuilder_LeavesNilContext is the opt-out path
// (context=none in the handler maps to nil SummaryBuilder here). Hits
// must have no SummaryContext.
func TestSearch_NoSummaryBuilder_LeavesNilContext(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"pods": {
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "api-1"},
				},
			},
		},
	}
	res, _ := Search(context.Background(), p, Parse("api-1"), Options{Include: IncludeNone})
	if len(res.Hits) != 1 {
		t.Fatalf("hits = %d, want 1", len(res.Hits))
	}
	if res.Hits[0].SummaryContext != nil {
		t.Errorf("expected nil SummaryContext when SummaryBuilder unset, got %+v", res.Hits[0].SummaryContext)
	}
}
