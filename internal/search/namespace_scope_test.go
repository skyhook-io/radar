package search

import (
	"context"
	"reflect"
	"slices"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func TestScopeNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name              string
		query             string
		visible, ceiling  []string
		wantScan          []string
		wantExcluded      []string
		wantSelection     []string
		wantNoNamespaceOK bool
	}{
		{name: "unrestricted", query: "x"},
		{name: "rbac ceiling is not a gap", query: "x", visible: []string{"a", "b"}, ceiling: []string{"b", "a"}, wantScan: []string{"a", "b"}},
		{name: "pick narrows silently", query: "x", visible: []string{"b", "a"}, ceiling: nil, wantScan: []string{"b", "a"}},
		{name: "terms within pick", query: "x ns:a", visible: []string{"a"}, ceiling: nil, wantScan: []string{"a"}},
		{name: "terms outside pick", query: "x ns:a ns:b ns:b", visible: []string{"a"}, ceiling: nil, wantScan: []string{"a"}, wantSelection: []string{"b"}},
		{name: "terms outside rbac", query: "x ns:b ns:c", visible: []string{"a"}, ceiling: []string{"a"}, wantScan: []string{}, wantExcluded: []string{"b", "c"}, wantNoNamespaceOK: true},
		{name: "rbac and pick", query: "x ns:a ns:b ns:c", visible: []string{"a"}, ceiling: []string{"a", "b"}, wantScan: []string{"a"}, wantExcluded: []string{"c"}, wantSelection: []string{"b"}},
		{name: "unrestricted terms", query: "x ns:b", wantScan: []string{"b"}},
		{name: "no namespace access", query: "x", visible: []string{}, ceiling: []string{}, wantScan: []string{}, wantNoNamespaceOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts Options
			opts.ScopeNamespaces(Parse(tc.query), tc.visible, tc.ceiling)
			if !reflect.DeepEqual(opts.Namespaces, tc.wantScan) || !reflect.DeepEqual(opts.ExcludedNamespaces, tc.wantExcluded) ||
				!reflect.DeepEqual(opts.SelectionExcludedNamespaces, tc.wantSelection) || opts.NamespaceExcluded != tc.wantNoNamespaceOK {
				t.Fatalf("got scan=%#v excluded=%#v selection=%#v noNamespace=%v", opts.Namespaces, opts.ExcludedNamespaces, opts.SelectionExcludedNamespaces, opts.NamespaceExcluded)
			}
		})
	}
}

func namespaceFixture() *fakeProvider {
	return &fakeProvider{typed: map[string][]runtime.Object{
		"clusterroles": {&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "robusta-cr"}}},
		"nodes":        {&corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: "robusta-node"}}},
		// The fake lister ignores namespaces, so the out-of-scope Pod also
		// proves that namespaced hits are held to the scanned namespaces.
		"pods": {newPod("a", "robusta-a", "image", nil), newPod("b", "robusta-b", "image", nil)},
	}}
}

func hitNames(res Result) []string {
	var names []string
	for _, hit := range res.Hits {
		names = append(names, hit.Kind+"/"+hit.Namespace+"/"+hit.Name)
	}
	return names
}

func gapsWithReason(res Result, reason string) []UnsearchedKind {
	var out []UnsearchedKind
	for _, gap := range unsearched(res) {
		if string(gap.Reason) == reason {
			out = append(out, gap)
		}
	}
	return out
}

func TestClusterScopedHitsIgnoreNamespaceVisibilityAndSelection(t *testing.T) {
	for _, tc := range []struct {
		name             string
		visible, ceiling []string
	}{
		{"restricted caller", []string{"a"}, []string{"a"}},
		{"saved pick", []string{"a"}, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := Parse("robusta")
			opts := Options{Include: IncludeNone, CanReadClusterScoped: func(kind, group, resource string) (bool, bool) {
				return resource != "nodes", true
			}}
			opts.ScopeNamespaces(q, tc.visible, tc.ceiling)
			res, _ := Search(context.Background(), namespaceFixture(), q, opts)
			if got, want := hitNames(res), []string{"ClusterRole//robusta-cr", "Pod/a/robusta-a"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("hits = %v, want %v", got, want)
			}
			if partial(res) || len(unsearched(res)) != 0 {
				t.Fatalf("a broad search reported the caller's scope: %+v", unsearched(res))
			}
			q = Parse("kind:Node robusta")
			opts.ScopeNamespaces(q, tc.visible, tc.ceiling)
			res, _ = Search(context.Background(), namespaceFixture(), q, opts)
			if denied := gapsWithReason(res, "user_forbidden"); len(res.Hits) != 0 || len(denied) != 1 || denied[0].Kind != "Node" {
				t.Fatalf("cluster-scoped gate: %+v", unsearched(res))
			}
		})
	}
}

func TestExplicitNamespaceTermsAndClusterScopedKinds(t *testing.T) {
	refuse := func(kind, group, resource string) (bool, bool) {
		t.Fatalf("broad ns: query checked cluster-scoped %s", kind)
		return false, true
	}
	q := Parse("ns:a robusta")
	opts := Options{Include: IncludeNone, CanReadClusterScoped: refuse}
	opts.ScopeNamespaces(q, nil, nil)
	res, _ := Search(context.Background(), namespaceFixture(), q, opts)
	if got, want := hitNames(res), []string{"Pod/a/robusta-a"}; !reflect.DeepEqual(got, want) || partial(res) {
		t.Fatalf("broad ns: query = %v %+v, want %v", got, unsearched(res), want)
	}

	q = Parse("kind:ClusterRole kind:Pod ns:a robusta")
	opts = Options{Include: IncludeNone}
	opts.ScopeNamespaces(q, nil, nil)
	res, _ = Search(context.Background(), namespaceFixture(), q, opts)
	if got, want := hitNames(res), []string{"ClusterRole//robusta-cr", "Pod/a/robusta-a"}; !reflect.DeepEqual(got, want) || partial(res) {
		t.Fatalf("named cluster-scoped kind with ns: = %v %+v, want %v", got, unsearched(res), want)
	}
}

// Namespace entries name only namespaces the query named, once per reason
// whatever the kinds searched, and say which of the caller's RBAC, the
// namespace selection or Radar's collection left each one out.
func TestNamespaceCoverageNamesOnlyNamedNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name             string
		query            string
		visible, ceiling []string
		collectorLimited bool
		want             []UnsearchedKind
		hits             int
	}{
		{"rbac", "ns:b ns:c robusta", []string{"a"}, []string{"a"}, false, []UnsearchedKind{{Kind: "*", Reason: "user_namespace_not_visible", Namespaces: []string{"b", "c"}}}, 0},
		{"rbac with kinds", "kind:Pod kind:ClusterRole ns:a ns:b robusta", []string{"a"}, []string{"a"}, false, []UnsearchedKind{{Kind: "*", Reason: "user_namespace_not_visible", Namespaces: []string{"b"}}}, 2},
		{"selection", "kind:Pod ns:b robusta", []string{"a"}, nil, false, []UnsearchedKind{{Kind: "*", Reason: "outside_namespace_selection", Namespaces: []string{"b"}}}, 0},
		{"rbac and selection", "ns:a ns:b ns:c robusta", []string{"a"}, []string{"a", "b"}, false, []UnsearchedKind{
			{Kind: "*", Reason: "outside_namespace_selection", Namespaces: []string{"b"}},
			{Kind: "*", Reason: "user_namespace_not_visible", Namespaces: []string{"c"}},
		}, 1},
		{"collection", "ns:a robusta", nil, nil, true, []UnsearchedKind{{Kind: "*", Reason: "radar_namespace_not_collected", Namespaces: []string{"a"}}}, 1},
		{"selection without ns:", "robusta", []string{"a"}, nil, true, []UnsearchedKind{}, 3},
		{"kind: without ns:", "kind:Pod robusta", []string{"a"}, nil, true, []UnsearchedKind{}, 1},
		{"cluster-scoped kind", "kind:ClusterRole ns:b robusta", []string{"a"}, []string{"a"}, false, []UnsearchedKind{}, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := namespaceFixture()
			if tc.collectorLimited {
				p.typedReasons = map[string]string{"Pod": "radar_namespace_not_collected", "ConfigMap": "radar_namespace_not_collected", "Secret": "radar_namespace_not_collected"}
			}
			q := Parse(tc.query)
			opts := Options{Include: IncludeNone}
			opts.ScopeNamespaces(q, tc.visible, tc.ceiling)
			res, _ := Search(context.Background(), p, q, opts)
			if !reflect.DeepEqual(unsearched(res), tc.want) || partial(res) != (len(tc.want) > 0) || len(res.Hits) != tc.hits {
				t.Fatalf("coverage %+v hits %v, want %+v and %d hits", unsearched(res), hitNames(res), tc.want, tc.hits)
			}
			// Without ns:, what Radar doesn't collect narrowed the search
			// instead of leaving a gap in it.
			if collected := slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByNamespaceCollection); tc.collectorLimited && len(q.NSFilter) == 0 && !collected {
				t.Fatalf("narrowedBy = %v, want collection", res.Coverage.NarrowedBy)
			}
		})
	}
}

func TestUnknownKindKeepsRequestedSpelling(t *testing.T) {
	res, _ := Search(context.Background(), &fakeProvider{}, Parse("kind:NoSuchKindXyz kind:nosuchkindxyz"), Options{})
	want := []UnsearchedKind{{Kind: "NoSuchKindXyz", Reason: "kind_unknown"}}
	if !partial(res) || !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("unknown kind = %+v, want %+v", unsearched(res), want)
	}
}

func TestDynamicNamespaceCoveragePrecedesReadiness(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{
		kinds:        map[schema.GroupVersionResource]string{gvr: "Widget"},
		namespaced:   map[schema.GroupVersionResource]bool{gvr: true},
		observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSyncing}},
	}
	for _, tc := range []struct {
		query string
		want  []UnsearchedKind
	}{
		{"kind:Widget", []UnsearchedKind{{Kind: "Widget", Group: "example.io", Reason: "kind_sync_pending"}}},
		{"kind:Widget ns:b", []UnsearchedKind{{Kind: "*", Reason: "outside_namespace_selection", Namespaces: []string{"b"}}}},
	} {
		q := Parse(tc.query)
		var opts Options
		opts.ScopeNamespaces(q, []string{"a"}, nil)
		res, _ := Search(context.Background(), p, q, opts)
		if !reflect.DeepEqual(unsearched(res), tc.want) || len(p.warmed) != 0 {
			t.Fatalf("%s: coverage %+v, want %+v (warmed %v)", tc.query, unsearched(res), tc.want, p.warmed)
		}
	}
}

// Per-namespace readiness holds in every scope: a stalled namespace reports
// kind_sync_failed for itself only, and a namespace no informer serves is out
// of the collector's scope whatever the other namespaces' state.
func TestDynamicNamespaceReadinessInEveryScope(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	explicit := func(state k8score.DynamicObservationState, reason string) k8score.DynamicResourceObservation {
		return k8score.DynamicResourceObservation{State: state, ReasonCode: reason, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"a", "b"}}
	}
	for _, tc := range []struct {
		name, query string
		namespaces  []string
		want        []UnsearchedKind
	}{
		{"ready namespace only", "kind:Widget", []string{"a"}, []UnsearchedKind{}},
		{"stalled namespace", "kind:Widget", []string{"a", "b"}, []UnsearchedKind{{Kind: "Widget", Group: "example.io", Reason: "kind_sync_timed_out"}}},
		{"all namespaces", "robusta", nil, []UnsearchedKind{{Kind: "Widget", Group: "example.io", Reason: "kind_sync_timed_out"}}},
		{"unwatched namespace", "robusta", []string{"a", "c"}, []UnsearchedKind{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{
				kinds:        map[schema.GroupVersionResource]string{gvr: "Widget"},
				namespaced:   map[schema.GroupVersionResource]bool{gvr: true},
				observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: explicit(k8score.DynamicObservationSyncing, "sync_stalled")},
				namespaceObservations: map[schema.GroupVersionResource]map[string]k8score.DynamicResourceObservation{gvr: {
					"a": explicit(k8score.DynamicObservationSynced, "namespace_partial"),
					"b": explicit(k8score.DynamicObservationSyncing, "sync_stalled"),
					"c": explicit(k8score.DynamicObservationUnwatched, "not_observed"),
				}},
				dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {{Object: map[string]any{"metadata": map[string]any{"name": "robusta-a", "namespace": "a"}}}}},
			}
			res, _ := Search(context.Background(), p, Parse(tc.query), Options{Include: IncludeNone, Namespaces: tc.namespaces})
			if !reflect.DeepEqual(unsearched(res), tc.want) || !reflect.DeepEqual(p.dynamicListNamespaces, []string{"a"}) || len(p.warmed) != 0 {
				t.Fatalf("coverage %+v lists %v warmed %v, want %+v listing only a", unsearched(res), p.dynamicListNamespaces, p.warmed, tc.want)
			}
			if got := hitNames(res); !reflect.DeepEqual(got, []string{"Widget/a/robusta-a"}) {
				t.Fatalf("hits %v", got)
			}
		})
	}
}

// When the caller's namespaces couldn't be determined, namespaced kinds stay
// unsearched (fail closed) and the result says the check failed: neither
// complete nor a denial, and no named namespace is blamed on the caller's RBAC.
func TestFailedNamespaceCheckIsAFailureNotADenial(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []UnsearchedKind
		hits  []string
	}{
		{"robusta", []UnsearchedKind{{Kind: "*", Reason: "user_permission_check_failed"}}, []string{"ClusterRole//robusta-cr", "Node//robusta-node"}},
		{"ns:a robusta", []UnsearchedKind{{Kind: "*", Reason: "user_permission_check_failed"}}, nil},
		{"kind:Pod ns:a robusta", []UnsearchedKind{{Kind: "Pod", Reason: "user_permission_check_failed"}}, nil},
	} {
		q := Parse(tc.query)
		opts := Options{Include: IncludeNone, NamespaceCheckFailed: true}
		opts.ScopeNamespaces(q, []string{}, []string{})
		res, _ := Search(context.Background(), namespaceFixture(), q, opts)
		if !partial(res) || !reflect.DeepEqual(unsearched(res), tc.want) || !reflect.DeepEqual(hitNames(res), tc.hits) {
			t.Fatalf("%s: coverage %+v hits %v, want %+v and %v", tc.query, unsearched(res), hitNames(res), tc.want, tc.hits)
		}
	}
}

type existenceProbe struct {
	*fakeProvider
	asked []string
}

func (p *existenceProbe) NamespaceExists(name string) (bool, bool) {
	p.asked = append(p.asked, name)
	return p.fakeProvider.NamespaceExists(name)
}

// A named namespace that doesn't exist is reported as not found to a caller
// who can see every namespace, never searched to a silent empty answer, and
// never blamed on the pin. Existence is only checked for namespaces the
// caller may see; to anyone else a missing namespace stays not visible.
func TestNamedNamespaceThatDoesNotExist(t *testing.T) {
	for _, tc := range []struct {
		name             string
		query            string
		visible, ceiling []string
		want             []UnsearchedKind
		hits             []string
		asked            []string
	}{
		{"unrestricted", "ns:payments robusta", nil, nil, []UnsearchedKind{{Kind: "*", Reason: "namespace_not_found", Namespaces: []string{"payments"}}}, nil, []string{"payments"}},
		{"unrestricted, one exists", "ns:a ns:payments robusta", nil, nil, []UnsearchedKind{{Kind: "*", Reason: "namespace_not_found", Namespaces: []string{"payments"}}}, []string{"Pod/a/robusta-a"}, []string{"a", "payments"}},
		{"pinned, exists elsewhere", "ns:b robusta", []string{"a"}, nil, []UnsearchedKind{{Kind: "*", Reason: "outside_namespace_selection", Namespaces: []string{"b"}}}, nil, []string{"b"}},
		{"pinned, exists nowhere", "ns:payments robusta", []string{"a"}, nil, []UnsearchedKind{{Kind: "*", Reason: "namespace_not_found", Namespaces: []string{"payments"}}}, nil, []string{"payments"}},
		{"restricted caller", "ns:payments ns:b robusta", []string{"a"}, []string{"a"}, []UnsearchedKind{{Kind: "*", Reason: "user_namespace_not_visible", Namespaces: []string{"b", "payments"}}}, nil, nil},
		// A namespace inside a restricted caller's (possibly stale) RBAC list
		// keeps the pin's entry: existence is never checked for them.
		{"restricted caller, pinned", "ns:gone robusta", []string{"a"}, []string{"a", "gone"}, []UnsearchedKind{{Kind: "*", Reason: "outside_namespace_selection", Namespaces: []string{"gone"}}}, nil, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := namespaceFixture()
			fixture.existingNamespaces = []string{"a", "b"}
			p := &existenceProbe{fakeProvider: fixture}
			q := Parse(tc.query)
			opts := Options{Include: IncludeNone}
			opts.ScopeNamespaces(q, tc.visible, tc.ceiling)
			res, _ := Search(context.Background(), p, q, opts)
			if !reflect.DeepEqual(unsearched(res), tc.want) || !reflect.DeepEqual(hitNames(res), tc.hits) || !reflect.DeepEqual(p.asked, tc.asked) {
				t.Fatalf("coverage %+v hits %v asked %v, want %+v, %v, %v", unsearched(res), hitNames(res), p.asked, tc.want, tc.hits, tc.asked)
			}
		})
	}
	// Without a cluster-wide view of Namespaces, existence is unknown and a
	// named namespace is simply searched.
	q := Parse("ns:payments robusta")
	var opts Options
	opts.ScopeNamespaces(q, nil, nil)
	if res, _ := Search(context.Background(), namespaceFixture(), q, opts); partial(res) {
		t.Fatalf("unknown existence reported: %+v", unsearched(res))
	}
}
