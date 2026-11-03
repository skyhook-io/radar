package server

import (
	"fmt"
	"slices"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/timeline"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// A Kueue-queued Job across two incarnations: the first run's Workload and
// its two ProvisioningRequests (a retried check), the second run's Workload,
// and a sibling Job's Workload in the same namespace.
func seedKueueHistory(t *testing.T, store timeline.EventStore) {
	t.Helper()
	base := time.Now().Add(-time.Hour)
	n := 0
	add := func(id, apiVersion, kind, name, uid string, owner *timeline.OwnerInfo) {
		t.Helper()
		n++
		if err := store.Append(t.Context(), timeline.TimelineEvent{
			ID: id, Timestamp: base.Add(time.Duration(n) * time.Minute), Source: timeline.SourceInformer,
			ClusterContext: k8s.ActiveClusterContext(), APIVersion: apiVersion,
			Kind: kind, Namespace: "ml", Name: name, UID: uid, Owner: owner,
			EventType: timeline.EventTypeUpdate,
		}); err != nil {
			t.Fatalf("Append %s: %v", id, err)
		}
	}
	owner := func(kind, name, uid string) *timeline.OwnerInfo {
		return &timeline.OwnerInfo{Kind: kind, Name: name, UID: uid}
	}
	add("job-1", "batch/v1", "Job", "train", "job-1", nil)
	add("wl-1", "kueue.x-k8s.io/v1beta2", "Workload", "job-train-aaaaa", "wl-1", owner("Job", "train", "job-1"))
	add("prov-1", "autoscaling.x-k8s.io/v1", "ProvisioningRequest", "job-train-aaaaa-prov-1", "pr-1", owner("Workload", "job-train-aaaaa", "wl-1"))
	add("prov-2", "autoscaling.x-k8s.io/v1", "ProvisioningRequest", "job-train-aaaaa-prov-2", "pr-2", owner("Workload", "job-train-aaaaa", "wl-1"))
	add("variant", "kueue.x-k8s.io/v1beta2", "Workload", "job-train-aaaaa-v1", "wl-1-variant", owner("Workload", "job-train-aaaaa", "wl-1"))
	add("job-2", "batch/v1", "Job", "train", "job-2", nil)
	add("wl-2", "kueue.x-k8s.io/v1beta2", "Workload", "job-train-bbbbb", "wl-2", owner("Job", "train", "job-2"))
	add("other-job", "batch/v1", "Job", "eval", "job-eval", nil)
	add("other-wl", "kueue.x-k8s.io/v1beta2", "Workload", "job-eval-ccccc", "wl-eval", owner("Job", "eval", "job-eval"))
}

func TestWorkloadHistory_KueueWorkloadAndProvisioningRequestsJoinTheirJob(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	seedKueueHistory(t, store)

	code, resp := getWorkloadHistory(t, "/api/workloads/jobs/ml/train/history")
	if code != 200 {
		t.Fatalf("status = %d", code)
	}
	got := historyIDs(resp.Events)
	want := []string{"job-1", "job-2", "prov-1", "prov-2", "variant", "wl-1", "wl-2"}
	if !slices.Equal(got, want) {
		t.Fatalf("history = %v, want %v (a sibling Job's Workload must stay out)", got, want)
	}
	byID := map[string]timeline.TimelineEvent{}
	for _, e := range resp.Events {
		byID[e.ID] = e
	}
	// Each row keeps its own UID and owner, so the two runs and the two
	// requests stay apart; nothing numbers them.
	if byID["wl-1"].Owner.UID != "job-1" || byID["wl-2"].Owner.UID != "job-2" {
		t.Fatalf("Workload owners = %+v / %+v", byID["wl-1"].Owner, byID["wl-2"].Owner)
	}
	if byID["prov-1"].UID == byID["prov-2"].UID {
		t.Fatal("two ProvisioningRequests merged")
	}
}

func liveObject(uid string, created time.Time, labels map[string]string) liveIdentity {
	u := &unstructured.Unstructured{}
	u.SetUID(types.UID(uid))
	u.SetCreationTimestamp(metav1.NewTime(created))
	u.SetLabels(labels)
	return liveIdentity{Object: u, UID: uid}
}

func TestWorkloadHistoryCoverage(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	started := time.Now().Add(-30 * time.Minute).Truncate(time.Second)
	timeline.SetObservationStartForTest(started)
	t.Cleanup(func() { timeline.SetObservationStartForTest(time.Time{}) })
	key := resourceid.NewRef("batch", "Job", "ml", "train")
	ctx := k8s.ActiveClusterContext()

	if got := workloadHistoryCoverage(t.Context(), store, ctx, key, liveObject("job-2", started.Add(time.Minute), nil), time.Time{}, true); got != nil {
		t.Fatalf("a Job created while Radar watched reads incomplete: %+v", got)
	}

	got := workloadHistoryCoverage(t.Context(), store, ctx, key, liveObject("job-2", started.Add(-time.Hour), nil), time.Time{}, true)
	if got == nil || !got.Incomplete || !got.CompleteSince.Equal(started.UTC()) || !slices.Equal(got.Reasons, []issuesapi.HistoryGapReason{issuesapi.HistoryObservationStartedLate}) {
		t.Fatalf("a Job older than Radar's observation = %+v", got)
	}
	if got := workloadHistoryCoverage(t.Context(), store, ctx, key, liveObject("job-2", started.Add(-time.Hour), nil), time.Time{}, false); got == nil || !got.CompleteSince.IsZero() {
		t.Fatalf("a namespace-limited caller gets the store-wide time: %+v", got)
	}

	watched := started.Add(10 * time.Minute)
	got = workloadHistoryCoverage(t.Context(), store, ctx, key, liveObject("job-2", started.Add(time.Minute), nil), watched, true)
	if got == nil || !got.CompleteSince.Equal(watched.Add(time.Second).Truncate(time.Second).UTC()) {
		t.Fatalf("a later watch of the Workload kind = %+v", got)
	}

	if got := workloadHistoryCoverage(t.Context(), store, ctx, key, liveIdentity{}, time.Time{}, true); got == nil || !got.Incomplete {
		t.Fatalf("a deleted Job's window can't be dated: %+v", got)
	}

	seedKueueHistory(t, store)
	if got := workloadHistoryCoverage(t.Context(), store, ctx, key, liveObject("job-2", started.Add(time.Minute), nil), time.Time{}, true); got == nil || !got.Incomplete {
		t.Fatalf("an earlier incarnation under the key (job-1) predates the live Job: %+v", got)
	}
}

func TestKueueReadGaps(t *testing.T) {
	workload := resourceapi.KindRef{Kind: "Workload", Group: "kueue.x-k8s.io"}
	request := resourceapi.KindRef{Kind: "ProvisioningRequest", Group: "autoscaling.x-k8s.io"}
	early, late := time.Now().Add(-time.Hour), time.Now().Add(-time.Minute)

	gaps, since := kueueReadGaps("ml", []kueueKindRead{
		{ref: workload, allowed: true, authoritative: true, watchedSince: early},
		{ref: request, allowed: true, authoritative: true, watchedSince: late},
	})
	if len(gaps) != 0 || !since.Equal(late) {
		t.Fatalf("all readable: gaps %+v since %v", gaps, since)
	}

	gaps, since = kueueReadGaps("ml", []kueueKindRead{
		{ref: workload, allowed: false, authoritative: true},
		{ref: request, allowed: false, authoritative: true},
	})
	if len(gaps) != 1 || gaps[0].Code != readreason.UserForbidden || gaps[0].Kinds.Count != 2 ||
		gaps[0].Namespaces.Count != 1 || gaps[0].Namespaces.Names[0] != "ml" || !since.IsZero() {
		t.Fatalf("both denied: %+v", gaps)
	}

	gaps, _ = kueueReadGaps("ml", []kueueKindRead{
		{ref: workload, allowed: false, authoritative: false},
		{ref: request, allowed: true, authoritative: true, uncollected: []readreason.Code{readreason.RadarForbidden}},
	})
	if len(gaps) != 2 || gaps[0].Code != readreason.UserPermissionCheckFailed || gaps[1].Code != readreason.RadarForbidden ||
		gaps[1].Kinds.Names[0] != request {
		t.Fatalf("check failed + not collected: %+v", gaps)
	}
}

func TestKueueManagedRoot_QueueLabel(t *testing.T) {
	if !kueueManagedRoot(nil, schema.GroupVersionResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Resource: "workloads"}, "ml", liveObject("job-2", time.Now(), map[string]string{kueueQueueNameLabel: "team-a"}), true) {
		t.Fatal("a queue-labelled Job isn't recognized")
	}
}

func TestKueueManagedRoot_UnreadableOrDeletedDoesNotConsultInventory(t *testing.T) {
	for _, live := range []liveIdentity{liveObject("job", time.Now(), nil), {}} {
		if !kueueManagedRoot(nil, schema.GroupVersionResource{}, "ml", live, false) {
			t.Fatal("unknown affiliation must qualify independently of hidden inventory")
		}
	}
}

func TestWorkloadHistory_ExpiredOwnershipAlwaysQualifiesScope(t *testing.T) {
	store := withWorkloadHistoryStore(t)
	for i := 0; i < 1001; i++ {
		if err := store.Append(t.Context(), timeline.TimelineEvent{ID: fmt.Sprintf("event-%d", i), Timestamp: time.Now().Add(-time.Hour), ClusterContext: k8s.ActiveClusterContext(), Kind: "ConfigMap", APIVersion: "v1", Namespace: "ml", Name: "noise", UID: "noise"}); err != nil {
			t.Fatal(err)
		}
	}
	if timeline.HistoryEvictedThrough().IsZero() {
		t.Fatal("test did not evict")
	}
	code, resp := getWorkloadHistory(t, "/api/workloads/jobs/ml/train/history")
	if code != 200 || !resp.Incomplete {
		t.Fatalf("status=%d incomplete=%v; expired ancestry must remain unknown independently of current root age", code, resp.Incomplete)
	}
}
