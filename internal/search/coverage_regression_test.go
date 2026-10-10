package search

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/filter"
	"github.com/skyhook-io/radar/pkg/k8score"
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
	if err != nil || res.Partial {
		t.Fatalf("search: %+v %v", res, err)
	}
	if checks != 10 {
		t.Fatalf("permission calls = %d; want 3 sensitive + 7 cluster-scoped", checks)
	}
	if len(p.dynamicListNamespaces) != 2000 {
		t.Fatalf("expected 200 CRDs across 10 visible namespaces, got %d lists", len(p.dynamicListNamespaces))
	}
}

func TestBroadCoverageCollapsesColdAndOmitsUnsupportedBeforeSAR(t *testing.T) {
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
	if !res.Partial || len(res.Unsearched) != 1 || !reflect.DeepEqual(res.Unsearched[0], UnsearchedKind{Kind: "*", Group: "", Reason: "cold"}) || len(p.warmed) != 0 {
		t.Fatalf("broad coverage: %+v", res)
	}
	res, _ = Search(context.Background(), p, Parse("kind:Thing2"), Options{CanReadClusterScoped: func(string, string, string) (bool, bool) { t.Fatal("unsupported kind issued SAR"); return false, true }})
	if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Kind != "Thing2" || res.Unsearched[0].Reason != "not_indexed" {
		t.Fatalf("explicit unsupported: %+v", res)
	}
}

func TestBroadOnlyUnsupportedIsComplete(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "metrics.k8s.io", Version: "v1beta1", Resource: "pods"}
	p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "PodMetrics"}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: {State: k8score.DynamicObservationUnsupported}}}
	res, _ := Search(context.Background(), p, Parse(""), Options{})
	if res.Partial || len(res.Unsearched) != 0 {
		t.Fatalf("list-only API makes broad search partial: %+v", res)
	}
}

func TestTypedReadinessPrecedesCallerPermissionChecks(t *testing.T) {
	p := &fakeProvider{typedReasons: map[string]string{"Secret": "syncing", "Node": "sa_forbidden"}}
	for _, kind := range []string{"Secret", "Node"} {
		res, _ := Search(context.Background(), p, Parse("kind:"+kind), Options{
			NamespacedRBAC: func([]string, string, string) (string, []string) {
				t.Fatal("unready typed informer issued SAR")
				return "skip", nil
			},
			CanReadClusterScoped: func(string, string, string) (bool, bool) {
				t.Fatal("unavailable typed informer issued SAR")
				return false, true
			},
		})
		if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != p.typedReasons[kind] {
			t.Fatalf("readiness coverage: %+v", res)
		}
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
	for _, tc := range []struct {
		state        k8score.DynamicObservationState
		code, reason string
	}{
		{k8score.DynamicObservationSyncing, "sync_stalled", "sync_failed"},
		{k8score.DynamicObservationDeferred, "scope_probe_incomplete", "syncing"},
	} {
		p.observations[gvr] = k8score.DynamicResourceObservation{State: tc.state, ReasonCode: tc.code}
		res, _ := Search(context.Background(), p, Parse("kind:Widget"), Options{})
		if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != tc.reason || len(p.warmed) != 0 {
			t.Fatalf("reason %s: %+v", tc.code, res)
		}
	}
	p.warmError = fmt.Errorf("watch failed")
	p.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"a"}, Truncated: true}
	for _, tc := range []struct {
		ns      []string
		partial bool
	}{{[]string{"a"}, false}, {[]string{"a", "b"}, true}, {nil, true}} {
		res, _ := Search(context.Background(), p, Parse("kind:Widget"), Options{Namespaces: tc.ns})
		if res.Partial != tc.partial {
			t.Fatalf("namespace %v: %+v", tc.ns, res)
		}
	}
}

func TestPermissionCheckFailuresAreNotReportedAsDenial(t *testing.T) {
	for _, kind := range []string{"Secret", "Node"} {
		res, _ := Search(context.Background(), &fakeProvider{}, Parse("kind:"+kind), Options{
			NamespacedRBAC:       func([]string, string, string) (string, []string) { return "list_error", nil },
			CanReadClusterScoped: func(string, string, string) (bool, bool) { return false, false },
		})
		if !res.Partial || len(res.Unsearched) != 1 || res.Unsearched[0].Reason != "list_error" {
			t.Fatalf("failed SAR: %+v", res)
		}
	}
}
