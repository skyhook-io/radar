package meaningfulchanges

import (
	"context"
	"fmt"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestClassifyCustomResourceLifecycle(t *testing.T) {
	// Recreate rows exercise classification only; custom kinds are not
	// enrolled in the recording layer's recreate stash.
	for _, tt := range []struct {
		name     string
		op       timeline.EventType
		reason   string
		diff     *timeline.DiffInfo
		category issuesapi.ChangeCategory
	}{
		{name: "add", op: timeline.EventTypeAdd, category: issuesapi.ChangeCategoryLifecycle},
		{name: "delete", op: timeline.EventTypeDelete, category: issuesapi.ChangeCategoryLifecycle},
		{name: "recreate with diff", op: timeline.EventTypeAdd, reason: timeline.ReasonRecreated, diff: &timeline.DiffInfo{
			Fields: []timeline.FieldChange{{Path: "spec.asNumber", OldValue: int64(64512), NewValue: int64(64513)}},
		}, category: issuesapi.ChangeCategorySpecConfig},
		{name: "recreate without diff", op: timeline.EventTypeAdd, reason: timeline.ReasonRecreated, category: issuesapi.ChangeCategoryLifecycle},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e := timeline.TimelineEvent{Source: timeline.SourceInformer, APIVersion: "projectcalico.org/v3", Kind: "BGPPeer", EventType: tt.op, Reason: tt.reason, Diff: tt.diff}
			if got, _ := classify(e); got != tt.category {
				t.Fatalf("classify = %q, want %q", got, tt.category)
			}
			e.Source = timeline.SourceK8sEvent
			if got, _ := classify(e); got != "" {
				t.Fatalf("non-informer lifecycle = %q, want empty", got)
			}
		})
	}
}

func TestClassifyBuiltinLifecycleExclusions(t *testing.T) {
	for _, kind := range []string{"Pod", "ReplicaSet", "Job", "Node", "Endpoints", "PersistentVolumeClaim", "ServiceAccount"} {
		for _, op := range []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeDelete} {
			if got, _ := classify(timeline.TimelineEvent{Source: timeline.SourceInformer, Kind: kind, EventType: op}); got != "" {
				t.Fatalf("%s %s = %q, want excluded", kind, op, got)
			}
		}
	}
	for _, op := range []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeDelete} {
		if got, _ := classify(timeline.TimelineEvent{Source: timeline.SourceInformer, APIVersion: "storage.k8s.io/v1", Kind: "CSINode", EventType: op}); got != "" {
			t.Fatalf("CSINode %s = %q, want excluded", op, got)
		}
	}
}

func TestRecentCustomResourceExplicitFilters(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
		t.Fatal(err)
	}
	store := timeline.GetStore()
	now := time.Now().UTC().Truncate(time.Second)
	for i, op := range []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeDelete, timeline.EventTypeAdd, timeline.EventTypeUpdate} {
		e := timeline.TimelineEvent{
			ID: string(op) + string(rune('0'+i)), Timestamp: now.Add(-time.Duration(i) * time.Minute),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			APIVersion: "projectcalico.org/v3", Kind: "BGPPeer", Name: "edge-peer", EventType: op,
		}
		if i == 2 {
			e.Name = "recreated-peer"
			e.Reason = timeline.ReasonRecreated
			e.Diff = &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.asNumber", OldValue: int64(64512), NewValue: int64(64513)}}, Summary: "other changes: asNumber"}
		}
		if op == timeline.EventTypeUpdate {
			e.Name = "updated-peer"
			e.Diff = &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.asNumber", OldValue: int64(64512), NewValue: int64(64513)}}, Summary: "other changes: asNumber"}
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	for _, tt := range []struct {
		name  string
		query Query
		want  int
	}{
		{name: "kind", query: Query{Kinds: []string{"BGPPeer"}}, want: 4},
		{name: "name", query: Query{Name: "edge-peer"}, want: 2},
		{name: "kind and name", query: Query{Kinds: []string{"BGPPeer"}, Name: "edge-peer"}, want: 2},
		{name: "unfiltered", want: 0},
		{name: "other kind", query: Query{Kinds: []string{"TidbCluster"}}, want: 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Recent(context.Background(), tt.query)
			if err != nil {
				t.Fatal(err)
			}
			if len(got.Changes) != tt.want {
				t.Fatalf("got %+v, want %d changes", got.Changes, tt.want)
			}
			for _, c := range got.Changes {
				want := issuesapi.ChangeCategoryLifecycle
				if c.Name == "recreated-peer" || c.Name == "updated-peer" {
					want = issuesapi.ChangeCategorySpecConfig
				}
				if c.ChangeCategory != want {
					t.Fatalf("change = %+v, want category %s", c, want)
				}
				if c.Name == "edge-peer" && c.ChangeType == "add" && (c.Summary != "add" || c.RankReason != lifecycleRankReason || len(c.Fields) != 0) {
					t.Fatalf("BGPPeer add output = %+v", c)
				}
			}
		})
	}
	for _, kind := range []string{"BGPPeer", "TidbCluster"} {
		if TrackedKind(kind) || TrackedKindForGroup(kind, "example.com") {
			t.Fatalf("custom kind %s must remain ineligible for negative correlation claims", kind)
		}
	}
}

func TestRecentOwnedAndCollidingCustomResources(t *testing.T) {
	for _, resource := range []struct{ kind, apiVersion string }{
		{"CertificateRequest", "cert-manager.io/v1"},
		{"NodeClaim", "karpenter.sh/v1"},
		{"NetworkPolicy", "crd.projectcalico.org/v1"},
		{"NetworkPolicy", "projectcalico.org/v3"},
		{"Service", "serving.knative.dev/v1"},
		{"Secret", "example.com/v1"},
		{"ConfigMap", "example.com/v1"},
	} {
		for _, owned := range []bool{false, true} {
			t.Run(resource.kind+"/"+resource.apiVersion+"/owned="+fmt.Sprint(owned), func(t *testing.T) {
				timeline.ResetStore()
				t.Cleanup(timeline.ResetStore)
				if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
					t.Fatal(err)
				}
				store := timeline.GetStore()
				for i, op := range []timeline.EventType{timeline.EventTypeAdd, timeline.EventTypeUpdate, timeline.EventTypeDelete} {
					e := timeline.TimelineEvent{
						ID: string(op), Timestamp: time.Now().Add(-time.Duration(i+1) * time.Minute),
						Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
						Kind: resource.kind, APIVersion: resource.apiVersion, Namespace: "shop", Name: "target", EventType: op,
					}
					if owned {
						e.Owner = &timeline.OwnerInfo{Kind: "Controller", Name: "parent", UID: "parent-uid"}
					}
					if op == timeline.EventTypeUpdate {
						e.Diff = &timeline.DiffInfo{Fields: []timeline.FieldChange{
							{Path: "spec.mode", OldValue: "old", NewValue: "new"},
							{Path: "metadata.generation", OldValue: int64(1), NewValue: int64(2)},
						}}
					}
					if err := store.Append(context.Background(), e); err != nil {
						t.Fatal(err)
					}
				}
				for _, q := range []Query{{Kinds: []string{resource.kind}}, {Name: "target"}, {Kinds: []string{resource.kind}, Name: "target"}, {}} {
					got, err := Recent(context.Background(), q)
					if err != nil {
						t.Fatal(err)
					}
					want := 3
					if len(q.Kinds) == 0 && q.Name == "" {
						want = 0
					}
					if len(got.Changes) != want {
						t.Fatalf("query %+v: got %+v, want %d changes", q, got.Changes, want)
					}
					for _, c := range got.Changes {
						wantCategory := issuesapi.ChangeCategoryLifecycle
						if c.ChangeType == "update" {
							wantCategory = issuesapi.ChangeCategorySpecConfig
						}
						if c.APIVersion != resource.apiVersion || c.ChangeCategory != wantCategory {
							t.Fatalf("change = %+v, want group %s and category %s", c, resource.apiVersion, wantCategory)
						}
					}
				}
			})
		}
	}
}

func TestRecentExplicitFiltersStillExcludeManagedBuiltins(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"Deployment", "Service", "NetworkPolicy", "Pod", "ReplicaSet"} {
		apiVersion, _ := resourceid.BuiltinAPIVersion(kind)
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: kind, Timestamp: time.Now().Add(-time.Minute), Source: timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(), Kind: kind, APIVersion: apiVersion, Name: "target",
			EventType: timeline.EventTypeUpdate, Owner: &timeline.OwnerInfo{Kind: "Controller", Name: "parent"},
			Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.replicas", OldValue: 1, NewValue: 2}}},
		}); err != nil {
			t.Fatal(err)
		}
		for _, q := range []Query{{Kinds: []string{kind}}, {Name: "target"}, {Kinds: []string{kind}, Name: "target"}, {}} {
			got, err := Recent(context.Background(), q)
			if err != nil || len(got.Changes) != 0 {
				t.Fatalf("query %+v: got %+v, err %v; managed builtins must stay excluded", q, got, err)
			}
		}
	}
}

func TestRecentCustomResourceOrderOnlyIsSpecConfig(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
		t.Fatal(err)
	}
	old := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "networking.istio.io/v1", "kind": "VirtualService",
		"spec": map[string]any{"http": []any{map[string]any{"name": "specific"}, map[string]any{"name": "fallback"}}},
	}}
	old.SetGeneration(1)
	new := old.DeepCopy()
	new.SetGeneration(2)
	routes := new.Object["spec"].(map[string]any)["http"].([]any)
	routes[0], routes[1] = routes[1], routes[0]
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
		ID: "order", Timestamp: time.Now().Add(-time.Minute), Source: timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(), Kind: "VirtualService", APIVersion: old.GetAPIVersion(), Name: "target",
		EventType: timeline.EventTypeUpdate, Diff: k8s.ComputeDiff("VirtualService", old, new),
	}); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(context.Background(), Query{Kinds: []string{"VirtualService"}, Name: "target"})
	if err != nil || len(got.Changes) != 1 {
		t.Fatalf("got %+v, err %v; want order-only change", got, err)
	}
	c := got.Changes[0]
	if c.ChangeCategory != issuesapi.ChangeCategorySpecConfig || len(c.Fields) != 2 || c.Fields[0].Path != "spec.http" || c.Summary != "desired state changes: spec.http order" || c.Fields[1].Path != "metadata.generation" {
		t.Fatalf("order-only change = %+v", c)
	}
}

func TestRecentExplicitManagedBuiltinChurnDoesNotCrowdOrSaturate(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 2 * NamedFetchBound}); err != nil {
		t.Fatal(err)
	}
	store := timeline.GetStore()
	for i := 0; i < NamedFetchBound+2; i++ {
		e := timeline.TimelineEvent{
			ID: fmt.Sprint(i), Timestamp: time.Now().Add(-time.Duration(i+1) * time.Second),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Service", APIVersion: "v1", Name: "target", EventType: timeline.EventTypeUpdate,
			Owner: &timeline.OwnerInfo{Kind: "Controller", Name: "parent"},
			Diff:  &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: "spec.type", OldValue: "ClusterIP", NewValue: "NodePort"}}},
		}
		if i > NamedFetchBound {
			e.Owner = nil
		}
		if err := store.Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Recent(context.Background(), Query{Kinds: []string{"Service"}, Name: "target"})
	if err != nil || len(got.Changes) != 1 || got.FetchSaturated {
		t.Fatalf("got %+v, err %v; managed builtin churn must not hide the unowned row or saturate", got, err)
	}
}

func TestRecentExplicitManagedBuiltinScanIsBounded(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10 * NamedFetchBound}); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 10*NamedFetchBound; i++ {
		if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
			ID: fmt.Sprint(i), Timestamp: time.Now().Add(-time.Duration(i) * time.Second),
			Source: timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Pod", APIVersion: "v1", Name: "target", EventType: timeline.EventTypeUpdate,
		}); err != nil {
			t.Fatal(err)
		}
	}
	got, err := Recent(context.Background(), Query{Kinds: []string{"Pod"}, Name: "target"})
	if err != nil || len(got.Changes) != 0 || !got.FetchSaturated {
		t.Fatalf("got %+v, err %v; capped raw scan must report saturation even when all rows are excluded", got, err)
	}
}

func TestRecentExplicitOwnedBuiltinBurstDoesNotHideTrackedRows(t *testing.T) {
	for _, kind := range []string{"Service", "ConfigMap"} {
		for _, named := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/named=%v", kind, named), func(t *testing.T) {
				bound := FetchBound
				q := Query{Kinds: []string{kind}}
				if named {
					bound = NamedFetchBound
					q.Name = "target"
				}
				store := initFetchBoundStore(t, 8*bound+10)
				now := time.Now()
				for _, op := range []timeline.EventType{timeline.EventTypeUpdate, timeline.EventTypeDelete} {
					path := "spec.type"
					if kind == "ConfigMap" {
						path = "data (modified keys)"
					}
					appendEvent(t, store, timeline.TimelineEvent{
						ID: string(op), Timestamp: now.Add(-30 * time.Minute),
						APIVersion: "v1", Kind: kind, Name: "target", EventType: op,
						Diff: &timeline.DiffInfo{Fields: []timeline.FieldChange{{Path: path, OldValue: "old", NewValue: "new"}}},
					})
				}
				for i := range 4*bound + 1 {
					for _, op := range []timeline.EventType{timeline.EventTypeUpdate, timeline.EventTypeDelete} {
						appendEvent(t, store, timeline.TimelineEvent{
							ID: fmt.Sprintf("owned-%s-%d", op, i), Timestamp: now.Add(-time.Minute),
							APIVersion: "v1", Kind: kind, Name: "target", EventType: op,
							Owner: &timeline.OwnerInfo{Kind: "Controller", Name: "parent"},
						})
					}
				}
				got, err := Recent(context.Background(), q)
				if err != nil || len(got.Changes) != 2 || !got.FetchSaturated {
					t.Fatalf("got %+v, err %v; want tracked update and delete with custom-scan saturation", got, err)
				}
				for _, c := range got.Changes {
					if c.APIVersion != "v1" || c.Name != "target" || c.Kind != kind {
						t.Fatalf("unexpected row: %+v", c)
					}
				}
			})
		}
	}
}

func TestQueryCandidatesCustomMerge(t *testing.T) {
	for _, tt := range []struct {
		name       string
		kind       string
		versions   []string
		wantIDs    []string
		saturated  bool
		ownedBurst bool
	}{
		{
			name: "overlapping reads deduplicate before cap", kind: "Rollout",
			versions: []string{"argoproj.io/v1alpha1", "argoproj.io/v1alpha1", "argoproj.io/v1alpha1"},
			wantIDs:  []string{"2", "1", "0"},
		},
		{
			name: "merge exceeds bound", kind: "Service",
			versions: []string{"v1", "serving.knative.dev/v1", "v1", "serving.knative.dev/v1"},
			wantIDs:  []string{"3", "2", "1"}, saturated: true,
		},
		{
			name: "tracked read exceeds bound", kind: "Service",
			versions: []string{"v1", "v1", "v1", "v1"},
			wantIDs:  []string{"3", "2", "1"}, saturated: true,
		},
		{
			name: "tracked row beyond custom scan prefix", kind: "Service",
			versions: []string{"v1", "serving.knative.dev/v1"},
			wantIDs:  []string{"1", "0"}, saturated: true, ownedBurst: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := initFetchBoundStore(t, 20)
			now := time.Now()
			for i, apiVersion := range tt.versions {
				if tt.ownedBurst && i == 1 {
					for j := range 13 {
						appendEvent(t, store, timeline.TimelineEvent{
							ID: fmt.Sprintf("owned-%d", j), Timestamp: now.Add(-time.Minute),
							APIVersion: "v1", Kind: tt.kind, Name: "target", EventType: timeline.EventTypeUpdate,
							Owner: &timeline.OwnerInfo{Kind: "Controller", Name: "parent"},
						})
					}
				}
				appendEvent(t, store, timeline.TimelineEvent{
					ID: fmt.Sprint(i), Timestamp: now.Add(-time.Duration(2-i/2) * time.Minute),
					APIVersion: apiVersion, Kind: tt.kind, Name: "target", EventType: timeline.EventTypeUpdate,
				})
			}
			q := normalizeQuery(Query{Kinds: []string{tt.kind}, Name: "target"})
			got, saturated, err := queryCandidates(context.Background(), store, q, q.Kinds, updateEventTypes, 3)
			if err != nil {
				t.Fatal(err)
			}
			var ids []string
			for _, e := range got {
				ids = append(ids, e.ID)
			}
			if !slices.Equal(ids, tt.wantIDs) || saturated != tt.saturated {
				t.Fatalf("IDs=%v saturated=%v, want %v/%v", ids, saturated, tt.wantIDs, tt.saturated)
			}
		})
	}
}

func TestQueryCandidatesCustomMergePreservesStoreOrder(t *testing.T) {
	for _, backend := range []string{"memory", "sqlite"} {
		t.Run(backend, func(t *testing.T) {
			var store timeline.EventStore = timeline.NewMemoryStore(20)
			if backend == "sqlite" {
				var err error
				store, err = timeline.NewSQLiteStore(filepath.Join(t.TempDir(), "history.db"))
				if err != nil {
					t.Fatal(err)
				}
			}
			t.Cleanup(func() { _ = store.Close() })
			now := time.Now()
			for i, minutesAgo := range []int{1, 2, 1, 10} {
				apiVersion := "v1"
				if i%2 != 0 {
					apiVersion = "serving.knative.dev/v1"
				}
				appendEvent(t, store, timeline.TimelineEvent{
					ID: fmt.Sprint(i), Timestamp: now.Add(-time.Duration(minutesAgo) * time.Minute),
					APIVersion: apiVersion, Kind: "Service", Name: "target", EventType: timeline.EventTypeUpdate,
				})
			}
			q := normalizeQuery(Query{Kinds: []string{"Service"}, Name: "target"})
			ordered, err := store.Query(context.Background(), timeline.QueryOptions{
				Namespaces: q.Namespaces, Kinds: compactKinds(q.Kinds), Names: compactNames(q.Name),
				Since: q.start, Sources: []timeline.EventSource{timeline.SourceInformer}, EventTypes: updateEventTypes,
				ClusterContext: k8s.ActiveClusterContext(), Limit: 13, IncludeManaged: true, IncludeK8sEvents: false,
			})
			if err != nil {
				t.Fatal(err)
			}
			got, saturated, err := queryCandidates(context.Background(), store, q, q.Kinds, updateEventTypes, 3)
			if err != nil || len(got) != 3 || !saturated {
				t.Fatalf("got %+v, saturated=%v, err=%v; want three rows and saturation", got, saturated, err)
			}
			for i, e := range got {
				if e.ID != ordered[i].ID {
					t.Fatalf("row %d: got %s, want %s in store order", i, e.ID, ordered[i].ID)
				}
			}
		})
	}
}

func TestRecentCollidingResourceIdentitiesStaySeparate(t *testing.T) {
	timeline.ResetStore()
	t.Cleanup(timeline.ResetStore)
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 20}); err != nil {
		t.Fatal(err)
	}
	for _, apiVersion := range []string{"v1", "serving.knative.dev/v1"} {
		e := timeline.TimelineEvent{
			ID:        apiVersion,
			Timestamp: time.Now().UTC().Truncate(time.Minute).Add(-time.Minute),
			Source:    timeline.SourceInformer, ClusterContext: k8s.ActiveClusterContext(),
			Kind: "Service", APIVersion: apiVersion, Namespace: "shop", Name: "target", EventType: timeline.EventTypeDelete,
		}
		if err := timeline.GetStore().Append(context.Background(), e); err != nil {
			t.Fatal(err)
		}
	}
	if err := timeline.GetStore().Append(context.Background(), timeline.TimelineEvent{
		ID: "recreated", Timestamp: time.Now(), Source: timeline.SourceInformer,
		ClusterContext: k8s.ActiveClusterContext(), Kind: "Service", APIVersion: "v1", Namespace: "shop", Name: "target",
		EventType: timeline.EventTypeAdd, Reason: timeline.ReasonRecreated,
	}); err != nil {
		t.Fatal(err)
	}
	got, err := Recent(context.Background(), Query{Kinds: []string{"Service"}, Name: "target"})
	if err != nil || len(got.Changes) != 2 {
		t.Fatalf("got %+v, err %v; want core recreate and custom delete", got, err)
	}
	for _, c := range got.Changes {
		if c.APIVersion == "v1" && c.ChangeType != "add" || c.APIVersion == "serving.knative.dev/v1" && c.ChangeType != "delete" {
			t.Fatalf("colliding history coalesced across groups: %+v", c)
		}
	}
}

func TestDedupeEventsWithoutIDsKeepsDistinctGroups(t *testing.T) {
	stamp := time.Now()
	var events []timeline.TimelineEvent
	for _, apiVersion := range []string{"v1", "serving.knative.dev/v1", "serving.knative.dev/v2"} {
		events = append(events, timeline.TimelineEvent{
			Timestamp: stamp, Kind: "Service", Namespace: "shop", Name: "target", APIVersion: apiVersion, EventType: timeline.EventTypeDelete,
		})
	}
	got := dedupeEvents(events)
	if len(got) != 2 || got[0].APIVersion != "v1" || got[1].APIVersion != "serving.knative.dev/v1" {
		t.Fatalf("deduped events = %+v, want two distinct groups with versions sharing identity", got)
	}
}
