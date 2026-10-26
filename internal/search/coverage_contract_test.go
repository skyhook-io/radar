package search

import (
	"context"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/ptr"

	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// A broad query searches only what the caller and Radar can read without
// naming each skipped kind, so it must say its scope was narrowed: "complete"
// alone would read as an exhaustive negative.
func TestBroadSearchSaysHowItWasNarrowed(t *testing.T) {
	for _, tc := range []struct {
		name             string
		query            string
		visible, ceiling []string
		pinned           bool
		want             []resourceapi.Narrowing
	}{
		{"unrestricted", "robusta", nil, nil, false, nil},
		{"restricted caller", "robusta", []string{"a"}, []string{"a"}, false, []resourceapi.Narrowing{resourceapi.NarrowedByUserAccess}},
		{"pin", "robusta", []string{"a"}, nil, true, []resourceapi.Narrowing{resourceapi.NarrowedByNamespacePin}},
		{"restricted caller under the pin they can read", "robusta", []string{"a"}, []string{"a"}, true, []resourceapi.Narrowing{resourceapi.NarrowedByNamespacePin, resourceapi.NarrowedByUserAccess}},
		{"a named namespace is not narrowing", "ns:a robusta", []string{"a"}, []string{"a"}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := Parse(tc.query)
			opts := Options{Include: IncludeNone}
			var pin *string
			if tc.pinned {
				pin = &tc.visible[0]
			}
			opts.Selection = SelectNamespaces(namespaceFixture(), q, readscope.NamespaceAccess{Namespaces: tc.ceiling, Checked: true}, pin)
			res, _ := Search(context.Background(), namespaceFixture(), q, opts)
			if !reflect.DeepEqual(res.Coverage.NarrowedBy, tc.want) || partial(res) {
				t.Fatalf("coverage = %+v, want complete narrowed by %v", res.Coverage, tc.want)
			}
		})
	}
}

// A scope reason a broad query doesn't report still narrowed it.
func TestQuietScopeReasonsNarrowTheSearch(t *testing.T) {
	p := namespaceFixture()
	p.typedReasons = map[string]string{"Node": string(readreason.RadarForbidden)}
	res, _ := Search(context.Background(), p, Parse("robusta"), Options{Include: IncludeNone, CanReadClusterScoped: func(kind, group, resource string) (bool, bool) { return kind != "ClusterRole", true }})
	want := []resourceapi.Narrowing{resourceapi.NarrowedByKindCollection, resourceapi.NarrowedByUserAccess}
	if partial(res) || !reflect.DeepEqual(res.Coverage.NarrowedBy, want) {
		t.Fatalf("coverage = %+v, want complete narrowed by %v", res.Coverage, want)
	}
}

// A named namespace the caller can see but the --namespace-scope pin leaves
// out is outside_namespace_pin; one they can't see is not visible, inside or
// outside the pin, so guesses don't reveal the pin.
func TestNamedNamespaceOutsideThePin(t *testing.T) {
	for _, tc := range []struct {
		ceiling []string
		want    readreason.Code
	}{
		{nil, readreason.OutsideNamespacePin},
		{[]string{"a", "b"}, readreason.OutsideNamespacePin},
		{[]string{"a"}, readreason.UserNamespaceNotVisible},
	} {
		q := Parse("ns:b robusta")
		opts := Options{Include: IncludeNone}
		opts.Selection = SelectNamespaces(namespaceFixture(), q, readscope.NamespaceAccess{Namespaces: tc.ceiling, Checked: true}, ptr.To("a"))
		res, _ := Search(context.Background(), namespaceFixture(), q, opts)
		if got := unsearched(res); len(got) != 1 || got[0].Reason != tc.want || !slices.Equal(got[0].Namespaces, []string{"b"}) {
			t.Fatalf("ceiling %v: %+v, want %s", tc.ceiling, got, tc.want)
		}
	}
}

// readableProvider is a provider whose typed Pods are still loading except in
// the namespaces it names.
type readableProvider struct {
	*fakeProvider
	readable []string
	limited  bool
	listed   [][]string
}

func (p *readableProvider) PlanTyped(kind string, namespaces []string) readplan.Plan {
	plan := p.fakeProvider.PlanTyped(kind, namespaces)
	if kind != "Pod" {
		return plan
	}
	plan.CollectionLimited = p.limited
	plan.Partitions = slices.DeleteFunc(plan.Partitions, func(part readplan.Partition) bool {
		return part.Extent == readplan.NamespaceSet && slices.Contains(p.readable, part.Namespaces[0])
	})
	for _, ns := range p.readable {
		plan.Partitions = append(plan.Partitions, readplan.Partition{Extent: readplan.NamespaceSet, Namespaces: []string{ns}, Verdict: readplan.Verdict{State: readplan.Readable}})
	}
	return plan
}

func (p *readableProvider) ListTyped(resource string, namespaces []string) ([]runtime.Object, error) {
	p.listed = append(p.listed, namespaces)
	return p.fakeProvider.ListTyped(resource, namespaces)
}

// A typed kind watched namespace by namespace is searched where its informers
// have synced while another namespace still loads, and the loading part is a
// gap instead of the whole kind going unsearched.
func TestSearchReadsSyncedNamespacesOfALoadingKind(t *testing.T) {
	p := &readableProvider{fakeProvider: namespaceFixture(), readable: []string{"a"}}
	p.typedReasons = map[string]string{"Pod": string(readreason.KindSyncPending)}
	q := Parse("kind:Pod robusta")
	opts := Options{Include: IncludeNone}
	opts.Selection = SelectNamespaces(p, q, readscope.NamespaceAccess{Namespaces: []string{"a", "b"}, Checked: true}, nil)
	res, _ := Search(context.Background(), p, q, opts)
	if !slices.Equal(hitNames(res), []string{"Pod/a/robusta-a"}) || !reflect.DeepEqual(p.listed, [][]string{{"a"}}) {
		t.Fatalf("hits %v listed %v", hitNames(res), p.listed)
	}
	if got := unsearched(res); len(got) != 1 || got[0].Reason != readreason.KindSyncPending || got[0].Kind != "Pod" || !partial(res) {
		t.Fatalf("the loading namespace must stay a gap: %+v", got)
	}
}

// A kind's state in namespaces the caller can't list it in says nothing about
// the search: Secrets stalled where the caller has no access to them don't
// make the search partial.
func TestPerKindAccessBoundsTheKindsReadiness(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{"secrets": {
			&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "robusta-a"}},
		}},
		typedNamespaceReasons: map[string]map[string]string{"Secret": {"b": string(readreason.KindSyncTimedOut)}},
	}
	res, _ := Search(context.Background(), p, Parse("robusta"), Options{
		Include:   IncludeNone,
		Selection: readscope.Selection{Namespaces: []string{"a", "b"}},
		TypedKindAccess: func(ns []string, group, resource string) ([]string, []string, []string) {
			if resource == "secrets" {
				return []string{"a"}, []string{"b"}, nil
			}
			return ns, nil, nil
		},
	})
	if !slices.Equal(hitNames(res), []string{"Secret/a/robusta-a"}) {
		t.Fatalf("hits %v", hitNames(res))
	}
	if got := gapsWithReason(res, string(readreason.KindSyncTimedOut)); len(got) != 0 || partial(res) {
		t.Fatalf("a namespace outside the caller's access made the search partial: %+v", res.Coverage)
	}
	if !slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByUserAccess) {
		t.Fatalf("the caller's access must still narrow the search: %+v", res.Coverage)
	}
}

// An installed API that can't be listed, such as a create-only review, is not
// unknown: naming it says search can't read it, not that it isn't installed.
func TestNamedKindThatCantBeListed(t *testing.T) {
	p := namespaceFixture()
	p.unlistable = []k8score.APIResource{{Group: "authentication.k8s.io", Version: "v1", Kind: "TokenReview", Name: "tokenreviews", Verbs: []string{"create"}}}
	for _, query := range []string{"kind:TokenReview", "kind:tokenreviews"} {
		res, _ := Search(context.Background(), p, Parse(query), Options{Include: IncludeNone})
		got := unsearched(res)
		if len(got) != 1 || got[0].Reason != readreason.KindNotWatchable || got[0].Kind != "TokenReview" || got[0].Group != "authentication.k8s.io" || !partial(res) {
			t.Fatalf("%s: %+v", query, got)
		}
	}
	res, _ := Search(context.Background(), p, Parse("kind:TokenReveiw"), Options{Include: IncludeNone})
	if got := unsearched(res); len(got) != 1 || got[0].Reason != readreason.KindUnknown {
		t.Fatalf("a misspelled kind is still unknown: %+v", got)
	}
}
