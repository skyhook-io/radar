package server

import (
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
