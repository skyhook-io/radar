package issues

import (
	"errors"
	"reflect"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
)

type loadingProvider struct {
	*fakeProvider
	readiness    map[string]k8s.KindReadiness
	discovering  bool
	dynamicGaps  map[schema.GroupVersionResource]issuesapi.UncheckedReason
	karpenterInv map[string]karpenterResourceInventory
}

func (p *loadingProvider) typedKindReadiness(key string) k8s.KindReadiness {
	if r, ok := p.readiness[key]; ok {
		return r
	}
	return k8s.KindReady
}

func (p *loadingProvider) crdDiscoveryPending() bool { return p.discovering }

func (p *loadingProvider) dynamicSyncGap(gvr schema.GroupVersionResource, _ []string) issuesapi.UncheckedReason {
	return p.dynamicGaps[gvr]
}

func (p *loadingProvider) karpenterResourceDiscovery(group, kind string) karpenterResourceCoverage {
	return p.karpenterResources(group, kind).Coverage
}

func (p *loadingProvider) karpenterResources(_, kind string) karpenterResourceInventory {
	if inv, ok := p.karpenterInv[kind]; ok {
		return inv
	}
	return karpenterResourceInventory{Coverage: karpenterCoverageNotInstalled}
}

// Right after startup the HPA and PVC caches are still empty. Compose must say
// so instead of answering as if neither had any problems.
func TestComposeReportsTypedKindsStillLoading(t *testing.T) {
	p := &loadingProvider{
		fakeProvider: &fakeProvider{},
		readiness: map[string]k8s.KindReadiness{
			"horizontalpodautoscalers": k8s.KindPending,
			"persistentvolumeclaims":   k8s.KindPending,
			"poddisruptionbudgets":     k8s.KindFailed,
			"nodes":                    k8s.KindPending,
			"secrets":                  k8s.KindUnavailable,
		},
	}

	_, stats := ComposeWithStats(p, Filters{Limit: NoLimit})
	want := []issuesapi.UncheckedSource{
		{Source: issuesapi.UncheckedSourceResources, Reason: issuesapi.UncheckedReasonSyncFailed, Kinds: []issuesapi.UncheckedKind{{Group: "policy", Kind: "PodDisruptionBudget"}}},
		{Source: issuesapi.UncheckedSourceResources, Reason: issuesapi.UncheckedReasonSyncPending, Kinds: []issuesapi.UncheckedKind{
			{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"},
			{Kind: "Node"},
			{Kind: "PersistentVolumeClaim"},
		}},
	}
	if !reflect.DeepEqual(stats.Unchecked, want) {
		t.Fatalf("unchecked = %+v\nwant %+v", stats.Unchecked, want)
	}
	if resp := NewListResponse(nil, stats); !reflect.DeepEqual(resp.UncheckedSources, want) {
		t.Fatalf("response unchecked_sources = %+v, want %+v", resp.UncheckedSources, want)
	}
}

// A gap only matters for issues the caller asked for and could see.
func TestComposeReportsTypedKindsOnlyInTheCallersScope(t *testing.T) {
	p := &loadingProvider{
		fakeProvider: &fakeProvider{},
		readiness: map[string]k8s.KindReadiness{
			"horizontalpodautoscalers": k8s.KindPending,
			"nodes":                    k8s.KindPending,
			"secrets":                  k8s.KindPending,
		},
	}
	cases := []struct {
		name string
		f    Filters
		want []issuesapi.UncheckedKind
	}{
		{name: "kind filter", f: Filters{Kinds: []string{"Pod"}}},
		{
			// A namespaced view gets no Node rows, so a Node gap is not its gap.
			name: "namespaced request",
			f:    Filters{Namespaces: []string{"shop"}},
			want: []issuesapi.UncheckedKind{{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"}, {Kind: "Secret"}},
		},
		{
			name: "caller may not read Nodes or Secrets",
			f: Filters{
				CanReadClusterScoped: func(kind, _ string) bool { return kind != "Node" },
				CanReadEvidence:      func(r EvidenceRead) bool { return r.Resource != "secrets" },
			},
			want: []issuesapi.UncheckedKind{{Group: "autoscaling", Kind: "HorizontalPodAutoscaler"}},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.f.Limit = NoLimit
			_, stats := ComposeWithStats(p, tc.f)
			var got []issuesapi.UncheckedKind
			for _, u := range stats.Unchecked {
				got = append(got, u.Kinds...)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("unchecked kinds = %+v, want %+v", got, tc.want)
			}
		})
	}
}

// A custom resource kind still loading, or failing to list, may hide a
// Ready=False object; CRD discovery still running means kinds aren't watched
// yet at all.
func TestComposeReportsCustomResourceGaps(t *testing.T) {
	widgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "widgets"}
	gadgets := schema.GroupVersionResource{Group: "example.com", Version: "v1", Resource: "gadgets"}
	p := &loadingProvider{
		fakeProvider: &fakeProvider{
			dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{widgets: nil, gadgets: nil},
			kinds:      map[schema.GroupVersionResource]string{widgets: "Widget", gadgets: "Gadget"},
			namespaced: map[schema.GroupVersionResource]bool{widgets: true, gadgets: true},
			listErr:    map[schema.GroupVersionResource]error{gadgets: errors.New("informer not found")},
		},
		discovering: true,
		dynamicGaps: map[schema.GroupVersionResource]issuesapi.UncheckedReason{widgets: issuesapi.UncheckedReasonSyncPending},
	}

	p.discovering = false
	_, stats := ComposeWithStats(p, Filters{Limit: NoLimit, Namespaces: []string{"shop"}})
	want := []issuesapi.UncheckedSource{
		{Source: issuesapi.UncheckedSourceCustomResources, Reason: issuesapi.UncheckedReasonSyncPending, Kinds: []issuesapi.UncheckedKind{{Group: "example.com", Kind: "Widget"}}},
		{Source: issuesapi.UncheckedSourceCustomResources, Reason: issuesapi.UncheckedReasonReadFailed, Kinds: []issuesapi.UncheckedKind{{Group: "example.com", Kind: "Gadget"}}},
	}
	if !reflect.DeepEqual(stats.Unchecked, want) {
		t.Fatalf("unchecked = %+v\nwant %+v", stats.Unchecked, want)
	}

	// Discovery still running can't be pinned to kinds, so the entry names
	// none, even though Widget is known to be loading too.
	p.discovering = true
	p.listErr = nil
	_, stats = ComposeWithStats(p, Filters{Limit: NoLimit})
	wantDiscovery := []issuesapi.UncheckedSource{{Source: issuesapi.UncheckedSourceCustomResources, Reason: issuesapi.UncheckedReasonSyncPending}}
	if !reflect.DeepEqual(stats.Unchecked, wantDiscovery) {
		t.Fatalf("unchecked during discovery = %+v, want %+v", stats.Unchecked, wantDiscovery)
	}
}

// Karpenter worked out its NodePool coverage was unknown and returned nothing;
// the answer must say NodePools weren't checked.
func TestComposeReportsKarpenterInventoryGaps(t *testing.T) {
	p := &loadingProvider{
		fakeProvider: &fakeProvider{},
		karpenterInv: map[string]karpenterResourceInventory{
			karpenter.NodePoolKind: {Coverage: karpenterCoverageUnknown, Gap: issuesapi.UncheckedReasonSyncPending},
		},
	}
	_, stats := ComposeWithStats(p, Filters{Limit: NoLimit})
	want := []issuesapi.UncheckedSource{{
		Source: issuesapi.UncheckedSourceKarpenter, Reason: issuesapi.UncheckedReasonSyncPending,
		Kinds: []issuesapi.UncheckedKind{{Group: karpenter.Group, Kind: karpenter.NodePoolKind}},
	}}
	if !reflect.DeepEqual(stats.Unchecked, want) {
		t.Fatalf("unchecked = %+v, want %+v", stats.Unchecked, want)
	}

	// A request for another kind doesn't hear about Karpenter; a request for
	// NodeClaims does, since nothing is read without the NodePools.
	if _, stats = ComposeWithStats(p, Filters{Limit: NoLimit, Kinds: []string{"Pod"}}); stats.Unchecked != nil {
		t.Fatalf("Pod request: unchecked = %+v, want none", stats.Unchecked)
	}
	if _, stats = ComposeWithStats(p, Filters{Limit: NoLimit, Kinds: []string{"nodeclaim"}}); !reflect.DeepEqual(stats.Unchecked, want) {
		t.Fatalf("NodeClaim request: unchecked = %+v, want %+v", stats.Unchecked, want)
	}

	// Not installed, or excluded from Radar's own access, is not a gap.
	p.karpenterInv[karpenter.NodePoolKind] = karpenterResourceInventory{Coverage: karpenterCoverageUnknown}
	if _, stats = ComposeWithStats(p, Filters{Limit: NoLimit}); stats.Unchecked != nil {
		t.Fatalf("unchecked = %+v, want none", stats.Unchecked)
	}
	// A caller who can't list NodePools gets no NodePool issues and no gap.
	p.karpenterInv[karpenter.NodePoolKind] = karpenterResourceInventory{Coverage: karpenterCoverageUnknown, Gap: issuesapi.UncheckedReasonReadFailed}
	noPools := Filters{Limit: NoLimit, CanReadClusterScoped: func(kind, _ string) bool { return kind != karpenter.NodePoolKind }}
	if _, stats = ComposeWithStats(p, noPools); stats.Unchecked != nil {
		t.Fatalf("unchecked = %+v, want none for a caller who can't list NodePools", stats.Unchecked)
	}
}
