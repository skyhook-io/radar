package search

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/internal/readscope"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

type typedPlanProvider struct {
	Provider
	plans map[string]readplan.Plan
}

func (p *typedPlanProvider) DynamicResources() ([]schema.GroupVersionResource, error) {
	return nil, nil
}

func (p *typedPlanProvider) PlanTyped(kind string, namespaces []string) readplan.Plan {
	if plan, ok := p.plans[kind]; ok {
		return plan
	}
	return p.Provider.PlanTyped(kind, namespaces)
}

func TestSearchTypedNamespaceCredentialRejection(t *testing.T) {
	client := fake.NewClientset(&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "robusta"}})
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client: client, ResourceTypes: map[string]bool{"configmaps": true},
		ResourceScopes: map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "alpha"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetResourceCache)
	t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{
		Perms:                &k8s.ResourcePermissions{},
		Scopes:               map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "alpha"}},
		NamespaceProbeErrors: map[string]map[string]error{"configmaps": {"beta": apierrors.NewUnauthorized("token rejected")}},
	}))
	p := &typedPlanProvider{Provider: NewCacheProvider()}
	for _, query := range []string{"robusta", "kind:ConfigMap robusta", "kind:ConfigMap ns:alpha ns:beta robusta"} {
		q := Parse(query)
		opts := Options{Include: IncludeNone}
		opts.Selection = SelectNamespaces(p, q, readscope.NamespaceAccess{Namespaces: []string{"alpha", "beta"}, Checked: true}, nil)
		res, err := Search(t.Context(), p, q, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(hitNames(res), []string{"ConfigMap/alpha/robusta"}) || res.Searched != 1 {
			t.Fatalf("%s: %+v", query, res)
		}
		want := []resourceapi.Gap{{Code: readreason.RadarCredentialsRejected, Kinds: resourceapi.Kinds([]resourceapi.KindRef{{Kind: "ConfigMap"}}, 1), Namespaces: resourceapi.UnknownNames()}}
		if !partial(res) || !reflect.DeepEqual(res.Coverage.Gaps, want) || slices.Contains(res.Coverage.NarrowedBy, resourceapi.NarrowedByNamespaceCollection) {
			t.Fatalf("%s: rejected namespace became collection narrowing: %+v", query, res.Coverage)
		}
	}
}

func TestSearchTypedPlanKeepsEveryReadinessReason(t *testing.T) {
	plan := readplan.Plan{Partitions: []readplan.Partition{
		{Extent: readplan.NamespaceSet, Namespaces: []string{"alpha"}, Verdict: readplan.Verdict{State: readplan.Readable}},
		{Extent: readplan.NamespaceSet, Namespaces: []string{"beta"}, Verdict: readplan.Verdict{State: readplan.Gap, Code: readreason.KindSyncPending}},
		{Extent: readplan.NamespaceSet, Namespaces: []string{"gamma"}, Verdict: readplan.Verdict{State: readplan.Gap, Code: readreason.KindSyncTimedOut}},
	}}
	rec := &recordingProvider{fakeProvider: &fakeProvider{typed: map[string][]runtime.Object{"configmaps": {
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "robusta"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "beta", Name: "robusta-hidden"}},
	}}}}
	p := &typedPlanProvider{Provider: rec, plans: map[string]readplan.Plan{"ConfigMap": plan}}
	for _, query := range []string{"robusta", "kind:ConfigMap robusta", "kind:ConfigMap ns:alpha ns:beta ns:gamma robusta"} {
		q := Parse(query)
		opts := Options{Include: IncludeNone}
		opts.Selection = SelectNamespaces(p, q, readscope.NamespaceAccess{Namespaces: []string{"alpha", "beta", "gamma"}, Checked: true}, nil)
		res, err := Search(t.Context(), p, q, opts)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(rec.listedWith["configmaps"], []string{"alpha"}) || !slices.Equal(hitNames(res), []string{"ConfigMap/alpha/robusta"}) || res.Searched != 2 || !partial(res) || len(res.Coverage.Gaps) != 2 {
			t.Fatalf("%s: %+v", query, res)
		}
		for _, gap := range res.Coverage.Gaps {
			if gap.Code != readreason.KindSyncPending && gap.Code != readreason.KindSyncTimedOut {
				t.Fatalf("unexpected reason: %+v", gap)
			}
			limit := 1
			if gap.Code == readreason.KindSyncPending && len(q.KindFilter) == 0 {
				limit = 0
			}
			if !reflect.DeepEqual(gap.Kinds, resourceapi.Kinds([]resourceapi.KindRef{{Kind: "ConfigMap"}}, limit)) || !reflect.DeepEqual(gap.Namespaces, resourceapi.UnknownNames()) {
				t.Fatalf("%s: changed fold or extent: %+v", query, gap)
			}
		}
		if res.Coverage.Gaps[0].Code == res.Coverage.Gaps[1].Code {
			t.Fatalf("lost one reason: %+v", res.Coverage)
		}
	}
}

type replaceSearchSourceProvider struct {
	*probeRetryProvider
	replace func()
}

func (p *replaceSearchSourceProvider) ListDynamic(ctx context.Context, gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	if p.replace != nil {
		replace := p.replace
		p.replace = nil
		replace()
	}
	return p.CacheProvider.ListDynamic(ctx, gvr, ns)
}

func TestSearchNamespaceSourceReplacementAndStop(t *testing.T) {
	for _, mode := range []string{"replacement", "stopped namespace"} {
		t.Run(mode, func(t *testing.T) {
			gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
			widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.io/v1", "kind": "Widget", "metadata": map[string]any{"name": "robusta", "namespace": "alpha"}}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, widget)
			var promote, hold atomic.Bool
			dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetNamespace() != "" {
					return false, nil, nil
				}
				if !promote.Load() {
					return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("namespace scope"))
				}
				if hold.Load() && a.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions().Limit != 1 {
					return true, nil, errors.New("initial list held")
				}
				return false, nil, nil
			})
			discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
			if err != nil {
				t.Fatal(err)
			}
			dc, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: discovery})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(dc.Stop)
			completeCRDDiscovery(t, dc)
			discovery.AddAPIResource(k8score.APIResource{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}})
			if err := dc.EnsureWatchingContext(t.Context(), gvr, "alpha"); err != nil || !dc.WaitForSync(gvr, 3*time.Second) {
				t.Fatalf("namespace source: %v", err)
			}
			p := &replaceSearchSourceProvider{probeRetryProvider: &probeRetryProvider{&CacheProvider{
				dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dc}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery},
			}}}
			p.PrepareDynamicObservations([]schema.GroupVersionResource{gvr})
			if plan := p.PlanDynamic(gvr, []string{"alpha"}, false); len(plan.Partitions) != 1 || plan.Partitions[0].Verdict.State != readplan.Readable {
				t.Fatalf("namespace plan: %+v", plan)
			}
			if mode == "stopped namespace" {
				dc.Stop()
				if plan := p.PlanDynamic(gvr, []string{"alpha"}, false); !slices.Equal(planCodes(plan), []readreason.Code{readreason.ConnectionChanged}) {
					t.Fatalf("stopped namespace plan: %+v", plan)
				}
				if _, err := Search(t.Context(), p, Parse("kind:Widget robusta"), Options{Selection: readscope.Selection{Namespaces: []string{"alpha"}}}); !errors.Is(err, ErrConnectionChanged) {
					t.Fatalf("stopped namespace cache: %v", err)
				}
				return
			}
			p.replace = func() {
				promote.Store(true)
				hold.Store(true)
				if err := dc.EnsureWatching(gvr); err != nil {
					t.Fatal(err)
				}
			}
			opts := Options{Selection: readscope.Selection{Namespaces: []string{"alpha"}}, Include: IncludeNone}
			res, err := Search(t.Context(), p, Parse("kind:Widget robusta"), opts)
			if err != nil || !partial(res) || len(res.Hits) != 0 || len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].Code != readreason.KindSyncPending {
				t.Fatalf("unloaded replacement became an empty complete result: %+v %v", res, err)
			}
			hold.Store(false)
			if !dc.WaitForSync(gvr, 5*time.Second) {
				t.Fatal("replacement never synced")
			}
			res, err = Search(t.Context(), p, Parse("kind:Widget robusta"), opts)
			if err != nil || partial(res) || !slices.Equal(hitNames(res), []string{"Widget/alpha/robusta"}) {
				t.Fatalf("synced replacement: %+v %v", res, err)
			}
			dc.Stop()
			if plan := p.PlanDynamic(gvr, []string{"alpha"}, false); !slices.Equal(planCodes(plan), []readreason.Code{readreason.ConnectionChanged}) {
				t.Fatalf("stopped plan: %+v", plan)
			}
			if _, err := Search(t.Context(), p, Parse("kind:Widget robusta"), opts); !errors.Is(err, ErrConnectionChanged) {
				t.Fatalf("stopped cache: %v", err)
			}
		})
	}
}

func TestSearchPlansKeepFailureExtents(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	for _, tc := range []struct {
		name       string
		whole      k8score.DynamicResourceObservation
		namespaces map[string]k8score.DynamicResourceObservation
		want       *resourceapi.NameSet
	}{
		{"whole kind", k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing, Scope: k8score.DynamicObservationScopeCluster}, nil, resourceapi.AllNames()},
		{"one namespace", k8score.DynamicResourceObservation{State: k8score.DynamicObservationSyncing, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"alpha", "beta"}}, map[string]k8score.DynamicResourceObservation{
			"alpha": {State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"alpha", "beta"}},
			"beta":  {State: k8score.DynamicObservationSyncing, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: []string{"alpha", "beta"}},
		}, resourceapi.UnknownNames()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{kinds: map[schema.GroupVersionResource]string{gvr: "Widget"}, namespaced: map[schema.GroupVersionResource]bool{gvr: true}, observations: map[schema.GroupVersionResource]k8score.DynamicResourceObservation{gvr: tc.whole}, namespaceObservations: map[schema.GroupVersionResource]map[string]k8score.DynamicResourceObservation{gvr: tc.namespaces}}
			res, err := Search(t.Context(), p, Parse("kind:Widget"), Options{Selection: readscope.Selection{Namespaces: []string{"alpha", "beta"}}})
			if err != nil || len(res.Coverage.Gaps) != 1 || res.Coverage.Gaps[0].Code != readreason.KindSyncPending || !reflect.DeepEqual(res.Coverage.Gaps[0].Namespaces, tc.want) {
				t.Fatalf("coverage=%+v err=%v want namespaces=%+v", res.Coverage, err, tc.want)
			}
		})
	}
}
