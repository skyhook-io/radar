package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

func withWorkloadHistoryStore(t *testing.T) timeline.EventStore {
	t.Helper()
	prev := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(prev) })
	historyScopes.reset()
	t.Cleanup(historyScopes.reset)
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 1000}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(func() {
		timeline.ResetStore()
		if err := timeline.InitStore(timeline.DefaultStoreConfig()); err != nil {
			t.Fatalf("re-init global store: %v", err)
		}
	})
	return timeline.GetStore()
}

// A Deployment recreated once, its ReplicaSet and Pod, a K8s Event about the
// Pod, and a sibling Deployment in the same namespace.
func seedWorkloadHistory(t *testing.T, store timeline.EventStore) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	n := 0
	add := func(id, apiVersion, kind, name, uid string, owner *timeline.OwnerInfo, source timeline.EventSource) {
		t.Helper()
		n++
		if err := store.Append(t.Context(), timeline.TimelineEvent{
			ID: id, Timestamp: base.Add(time.Duration(n) * time.Minute), Source: source,
			ClusterContext: k8s.ActiveClusterContext(), APIVersion: apiVersion,
			Kind: kind, Namespace: "default", Name: name, UID: uid, Owner: owner,
			EventType: timeline.EventTypeUpdate,
		}); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}
	owner := func(kind, name, uid string) *timeline.OwnerInfo {
		return &timeline.OwnerInfo{Kind: kind, Name: name, UID: uid}
	}
	add("web-v1", "apps/v1", "Deployment", "web", "dep-1", nil, timeline.SourceInformer)
	add("web-v1-rs", "apps/v1", "ReplicaSet", "web-old", "rs-1", owner("Deployment", "web", "dep-1"), timeline.SourceInformer)
	add("web-v2", "apps/v1", "Deployment", "web", "dep-2", nil, timeline.SourceInformer)
	add("web-v2-rs", "apps/v1", "ReplicaSet", "web-new", "rs-2", owner("Deployment", "web", "dep-2"), timeline.SourceInformer)
	add("web-pod", "v1", "Pod", "web-new-a", "pod-2", owner("ReplicaSet", "web-new", "rs-2"), timeline.SourceInformer)
	add("web-pod-event", "v1", "Pod", "web-new-a", "pod-2", owner("ReplicaSet", "web-new", "rs-2"), timeline.SourceK8sEvent)
	add("api", "apps/v1", "Deployment", "api", "dep-api", nil, timeline.SourceInformer)
	add("api-rs", "apps/v1", "ReplicaSet", "api-1", "rs-api", owner("Deployment", "api", "dep-api"), timeline.SourceInformer)
}

func getWorkloadHistory(t *testing.T, url string) (int, workloadHistoryResponse) {
	t.Helper()
	s := &Server{}
	router := chi.NewRouter()
	router.Get("/api/workloads/{kind}/{namespace}/{name}/history", s.handleWorkloadHistory)
	rr := httptest.NewRecorder()
	router.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, url, nil))
	var resp workloadHistoryResponse
	if rr.Code == http.StatusOK {
		if err := json.Unmarshal(rr.Body.Bytes(), &resp); err != nil {
			t.Fatalf("unmarshal: %v (%s)", err, rr.Body.String())
		}
	}
	return rr.Code, resp
}

func historyIDs(events []timeline.TimelineEvent) []string {
	ids := make([]string, 0, len(events))
	for _, e := range events {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestWorkloadHistory_WorkloadAndWhatItOwns(t *testing.T) {
	seedWorkloadHistory(t, withWorkloadHistoryStore(t))
	code, resp := getWorkloadHistory(t, "/api/workloads/deployments/default/web/history")
	if code != http.StatusOK {
		t.Fatalf("status = %d", code)
	}
	want := "[web-pod web-pod-event web-v1 web-v1-rs web-v2 web-v2-rs]"
	if got := fmt.Sprint(historyIDs(resp.Events)); got != want {
		t.Errorf("history = %s, want %s (both incarnations and their descendants, no sibling)", got, want)
	}
	if resp.Truncated {
		t.Error("a complete history must not be marked truncated")
	}
}

func TestWorkloadHistory_PagesOlderEventsWithoutGaps(t *testing.T) {
	seedWorkloadHistory(t, withWorkloadHistoryStore(t))
	_, first := getWorkloadHistory(t, "/api/workloads/deployments/default/web/history?limit=4")
	if !first.Truncated || first.NextBeforeSeq == 0 || len(first.Events) != 4 {
		t.Fatalf("first page = %d events, truncated=%v next=%d; want 4, true, a cursor", len(first.Events), first.Truncated, first.NextBeforeSeq)
	}
	_, second := getWorkloadHistory(t, fmt.Sprintf("/api/workloads/deployments/default/web/history?limit=4&before_seq=%d", first.NextBeforeSeq))
	if second.Truncated {
		t.Error("the last page must not be marked truncated")
	}
	all := append(append([]timeline.TimelineEvent{}, first.Events...), second.Events...)
	if got := fmt.Sprint(historyIDs(all)); got != "[web-pod web-pod-event web-v1 web-v1-rs web-v2 web-v2-rs]" {
		t.Errorf("pages together = %s, want the whole history once", got)
	}
}

func TestWorkloadHistory_RejectsUnknownKindAndBadCursor(t *testing.T) {
	withWorkloadHistoryStore(t)
	if code, _ := getWorkloadHistory(t, "/api/workloads/nosuchthings/default/web/history"); code != http.StatusBadRequest {
		t.Errorf("unknown kind: status %d, want 400", code)
	}
	if code, _ := getWorkloadHistory(t, "/api/workloads/deployments/default/web/history?before_seq=x"); code != http.StatusBadRequest {
		t.Errorf("bad cursor: status %d, want 400", code)
	}
}

func TestAttachedRefs_KeepsTheWorkloadsNamespaceAndFillsBuiltinGroups(t *testing.T) {
	rel := &topology.Relationships{
		Services:   []topology.ResourceRef{{Kind: "Service", Namespace: "default", Name: "web"}},
		ConfigRefs: []topology.ResourceRef{{Kind: "ConfigMap", Namespace: "default", Name: "web-config"}, {Kind: "ConfigMap", Namespace: "default", Name: "web-config"}},
		Scalers:    []topology.ResourceRef{{Kind: "HorizontalPodAutoscaler", Namespace: "default", Name: "web"}, {Kind: "ScaledObject", Namespace: "default", Name: "web", Group: "keda.sh"}},
		PDBs:       []topology.ResourceRef{{Kind: "PodDisruptionBudget", Namespace: "other", Name: "web"}},
	}
	got := attachedRefs(rel, "default")
	want := []resourceid.Ref{
		resourceid.NewRef("", "Service", "default", "web"),
		resourceid.NewRef("", "ConfigMap", "default", "web-config"),
		resourceid.NewRef("autoscaling", "HorizontalPodAutoscaler", "default", "web"),
		resourceid.NewRef("keda.sh", "ScaledObject", "default", "web"),
	}
	if fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("attachedRefs = %v, want %v", got, want)
	}
}

// A busy cluster may hold rows for a Deployment's ReplicaSets and Pods but
// none under the Deployment's own key. Its live UID starts the ownership walk.
func TestWorkloadHistoryScope_StartsFromTheLiveUIDWhenTheKeyHasNoRows(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	base := time.Now().Add(-time.Hour)
	for i, e := range []timeline.TimelineEvent{
		{ID: "rs", Kind: "ReplicaSet", Name: "web-1", UID: "rs-1", APIVersion: "apps/v1", Owner: &timeline.OwnerInfo{Kind: "Deployment", Name: "web", UID: "dep-live"}},
		{ID: "pod", Kind: "Pod", Name: "web-1-a", UID: "pod-1", APIVersion: "v1", Owner: &timeline.OwnerInfo{Kind: "ReplicaSet", Name: "web-1", UID: "rs-1"}},
		{ID: "other", Kind: "ReplicaSet", Name: "api-1", UID: "rs-api", APIVersion: "apps/v1", Owner: &timeline.OwnerInfo{Kind: "Deployment", Name: "api", UID: "dep-api"}},
	} {
		e.Timestamp, e.Source, e.Namespace, e.EventType = base.Add(time.Duration(i)*time.Minute), timeline.SourceHistorical, "default", timeline.EventTypeAdd
		e.ClusterContext = k8s.ActiveClusterContext()
		if err := store.Append(t.Context(), e); err != nil {
			t.Fatalf("Append: %v", err)
		}
	}
	key := resourceid.NewRef("apps", "Deployment", "default", "web")
	scope, _, err := workloadHistoryScope(t.Context(), store, k8s.ActiveClusterContext(), key, liveIdentity{UID: "dep-live"}, nil)
	if err != nil {
		t.Fatalf("workloadHistoryScope: %v", err)
	}
	events, err := store.Query(t.Context(), timeline.QueryOptions{Scope: scope, Limit: 100, IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got := fmt.Sprint(historyIDs(events)); got != "[pod rs]" {
		t.Errorf("history = %s, want [pod rs]", got)
	}
}

func appendHistoryRows(t *testing.T, store timeline.EventStore, rows []timeline.TimelineEvent) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	for i, e := range rows {
		e.Timestamp, e.Namespace, e.EventType = base.Add(time.Duration(i)*time.Minute), "default", timeline.EventTypeUpdate
		if e.Source == "" {
			e.Source = timeline.SourceInformer
		}
		e.ClusterContext = k8s.ActiveClusterContext()
		if err := store.Append(t.Context(), e); err != nil {
			t.Fatalf("Append %s: %v", e.ID, err)
		}
	}
}

func scopedHistoryIDs(t *testing.T, store timeline.EventStore, key resourceid.Ref, live liveIdentity) string {
	t.Helper()
	scope, _, err := workloadHistoryScope(t.Context(), store, k8s.ActiveClusterContext(), key, live, nil)
	if err != nil {
		t.Fatalf("workloadHistoryScope: %v", err)
	}
	events, err := store.Query(t.Context(), timeline.QueryOptions{Scope: scope, Limit: 100, IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	return fmt.Sprint(historyIDs(events))
}

// K8s Events about a CronJob's runs that were deleted before Radar started
// record no owner. They come back by the names the CronJob gives its Jobs and
// their Pods; a sibling CronJob's runs and rows owned by something else don't.
func TestWorkloadHistoryScope_RecoversPastRunsWithNoRecordedOwner(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	appendHistoryRows(t, store, []timeline.TimelineEvent{
		{ID: "cron", APIVersion: "batch/v1", Kind: "CronJob", Name: "backup", UID: "cj-1"},
		{ID: "past-job", APIVersion: "batch/v1", Kind: "Job", Name: "backup-29012345", UID: "job-old", Source: timeline.SourceK8sEvent},
		{ID: "past-pod", APIVersion: "v1", Kind: "Pod", Name: "backup-29012345-bcdfg", UID: "pod-old", Source: timeline.SourceK8sEvent},
		{ID: "sibling-job", APIVersion: "batch/v1", Kind: "Job", Name: "backup-nightly-29012345", UID: "job-sib", Source: timeline.SourceK8sEvent},
		{ID: "owned-elsewhere", APIVersion: "batch/v1", Kind: "Job", Name: "backup-29012399", UID: "job-other", Owner: &timeline.OwnerInfo{Kind: "CronJob", Name: "other", UID: "cj-other"}},
		{ID: "same-name-owned-elsewhere", APIVersion: "batch/v1", Kind: "Job", Name: "backup-29012345", UID: "job-old", Owner: &timeline.OwnerInfo{Kind: "CronJob", Name: "other", UID: "cj-other"}},
		{ID: "not-a-run", APIVersion: "v1", Kind: "ConfigMap", Name: "backup-29012345", UID: "cm-1"},
		{ID: "volcano-job", APIVersion: "batch.volcano.sh/v1alpha1", Kind: "Job", Name: "backup-29012346", UID: "vj-1", Source: timeline.SourceK8sEvent},
	})
	key := resourceid.NewRef("batch", "CronJob", "default", "backup")
	if got := scopedHistoryIDs(t, store, key, liveIdentity{UID: "cj-1"}); got != "[cron past-job past-pod]" {
		t.Errorf("history = %s, want [cron past-job past-pod]", got)
	}
}

// A ReplicaSet's history includes its Deployment's own rows, but not the
// Deployment's other ReplicaSets.
func TestWorkloadHistoryScope_IncludesTheControllerButNotItsOtherChildren(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	dep := &timeline.OwnerInfo{Kind: "Deployment", Name: "web", UID: "dep-1"}
	appendHistoryRows(t, store, []timeline.TimelineEvent{
		{ID: "dep", APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "dep-1"},
		{ID: "rs", APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-bcdfg", UID: "rs-1", Owner: dep},
		{ID: "other-rs", APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-hjklm", UID: "rs-2", Owner: dep},
	})
	key := resourceid.NewRef("apps", "ReplicaSet", "default", "web-bcdfg")
	if got := scopedHistoryIDs(t, store, key, liveIdentity{UID: "rs-1"}); got != "[dep rs]" {
		t.Errorf("history = %s, want [dep rs]", got)
	}
}

func TestAttachedRefs_IncludesIngressesAndRoutesButNotGateways(t *testing.T) {
	rel := &topology.Relationships{
		Ingresses: []topology.ResourceRef{{Kind: "Ingress", Namespace: "default", Name: "web"}},
		Routes:    []topology.ResourceRef{{Kind: "HTTPRoute", Namespace: "default", Name: "web", Group: "gateway.networking.k8s.io"}},
		Gateways:  []topology.ResourceRef{{Kind: "Gateway", Namespace: "default", Name: "shared", Group: "gateway.networking.k8s.io"}},
	}
	want := []resourceid.Ref{
		resourceid.NewRef("networking.k8s.io", "Ingress", "default", "web"),
		resourceid.NewRef("gateway.networking.k8s.io", "HTTPRoute", "default", "web"),
	}
	if got := attachedRefs(rel, "default"); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("attachedRefs = %v, want %v", got, want)
	}
}

func TestChildNamePatterns_MatchWhatTheControllerNamesAndNotSiblings(t *testing.T) {
	for _, tc := range []struct {
		group, kind, name, childKind, child string
		want                                bool
	}{
		{"batch", "CronJob", "backup", "Job", "backup-29012345", true},
		{"batch", "CronJob", "backup", "Job", "backup-nightly-29012345", false},
		{"apps", "Deployment", "web", "ReplicaSet", "web-5698ccbb7c", true},
		{"apps", "Deployment", "web", "Pod", "web-5698ccbb7c-rd558", true},
		{"apps", "Deployment", "web", "Pod", "web-api-5698ccbb7c-rd558", false},
		{"apps", "DaemonSet", "agent", "Pod", "agent-x5rjl", true},
		{"apps", "DaemonSet", "agent", "Pod", "agent-extra-x5rjl", false},
		{"apps", "StatefulSet", "db", "Pod", "db-2", true},
		{"apps", "StatefulSet", "db", "Pod", "db-replica-2", false},
		{"argoproj.io", "CronWorkflow", "report", "Workflow", "report-1790459400", true},
		{"argoproj.io", "CronWorkflow", "report", "Pod", "report-1790459400-echo-2408686475", true},
		{"argoproj.io", "CronWorkflow", "report", "Workflow", "report-weekly-1790459400", false},
		// Another group's same-named kind doesn't follow the built-in contract.
		{"batch.volcano.sh", "Job", "train", "Pod", "train-x5rjl", false},
	} {
		pattern, ok := childNamePatterns(tc.group, tc.kind, tc.name)[tc.childKind]
		if got := ok && pattern.name.MatchString(tc.child); got != tc.want {
			t.Errorf("%s/%s %s: %s %s matched=%v, want %v", tc.group, tc.kind, tc.name, tc.childKind, tc.child, got, tc.want)
		}
	}
}

// A tree deeper than the walk follows is reported, not silently cut.
func TestWorkloadHistoryScope_ReportsAWalkCutShort(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	var rows []timeline.TimelineEvent
	owner := "root"
	for level := 1; level <= workloadHistoryMaxDepth+1; level++ {
		uid := fmt.Sprintf("level-%d", level)
		rows = append(rows, timeline.TimelineEvent{ID: uid, APIVersion: "v1", Kind: "Pod", Name: uid, UID: uid, Owner: &timeline.OwnerInfo{Kind: "Thing", Name: owner, UID: owner}})
		owner = uid
	}
	appendHistoryRows(t, store, rows)
	key := resourceid.NewRef("example.com", "Thing", "default", "root")
	_, incomplete, err := workloadHistoryScope(t.Context(), store, k8s.ActiveClusterContext(), key, liveIdentity{UID: "root"}, nil)
	if err != nil {
		t.Fatalf("workloadHistoryScope: %v", err)
	}
	if !incomplete {
		t.Error("a tree deeper than the walk must be reported incomplete")
	}
	rows = rows[:workloadHistoryMaxDepth]
	store = withWorkloadHistoryStore(t)
	appendHistoryRows(t, store, rows)
	if _, incomplete, _ := workloadHistoryScope(t.Context(), store, k8s.ActiveClusterContext(), key, liveIdentity{UID: "root"}, nil); incomplete {
		t.Error("a tree exactly as deep as the walk is complete")
	}
}

// An Ingress in front of the workload brings the TLS chain it owns; a
// Service's EndpointSlices stay out.
func TestWorkloadHistoryScope_WalksWhatAnIngressOwns(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	appendHistoryRows(t, store, []timeline.TimelineEvent{
		{ID: "ing", APIVersion: "networking.k8s.io/v1", Kind: "Ingress", Name: "web", UID: "ing-1"},
		{ID: "cert", APIVersion: "cert-manager.io/v1", Kind: "Certificate", Name: "web-tls", UID: "cert-1", Owner: &timeline.OwnerInfo{Kind: "Ingress", Name: "web", UID: "ing-1"}},
		{ID: "cr", APIVersion: "cert-manager.io/v1", Kind: "CertificateRequest", Name: "web-tls-1", UID: "cr-1", Owner: &timeline.OwnerInfo{Kind: "Certificate", Name: "web-tls", UID: "cert-1"}},
		{ID: "svc", APIVersion: "v1", Kind: "Service", Name: "web", UID: "svc-1"},
		{ID: "slice", APIVersion: "discovery.k8s.io/v1", Kind: "EndpointSlice", Name: "web-abcde", UID: "es-1", Owner: &timeline.OwnerInfo{Kind: "Service", Name: "web", UID: "svc-1"}},
	})
	key := resourceid.NewRef("apps", "Deployment", "default", "web")
	attached := []resourceid.Ref{resourceid.NewRef("", "Service", "default", "web"), resourceid.NewRef("networking.k8s.io", "Ingress", "default", "web")}
	scope, _, err := workloadHistoryScope(t.Context(), store, k8s.ActiveClusterContext(), key, liveIdentity{UID: "dep-1"}, attached)
	if err != nil {
		t.Fatalf("workloadHistoryScope: %v", err)
	}
	events, err := store.Query(t.Context(), timeline.QueryOptions{Scope: scope, Limit: 100, IncludeManaged: true, IncludeK8sEvents: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if got := fmt.Sprint(historyIDs(events)); got != "[cert cr ing svc]" {
		t.Errorf("history = %s, want [cert cr ing svc]", got)
	}
}

type countingStore struct {
	timeline.EventStore
	queries int
}

func (c *countingStore) Query(ctx context.Context, opts timeline.QueryOptions) ([]timeline.TimelineEvent, error) {
	c.queries++
	return c.EventStore.Query(ctx, opts)
}

func (c *countingStore) OwnedUIDs(ctx context.Context, clusterContext string, owners []string, limit int) ([]string, error) {
	c.queries++
	return c.EventStore.OwnedUIDs(ctx, clusterContext, owners, limit)
}

func (c *countingStore) Identities(ctx context.Context, q timeline.IdentityQuery, limit int) ([]timeline.Identity, error) {
	c.queries++
	return c.EventStore.Identities(ctx, q, limit)
}

// A refresh on a cached scope is one query; a rollout's new ReplicaSet and
// its Pods, created after the scope was cached, still arrive on the next
// refresh, and are remembered for the one after.
func TestWorkloadHistory_CachedScopeLearnsARollout(t *testing.T) {
	store := &countingStore{EventStore: withWorkloadHistoryStore(t)}
	dep := &timeline.OwnerInfo{Kind: "Deployment", Name: "web", UID: "dep-1"}
	appendHistoryRows(t, store, []timeline.TimelineEvent{
		{ID: "dep", APIVersion: "apps/v1", Kind: "Deployment", Name: "web", UID: "dep-1"},
		{ID: "rs-1", APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-bcdfg", UID: "rs-1", Owner: dep},
		{ID: "api", APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "api-bcdfg", UID: "rs-api", Owner: &timeline.OwnerInfo{Kind: "Deployment", Name: "api", UID: "dep-api"}},
	})
	key := resourceid.NewRef("apps", "Deployment", "default", "web")
	live := liveIdentity{UID: "dep-1"}
	refresh := func() ([]string, int) {
		t.Helper()
		before := store.queries
		events, _, err := readWorkloadHistory(t.Context(), store, k8s.ActiveClusterContext(), key, live, nil, 0, 100)
		if err != nil {
			t.Fatalf("readWorkloadHistory: %v", err)
		}
		return historyIDs(events), store.queries - before
	}

	if ids, _ := refresh(); fmt.Sprint(ids) != "[dep rs-1]" {
		t.Fatalf("first page = %v", ids)
	}
	if ids, n := refresh(); n != 1 || fmt.Sprint(ids) != "[dep rs-1]" {
		t.Errorf("cached refresh = %v in %d queries, want [dep rs-1] in 1", ids, n)
	}
	appendHistoryRows(t, store, []timeline.TimelineEvent{
		{ID: "rs-2", APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-hjklm", UID: "rs-2", Owner: dep},
		{ID: "pod-2", APIVersion: "v1", Kind: "Pod", Name: "web-hjklm-x5rjl", UID: "pod-2", Owner: &timeline.OwnerInfo{Kind: "ReplicaSet", Name: "web-hjklm", UID: "rs-2"}},
	})
	if ids, _ := refresh(); fmt.Sprint(ids) != "[dep pod-2 rs-1 rs-2]" {
		t.Errorf("refresh during the rollout = %v, want the new ReplicaSet and its Pod", ids)
	}
	if ids, n := refresh(); n != 1 || fmt.Sprint(ids) != "[dep pod-2 rs-1 rs-2]" {
		t.Errorf("refresh after the rollout = %v in %d queries, want the rollout remembered in 1 query", ids, n)
	}
}

func TestMergeNewestFirst_KeepsTheNewestAcrossBothPages(t *testing.T) {
	row := func(id string, seq int64) timeline.TimelineEvent { return timeline.TimelineEvent{ID: id, Seq: seq} }
	got := mergeNewestFirst([]timeline.TimelineEvent{row("a", 9), row("b", 5)}, []timeline.TimelineEvent{row("c", 7), row("a", 9), row("d", 1)}, 3)
	var ids []string
	for _, e := range got {
		ids = append(ids, e.ID)
	}
	if fmt.Sprint(ids) != "[a c b]" {
		t.Errorf("merged = %v, want [a c b]", ids)
	}
}

// An Ingress whose own rows have left the timeline still brings the TLS
// chain it owns, from its live UID.
func TestWorkloadHistoryScope_WalksAnIngressWithNoRowsOfItsOwn(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	appendHistoryRows(t, store, []timeline.TimelineEvent{
		{ID: "cert", APIVersion: "cert-manager.io/v1", Kind: "Certificate", Name: "web-tls", UID: "cert-1", Owner: &timeline.OwnerInfo{Kind: "Ingress", Name: "web", UID: "ing-1"}},
		{ID: "cr", APIVersion: "cert-manager.io/v1", Kind: "CertificateRequest", Name: "web-tls-1", UID: "cr-1", Owner: &timeline.OwnerInfo{Kind: "Certificate", Name: "web-tls", UID: "cert-1"}},
	})
	key := resourceid.NewRef("apps", "Deployment", "default", "web")
	attached := []resourceid.Ref{resourceid.NewRef("networking.k8s.io", "Ingress", "default", "web")}
	live := liveIdentity{UID: "dep-1", AttachedUIDs: []string{"ing-1"}}
	events, _, err := readWorkloadHistory(t.Context(), store, k8s.ActiveClusterContext(), key, live, attached, 0, 100)
	if err != nil {
		t.Fatalf("readWorkloadHistory: %v", err)
	}
	if got := fmt.Sprint(historyIDs(events)); got != "[cert cr]" {
		t.Errorf("history = %s, want [cert cr]", got)
	}
}

// A Pod someone created by hand, named like one of the StatefulSet's, isn't
// adopted by name: the informer saw it and recorded that it has no owner.
func TestWorkloadHistory_StandalonePodNotAdoptedByName(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	for i, e := range []timeline.TimelineEvent{
		{ID: "sts", APIVersion: "apps/v1", Kind: "StatefulSet", Name: "web", UID: "sts-1"},
		{ID: "lonely-pod", APIVersion: "v1", Kind: "Pod", Name: "web-0", UID: "pod-x", OwnerEvidence: timeline.OwnerObserved},
		{ID: "missed-pod", APIVersion: "v1", Kind: "Pod", Name: "web-1", UID: "pod-y", OwnerEvidence: timeline.OwnerMissed, Source: timeline.SourceK8sEvent},
	} {
		e.Timestamp = time.Now().Add(time.Duration(i) * time.Second)
		if e.Source == "" {
			e.Source = timeline.SourceInformer
		}
		e.EventType, e.Namespace = timeline.EventTypeAdd, "default"
		e.ClusterContext = k8s.ActiveClusterContext()
		if err := store.Append(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	_, resp := getWorkloadHistory(t, "/api/workloads/statefulsets/default/web/history")
	if got := fmt.Sprint(historyIDs(resp.Events)); got != "[missed-pod sts]" {
		t.Errorf("StatefulSet web history = %s, want [missed-pod sts]: the unknown-owner Pod by name, not the standalone one", got)
	}
}
