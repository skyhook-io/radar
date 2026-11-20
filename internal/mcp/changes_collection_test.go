package mcp

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

var widgetGVR = schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}

type widgetFixture struct {
	fallbacks []string          // collector refused cluster-wide; watches these namespaces
	extra     []k8s.APIResource // more discovery entries
	objects   []runtime.Object  // more typed objects, such as namespaces
}

// setupWidgets serves a namespaced custom kind Widget in alpha, with an empty
// change history.
func setupWidgets(t *testing.T, f widgetFixture) *k8s.DynamicResourceCache {
	t.Helper()
	initCorrelationStore(t)
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces(f.objects...)...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"namespace": "alpha", "name": "w"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{widgetGVR: "WidgetList", {Group: "serving.knative.dev", Version: "v1", Resource: "services"}: "ServiceList", {Group: "projectcalico.org", Version: "v3", Resource: "networkpolicies"}: "NetworkPolicyList", {Group: "example.test", Version: "v1", Resource: "gadgets"}: "GadgetList"}, widget)
	if f.fallbacks != nil {
		dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
			if a.GetNamespace() == "" {
				return true, nil, collectorRefusal(widgetGVR.GroupResource(), "list", "")
			}
			return false, nil, nil
		})
	}
	resources := append([]k8s.APIResource{{Group: widgetGVR.Group, Version: widgetGVR.Version, Kind: "Widget", Name: widgetGVR.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}, f.extra...)
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	d := k8s.GetDynamicResourceCache()
	if f.fallbacks != nil {
		d.Stop()
		core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: k8s.GetResourceDiscovery().ResourceDiscovery, NamespaceFallbacks: f.fallbacks})
		if err != nil {
			t.Fatal(err)
		}
		d.DynamicResourceCache = core
	}
	return d
}

func TestGetChangesReportsCollectionOfCustomKind(t *testing.T) {
	d := setupWidgets(t, widgetFixture{})
	if d.IsSynced(widgetGVR) {
		t.Fatal("fixture must start with Widget unwatched")
	}

	// Radar wasn't watching Widget: asking starts collection, and the empty
	// answer says Radar has watched only since then.
	response, _ := getChangesForTest(t, context.Background(), getChangesInput{Kind: "Widget", Since: "1h"})
	if len(response.Changes) != 0 || response.Coverage == nil || response.Coverage.Status != resourceapi.CoverageComplete {
		t.Fatalf("changes=%+v coverage=%+v", response.Changes, response.Coverage)
	}
	if !strings.Contains(response.NarrowHint, "Radar has watched widgets only since") {
		t.Fatalf("narrowHint = %q, want when collection started", response.NarrowHint)
	}
	if !d.IsClusterWideSynced(widgetGVR) {
		t.Fatal("asking about Widget must start collecting it, as list_resources does")
	}

	// Watched since before the window: complete, and nothing to qualify.
	time.Sleep(1100 * time.Millisecond)
	response, _ = getChangesForTest(t, context.Background(), getChangesInput{Kind: "widgets", Since: "1s"})
	if response.Coverage == nil || response.Coverage.Status != resourceapi.CoverageComplete || response.NarrowHint != "" {
		t.Fatalf("coverage=%+v narrowHint=%q, want complete and no hint", response.Coverage, response.NarrowHint)
	}
}

// A kind Radar began collecting shortly after it started still gets its own
// sentence when the window starts after Radar did: nothing else says that
// the window's first seconds weren't collected.
func TestGetChangesCollectionStartNearRadarStart(t *testing.T) {
	setupWidgets(t, widgetFixture{})
	timeline.SetObservationStartForTest(time.Now().Add(-30 * time.Second))

	response, _ := getChangesForTest(t, context.Background(), getChangesInput{Kind: "Widget", Since: "20s"})
	if !strings.Contains(response.NarrowHint, "Radar has watched widgets only since") {
		t.Fatalf("window after Radar's start: narrowHint = %q, want the collection start", response.NarrowHint)
	}
	response, _ = getChangesForTest(t, context.Background(), getChangesInput{Kind: "Widget", Since: "1h"})
	if strings.Contains(response.NarrowHint, "Radar has watched widgets") || response.HistoryCoverage == nil {
		t.Fatalf("window before Radar's start: narrowHint = %q coverage = %+v, want only the store-wide boundary", response.NarrowHint, response.HistoryCoverage)
	}
}

// A namespace created after Radar started has no store-wide boundary to
// report, so the kind's own start must still be stated.
func TestGetChangesCollectionStartInNamespaceNewerThanRadar(t *testing.T) {
	fresh := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "fresh", CreationTimestamp: metav1.NewTime(time.Now().Add(-20 * time.Second))}}
	setupWidgets(t, widgetFixture{objects: []runtime.Object{fresh}})
	timeline.SetObservationStartForTest(time.Now().Add(-30 * time.Second))

	response, _ := getChangesForTest(t, context.Background(), getChangesInput{Kind: "Widget", Namespace: "fresh", Since: "1h"})
	if response.HistoryCoverage != nil || !strings.Contains(response.NarrowHint, "Radar has watched widgets only since") {
		t.Fatalf("historyCoverage = %+v narrowHint = %q, want the collection start stated", response.HistoryCoverage, response.NarrowHint)
	}
}

func TestGetChangesCollectionNarrowedToFallbackNamespaces(t *testing.T) {
	setupWidgets(t, widgetFixture{fallbacks: []string{"alpha", "beta"}})
	// A store-wide boundary inside the window must not stand in for the
	// unknown start of per-namespace watches.
	timeline.SetObservationStartForTest(time.Now().Add(-30 * time.Minute))
	response, _ := getChangesForTest(t, context.Background(), getChangesInput{Kind: "Widget"})
	if response.Coverage == nil || !slices.Contains(response.Coverage.NarrowedBy, resourceapi.NarrowedByNamespaceCollection) {
		t.Fatalf("coverage = %+v, want narrowed to Radar's collection", response.Coverage)
	}
	// Complete within the narrowed scope still has to say other namespaces
	// are unknown, and that the start of collection isn't established.
	for _, want := range []string{"Other namespaces are unknown", "can't establish since when"} {
		if !strings.Contains(response.NarrowHint, want) {
			t.Fatalf("narrowHint = %q, want %q", response.NarrowHint, want)
		}
	}
}

func TestGetChangesCollectionNeedsListOnTheKind(t *testing.T) {
	setupWidgets(t, widgetFixture{})
	t.Cleanup(func() { getPermCache().Invalidate() })
	ctx := withRestrictedUser(t, "bob", []string{"alpha"})
	getPermCache().Get("bob", nil).SetCanI("list", widgetGVR.Group, widgetGVR.Resource, "alpha", false)

	response, _ := getChangesForTest(t, ctx, getChangesInput{Kind: "Widget", Namespace: "alpha"})
	if len(response.Changes) != 0 || response.Coverage == nil || len(response.Coverage.Gaps) == 0 || response.Coverage.Gaps[0].Code != readreason.UserForbidden {
		t.Fatalf("coverage = %+v, want user_forbidden: namespace access is not access to the kind", response.Coverage)
	}
	if !strings.Contains(response.NarrowHint, "doesn't allow") || strings.Contains(response.NarrowHint, "list_resources again") {
		t.Fatalf("narrowHint = %q, want the denial without list_resources' own next step", response.NarrowHint)
	}
}

// A caller limited to some namespaces gets a cluster-scoped kind's changes
// when it may list that kind and names no namespace; otherwise not.
func TestGetChangesClusterScopedKindForNamespaceLimitedCaller(t *testing.T) {
	setupWidgets(t, widgetFixture{extra: []k8s.APIResource{{Group: "example.test", Version: "v1", Kind: "Gadget", Name: "gadgets", Namespaced: false, Verbs: []string{"get", "list", "watch"}}}})
	t.Cleanup(func() { getPermCache().Invalidate() })
	for _, e := range []struct{ id, namespace, kind, apiVersion string }{
		{"gadget", "", "Gadget", "example.test/v1"},
		{"widget", "beta", "Widget", "example.test/v1"},
	} {
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: e.id, Timestamp: time.Now().Add(-time.Minute), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: e.kind, APIVersion: e.apiVersion, Namespace: e.namespace, Name: "g", EventType: timeline.EventTypeAdd,
		}); err != nil {
			t.Fatal(err)
		}
	}
	gadgets := func(r getChangesResponseMCP) int {
		n := 0
		for _, c := range r.Changes {
			if c.Kind == "Gadget" {
				n++
			}
		}
		for _, run := range r.Created {
			n += run.Count
		}
		return n
	}

	bob := withRestrictedUser(t, "bob", []string{"alpha"})
	grantClusterRead(t, "bob", "example.test/gadgets")
	response, _ := getChangesForTest(t, bob, getChangesInput{Kind: "Gadget"})
	if gadgets(response) != 1 || response.Coverage == nil || strings.Contains(response.NarrowHint, "not listed here") {
		t.Fatalf("allowed to list gadgets: changes=%+v coverage=%+v narrowHint=%q", response.Changes, response.Coverage, response.NarrowHint)
	}
	// Unfiltered feeds stay within the caller's namespaces.
	if response, _ = getChangesForTest(t, bob, getChangesInput{}); gadgets(response) != 0 {
		t.Fatalf("unfiltered: changes=%+v, want no cluster-scoped rows", response.Changes)
	}

	// No visible namespace at all still reads the kind's cluster scope.
	carol := withRestrictedUser(t, "carol", []string{})
	grantClusterRead(t, "carol", "example.test/gadgets")
	if response, _ = getChangesForTest(t, carol, getChangesInput{Kind: "Gadget"}); gadgets(response) != 1 {
		t.Fatalf("no namespaces: changes=%+v, want the gadget change", response.Changes)
	}

	// Built-in cluster-scoped kinds resolve without discovery, which this
	// fixture doesn't serve Node from.
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
		ID: "node", Timestamp: time.Now().Add(-time.Minute), Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
		Kind: "Node", APIVersion: "v1", Name: "n1", EventType: timeline.EventTypeUpdate,
		Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.unschedulable", OldValue: false, NewValue: true}}},
	}); err != nil {
		t.Fatal(err)
	}
	grantClusterRead(t, "bob", "/nodes")
	if response, _ = getChangesForTest(t, bob, getChangesInput{Kind: "Node"}); len(response.Changes) != 1 {
		t.Fatalf("built-in Node: changes=%+v, want the cordon", response.Changes)
	}

	// A second resource of the same kind and group the caller may not list
	// withholds the group: change rows can't tell the two apart.
	k8s.GetResourceDiscovery().AddAPIResource(k8s.APIResource{Group: "example.test", Version: "v1", Kind: "Gadget", Name: "gadgetsets", Namespaced: false, Verbs: []string{"get", "list", "watch"}})
	getPermCache().Get("bob", nil).SetCanI("list", "example.test", "gadgetsets", "", false)
	if response, _ = getChangesForTest(t, bob, getChangesInput{Kind: "Gadget"}); gadgets(response) != 0 {
		t.Fatalf("one of two same-kind resources denied: changes=%+v, want none", response.Changes)
	}

	dave := withRestrictedUser(t, "dave", []string{"alpha"})
	getPermCache().Get("dave", nil).SetCanI("list", "example.test", "gadgets", "", false)
	response, _ = getChangesForTest(t, dave, getChangesInput{Kind: "Gadget"})
	if gadgets(response) != 0 || response.Coverage != nil {
		t.Fatalf("not allowed to list gadgets: changes=%+v coverage=%+v", response.Changes, response.Coverage)
	}
}

func TestGetChangesCollectionHints(t *testing.T) {
	extra := widgetFixture{extra: []k8s.APIResource{
		{Group: "example.test", Version: "v1", Kind: "Gadget", Name: "gadgets", Namespaced: false, Verbs: []string{"get", "list", "watch"}},
		{Group: "one.example", Version: "v1", Kind: "Twin", Name: "twins", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "two.example", Version: "v1", Kind: "Twin", Name: "twins", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "", Version: "v1", Kind: "Service", Name: "services", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "serving.knative.dev", Version: "v1", Kind: "Service", Name: "services", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		// Discovery here lacks the built-in NetworkPolicy, as partial
		// discovery can; its history rows still match the kind.
		{Group: "projectcalico.org", Version: "v3", Kind: "NetworkPolicy", Name: "networkpolicies", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}}
	for _, tc := range []struct {
		name  string
		input getChangesInput
		want  string
	}{
		{"unknown kind", getChangesInput{Kind: "Nonesuch"}, `doesn't know a resource kind named "Nonesuch"`},
		{"two custom groups", getChangesInput{Kind: "Twin"}, "more than one API group (one.example, two.example)"},
		// Beside core Service, Knative Service is checked by itself and
		// named by group; no coverage field, since rows mix both.
		{"custom kind sharing a core name", getChangesInput{Kind: "Service", Since: "1h"}, "Radar has watched services.serving.knative.dev only since"},
		{"custom namesake of a built-in missing from discovery", getChangesInput{Kind: "NetworkPolicy", Since: "1h"}, "networkpolicies.projectcalico.org"},
		// Native Helm history needs no watch, so the name is never unknown.
		{"helm without flux", getChangesInput{Kind: "HelmRelease"}, ""},
		// A short name the history query accepts is not an unknown kind.
		{"built-in short name", getChangesInput{Kind: "cm"}, ""},
		{"cluster-scoped kind with namespace", getChangesInput{Kind: "Gadget", Namespace: "alpha"}, "gadgets are cluster-scoped"},
		{"name without kind", getChangesInput{Name: "w"}, "pass kind= to check whether Radar collects the kind"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			// A discovery miss refreshes discovery, which the fake client empties.
			setupWidgets(t, extra)
			response, _ := getChangesForTest(t, context.Background(), tc.input)
			// get_changes has no group argument to suggest.
			if strings.Contains(response.NarrowHint, "pass group") {
				t.Fatalf("narrowHint = %q suggests an argument get_changes doesn't have", response.NarrowHint)
			}
			if response.Coverage != nil || !strings.Contains(response.NarrowHint, tc.want) || (tc.want == "" && strings.Contains(response.NarrowHint, "doesn't know")) {
				t.Fatalf("coverage=%+v narrowHint=%q, want no coverage and %q", response.Coverage, response.NarrowHint, tc.want)
			}
		})
	}
}

func TestGetChangesBuiltinKindHasNoCollectionCoverage(t *testing.T) {
	setupWidgets(t, widgetFixture{extra: []k8s.APIResource{{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}})
	response, _ := getChangesForTest(t, context.Background(), getChangesInput{Kind: "Deployment"})
	if response.Coverage != nil || response.NarrowHint != "" {
		t.Fatalf("coverage=%+v narrowHint=%q, want neither for a built-in kind", response.Coverage, response.NarrowHint)
	}
}
