package k8s

import (
	"context"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/timeline"
)

type kueueCondition struct{ typ, status, reason, message string }

func kueueWorkload(apiVersion string, mutate ...func(map[string]any)) *unstructured.Unstructured {
	obj := map[string]any{
		"apiVersion": apiVersion,
		"kind":       "Workload",
		"metadata": map[string]any{
			"name": "job-train-1a2b3", "namespace": "ml", "uid": "wl-uid", "resourceVersion": "1", "generation": int64(1),
			"creationTimestamp": "2026-11-03T01:00:00Z",
			"ownerReferences": []any{map[string]any{
				"apiVersion": "batch/v1", "kind": "Job", "name": "train", "uid": "job-uid", "controller": true,
			}},
		},
		"spec":   map[string]any{"queueName": "team-a", "podSets": []any{map[string]any{"name": "main", "count": int64(1)}}},
		"status": map[string]any{},
	}
	for _, m := range mutate {
		m(obj)
	}
	return &unstructured.Unstructured{Object: obj}
}

func withConditions(conds ...kueueCondition) func(map[string]any) {
	return func(obj map[string]any) {
		items := make([]any, 0, len(conds))
		for _, c := range conds {
			items = append(items, map[string]any{
				"type": c.typ, "status": c.status, "reason": c.reason, "message": c.message,
				"lastTransitionTime": "2026-11-03T01:00:00Z", "observedGeneration": int64(1),
			})
		}
		obj["status"].(map[string]any)["conditions"] = items
	}
}

func withAdmission(cq string, count int64, flavors map[string]any, usage map[string]any) func(map[string]any) {
	return func(obj map[string]any) {
		obj["status"].(map[string]any)["admission"] = map[string]any{
			"clusterQueue": cq,
			"podSetAssignments": []any{map[string]any{
				"name": "main", "count": count, "flavors": flavors, "resourceUsage": usage,
			}},
		}
	}
}

func withCheck(name, state, message string, retries int64) func(map[string]any) {
	return func(obj map[string]any) {
		status := obj["status"].(map[string]any)
		checks, _ := status["admissionChecks"].([]any)
		check := map[string]any{"name": name, "state": state, "message": message, "lastTransitionTime": "2026-11-03T01:00:00Z"}
		if retries > 0 {
			check["retryCount"] = retries
		}
		status["admissionChecks"] = append(checks, check)
	}
}

func withStatus(key string, value any) func(map[string]any) {
	return func(obj map[string]any) { obj["status"].(map[string]any)[key] = value }
}

func withSpec(key string, value any) func(map[string]any) {
	return func(obj map[string]any) {
		obj["spec"].(map[string]any)[key] = value
		meta := obj["metadata"].(map[string]any)
		meta["generation"] = meta["generation"].(int64) + 1
	}
}

const v1beta2 = "kueue.x-k8s.io/v1beta2"

func fieldPaths(d *DiffInfo) []string {
	if d == nil {
		return nil
	}
	out := make([]string, len(d.Fields))
	for i, f := range d.Fields {
		out[i] = f.Path
	}
	return out
}

func field(t *testing.T, d *DiffInfo, path string) FieldChange {
	t.Helper()
	if d == nil {
		t.Fatalf("no diff; want field %s", path)
	}
	for _, f := range d.Fields {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("diff has no %s; fields %v", path, fieldPaths(d))
	return FieldChange{}
}

const pendingMsg = "couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 50m more needed"

func TestKueueWorkloadDiff_AdmissionStory(t *testing.T) {
	pending := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", pendingMsg}))
	reserved := kueueWorkload(v1beta2,
		withConditions(kueueCondition{"QuotaReserved", "True", "QuotaReserved", "Quota reserved in ClusterQueue busy"}),
		withAdmission("busy", 1, map[string]any{"cpu": "default", "memory": "default"}, map[string]any{"cpu": "20m", "memory": "16Mi"}),
		withCheck("prov", "Pending", "", 0))

	d := ComputeDiff("Workload", pending, reserved)
	if got := field(t, d, "status.conditions[QuotaReserved]"); got.OldValue != "False (Pending)" || got.NewValue != "True (QuotaReserved)" {
		t.Fatalf("QuotaReserved = %v → %v", got.OldValue, got.NewValue)
	}
	if got := field(t, d, "status.conditions[QuotaReserved].message"); got.NewValue != "Quota reserved in ClusterQueue busy" {
		t.Fatalf("message = %v", got.NewValue)
	}
	if got := field(t, d, "status.admission.clusterQueue"); got.OldValue != nil || got.NewValue != "busy" {
		t.Fatalf("clusterQueue = %v → %v", got.OldValue, got.NewValue)
	}
	if got := field(t, d, "status.admission.podSetAssignments[main]"); got.NewValue != "count 1; cpu=default (20m); memory=default (16Mi)" {
		t.Fatalf("assignment = %v", got.NewValue)
	}
	if got := field(t, d, "status.admissionChecks[prov]"); got.OldValue != nil || got.NewValue != "Pending" {
		t.Fatalf("check = %v → %v", got.OldValue, got.NewValue)
	}
	for _, want := range []string{"quota reserved in ClusterQueue busy", "check prov: Pending"} {
		if !strings.Contains(d.Summary, want) {
			t.Fatalf("summary %q lacks %q", d.Summary, want)
		}
	}
	if strings.Contains(d.Summary, "admission assignment") {
		t.Fatalf("summary repeats the assignment the QuotaReserved transition names: %q", d.Summary)
	}

	ready := kueueWorkload(v1beta2,
		withConditions(
			kueueCondition{"QuotaReserved", "True", "QuotaReserved", "Quota reserved in ClusterQueue busy"},
			kueueCondition{"Admitted", "True", "Admitted", "The workload is admitted"}),
		withAdmission("busy", 1, map[string]any{"cpu": "default", "memory": "default"}, map[string]any{"cpu": "20m", "memory": "16Mi"}),
		withCheck("prov", "Ready", "Provisioned", 0))
	d = ComputeDiff("Workload", reserved, ready)
	if got := field(t, d, "status.admissionChecks[prov]"); got.OldValue != "Pending" || got.NewValue != "Ready" {
		t.Fatalf("check = %v → %v", got.OldValue, got.NewValue)
	}
	for _, want := range []string{"admitted", "check prov: Pending → Ready"} {
		if !strings.Contains(d.Summary, want) {
			t.Fatalf("summary %q lacks %q", d.Summary, want)
		}
	}
}

func TestKueueWorkloadDiff_EvictionRequeueAndRetry(t *testing.T) {
	admitted := kueueWorkload(v1beta2,
		withConditions(
			kueueCondition{"QuotaReserved", "True", "QuotaReserved", "Quota reserved in ClusterQueue busy"},
			kueueCondition{"Admitted", "True", "Admitted", "The workload is admitted"}),
		withAdmission("busy", 1, map[string]any{"cpu": "default"}, map[string]any{"cpu": "1"}),
		withCheck("check", "Ready", "", 0))
	evicted := kueueWorkload(v1beta2,
		withConditions(
			kueueCondition{"QuotaReserved", "False", "Preempted", "Preempted to accommodate a workload (UID: x) due to prioritization in the ClusterQueue"},
			kueueCondition{"Admitted", "False", "NoReservation", "The workload has no reservation"},
			kueueCondition{"Evicted", "True", "Preempted", "Preempted to accommodate a workload (UID: x) due to prioritization in the ClusterQueue"},
			kueueCondition{"Preempted", "True", "InClusterQueue", "Preempted to accommodate a workload (UID: x) due to prioritization in the ClusterQueue"},
			kueueCondition{"Requeued", "False", "Preempted", "Preempted to accommodate a workload"}),
		withCheck("check", "Retry", "Reset after eviction", 1),
		withStatus("requeueState", map[string]any{"count": int64(1), "requeueAt": "2026-11-03T01:05:00Z"}),
		withStatus("schedulingStats", map[string]any{"evictions": []any{map[string]any{"reason": "Preempted", "count": int64(1)}}}))

	d := ComputeDiff("Workload", admitted, evicted)
	if got := field(t, d, "status.conditions[Evicted]"); got.OldValue != nil || got.NewValue != "True (Preempted)" {
		t.Fatalf("Evicted = %v → %v", got.OldValue, got.NewValue)
	}
	if got := field(t, d, "status.admission.clusterQueue"); got.OldValue != "busy" || got.NewValue != nil {
		t.Fatalf("clusterQueue = %v → %v", got.OldValue, got.NewValue)
	}
	if got := field(t, d, "status.admissionChecks[check]"); got.OldValue != "Ready" || got.NewValue != "Retry (retry 1)" {
		t.Fatalf("check = %v → %v", got.OldValue, got.NewValue)
	}
	if got := field(t, d, "status.requeueState"); got.NewValue != "count 1, requeue at 2026-11-03T01:05:00Z" {
		t.Fatalf("requeueState = %v", got.NewValue)
	}
	for _, p := range fieldPaths(d) {
		if strings.HasPrefix(p, "status.schedulingStats") {
			t.Fatalf("counter recorded: %v", fieldPaths(d))
		}
	}
	for _, want := range []string{"evicted (Preempted)", "preempted (InClusterQueue)", "requeue scheduled for 2026-11-03T01:05:00Z", "check check: Ready → Retry"} {
		if !strings.Contains(d.Summary, want) {
			t.Fatalf("summary %q lacks %q", d.Summary, want)
		}
	}

	requeued := evicted.DeepCopy()
	conds, _, _ := unstructured.NestedSlice(requeued.Object, "status", "conditions")
	conds[4] = map[string]any{"type": "Requeued", "status": "True", "reason": "BackoffFinished", "message": "The workload backoff was finished"}
	_ = unstructured.SetNestedSlice(requeued.Object, conds, "status", "conditions")
	unstructured.RemoveNestedField(requeued.Object, "status", "requeueState", "requeueAt")
	d = ComputeDiff("Workload", evicted, requeued)
	if !strings.Contains(d.Summary, "requeued (BackoffFinished)") {
		t.Fatalf("summary %q", d.Summary)
	}
	if got := field(t, d, "status.requeueState"); got.NewValue != "count 1" {
		t.Fatalf("requeueState = %v", got.NewValue)
	}
}

func TestKueueWorkloadDiff_MessageRewrites(t *testing.T) {
	base := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", pendingMsg}))
	tests := []struct {
		name    string
		message string
		record  bool
	}{
		{"shortfall only", strings.Replace(pendingMsg, "50m", "100m", 1), false},
		{"capacity number only", "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (2) > maximum capacity (1200m)", true},
		{"flavor with digits", strings.Replace(pendingMsg, "flavor default", "flavor gpu-a100", 1), true},
		{"different blocker", "Workload namespace doesn't match ClusterQueue selector", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			changed := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", tt.message}))
			d := ComputeDiff("Workload", base, changed)
			if tt.record != (d != nil) {
				t.Fatalf("recorded = %v, want %v (diff %+v)", d != nil, tt.record, d)
			}
			if d != nil && d.Summary != "QuotaReserved message changed" {
				t.Fatalf("summary = %q", d.Summary)
			}
		})
	}

	capacity := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", tests[1].message}))
	moreCapacity := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", strings.Replace(tests[1].message, "(1200m)", "(1500m)", 1)}))
	if d := ComputeDiff("Workload", capacity, moreCapacity); d != nil {
		t.Fatalf("a parenthesized quantity alone recorded: %+v", d)
	}
	gpu := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", "insufficient unused quota for nvidia.com/mig-1g.5gb in flavor a, 1 more needed"}))
	otherGPU := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", "insufficient unused quota for nvidia.com/mig-2g.10gb in flavor a, 1 more needed"}))
	if d := ComputeDiff("Workload", gpu, otherGPU); d == nil {
		t.Fatal("a different resource name was masked as a quantity")
	}
}

func TestKueueWorkloadDiff_ReasonOnlyAndLongMessage(t *testing.T) {
	long := strings.Repeat("no quota left ", 40)
	old := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", "a"}))
	cur := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Inadmissible", long}))
	d := ComputeDiff("Workload", old, cur)
	if got := field(t, d, "status.conditions[QuotaReserved]"); got.NewValue != "False (Inadmissible)" {
		t.Fatalf("value = %v", got.NewValue)
	}
	if got := boundMessage("bad\x00byte \xff"); got != "badbyte \uFFFD" {
		t.Fatalf("boundMessage kept bytes Postgres rejects: %q", got)
	}
	msg := field(t, d, "status.conditions[QuotaReserved].message").NewValue.(string)
	if n := len([]rune(msg)); n != maxConditionMessageRunes || !strings.HasSuffix(msg, "…") {
		t.Fatalf("message not bounded: %d runes", n)
	}
	if d.Summary != "QuotaReserved=False (Inadmissible)" {
		t.Fatalf("summary = %q", d.Summary)
	}
}

func TestKueueWorkloadDiff_AssignmentCountAndFlavorChange(t *testing.T) {
	cond := withConditions(kueueCondition{"QuotaReserved", "True", "QuotaReserved", "Quota reserved in ClusterQueue busy"})
	old := kueueWorkload(v1beta2, cond, withAdmission("busy", 4, map[string]any{"cpu": "default"}, map[string]any{"cpu": "4"}))
	cur := kueueWorkload(v1beta2, cond, withAdmission("busy", 2, map[string]any{"cpu": "spare"}, map[string]any{"cpu": "2"}))
	d := ComputeDiff("Workload", old, cur)
	if got := field(t, d, "status.admission.podSetAssignments[main]"); got.OldValue != "count 4; cpu=default (4)" || got.NewValue != "count 2; cpu=spare (2)" {
		t.Fatalf("assignment = %v → %v", got.OldValue, got.NewValue)
	}
	if d.Summary != "admission assignment changed" {
		t.Fatalf("summary = %q", d.Summary)
	}
}

func TestKueueWorkloadDiff_SpecAndOtherStatus(t *testing.T) {
	old := kueueWorkload(v1beta2)
	d := ComputeDiff("Workload", old, kueueWorkload(v1beta2, withSpec("active", false)))
	if got := field(t, d, "spec.active"); got.OldValue != true || got.NewValue != false {
		t.Fatalf("active = %v → %v", got.OldValue, got.NewValue)
	}
	if d.Summary != "deactivated" {
		t.Fatalf("summary = %q (fields %v)", d.Summary, fieldPaths(d))
	}

	d = ComputeDiff("Workload", old, kueueWorkload(v1beta2, withSpec("queueName", "team-b")))
	if d == nil || d.Summary != "queue: team-a → team-b" || len(d.Fields) != 1 {
		t.Fatalf("queue change = %+v", d)
	}

	d = ComputeDiff("Workload", old, kueueWorkload(v1beta2, withSpec("priority", int64(100))))
	if d == nil || !strings.Contains(d.Summary, "priority") {
		t.Fatalf("other spec change not backstopped: %+v", d)
	}

	d = ComputeDiff("Workload", old, kueueWorkload(v1beta2, withStatus("unhealthyNodes", []any{map[string]any{"name": "node-a"}})))
	if d == nil || !strings.Contains(d.Summary, "other status changes: unhealthyNodes") {
		t.Fatalf("unknown status field dropped: %+v", d)
	}

	counterOnly := kueueWorkload(v1beta2, withStatus("accumulatedPastExecutionTimeSeconds", int64(30)))
	if d := ComputeDiff("Workload", old, counterOnly); d != nil {
		t.Fatalf("counter-only update recorded: %+v", d)
	}

	gated := kueueWorkload(v1beta2, func(obj map[string]any) {
		obj["metadata"].(map[string]any)["annotations"] = map[string]any{"kueue.x-k8s.io/admission-gated-by": "example.com/gate", "unrelated": "x"}
	})
	d = ComputeDiff("Workload", old, gated)
	if got := field(t, d, "metadata.annotations[kueue.x-k8s.io/admission-gated-by]"); got.NewValue != "example.com/gate" {
		t.Fatalf("annotation = %v", got.NewValue)
	}
	if len(d.Fields) != 1 {
		t.Fatalf("unrelated annotation recorded: %v", fieldPaths(d))
	}

	gate := func(state string) func(map[string]any) {
		return withStatus("preemptionGates", []any{map[string]any{"name": "example.com/gate", "position": state, "lastTransitionTime": "2026-11-03T01:00:00Z"}})
	}
	d = ComputeDiff("Workload", kueueWorkload(v1beta2, gate("Closed")), kueueWorkload(v1beta2, gate("Open")))
	if d == nil || !strings.Contains(d.Summary, "other status changes: preemptionGates") {
		t.Fatalf("preemption gate opening dropped: %+v", d)
	}

	relabeled := kueueWorkload(v1beta2, func(obj map[string]any) {
		obj["metadata"].(map[string]any)["labels"] = map[string]any{"kueue.x-k8s.io/queue-name": "team-b"}
	})
	if got := field(t, ComputeDiff("Workload", old, relabeled), "metadata.labels[kueue.x-k8s.io/queue-name]"); got.NewValue != "team-b" {
		t.Fatalf("label = %v", got.NewValue)
	}

	deleting := kueueWorkload(v1beta2, func(obj map[string]any) {
		obj["metadata"].(map[string]any)["deletionTimestamp"] = "2026-11-03T02:00:00Z"
	})
	if d := ComputeDiff("Workload", old, deleting); d == nil || d.Summary != "deletion started" {
		t.Fatalf("deletion start = %+v", d)
	}
}

func TestKueueWorkloadDiff_TransitionTimeOnlyAndOtherGroups(t *testing.T) {
	old := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "True", "QuotaReserved", "ok"}), withCheck("c", "Pending", "", 0))
	cur := old.DeepCopy()
	conds, _, _ := unstructured.NestedSlice(cur.Object, "status", "conditions")
	conds[0].(map[string]any)["lastTransitionTime"] = "2026-11-03T03:00:00Z"
	_ = unstructured.SetNestedSlice(cur.Object, conds, "status", "conditions")
	checks, _, _ := unstructured.NestedSlice(cur.Object, "status", "admissionChecks")
	checks[0].(map[string]any)["lastTransitionTime"] = "2026-11-03T03:00:00Z"
	_ = unstructured.SetNestedSlice(cur.Object, checks, "status", "admissionChecks")
	if d := ComputeDiff("Workload", old, cur); d != nil {
		t.Fatalf("timestamp-only update recorded: %+v", d)
	}

	v1beta1Old := kueueWorkload("kueue.x-k8s.io/v1beta1", withConditions(kueueCondition{"Admitted", "False", "NoReservation", ""}))
	v1beta1New := kueueWorkload("kueue.x-k8s.io/v1beta1", withConditions(kueueCondition{"Admitted", "True", "Admitted", ""}))
	if d := ComputeDiff("Workload", v1beta1Old, v1beta1New); d == nil || d.Summary != "admitted" {
		t.Fatalf("v1beta1 = %+v", d)
	}

	foreignOld := kueueWorkload("example.com/v1", withStatus("phase", "Pending"))
	foreignNew := kueueWorkload("example.com/v1", withStatus("phase", "Running"))
	if d := ComputeDiff("Workload", foreignOld, foreignNew); d == nil || d.Summary != "resource changed" {
		t.Fatalf("a foreign-group Workload should use the generic differ: %+v", d)
	}
}

func provisioningRequest(conds ...kueueCondition) *unstructured.Unstructured {
	u := kueueWorkload("autoscaling.x-k8s.io/v1", withConditions(conds...))
	u.SetKind("ProvisioningRequest")
	u.SetName("job-train-1a2b3-prov-1")
	return u
}

func TestProvisioningRequestDiff(t *testing.T) {
	accepted := provisioningRequest(kueueCondition{"Accepted", "True", "Accepted", "accepted"})
	provisioned := provisioningRequest(
		kueueCondition{"Accepted", "True", "Accepted", "accepted"},
		kueueCondition{"Provisioned", "True", "Provisioned", "capacity is ready"})
	d := ComputeDiff("ProvisioningRequest", accepted, provisioned)
	if got := field(t, d, "status.conditions[Provisioned]"); got.NewValue != "True (Provisioned)" {
		t.Fatalf("Provisioned = %v", got.NewValue)
	}
	if d.Summary != "Provisioned=True (Provisioned)" {
		t.Fatalf("summary = %q", d.Summary)
	}
	failed := provisioningRequest(
		kueueCondition{"Accepted", "True", "Accepted", "accepted"},
		kueueCondition{"Failed", "True", "CapacityUnavailable", "no capacity in zone us-east-1a"})
	d = ComputeDiff("ProvisioningRequest", accepted, failed)
	if got := field(t, d, "status.conditions[Failed].message"); got.NewValue != "no capacity in zone us-east-1a" {
		t.Fatalf("Failed message = %v", got.NewValue)
	}
}

func TestKueueHistoricalEvents_StatusOnly(t *testing.T) {
	wl := kueueWorkload(v1beta2,
		withConditions(
			kueueCondition{"QuotaReserved", "True", "QuotaReserved", "Quota reserved"},
			kueueCondition{"Evicted", "False", "QuotaReserved", "Previously: Evicted"}),
		withCheck("prov", "Pending", "Reset to Pending after eviction", 1))
	events := extractTimelineHistoricalEvents("ctx", "Workload", v1beta2, "ml", wl.GetName(), wl, nil, nil)
	var reasons []string
	for _, e := range events {
		reasons = append(reasons, e.Reason)
		if e.Message != "" {
			t.Fatalf("snapshot row carries a reason/message the transition time doesn't date: %+v", e)
		}
		if e.Source != timeline.SourceHistorical {
			t.Fatalf("source = %s", e.Source)
		}
	}
	want := []string{"created", "QuotaReserved=True", "Evicted=False", "Check prov=Pending"}
	if strings.Join(reasons, ",") != strings.Join(want, ",") {
		t.Fatalf("reasons = %v, want %v", reasons, want)
	}

	recreated := wl.DeepCopy()
	recreated.SetUID("wl-recreated")
	again := extractTimelineHistoricalEvents("ctx", "Workload", v1beta2, "ml", wl.GetName(), recreated, nil, nil)
	for i := 1; i < len(events); i++ {
		if events[i].ID == again[i].ID {
			t.Fatal("recreated resources share a status snapshot ID")
		}
	}

	foreign := kueueWorkload("example.com/v1", withConditions(kueueCondition{"Ready", "True", "Ready", ""}))
	events = extractTimelineHistoricalEvents("ctx", "Workload", "example.com/v1", "ml", foreign.GetName(), foreign, nil, nil)
	if len(events) != 1 || events[0].Reason != "created" {
		t.Fatalf("foreign-group Workload snapshot = %+v", events)
	}
}

func TestRecordToTimelineStore_KueueWorkloadChurnDroppedTransitionKept(t *testing.T) {
	timeline.ResetStore()
	if err := timeline.InitStore(timeline.StoreConfig{Type: timeline.StoreTypeMemory, MaxSize: 10}); err != nil {
		t.Fatalf("InitStore: %v", err)
	}
	t.Cleanup(timeline.ResetStore)

	pending := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", pendingMsg}))
	churn := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "False", "Pending", strings.Replace(pendingMsg, "50m", "70m", 1)}))
	churn.SetResourceVersion("2")
	reserved := kueueWorkload(v1beta2, withConditions(kueueCondition{"QuotaReserved", "True", "QuotaReserved", "Quota reserved in ClusterQueue busy"}),
		withAdmission("busy", 1, map[string]any{"cpu": "default"}, map[string]any{"cpu": "20m"}))
	reserved.SetResourceVersion("3")

	recordToTimelineStore(ActiveClusterContext(), "Workload", "ml", pending.GetName(), "wl-uid", "update", pending, churn, nil, false)
	recordToTimelineStore(ActiveClusterContext(), "Workload", "ml", pending.GetName(), "wl-uid", "update", churn, reserved, nil, false)

	events, err := timeline.GetStore().Query(context.Background(), timeline.QueryOptions{Kinds: []string{"Workload"}, Namespaces: []string{"ml"}, IncludeManaged: true})
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if len(events) != 1 {
		t.Fatalf("want only the transition row, got %d: %+v", len(events), events)
	}
	e := events[0]
	if e.Owner == nil || e.Owner.UID != "job-uid" || e.Owner.Kind != "Job" {
		t.Fatalf("owner = %+v; the root's history reaches the Workload by this owner UID", e.Owner)
	}
	if e.Diff == nil || !strings.Contains(e.Diff.Summary, "quota reserved in ClusterQueue busy") {
		t.Fatalf("diff = %+v", e.Diff)
	}
}

func TestKueueWorkloadDiff_PreservesNestedStatusAndGenericFallback(t *testing.T) {
	base := kueueWorkload(v1beta2, withAdmission("busy", 1, map[string]any{"cpu": "default"}, map[string]any{"cpu": "1"}), withCheck("prov", "Ready", "", 0))
	for _, tt := range []struct {
		name  string
		path  []string
		value any
		want  string
	}{
		{"topology assignment", []string{"status", "admission", "podSetAssignments"}, []any{map[string]any{"name": "main", "count": int64(1), "flavors": map[string]any{"cpu": "default"}, "resourceUsage": map[string]any{"cpu": "1"}, "topologyAssignment": map[string]any{"levels": []any{"zone"}, "domains": []any{map[string]any{"values": []any{"zone-a"}, "count": int64(1)}}}}}, "status.admission.podSetAssignments[main].topologyAssignment"},
		{"check podset updates", []string{"status", "admissionChecks"}, []any{map[string]any{"name": "prov", "state": "Ready", "message": "", "podSetUpdates": []any{map[string]any{"name": "main", "nodeSelector": map[string]any{"zone": "a"}}}}}, "status.admissionChecks[prov].podSetUpdates"},
		{"future admission field", []string{"status", "admission", "extra"}, "value", "status.admission.extra"},
		{"future requeue field", []string{"status", "requeueState", "extra"}, "value", "status.requeueState.extra"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cur := base.DeepCopy()
			if err := unstructured.SetNestedField(cur.Object, tt.value, tt.path...); err != nil {
				t.Fatal(err)
			}
			d := ComputeDiff("Workload", base, cur)
			if d == nil {
				t.Fatal("meaningful nested status change dropped")
			}
			found := false
			for _, f := range d.Fields {
				if strings.HasPrefix(f.Path, tt.want) || strings.HasPrefix(tt.want, f.Path+".") || strings.HasPrefix(tt.want, f.Path+"[") {
					found = true
				}
			}
			if !found {
				t.Fatalf("want %s, fields %v", tt.want, fieldPaths(d))
			}
		})
	}
	effective := base.DeepCopy()
	_ = unstructured.SetNestedSlice(effective.Object, []any{map[string]any{"name": "main", "resources": map[string]any{"cpu": "150m"}}}, "status", "resourceRequests")
	if d := ComputeDiff("Workload", base, effective); d == nil {
		t.Fatal("effective resource request change dropped")
	}

	cur := base.DeepCopy()
	cur.SetLabels(map[string]string{"example.com/label": "changed"})
	if d := ComputeDiff("Workload", base, cur); d == nil || d.Summary != "resource changed" {
		t.Fatalf("generic fallback = %+v", d)
	}
}

func TestKueueDiff_DoesNotMaskArbitraryControllerMessages(t *testing.T) {
	for _, kind := range []string{"Workload", "ProvisioningRequest"} {
		old := kueueWorkload(v1beta2, withConditions(kueueCondition{"Ready", "False", "Failed", "failed after (1) attempts"}), withCheck("custom", "Pending", "(1) rejected", 0))
		cur := kueueWorkload(v1beta2, withConditions(kueueCondition{"Ready", "False", "Failed", "failed after (2) attempts"}), withCheck("custom", "Pending", "(2) rejected", 0))
		if kind == "ProvisioningRequest" {
			old.SetAPIVersion("autoscaling.x-k8s.io/v1")
			cur.SetAPIVersion("autoscaling.x-k8s.io/v1")
			old.SetKind(kind)
			cur.SetKind(kind)
		}
		if d := ComputeDiff(kind, old, cur); d == nil {
			t.Fatalf("%s numeric failure evidence dropped", kind)
		}
	}
}
