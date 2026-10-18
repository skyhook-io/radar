package search

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

func TestCELUsesCachedFieldsWithoutDetailPruning(t *testing.T) {
	pod := newPod("team-a", "worker", "image", nil)
	pod.UID = types.UID("pod-uid")
	pod.Finalizers = []string{"example.io/hold"}
	pod.Spec.NodeName = "node-1"
	pod.Status.PodIP = "10.1.2.3"
	pod.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "TOKEN", Value: "secret-token-value"}}
	deployment := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "worker", Generation: 5}, Status: appsv1.DeploymentStatus{ObservedGeneration: 4}}
	for _, tc := range []struct {
		obj              runtime.Object
		kind, expression string
	}{
		{pod, "Pod", `spec.nodeName == "node-1" && object.spec.nodeName == spec.nodeName`},
		{pod, "Pod", `status.podIP == "10.1.2.3"`},
		{pod, "Pod", `metadata.uid == "pod-uid"`},
		{pod, "Pod", `has(metadata.finalizers)`},
		{pod, "Pod", `spec.containers[0].env[0].value == "secret-token-value"`},
		{newPod("team-a", "pending", "image", nil), "Pod", `!has(spec.nodeName)`},
		{deployment, "Deployment", `status.observedGeneration < metadata.generation`},
	} {
		t.Run(tc.expression, func(t *testing.T) {
			f, err := filter.CompileObjectFilter(tc.expression)
			if err != nil {
				t.Fatal(err)
			}
			before := tc.obj.DeepCopyObject()
			act, err := objectActivation(tc.obj, tc.kind)
			if err != nil {
				t.Fatal(err)
			}
			if got, err := f.Match(act); err != nil || !got {
				t.Fatalf("typed predicate: %v, %v", got, err)
			}
			m, err := runtime.DefaultUnstructuredConverter.ToUnstructured(tc.obj)
			if err != nil {
				t.Fatal(err)
			}
			u := &unstructured.Unstructured{Object: m}
			uBefore := u.DeepCopy()
			if got, err := f.Match(unstructuredActivation(u, tc.kind)); err != nil || !got {
				t.Fatalf("unstructured predicate: %v, %v", got, err)
			}
			if !reflect.DeepEqual(tc.obj, before) || !reflect.DeepEqual(u, uBefore) {
				t.Fatal("activation mutated cached object")
			}
		})
	}
}

func TestBroadSearchPermissionChecksDoNotScaleWithNamespacedCRDs(t *testing.T) {
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{}, namespaced: map[schema.GroupVersionResource]bool{}}
	for i := 0; i < 200; i++ {
		gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: fmt.Sprintf("widgets%d", i)}
		p.kinds[gvr], p.namespaced[gvr] = fmt.Sprintf("Widget%d", i), true
	}
	namespaces := make([]string, 10)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("team-%d", i)
	}
	checks := 0
	res, err := Search(context.Background(), p, Parse(""), Options{
		Namespaces: namespaces,
		NamespacedRBAC: func(ns []string, group, resource string) (string, []string) {
			checks++
			if group == "example.io" {
				t.Fatal("ordinary namespaced CRD was per-kind gated")
			}
			return "", nil
		},
		CanReadClusterScoped: func(kind, group, resource string) (bool, bool) { checks++; return true, true },
	})
	if err != nil || partial(res) {
		t.Fatalf("search: %+v %v", res, err)
	}
	clusterScoped := 0
	for _, tk := range typedKinds {
		if isClusterScopedKind(tk.Kind) {
			clusterScoped++
		}
	}
	if checks != 4+clusterScoped {
		t.Fatalf("permission calls = %d; want 4 sensitive + %d cluster-scoped", checks, clusterScoped)
	}
	if len(p.dynamicListNamespaces) != 2000 {
		t.Fatalf("expected 200 CRDs across 10 visible namespaces, got %d lists", len(p.dynamicListNamespaces))
	}
}

func TestBroadCoverageOmitsColdAndUnsupportedBeforeSAR(t *testing.T) {
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{}, namespaced: map[schema.GroupVersionResource]bool{}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{}}
	for i, state := range []k8score.DynamicObservationState{k8score.DynamicObservationUnwatched, k8score.DynamicObservationUnwatched, k8score.DynamicObservationUnsupported} {
		gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: fmt.Sprintf("things%d", i)}
		p.kinds[gvr], p.namespaced[gvr] = fmt.Sprintf("Thing%d", i), false
		p.observations[gvr] = k8score.DynamicResourceObservation{State: state}
	}
	res, _ := Search(context.Background(), p, Parse(""), Options{CanReadClusterScoped: func(kind, group, resource string) (bool, bool) {
		if group == "example.io" {
			t.Fatal("cold or unsupported kind issued SAR")
		}
		return true, true
	}})
	if partial(res) || len(unsearched(res)) != 0 || len(p.warmed) != 0 {
		t.Fatalf("broad coverage: %+v", res)
	}
	res, _ = Search(context.Background(), p, Parse("kind:Thing2"), Options{CanReadClusterScoped: func(string, string, string) (bool, bool) { t.Fatal("unsupported kind issued SAR"); return false, true }})
	if !partial(res) || len(unsearched(res)) != 1 || unsearched(res)[0].Kind != "Thing2" || unsearched(res)[0].Reason != "kind_not_watchable" {
		t.Fatalf("explicit unsupported: %+v", res)
	}
}

func TestBroadOnlyUnsupportedIsComplete(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "PodMetrics"}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationUnsupported}}}
	res, _ := Search(context.Background(), p, Parse(""), Options{})
	if partial(res) || len(unsearched(res)) != 0 {
		t.Fatalf("list-only API makes broad search partial: %+v", res)
	}
}

// The caller's own access is checked first: a named kind both the caller and
// Radar can't read is the caller's to fix, and a kind the caller can't read is
// never "still loading" or "Radar can't read it" for them. Radar's denial is
// reported only when the caller could read the kind.
func TestCallerAccessPrecedesTransientCoverage(t *testing.T) {
	gadget := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "gadgets"}
	collectorDenied := &fakeProvider{
		typedReasons: map[string]string{"Node": "radar_forbidden", "Secret": "radar_forbidden"},
		kinds:        map[schema.GroupVersionResource]string{gadget: "Gadget"},
		namespaced:   map[schema.GroupVersionResource]bool{gadget: false},
		observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gadget: {State: k8score.DynamicObservationDenied}},
	}
	for _, tc := range []struct {
		allowed bool
		reason  string
	}{{false, "user_forbidden"}, {true, "radar_forbidden"}} {
		res, _ := Search(context.Background(), collectorDenied, Parse("kind:Node kind:Secret kind:Gadget"), Options{
			NamespacedRBAC: func([]string, string, string) (string, []string) {
				if tc.allowed {
					return "", nil
				}
				return "skip", nil
			},
			CanReadClusterScoped: func(string, string, string) (bool, bool) { return tc.allowed, true },
		})
		want := []UnsearchedKind{
			{Kind: "Gadget", Group: "example.io", Reason: readreason.Code(tc.reason)},
			{Kind: "Node", Reason: readreason.Code(tc.reason)},
			{Kind: "Secret", Reason: readreason.Code(tc.reason)},
		}
		if !reflect.DeepEqual(unsearched(res), want) {
			t.Fatalf("caller allowed=%v: %+v, want %+v", tc.allowed, unsearched(res), want)
		}
	}
	if res, _ := Search(context.Background(), collectorDenied, Parse("robusta"), Options{
		CanReadClusterScoped: func(kind, _, _ string) (bool, bool) {
			if kind == "Node" || kind == "Gadget" {
				t.Fatalf("a broad search checked the caller for %s, which Radar can't collect", kind)
			}
			return true, true
		},
	}); partial(res) {
		t.Fatalf("broad search reported Radar's setup: %+v", unsearched(res))
	}

	loading := &fakeProvider{typedReasons: map[string]string{"Secret": "kind_sync_pending", "Node": "kind_sync_timed_out"}}
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "gadgets"}
	loading.kinds = map[schema.GroupVersionResource]string{gvr: "Gadget"}
	loading.namespaced = map[schema.GroupVersionResource]bool{gvr: false}
	loading.observations = map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSyncing}}
	denied := Options{
		NamespacedRBAC:       func([]string, string, string) (string, []string) { return "skip", nil },
		CanReadClusterScoped: func(string, string, string) (bool, bool) { return false, true },
	}
	for _, query := range []string{"robusta", "ns:team-a robusta"} {
		q := Parse(query)
		opts := denied
		opts.ScopeNamespaces(q, nil, nil)
		if res, _ := Search(context.Background(), loading, q, opts); partial(res) || len(unsearched(res)) != 0 {
			t.Fatalf("%s: unreadable kinds reported to the caller: %+v", query, unsearched(res))
		}
	}
	res, _ := Search(context.Background(), loading, Parse("kind:Secret kind:Node kind:Gadget"), denied)
	want := []UnsearchedKind{
		{Kind: "Gadget", Group: "example.io", Reason: "user_forbidden"},
		{Kind: "Node", Reason: "user_forbidden"},
		{Kind: "Secret", Reason: "user_forbidden"},
	}
	if !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("named unreadable kinds = %+v, want %+v", unsearched(res), want)
	}
	res, _ = Search(context.Background(), loading, Parse("robusta"), Options{
		NamespacedRBAC:       func([]string, string, string) (string, []string) { return "", nil },
		CanReadClusterScoped: func(string, string, string) (bool, bool) { return true, true },
	})
	want = []UnsearchedKind{
		{Kind: "*", Reason: "kind_sync_pending", Count: 2},
		{Kind: "Node", Reason: "kind_sync_timed_out"},
	}
	if !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("readable loading kinds = %+v, want %+v", unsearched(res), want)
	}
}

func TestDynamicCollisionIgnoresTypedSkipKinds(t *testing.T) {
	for _, kind := range []string{"Role", "NetworkPolicy"} {
		gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "things"}
		p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: kind}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}, dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {{Object: map[string]any{"kind": kind, "metadata": map[string]any{"name": "visible", "namespace": "team-a"}}}}}}
		res, _ := Search(context.Background(), p, Parse("kind:"+kind), Options{SkipKinds: map[string]bool{kind: true}, Namespaces: []string{"team-a"}})
		if len(res.Hits) != 1 || res.Hits[0].Group != "example.io" {
			t.Fatalf("colliding CRD skipped: %+v", res)
		}
	}
}

func TestDynamicObservationReasonsAndRequestedScope(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{}}
	p.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing, ReasonCode: "sync_stalled"}
	res, _ := Search(context.Background(), p, Parse("kind:Widget"), Options{})
	if !partial(res) || len(unsearched(res)) != 1 || string(unsearched(res)[0].Reason) != "kind_sync_timed_out" || len(p.warmed) != 0 {
		t.Fatalf("stalled sync: %+v", res)
	}
	p.warmError = fmt.Errorf("watch failed")
	p.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"a"}, Truncated: true}
	for _, tc := range []struct {
		ns      []string
		partial bool
	}{{[]string{"a"}, false}, {[]string{"a", "b"}, true}, {nil, true}} {
		res, _ = Search(context.Background(), p, Parse("kind:Widget"), Options{Namespaces: tc.ns})
		if partial(res) != tc.partial {
			t.Fatalf("namespace %v: %+v", tc.ns, res)
		}
	}
}

// Warming an uncovered namespace of a kind Radar already watches elsewhere
// talks to the API server as Radar; a refusal there is the collector's
// namespace scope, reported only for a namespace the query names.
func TestWarmingRefusedNamespaceIsCollectorScope(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	p := &fakeProvider{
		kinds:        map[schema.GroupVersionResource]string{gvr: "Widget"},
		namespaced:   map[schema.GroupVersionResource]bool{gvr: true},
		observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"a"}}},
		warmError:    apierrors.NewUnauthorized("token expired"),
	}
	if res, _ := Search(context.Background(), p, Parse("kind:Widget"), Options{Namespaces: []string{"a", "b"}}); partial(res) || len(unsearched(res)) != 0 {
		t.Fatalf("kind: alone reported namespace scope: %+v", unsearched(res))
	}
	q := Parse("kind:Widget ns:a ns:b")
	var opts Options
	opts.ScopeNamespaces(q, nil, nil)
	res, _ := Search(context.Background(), p, q, opts)
	want := []UnsearchedKind{{Kind: "*", Reason: "radar_namespace_not_collected", Namespaces: []string{"b"}}}
	if !partial(res) || !reflect.DeepEqual(unsearched(res), want) {
		t.Fatalf("refused named namespace = %+v, want %+v", unsearched(res), want)
	}
}

func TestPermissionCheckFailuresAreNotReportedAsDenial(t *testing.T) {
	for _, kind := range []string{"Secret", "Node"} {
		res, _ := Search(context.Background(), &fakeProvider{}, Parse("kind:"+kind), Options{
			NamespacedRBAC:       func([]string, string, string) (string, []string) { return "permission_check_failed", nil },
			CanReadClusterScoped: func(string, string, string) (bool, bool) { return false, false },
		})
		if !partial(res) || len(unsearched(res)) != 1 || unsearched(res)[0].Reason != "user_permission_check_failed" {
			t.Fatalf("failed SAR: %+v", res)
		}
	}
}

// The documented example for "zero or unreported ready replicas": Kubernetes
// omits readyReplicas at 0, and the kind check keeps objects that never carry
// the field (ConfigMaps) out.
func TestZeroReadyReplicasExample(t *testing.T) {
	f, err := filter.CompileObjectFilter(`kind == "Deployment" && (!has(status.readyReplicas) || status.readyReplicas == 0)`)
	if err != nil {
		t.Fatal(err)
	}
	p := &fakeProvider{typed: map[string][]runtime.Object{
		"deployments": {
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "robusta-down", Namespace: "a"}},
			&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "robusta-up", Namespace: "a"}, Status: appsv1.DeploymentStatus{ReadyReplicas: 2}},
		},
		"configmaps": {&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "robusta-config", Namespace: "a"}}},
	}}
	res, _ := Search(context.Background(), p, Parse("robusta"), Options{Include: IncludeNone, Filter: f})
	if got := hitNames(res); !reflect.DeepEqual(got, []string{"Deployment/a/robusta-down"}) || res.FilterErrors != 0 {
		t.Fatalf("hits %v, filter errors %d", got, res.FilterErrors)
	}
}
