package search

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
)

type fakeProvider struct {
	observations map[schema.GroupVersionResource]k8score.DynamicResourceObservation
	// namespaceObservations override observations for one namespace's reads.
	namespaceObservations map[schema.GroupVersionResource]map[string]k8score.DynamicResourceObservation
	warmed                []schema.GroupVersionResource
	warmNamespaces        []string
	warmFunc              func(context.Context, schema.GroupVersionResource, string) error
	typedReasons          map[string]string
	unlistable            []k8score.APIResource
	// typedNamespaceReasons give a typed kind's reason per namespace.
	typedNamespaceReasons map[string]map[string]string
	listErrors            map[string]error
	dynamicErrors         map[string]error
	warmError             error
	catalogErr            error
	typed                 map[string][]runtime.Object
	typedErr              map[string]error
	dynamic               map[schema.GroupVersionResource][]*unstructured.Unstructured
	dynamicErrNamespaces  map[string]bool
	kinds                 map[schema.GroupVersionResource]string
	namespaced            map[schema.GroupVersionResource]bool
	dynamicListNamespaces []string
	// filterDynamicByNamespace makes ListDynamic honor its namespace argument
	// the way the real cache does.
	filterDynamicByNamespace bool
	// existingNamespaces, when non-nil, are the namespaces on the cluster;
	// nil means existence is unknown.
	existingNamespaces []string
}

func (f *fakeProvider) KnownMissingNamespaces(names []string) []string {
	if f.existingNamespaces == nil {
		return nil
	}
	return slices.DeleteFunc(slices.Clone(names), func(ns string) bool { return slices.Contains(f.existingNamespaces, ns) })
}

func (f *fakeProvider) ListTyped(resource string, namespaces []string) ([]runtime.Object, error) {
	if err := f.typedErr[resource]; err != nil {
		return nil, err
	}
	return f.typed[resource], f.listErrors[resource]
}

func (f *fakeProvider) ListDynamic(_ context.Context, gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	f.dynamicListNamespaces = append(f.dynamicListNamespaces, namespace)
	if f.dynamicErrNamespaces[namespace] {
		return nil, fmt.Errorf("list %s in %s: forbidden", gvr.Resource, namespace)
	}
	if !f.filterDynamicByNamespace || namespace == "" {
		return f.dynamic[gvr], f.dynamicErrors[namespace]
	}
	var out []*unstructured.Unstructured
	for _, u := range f.dynamic[gvr] {
		if u.GetNamespace() == namespace {
			out = append(out, u)
		}
	}
	return out, f.dynamicErrors[namespace]
}

func (f *fakeProvider) DynamicResources() ([]schema.GroupVersionResource, error) {
	if f.catalogErr != nil {
		return nil, f.catalogErr
	}
	seen := map[schema.GroupVersionResource]bool{}
	for gvr := range f.dynamic {
		seen[gvr] = true
	}
	for gvr := range f.kinds {
		seen[gvr] = true
	}
	out := make([]schema.GroupVersionResource, 0, len(seen))
	for gvr := range seen {
		out = append(out, gvr)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].String() < out[j].String() })
	return out, nil
}
func (f *fakeProvider) PlanTyped(kind string, namespaces []string) readplan.Plan {
	extent := readplan.NamespaceSet
	if namespaces == nil {
		extent = readplan.AllNamespaces
	}
	if isClusterScopedKind(kind) {
		extent = readplan.Cluster
	}
	plan := readplan.Plan{}
	targets := namespaces
	if extent != readplan.NamespaceSet {
		targets = []string{""}
	}
	for _, ns := range targets {
		reason := readreason.Code(f.typedNamespaceReasons[kind][ns])
		if reason == "" {
			reason = readreason.Code(f.typedReasons[kind])
		}
		state := readplan.Readable
		if reason == readreason.RadarNamespaceNotCollected {
			state = readplan.OutsideCollection
		} else if reason != "" {
			state = readplan.Gap
		}
		part := readplan.Partition{Extent: extent, Verdict: readplan.Verdict{State: state, Code: reason}}
		if extent == readplan.NamespaceSet {
			part.Namespaces = []string{ns}
		}
		if reason == readreason.RadarNamespaceNotCollected && namespaces == nil {
			plan.CollectionLimited = true
		}
		plan.Partitions = append(plan.Partitions, part)
	}
	return plan
}
func (f *fakeProvider) PlanDynamic(gvr schema.GroupVersionResource, namespaces []string, clusterScoped bool) readplan.Plan {
	plan := readplan.Dynamic(f, gvr, namespaces, clusterScoped)
	for i := range plan.Partitions {
		if plan.Partitions[i].Verdict.Code == readreason.KindNotWatchable && f.NamespaceObservation(gvr, "").ReasonCode == "direct_read_only" {
			plan.Partitions[i].Verdict.Code = readreason.KindNotSearchable
		}
	}
	return plan
}
func (f *fakeProvider) UnlistableKinds() []k8score.APIResource                     { return f.unlistable }
func (f *fakeProvider) PrepareDynamicObservations(_ []schema.GroupVersionResource) {}
func (f *fakeProvider) DynamicCacheStopped() bool                                  { return false }

func (f *fakeProvider) NamespaceObservation(gvr schema.GroupVersionResource, namespace string) k8score.DynamicResourceObservation {
	if o, ok := f.namespaceObservations[gvr][namespace]; ok && namespace != "" {
		return o
	}
	if o, ok := f.observations[gvr]; ok {
		if namespace != "" && o.Scope == k8score.DynamicObservationScopeExplicitNamespaces && !slices.Contains(o.Namespaces, namespace) && !slices.Contains(o.UnansweredNamespaces, namespace) {
			o.State, o.ReasonCode = k8score.DynamicObservationUnwatched, "not_observed"
		}
		return o
	}
	return k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}
}
func (f *fakeProvider) WarmDynamic(ctx context.Context, gvr schema.GroupVersionResource, namespace string) error {
	f.warmed = append(f.warmed, gvr)
	f.warmNamespaces = append(f.warmNamespaces, namespace)
	if f.warmFunc != nil {
		return f.warmFunc(ctx, gvr, namespace)
	}
	if f.warmError != nil {
		return f.warmError
	}
	f.observations[gvr] = k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced}
	return nil
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

func TestSearch_NetworkPolicyKindFilter(t *testing.T) {
	p := &fakeProvider{
		typed: map[string][]runtime.Object{
			"networkpolicies": {&networkingv1.NetworkPolicy{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "deny-all"}}},
		},
	}
	res, _ := Search(context.Background(), p, Parse("ns:shop kind:NetworkPolicy"), Options{Include: IncludeNone})
	if res.Searched != 1 || len(res.Hits) != 1 || res.Hits[0].Kind != "NetworkPolicy" || res.Hits[0].Name != "deny-all" {
		t.Fatalf("expected the NetworkPolicy to be scanned and returned, got searched=%d hits=%+v", res.Searched, res.Hits)
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
	eventGVR := schema.GroupVersionResource{Group: "events.k8s.io", Version: "v1", Resource: "events"}
	p.dynamic = map[schema.GroupVersionResource][]*unstructured.Unstructured{eventGVR: {{Object: map[string]any{"kind": "Event", "apiVersion": "events.k8s.io/v1", "metadata": map[string]any{"name": "redis-event-new", "namespace": "ns"}}}}}
	p.kinds = map[schema.GroupVersionResource]string{eventGVR: "Event"}

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
		CanReadClusterScoped: func(kind, group, resource string) (bool, bool) {
			if kind != "NodePool" || group != "karpenter.sh" || resource != "nodepools" {
				t.Fatalf("unexpected SAR tuple: kind=%q group=%q resource=%q", kind, group, resource)
			}
			return false, true
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
		Include:   IncludeNone,
		Selection: readscope.Selection{Namespaces: []string{"team-a", "team-b"}},
		CanReadClusterScoped: func(kind, group, resource string) (bool, bool) {
			return kind == "NodePool" && group == "karpenter.sh" && resource == "nodepools", true
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
