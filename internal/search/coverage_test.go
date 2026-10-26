package search

import (
	"context"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

func TestSearchRoleBindingFilter(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{"rolebindings": {&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "binding", Namespace: "team-a"}, Subjects: []rbacv1.Subject{{Kind: "ServiceAccount", Name: "worker"}}}}}}
	f, err := filter.CompileObjectFilter(`has(object.subjects) && object.subjects.exists(s, s.name == "worker")`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := Search(context.Background(), p, Parse("kind:RoleBinding"), Options{Filter: f})
	if err != nil || len(res.Hits) != 1 || partial(res) {
		t.Fatalf("RBAC search: %+v %v", res, err)
	}
}

func TestSearchCoverageColdWarmAndUnknown(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationUnwatched}}}
	res, _ := Search(context.Background(), p, Parse(""), Options{})
	if partial(res) || len(unsearched(res)) != 0 || len(p.warmed) != 0 {
		t.Fatalf("a broad search treats a cold kind as scope: %+v warmed=%v", res, p.warmed)
	}
	res, _ = Search(context.Background(), p, Parse("kind:Widget"), Options{})
	if partial(res) || len(p.warmed) != 1 {
		t.Fatalf("warm: %+v warmed=%v", res, p.warmed)
	}
	res, _ = Search(context.Background(), p, Parse("kind:DoesNotExist"), Options{})
	if !partial(res) || len(unsearched(res)) != 1 || unsearched(res)[0].Reason != "kind_unknown" {
		t.Fatalf("unknown: %+v", res)
	}
}

func TestSearchFilterFailuresAreBoundedAndSecretSafe(t *testing.T) {
	pods := make([]runtime.Object, 25)
	for i := range pods {
		pods[i] = newPod("team-a", fmt.Sprintf("p-%02d", i), "image", nil)
	}
	p := &fakeProvider{typed: map[string][]runtime.Object{"pods": pods}}
	f, _ := filter.CompileObjectFilter(`object.nonexistent == 1`)
	res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{Filter: f})
	var failed *resourceapi.ObjectSet
	for _, gap := range res.Coverage.Gaps {
		if gap.Code == readreason.FilterEvalFailed {
			failed = gap.Objects
		}
	}
	if !partial(res) || res.FilterErrors != 25 || failed == nil || failed.Count != 25 || failed.Names != nil || res.FilterErrorSample == "" {
		t.Fatalf("failed refs: %+v", res)
	}
	p.typed = map[string][]runtime.Object{"secrets": {&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "credential", Namespace: "team-a"}, Data: map[string][]byte{"token": []byte("sensitive")}, StringData: map[string]string{"other": "sensitive"}}}}
	f, _ = filter.CompileObjectFilter(`!has(object.data) && !has(object.stringData) && metadata.namespace == object.metadata.namespace`)
	res, _ = Search(context.Background(), p, Parse("kind:Secret"), Options{Filter: f})
	if partial(res) || len(res.Hits) != 1 {
		t.Fatalf("Secret binding: %+v", res)
	}
}

func TestSearchCoverageReadFailuresAndPartialNamespaces(t *testing.T) {
	for _, reason := range []string{"kind_sync_pending", "kind_sync_timed_out"} {
		t.Run(reason, func(t *testing.T) {
			p := &fakeProvider{typedReasons: map[string]string{"Pod": reason}, typed: map[string][]runtime.Object{"pods": {newPod("a", "visible", "x", nil)}}}
			res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{})
			if !partial(res) || len(unsearched(res)) != 1 || string(unsearched(res)[0].Reason) != reason || len(res.Hits) != 0 {
				t.Fatalf("read coverage: %+v", res)
			}
		})
	}
	// Radar's collection scope is reported only for a namespace the query
	// names.
	p := &fakeProvider{typedReasons: map[string]string{"Pod": "radar_namespace_not_collected"}, typed: map[string][]runtime.Object{"pods": {newPod("a", "visible", "x", nil)}}}
	if res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{}); partial(res) || len(unsearched(res)) != 0 || len(res.Hits) != 0 {
		t.Fatalf("kind: alone reported collector scope: %+v", res)
	}
	q := Parse("kind:Pod ns:a")
	opts := Options{}
	opts.Selection = SelectNamespaces(p, q, readscope.NamespaceAccess{Namespaces: nil, Checked: true}, nil)
	want := []UnsearchedKind{{Kind: "*", Reason: "radar_namespace_not_collected", Namespaces: []string{"a"}}}
	if res, _ := Search(context.Background(), p, q, opts); !partial(res) || !reflect.DeepEqual(unsearched(res), want) || len(res.Hits) != 0 {
		t.Fatalf("named namespace outside collection: %+v, want %+v", unsearched(res), want)
	}
	for _, tc := range []struct {
		err    error
		reason string
	}{
		{fmt.Errorf("forbidden: pods"), "radar_forbidden"},
		{apierrors.NewUnauthorized("token expired"), "radar_credentials_rejected"},
		{fmt.Errorf("broken list"), "read_error"},
	} {
		p := &fakeProvider{listErrors: map[string]error{"pods": tc.err}}
		res, _ := Search(context.Background(), p, Parse("kind:Pod"), Options{})
		if !partial(res) || len(unsearched(res)) != 1 || string(unsearched(res)[0].Reason) != tc.reason {
			t.Fatalf("list failure: %+v", res)
		}
	}
	rec := &recordingProvider{fakeProvider: &fakeProvider{}}
	res, _ := Search(context.Background(), rec, Parse("kind:Role"), Options{Selection: readscope.Selection{Namespaces: []string{"a", "b"}}, TypedKindAccess: func([]string, string, string) ([]string, []string, []string) {
		return []string{"a"}, []string{"b"}, nil
	}})
	if !partial(res) || len(unsearched(res)) != 1 || unsearched(res)[0].Reason != "user_forbidden" || len(rec.listedWith["roles"]) != 1 || rec.listedWith["roles"][0] != "a" {
		t.Fatalf("namespace narrowing: %+v lists=%v", res, rec.listedWith)
	}
	res, _ = Search(context.Background(), rec, Parse("kind:Role"), Options{TypedKindAccess: func([]string, string, string) ([]string, []string, []string) { return []string{}, []string{""}, nil }})
	if !partial(res) || res.Searched != 0 {
		t.Fatalf("empty override: %+v", res)
	}
}

func TestSearchDynamicCoverageDoesNotWarmDeniedAndRetainsGoodNamespaces(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, namespaced: map[schema.GroupVersionResource]bool{gvr: false}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationUnwatched}}}
	res, _ := Search(context.Background(), p, Parse("kind:Widget"), Options{CanReadClusterScoped: func(kind, group, resource string) (bool, bool) { return false, true }})
	if !partial(res) || unsearched(res)[0].Reason != "user_forbidden" || len(p.warmed) != 0 || len(p.dynamicListNamespaces) != 0 {
		t.Fatalf("denied kind warmed or listed: %+v %+v", res, p)
	}
	for _, tc := range []struct {
		state       k8score.DynamicObservationState
		query       string
		kind        string
		reason      string
		count       int
		wantPartial bool
	}{
		{k8score.DynamicObservationDenied, "kind:Widget", "Widget", "radar_forbidden", 0, true},
		{k8score.DynamicObservationDenied, "", "", "", 0, false},
		{k8score.DynamicObservationSyncing, "", "*", "kind_sync_pending", 1, true},
		{k8score.DynamicObservationUnsupported, "kind:Widget", "Widget", "kind_not_watchable", 0, true},
		{k8score.DynamicObservationUnsupported, "", "", "", 0, false},
		{k8score.DynamicObservationDeferred, "", "", "", 0, false},
	} {
		p.observations[gvr] = k8score.DynamicResourceObservation{State: tc.state}
		res, _ = Search(context.Background(), p, Parse(tc.query), Options{})
		if partial(res) != tc.wantPartial || (tc.wantPartial && (len(unsearched(res)) != 1 || unsearched(res)[0].Kind != tc.kind || string(unsearched(res)[0].Reason) != tc.reason || unsearched(res)[0].Count != tc.count)) {
			t.Fatalf("dynamic state %s, query %q: %+v", tc.state, tc.query, res)
		}
		// A kind Radar is refused narrows a broad search; a kind no search can
		// watch is outside every search and narrows none.
		if tc.query == "" && tc.state == k8score.DynamicObservationDenied && !slices.Equal(res.Coverage.NarrowedBy, []resourceapi.Narrowing{resourceapi.NarrowedByKindCollection}) {
			t.Fatalf("refused kind narrowedBy = %v", res.Coverage.NarrowedBy)
		}
		if tc.query == "" && tc.state == k8score.DynamicObservationUnsupported && len(res.Coverage.NarrowedBy) != 0 {
			t.Fatalf("list-only kind narrowedBy = %v", res.Coverage.NarrowedBy)
		}
	}
	p.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}
	p.namespaced[gvr] = true
	p.dynamicErrors = map[string]error{"a": fmt.Errorf("broken list")}
	res, _ = Search(context.Background(), p, Parse("kind:Widget"), Options{Selection: readscope.Selection{Namespaces: []string{"a", "b"}}})
	if !partial(res) || len(p.dynamicListNamespaces) != 2 || unsearched(res)[0].Reason != "read_error" {
		t.Fatalf("dynamic partial list: %+v lists=%v", res, p.dynamicListNamespaces)
	}
}

func TestSearchObjectClusterNamespaceAndGuardedArgo(t *testing.T) {
	p := &fakeProvider{typed: map[string][]runtime.Object{"clusterroles": {&rbacv1.ClusterRole{ObjectMeta: metav1.ObjectMeta{Name: "reader"}}}}}
	f, _ := filter.CompileObjectFilter(`metadata.namespace == "" && object.metadata.namespace == "" && !("app" in labels)`)
	res, _ := Search(context.Background(), p, Parse("kind:ClusterRole"), Options{Filter: f})
	if partial(res) || len(res.Hits) != 1 {
		t.Fatalf("cluster namespace: %+v", res)
	}
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	p = &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Application"}, dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {
		{Object: map[string]any{"kind": "Application", "metadata": map[string]any{"name": "single"}, "spec": map[string]any{"source": map[string]any{"repoURL": "https://github.com/one"}}}},
		{Object: map[string]any{"kind": "Application", "metadata": map[string]any{"name": "multi"}, "spec": map[string]any{"sources": []any{map[string]any{"repoURL": "https://github.com/two"}}}}},
	}}}
	f, err := filter.CompileObjectFilter(`kind == "Application" && has(spec.sources) && spec.sources.exists(s, has(s.repoURL) && s.repoURL.contains("github.com"))`)
	if err != nil {
		t.Fatal(err)
	}
	res, _ = Search(context.Background(), p, Parse("kind:Application"), Options{Filter: f})
	if partial(res) || len(res.Hits) != 1 || res.Hits[0].Name != "multi" {
		t.Fatalf("Argo guard: %+v", res)
	}
}

// capFixture serves count cluster-scoped kinds named prefix00, prefix01, …
// observed as observation. Each kind's group sorts in the reverse of its name,
// so discovery hands them to Search last-kind-first and folding cannot lean on
// discovery order.
func capFixture(p *fakeProvider, prefix string, count int, observation k8score.DynamicResourceObservation) {
	for i := range count {
		gvr := schema.GroupVersionResource{Group: fmt.Sprintf("g%02d.%s.example.io", count-1-i, strings.ToLower(prefix)), Version: "v1", Resource: fmt.Sprintf("%s%02ds", strings.ToLower(prefix), i)}
		p.kinds[gvr] = fmt.Sprintf("%s%02d", prefix, i)
		p.namespaced[gvr] = false
		p.observations[gvr] = observation
	}
}

func newCapProvider() *fakeProvider {
	return &fakeProvider{kinds: map[schema.GroupVersionResource]string{}, namespaced: map[schema.GroupVersionResource]bool{}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{}}
}

// capNamed is the named entries capFixture produces for kinds first..last.
func capNamed(prefix string, count, first, last int, reason readreason.Code) []UnsearchedKind {
	var gaps []UnsearchedKind
	for i := first; i <= last; i++ {
		gaps = append(gaps, UnsearchedKind{Kind: fmt.Sprintf("%s%02d", prefix, i), Group: fmt.Sprintf("g%02d.%s.example.io", count-1-i, strings.ToLower(prefix)), Reason: reason})
	}
	return gaps
}

func denyDenied(kind, _, _ string) (bool, bool) {
	return !strings.HasPrefix(kind, "Denied"), true
}

var (
	synced  = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}
	syncing = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing}
	stalled = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing, ReasonCode: "sync_stalled"}
)

// The caller's access and Radar's setup are a search's scope: a broad search
// names none of it, however many kinds it covers, while a kind: term gets an
// exact entry for every kind it names.
func TestBroadSearchNamesNoScopeReasons(t *testing.T) {
	p := newCapProvider()
	capFixture(p, "Denied", 25, synced)
	capFixture(p, "Blocked", 3, k8score.DynamicResourceObservation{State: k8score.DynamicObservationDenied})
	capFixture(p, "Cold", 2, k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnwatched})
	capFixture(p, "Listonly", 2, k8score.DynamicResourceObservation{State: k8score.DynamicObservationUnsupported})

	res, err := Search(context.Background(), p, Parse("robusta"), Options{CanReadClusterScoped: denyDenied})
	if err != nil || partial(res) || !reflect.DeepEqual(unsearched(res), []UnsearchedKind{}) || len(p.warmed) != 0 {
		t.Fatalf("broad search reported scope: %+v %v warmed=%v", res, err, p.warmed)
	}

	var terms []string
	for i := range 25 {
		terms = append(terms, fmt.Sprintf("kind:Denied%02d", i))
	}
	res, _ = Search(context.Background(), p, Parse(strings.Join(terms, " ")+" robusta"), Options{CanReadClusterScoped: denyDenied})
	if want := capNamed("Denied", 25, 0, 24, readreason.UserForbidden); !partial(res) || !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("kind: query must name every named kind, got %d: %+v", len(unsearched(res)), unsearched(res))
	}
	res, _ = Search(context.Background(), p, Parse("kind:Blocked01 kind:Listonly00"), Options{CanReadClusterScoped: denyDenied})
	want := append(capNamed("Blocked", 3, 1, 1, readreason.RadarForbidden), capNamed("Listonly", 2, 0, 0, readreason.KindNotWatchable)...)
	if !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("named setup reasons = %+v, want %+v", unsearched(res), want)
	}
}

// Loading and failed kinds are always reported, folded: pending kinds become
// one counted entry, and a failure reason names its kinds when there are at
// most five and otherwise counts them.
func TestBroadSearchFoldsTransientAndFailureReasons(t *testing.T) {
	p := newCapProvider()
	capFixture(p, "Pending", 22, syncing)
	capFixture(p, "Failed", 8, stalled)
	capFixture(p, "Broken", 2, stalled)

	res, _ := Search(context.Background(), p, Parse("robusta"), Options{})
	want := []UnsearchedKind{
		{Kind: "*", Reason: readreason.KindSyncPending, Count: 22},
		{Kind: "*", Reason: readreason.KindSyncTimedOut, Count: 10},
	}
	if !partial(res) || !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("folded coverage (%d entries): %+v\nwant %+v", len(unsearched(res)), unsearched(res), want)
	}
	again, _ := Search(context.Background(), p, Parse("robusta"), Options{})
	if !reflect.DeepEqual(again.Coverage, res.Coverage) {
		t.Fatalf("repeat search changed coverage: %+v", unsearched(again))
	}

	res, _ = Search(context.Background(), p, Parse("kind:Pending03 kind:Failed07"), Options{})
	want = append(capNamed("Failed", 8, 7, 7, readreason.KindSyncTimedOut), capNamed("Pending", 22, 3, 3, readreason.KindSyncPending)...)
	if !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("named kinds stay exact: %+v, want %+v", unsearched(res), want)
	}
}

func TestFailureFoldOrdersByKindThenGroup(t *testing.T) {
	all := []UnsearchedKind{
		{Kind: "Alpha", Group: "a.example.io", Reason: readreason.ReadError},
		{Kind: "Alpha", Group: "b.example.io", Reason: readreason.ReadError},
		{Kind: "Beta", Group: "", Reason: readreason.ReadError},
		{Kind: "Beta", Group: "a.example.io", Reason: readreason.ReadError},
		{Kind: "Delta", Group: "a.example.io", Reason: readreason.ReadError},
		{Kind: "Gamma", Group: "a.example.io", Reason: readreason.ReadError},
		{Kind: "Gamma", Group: "b.example.io", Reason: readreason.ReadError},
	}
	for _, order := range [][]int{{0, 1, 2, 3, 4}, {4, 3, 2, 1, 0}, {3, 1, 4, 0, 2}} {
		cov := newCoverage(Parse("robusta"))
		for _, i := range order {
			cov.kind(all[i].Kind, all[i].Group, all[i].Reason)
		}
		wantKinds := make([]resourceapi.KindRef, 5)
		for i, k := range all[:5] {
			wantKinds[i] = resourceapi.KindRef{Kind: k.Kind, Group: k.Group}
		}
		want := []resourceapi.Gap{{Code: readreason.ReadError, Kinds: &resourceapi.KindSet{Count: 5, Names: wantKinds}, Namespaces: resourceapi.AllNames()}}
		if got := cov.result(); !reflect.DeepEqual(got, want) {
			t.Fatalf("input order %v: %+v, want %+v", order, got, want)
		}

	}
	// Past five, the reason counts its kinds and names none.
	cov := newCoverage(Parse("robusta"))
	for _, k := range all {
		cov.kind(k.Kind, k.Group, k.Reason)
	}
	if got := cov.result(); !reflect.DeepEqual(got, []resourceapi.Gap{{Code: readreason.ReadError, Kinds: &resourceapi.KindSet{Count: 7}, Namespaces: resourceapi.AllNames()}}) {
		t.Fatalf("over the limit: %+v", got)
	}
}

// A kind seen pending in several namespaces is one kind still loading.
func TestPendingCountCountsDistinctKinds(t *testing.T) {
	cov := newCoverage(Parse("robusta"))
	for _, gap := range []UnsearchedKind{{Kind: "Widget", Group: "a.example.io"}, {Kind: "Widget", Group: "a.example.io"}, {Kind: "Widget", Group: "b.example.io"}, {Kind: "Pod"}} {
		cov.kind(gap.Kind, gap.Group, readreason.KindSyncPending)
	}
	if got, want := unsearched(Result{Coverage: resourceapi.SearchCoverage{Gaps: cov.result()}}), []UnsearchedKind{{Kind: "*", Reason: readreason.KindSyncPending, Count: 3}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pending fold = %+v, want %+v", got, want)
	}
}

// Until discovery is ready a kind: term that matched nothing may simply not be
// discovered yet, so the answer is "still loading", never "no such kind", and
// the pending entry carries no count because the catalog is unknown.
func TestLoadingCatalogIsPendingNotUnknown(t *testing.T) {
	p := &fakeProvider{catalogErr: fmt.Errorf("%w: discovery", k8s.ErrDynamicNotReady), typedReasons: map[string]string{"ConfigMap": "kind_sync_pending"}}
	want := []UnsearchedKind{{Kind: "*", Reason: readreason.KindSyncPending}}
	for _, query := range []string{"kind:Certificate", "robusta"} {
		if res, _ := Search(context.Background(), p, Parse(query), Options{}); !partial(res) || !reflect.DeepEqual(unsearched(res), want) {
			t.Fatalf("%s while discovery loads = %+v, want %+v", query, unsearched(res), want)
		}
	}
}

// When some API groups failed discovery, an unmatched kind: term is still
// reported as unknown, but with the failure beside it: the kind may live in a
// group Radar couldn't discover. A broad search records kind-collection narrowing.
func TestIncompleteDiscoveryQualifiesUnknownKinds(t *testing.T) {
	p := &fakeProvider{catalogErr: errIncompleteDiscovery}
	if res, _ := Search(context.Background(), p, Parse("robusta"), Options{}); partial(res) || len(unsearched(res)) != 0 || !slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByKindCollection) {
		t.Fatalf("broad search did not qualify incomplete discovery: %+v", res.Coverage)
	}
	res, _ := Search(context.Background(), p, Parse("kind:Certificate kind:Pod"), Options{})
	want := []UnsearchedKind{{Kind: "*", Reason: readreason.ReadError}, {Kind: "Certificate", Reason: readreason.KindUnknown}}
	if !partial(res) || !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("unmatched kind with incomplete discovery = %+v, want %+v", unsearched(res), want)
	}
}

func TestDeferredKindsSeparateSetupFromFailure(t *testing.T) {
	for _, tc := range []struct {
		code, broad string
	}{
		{"resource_count_exceeds_eager_limit", ""},
		{"legacy_api_deferred", ""},
		{"eager_watch_pending", "kind_sync_pending"},
		{"scope_probe_incomplete", "read_error"},
		{"scope_probe_failed", "read_error"},
		{"resource_count_probe_failed", ""},
	} {
		t.Run(tc.code, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
			p := &fakeProvider{
				kinds:        map[schema.GroupVersionResource]string{gvr: "Widget"},
				namespaced:   map[schema.GroupVersionResource]bool{gvr: false},
				observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationDeferred, ReasonCode: tc.code}},
			}
			res, _ := Search(context.Background(), p, Parse("robusta"), Options{})
			if got := reasonsOf(res); !reflect.DeepEqual(got, nonEmpty(tc.broad)) || len(p.warmed) != 0 {
				t.Fatalf("broad: %+v warmed=%v", unsearched(res), p.warmed)
			}
			res, _ = Search(context.Background(), p, Parse("kind:Widget"), Options{})
			if partial(res) || len(p.warmed) != 1 {
				t.Fatalf("named kind was not warmed: %+v warmed=%v", unsearched(res), p.warmed)
			}
		})
	}
}

func reasonsOf(res Result) []string {
	var out []string
	for _, gap := range unsearched(res) {
		out = append(out, string(gap.Reason))
	}
	return out
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}

// A probe the client-side rate limiter refused never reached the API server:
// warming a named kind that hit it is still loading, not a read failure.
func TestRateLimitedWarmIsPending(t *testing.T) {
	err := fmt.Errorf("no access to widgets.example.io/v1: %w", fmt.Errorf("client rate limiter Wait returned an error: %w", fmt.Errorf("rate: Wait(n=1) would exceed context deadline")))
	if got := listErrorReason(err); got != readreason.KindSyncPending {
		t.Fatalf("rate-limited warm = %q, want kind_sync_pending", got)
	}
}

func TestStalledWatchDoesNotOfferKindRetry(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{
		kinds:        map[schema.GroupVersionResource]string{gvr: "Widget"},
		namespaced:   map[schema.GroupVersionResource]bool{gvr: false},
		observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSyncing, ReasonCode: "sync_stalled"}},
	}
	for _, query := range []string{"renewal", "kind:Widget renewal"} {
		res, _ := Search(context.Background(), p, Parse(query), Options{})
		if len(unsearched(res)) != 1 || unsearched(res)[0].Reason != readreason.KindSyncTimedOut || len(p.warmed) != 0 {
			t.Fatalf("%s: %+v warm=%v", query, unsearched(res), p.warmed)
		}
	}
}
