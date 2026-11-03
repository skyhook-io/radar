package schedulinginsight

import (
	"encoding/json"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
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
	// One flavor: memory is only short now, but cpu is over the maximum, so
	// waiting cannot make this flavor fit.
	legacyCoRequisite = "couldn't assign flavors to pod set main: insufficient unused quota for memory in flavor default, 1Gi more needed, insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (4) > maximum capacity (2)"
	// Two PodSets: the second fits nowhere, so the Workload never fits.
	legacyTwoPodSets = "couldn't assign flavors to pod set driver: insufficient unused quota for cpu in flavor default, 1 more needed; couldn't assign flavors to pod set workers: insufficient quota for nvidia.com/gpu in flavor a10, previously considered podsets requests (0) + current podset request (8) > maximum capacity (4)"
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
	noQueue     bool
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
	if spec.noQueue {
		unstructured.RemoveNestedField(u.Object, "spec", "queueName")
	}
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

func classify(t *testing.T, spec workloadSpec) KueueClassification {
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
			name:    "no queue name and no conditions",
			spec:    workloadSpec{noQueue: true},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoDecision,
			blocker: rc.KueueAdmissionNoDecision,
		},
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
			name:    "a resource over the maximum rules out the flavor even when another resource is only short",
			spec:    workloadSpec{conditions: []condition{pending("Pending", legacyCoRequisite)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionExceedsQuota,
			blocker: rc.KueueAdmissionExceedsQuota,
		},
		{
			name:    "one PodSet that fits nowhere decides",
			spec:    workloadSpec{conditions: []condition{pending("Pending", legacyTwoPodSets)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionExceedsQuota,
			blocker: rc.KueueAdmissionExceedsQuota,
		},
		{
			name:    "granular topology placement failure",
			spec:    workloadSpec{conditions: []condition{pending("TopologyPlacementFailed", "cannot fit in topology")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionTopologyPlacementFailed,
			blocker: rc.KueueAdmissionTopologyPlacementFailed,
		},
		{
			name:    "DRA resources Kueue could not resolve are not blamed on the Workload",
			spec:    workloadSpec{conditions: []condition{pending("DRAResourcesUnresolved", "failed to list ResourceSlices: timeout")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionDRAResourcesUnresolved,
			blocker: rc.KueueAdmissionDRAResourcesUnresolved,
		},
		{
			name:    "usage Kueue cannot record",
			spec:    workloadSpec{conditions: []condition{pending("UnrecordableResourceUsage", "total exceeds the recordable maximum")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionInvalidWorkload,
			blocker: rc.KueueAdmissionInvalidWorkload,
		},
		{
			name:    "pending evaluation blocked by retrying checks (v0.20 wording)",
			spec:    workloadSpec{conditions: []condition{pending("PendingEvaluation", "The workload has failed admission checks")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionChecksPending,
			blocker: rc.KueueAdmissionChecksPending,
		},
		{
			name: "v0.20 admitted reason for unready checks",
			spec: workloadSpec{
				conditions: []condition{reserved, {kind: "Admitted", status: "False", reason: "UnsatisfiedAdmissionChecks"}},
				checks:     []map[string]any{{"name": "provision", "state": "Ready"}},
			},
			phase:   rc.KueuePhaseQuotaReserved,
			state:   rc.KueueAdmissionChecksPending,
			blocker: rc.KueueAdmissionChecksPending,
		},
		{
			name: "v0.19 preemption gating wrote QuotaReserved AdmissionGated with its own message",
			spec: workloadSpec{conditions: []condition{
				pending("AdmissionGated", "Workload requires preemption but preemption is gated"),
				{kind: "BlockedOnPreemptionGates", status: "True", reason: "PreemptionGated", message: "Workload could free quota by preemption but a gate is closed"},
			}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionPreemptionGated,
			blocker: rc.KueueAdmissionPreemptionGated,
		},
		{
			name: "backoff outranks checks reset to Pending",
			spec: workloadSpec{
				conditions: []condition{
					pending("Pending", "The workload has failed admission checks"),
					{kind: "Evicted", status: "True", reason: "AdmissionCheck", message: "At least one admission check is false"},
					{kind: "Requeued", status: "False", reason: "AdmissionCheck", message: "At least one admission check is false"},
				},
				checks: []map[string]any{{"name": "provision", "state": "Pending", "message": "Reset to Pending after eviction. Previously: Retry"}},
			},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionRequeueBackoff,
			blocker:    rc.KueueAdmissionRequeueBackoff,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name: "every taint clause rules out its flavor, not only the last",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				"couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor a100, 1 more needed, untolerated taint {gpu true NoSchedule <nil>} in flavor a100, untolerated taint {spot true NoSchedule <nil>} in flavor spot")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoMatchingFlavor,
			blocker: rc.KueueAdmissionNoMatchingFlavor,
		},
		{
			name: "taint and unused quota in the same flavor rule it out",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				"couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor spot, 1 more needed, untolerated taint {spot true NoSchedule <nil>} in flavor spot")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoMatchingFlavor,
			blocker: rc.KueueAdmissionNoMatchingFlavor,
		},
		{
			name: "an unrecognized PodSet failure is not covered by another PodSet's quota wait",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				"couldn't assign flavors to pod set driver: insufficient unused quota for cpu in flavor default, 1 more needed; couldn't assign flavors to pod set workers: something Kueue adds later")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionUnrecognized,
			blocker: rc.KueueAdmissionUnrecognized,
		},
		{
			name: "a flavor without topology support is ruled out",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				`couldn't assign flavors to pod set driver: insufficient unused quota for cpu in flavor default, 1 more needed; couldn't assign flavors to pod set workers: Flavor "default" does not support TopologyAwareScheduling`)}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoMatchingFlavor,
			blocker: rc.KueueAdmissionNoMatchingFlavor,
		},
		{
			name: "an external admission gate after a lingering preemption gate",
			spec: workloadSpec{
				annotations: map[string]string{admissionGatedByAnnotation: "example.com/gate"},
				conditions: []condition{
					pending("AdmissionGated", "Admission is gated by: example.com/gate"),
					{kind: "BlockedOnPreemptionGates", status: "True", reason: "PreemptionGated", message: legacyUnusedQuota},
				},
			},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionAdmissionGated,
			blocker: rc.KueueAdmissionAdmissionGated,
		},
		{
			name: "an old eviction does not hide a new reason Radar does not recognize",
			spec: workloadSpec{conditions: []condition{
				pending("Pending", "A reason a future Kueue writes"),
				{kind: "Evicted", status: "True", reason: "Preempted", message: "Preempted to accommodate a workload"},
				{kind: "Preempted", status: "True", reason: "InClusterQueue", message: "Preempted to accommodate a workload"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionUnrecognized,
			blocker:    rc.KueueAdmissionUnrecognized,
			disruption: rc.KueueAdmissionPreempted,
		},
		{
			name: "drained by a stopped ClusterQueue, not requeued until it restarts",
			spec: workloadSpec{conditions: []condition{
				pending("Pending", "The ClusterQueue is stopped"),
				{kind: "Evicted", status: "True", reason: "ClusterQueueStopped", message: "The ClusterQueue is stopped"},
				{kind: "Requeued", status: "False", reason: "ClusterQueueStopped", message: "The ClusterQueue is stopped"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionClusterQueueInactive,
			blocker:    rc.KueueAdmissionClusterQueueInactive,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name: "drained by a stopped LocalQueue",
			spec: workloadSpec{conditions: []condition{
				pending("Pending", "The LocalQueue is stopped"),
				{kind: "Evicted", status: "True", reason: "LocalQueueStopped", message: "The LocalQueue is stopped"},
				{kind: "Requeued", status: "False", reason: "LocalQueueStopped", message: "The LocalQueue is stopped"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionLocalQueueInactive,
			blocker:    rc.KueueAdmissionLocalQueueInactive,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name:    "preemptions in flight (v0.18 wording)",
			spec:    workloadSpec{conditions: []condition{pending("Pending", legacyUnusedQuota+". Pending the preemption of 2 workload(s)")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionWaitingForPreemption,
			blocker: rc.KueueAdmissionWaitingForPreemption,
		},
		{
			name:    "a cluster-side misconfiguration is not blamed on the Workload",
			spec:    workloadSpec{conditions: []condition{pending("Misconfigured", "invalid namespace selector: a bad operator")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionUnrecognized,
			blocker: rc.KueueAdmissionUnrecognized,
		},
		{
			name: "an elastic slice pinned to its original flavor cannot use the alternate",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				"couldn't assign flavors to pod set main: could not assign spot flavor since the original workload is assigned: default, insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (8) > maximum capacity (4), insufficient unused quota for cpu in flavor spot, 2 more needed")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionExceedsQuota,
			blocker: rc.KueueAdmissionExceedsQuota,
		},
		{
			name: "a clause Radar does not understand keeps that flavor from counting as viable",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				"couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor spot, 2 more needed, some future restriction on flavor spot")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionUnrecognized,
			blocker: rc.KueueAdmissionUnrecognized,
		},
		{
			name:    "skipped for another Workload in the same cycle (v0.18 wording)",
			spec:    workloadSpec{conditions: []condition{pending("Pending", "Workload no longer fits after processing another workload")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionInsufficientQuota,
			blocker: rc.KueueAdmissionInsufficientQuota,
		},
		{
			name:    "overlapping preemption targets (v0.18 wording)",
			spec:    workloadSpec{conditions: []condition{pending("Pending", "Workload has overlapping preemption targets with another workload")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionInsufficientQuota,
			blocker: rc.KueueAdmissionInsufficientQuota,
		},
		{
			name:    "fits after in-flight preemptions (v0.20 wording, granular reasons off)",
			spec:    workloadSpec{conditions: []condition{pending("Pending", "Workload has overlapping preemption targets with another workload, but will fit after these preemptions complete")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionWaitingForPreemption,
			blocker: rc.KueueAdmissionWaitingForPreemption,
		},
		{
			name: "a PodSet that fits nowhere outranks one that exceeds quota",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				"couldn't assign flavors to pod set launcher: untolerated taint {gpu true NoSchedule <nil>} in flavor a100; couldn't assign flavors to pod set workers: insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (8) > maximum capacity (4)")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoMatchingFlavor,
			blocker: rc.KueueAdmissionNoMatchingFlavor,
		},
		{
			name:    "preemption failed and will be retried (v0.18 wording)",
			spec:    workloadSpec{conditions: []condition{pending("Pending", legacyUnusedQuota+". Preempting 1 workload(s) failed, will retry.")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionWaitingForPreemption,
			blocker: rc.KueueAdmissionWaitingForPreemption,
		},
		{
			name: "evicted while quota is still reserved: the eviction decides",
			spec: workloadSpec{
				conditions: []condition{reserved, {kind: "Evicted", status: "True", reason: "AdmissionCheck", message: "At least one admission check is false"}},
				checks:     []map[string]any{{"name": "provision", "state": "Retry", "message": "Retrying"}},
			},
			phase:      rc.KueuePhaseQuotaReserved,
			state:      rc.KueueAdmissionEvicted,
			blocker:    rc.KueueAdmissionChecksPending,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name: "a resource the ClusterQueue doesn't offer outranks another resource's quota wait",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				"couldn't assign flavors to pod set main: insufficient unused quota for cpu in flavor default, 1 more needed, resource nvidia.com/gpu unavailable in ClusterQueue")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionNoMatchingFlavor,
			blocker: rc.KueueAdmissionNoMatchingFlavor,
		},
		{
			name: "a taint on one flavor and an unknown failure on another is not a confident no-match",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				"couldn't assign flavors to pod set main: some future restriction on flavor b, untolerated taint {spot true NoSchedule <nil>} in flavor a")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionUnrecognized,
			blocker: rc.KueueAdmissionUnrecognized,
		},
		{
			name: "a maximum on one flavor and an unknown failure on another is not a confident exceed",
			spec: workloadSpec{conditions: []condition{pending("Pending",
				"couldn't assign flavors to pod set main: insufficient quota for cpu in flavor a, previously considered podsets requests (0) + current podset request (8) > maximum capacity (4), some future restriction on flavor b")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionUnrecognized,
			blocker: rc.KueueAdmissionUnrecognized,
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
			name:    "stopped LocalQueue reads the same as inactive",
			spec:    workloadSpec{conditions: []condition{pending("Suspended", "LocalQueue gpu is stopped")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionLocalQueueInactive,
			blocker: rc.KueueAdmissionLocalQueueInactive,
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
			name:    "stopped ClusterQueue in the Workload controller's wording",
			spec:    workloadSpec{conditions: []condition{pending("Suspended", "ClusterQueue gpu-team is stopped")}},
			phase:   rc.KueuePhasePending,
			state:   rc.KueueAdmissionClusterQueueInactive,
			blocker: rc.KueueAdmissionClusterQueueInactive,
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
			name:    "the Workload's own requests fail validation",
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
			name: "deactivated by Kueue after a rejected check",
			spec: workloadSpec{active: boolPtr(false), conditions: []condition{
				pending("Pending", "The workload is deactivated due to Admission check(s): [capacity], were in the Rejected state"),
				{kind: "Evicted", status: "True", reason: "DeactivatedDueToAdmissionCheck", message: "The workload is deactivated due to Admission check(s): [capacity], were in the Rejected state"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionDeactivatedByKueue,
			inactive:   rc.KueueAdmissionDeactivatedByKueue,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name: "deactivated by Kueue at the requeuing limit",
			spec: workloadSpec{active: boolPtr(false), conditions: []condition{
				{kind: "Evicted", status: "True", reason: "DeactivatedDueToRequeuingLimitExceeded", message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionDeactivatedByKueue,
			inactive:   rc.KueueAdmissionDeactivatedByKueue,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name: "Kueue v0.18 deactivated an already-evicted Workload: the cause is not recorded",
			spec: workloadSpec{active: boolPtr(false), conditions: []condition{
				pending("Pending", "Exceeded the PodsReady timeout ml/train"),
				{kind: "Evicted", status: "True", reason: "PodsReadyTimeout", message: "Exceeded the PodsReady timeout ml/train"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionDeactivated,
			inactive:   rc.KueueAdmissionDeactivated,
			disruption: rc.KueueAdmissionPodsReadyTimeout,
		},
		{
			name: "preempted, then deactivated by Kueue: the cause stays on the inactive fact",
			spec: workloadSpec{active: boolPtr(false), conditions: []condition{
				{kind: "Preempted", status: "True", reason: "InClusterQueue", message: "Preempted to accommodate a workload"},
				{kind: "Evicted", status: "True", reason: "DeactivatedDueToAdmissionCheck", message: "The workload is deactivated due to a rejected check"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionDeactivatedByKueue,
			inactive:   rc.KueueAdmissionDeactivatedByKueue,
			disruption: rc.KueueAdmissionPreempted,
		},
		{
			name: "deactivated by a person and acknowledged",
			spec: workloadSpec{active: boolPtr(false), conditions: []condition{
				{kind: "Evicted", status: "True", reason: "Deactivated", message: "The workload is deactivated"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionDeactivated,
			inactive:   rc.KueueAdmissionDeactivated,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name: "spec.active false while Kueue's own target is still set",
			spec: workloadSpec{active: boolPtr(false), conditions: []condition{
				{kind: "DeactivationTarget", status: "True", reason: "MaximumExecutionTimeExceeded", message: "exceeding the maximum execution time"},
			}},
			phase:    rc.KueuePhasePending,
			state:    rc.KueueAdmissionDeactivatedByKueue,
			inactive: rc.KueueAdmissionDeactivatedByKueue,
		},
		{
			name: "right after an eviction QuotaReserved carries the eviction message",
			spec: workloadSpec{conditions: []condition{
				pending("Pending", "Preempted to accommodate a workload"),
				{kind: "Evicted", status: "True", reason: "Preempted", message: "Preempted to accommodate a workload"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionPreempted,
			blocker:    rc.KueueAdmissionUnrecognized,
			disruption: rc.KueueAdmissionPreempted,
		},
		{
			name: "pending evaluation right after an eviction",
			spec: workloadSpec{conditions: []condition{
				pending("PendingEvaluation", "Workload is pending evaluation in the scheduling queue"),
				{kind: "Evicted", status: "True", reason: "ClusterQueueStopped", message: "The ClusterQueue is stopped"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionEvicted,
			blocker:    rc.KueueAdmissionNoDecision,
			disruption: rc.KueueAdmissionEvicted,
		},
		{
			name: "reactivated with the old QuotaReserved unchanged",
			spec: workloadSpec{active: boolPtr(true), conditions: []condition{
				pending("Pending", "The workload is deactivated"),
				{kind: "Evicted", status: "True", reason: "Deactivated", message: "The workload is deactivated"},
				{kind: "Requeued", status: "True", reason: "Reactivated", message: "The workload was reactivated"},
			}},
			phase:      rc.KueuePhasePending,
			state:      rc.KueueAdmissionEvicted,
			blocker:    rc.KueueAdmissionUnrecognized,
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
	t.Run("stale negative condition is not a confirmed blocker", func(t *testing.T) {
		quota := pending("Pending", legacyUnusedQuota)
		quota.observedGeneration = 1
		got := classify(t, workloadSpec{generation: 2, conditions: []condition{quota}}).Admission
		if got.State != rc.KueueAdmissionInsufficientQuota || !got.Stale || got.Blocker == nil || !got.Blocker.Stale {
			t.Fatalf("admission = %+v", got)
		}
	})
	t.Run("stale positive condition is not a confirmed admission", func(t *testing.T) {
		// Kueue rewrites spec.priority when a WorkloadPriorityClass changes and
		// leaves Admitted untouched; the code survives, flagged as last reported.
		quota, admit := reserved, admitted
		quota.observedGeneration, admit.observedGeneration = 2, 2
		got := classify(t, workloadSpec{generation: 3, conditions: []condition{quota, admit}}).Admission
		if got.State != rc.KueueAdmissionAdmitted || !got.Stale || got.Attention != rc.KueueAttentionNone {
			t.Fatalf("admission = %+v", got)
		}
	})
	t.Run("a stale reservation makes the blocker it selected stale", func(t *testing.T) {
		quota := reserved
		quota.observedGeneration = 1
		got := classify(t, workloadSpec{generation: 2, conditions: []condition{quota}, checks: []map[string]any{{"name": "provision", "state": "Pending"}}}).Admission
		if got.State != rc.KueueAdmissionChecksPending || !got.Stale {
			t.Fatalf("admission = %+v", got)
		}
	})
	t.Run("stale finish", func(t *testing.T) {
		got := classify(t, workloadSpec{generation: 4, conditions: []condition{{kind: "Finished", status: "True", reason: "Succeeded", observedGeneration: 3}}}).Admission
		if got.State != rc.KueueAdmissionFinished || !got.Stale {
			t.Fatalf("admission = %+v", got)
		}
	})
	t.Run("current spec decides even when a secondary axis is stale", func(t *testing.T) {
		got := classify(t, workloadSpec{generation: 3, active: boolPtr(false), conditions: []condition{
			{kind: "Evicted", status: "True", reason: "Deactivated", observedGeneration: 2},
		}}).Admission
		if got.State != rc.KueueAdmissionDeactivated || got.Stale || got.Disruption == nil || !got.Disruption.Stale {
			t.Fatalf("admission = %+v", got)
		}
		uncertain := classify(t, workloadSpec{generation: 3, active: boolPtr(false), conditions: []condition{
			{kind: "Evicted", status: "True", reason: "Preempted", observedGeneration: 2},
		}}).Admission
		if uncertain.State != rc.KueueAdmissionDeactivated || uncertain.Stale || uncertain.Attention != rc.KueueAttentionUnknown {
			t.Fatalf("admission = %+v", uncertain)
		}
	})
	t.Run("Kueue's own spec.active flip does not make its recorded cause stale", func(t *testing.T) {
		got := classify(t, workloadSpec{generation: 2, active: boolPtr(false), conditions: []condition{
			{kind: "DeactivationTarget", status: "True", reason: "AdmissionCheck", observedGeneration: 1},
		}}).Admission
		if got.State != rc.KueueAdmissionDeactivatedByKueue || got.Stale || got.Inactive == nil || !got.Inactive.Stale {
			t.Fatalf("admission = %+v", got)
		}
	})
	t.Run("current generation and unreported generation are not stale", func(t *testing.T) {
		quota := pending("Pending", legacyUnusedQuota)
		quota.observedGeneration = 2
		if got := classify(t, workloadSpec{generation: 2, conditions: []condition{quota}}).Admission; got.Stale || got.Blocker.Stale {
			t.Fatalf("current generation = %+v", got)
		}
		if got := classify(t, workloadSpec{generation: 2, conditions: []condition{pending("Pending", legacyUnusedQuota)}}).Admission; got.Stale {
			t.Fatalf("unreported generation = %+v", got)
		}
	})
}

func TestClassifyKueueWorkloadAttention(t *testing.T) {
	for _, test := range []struct {
		spec      workloadSpec
		attention rc.KueueAttention
	}{
		{workloadSpec{conditions: []condition{reserved, admitted}}, rc.KueueAttentionNone},
		{workloadSpec{conditions: []condition{pending("Pending", legacyUnusedQuota)}}, rc.KueueAttentionWaiting},
		{workloadSpec{active: boolPtr(false)}, rc.KueueAttentionPaused},
		{workloadSpec{conditions: []condition{pending("Inadmissible", "LocalQueue gpu doesn't exist")}}, rc.KueueAttentionActionNeeded},
		{workloadSpec{conditions: []condition{pending("Inadmissible", "ClusterQueue gpu-team is inactive")}}, rc.KueueAttentionUnknown},
		{workloadSpec{conditions: []condition{pending("SomethingNew", "x")}}, rc.KueueAttentionUnknown},
	} {
		if got := classify(t, test.spec).Admission; got.Attention != test.attention {
			t.Errorf("state %s attention = %s, want %s", got.State, got.Attention, test.attention)
		}
	}
}

func TestEveryAdmissionCodeHasAnAttentionClass(t *testing.T) {
	// unrecognized and the queue-inactive codes are deliberately unknown; any
	// other code falling through to the default was forgotten.
	deliberatelyUnknown := map[rc.KueueAdmissionCode]bool{
		rc.KueueAdmissionUnrecognized: true, rc.KueueAdmissionLocalQueueInactive: true, rc.KueueAdmissionClusterQueueInactive: true,
		rc.KueueAdmissionDRAResourcesUnresolved: true,
	}
	for _, code := range allAdmissionCodes {
		if got := attention(code); got == rc.KueueAttentionUnknown && !deliberatelyUnknown[code] {
			t.Errorf("code %s has no attention class", code)
		}
	}
}

var allAdmissionCodes = []rc.KueueAdmissionCode{
	rc.KueueAdmissionFinished, rc.KueueAdmissionAdmitted, rc.KueueAdmissionQuotaReserved,
	rc.KueueAdmissionDeactivated, rc.KueueAdmissionDeactivatedByKueue, rc.KueueAdmissionDeactivationPending, rc.KueueAdmissionOnHold,
	rc.KueueAdmissionNoDecision, rc.KueueAdmissionInsufficientQuota, rc.KueueAdmissionExceedsQuota, rc.KueueAdmissionNoMatchingFlavor,
	rc.KueueAdmissionTopologyPlacementFailed, rc.KueueAdmissionWaitingForPreemption, rc.KueueAdmissionPreemptionGated,
	rc.KueueAdmissionAdmissionGated, rc.KueueAdmissionNamespaceNotEligible, rc.KueueAdmissionLocalQueueMissing,
	rc.KueueAdmissionLocalQueueInactive, rc.KueueAdmissionClusterQueueMissing, rc.KueueAdmissionClusterQueueInactive,
	rc.KueueAdmissionCheckRejected, rc.KueueAdmissionChecksPending, rc.KueueAdmissionTopologyAssignmentPending,
	rc.KueueAdmissionWaitingForPodsReady, rc.KueueAdmissionRequeueBackoff, rc.KueueAdmissionInvalidWorkload, rc.KueueAdmissionDRAResourcesUnresolved,
	rc.KueueAdmissionUnrecognized, rc.KueueAdmissionPreempted, rc.KueueAdmissionPodsReadyTimeout, rc.KueueAdmissionEvicted,
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

func TestPrimaryConditionFollowsTheCurrentAttempt(t *testing.T) {
	externallyGated := buildWorkload(workloadSpec{
		annotations: map[string]string{admissionGatedByAnnotation: "example.com/gate"},
		conditions: []condition{
			pending("AdmissionGated", "Admission is gated by: example.com/gate"),
			{kind: "BlockedOnPreemptionGates", status: "True", reason: "PreemptionGated", message: legacyUnusedQuota},
		},
	})
	observation := onlyObservation(t, ForResource(externallyGated, rc.TierBasic))
	if observation.PrimaryCondition == nil || observation.PrimaryCondition.Type != "QuotaReserved" || observation.Decision != rc.SchedulingDecisionHeld {
		t.Fatalf("external gate hidden by a lingering preemption gate: decision=%s primary=%+v", observation.Decision, observation.PrimaryCondition)
	}

	lingering := buildWorkload(workloadSpec{conditions: []condition{
		pending("Inadmissible", "LocalQueue gpu doesn't exist"),
		{kind: "BlockedOnPreemptionGates", status: "True", reason: "PreemptionGated", message: legacyUnusedQuota},
	}})
	if primary := onlyObservation(t, ForResource(lingering, rc.TierBasic)).PrimaryCondition; primary == nil || primary.Type != "QuotaReserved" {
		t.Fatalf("lingering preemption gate became the primary condition: %+v", primary)
	}
	withChecks := buildWorkload(workloadSpec{conditions: []condition{
		pending("Pending", legacyUnusedQuota),
		{kind: "Admitted", status: "False", reason: "NoReservationUnsatisfiedChecks", message: "The workload has no reservation and not all checks ready"},
	}})
	if primary := onlyObservation(t, ForResource(withChecks, rc.TierBasic)).PrimaryCondition; primary == nil || primary.Type != "QuotaReserved" {
		t.Fatalf("generic NoReservationUnsatisfiedChecks hid the quota reason: %+v", primary)
	}
}

// The shared vector lists every code once; the k8s-ui presentation test reads
// the same file, so a code added here without a UI label fails there.
func TestSharedCodeVectorMatchesDeclaredCodes(t *testing.T) {
	raw, err := os.ReadFile("testdata/kueue_admission_codes.json")
	if err != nil {
		t.Fatal(err)
	}
	var vector struct {
		Codes     []string `json:"codes"`
		Attention []string `json:"attention"`
	}
	if err := json.Unmarshal(raw, &vector); err != nil {
		t.Fatal(err)
	}
	declared := map[string][]string{}
	file, err := parser.ParseFile(token.NewFileSet(), "../resourcecontext/kueue_admission.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.CONST {
			continue
		}
		for _, spec := range gen.Specs {
			value := spec.(*ast.ValueSpec)
			ident, ok := value.Type.(*ast.Ident)
			if !ok || len(value.Values) != 1 {
				continue
			}
			literal, ok := value.Values[0].(*ast.BasicLit)
			if !ok {
				continue
			}
			unquoted, _ := strconv.Unquote(literal.Value)
			declared[ident.Name] = append(declared[ident.Name], unquoted)
		}
	}
	sameSet := func(name string, got, want []string) {
		sort.Strings(got)
		sort.Strings(want)
		if !reflect.DeepEqual(got, want) {
			t.Errorf("%s: shared vector %v, declared %v", name, want, got)
		}
	}
	sameSet("KueueAdmissionCode", declared["KueueAdmissionCode"], vector.Codes)
	sameSet("KueueAttention", declared["KueueAttention"], vector.Attention)
	listed := make([]string, 0, len(allAdmissionCodes))
	for _, code := range allAdmissionCodes {
		listed = append(listed, string(code))
	}
	sameSet("allAdmissionCodes", listed, vector.Codes)
}

func TestPendingChecksCiteTheCheck(t *testing.T) {
	got := classify(t, workloadSpec{
		conditions: []condition{reserved, {kind: "Admitted", status: "False", reason: "UnsatisfiedChecks", message: "The workload has not all checks ready"}},
		checks:     []map[string]any{{"name": "policy", "state": "Ready"}, {"name": "provision", "state": "Pending", "message": "Waiting for capacity"}},
	}).Admission
	if got.Blocker == nil || got.Blocker.Source != rc.KueueFactSourceAdmissionCheck || got.Blocker.Name != "provision" || got.Blocker.Message != "Waiting for capacity" {
		t.Fatalf("blocker = %+v", got.Blocker)
	}
}

// decision is the controller-neutral scheduling view and keeps its own
// meaning; these pairs are where it and the classification deliberately
// differ, so consumers know to read state and attention instead.
func TestDecisionAndStateDisagreeOnlyWhereDocumented(t *testing.T) {
	for _, test := range []struct {
		name      string
		spec      workloadSpec
		decision  rc.SchedulingDecision
		state     rc.KueueAdmissionCode
		attention rc.KueueAttention
	}{
		{"inactive queue", workloadSpec{conditions: []condition{pending("Inadmissible", "ClusterQueue gpu-team is inactive")}}, rc.SchedulingDecisionUnsatisfied, rc.KueueAdmissionClusterQueueInactive, rc.KueueAttentionUnknown},
		{"external admission gate", workloadSpec{annotations: map[string]string{admissionGatedByAnnotation: "example.com/gate"}, conditions: []condition{pending("AdmissionGated", "Admission is gated by: example.com/gate")}}, rc.SchedulingDecisionHeld, rc.KueueAdmissionAdmissionGated, rc.KueueAttentionWaiting},
		{"Kueue is deactivating", workloadSpec{conditions: []condition{reserved, {kind: "DeactivationTarget", status: "True", reason: "AdmissionCheck"}}}, rc.SchedulingDecisionUnknown, rc.KueueAdmissionDeactivationPending, rc.KueueAttentionActionNeeded},
	} {
		t.Run(test.name, func(t *testing.T) {
			u := buildWorkload(test.spec)
			observation := onlyObservation(t, ForResource(u, rc.TierBasic))
			admission := observation.Kueue.Admission
			if observation.Decision != test.decision || admission.State != test.state || admission.Attention != test.attention {
				t.Fatalf("decision=%s state=%s attention=%s", observation.Decision, admission.State, admission.Attention)
			}
		})
	}
}

func TestTruncatedFlavorMessageProvesOnlyItsCompleteSegments(t *testing.T) {
	var clauses []string
	for i := 0; len(strings.Join(clauses, ", ")) < 33*1024; i++ {
		clauses = append(clauses, fmt.Sprintf("insufficient unused quota for example.com/r%03d in flavor default, 1 more needed", i))
	}
	full := "couldn't assign flavors to pod set main: " + strings.Join(clauses, ", ") +
		", insufficient quota for cpu in flavor default, previously considered podsets requests (0) + current podset request (8) > maximum capacity (4)"
	// Kueue's TruncateConditionMessage: cut to 32 KiB, ending in " ...".
	truncated := full[:32*1024-len(" ...")] + " ..."
	got := classify(t, workloadSpec{conditions: []condition{pending("Pending", truncated)}}).Admission
	if got.State != rc.KueueAdmissionUnrecognized {
		t.Fatalf("truncated message read as %s", got.State)
	}

	// A complete earlier PodSet that fits nowhere is still proof.
	proven := "couldn't assign flavors to pod set launcher: insufficient quota for cpu in flavor a, previously considered podsets requests (0) + current podset request (8) > maximum capacity (4); couldn't assign flavors to pod set " + truncated[len("couldn't assign flavors to pod set "):]
	proven = proven[:32*1024-len(" ...")] + " ..."
	if got := classify(t, workloadSpec{conditions: []condition{pending("Pending", proven)}}).Admission; got.State != rc.KueueAdmissionExceedsQuota {
		t.Fatalf("complete segment before the cut read as %s", got.State)
	}
}
