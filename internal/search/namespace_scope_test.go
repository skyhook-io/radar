package search

import (
	"context"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestScopeNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name              string
		query             string
		visible, ceiling  []string
		wantScan          []string
		wantExcluded      []string
		wantScoped        []string
		wantNoNamespaceOK bool
	}{
		{name: "unrestricted", query: "x"},
		{name: "rbac ceiling is not a gap", query: "x", visible: []string{"a", "b"}, ceiling: []string{"b", "a"}, wantScan: []string{"a", "b"}},
		{name: "pick narrows", query: "x", visible: []string{"b", "a"}, ceiling: nil, wantScan: []string{"b", "a"}, wantScoped: []string{"a", "b"}},
		{name: "pick narrows a restricted ceiling", query: "x", visible: []string{"a"}, ceiling: []string{"a", "b"}, wantScan: []string{"a"}, wantScoped: []string{"a"}},
		{name: "terms within pick", query: "x ns:a", visible: []string{"a"}, ceiling: nil, wantScan: []string{"a"}},
		{name: "terms outside pick", query: "x ns:a ns:b ns:b", visible: []string{"a"}, ceiling: nil, wantScan: []string{"a"}, wantExcluded: []string{"b"}},
		{name: "every term excluded", query: "x ns:b ns:c", visible: []string{"a"}, ceiling: []string{"a"}, wantScan: []string{}, wantExcluded: []string{"b", "c"}, wantNoNamespaceOK: true},
		{name: "unrestricted terms", query: "x ns:b", wantScan: []string{"b"}},
		{name: "no namespace access", query: "x", visible: []string{}, ceiling: []string{}, wantScan: []string{}, wantNoNamespaceOK: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var opts Options
			opts.ScopeNamespaces(Parse(tc.query), tc.visible, tc.ceiling)
			if !reflect.DeepEqual(opts.Namespaces, tc.wantScan) || !reflect.DeepEqual(opts.ExcludedNamespaces, tc.wantExcluded) ||
				!reflect.DeepEqual(opts.ScopedNamespaces, tc.wantScoped) || opts.NamespaceExcluded != tc.wantNoNamespaceOK {
				t.Fatalf("got scan=%#v excluded=%#v scoped=%#v noNamespace=%v", opts.Namespaces, opts.ExcludedNamespaces, opts.ScopedNamespaces, opts.NamespaceExcluded)
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
	for _, gap := range res.Unsearched {
		if gap.Reason == reason {
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
			if denied := gapsWithReason(res, "rbac_denied"); len(denied) != 1 || denied[0].Kind != "Node" {
				t.Fatalf("cluster-scoped gate: %+v", res.Unsearched)
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
	if got, want := hitNames(res), []string{"Pod/a/robusta-a"}; !reflect.DeepEqual(got, want) || res.Partial {
		t.Fatalf("broad ns: query = %v %+v, want %v", got, res.Unsearched, want)
	}

	q = Parse("kind:ClusterRole kind:Pod ns:a robusta")
	opts = Options{Include: IncludeNone}
	opts.ScopeNamespaces(q, nil, nil)
	res, _ = Search(context.Background(), namespaceFixture(), q, opts)
	if got, want := hitNames(res), []string{"ClusterRole//robusta-cr", "Pod/a/robusta-a"}; !reflect.DeepEqual(got, want) || res.Partial {
		t.Fatalf("named cluster-scoped kind with ns: = %v %+v, want %v", got, res.Unsearched, want)
	}
}

func TestNamespaceCoverageCollapsesOnBroadQueries(t *testing.T) {
	p := namespaceFixture()
	q := Parse("ns:b ns:c robusta")
	opts := Options{Include: IncludeNone}
	opts.ScopeNamespaces(q, []string{"a"}, []string{"a"})
	res, _ := Search(context.Background(), p, q, opts)
	want := []UnsearchedKind{{Kind: "*", Reason: "namespace_excluded", Namespaces: []string{"b", "c"}}}
	if !res.Partial || len(res.Hits) != 0 || !reflect.DeepEqual(res.Unsearched, want) {
		t.Fatalf("broad excluded coverage = %+v, want %+v", res.Unsearched, want)
	}

	q = Parse("kind:Pod kind:ClusterRole ns:a ns:b robusta")
	opts = Options{Include: IncludeNone}
	opts.ScopeNamespaces(q, []string{"a"}, []string{"a"})
	res, _ = Search(context.Background(), p, q, opts)
	want = []UnsearchedKind{{Kind: "Pod", Reason: "namespace_excluded", Namespaces: []string{"b"}}}
	if got := hitNames(res); !reflect.DeepEqual(res.Unsearched, want) || len(got) != 2 {
		t.Fatalf("explicit excluded coverage = %v %+v, want %+v", got, res.Unsearched, want)
	}

	p.typedReasons = map[string]string{"Pod": "namespace_scope", "ConfigMap": "namespace_scope", "Secret": "namespace_scope"}
	q = Parse("robusta")
	opts = Options{Include: IncludeNone}
	opts.ScopeNamespaces(q, []string{"a"}, nil)
	res, _ = Search(context.Background(), p, q, opts)
	want = []UnsearchedKind{{Kind: "*", Reason: "namespace_scope", Namespaces: []string{"a"}}}
	if !reflect.DeepEqual(res.Unsearched, want) {
		t.Fatalf("broad scope coverage = %+v, want %+v", res.Unsearched, want)
	}
}

func TestSelectionCoverageNamesOnlyNamespacedKinds(t *testing.T) {
	for _, tc := range []struct {
		query string
		want  []UnsearchedKind
	}{
		{"kind:Pod robusta", []UnsearchedKind{{Kind: "Pod", Reason: "namespace_scope", Namespaces: []string{"a"}}}},
		{"kind:ClusterRole robusta", []UnsearchedKind{}},
	} {
		q := Parse(tc.query)
		opts := Options{Include: IncludeNone}
		opts.ScopeNamespaces(q, []string{"a"}, nil)
		res, _ := Search(context.Background(), namespaceFixture(), q, opts)
		if !reflect.DeepEqual(res.Unsearched, tc.want) || res.Partial != (len(tc.want) > 0) || len(res.Hits) != 1 {
			t.Fatalf("%s: coverage %+v hits %v, want %+v", tc.query, res.Unsearched, hitNames(res), tc.want)
		}
	}
}

func TestUnknownKindKeepsRequestedSpelling(t *testing.T) {
	res, _ := Search(context.Background(), &fakeProvider{}, Parse("kind:NoSuchKindXyz kind:nosuchkindxyz"), Options{})
	want := []UnsearchedKind{{Kind: "NoSuchKindXyz", Reason: "not_indexed"}}
	if !res.Partial || !reflect.DeepEqual(res.Unsearched, want) {
		t.Fatalf("unknown kind = %+v, want %+v", res.Unsearched, want)
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
		{"kind:Widget", []UnsearchedKind{
			{Kind: "Widget", Group: "example.io", Reason: "namespace_scope", Namespaces: []string{"a"}},
			{Kind: "Widget", Group: "example.io", Reason: "syncing"},
		}},
		{"kind:Widget ns:b", []UnsearchedKind{{Kind: "Widget", Group: "example.io", Reason: "namespace_excluded", Namespaces: []string{"b"}}}},
	} {
		q := Parse(tc.query)
		var opts Options
		opts.ScopeNamespaces(q, []string{"a"}, nil)
		res, _ := Search(context.Background(), p, q, opts)
		if !reflect.DeepEqual(res.Unsearched, tc.want) || len(p.warmed) != 0 {
			t.Fatalf("%s: coverage %+v, want %+v (warmed %v)", tc.query, res.Unsearched, tc.want, p.warmed)
		}
	}
}
