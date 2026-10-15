package search

import (
	"context"
	"reflect"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"k8s.io/apimachinery/pkg/runtime"
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
		{"restricted caller", "robusta", []string{"a"}, []string{"a"}, false, []resourceapi.Narrowing{resourceapi.NarrowedByCallerAccess}},
		{"saved pick", "robusta", []string{"a"}, nil, false, []resourceapi.Narrowing{resourceapi.NarrowedByNamespaceSelection}},
		{"pin", "robusta", []string{"a"}, nil, true, []resourceapi.Narrowing{resourceapi.NarrowedByNamespacePin}},
		{"restricted caller under a pick", "robusta", []string{"a"}, []string{"a", "b"}, false, []resourceapi.Narrowing{resourceapi.NarrowedByCallerAccess, resourceapi.NarrowedByNamespaceSelection}},
		{"restricted caller under the pin they can read", "robusta", []string{"a"}, []string{"a"}, true, []resourceapi.Narrowing{resourceapi.NarrowedByCallerAccess, resourceapi.NarrowedByNamespacePin}},
		{"a named namespace is not narrowing", "ns:a robusta", []string{"a"}, []string{"a"}, false, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := Parse(tc.query)
			opts := Options{Include: IncludeNone, Pinned: tc.pinned}
			opts.ScopeNamespaces(q, tc.visible, tc.ceiling)
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
	p.typedReasons = map[string]string{"Node": string(readreason.CollectorForbidden)}
	res, _ := Search(context.Background(), p, Parse("robusta"), Options{Include: IncludeNone, SkipKinds: map[string]bool{"ClusterRole": true}})
	want := []resourceapi.Narrowing{resourceapi.NarrowedByCallerAccess, resourceapi.NarrowedByCollection}
	if partial(res) || !reflect.DeepEqual(res.Coverage.NarrowedBy, want) {
		t.Fatalf("coverage = %+v, want complete narrowed by %v", res.Coverage, want)
	}
}

// The --namespace-scope pin and the caller's saved pick are different fixes,
// so a named namespace either leaves out gets its own code.
func TestNamedNamespaceOutsideThePinOrThePick(t *testing.T) {
	for _, pinned := range []bool{false, true} {
		q := Parse("ns:b robusta")
		opts := Options{Include: IncludeNone, Pinned: pinned}
		opts.ScopeNamespaces(q, []string{"a"}, nil)
		res, _ := Search(context.Background(), namespaceFixture(), q, opts)
		want := readreason.OutsideNamespaceSelection
		if pinned {
			want = readreason.OutsideNamespaceScope
		}
		if got := unsearched(res); len(got) != 1 || got[0].Reason != want || !slices.Equal(got[0].Namespaces, []string{"b"}) {
			t.Fatalf("pinned=%v: %+v, want %s", pinned, got, want)
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

func (p *readableProvider) TypedReadable(string, []string) []string { return p.readable }

func (p *readableProvider) TypedCollectionLimited(string, []string) bool { return p.limited }

func (p *readableProvider) ListTyped(kind string, namespaces []string) ([]runtime.Object, error) {
	p.listed = append(p.listed, namespaces)
	return p.fakeProvider.ListTyped(kind, namespaces)
}

// A typed kind watched namespace by namespace is searched where its informers
// have synced while another namespace still loads, and the loading part is a
// gap instead of the whole kind going unsearched.
func TestSearchReadsSyncedNamespacesOfALoadingKind(t *testing.T) {
	p := &readableProvider{fakeProvider: namespaceFixture(), readable: []string{"a"}}
	p.typedReasons = map[string]string{"Pod": string(readreason.KindSyncPending)}
	q := Parse("kind:Pod robusta")
	opts := Options{Include: IncludeNone}
	opts.ScopeNamespaces(q, []string{"a", "b"}, nil)
	res, _ := Search(context.Background(), p, q, opts)
	if !slices.Equal(hitNames(res), []string{"Pod/a/robusta-a"}) || !reflect.DeepEqual(p.listed, [][]string{{"a"}}) {
		t.Fatalf("hits %v listed %v", hitNames(res), p.listed)
	}
	if got := unsearched(res); len(got) != 1 || got[0].Reason != readreason.KindSyncPending || got[0].Kind != "Pod" || !partial(res) {
		t.Fatalf("the loading namespace must stay a gap: %+v", got)
	}
}
