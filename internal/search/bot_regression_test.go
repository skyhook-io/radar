package search

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestExplicitSearchWarmsRequestedNamespace(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, fail := range []bool{false, true} {
		t.Run(fmt.Sprint(fail), func(t *testing.T) {
			p := &fakeProvider{
				kinds:      map[schema.GroupVersionResource]string{gvr: "Widget"},
				namespaced: map[schema.GroupVersionResource]bool{gvr: true},
				observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {
					State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"a"},
				}},
				dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {{Object: map[string]any{"metadata": map[string]any{"name": "cached", "namespace": "a"}}}}},
			}
			if fail {
				p.warmError = errors.New("watch failed")
			}
			res, err := Search(context.Background(), p, Parse("kind:Widget"), Options{Namespaces: []string{"a", "b"}})
			if err != nil || res.Partial != fail || !reflect.DeepEqual(p.warmNamespaces, []string{"b"}) {
				t.Fatalf("result=%+v err=%v warmed=%v", res, err, p.warmNamespaces)
			}
			want := []string{"a", "b"}
			if fail {
				want = []string{"a"}
			}
			if !reflect.DeepEqual(p.dynamicListNamespaces, want) || len(res.Hits) == 0 {
				t.Fatalf("surviving hits=%+v lists=%v", res, p.dynamicListNamespaces)
			}
			if fail {
				res, _ = Search(context.Background(), p, Parse("kind:Widget"), Options{})
				if !res.Partial || len(res.Hits) == 0 {
					t.Fatalf("all-namespace failed warm lost cached hits: %+v", res)
				}
			}
		})
	}
}

func TestExplicitSearchSharesWarmDeadline(t *testing.T) {
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{}}
	for i := range 3 {
		gvr := schema.GroupVersionResource{Group: fmt.Sprintf("group%d.io", i), Version: "v1", Resource: "widgets"}
		p.kinds[gvr] = "Widget"
		p.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnwatched}
	}
	var deadline time.Time
	p.warmFunc = func(ctx context.Context, _ schema.GroupVersionResource, _ string) error {
		d, ok := ctx.Deadline()
		if !ok || time.Until(d) > 2*time.Second {
			t.Fatalf("unbounded warm: %v %v", d, ok)
		}
		if deadline.IsZero() {
			deadline = d
		} else if !d.Equal(deadline) {
			t.Fatalf("warm deadline reset: %v vs %v", d, deadline)
		}
		<-ctx.Done()
		return ctx.Err()
	}
	start := time.Now()
	res, err := Search(context.Background(), p, Parse("kind:Widget"), Options{})
	if err != nil || time.Since(start) > 3*time.Second || len(p.warmed) != 3 || !res.Partial || len(res.Unsearched) != 3 {
		t.Fatalf("result=%+v err=%v elapsed=%s warmed=%v", res, err, time.Since(start), p.warmed)
	}
	for _, gap := range res.Unsearched {
		if gap.Reason != "syncing" {
			t.Fatalf("deadline coverage: %+v", gap)
		}
	}
}

func TestBroadSearchIncludesThirdPartyEvent(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "events"}
	builtin := schema.GroupVersionResource{Group: "events.k8s.io", Version: "v1", Resource: "events"}
	p := &fakeProvider{
		kinds: map[schema.GroupVersionResource]string{gvr: "Event", builtin: "Event"},
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
			gvr:     {{Object: map[string]any{"metadata": map[string]any{"name": "custom-event"}}}},
			builtin: {{Object: map[string]any{"metadata": map[string]any{"name": "diagnostic-event"}}}},
		},
	}
	res, _ := Search(context.Background(), p, Parse(""), Options{})
	if res.Partial || len(res.Hits) != 1 || res.Hits[0].Group != "example.io" {
		t.Fatalf("third-party Event: %+v", res)
	}
	p.observations = map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSyncing}}
	res, _ = Search(context.Background(), p, Parse(""), Options{})
	if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Kind != "Event" || res.Unsearched[0].Group != "example.io" {
		t.Fatalf("Event coverage: %+v", res)
	}
}
