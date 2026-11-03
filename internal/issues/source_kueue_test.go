package issues

import (
	"encoding/json"
	"os"
	"sort"
	"strings"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/schedulinginsight"
)

// fakeKueueProvider serves Kueue inventories from a fixed object set and
// resolves Workload roots by following controller references whose UID
// matches an object in the set, as the ownership view does.
type fakeKueueProvider struct {
	*fakeProvider
	objects     []*unstructured.Unstructured
	gaps        map[string]issuesapi.UncheckedReason
	rootsBroken bool
	reads       map[string]int
}

func newFakeKueueProvider(objects ...*unstructured.Unstructured) *fakeKueueProvider {
	return &fakeKueueProvider{fakeProvider: &fakeProvider{}, objects: objects, gaps: map[string]issuesapi.UncheckedReason{}, reads: map[string]int{}}
}

func (p *fakeKueueProvider) kueueInventory(group, kind string, namespaces []string) kueueInventory {
	key := group + "/" + kind
	p.reads[key]++
	if gap, ok := p.gaps[key]; ok {
		return kueueInventory{Gap: gap}
	}
	inNamespace := map[string]bool{}
	for _, ns := range namespaces {
		inNamespace[ns] = true
	}
	var items []*unstructured.Unstructured
	for _, o := range p.objects {
		if o.GroupVersionKind().Group != group || o.GetKind() != kind {
			continue
		}
		if o.GetNamespace() != "" && len(namespaces) > 0 && !inNamespace[o.GetNamespace()] {
			continue
		}
		items = append(items, o)
	}
	return kueueInventory{Items: items, OK: true}
}

func (p *fakeKueueProvider) kueueRoots() kueueRootResolver { return fakeKueueRoots{p} }

type fakeKueueRoots struct{ p *fakeKueueProvider }

func (r fakeKueueRoots) chain(w *unstructured.Unstructured) []Ref {
	current := w
	var out []Ref
	for depth := 0; depth < 8; depth++ {
		owner := metav1.GetControllerOf(current)
		if owner == nil {
			break
		}
		group := resourceid.GroupFromAPIVersion(owner.APIVersion)
		var next *unstructured.Unstructured
		for _, o := range r.p.objects {
			if o.GroupVersionKind().Group == group && o.GetKind() == owner.Kind && o.GetNamespace() == current.GetNamespace() && o.GetName() == owner.Name && o.GetUID() == owner.UID {
				next = o
			}
		}
		if next == nil {
			break
		}
		current = next
		out = append(out, Ref{Group: group, Kind: owner.Kind, Namespace: next.GetNamespace(), Name: next.GetName()})
	}
	return out
}

func (r fakeKueueRoots) valid() bool { return !r.p.rootsBroken }

func loadKueueLiveFixture(t *testing.T) []*unstructured.Unstructured {
	t.Helper()
	return loadKueueFixture(t, "testdata/kueue/queue-scenarios-live.json")
}

func loadKueueFixture(t *testing.T, path string) []*unstructured.Unstructured {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var list struct {
		Items []map[string]any `json:"items"`
	}
	if err := json.Unmarshal(data, &list); err != nil {
		t.Fatal(err)
	}
	out := make([]*unstructured.Unstructured, 0, len(list.Items))
	for _, item := range list.Items {
		// JSON numbers decode as float64; unstructured accessors want int64.
		out = append(out, &unstructured.Unstructured{Object: normalizeJSONNumbers(item).(map[string]any)})
	}
	return out
}

func normalizeJSONNumbers(v any) any {
	switch t := v.(type) {
	case map[string]any:
		for k, child := range t {
			t[k] = normalizeJSONNumbers(child)
		}
		return t
	case []any:
		for i, child := range t {
			t[i] = normalizeJSONNumbers(child)
		}
		return t
	case float64:
		if t == float64(int64(t)) {
			return int64(t)
		}
	}
	return v
}

type kueueRow struct{ subject, owner, reason, category string }

func kueueRows(list []Issue) []kueueRow {
	var out []kueueRow
	for _, i := range list {
		if !strings.HasPrefix(i.Reason, "Kueue") {
			continue
		}
		row := kueueRow{subject: i.Kind + " " + i.Namespace + "/" + i.Name, reason: i.Reason, category: string(i.Category)}
		if i.Owner.Kind != "" {
			row.owner = i.Owner.Group + "/" + i.Owner.Kind + " " + i.Owner.Name
		}
		out = append(out, row)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].subject+out[a].reason < out[b].subject+out[b].reason })
	return out
}

var allowAllEvidence = func(EvidenceRead) bool { return true }

const (
	kueueExceedsMessage = "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (1) > maximum capacity (200m)"
	kueueWaitMessage    = "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 50m more needed"
)

// The live fixture is every Kueue scenario object on a test cluster
// (Kueue v0.18.0), produced by the real controller (see testdata/kueue/README.md).
func TestKueueLiveScenarioFindings(t *testing.T) {
	p := newFakeKueueProvider(loadKueueLiveFixture(t)...)
	got, stats := ComposeWithStats(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	want := []kueueRow{
		{subject: "ClusterQueue /gpu-night-819-missing-flavor", reason: ReasonKueueClusterQueueInactive, category: "queue_admission_blocked"},
		{subject: "LocalQueue gpu-night-819-missing-flavor/missing-flavor", reason: ReasonKueueClusterQueueInactive, category: "queue_admission_blocked"},
		{subject: "Workload gpu-night-819-busy/job-check-rejected-779e4", owner: "batch/Job check-rejected", reason: ReasonKueueAdmissionCheckRejected, category: "queue_admission_blocked"},
		{subject: "Workload gpu-night-819-ineligible/job-not-eligible-6abe4", owner: "batch/Job not-eligible", reason: ReasonKueueNamespaceNotEligible, category: "queue_admission_blocked"},
		{subject: "Workload gpu-night-819-lifecycle/job-no-localqueue-8c90a", owner: "batch/Job no-localqueue", reason: ReasonKueueLocalQueueMissing, category: "queue_admission_blocked"},
	}
	if rows := kueueRows(got); !equalKueueRows(rows, want) {
		t.Fatalf("rows:\n%s\nwant:\n%s", formatKueueRows(rows), formatKueueRows(want))
	}
	if len(stats.Unchecked) != 0 {
		t.Fatalf("unchecked = %+v", stats.Unchecked)
	}
	for _, i := range got {
		if !strings.HasPrefix(i.Reason, "Kueue") {
			continue
		}
		if i.Severity != SeverityWarning || i.Cause == "" || i.Action == "" {
			t.Errorf("%s %s: severity %q cause %q action %q", i.Kind, i.Name, i.Severity, i.Cause, i.Action)
		}
		if i.Kind == kueueLocalQueueKind || i.Kind == kueueClusterQueueKind {
			want := []Ref{
				{Group: "batch", Kind: "Job", Namespace: "gpu-night-819-missing-flavor", Name: "blocked"},
				{Group: kueueGroup, Kind: kueueWorkloadKind, Namespace: "gpu-night-819-missing-flavor", Name: "job-blocked-b8211"},
			}
			if len(i.AffectedRefs) != 2 || i.AffectedRefs[0] != want[0] || i.AffectedRefs[1] != want[1] {
				t.Errorf("%s affected = %+v", i.Kind, i.AffectedRefs)
			}
		}
	}
}

func TestKueueLiveScenarioGroupedRowsAreRunsAndQueues(t *testing.T) {
	p := newFakeKueueProvider(loadKueueLiveFixture(t)...)
	got := Compose(p, Filters{Limit: NoLimit, Grouped: true, CanReadEvidence: allowAllEvidence})
	var lq, cq *Issue
	subjects := map[string]bool{}
	for idx := range got {
		i := &got[idx]
		if !strings.HasPrefix(i.Reason, "Kueue") {
			continue
		}
		subjects[i.Kind+"/"+i.Name] = true
		switch i.Kind {
		case kueueLocalQueueKind:
			lq = i
		case kueueClusterQueueKind:
			cq = i
		}
	}
	for _, want := range []string{"Job/check-rejected", "Job/not-eligible", "Job/no-localqueue"} {
		if !subjects[want] {
			t.Errorf("grouped subjects %v lack %s", subjects, want)
		}
	}
	if lq == nil || cq == nil {
		t.Fatalf("queue rows lq=%v cq=%v", lq, cq)
	}
	if lq.IncidentParent == nil || lq.IncidentParent.ID != cq.ID || lq.IncidentParent.FactType != factKueueQueue || lq.IncidentParent.Confidence != issuesapi.ConfidenceHigh {
		t.Fatalf("LocalQueue incident parent = %+v, want the ClusterQueue row %s", lq.IncidentParent, cq.ID)
	}
	if !hasFact(lq, factKueueQueue) || !hasFact(lq, factKueueAffectedRuns) || !hasFact(cq, factKueueAffectedRuns) {
		t.Fatalf("queue facts lq=%+v cq=%+v", lq.DiagnosticContext, cq.DiagnosticContext)
	}
	for _, i := range got {
		if i.Kind == "Job" && i.Name == "check-rejected" && (!hasFact(&i, factKueueAdmission) || !hasFact(&i, factKueueAdmissionCheck)) {
			t.Fatalf("rejected check row facts = %+v", i.DiagnosticContext)
		}
	}
}

func TestKueueRunPageFindsQueueRowThroughAffectedRuns(t *testing.T) {
	p := newFakeKueueProvider(loadKueueLiveFixture(t)...)
	opts := RelatedIssueOptions{
		Namespaces:      []string{"gpu-night-819-missing-flavor"},
		CanReadRelated:  func(Ref) bool { return true },
		CanReadEvidence: allowAllEvidence,
	}
	for _, page := range []Ref{
		{Group: "batch", Kind: "Job", Namespace: "gpu-night-819-missing-flavor", Name: "blocked"},
		{Group: kueueGroup, Kind: kueueWorkloadKind, Namespace: "gpu-night-819-missing-flavor", Name: "job-blocked-b8211"},
	} {
		related := RelatedIssues(p, opts, page.Group, page.Kind, page.Namespace, page.Name)
		if len(related) != 1 || related[0].Kind != kueueLocalQueueKind || related[0].Reason != ReasonKueueClusterQueueInactive {
			t.Fatalf("%s page: related = %+v", page.Kind, related)
		}
	}
}

func hasFact(i *Issue, factType string) bool {
	if i.DiagnosticContext == nil {
		return false
	}
	for _, f := range i.DiagnosticContext.Facts {
		if f.Type == factType {
			return true
		}
	}
	return false
}

func equalKueueRows(a, b []kueueRow) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func formatKueueRows(rows []kueueRow) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString("  " + r.subject + " owner=" + r.owner + " " + r.reason + " " + r.category + "\n")
	}
	return b.String()
}

// ---- synthetic objects ------------------------------------------------------

var kueueTestNow = time.Now()

func kueueTestObject(apiVersion, kind, namespace, name string) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind}}
	u.SetNamespace(namespace)
	u.SetName(name)
	u.SetUID(types.UID(namespace + "/" + kind + "/" + name))
	u.SetGeneration(1)
	u.SetCreationTimestamp(metav1.NewTime(kueueTestNow.Add(-time.Hour)))
	return u
}

func kueueCondition(conditionType, status, reason, message string, observedGeneration int64) map[string]any {
	return map[string]any{
		"type": conditionType, "status": status, "reason": reason, "message": message,
		"observedGeneration": observedGeneration, "lastTransitionTime": kueueTestNow.Add(-10 * time.Minute).UTC().Format(time.RFC3339),
	}
}

func kueueSetConditions(u *unstructured.Unstructured, conditions ...map[string]any) {
	list := make([]any, len(conditions))
	for i, c := range conditions {
		list[i] = c
	}
	_ = unstructured.SetNestedSlice(u.Object, list, "status", "conditions")
}

func kueueJob(namespace, name string) *unstructured.Unstructured {
	return kueueTestObject("batch/v1", "Job", namespace, name)
}

func ownedBy(u, owner *unstructured.Unstructured) *unstructured.Unstructured {
	controller := true
	u.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: owner.GetAPIVersion(), Kind: owner.GetKind(), Name: owner.GetName(), UID: owner.GetUID(), Controller: &controller}})
	return u
}

func kueueWorkload(namespace, name, queue string, conditions ...map[string]any) *unstructured.Unstructured {
	w := kueueTestObject("kueue.x-k8s.io/v1beta2", kueueWorkloadKind, namespace, name)
	_ = unstructured.SetNestedField(w.Object, queue, "spec", "queueName")
	kueueSetConditions(w, conditions...)
	return w
}

// actionableWorkload reports a missing LocalQueue, which the classification reads as
// action_needed.
func actionableWorkload(namespace, name, queue string) *unstructured.Unstructured {
	return kueueWorkload(namespace, name, queue, kueueCondition("QuotaReserved", "False", "Inadmissible", "LocalQueue "+queue+" doesn't exist", 1))
}

func exceedsWorkload(namespace, name, queue string) *unstructured.Unstructured {
	return kueueWorkload(namespace, name, queue, kueueCondition("QuotaReserved", "False", "Pending", kueueExceedsMessage, 1))
}

func inactiveQueueWorkload(namespace, name, queue, clusterQueue string) *unstructured.Unstructured {
	return kueueWorkload(namespace, name, queue, kueueCondition("QuotaReserved", "False", "Inadmissible", "ClusterQueue "+clusterQueue+" is inactive", 1))
}

func kueueLocalQueue(namespace, name, clusterQueue, activeStatus, reason string) *unstructured.Unstructured {
	lq := kueueTestObject("kueue.x-k8s.io/v1beta2", kueueLocalQueueKind, namespace, name)
	_ = unstructured.SetNestedField(lq.Object, clusterQueue, "spec", "clusterQueue")
	_ = unstructured.SetNestedField(lq.Object, "None", "spec", "stopPolicy")
	kueueSetConditions(lq, kueueCondition("Active", activeStatus, reason, "Can't submit new workloads to clusterQueue", 1))
	return lq
}

func kueueClusterQueue(name, stopPolicy, activeStatus, reason, message string) *unstructured.Unstructured {
	cq := kueueTestObject("kueue.x-k8s.io/v1beta2", kueueClusterQueueKind, "", name)
	_ = unstructured.SetNestedField(cq.Object, stopPolicy, "spec", "stopPolicy")
	kueueSetConditions(cq, kueueCondition("Active", activeStatus, reason, message, 1))
	return cq
}

func brokenClusterQueue(name string) *unstructured.Unstructured {
	return kueueClusterQueue(name, "None", "False", "FlavorNotFound", "Can't admit new workloads: references missing ResourceFlavor(s): gpu.")
}

func kueueReasons(list []Issue) map[string]string {
	out := map[string]string{}
	for _, i := range list {
		if strings.HasPrefix(i.Reason, "Kueue") {
			out[i.Kind+" "+i.Namespace+"/"+i.Name] = i.Reason
		}
	}
	return out
}

// ---- policy -----------------------------------------------------------------

func TestKueueWaitingPauseAndUnknownRaiseNothing(t *testing.T) {
	user := kueueWorkload("team", "user-paused", "q", kueueCondition("QuotaReserved", "False", "Pending", "The workload is deactivated", 2), kueueCondition("Evicted", "True", "Deactivated", "The workload is deactivated", 2))
	user.SetGeneration(2)
	_ = unstructured.SetNestedField(user.Object, false, "spec", "active")
	unrecognized := kueueWorkload("team", "unrecognized", "q", kueueCondition("QuotaReserved", "False", "SomethingNew", "a reason Kueue added later", 1))
	waiting := kueueWorkload("team", "waiting", "q", kueueCondition("QuotaReserved", "False", "Pending", kueueWaitMessage, 1))
	preempted := kueueWorkload("team", "preempted", "q",
		kueueCondition("QuotaReserved", "False", "Pending", kueueWaitMessage, 1),
		kueueCondition("Evicted", "True", "Preempted", "Preempted to accommodate a workload", 1),
		kueueCondition("Preempted", "True", "InClusterQueue", "Preempted to accommodate a workload", 1))
	podsReady := kueueWorkload("team", "pods-ready", "q",
		kueueCondition("QuotaReserved", "False", "Pending", kueueWaitMessage, 1),
		kueueCondition("Evicted", "True", "PodsReadyTimeout", "Exceeded the PodsReady timeout", 1))
	finished := kueueWorkload("team", "finished", "q", kueueCondition("Finished", "True", "Failed", "Job failed", 1))
	// Kueue's message names only the flavors it tried last, so the classification can't
	// tell "never fits" from "not now".
	exceeds := exceedsWorkload("team", "exceeds", "q")
	mismatch := kueueWorkload("team", "mismatch", "q", kueueCondition("QuotaReserved", "False", "Pending", "couldn't assign flavors to pod set main: flavor default doesn't match node affinity", 1))
	p := newFakeKueueProvider(user, unrecognized, waiting, preempted, podsReady, finished, exceeds, mismatch, kueueLocalQueue("team", "q", "cq", "True", "Ready"))
	if got := kueueReasons(Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})); len(got) != 0 {
		t.Fatalf("rows = %v, want none", got)
	}
}

func TestKueueStaleEvidenceRaisesNothing(t *testing.T) {
	w := actionableWorkload("team", "stale", "q")
	w.SetGeneration(3) // the condition was written for generation 1
	cq := brokenClusterQueue("cq")
	cq.SetGeneration(4)
	lq := kueueLocalQueue("team", "q", "cq", "False", kueueLocalQueueClusterQueueInactive)
	p := newFakeKueueProvider(w, cq, lq, inactiveQueueWorkload("team", "dep", "q", "cq"))
	if got := kueueReasons(Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})); len(got) != 0 {
		t.Fatalf("rows = %v, want none from stale Workload and ClusterQueue evidence", got)
	}
}

func TestKueueClusterQueueStopPolicyTerminatingAndUnknownAreNotBroken(t *testing.T) {
	for _, tc := range []struct {
		name string
		cq   *unstructured.Unstructured
	}{
		{"hold", kueueClusterQueue("cq", "Hold", "False", "Stopped", "Can't admit new workloads: is stopped.")},
		{"hold and drain with a broken flavor too", kueueClusterQueue("cq", "HoldAndDrain", "False", "Stopped", "Can't admit new workloads: is stopped, references missing ResourceFlavor(s): gpu.")},
		{"terminating", kueueClusterQueue("cq", "None", "False", "Terminating", "Can't admit new workloads; clusterQueue is terminating")},
		{"unknown", kueueClusterQueue("cq", "None", "False", "Unknown", "Can't admit new workloads.")},
		{"a reason Kueue adds later", kueueClusterQueue("cq", "None", "False", "SomethingNew", "Can't admit new workloads: something.")},
		{"recovered", kueueClusterQueue("cq", "None", "True", "Ready", "Can admit new workloads")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakeKueueProvider(tc.cq, kueueLocalQueue("team", "q", "cq", "False", kueueLocalQueueClusterQueueInactive), inactiveQueueWorkload("team", "w", "q", "cq"))
			if got := kueueReasons(Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})); len(got) != 0 {
				t.Fatalf("rows = %v, want none", got)
			}
		})
	}
}

func TestKueueBrokenClusterQueueWithoutRunsStillRaised(t *testing.T) {
	p := newFakeKueueProvider(brokenClusterQueue("cq"), kueueLocalQueue("team", "q", "cq", "False", kueueLocalQueueClusterQueueInactive))
	got := Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	reasons := kueueReasons(got)
	if reasons["ClusterQueue /cq"] != ReasonKueueClusterQueueInactive || reasons["LocalQueue team/q"] != ReasonKueueClusterQueueInactive || len(reasons) != 2 {
		t.Fatalf("rows = %v", reasons)
	}
	for _, i := range got {
		if len(i.AffectedRefs) != 0 || hasFact(&i, factKueueAffectedRuns) {
			t.Fatalf("%s lists affected runs with none blocked: %+v", i.Kind, i.AffectedRefs)
		}
	}
}

func TestKueueLocalQueueWhoseClusterQueueIsMissing(t *testing.T) {
	lq := kueueLocalQueue("team", "q", "gone", "False", kueueLocalQueueClusterQueueMissing)
	job := kueueJob("team", "train")
	w := ownedBy(kueueWorkload("team", "w", "q", kueueCondition("QuotaReserved", "False", "Inadmissible", "ClusterQueue gone doesn't exist", 1)), job)
	p := newFakeKueueProvider(lq, job, w)
	got := Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	reasons := kueueReasons(got)
	if len(reasons) != 1 || reasons["LocalQueue team/q"] != ReasonKueueClusterQueueMissing {
		t.Fatalf("rows = %v, want only the LocalQueue row (the run folds into it)", reasons)
	}
	if got[0].RequiredReads != nil {
		t.Fatalf("Kueue's own LocalQueue report needs no ClusterQueue read: %+v", got[0].RequiredReads)
	}

	// Without the LocalQueue (unreadable or not marked yet) the run reports it.
	p = newFakeKueueProvider(job, w)
	p.gaps[kueueGroup+"/"+kueueLocalQueueKind] = issuesapi.UncheckedReasonSyncPending
	reasons = kueueReasons(Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence}))
	if reasons["Workload team/w"] != ReasonKueueClusterQueueMissing {
		t.Fatalf("rows = %v, want the run's own row", reasons)
	}
}

func TestKueueSharedClusterQueueIncident(t *testing.T) {
	cq := brokenClusterQueue("gpu")
	jobA1, jobA2, jobB := kueueJob("team-a", "a1"), kueueJob("team-a", "a2"), kueueJob("team-b", "b1")
	objects := []*unstructured.Unstructured{
		cq, jobA1, jobA2, jobB,
		kueueLocalQueue("team-a", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive),
		kueueLocalQueue("team-b", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive),
		ownedBy(inactiveQueueWorkload("team-a", "wa1", "q", "gpu"), jobA1),
		ownedBy(inactiveQueueWorkload("team-a", "wa2", "q", "gpu"), jobA2),
		ownedBy(inactiveQueueWorkload("team-b", "wb1", "q", "gpu"), jobB),
	}
	p := newFakeKueueProvider(objects...)
	got := Compose(p, Filters{Limit: NoLimit, Grouped: true, CanReadEvidence: allowAllEvidence})
	reasons := kueueReasons(got)
	if len(reasons) != 3 || reasons["ClusterQueue /gpu"] == "" || reasons["LocalQueue team-a/q"] == "" || reasons["LocalQueue team-b/q"] == "" {
		t.Fatalf("rows = %v, want one row per LocalQueue plus the ClusterQueue, none per run", reasons)
	}
	for _, i := range got {
		if i.Kind == kueueLocalQueueKind && (i.IncidentParent == nil || i.IncidentParent.Ref.Name != "gpu") {
			t.Fatalf("LocalQueue %s parent = %+v", i.Namespace, i.IncidentParent)
		}
		if i.Kind == kueueClusterQueueKind && len(i.AffectedRefs) != 6 {
			t.Fatalf("ClusterQueue affected = %+v", i.AffectedRefs)
		}
	}

	// A caller who can read only team-a sees its LocalQueue, and the
	// ClusterQueue links only the runs it can read, with no count.
	readA := func(ref Ref) bool { return ref.Namespace == "" || ref.Namespace == "team-a" }
	scoped := Compose(p, Filters{Limit: NoLimit, Grouped: true, CanReadEvidence: allowAllEvidence, CanReadSubject: readA, CanReadRelated: readA})
	for _, i := range scoped {
		if i.Kind == kueueLocalQueueKind && i.Namespace == "team-b" {
			t.Fatalf("team-b LocalQueue reached a team-a caller")
		}
		if i.Kind != kueueClusterQueueKind {
			continue
		}
		if len(i.AffectedRefs) != 4 {
			t.Fatalf("ClusterQueue affected for team-a caller = %+v", i.AffectedRefs)
		}
		for _, f := range i.DiagnosticContext.Facts {
			if f.Type != factKueueAffectedRuns {
				continue
			}
			for _, ref := range f.Refs {
				if ref.Namespace != "team-a" {
					t.Fatalf("unreadable run linked: %+v", ref)
				}
			}
			if strings.ContainsAny(f.Message, "0123456789") {
				t.Fatalf("affected-runs fact carries a count: %q", f.Message)
			}
		}
	}
}

func TestKueueClusterQueueRowsOnlyWithoutNamespaceFilter(t *testing.T) {
	p := newFakeKueueProvider(brokenClusterQueue("gpu"), kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive))
	reasons := kueueReasons(Compose(p, Filters{Limit: NoLimit, Namespaces: []string{"team"}, CanReadEvidence: allowAllEvidence}))
	if len(reasons) != 1 || reasons["LocalQueue team/q"] == "" {
		t.Fatalf("rows = %v, want the namespaced LocalQueue row only", reasons)
	}
}

func TestKueueCallerWhoCannotReadClusterQueuesGetsCoverage(t *testing.T) {
	p := newFakeKueueProvider(brokenClusterQueue("gpu"), kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive), inactiveQueueWorkload("team", "w", "q", "gpu"))
	denyClusterQueues := func(kind, group string) bool { return !(group == kueueGroup && kind == kueueClusterQueueKind) }
	got, stats := ComposeWithStats(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence, CanReadClusterScoped: denyClusterQueues})
	if reasons := kueueReasons(got); len(reasons) != 0 {
		t.Fatalf("rows = %v: a stop policy and a broken queue must look the same to this caller", reasons)
	}
	if p.reads[kueueGroup+"/"+kueueClusterQueueKind] != 0 {
		t.Fatalf("ClusterQueues read for a caller who can't read them")
	}
	want := issuesapi.UncheckedSource{Source: issuesapi.UncheckedSourceKueue, Reason: issuesapi.UncheckedReasonCallerAccessDenied, Kinds: []issuesapi.UncheckedKind{{Group: kueueGroup, Kind: kueueClusterQueueKind}}}
	if len(stats.Unchecked) != 1 || stats.Unchecked[0].Source != want.Source || stats.Unchecked[0].Reason != want.Reason || len(stats.Unchecked[0].Kinds) != 1 || stats.Unchecked[0].Kinds[0] != want.Kinds[0] {
		t.Fatalf("unchecked = %+v, want %+v", stats.Unchecked, want)
	}

	// The same answer when the queue is stopped on purpose.
	stopped := newFakeKueueProvider(kueueClusterQueue("gpu", "Hold", "False", "Stopped", "is stopped"), kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive), inactiveQueueWorkload("team", "w", "q", "gpu"))
	_, stoppedStats := ComposeWithStats(stopped, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence, CanReadClusterScoped: denyClusterQueues})
	if len(stoppedStats.Unchecked) != 1 || stoppedStats.Unchecked[0].Reason != issuesapi.UncheckedReasonCallerAccessDenied {
		t.Fatalf("stopped queue unchecked = %+v", stoppedStats.Unchecked)
	}
}

func TestKueueLocalQueueRowNeedsClusterQueueEvidence(t *testing.T) {
	job := kueueJob("team", "train")
	p := newFakeKueueProvider(brokenClusterQueue("gpu"), job, kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive), ownedBy(inactiveQueueWorkload("team", "w", "q", "gpu"), job))
	var withheld []Issue
	denyClusterQueueList := func(read EvidenceRead) bool { return !(read.Group == kueueGroup && read.Resource == "clusterqueues") }
	// A memoized compose authorizes the ClusterQueue read only at projection.
	got := Compose(p, Filters{Limit: NoLimit, Namespaces: []string{"team"}, CanReadEvidence: denyClusterQueueList, OnEvidenceWithheld: func(i Issue) { withheld = append(withheld, i) }})
	if reasons := kueueReasons(got); len(reasons) != 0 {
		t.Fatalf("rows = %v", reasons)
	}
	if len(withheld) != 1 || withheld[0].Kind != kueueLocalQueueKind {
		t.Fatalf("withheld = %+v, want the LocalQueue row", withheld)
	}
}

// The server checks the same "list clusterqueues" permission for the
// cluster-scoped gate and for evidence; a run page then says it couldn't
// check, the same way for a stopped and a broken queue.
func TestKueueRunPageReportsTheUncheckedQueueForACallerWhoCannotReadClusterQueues(t *testing.T) {
	denyCQ := func(kind, group string) bool { return !(group == kueueGroup && kind == kueueClusterQueueKind) }
	denyCQList := func(read EvidenceRead) bool { return !(read.Group == kueueGroup && read.Resource == "clusterqueues") }
	readAll := func(Ref) bool { return true }
	for _, cq := range []*unstructured.Unstructured{brokenClusterQueue("gpu"), kueueClusterQueue("gpu", "Hold", "False", "Stopped", "is stopped")} {
		job := kueueJob("team", "train")
		other := kueueJob("team", "unrelated")
		p := newFakeKueueProvider(cq, job, other, kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive), ownedBy(inactiveQueueWorkload("team", "w", "q", "gpu"), job))
		for _, page := range []struct {
			ref  Ref
			want bool
		}{
			{Ref{Group: "batch", Kind: "Job", Namespace: "team", Name: "train"}, true},
			{Ref{Group: kueueGroup, Kind: kueueWorkloadKind, Namespace: "team", Name: "w"}, true},
			{Ref{Group: kueueGroup, Kind: kueueLocalQueueKind, Namespace: "team", Name: "q"}, true},
			{Ref{Group: "batch", Kind: "Job", Namespace: "team", Name: "unrelated"}, false},
		} {
			var gaps []issuesapi.UncheckedSource
			related := RelatedIssues(p, RelatedIssueOptions{
				Namespaces: []string{"team"}, CanReadClusterScoped: denyCQ, CanReadEvidence: denyCQList, CanReadRelated: readAll, CanReadSubject: readAll,
				OnUnchecked: func(g []issuesapi.UncheckedSource) { gaps = g },
			}, page.ref.Group, page.ref.Kind, page.ref.Namespace, page.ref.Name)
			if len(related) != 0 {
				t.Fatalf("%s %s: related = %+v", cq.GetName(), page.ref.Name, related)
			}
			if got := len(gaps) == 1 && gaps[0].Source == issuesapi.UncheckedSourceKueue && gaps[0].Reason == issuesapi.UncheckedReasonCallerAccessDenied && gaps[0].Subjects == nil; got != page.want {
				t.Fatalf("%s %s page: gaps = %+v, want reported=%v", cq.GetName(), page.ref.Name, gaps, page.want)
			}
		}
	}
}

func TestKueueQueuesAreCheckedWithoutWorkloadsOrLocalQueues(t *testing.T) {
	p := newFakeKueueProvider(brokenClusterQueue("gpu"), kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive))
	p.gaps[kueueGroup+"/"+kueueWorkloadKind] = issuesapi.UncheckedReasonCollectorAccessDenied
	reasons := kueueReasons(Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence}))
	if reasons["ClusterQueue /gpu"] == "" || reasons["LocalQueue team/q"] == "" {
		t.Fatalf("rows without Workloads = %v", reasons)
	}
	p.gaps[kueueGroup+"/"+kueueLocalQueueKind] = issuesapi.UncheckedReasonSyncPending
	reasons = kueueReasons(Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence}))
	if len(reasons) != 1 || reasons["ClusterQueue /gpu"] == "" {
		t.Fatalf("rows without LocalQueues = %v", reasons)
	}
}

func TestKueueEveryAllowlistedClusterQueueReasonIsBroken(t *testing.T) {
	for reason := range kueueBrokenClusterQueueReasons {
		cq := kueueClusterQueue("cq", "None", "False", reason, "Can't admit new workloads: "+reason)
		if _, _, broken := kueueBrokenClusterQueue(cq); !broken {
			t.Errorf("%s not treated as broken", reason)
		}
	}
}

func TestKueueQueueGraceRunsFromTheActiveTransition(t *testing.T) {
	lq := kueueLocalQueue("team", "q", "gone", "False", kueueLocalQueueClusterQueueMissing)
	active := kueueCondition("Active", "False", kueueLocalQueueClusterQueueMissing, "Can't submit new workloads to clusterQueue", 1)
	active["lastTransitionTime"] = time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	kueueSetConditions(lq, active)
	if reasons := kueueReasons(Compose(newFakeKueueProvider(lq), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})); len(reasons) != 0 {
		t.Fatalf("rows = %v, want none for a queue that just went inactive", reasons)
	}
}

func TestKueueReplacedCachesKeepWorkloadsAsAffected(t *testing.T) {
	job := kueueJob("team", "train")
	p := newFakeKueueProvider(brokenClusterQueue("gpu"), job, kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive), ownedBy(inactiveQueueWorkload("team", "w", "q", "gpu"), job))
	p.rootsBroken = true
	for _, i := range Compose(p, Filters{Limit: NoLimit, Namespaces: []string{"team"}, CanReadEvidence: allowAllEvidence}) {
		if i.Kind != kueueLocalQueueKind {
			continue
		}
		if len(i.AffectedRefs) != 1 || i.AffectedRefs[0].Kind != kueueWorkloadKind || !hasFact(&i, factKueueAffectedRuns) {
			t.Fatalf("affected = %+v facts = %+v", i.AffectedRefs, i.DiagnosticContext)
		}
		return
	}
	t.Fatal("no LocalQueue row")
}

func TestKueueCronJobRunIsFoundFromItsJob(t *testing.T) {
	cronJob := kueueTestObject("batch/v1", "CronJob", "team", "nightly")
	job := ownedBy(kueueJob("team", "nightly-1"), cronJob)
	p := newFakeKueueProvider(cronJob, job, ownedBy(actionableWorkload("team", "w", "q"), job))
	related := RelatedIssues(p, RelatedIssueOptions{Namespaces: []string{"team"}, CanReadRelated: func(Ref) bool { return true }, CanReadEvidence: allowAllEvidence}, "batch", "Job", "team", "nightly-1")
	if len(related) != 1 || related[0].Kind != "CronJob" || related[0].Reason != ReasonKueueLocalQueueMissing {
		t.Fatalf("related = %+v", related)
	}
}

// ---- attribution// ---- attribution ------------------------------------------------------------

func TestKueueRootAttribution(t *testing.T) {
	cronJob := kueueTestObject("batch/v1", "CronJob", "team", "nightly")
	scheduled := ownedBy(kueueJob("team", "nightly-1"), cronJob)
	jobSet := kueueTestObject("jobset.x-k8s.io/v1alpha2", "JobSet", "team", "trainer")
	rayJob := kueueTestObject("ray.io/v1", "RayJob", "team", "tune")
	recreated := kueueJob("team", "recreated")
	staleOwner := actionableWorkload("team", "w-recreated", "q")
	controller := true
	staleOwner.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "recreated", UID: "an-earlier-incarnation", Controller: &controller}})
	p := newFakeKueueProvider(
		cronJob, scheduled, jobSet, rayJob, recreated,
		ownedBy(actionableWorkload("team", "w-cron", "q"), scheduled),
		ownedBy(actionableWorkload("team", "w-jobset", "q"), jobSet),
		ownedBy(actionableWorkload("team", "w-ray", "q"), rayJob),
		staleOwner,
		actionableWorkload("team", "w-standalone", "q"),
	)
	owners := map[string]Ref{}
	for _, i := range Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence}) {
		if i.Reason == ReasonKueueLocalQueueMissing {
			owners[i.Name] = i.Owner
		}
	}
	want := map[string]Ref{
		"w-cron":       {Group: "batch", Kind: "CronJob", Namespace: "team", Name: "nightly"},
		"w-jobset":     {Group: "jobset.x-k8s.io", Kind: "JobSet", Namespace: "team", Name: "trainer"},
		"w-ray":        {Group: "ray.io", Kind: "RayJob", Namespace: "team", Name: "tune"},
		"w-recreated":  {},
		"w-standalone": {},
	}
	for name, ref := range want {
		got, ok := owners[name]
		if !ok || got != ref {
			t.Errorf("%s owner = %+v (present %v), want %+v", name, got, ok, ref)
		}
	}

	p.rootsBroken = true
	for _, i := range Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence}) {
		if i.Reason == ReasonKueueLocalQueueMissing && i.Owner.Kind != "" {
			t.Fatalf("owner claimed from replaced caches: %+v", i.Owner)
		}
	}
}

func TestKueueRecreatedRootIsNotGivenTheOldRunsFinding(t *testing.T) {
	job := kueueJob("team", "train")
	old := actionableWorkload("team", "w-old", "q")
	controller := true
	old.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: "train", UID: "the-deleted-job", Controller: &controller}})
	current := ownedBy(kueueWorkload("team", "w-new", "q", kueueCondition("QuotaReserved", "False", "Pending", kueueWaitMessage, 1)), job)
	got := Compose(newFakeKueueProvider(job, old, current), Filters{Limit: NoLimit, Grouped: true, CanReadEvidence: allowAllEvidence})
	for _, i := range got {
		if i.Kind == "Job" && i.Name == "train" {
			t.Fatalf("the recreated Job inherited the old run's finding: %+v", i)
		}
	}
	if reasons := kueueReasons(got); reasons["Workload team/w-old"] != ReasonKueueLocalQueueMissing {
		t.Fatalf("rows = %v, want the orphaned Workload's own row", reasons)
	}
}

func TestKueueConcurrentAdmissionVariantsRaiseNothing(t *testing.T) {
	job := kueueJob("team", "train")
	parent := ownedBy(actionableWorkload("team", "parent", "q"), job)
	parent.SetLabels(map[string]string{"kueue.x-k8s.io/concurrent-admission-parent": "true"})
	variant := ownedBy(actionableWorkload("team", "variant-a", "q"), parent)
	got := Compose(newFakeKueueProvider(job, parent, variant), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	if reasons := kueueReasons(got); len(reasons) != 0 {
		t.Fatalf("rows = %v", reasons)
	}
}

func TestKueueConfigurationGrace(t *testing.T) {
	young := actionableWorkload("team", "young", "q")
	young.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-10 * time.Second)))
	youngLQ := kueueLocalQueue("team", "new", "gone", "False", kueueLocalQueueClusterQueueMissing)
	youngLQ.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-10 * time.Second)))
	if reasons := kueueReasons(Compose(newFakeKueueProvider(young, youngLQ), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})); len(reasons) != 0 {
		t.Fatalf("rows = %v, want none inside the grace", reasons)
	}
}

func TestKueueRejectedCheckKeepsOneIdentityThroughDeactivation(t *testing.T) {
	job := kueueJob("team", "train")
	rejected := ownedBy(kueueWorkload("team", "w", "q",
		kueueCondition("QuotaReserved", "True", "QuotaReserved", "Quota reserved in ClusterQueue cq", 1)), job)
	_ = unstructured.SetNestedSlice(rejected.Object, []any{map[string]any{"name": "capacity", "state": "Rejected", "message": "no capacity", "lastTransitionTime": kueueTestNow.Add(-time.Minute).UTC().Format(time.RFC3339)}}, "status", "admissionChecks")
	deactivated := ownedBy(kueueWorkload("team", "w", "q",
		kueueCondition("QuotaReserved", "False", "Pending", "The workload is deactivated due to AdmissionCheck in Rejected state", 2),
		kueueCondition("Evicted", "True", "DeactivatedDueToAdmissionCheck", "The workload is deactivated due to AdmissionCheck in Rejected state", 2)), job)
	deactivated.SetGeneration(2)
	_ = unstructured.SetNestedField(deactivated.Object, false, "spec", "active")
	var ids []string
	for _, w := range []*unstructured.Unstructured{rejected, deactivated} {
		got := Compose(newFakeKueueProvider(job, w), Filters{Limit: NoLimit, Grouped: true, CanReadEvidence: allowAllEvidence})
		for _, i := range got {
			if i.Reason == ReasonKueueAdmissionCheckRejected {
				ids = append(ids, i.ID)
			}
		}
	}
	if len(ids) != 2 || ids[0] != ids[1] {
		t.Fatalf("ids = %v, want one identity across rejection and deactivation", ids)
	}
}

func TestKueueExecutionStopsAreTheirOwnCategory(t *testing.T) {
	for cause, reason := range map[string]string{
		"DeactivatedDueToRequeuingLimitExceeded":       ReasonKueueRequeuingLimitExceeded,
		"InactiveWorkloadRequeuingLimitExceeded":       ReasonKueueRequeuingLimitExceeded,
		"DeactivatedDueToMaximumExecutionTimeExceeded": ReasonKueueMaximumExecutionTimeExceeded,
	} {
		w := kueueWorkload("team", "w", "q",
			kueueCondition("QuotaReserved", "False", "Pending", "The workload is deactivated", 2),
			kueueCondition("Evicted", "True", cause, "The workload is deactivated", 2))
		w.SetGeneration(2)
		if strings.HasPrefix(cause, "InactiveWorkload") {
			w.SetAPIVersion("kueue.x-k8s.io/v1beta1")
		}
		_ = unstructured.SetNestedField(w.Object, false, "spec", "active")
		got := Compose(newFakeKueueProvider(w), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
		if len(got) != 1 || got[0].Reason != reason || got[0].Category != issuesapi.CategoryBatchRunStopped || got[0].CategoryGroup != issuesapi.GroupRuntime {
			t.Fatalf("%s: rows = %+v", cause, got)
		}
		if !got[0].OnsetUnknown || !got[0].FirstSeen.IsZero() {
			t.Fatalf("%s: an eviction transition does not date the stop: %+v", cause, got[0])
		}
	}
}

func TestKueueCheckRowsLinkChecksAndOwnedProvisioningRequests(t *testing.T) {
	job := kueueJob("team", "train")
	w := ownedBy(kueueWorkload("team", "w", "q", kueueCondition("QuotaReserved", "True", "QuotaReserved", "reserved", 1)), job)
	_ = unstructured.SetNestedSlice(w.Object, []any{map[string]any{"name": "capacity", "state": "Rejected", "message": "no capacity"}}, "status", "admissionChecks")
	check := kueueTestObject("kueue.x-k8s.io/v1beta2", kueueAdmissionCheckKind, "", "capacity")
	_ = unstructured.SetNestedField(check.Object, "kueue.x-k8s.io/provisioning-request", "spec", "controllerName")
	kueueSetConditions(check, kueueCondition("Active", "True", "Active", "The admission check is active", 1))
	owned := ownedBy(kueueTestObject("autoscaling.x-k8s.io/v1", provisioningRequestKind, "team", "w-capacity-1"), w)
	kueueSetConditions(owned, kueueCondition("Failed", "True", "CapacityUnavailable", "out of g5 capacity", 1))
	other := kueueTestObject("autoscaling.x-k8s.io/v1", provisioningRequestKind, "team", "w-capacity-2")
	controller := true
	other.SetOwnerReferences([]metav1.OwnerReference{{APIVersion: "kueue.x-k8s.io/v1beta2", Kind: kueueWorkloadKind, Name: "w", UID: "an-earlier-w", Controller: &controller}})
	p := newFakeKueueProvider(job, w, check, owned, other)
	got := Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	if len(got) != 1 || got[0].Reason != ReasonKueueAdmissionCheckRejected {
		t.Fatalf("rows = %+v", got)
	}
	var checkFact, prFacts []issuesapi.DiagnosticFact
	for _, f := range got[0].DiagnosticContext.Facts {
		switch f.Type {
		case factKueueAdmissionCheck:
			checkFact = append(checkFact, f)
		case factKueueProvisioningRequest:
			prFacts = append(prFacts, f)
		}
	}
	if len(checkFact) != 1 || !strings.Contains(checkFact[0].Message, "Active=True") || !strings.Contains(checkFact[0].Message, "provisioning-request") {
		t.Fatalf("check facts = %+v", checkFact)
	}
	if len(prFacts) != 1 || prFacts[0].Refs[0].Name != "w-capacity-1" || !strings.Contains(prFacts[0].Message, "Failed=True (CapacityUnavailable): out of g5 capacity") {
		t.Fatalf("provisioning facts = %+v", prFacts)
	}

	// A caller who can't read AdmissionChecks or ProvisioningRequests gets
	// the row without those links or what they say.
	onlyWorkloads := func(ref Ref) bool { return ref.Kind == kueueWorkloadKind || ref.Kind == "Job" }
	scoped := Compose(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence, CanReadRelated: onlyWorkloads})
	if len(scoped) != 1 || hasFact(&scoped[0], factKueueAdmissionCheck) || hasFact(&scoped[0], factKueueProvisioningRequest) || !hasFact(&scoped[0], factKueueAdmission) {
		t.Fatalf("redacted facts = %+v", scoped[0].DiagnosticContext)
	}
}

// ---- coverage and filters ---------------------------------------------------

func TestKueueEvidenceOnlyGapsThinTheRowNotTheCoverage(t *testing.T) {
	job := kueueJob("team", "train")
	w := ownedBy(kueueWorkload("team", "w", "q", kueueCondition("QuotaReserved", "True", "QuotaReserved", "reserved", 1)), job)
	_ = unstructured.SetNestedSlice(w.Object, []any{map[string]any{"name": "capacity", "state": "Rejected", "message": "no capacity"}}, "status", "admissionChecks")
	p := newFakeKueueProvider(job, w)
	p.gaps["autoscaling.x-k8s.io/"+provisioningRequestKind] = issuesapi.UncheckedReasonCollectorAccessDenied
	p.gaps[kueueGroup+"/"+kueueAdmissionCheckKind] = issuesapi.UncheckedReasonSyncPending
	got, stats := ComposeWithStats(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	if len(stats.Unchecked) != 0 {
		t.Fatalf("unchecked = %+v: evidence-only kinds hide no finding", stats.Unchecked)
	}
	if len(got) != 1 {
		t.Fatalf("rows = %+v", got)
	}
	var said []string
	for _, f := range got[0].DiagnosticContext.Facts {
		said = append(said, f.Message)
	}
	joined := strings.Join(said, " | ")
	if !strings.Contains(joined, "couldn't read ProvisioningRequests") || !strings.Contains(joined, "Radar couldn't read it") {
		t.Fatalf("facts = %s", joined)
	}
}

func TestKueueInventoryGapsAreUnchecked(t *testing.T) {
	p := newFakeKueueProvider(actionableWorkload("team", "w", "q"))
	p.gaps[kueueGroup+"/"+kueueWorkloadKind] = issuesapi.UncheckedReasonSyncPending
	got, stats := ComposeWithStats(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	if reasons := kueueReasons(got); len(reasons) != 0 {
		t.Fatalf("rows = %v", reasons)
	}
	if len(stats.Unchecked) != 1 || stats.Unchecked[0].Source != issuesapi.UncheckedSourceKueue || stats.Unchecked[0].Reason != issuesapi.UncheckedReasonSyncPending {
		t.Fatalf("unchecked = %+v", stats.Unchecked)
	}

	p = newFakeKueueProvider(kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive), inactiveQueueWorkload("team", "w", "q", "gpu"))
	p.gaps[kueueGroup+"/"+kueueClusterQueueKind] = issuesapi.UncheckedReasonCollectorAccessDenied
	got, stats = ComposeWithStats(p, Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	if reasons := kueueReasons(got); len(reasons) != 0 {
		t.Fatalf("rows = %v", reasons)
	}
	if len(stats.Unchecked) != 1 || stats.Unchecked[0].Reason != issuesapi.UncheckedReasonCollectorAccessDenied {
		t.Fatalf("unchecked = %+v", stats.Unchecked)
	}
}

func TestKueueRowsSurviveRootKindFilter(t *testing.T) {
	jobSet := kueueTestObject("jobset.x-k8s.io/v1alpha2", "JobSet", "team", "trainer")
	job := kueueJob("team", "train")
	p := newFakeKueueProvider(jobSet, job, ownedBy(actionableWorkload("team", "wj", "q"), job), ownedBy(actionableWorkload("team", "ws", "q"), jobSet))
	for kind, name := range map[string]string{"Job": "train", "JobSet": "trainer"} {
		got := Compose(p, Filters{Limit: NoLimit, Grouped: true, Kinds: []string{kind}, CanReadEvidence: allowAllEvidence})
		if len(got) != 1 || got[0].Kind != kind || got[0].Name != name {
			t.Fatalf("Kinds=[%s]: %+v", kind, got)
		}
	}
}

func TestKueueRunRowsCarryTheirOwnEvidence(t *testing.T) {
	job := kueueJob("team", "train")
	got := Compose(newFakeKueueProvider(job, ownedBy(actionableWorkload("team", "w", "q"), job)), Filters{Limit: NoLimit, Grouped: true, CanReadEvidence: allowAllEvidence})
	if len(got) != 1 {
		t.Fatalf("rows = %+v", got)
	}
	i := got[0]
	if i.Kind != "Job" || len(i.Members) != 1 || i.Members[0].Kind != kueueWorkloadKind || i.Message != "LocalQueue q doesn't exist" || !i.OnsetUnknown {
		t.Fatalf("row = %+v", i)
	}
	if !hasFact(&i, factKueueAdmission) {
		t.Fatalf("facts = %+v", i.DiagnosticContext)
	}
}

func BenchmarkKueueCompose(b *testing.B) {
	objects := []*unstructured.Unstructured{brokenClusterQueue("gpu")}
	for n := 0; n < 20; n++ {
		ns := "team-" + string(rune('a'+n))
		objects = append(objects, kueueLocalQueue(ns, "q", "gpu", "False", kueueLocalQueueClusterQueueInactive))
		for i := 0; i < 100; i++ {
			job := kueueJob(ns, "job-"+string(rune('a'+i%26))+string(rune('a'+i/26)))
			var w *unstructured.Unstructured
			switch i % 3 {
			case 0:
				w = inactiveQueueWorkload(ns, job.GetName(), "q", "gpu")
			case 1:
				w = kueueWorkload(ns, job.GetName(), "q", kueueCondition("QuotaReserved", "False", "Pending", kueueWaitMessage, 1))
			default:
				w = actionableWorkload(ns, job.GetName(), "q")
			}
			objects = append(objects, job, ownedBy(w, job))
		}
	}
	p := newFakeKueueProvider(objects...)
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = Compose(p, Filters{Limit: NoLimit, Grouped: true, CanReadEvidence: allowAllEvidence})
	}
}

func TestKueueChainReadsTheClassifiedIncarnation(t *testing.T) {
	w := actionableWorkload("team", "w", "q")
	subject := resourceid.Ref{Group: kueueGroup, Kind: kueueWorkloadKind, Namespace: "team", Name: "w"}
	job := resourceid.Ref{Group: "batch", Kind: "Job", Namespace: "team", Name: "train"}
	cron := resourceid.Ref{Group: "batch", Kind: "CronJob", Namespace: "team", Name: "nightly"}
	read := func(uid types.UID) ownership.Subject {
		return ownership.Subject{Access: ownership.Allowed, Read: ownership.ObjectRead{State: ownership.Present, Object: ownership.Observation{Ref: subject, UID: uid}}}
	}
	for _, tc := range []struct {
		name   string
		answer ownership.ControllerChainAnswer
		want   int
	}{
		{"the whole chain, root last", ownership.ControllerChainAnswer{Subject: read(w.GetUID()), Chain: []resourceid.Ref{subject, job, cron}}, 2},
		{"recreated under the same name since it was listed", ownership.ControllerChainAnswer{Subject: read("another-incarnation"), Chain: []resourceid.Ref{subject, job}}, 0},
		{"no controller", ownership.ControllerChainAnswer{Subject: read(w.GetUID()), Chain: []resourceid.Ref{subject}}, 0},
		{"cycle", ownership.ControllerChainAnswer{Subject: read(w.GetUID()), Chain: []resourceid.Ref{subject, job}, Cycle: true}, 0},
		{"not read", ownership.ControllerChainAnswer{}, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := kueueChain(tc.answer, w)
			if len(got) != tc.want || (tc.want == 2 && (got[0].Name != "train" || got[1].Name != "nightly")) {
				t.Fatalf("chain = %+v", got)
			}
		})
	}
}

// Kueue's real provisioning AdmissionCheck controller reacting to a provider
// that failed twice (retry limit 1) and to capacity revoked from an admitted
// run (testdata/kueue/README.md).
func TestKueueLiveProvisioningOutcomes(t *testing.T) {
	p := newFakeKueueProvider(loadKueueFixture(t, "testdata/kueue/provisioning-live.json")...)
	got := Compose(p, Filters{Limit: NoLimit, Grouped: true, CanReadEvidence: allowAllEvidence})
	want := map[string]string{
		"provisioning-fails": "Failed=True (CapacityUnavailable): RAD-843 fixture provider: no capacity for 1 node (attempt 2)",
		"capacity-revoked":   "CapacityRevoked=True (CapacityRevoked): RAD-843 fixture provider: the nodes were reclaimed",
	}
	seen := 0
	for _, i := range got {
		outcome, ok := want[i.Name]
		if !ok || i.Kind != "Job" {
			continue
		}
		seen++
		if i.Reason != ReasonKueueAdmissionCheckRejected || i.Category != issuesapi.CategoryQueueAdmissionBlocked {
			t.Errorf("%s: %s %s", i.Name, i.Reason, i.Category)
		}
		var facts []string
		for _, f := range i.DiagnosticContext.Facts {
			facts = append(facts, f.Type+": "+f.Message)
		}
		joined := strings.Join(facts, " | ")
		if !strings.Contains(joined, outcome) || !strings.Contains(joined, "AdmissionCheck gpu-night-843-prov (controller kueue.x-k8s.io/provisioning-request): Active=True") {
			t.Errorf("%s facts: %s", i.Name, joined)
		}
	}
	if seen != 2 {
		t.Fatalf("rows = %v, want both provisioning runs", kueueReasons(got))
	}
}

func TestKueueClusterQueueInventoryGapReachesTheRunPages(t *testing.T) {
	job := kueueJob("team", "train")
	p := newFakeKueueProvider(job, kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive), ownedBy(inactiveQueueWorkload("team", "w", "q", "gpu"), job))
	p.gaps[kueueGroup+"/"+kueueClusterQueueKind] = issuesapi.UncheckedReasonCollectorAccessDenied
	var gaps []issuesapi.UncheckedSource
	related := RelatedIssues(p, RelatedIssueOptions{Namespaces: []string{"team"}, CanReadRelated: func(Ref) bool { return true }, CanReadEvidence: allowAllEvidence,
		OnUnchecked: func(g []issuesapi.UncheckedSource) { gaps = g }}, "batch", "Job", "team", "train")
	if len(related) != 0 || len(gaps) != 1 || gaps[0].Reason != issuesapi.UncheckedReasonCollectorAccessDenied || gaps[0].Kinds[0].Kind != kueueClusterQueueKind {
		t.Fatalf("related = %+v gaps = %+v", related, gaps)
	}
}

func TestKueueRunsWaitWithALocalQueueThatHasNotSettled(t *testing.T) {
	lq := kueueLocalQueue("team", "q", "gone", "False", kueueLocalQueueClusterQueueMissing)
	active := kueueCondition("Active", "False", kueueLocalQueueClusterQueueMissing, "Can't submit new workloads to clusterQueue", 1)
	active["lastTransitionTime"] = time.Now().Add(-10 * time.Second).UTC().Format(time.RFC3339)
	kueueSetConditions(lq, active)
	w := kueueWorkload("team", "w", "q", kueueCondition("QuotaReserved", "False", "Inadmissible", "ClusterQueue gone doesn't exist", 1))
	if reasons := kueueReasons(Compose(newFakeKueueProvider(lq, w), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})); len(reasons) != 0 {
		t.Fatalf("rows = %v: the run raised its own row while its LocalQueue was still in its grace", reasons)
	}
}

func TestKueueRejectedCheckIsLinkedFirst(t *testing.T) {
	w := kueueWorkload("team", "w", "q", kueueCondition("QuotaReserved", "True", "QuotaReserved", "reserved", 1))
	_ = unstructured.SetNestedSlice(w.Object, []any{
		map[string]any{"name": "a", "state": "Ready"}, map[string]any{"name": "b", "state": "Ready"},
		map[string]any{"name": "c", "state": "Ready"}, map[string]any{"name": "z-capacity", "state": "Rejected", "message": "no capacity"},
	}, "status", "admissionChecks")
	got := Compose(newFakeKueueProvider(w), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	if len(got) != 1 {
		t.Fatalf("rows = %+v", got)
	}
	var linked []string
	for _, f := range got[0].DiagnosticContext.Facts {
		if f.Type == factKueueAdmissionCheck {
			linked = append(linked, f.Refs[0].Name)
		}
	}
	if len(linked) != 1 || linked[0] != "z-capacity" {
		t.Fatalf("linked checks = %v", linked)
	}
}

func TestKueueBlockedRunsReadRunByRun(t *testing.T) {
	jobA, jobB := kueueJob("team", "a"), kueueJob("team", "b")
	p := newFakeKueueProvider(brokenClusterQueue("gpu"), jobA, jobB, kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive),
		ownedBy(inactiveQueueWorkload("team", "wb", "q", "gpu"), jobB), ownedBy(inactiveQueueWorkload("team", "wa", "q", "gpu"), jobA))
	for _, i := range Compose(p, Filters{Limit: NoLimit, Namespaces: []string{"team"}, CanReadEvidence: allowAllEvidence}) {
		if i.Kind != kueueLocalQueueKind {
			continue
		}
		var names []string
		for _, ref := range i.AffectedRefs {
			names = append(names, ref.Kind+"/"+ref.Name)
		}
		if strings.Join(names, ",") != "Job/a,Workload/wa,Job/b,Workload/wb" {
			t.Fatalf("affected = %v", names)
		}
		return
	}
	t.Fatal("no LocalQueue row")
}

func TestUncheckedForMatchesOnlyNamedSubjects(t *testing.T) {
	sources := []issuesapi.UncheckedSource{
		{Source: issuesapi.UncheckedSourceKueue, Reason: issuesapi.UncheckedReasonCallerAccessDenied, Subjects: []Ref{{Group: "batch", Kind: "Job", Namespace: "team", Name: "train"}}},
		{Source: issuesapi.UncheckedSourceCustomResources, Reason: issuesapi.UncheckedReasonSyncPending},
	}
	got := UncheckedFor(sources, "batch", "job", "team", "train")
	if len(got) != 1 || got[0].Source != issuesapi.UncheckedSourceKueue || got[0].Subjects != nil {
		t.Fatalf("got = %+v", got)
	}
	if got := UncheckedFor(sources, "batch", "Job", "team", "other"); len(got) != 0 {
		t.Fatalf("other Job got %+v", got)
	}
}

func TestKueueResourceTheQueueDoesNotOfferIsRaised(t *testing.T) {
	job := kueueJob("team", "train")
	w := ownedBy(kueueWorkload("team", "w", "q", kueueCondition("QuotaReserved", "False", "Pending", "couldn't assign flavors to pod set main: resource nvidia.com/gpu unavailable in ClusterQueue", 1)), job)
	got := Compose(newFakeKueueProvider(job, w), Filters{Limit: NoLimit, Grouped: true, CanReadEvidence: allowAllEvidence})
	if len(got) != 1 || got[0].Reason != ReasonKueueResourceUnavailable || got[0].Kind != "Job" || got[0].Category != issuesapi.CategoryQueueAdmissionBlocked {
		t.Fatalf("rows = %+v", got)
	}
}

func TestKueueRecoveredQueueOutranksALaggingRun(t *testing.T) {
	job := kueueJob("team", "train")
	for name, objects := range map[string][]*unstructured.Unstructured{
		"LocalQueue active again": {job, kueueLocalQueue("team", "q", "gpu", "True", "Ready"),
			ownedBy(kueueWorkload("team", "w", "q", kueueCondition("QuotaReserved", "False", "Inadmissible", "ClusterQueue gpu doesn't exist", 1)), job)},
		"LocalQueue created since": {job, kueueLocalQueue("team", "q", "gpu", "True", "Ready"), ownedBy(actionableWorkload("team", "w", "q"), job)},
		"ClusterQueue just changed": {job, func() *unstructured.Unstructured {
			cq := brokenClusterQueue("gpu")
			active := kueueCondition("Active", "False", "FlavorNotFound", "Can't admit new workloads: references missing ResourceFlavor(s): gpu.", 1)
			active["lastTransitionTime"] = time.Now().Add(-5 * time.Second).UTC().Format(time.RFC3339)
			kueueSetConditions(cq, active)
			return cq
		}(), kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive), ownedBy(inactiveQueueWorkload("team", "w", "q", "gpu"), job)},
	} {
		if reasons := kueueReasons(Compose(newFakeKueueProvider(objects...), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})); len(reasons) != 0 {
			t.Errorf("%s: rows = %v", name, reasons)
		}
	}
}

func TestKueueWorkloadInventoryGapReachesRunPages(t *testing.T) {
	p := newFakeKueueProvider(kueueJob("team", "train"))
	p.gaps[kueueGroup+"/"+kueueWorkloadKind] = issuesapi.UncheckedReasonSyncPending
	for _, page := range []Ref{{Group: "batch", Kind: "Job", Namespace: "team", Name: "train"}, {Group: "apps", Kind: "Deployment", Namespace: "team", Name: "web"}} {
		var gaps []issuesapi.UncheckedSource
		RelatedIssues(p, RelatedIssueOptions{Namespaces: []string{"team"}, CanReadRelated: func(Ref) bool { return true }, CanReadEvidence: allowAllEvidence,
			OnUnchecked: func(g []issuesapi.UncheckedSource) { gaps = g }}, page.Group, page.Kind, page.Namespace, page.Name)
		want := page.Kind == "Job"
		if got := len(gaps) == 1 && gaps[0].Reason == issuesapi.UncheckedReasonSyncPending; got != want {
			t.Fatalf("%s page: gaps = %+v, want reported=%v", page.Kind, gaps, want)
		}
	}
}

func TestKueueAttemptLocalFlavorFailuresAreUnknownNotIssues(t *testing.T) {
	for _, scenario := range []struct{ message, state string }{
		{kueueExceedsMessage, "exceeds_quota"},
		{"couldn't assign flavors to pod set main: flavor default doesn't match node affinity", "no_matching_flavor"},
	} {
		w := kueueWorkload("team", "w", "q", kueueCondition("QuotaReserved", "False", "Pending", scenario.message, 1))
		classification, ok := schedulinginsight.ClassifyKueueWorkload(w)
		if !ok || string(classification.Admission.State) != scenario.state || classification.Admission.Attention != "unknown" {
			t.Fatalf("attempt-local evidence must remain unknown: %+v", classification)
		}
		if reasons := kueueReasons(Compose(newFakeKueueProvider(w), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})); len(reasons) != 0 {
			t.Fatalf("attempt-local evidence raised an Issue: %v", reasons)
		}
	}
}

func TestKueueDeletingRunIsNotAffectedByQueueIssue(t *testing.T) {
	w := inactiveQueueWorkload("team", "w", "q", "gpu")
	at := metav1.Now()
	w.SetDeletionTimestamp(&at)
	rows := Compose(newFakeKueueProvider(w, brokenClusterQueue("gpu"), kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive)), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	for _, row := range rows {
		if len(row.AffectedRefs) != 0 || row.Kind == kueueWorkloadKind {
			t.Fatalf("deleting run was claimed: %+v", row)
		}
	}
}

func TestKueueInventoryGapSurvivesSubjectSpecificQueueGap(t *testing.T) {
	p := newFakeKueueProvider(kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive))
	p.gaps[kueueGroup+"/"+kueueWorkloadKind] = issuesapi.UncheckedReasonSyncPending
	p.gaps[kueueGroup+"/"+kueueClusterQueueKind] = issuesapi.UncheckedReasonSyncPending
	_, stats := ComposeWithStats(p, Filters{Namespaces: []string{"team"}, Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	gaps := UncheckedFor(stats.Unchecked, "batch", "Job", "team", "unlisted-run")
	if len(gaps) != 1 || gaps[0].Reason != issuesapi.UncheckedReasonSyncPending || len(gaps[0].Subjects) != 0 {
		t.Fatalf("Workload inventory gap lost when merged with a LocalQueue subject: %+v", gaps)
	}
	if gaps := UncheckedFor(stats.Unchecked, "apps", "Deployment", "team", "web"); len(gaps) != 0 {
		t.Fatalf("unrelated page unchecked: %+v", gaps)
	}
}

func TestKueueMissingQueueFallbackDoesNotOutvotePauseOrRecovery(t *testing.T) {
	for _, stopped := range []bool{false, true} {
		lq := kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueInactive)
		if stopped {
			_ = unstructured.SetNestedField(lq.Object, "Hold", "spec", "stopPolicy")
		}
		cq := kueueClusterQueue("gpu", "None", "True", "Ready", "Can admit new workloads")
		w := kueueWorkload("team", "lagging", "q", kueueCondition("QuotaReserved", "False", "Inadmissible", "ClusterQueue gpu doesn't exist", 1))
		rows := kueueReasons(Compose(newFakeKueueProvider(w, lq, cq), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence}))
		if len(rows) != 0 {
			t.Fatalf("stopped=%v: lagging missing-queue report overrode current queue: %v", stopped, rows)
		}
	}
}

func TestKueueDeactivationDoesNotReuseEarlierEvictionOnset(t *testing.T) {
	w := kueueWorkload("team", "stopped", "q", kueueCondition("Evicted", "True", "DeactivatedDueToRequeuingLimitExceeded", "requeuing limit reached", 1))
	_ = unstructured.SetNestedField(w.Object, false, "spec", "active")
	rows := Compose(newFakeKueueProvider(w), Filters{Limit: NoLimit, CanReadEvidence: allowAllEvidence})
	if len(rows) != 1 || rows[0].Reason != ReasonKueueRequeuingLimitExceeded || !rows[0].FirstSeen.IsZero() || !rows[0].OnsetUnknown || rows[0].IssueTiming != "" {
		t.Fatalf("retained eviction timestamp was asserted as deactivation onset: %+v", rows)
	}
}

func TestKueueCreatedClusterQueueOutranksMissingLocalQueueReport(t *testing.T) {
	for _, state := range []struct{ status, reason, policy string }{
		{"True", "Ready", "None"},
		{"False", "Stopped", "Hold"},
		{"False", "FlavorNotFound", "None"},
	} {
		cq := kueueClusterQueue("gpu", state.policy, state.status, state.reason, "current queue evidence")
		cq.SetCreationTimestamp(metav1.NewTime(time.Now().Add(-5 * time.Second)))
		lq := kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueMissing)
		w := kueueWorkload("team", "lagging", "q", kueueCondition("QuotaReserved", "False", "Inadmissible", "ClusterQueue gpu doesn't exist", 1))
		rows := Compose(newFakeKueueProvider(w, lq, cq), Filters{Namespaces: []string{"team"}, Limit: NoLimit, CanReadEvidence: allowAllEvidence})
		if len(rows) != 0 {
			t.Fatalf("%+v: current queue did not suppress the obsolete missing report: %+v", state, rows)
		}
	}
}

func TestKueueMissingQueueRecoveryDoesNotUseUnreadableInventory(t *testing.T) {
	lq := kueueLocalQueue("team", "q", "gpu", "False", kueueLocalQueueClusterQueueMissing)
	cq := kueueClusterQueue("gpu", "None", "True", "Ready", "current")
	p := newFakeKueueProvider(lq, cq)
	rows := Compose(p, Filters{Namespaces: []string{"team"}, Limit: NoLimit, CanReadEvidence: func(read EvidenceRead) bool { return read.Resource != "clusterqueues" }})
	if len(rows) != 1 || rows[0].Reason != ReasonKueueClusterQueueMissing || p.reads[kueueGroup+"/"+kueueClusterQueueKind] != 0 {
		t.Fatalf("unreadable recovery influenced native evidence: rows=%+v reads=%v", rows, p.reads)
	}
}

func TestKueueClusterQueueInventoryGapReachesItsOwnPage(t *testing.T) {
	p := newFakeKueueProvider()
	p.gaps[kueueGroup+"/"+kueueClusterQueueKind] = issuesapi.UncheckedReasonSyncPending
	for _, group := range []string{kueueGroup, "unrelated.io"} {
		var gaps []issuesapi.UncheckedSource
		RelatedIssues(p, RelatedIssueOptions{CanReadEvidence: allowAllEvidence, OnUnchecked: func(g []issuesapi.UncheckedSource) { gaps = g }}, group, kueueClusterQueueKind, "", "gpu")
		if group == kueueGroup {
			if len(gaps) != 1 || gaps[0].Reason != issuesapi.UncheckedReasonSyncPending || len(gaps[0].Subjects) != 0 {
				t.Fatalf("ClusterQueue own inventory failure lost: %+v", gaps)
			}
		} else if len(gaps) != 0 {
			t.Fatalf("same kind in another group inherited Kueue gap: %+v", gaps)
		}
	}
}
