package schedulinginsight

import (
	"encoding/json"
	"reflect"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	rc "github.com/skyhook-io/radar/pkg/resourcecontext"
)

// Messages below are copied from Kueue's controllers (v0.18.0 coarse reasons,
// v0.20 granular reasons) so the parser is pinned to what Kueue writes.
const (
	legacyUnusedQuota   = "couldn't assign flavors to pod set main: insufficient unused quota for nvidia.com/gpu in flavor a10, 1 more needed"
	legacyExceedsQuota  = "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (100) > maximum capacity (10)"
	legacyMixedQuota    = "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor big, previously considered podsets requests (0) + current podset request (100) > maximum capacity (10), insufficient unused quota for cpu in flavor small, 4 more needed"
	legacyTaintMismatch = "couldn't assign flavors to pod set main: untolerated taint {spot true NoSchedule <nil>} in flavor spot"
	legacyNoGPUResource = "couldn't assign flavors to pod set main: resource nvidia.com/gpu unavailable in ClusterQueue"
	namespaceMismatch   = "Workload namespace doesn't match ClusterQueue selector"
)

type condition struct {
	kind, status, reason, message string
	observedGeneration            int64
}

type workloadSpec struct {
	version     string
	generation  int64
	active      *bool
	annotations map[string]string
	conditions  []condition
	checks      []map[string]any
}

func buildWorkload(spec workloadSpec) *unstructured.Unstructured {
	version := spec.version
	if version == "" {
		version = "v1beta2"
	}
	u := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kueue.x-k8s.io/" + version,
		"kind":       "Workload",
		"metadata":   map[string]any{"name": "train", "namespace": "ml"},
		"spec":       map[string]any{"queueName": "gpu"},
	}}
	if spec.generation != 0 {
		u.SetGeneration(spec.generation)
	}
	if spec.active != nil {
		_ = unstructured.SetNestedField(u.Object, *spec.active, "spec", "active")
	}
	if spec.annotations != nil {
		u.SetAnnotations(spec.annotations)
	}
	conditions := make([]any, 0, len(spec.conditions))
	for _, c := range spec.conditions {
		entry := map[string]any{"type": c.kind, "status": c.status, "reason": c.reason, "message": c.message, "lastTransitionTime": "2026-11-01T10:00:00Z"}
		if c.observedGeneration != 0 {
			entry["observedGeneration"] = c.observedGeneration
		}
		conditions = append(conditions, entry)
	}
	_ = unstructured.SetNestedSlice(u.Object, conditions, "status", "conditions")
	if spec.checks != nil {
		checks := make([]any, 0, len(spec.checks))
		for _, check := range spec.checks {
			checks = append(checks, check)
		}
		_ = unstructured.SetNestedSlice(u.Object, checks, "status", "admissionChecks")
	}
	return u
}

func pending(reason, message string) condition {
	return condition{kind: "QuotaReserved", status: "False", reason: reason, message: message}
}

var (
	reserved      = condition{kind: "QuotaReserved", status: "True", reason: "QuotaReserved", message: "Quota reserved in ClusterQueue gpu-team"}
	admitted      = condition{kind: "Admitted", status: "True", reason: "Admitted", message: "The workload is admitted"}
	noReservation = condition{kind: "Admitted", status: "False", reason: "NoReservation", message: "The workload has no reservation"}
)

func boolPtr(value bool) *bool { return &value }

func classify(t *testing.T, spec workloadSpec) rc.KueueWorkloadClassification {
	t.Helper()
	got, ok := ClassifyKueueWorkload(buildWorkload(spec))
	if !ok {
		t.Fatal("Workload was not classified")
	}
	return got
}

func factCode(fact *rc.KueueAdmissionFact) rc.KueueAdmissionCode {
	if fact == nil {
		return ""
	}
	return fact.Code
}

func TestClassifyKueueWorkloadStates(t *testing.T) {
	tests := []struct {
		name       string
		spec       workloadSpec
		phase      rc.KueuePhase
		state      rc.KueueAdmissionCode
		inactive   rc.KueueAdmissionCode
		blocker    rc.KueueAdmissionCode
		disruption rc.KueueAdmissionCode
	}{
		{
			name:    "no conditions yet",
			spec:    workloadSpec{},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoDecision,
			blocker: rc.KueueAdmissionNoDecision,
		},
		{
			name:    "stuck on quota (v0.18 coarse reason)",
			spec:    workloadSpec{conditions: []condition{pending("Pending", legacyUnusedQuota), noReservation}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionInsufficientQuota,
			blocker: rc.KueueAdmissionInsufficientQuota,
		},
		{
			name:    "stuck on quota served as v1beta1",
			spec:    workloadSpec{version: "v1beta1", conditions: []condition{pending("Pending", legacyUnusedQuota)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionInsufficientQuota,
			blocker: rc.KueueAdmissionInsufficientQuota,
		},
		{
			name:    "stuck on quota (v0.20 granular reason)",
			spec:    workloadSpec{conditions: []condition{pending("WaitingForQuota", legacyUnusedQuota)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionInsufficientQuota,
			blocker: rc.KueueAdmissionInsufficientQuota,
		},
		{
			name:    "every quota clause exceeds the maximum",
			spec:    workloadSpec{conditions: []condition{pending("Pending", legacyExceedsQuota)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionExceedsQuota,
			blocker: rc.KueueAdmissionExceedsQuota,
		},
		{
			name:    "mixed quota clauses could still fit later",
			spec:    workloadSpec{conditions: []condition{pending("Pending", legacyMixedQuota)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionInsufficientQuota,
			blocker: rc.KueueAdmissionInsufficientQuota,
		},
		{
			name:    "granular exceeds",
			spec:    workloadSpec{conditions: []condition{pending("ExceedsMaxQuota", "exceeds")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionExceedsQuota,
			blocker: rc.KueueAdmissionExceedsQuota,
		},
		{
			name:    "untolerated taint in every flavor",
			spec:    workloadSpec{conditions: []condition{pending("Pending", legacyTaintMismatch)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoMatchingFlavor,
			blocker: rc.KueueAdmissionNoMatchingFlavor,
		},
		{
			name:    "resource the ClusterQueue has no quota for",
			spec:    workloadSpec{conditions: []condition{pending("Pending", legacyNoGPUResource)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoMatchingFlavor,
			blocker: rc.KueueAdmissionNoMatchingFlavor,
		},
		{
			name:    "missing LocalQueue (v0.18)",
			spec:    workloadSpec{conditions: []condition{pending("Inadmissible", "LocalQueue gpu doesn't exist")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionLocalQueueMissing,
			blocker: rc.KueueAdmissionLocalQueueMissing,
		},
		{
			name:    "missing LocalQueue (v0.20)",
			spec:    workloadSpec{conditions: []condition{pending("Misconfigured", "LocalQueue gpu doesn't exist")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionLocalQueueMissing,
			blocker: rc.KueueAdmissionLocalQueueMissing,
		},
		{
			name:    "LocalQueue being deleted",
			spec:    workloadSpec{conditions: []condition{pending("Inadmissible", "LocalQueue gpu is terminating or missing")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionLocalQueueMissing,
			blocker: rc.KueueAdmissionLocalQueueMissing,
		},
		{
			name:    "stopped LocalQueue",
			spec:    workloadSpec{conditions: []condition{pending("Suspended", "LocalQueue gpu is stopped")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionLocalQueueStopped,
			blocker: rc.KueueAdmissionLocalQueueStopped,
		},
		{
			name:    "inactive LocalQueue",
			spec:    workloadSpec{conditions: []condition{pending("Inadmissible", "LocalQueue gpu is inactive")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionLocalQueueInactive,
			blocker: rc.KueueAdmissionLocalQueueInactive,
		},
		{
			name:    "missing ClusterQueue (scheduler wording)",
			spec:    workloadSpec{conditions: []condition{pending("Pending", "ClusterQueue gpu-team not found")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionClusterQueueMissing,
			blocker: rc.KueueAdmissionClusterQueueMissing,
		},
		{
			name:    "inactive ClusterQueue is a blocker, not a hold",
			spec:    workloadSpec{conditions: []condition{pending("Inadmissible", "ClusterQueue gpu-team is inactive")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionClusterQueueInactive,
			blocker: rc.KueueAdmissionClusterQueueInactive,
		},
		{
			name:    "stopped ClusterQueue",
			spec:    workloadSpec{conditions: []condition{pending("Suspended", "ClusterQueue gpu-team is stopped")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionClusterQueueStopped,
			blocker: rc.KueueAdmissionClusterQueueStopped,
		},
		{
			name:    "namespace not eligible (v0.18)",
			spec:    workloadSpec{conditions: []condition{pending("Pending", namespaceMismatch)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNamespaceNotEligible,
			blocker: rc.KueueAdmissionNamespaceNotEligible,
		},
		{
			name:    "namespace not eligible (v0.20)",
			spec:    workloadSpec{conditions: []condition{pending("Misconfigured", "workload namespace doesn't match ClusterQueue selector")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNamespaceNotEligible,
			blocker: rc.KueueAdmissionNamespaceNotEligible,
		},
		{
			name:    "other misconfiguration",
			spec:    workloadSpec{conditions: []condition{pending("Misconfigured", "resources validation failed: spec.podSets[0]: Invalid value")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionInvalidWorkload,
			blocker: rc.KueueAdmissionInvalidWorkload,
		},
		{
			name:    "waiting for other Workloads to become PodsReady",
			spec:    workloadSpec{conditions: []condition{pending("Waiting", "waiting for all admitted workloads to be in PodsReady condition")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionWaitingForPodsReady,
			blocker: rc.KueueAdmissionWaitingForPodsReady,
		},
		{
			name:    "pending evaluation",
			spec:    workloadSpec{conditions: []condition{pending("PendingEvaluation", "Workload is pending evaluation in the scheduling queue")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoDecision,
			blocker: rc.KueueAdmissionNoDecision,
		},
		{
			name:    "admission gate cleared, not yet evaluated",
			spec:    workloadSpec{conditions: []condition{pending("Pending", "AdmissionGatedBy cleared, waiting for quota reservation")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoDecision,
			blocker: rc.KueueAdmissionNoDecision,
		},
		{
			name: "admission gated by an external controller",
			spec: workloadSpec{
				annotations: map[string]string{admissionGatedByAnnotation: "example.com/gate"},
				conditions:  []condition{pending("AdmissionGated", "Admission is gated by: example.com/gate")},
			},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionAdmissionGated,
			blocker: rc.KueueAdmissionAdmissionGated,
		},
		{
			name:    "AdmissionGated without the current annotation is not an admission gate",
			spec:    workloadSpec{conditions: []condition{pending("AdmissionGated", "Admission is gated by: example.com/gate")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionUnrecognized,
			blocker: rc.KueueAdmissionUnrecognized,
		},
		{
			name: "preemption gated in the current attempt",
			spec: workloadSpec{conditions: []condition{
				pending("Pending", legacyUnusedQuota),
				{kind: "BlockedOnPreemptionGates", status: "True", reason: "PreemptionGated", message: legacyUnusedQuota},
			}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionPreemptionGated,
			blocker: rc.KueueAdmissionPreemptionGated,
		},
		{
			name: "gate opened, a later attempt reports a different reason",
			spec: workloadSpec{conditions: []condition{
				pending("Pending", "LocalQueue gpu doesn't exist"),
				{kind: "BlockedOnPreemptionGates", status: "True", reason: "PreemptionGated", message: legacyUnusedQuota},
			}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionLocalQueueMissing,
			blocker: rc.KueueAdmissionLocalQueueMissing,
		},
		{
			name:    "granular preemption-gated reason",
			spec:    workloadSpec{conditions: []condition{pending("PreemptionGated", "blocked")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionPreemptionGated,
			blocker: rc.KueueAdmissionPreemptionGated,
		},
		{
			name:    "waiting for preempted Workloads to release quota",
			spec:    workloadSpec{conditions: []condition{pending("WaitingForPreemptedWorkloads", "waiting")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionWaitingForPreemption,
			blocker: rc.KueueAdmissionWaitingForPreemption,
		},
		{
			name:    "unknown reason keeps native evidence",
			spec:    workloadSpec{conditions: []condition{pending("SomethingNew", "a future Kueue reason")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionUnrecognized,
			blocker: rc.KueueAdmissionUnrecognized,
		},
		{
			name: "quota reserved, waiting on admission checks",
			spec: workloadSpec{
				conditions: []condition{reserved, {kind: "Admitted", status: "False", reason: "UnsatisfiedChecks", message: "The workload has not all checks ready"}},
				checks:     []map[string]any{{"name": "provision", "state": "Pending", "message": "Waiting for capacity"}},
			},
			phase:   rc.KueuePhaseQuotaReserved,
			state:   rc.KueueAdmissionChecksPending,
			blocker: rc.KueueAdmissionChecksPending,
		},
		{
			name: "quota reserved, a check retrying and no Admitted condition yet",
			spec: workloadSpec{
				conditions: []condition{reserved},
				checks:     []map[string]any{{"name": "policy", "state": "Ready"}, {"name": "provision", "state": "Retry", "message": "Retrying"}},
			},
			phase:   rc.KueuePhaseQuotaReserved,
			state:   rc.KueueAdmissionChecksPending,
			blocker: rc.KueueAdmissionChecksPending,
		},
		{
			name:  "quota reserved and nothing reported blocking",
			spec:  workloadSpec{conditions: []condition{reserved}},
			phase: rc.KueuePhaseQuotaReserved,
			state: rc.KueueAdmissionQuotaReserved,
		},
		{
			name:    "delayed topology assignment",
			spec:    workloadSpec{conditions: []condition{reserved, {kind: "Admitted", status: "False", reason: "PendingDelayedTopologyRequests"}}},
			phase:   rc.KueuePhaseQuotaReserved,
			state:   rc.KueueAdmissionTopologyAssignmentPending,
			blocker: rc.KueueAdmissionTopologyAssignmentPending,
		},
		{
			name: "rejected admission check without deactivation",
			spec: workloadSpec{
				conditions: []condition{reserved, {kind: "Admitted", status: "False", reason: "UnsatisfiedChecks"}},
				checks:     []map[string]any{{"name": "policy", "state": "Ready"}, {"name": "capacity", "state": "Rejected", "message": "Provisioning request cannot satisfy this workload"}},
			},
			phase:   rc.KueuePhaseQuotaReserved,
			state:   rc.KueueAdmissionCheckRejected,
			blocker: rc.KueueAdmissionCheckRejected,
		},
		{
			name: "rejected check after Kueue decided to deactivate",
			spec: workloadSpec{
				conditions: []condition{reserved, {kind: "DeactivationTarget", status: "True", reason: "AdmissionCheck", message: "Admission check(s): [capacity], were in the Rejected state"}},
				checks:     []map[string]any{{"name": "capacity", "state": "Rejected"}},
			},
			phase:    rc.KueuePhaseQuotaReserved,
			state:    rc.KueueAdmissionDeactivationPending,
			inactive: rc.KueueAdmissionDeactivationPending,
		},
		{
			name:     "deactivated, not yet acknowledged",
			spec:     workloadSpec{active: boolPtr(false), conditions: []condition{pending("Pending", legacyUnusedQuota)}},
			phase:    rc.KueuePhasePending,
			state:    rc.KueueAdmissionDeactivated,
			inactive: rc.KueueAdmissionDeactivated,
		},
		{
			name: "deactivated and evicted by Kueue",
			spec: workloadSpec{active: boolPtr(false), conditions: []condition{
				pending("Pending", "The workload is deactivated due to Admission check(s): [capacity], were in the Rejected state"),
				{kind: "Evicted", status: "True", reason: "DeactivatedDueToAdmissionCheck", message: "The workload is deactivated due to Admission check(s): [capacity], were in the Rejected state"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionDeactivated,
			inactive:   rc.KueueAdmissionDeactivated,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name: "reactivated, old deactivation eviction still reported, now waiting on quota",
			spec: workloadSpec{active: boolPtr(true), conditions: []condition{
				pending("Pending", legacyUnusedQuota),
				{kind: "Evicted", status: "True", reason: "DeactivatedDueToAdmissionCheck", message: "The workload is deactivated"},
				{kind: "Requeued", status: "True", reason: "Reactivated", message: "The workload was reactivated"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionInsufficientQuota,
			blocker:    rc.KueueAdmissionInsufficientQuota,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name:     "on hold",
			spec:     workloadSpec{conditions: []condition{pending("OnHold", "scaled to zero")}},
			phase:    rc.KueuePhasePending,
			state:    rc.KueueAdmissionOnHold,
			inactive: rc.KueueAdmissionOnHold,
		},
		{
			name: "PodsReadyTimeout eviction waiting out backoff",
			spec: workloadSpec{conditions: []condition{
				pending("Pending", "Exceeded the PodsReady timeout ml/train"),
				{kind: "Evicted", status: "True", reason: "PodsReadyTimeout", message: "Exceeded the PodsReady timeout ml/train"},
				{kind: "Requeued", status: "False", reason: "PodsReadyTimeout", message: "Exceeded the PodsReady timeout ml/train"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionRequeueBackoff,
			blocker:    rc.KueueAdmissionRequeueBackoff,
			disruption: rc.KueueAdmissionPodsReadyTimeout,
		},
		{
			name: "PodsReadyTimeout eviction still releasing quota",
			spec: workloadSpec{conditions: []condition{
				reserved, admitted,
				{kind: "Evicted", status: "True", reason: "PodsReadyTimeout", message: "Exceeded the PodsReady timeout ml/train"},
			}},
			phase:      rc.KueuePhaseAdmitted,
			state:      rc.KueueAdmissionPodsReadyTimeout,
			disruption: rc.KueueAdmissionPodsReadyTimeout,
		},
		{
			name: "after eviction, checks reset to Pending do not decide while pending",
			spec: workloadSpec{
				conditions: []condition{
					pending("Pending", legacyUnusedQuota),
					{kind: "Evicted", status: "True", reason: "Preempted", message: "Preempted to accommodate a workload"},
					{kind: "Preempted", status: "True", reason: "InClusterQueue", message: "Preempted to accommodate a workload"},
					{kind: "Requeued", status: "True", reason: "Preempted"},
				},
				checks: []map[string]any{{"name": "provision", "state": "Pending", "message": "Reset to Pending after eviction. Previously: Ready"}},
			},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionInsufficientQuota,
			blocker:    rc.KueueAdmissionInsufficientQuota,
			disruption: rc.KueueAdmissionPreempted,
		},
		{
			name: "old failure after recovery",
			spec: workloadSpec{
				conditions: []condition{
					reserved, admitted,
					{kind: "Evicted", status: "False", reason: "QuotaReserved", message: "Previously: Exceeded the PodsReady timeout"},
					{kind: "Preempted", status: "False", reason: "QuotaReserved", message: "Previously: Preempted"},
					{kind: "BlockedOnPreemptionGates", status: "False", reason: "QuotaReserved"},
					{kind: "Requeued", status: "True", reason: "BackoffFinished"},
				},
				checks: []map[string]any{{"name": "provision", "state": "Ready", "message": "Previously retried"}},
			},
			phase: rc.KueuePhaseAdmitted,
			state: rc.KueueAdmissionAdmitted,
		},
		{
			name:  "finished successfully",
			spec:  workloadSpec{conditions: []condition{reserved, admitted, {kind: "Finished", status: "True", reason: "Succeeded", message: "Job finished successfully"}}},
			phase: rc.KueuePhaseFinished,
			state: rc.KueueAdmissionFinished,
		},
		{
			name: "finished while inactive keeps finished",
			spec: workloadSpec{active: boolPtr(false), conditions: []condition{
				{kind: "Finished", status: "True", reason: "Failed", message: "Job failed"},
				{kind: "Evicted", status: "True", reason: "Deactivated", message: "The workload is deactivated"},
			}},
			phase:      rc.KueuePhaseFinished,
			state:      rc.KueueAdmissionFinished,
			disruption: rc.KueueAdmissionEvicted,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := classify(t, test.spec)
			admission := got.Admission
			if got.Phase != test.phase || admission.State != test.state || factCode(admission.Inactive) != test.inactive ||
				factCode(admission.Blocker) != test.blocker || factCode(admission.Disruption) != test.disruption {
				wire, _ := json.Marshal(admission)
				t.Fatalf("phase=%s admission=%s; want phase=%s state=%s inactive=%q blocker=%q disruption=%q",
					got.Phase, wire, test.phase, test.state, test.inactive, test.blocker, test.disruption)
			}
		})
	}
}

func TestClassifyKueueWorkloadKeepsNativeEvidence(t *testing.T) {
	got := classify(t, workloadSpec{conditions: []condition{pending("SomethingNew", "a future Kueue reason")}}).Admission
	want := &rc.KueueAdmissionFact{
		Code: rc.KueueAdmissionUnrecognized, Source: rc.KueueFactSourceCondition, Name: "QuotaReserved",
		Reason: "SomethingNew", Message: "a future Kueue reason",
	}
	if !reflect.DeepEqual(got.Blocker, want) {
		t.Fatalf("blocker = %+v, want %+v", got.Blocker, want)
	}

	rejected := classify(t, workloadSpec{
		conditions: []condition{reserved},
		checks:     []map[string]any{{"name": "capacity", "state": "Rejected", "message": "Provisioning request cannot satisfy this workload"}},
	}).Admission
	wantCheck := &rc.KueueAdmissionFact{
		Code: rc.KueueAdmissionCheckRejected, Source: rc.KueueFactSourceAdmissionCheck, Name: "capacity",
		Reason: "Rejected", Message: "Provisioning request cannot satisfy this workload",
	}
	if !reflect.DeepEqual(rejected.Blocker, wantCheck) || rejected.Inactive != nil {
		t.Fatalf("rejected check = %+v inactive=%+v, want %+v and no inactive fact", rejected.Blocker, rejected.Inactive, wantCheck)
	}

	deactivated := classify(t, workloadSpec{active: boolPtr(false)}).Admission
	if !reflect.DeepEqual(deactivated.Inactive, &rc.KueueAdmissionFact{Code: rc.KueueAdmissionDeactivated, Source: rc.KueueFactSourceSpec, Name: "active"}) || deactivated.Disruption != nil {
		t.Fatalf("unacknowledged deactivation = %+v", deactivated)
	}

	pendingTarget := classify(t, workloadSpec{conditions: []condition{reserved, {kind: "DeactivationTarget", status: "True", reason: "RequeuingLimitExceeded", message: "exceeding the maximum number of re-queuing retries"}}}).Admission
	if pendingTarget.Inactive == nil || pendingTarget.Inactive.Reason != "RequeuingLimitExceeded" {
		t.Fatalf("deactivation cause lost: %+v", pendingTarget.Inactive)
	}

	noDecision := classify(t, workloadSpec{}).Admission
	if !reflect.DeepEqual(noDecision.Blocker, &rc.KueueAdmissionFact{Code: rc.KueueAdmissionNoDecision}) {
		t.Fatalf("no_decision without native evidence = %+v", noDecision.Blocker)
	}
}

func TestClassifyKueueWorkloadStaleEvidence(t *testing.T) {
	t.Run("stale negative condition is not a confident blocker", func(t *testing.T) {
		quota := pending("Pending", legacyUnusedQuota)
		quota.observedGeneration = 1
		got := classify(t, workloadSpec{generation: 2, conditions: []condition{quota}}).Admission
		if got.State != rc.KueueAdmissionStale || got.Blocker == nil || got.Blocker.Code != rc.KueueAdmissionInsufficientQuota || !got.Blocker.Stale {
			t.Fatalf("admission = %+v", got)
		}
	})
	t.Run("stale positive condition is not a confident admission", func(t *testing.T) {
		quota, admit := reserved, admitted
		quota.observedGeneration, admit.observedGeneration = 2, 2
		got := classify(t, workloadSpec{generation: 3, conditions: []condition{quota, admit}}).Admission
		if got.State != rc.KueueAdmissionStale {
			t.Fatalf("admission = %+v", got)
		}
	})
	t.Run("stale finish", func(t *testing.T) {
		got := classify(t, workloadSpec{generation: 4, conditions: []condition{{kind: "Finished", status: "True", reason: "Succeeded", observedGeneration: 3}}}).Admission
		if got.State != rc.KueueAdmissionStale {
			t.Fatalf("admission = %+v", got)
		}
	})
	t.Run("current spec decides even when a secondary axis is stale", func(t *testing.T) {
		got := classify(t, workloadSpec{generation: 3, active: boolPtr(false), conditions: []condition{
			{kind: "Evicted", status: "True", reason: "Preempted", observedGeneration: 2},
		}}).Admission
		if got.State != rc.KueueAdmissionDeactivated || got.Disruption == nil || !got.Disruption.Stale {
			t.Fatalf("admission = %+v", got)
		}
	})
	t.Run("current generation and unreported generation are not stale", func(t *testing.T) {
		quota := pending("Pending", legacyUnusedQuota)
		quota.observedGeneration = 2
		if got := classify(t, workloadSpec{generation: 2, conditions: []condition{quota}}).Admission; got.State != rc.KueueAdmissionInsufficientQuota || got.Blocker.Stale {
			t.Fatalf("current generation = %+v", got)
		}
		if got := classify(t, workloadSpec{generation: 2, conditions: []condition{pending("Pending", legacyUnusedQuota)}}).Admission; got.State != rc.KueueAdmissionInsufficientQuota {
			t.Fatalf("unreported generation = %+v", got)
		}
	})
}

func TestClassifyKueueWorkloadPreservesExistingDecision(t *testing.T) {
	for _, test := range []struct {
		name     string
		spec     workloadSpec
		decision rc.SchedulingDecision
	}{
		{name: "inactive ClusterQueue stays unsatisfied", spec: workloadSpec{conditions: []condition{pending("Inadmissible", "ClusterQueue gpu-team is inactive")}}, decision: rc.SchedulingDecisionUnsatisfied},
		{name: "rejected check stays unsatisfied", spec: workloadSpec{conditions: []condition{reserved}, checks: []map[string]any{{"name": "capacity", "state": "Rejected"}}}, decision: rc.SchedulingDecisionUnsatisfied},
		{name: "deactivated stays held", spec: workloadSpec{active: boolPtr(false)}, decision: rc.SchedulingDecisionHeld},
	} {
		t.Run(test.name, func(t *testing.T) {
			u := buildWorkload(test.spec)
			observation := onlyObservation(t, ForResource(u, rc.TierBasic))
			classification, _ := ClassifyKueueWorkload(u)
			if observation.Decision != test.decision {
				t.Fatalf("decision = %s, want %s", observation.Decision, test.decision)
			}
			if observation.Kueue.Admission == nil || !reflect.DeepEqual(*observation.Kueue.Admission, classification.Admission) ||
				observation.Kueue.Phase != classification.Phase || observation.Kueue.Outcome != classification.Outcome {
				t.Fatalf("ForResource and ClassifyKueueWorkload disagree: %+v vs %+v", observation.Kueue, classification)
			}
		})
	}
}

func TestClassifyKueueWorkloadRejectsOtherObjects(t *testing.T) {
	foreign := buildWorkload(workloadSpec{})
	foreign.SetAPIVersion("example.io/v1beta2")
	if _, ok := ClassifyKueueWorkload(foreign); ok {
		t.Fatal("Workload kind in another group was classified")
	}
	if _, ok := ClassifyKueueWorkload(&corev1.Pod{}); ok {
		t.Fatal("typed object was classified")
	}
}
