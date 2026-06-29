package search

import (
	"context"
	"fmt"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeProvider struct {
	typed                 map[string][]runtime.Object
	dynamic               map[schema.GroupVersionResource][]*unstructured.Unstructured
	kinds                 map[schema.GroupVersionResource]string
	namespaced            map[schema.GroupVersionResource]bool
	dynamicListNamespaces []string
}

func (f *fakeProvider) ListTyped(kind string, namespaces []string) ([]runtime.Object, error) {
	return f.typed[kind], nil
}

func (f *fakeProvider) ListDynamic(_ context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	f.dynamicListNamespaces = append(f.dynamicListNamespaces, namespace)
	return f.dynamic[gvr], nil
}

func (f *fakeProvider) WatchedDynamic() []schema.GroupVersionResource {
	out := make([]schema.GroupVersionResource, 0, len(f.dynamic))
	for g := range f.dynamic {
		out = append(out, g)
	}
	return out
}

func (f *fakeProvider) KindForGVR(gvr schema.GroupVersionResource) string {
	return f.kinds[gvr]
}

func (f *fakeProvider) NamespacedForGVR(gvr schema.GroupVersionResource) (bool, bool) {
	namespaced, ok := f.namespaced[gvr]
	return namespaced, ok
}

func newPod(ns, name, image string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Name: "c", Image: image}},
		},
	}
}

func newDeploy(ns, name, image string, labels map[string]string) *appsv1.Deployment {
	return &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, Labels: labels},
		Spec: appsv1.DeploymentSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c", Image: image}},
				},
			},
		},
	}
}

func TestSearch_RanksExactNameAboveSubstring(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"pods": {
				newPod("default", "redis-master-1", "redis:6.2", nil),
				newPod("default", "redis", "redis:7.0", nil),
				newPod("default", "other", "redis:6.2", nil),
			},
		},
	}
	res, err := Search(context.Background(), p, Parse("redis"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) < 2 {
		t.Fatalf("expected ≥2 hits, got %+v", res.Hits)
	}
	if res.Hits[0].Name != "redis" {
		t.Fatalf("expected 'redis' first (exact name beats prefix), got %q", res.Hits[0].Name)
	}
}

func TestSearch_KindFilter(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"pods":        {newPod("ns", "redis", "redis:6.2", nil)},
			"deployments": {newDeploy("ns", "redis", "redis:6.2", nil)},
		},
	}
	res, _ := Search(context.Background(), p, Parse("kind:Deployment redis"), Options{Include: IncludeNone})
	if len(res.Hits) != 1 || res.Hits[0].Kind != "Deployment" {
		t.Fatalf("expected single Deployment hit, got %+v", res.Hits)
	}
}

func TestSearch_ImageMatch(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"pods": {
				newPod("ns", "anonymous-1", "redis:6.2", nil),
				newPod("ns", "anonymous-2", "nginx:1.21", nil),
			},
		},
	}
	res, _ := Search(context.Background(), p, Parse("image:redis"), Options{Include: IncludeNone})
	if len(res.Hits) != 1 || res.Hits[0].Name != "anonymous-1" {
		t.Fatalf("expected anonymous-1 only, got %+v", res.Hits)
	}
}

func TestSearch_ConfigMapDataMatchWithSnippet(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"configmaps": {
				&corev1.ConfigMap{
					ObjectMeta: metav1.ObjectMeta{Name: "flagd-config", Namespace: "astronomy-shop"},
					Data: map[string]string{
						"flags.json": `{"adServiceFailure":{"defaultVariant":"on"}}`,
					},
				},
			},
		},
	}
	res, err := Search(context.Background(), p, Parse("adServiceFailure"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("expected configmap content hit, got %+v", res.Hits)
	}
	h := res.Hits[0]
	if h.Kind != "ConfigMap" || h.Name != "flagd-config" {
		t.Fatalf("wrong hit: %+v", h)
	}
	if len(h.Snippets) != 1 || h.Snippets[0].Path != "data.flags.json" {
		t.Fatalf("expected data snippet, got %+v", h.Snippets)
	}
}

func TestSearch_DynamicSpecMatchWithSnippet(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "chaos-mesh.org", Version: "v1alpha1", Resource: "networkchaos"}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "chaos-mesh.org/v1alpha1",
		"kind":       "NetworkChaos",
		"metadata": map[string]any{
			"name":      "net-fault",
			"namespace": "hotel",
		},
		"spec": map[string]any{
			"selector": map[string]any{
				"labelSelectors": map[string]any{
					"app": "user",
				},
			},
			"action": "delay",
		},
	}}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "NetworkChaos"},
	}
	res, err := Search(context.Background(), p, Parse("delay"), Options{Include: IncludeNone})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Hits) != 1 {
		t.Fatalf("expected CRD content hit, got %+v", res.Hits)
	}
	if len(res.Hits[0].Snippets) != 1 || res.Hits[0].Snippets[0].Path != "spec.action" {
		t.Fatalf("expected spec snippet, got %+v", res.Hits[0].Snippets)
	}
}

func TestSearch_LimitTruncates(t *testing.T) {
	// Unique names per pod — dedup-by-(kind,group,ns,name) collapses
	// identical entries, so a limit-truncation test needs distinct inputs.
	pods := make([]runtime.Object, 0, 100)
	for i := 0; i < 100; i++ {
		pods = append(pods, newPod("ns", fmt.Sprintf("pod-with-redis-%03d", i), "redis:6.2", nil))
	}
	p := &fakeProvider{typed: map[string][]runtime.Object{"pods": pods}}
	res, _ := Search(context.Background(), p, Parse("redis"), Options{Limit: 10, Include: IncludeNone})
	if len(res.Hits) != 10 {
		t.Fatalf("limit=10 not honored, got %d", len(res.Hits))
	}
	if res.Searched != 100 {
		t.Fatalf("searched=%d, expected 100", res.Searched)
	}
}

func TestSearch_DefaultSkipsEvents(t *testing.T) {
	// Events are skipped unless kind:Event is explicit.
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"events": {&corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "redis-event", Namespace: "ns"}}},
		},
	}
	res, _ := Search(context.Background(), p, Parse("redis"), Options{Include: IncludeNone})
	if len(res.Hits) != 0 {
		t.Fatalf("default search should skip events, got %+v", res.Hits)
	}
	res, _ = Search(context.Background(), p, Parse("kind:Event redis"), Options{Include: IncludeNone})
	if len(res.Hits) != 1 {
		t.Fatalf("kind:Event should opt in, got %+v", res.Hits)
	}
}

func TestSearch_DedupsResourceIndexedTwice(t *testing.T) {
	// A Deployment that's also registered as a watched dynamic GVR (e.g.
	// by a controller indexing built-in workloads as CRDs) would surface
	// in both the typed loop and the dynamic loop. Dedup by
	// (kind, group, ns, name) keeps a single hit regardless of how many
	// indexing paths reached the resource.
	gvr := schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "apps/v1",
		"kind":       "Deployment",
		"metadata": map[string]any{
			"name":      "coredns",
			"namespace": "kube-system",
		},
	}}
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"deployments": {newDeploy("kube-system", "coredns", "coredns:1.10", nil)},
		},
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Deployment"},
	}
	res, _ := Search(context.Background(), p, Parse("coredns"), Options{Include: IncludeNone})
	if len(res.Hits) != 1 {
		t.Fatalf("expected 1 hit after dedup, got %d: %+v", len(res.Hits), res.Hits)
	}
	if res.Hits[0].Name != "coredns" || res.Hits[0].Kind != "Deployment" {
		t.Fatalf("unexpected hit: %+v", res.Hits[0])
	}
}

func TestSearch_DynamicCRD(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "postgresql.cnpg.io", Version: "v1", Resource: "clusters"}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "postgresql.cnpg.io/v1",
		"kind":       "Cluster",
		"metadata": map[string]any{
			"name":      "redis-pg",
			"namespace": "data",
			"labels":    map[string]any{"app": "redis-cache-store"},
		},
	}}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Cluster"},
	}
	res, _ := Search(context.Background(), p, Parse("redis"), Options{Include: IncludeNone})
	if len(res.Hits) != 1 {
		t.Fatalf("expected 1 hit, got %+v", res.Hits)
	}
	if res.Hits[0].Group != "postgresql.cnpg.io" {
		t.Fatalf("group not propagated: %+v", res.Hits[0])
	}
}

func TestSearch_DynamicClusterScopedCRDRequiresAccess(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata": map[string]any{
			"name": "redis-workers",
		},
	}}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "NodePool"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: false},
	}
	res, _ := Search(context.Background(), p, Parse("kind:NodePool redis"), Options{
		Include: IncludeNone,
		CanReadClusterScoped: func(kind, group, resource string) bool {
			if kind != "NodePool" || group != "karpenter.sh" || resource != "nodepools" {
				t.Fatalf("unexpected SAR tuple: kind=%q group=%q resource=%q", kind, group, resource)
			}
			return false
		},
	})
	if len(res.Hits) != 0 {
		t.Fatalf("cluster-scoped CRD leaked despite denied access: %+v", res.Hits)
	}
	if len(p.dynamicListNamespaces) != 0 {
		t.Fatalf("denied cluster-scoped CRD should not be listed, got namespaces %v", p.dynamicListNamespaces)
	}
}

func TestSearch_DynamicClusterScopedCRDListsAtClusterScopeWhenAllowed(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata": map[string]any{
			"name": "redis-workers",
		},
	}}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {u}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "NodePool"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: false},
	}
	res, _ := Search(context.Background(), p, Parse("kind:NodePool redis"), Options{
		Include:    IncludeNone,
		Namespaces: []string{"team-a", "team-b"},
		CanReadClusterScoped: func(kind, group, resource string) bool {
			return kind == "NodePool" && group == "karpenter.sh" && resource == "nodepools"
		},
	})
	if len(res.Hits) != 1 {
		t.Fatalf("expected cluster-scoped CRD hit, got %+v", res.Hits)
	}
	if len(p.dynamicListNamespaces) != 1 || p.dynamicListNamespaces[0] != "" {
		t.Fatalf("cluster-scoped CRD should list once at cluster scope, got namespaces %v", p.dynamicListNamespaces)
	}
}

func TestSearch_IncludeSummary(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"pods": {newPod("ns", "redis", "redis:6.2", map[string]string{"app": "redis"})},
		},
	}
	res, _ := Search(context.Background(), p, Parse("redis"), Options{Include: IncludeSummary})
	if res.Hits[0].Summary == nil {
		t.Fatalf("expected summary, got %+v", res.Hits[0])
	}
	if res.Hits[0].Raw != nil {
		t.Fatalf("expected no raw, got %+v", res.Hits[0])
	}
}

func TestSearch_IncludeNoneIdentityOnly(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"pods": {newPod("ns", "redis", "redis:6.2", nil)},
		},
	}
	res, _ := Search(context.Background(), p, Parse("redis"), Options{Include: IncludeNone})
	if res.Hits[0].Summary != nil || res.Hits[0].Raw != nil {
		t.Fatalf("IncludeNone should leave both empty, got %+v", res.Hits[0])
	}
	if res.Hits[0].Name != "redis" {
		t.Fatalf("identity missing: %+v", res.Hits[0])
	}
}
