package schedulinginsight

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func TestForResourceRequiresExactKueueWorkloadGVK(t *testing.T) {
	base := loadFixture(t, "waiting-granular.yaml")
	tests := []struct {
		name       string
		apiVersion string
		kind       string
		want       bool
	}{
		{name: "supported", apiVersion: "kueue.x-k8s.io/v1beta2", kind: "Workload", want: true},
		{name: "old served version is not a compatibility fallback", apiVersion: "kueue.x-k8s.io/v1beta1", kind: "Workload"},
		{name: "same kind in another group", apiVersion: "example.io/v1beta2", kind: "Workload"},
		{name: "other Kueue kind", apiVersion: "kueue.x-k8s.io/v1beta2", kind: "LocalQueue"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			u := base.DeepCopy()
			u.SetAPIVersion(test.apiVersion)
			u.SetKind(test.kind)
			if got := ForResource(u, resourcecontext.TierBasic); (got != nil) != test.want {
				t.Fatalf("ForResource() = %+v, want supported=%t", got, test.want)
			}
		})
	}

	if got := ForResource(&corev1.Pod{}, resourcecontext.TierBasic); got != nil {
		t.Fatalf("typed lookalike produced scheduling summary: %+v", got)
	}
}

func TestForResourceProjectsOneKueueAdmissionObservation(t *testing.T) {
	observation := onlyObservation(t, ForResource(loadFixture(t, "waiting-granular.yaml"), resourcecontext.TierBasic))
	if observation.Source != resourcecontext.SchedulingSourceKueue || observation.Domain != resourcecontext.SchedulingDomainAdmission {
		t.Fatalf("source/domain = %q/%q", observation.Source, observation.Domain)
	}
	if observation.Subject != (resourcecontext.ContextRef{
		Kind: "Workload", Group: "kueue.x-k8s.io", Namespace: "training", Name: "waiting-for-gpu-quota",
	}) {
		t.Fatalf("subject = %+v", observation.Subject)
	}
	if observation.SubjectGeneration != 5 {
		t.Fatalf("subject generation = %d, want 5", observation.SubjectGeneration)
	}
	if observation.Decision != resourcecontext.SchedulingDecisionUnsatisfied {
		t.Fatalf("decision = %q", observation.Decision)
	}
	if observation.PrimaryCondition == nil || observation.PrimaryCondition.Type != "QuotaReserved" ||
		observation.PrimaryCondition.Status != "False" || observation.PrimaryCondition.Reason != "WaitingForQuota" ||
		observation.PrimaryCondition.Message != "Waiting for quota in ClusterQueue gpu-team" ||
		observation.PrimaryCondition.ObservedGeneration != 3 ||
		observation.PrimaryCondition.LastTransitionTime != "2026-08-30T10:00:00Z" {
		t.Fatalf("primary condition = %+v", observation.PrimaryCondition)
	}
	if len(observation.Queues) != 1 || observation.Queues[0].Name != "gpu" ||
		fmt.Sprint(observation.Queues[0].Roles) != fmt.Sprint([]resourcecontext.SchedulingQueueRole{resourcecontext.SchedulingQueueSubmission}) ||
		observation.Queues[0].Ref == nil || *observation.Queues[0].Ref != (resourcecontext.ContextRef{
		Kind: "LocalQueue", Group: "kueue.x-k8s.io", Namespace: "training", Name: "gpu",
	}) {
		t.Fatalf("queues = %+v", observation.Queues)
	}
	if observation.Kueue == nil || observation.Kueue.Phase != resourcecontext.KueuePhasePending || observation.Kueue.Outcome != "" {
		t.Fatalf("Kueue details = %+v", observation.Kueue)
	}

	wire, err := json.Marshal(observation)
	if err != nil {
		t.Fatal(err)
	}
	for _, retiredField := range []string{`"stage"`, `"blocker"`, `"controller"`, `"reasonPrecision"`} {
		if strings.Contains(string(wire), retiredField) {
			t.Fatalf("retired universal lifecycle field %s remains in %s", retiredField, wire)
		}
	}
}

func TestForResourceEvidenceFreeIsUnknownWithoutInventedReason(t *testing.T) {
	observation := onlyObservation(t, ForResource(loadFixture(t, "evidence-free.yaml"), resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionUnknown || observation.PrimaryCondition != nil {
		t.Fatalf("observation = %+v", observation)
	}
	if observation.Kueue == nil || observation.Kueue.Phase != resourcecontext.KueuePhasePending || observation.Kueue.Active != nil {
		t.Fatalf("Kueue details = %+v", observation.Kueue)
	}
}

func TestForResourceFinishedPrecedesInactiveAndRetainsAdmissionDecision(t *testing.T) {
	u := loadFixture(t, "finished-inactive.yaml")
	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionSatisfied {
		t.Fatalf("decision = %q, want retained successful admission", observation.Decision)
	}
	if observation.PrimaryCondition == nil || observation.PrimaryCondition.Type != "Finished" ||
		observation.PrimaryCondition.Message != "Training completed" {
		t.Fatalf("primary condition = %+v", observation.PrimaryCondition)
	}
	if observation.Kueue == nil || observation.Kueue.Phase != resourcecontext.KueuePhaseFinished ||
		observation.Kueue.Outcome != resourcecontext.KueueOutcomeSucceeded || observation.Kueue.Active == nil || *observation.Kueue.Active {
		t.Fatalf("Kueue details = %+v", observation.Kueue)
	}
	if got := conditionTypes(observation.Disruptions); fmt.Sprint(got) != fmt.Sprint([]string{"DeactivationTarget"}) {
		t.Fatalf("disruptions = %v", got)
	}

	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		condition := item.(map[string]any)
		if condition["type"] == "Finished" {
			condition["reason"] = "OutOfSync"
		}
	}
	setConditions(t, u, conditions)
	if outcome := onlyObservation(t, ForResource(u, resourcecontext.TierBasic)).Kueue.Outcome; outcome != resourcecontext.KueueOutcomeFailed {
		t.Fatalf("known abnormal terminal reason outcome = %q", outcome)
	}
	for _, item := range conditions {
		condition := item.(map[string]any)
		if condition["type"] == "Finished" {
			condition["reason"] = "FutureTerminalReason"
		}
	}
	setConditions(t, u, conditions)
	if outcome := onlyObservation(t, ForResource(u, resourcecontext.TierBasic)).Kueue.Outcome; outcome != "" {
		t.Fatalf("unknown terminal reason was relabeled as outcome %q", outcome)
	}

	conditions = conditionsWithoutType(conditions, "Admitted")
	setConditions(t, u, conditions)
	if decision := onlyObservation(t, ForResource(u, resourcecontext.TierBasic)).Decision; decision != resourcecontext.SchedulingDecisionUnknown {
		t.Fatalf("finished workload without conclusive admission evidence decision = %q", decision)
	}
}

func TestWorkloadOutcomeUsesExactV0192FinishedReasons(t *testing.T) {
	tests := []struct {
		reason string
		want   resourcecontext.KueueOutcome
	}{
		{reason: "Succeeded", want: resourcecontext.KueueOutcomeSucceeded},
		{reason: "Failed", want: resourcecontext.KueueOutcomeFailed},
		{reason: "OutOfSync", want: resourcecontext.KueueOutcomeFailed},
		{reason: "OwnerNotFound", want: resourcecontext.KueueOutcomeFailed},
		{reason: "FailedToStart", want: resourcecontext.KueueOutcomeFailed},
		{reason: "WorkloadSliceReplaced"},
		{reason: "FutureTerminalReason"},
	}
	for _, test := range tests {
		t.Run(test.reason, func(t *testing.T) {
			conditions := map[string]workloadCondition{
				"Finished": {Type: "Finished", Status: "True", Reason: test.reason},
			}
			if got := workloadOutcome(conditions); got != test.want {
				t.Fatalf("outcome = %q, want %q", got, test.want)
			}
		})
	}
}

func TestForResourceInactiveWithoutConditionIsHeld(t *testing.T) {
	u := loadFixture(t, "evidence-free.yaml")
	if err := unstructured.SetNestedField(u.Object, false, "spec", "active"); err != nil {
		t.Fatal(err)
	}
	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionHeld || observation.PrimaryCondition != nil {
		t.Fatalf("observation = %+v", observation)
	}
	if observation.Kueue.Active == nil || *observation.Kueue.Active {
		t.Fatalf("active = %+v", observation.Kueue.Active)
	}
}

func TestForResourceOnHoldIsHeldWhileActive(t *testing.T) {
	u := loadFixture(t, "no-reservation.yaml")
	if err := unstructured.SetNestedField(u.Object, true, "spec", "active"); err != nil {
		t.Fatal(err)
	}
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		condition := item.(map[string]any)
		if condition["type"] == "QuotaReserved" {
			condition["reason"] = "OnHold"
			condition["message"] = "Quota intentionally released while scaled to zero"
		}
	}
	setConditions(t, u, conditions)

	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionHeld || observation.PrimaryCondition == nil ||
		observation.PrimaryCondition.Type != "QuotaReserved" || observation.PrimaryCondition.Reason != "OnHold" {
		t.Fatalf("on-hold observation = %+v", observation)
	}
}

func TestForResourceAdmissionGatedByControllerIsHeld(t *testing.T) {
	u := loadFixture(t, "no-reservation.yaml")
	u.SetAnnotations(map[string]string{admissionGatedByAnnotation: "example.com/budget-approver"})
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		condition := item.(map[string]any)
		if condition["type"] == "QuotaReserved" {
			condition["reason"] = "AdmissionGated"
			condition["message"] = "Admission is gated by: example.com/budget-approver"
		}
	}
	setConditions(t, u, conditions)

	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionHeld || observation.PrimaryCondition == nil ||
		observation.PrimaryCondition.Reason != "AdmissionGated" {
		t.Fatalf("controller-gated observation = %+v", observation)
	}

	u.SetAnnotations(map[string]string{admissionGatedByAnnotation: " "})
	if decision := onlyObservation(t, ForResource(u, resourcecontext.TierBasic)).Decision; decision != resourcecontext.SchedulingDecisionHeld {
		t.Fatalf("literal nonempty annotation decision = %q, want held", decision)
	}

	u.SetAnnotations(nil)
	if decision := onlyObservation(t, ForResource(u, resourcecontext.TierBasic)).Decision; decision != resourcecontext.SchedulingDecisionUnsatisfied {
		t.Fatalf("condition without current annotation decision = %q, want unsatisfied", decision)
	}
}

func TestForResourcePreemptionGateTakesPrecedenceOverAdmissionGatedByAnnotation(t *testing.T) {
	u := loadFixture(t, "blocked-preemption.yaml")
	u.SetAnnotations(map[string]string{admissionGatedByAnnotation: "example.com/budget-approver"})

	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionUnsatisfied || observation.PrimaryCondition == nil ||
		observation.PrimaryCondition.Type != "BlockedOnPreemptionGates" {
		t.Fatalf("preemption-gated observation = %+v", observation)
	}
}

func TestForResourceAdmissionGateAnnotationAloneDoesNotInferFeatureState(t *testing.T) {
	u := loadFixture(t, "no-reservation.yaml")
	u.SetAnnotations(map[string]string{admissionGatedByAnnotation: "example.com/budget-approver"})

	if decision := onlyObservation(t, ForResource(u, resourcecontext.TierBasic)).Decision; decision != resourcecontext.SchedulingDecisionUnsatisfied {
		t.Fatalf("annotation-only decision = %q, want unsatisfied", decision)
	}
}

func TestForResourceSuspendedReasonRemainsUnsatisfiedWithoutQueueEvidence(t *testing.T) {
	u := loadFixture(t, "no-reservation.yaml")
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		condition := item.(map[string]any)
		if condition["type"] == "QuotaReserved" {
			condition["reason"] = "Suspended"
			condition["message"] = "ClusterQueue gpu-team is inactive"
		}
	}
	setConditions(t, u, conditions)

	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionUnsatisfied || observation.PrimaryCondition == nil ||
		observation.PrimaryCondition.Reason != "Suspended" {
		t.Fatalf("inactive-queue observation = %+v", observation)
	}
}

func TestForResourceNoReservationUsesActionableQuotaCondition(t *testing.T) {
	observation := onlyObservation(t, ForResource(loadFixture(t, "no-reservation.yaml"), resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionUnsatisfied {
		t.Fatalf("decision = %q", observation.Decision)
	}
	if observation.PrimaryCondition == nil || observation.PrimaryCondition.Type != "QuotaReserved" ||
		observation.PrimaryCondition.Reason != "NoMatchingFlavor" ||
		observation.PrimaryCondition.Message != "No ResourceFlavor matches nvidia.com/gpu" {
		t.Fatalf("primary condition = %+v", observation.PrimaryCondition)
	}
}

func TestForResourceRetainsLegacyInadmissibleReasonWithoutInventingCondition(t *testing.T) {
	u := loadFixture(t, "no-reservation.yaml")
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		condition := item.(map[string]any)
		if condition["type"] == "QuotaReserved" {
			condition["reason"] = "Inadmissible"
			condition["message"] = "LocalQueue gpu is missing"
		}
	}
	setConditions(t, u, conditions)

	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionUnsatisfied || observation.PrimaryCondition == nil ||
		observation.PrimaryCondition.Type != "QuotaReserved" || observation.PrimaryCondition.Reason != "Inadmissible" {
		t.Fatalf("legacy inadmissible reason = %+v", observation)
	}
}

func TestForResourceProjectsAffirmativeDisruptions(t *testing.T) {
	observation := onlyObservation(t, ForResource(loadFixture(t, "disrupted.yaml"), resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionUnsatisfied || observation.PrimaryCondition == nil ||
		observation.PrimaryCondition.Type != "QuotaReserved" {
		t.Fatalf("current admission = %+v", observation)
	}
	if got := conditionTypes(observation.Disruptions); fmt.Sprint(got) != fmt.Sprint([]string{"Evicted", "Preempted"}) {
		t.Fatalf("affirmative disruptions = %v", got)
	}
	if observation.Kueue.Phase != resourcecontext.KueuePhasePending || observation.Kueue.PodsReady == nil ||
		observation.Kueue.PodsReady.Status != "False" || observation.Kueue.PodsReady.Reason != "WaitForRecovery" {
		t.Fatalf("Kueue details = %+v", observation.Kueue)
	}
}

func TestForResourceReadmittedRecoveryUsesResetConditionsAndTypedReplacementWait(t *testing.T) {
	observation := onlyObservation(t, ForResource(loadFixture(t, "disrupted-readmitted.yaml"), resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionSatisfied || observation.PrimaryCondition == nil ||
		observation.PrimaryCondition.Type != "Admitted" {
		t.Fatalf("current admission = %+v", observation)
	}
	if len(observation.Disruptions) != 0 {
		t.Fatalf("reset disruption conditions treated as active = %+v", observation.Disruptions)
	}
	if observation.Kueue.Phase != resourcecontext.KueuePhaseAdmitted || observation.Kueue.PodsReady == nil ||
		observation.Kueue.PodsReady.Status != "False" || observation.Kueue.PodsReady.Reason != "WaitForRecovery" ||
		observation.Kueue.WaitingForReplacementPods == nil || observation.Kueue.WaitingForReplacementPods.Status != "True" ||
		observation.Kueue.WaitingForReplacementPods.Reason != "PodsFailed" ||
		observation.Kueue.WaitingForReplacementPods.ObservedGeneration != 5 {
		t.Fatalf("Kueue recovery details = %+v", observation.Kueue)
	}
	if len(observation.Queues) != 2 || observation.Queues[1].Name != "gpu-team" ||
		fmt.Sprint(observation.Queues[1].Roles) != fmt.Sprint([]resourcecontext.SchedulingQueueRole{resourcecontext.SchedulingQueueEntitlement}) ||
		observation.Queues[1].Ref == nil || observation.Queues[1].Ref.Name != "gpu-team" {
		t.Fatalf("queues = %+v", observation.Queues)
	}

	assignment := observation.Kueue.PodSetAssignments[0]
	if assignment.Count == nil || *assignment.Count != 2 {
		t.Fatalf("assignment count = %+v", assignment.Count)
	}
	assertResourceAssignment(t, assignment.Resources, "cpu", "on-demand", "4")
	assertResourceAssignment(t, assignment.Resources, "memory", "", "16Gi")
	assertResourceAssignment(t, assignment.Resources, "nvidia.com/gpu", "a10", "2")
}

func TestForResourcePodsReadyRemainsAdmissionEvidenceNotUniversalRunningPhase(t *testing.T) {
	u := loadFixture(t, "disrupted-readmitted.yaml")
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		condition := item.(map[string]any)
		if condition["type"] == "PodsReady" {
			condition["status"] = "True"
			condition["reason"] = "Started"
		}
	}
	setConditions(t, u, conditions)

	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionSatisfied || observation.PrimaryCondition == nil ||
		observation.PrimaryCondition.Type != "Admitted" || observation.Kueue.Phase != resourcecontext.KueuePhaseAdmitted ||
		observation.Kueue.PodsReady == nil || observation.Kueue.PodsReady.Status != "True" {
		t.Fatalf("admission observation = %+v", observation)
	}
}

func TestForResourceAdmissionChecksPreserveNativeStateAndBasicEvidence(t *testing.T) {
	u := loadFixture(t, "admission-check-rejected.yaml")
	basic := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	diagnostic := onlyObservation(t, ForResource(u, resourcecontext.TierDiagnostic))

	if basic.Kueue.Phase != resourcecontext.KueuePhaseQuotaReserved || basic.Decision != resourcecontext.SchedulingDecisionUnsatisfied {
		t.Fatalf("phase/decision = %q/%q", basic.Kueue.Phase, basic.Decision)
	}
	if basic.PrimaryCondition == nil || basic.PrimaryCondition.Type != "Admitted" ||
		basic.PrimaryCondition.Reason != "UnsatisfiedAdmissionChecks" || basic.PrimaryCondition.Message != "Admission checks are not ready" {
		t.Fatalf("primary condition = %+v", basic.PrimaryCondition)
	}
	if got := gateNames(basic.Gates); fmt.Sprint(got) != fmt.Sprint([]string{"capacity", "policy"}) {
		t.Fatalf("gate order = %v", got)
	}
	capacity := basic.Gates[0]
	if capacity.Kind != resourcecontext.SchedulingGateAdmissionCheck || capacity.NativeState != "Rejected" ||
		capacity.Decision != resourcecontext.SchedulingDecisionUnsatisfied ||
		capacity.Message != "Provisioning request cannot satisfy this workload" ||
		capacity.LastTransitionTime != "2026-08-30T10:03:00Z" || capacity.RetryCount == nil || *capacity.RetryCount != 2 ||
		capacity.RequeueAfterSeconds == nil || *capacity.RequeueAfterSeconds != 60 {
		t.Fatalf("capacity gate = %+v", capacity)
	}
	ready := basic.Gates[1]
	if ready.NativeState != "Ready" || ready.Decision != resourcecontext.SchedulingDecisionSatisfied ||
		ready.Message != "" || ready.LastTransitionTime != "2026-08-30T10:02:00Z" || ready.RetryCount != nil || ready.RequeueAfterSeconds != nil {
		t.Fatalf("basic ready gate = %+v", ready)
	}
	if diagnostic.Gates[1].Message != "Policy passed" || diagnostic.Gates[1].LastTransitionTime != "2026-08-30T10:02:00Z" {
		t.Fatalf("diagnostic ready gate = %+v", diagnostic.Gates[1])
	}
	if basic.Kueue.RequeueState == nil || basic.Kueue.RequeueState.Count == nil || *basic.Kueue.RequeueState.Count != 2 ||
		basic.Kueue.RequeueState.RequeueAt != "2026-08-30T10:04:00Z" {
		t.Fatalf("requeue state = %+v", basic.Kueue.RequeueState)
	}
}

func TestForResourceReadyGateRetainsStructuredRetryEvidenceInBasic(t *testing.T) {
	u := loadFixture(t, "admission-check-rejected.yaml")
	checks, _, _ := unstructured.NestedSlice(u.Object, "status", "admissionChecks")
	ready := checks[0].(map[string]any)
	ready["retryCount"] = int64(3)
	ready["requeueAfterSeconds"] = int64(15)
	if err := unstructured.SetNestedSlice(u.Object, checks, "status", "admissionChecks"); err != nil {
		t.Fatal(err)
	}

	gates := onlyObservation(t, ForResource(u, resourcecontext.TierBasic)).Gates
	readyGate := gates[1]
	if readyGate.Name != "policy" || readyGate.Ref == nil || readyGate.Ref.Name != "policy" || readyGate.Message != "" ||
		readyGate.LastTransitionTime != "2026-08-30T10:02:00Z" || readyGate.RetryCount == nil || *readyGate.RetryCount != 3 ||
		readyGate.RequeueAfterSeconds == nil || *readyGate.RequeueAfterSeconds != 15 {
		t.Fatalf("ready gate = %+v", readyGate)
	}
}

func TestAdmissionCheckOrderingAndDecisionMapping(t *testing.T) {
	u := loadFixture(t, "evidence-free.yaml")
	checks := []any{
		map[string]any{"name": "ready", "state": "Ready", "message": "ready"},
		map[string]any{"name": "unknown", "state": "FutureState", "message": "unknown"},
		map[string]any{"name": "pending", "state": "Pending", "message": "pending"},
		map[string]any{"name": "retry", "state": "Retry", "message": "retry"},
		map[string]any{"name": "rejected", "state": "Rejected", "message": "rejected"},
	}
	if err := unstructured.SetNestedSlice(u.Object, checks, "status", "admissionChecks"); err != nil {
		t.Fatal(err)
	}
	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if got := gateNames(observation.Gates); fmt.Sprint(got) != fmt.Sprint([]string{"rejected", "retry", "pending", "ready", "unknown"}) {
		t.Fatalf("gate order = %v", got)
	}
	wantDecisions := []resourcecontext.SchedulingDecision{
		resourcecontext.SchedulingDecisionUnsatisfied,
		resourcecontext.SchedulingDecisionUnsatisfied,
		resourcecontext.SchedulingDecisionUnsatisfied,
		resourcecontext.SchedulingDecisionSatisfied,
		resourcecontext.SchedulingDecisionUnknown,
	}
	for i, want := range wantDecisions {
		if observation.Gates[i].Decision != want {
			t.Fatalf("gate %q decision = %q, want %q", observation.Gates[i].Name, observation.Gates[i].Decision, want)
		}
	}
	if observation.Decision != resourcecontext.SchedulingDecisionUnsatisfied {
		t.Fatalf("incomplete known gates did not block observation: %q", observation.Decision)
	}

	if err := unstructured.SetNestedSlice(u.Object, []any{checks[1]}, "status", "admissionChecks"); err != nil {
		t.Fatal(err)
	}
	unknownOnly := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if unknownOnly.Decision != resourcecontext.SchedulingDecisionUnknown || unknownOnly.Gates[0].Message != "" {
		t.Fatalf("unknown gate invented a blocker/evidence: %+v", unknownOnly)
	}
}

func TestForResourceBlockedOnPreemptionGatesIsPrimaryAndProjectsGateUnion(t *testing.T) {
	observation := onlyObservation(t, ForResource(loadFixture(t, "blocked-preemption.yaml"), resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionUnsatisfied || observation.PrimaryCondition == nil ||
		observation.PrimaryCondition.Type != "BlockedOnPreemptionGates" || observation.PrimaryCondition.Status != "True" ||
		observation.PrimaryCondition.Reason != "PreemptionGated" || observation.PrimaryCondition.ObservedGeneration != 7 {
		t.Fatalf("blocked observation = %+v", observation)
	}
	if got := gateNames(observation.Gates); fmt.Sprint(got) != fmt.Sprint([]string{"capacity-preemption", "priority-budget", "policy", "concurrent-admission"}) {
		t.Fatalf("combined gate order = %v", got)
	}

	capacity := observation.Gates[0]
	if capacity.Kind != resourcecontext.SchedulingGatePreemption || capacity.Ref != nil || capacity.NativeState != "Closed" ||
		capacity.Decision != resourcecontext.SchedulingDecisionUnsatisfied || capacity.LastTransitionTime != "2026-08-30T09:59:00Z" {
		t.Fatalf("closed preemption gate = %+v", capacity)
	}
	defaultClosed := observation.Gates[1]
	if defaultClosed.Kind != resourcecontext.SchedulingGatePreemption || defaultClosed.Ref != nil ||
		defaultClosed.NativeState != "Closed" || defaultClosed.Decision != resourcecontext.SchedulingDecisionUnsatisfied ||
		defaultClosed.LastTransitionTime != "" {
		t.Fatalf("spec-only preemption gate = %+v", defaultClosed)
	}
	policy := observation.Gates[2]
	if policy.Kind != resourcecontext.SchedulingGateAdmissionCheck || policy.Ref == nil || policy.Ref.Name != "policy" ||
		policy.Decision != resourcecontext.SchedulingDecisionSatisfied {
		t.Fatalf("admission gate = %+v", policy)
	}
	opened := observation.Gates[3]
	if opened.Kind != resourcecontext.SchedulingGatePreemption || opened.Ref != nil || opened.NativeState != "Open" ||
		opened.Decision != resourcecontext.SchedulingDecisionSatisfied || opened.LastTransitionTime != "2026-08-30T09:58:00Z" {
		t.Fatalf("open preemption gate = %+v", opened)
	}
}

func TestForResourceClosedPreemptionGateDoesNotInventCurrentBlock(t *testing.T) {
	u := loadFixture(t, "evidence-free.yaml")
	if err := unstructured.SetNestedSlice(u.Object, []any{map[string]any{"name": "preempt-if-needed"}}, "spec", "preemptionGates"); err != nil {
		t.Fatal(err)
	}

	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if observation.Decision != resourcecontext.SchedulingDecisionUnknown || observation.PrimaryCondition != nil ||
		len(observation.Gates) != 1 || observation.Gates[0].Decision != resourcecontext.SchedulingDecisionUnsatisfied {
		t.Fatalf("spec-only gate was mistaken for a current admission block: %+v", observation)
	}
}

func TestForResourceIgnoresStatusForRemovedPreemptionGate(t *testing.T) {
	u := loadFixture(t, "evidence-free.yaml")
	if err := unstructured.SetNestedSlice(u.Object, []any{map[string]any{
		"name": "removed-gate", "position": "Closed", "lastTransitionTime": "2026-08-30T10:00:00Z",
	}}, "status", "preemptionGates"); err != nil {
		t.Fatal(err)
	}

	observation := onlyObservation(t, ForResource(u, resourcecontext.TierBasic))
	if len(observation.Gates) != 0 {
		t.Fatalf("status-only preemption gate remained after spec removal: %+v", observation.Gates)
	}
}

func TestKueueV0192AdmissionCheckMaximumIsSerializedWithoutLoss(t *testing.T) {
	u := loadFixture(t, "evidence-free.yaml")
	checks := make([]any, 0, maxAdmissionChecks)
	for i := 0; i < maxAdmissionChecks; i++ {
		checks = append(checks, map[string]any{
			"name":  fmt.Sprintf("check-%02d", i),
			"state": "Ready",
		})
	}
	if err := unstructured.SetNestedSlice(u.Object, checks, "status", "admissionChecks"); err != nil {
		t.Fatal(err)
	}
	gates := onlyObservation(t, ForResource(u, resourcecontext.TierBasic)).Gates
	if maxAdmissionChecks != 8 || len(gates) != maxAdmissionChecks {
		t.Fatalf("v0.19.2 admission-check maximum/output = %d/%d, want 8/8", maxAdmissionChecks, len(gates))
	}
}

func TestForResourceConcurrentAdmissionUsesExactControllerOwner(t *testing.T) {
	variant := loadFixture(t, "concurrent-variant.yaml")
	details := onlyObservation(t, ForResource(variant, resourcecontext.TierBasic)).Kueue
	if details.ConcurrentAdmission == nil || details.ConcurrentAdmission.ParentName != "training-parent" ||
		details.ConcurrentAdmission.ParentRef == nil || *details.ConcurrentAdmission.ParentRef != (resourcecontext.ContextRef{
		Kind: "Workload", Group: "kueue.x-k8s.io", Namespace: "training", Name: "training-parent",
	}) {
		t.Fatalf("concurrent admission = %+v", details.ConcurrentAdmission)
	}

	oldVersion := variant.DeepCopy()
	owners := oldVersion.GetOwnerReferences()
	owners[0].APIVersion = "kueue.x-k8s.io/v1beta1"
	oldVersion.SetOwnerReferences(owners)
	if got := onlyObservation(t, ForResource(oldVersion, resourcecontext.TierBasic)).Kueue.ConcurrentAdmission; got != nil {
		t.Fatalf("unsupported owner version was treated as a parent: %+v", got)
	}

	ordinary := onlyObservation(t, ForResource(loadFixture(t, "waiting-granular.yaml"), resourcecontext.TierBasic))
	if ordinary.Kueue.ConcurrentAdmission != nil {
		t.Fatalf("ordinary Job-owned Workload classified as Concurrent Admission: %+v", ordinary.Kueue.ConcurrentAdmission)
	}
}

func TestForResourceBoundsPodSetAssignmentsWithoutBreakingCorrelation(t *testing.T) {
	u := loadFixture(t, "admission-check-rejected.yaml")
	const kueueAPIMaxPodSetAssignments = 18
	assignments := make([]any, 0, kueueAPIMaxPodSetAssignments)
	for i := kueueAPIMaxPodSetAssignments - 1; i >= 0; i-- {
		flavors := make(map[string]any, maxProjectedResourcesPerPodSet+2)
		usage := make(map[string]any, maxProjectedResourcesPerPodSet+2)
		for resourceIndex := 0; resourceIndex < maxProjectedResourcesPerPodSet+2; resourceIndex++ {
			name := fmt.Sprintf("example.com/resource-%02d", resourceIndex)
			if resourceIndex%2 == 0 {
				flavors[name] = fmt.Sprintf("flavor-%02d", resourceIndex)
			}
			usage[name] = fmt.Sprintf("%d", resourceIndex+1)
		}
		assignments = append(assignments, map[string]any{
			"name":          fmt.Sprintf("set-%02d", i),
			"count":         int64(i + 1),
			"flavors":       flavors,
			"resourceUsage": usage,
		})
	}
	if err := unstructured.SetNestedSlice(u.Object, assignments, "status", "admission", "podSetAssignments"); err != nil {
		t.Fatal(err)
	}

	details := onlyObservation(t, ForResource(u, resourcecontext.TierBasic)).Kueue
	if maxProjectedPodSetAssignments != 8 || len(details.PodSetAssignments) != maxProjectedPodSetAssignments || !details.PodSetAssignmentsTruncated {
		t.Fatalf("PodSets len/truncated = %d/%t", len(details.PodSetAssignments), details.PodSetAssignmentsTruncated)
	}
	first := details.PodSetAssignments[0]
	if first.Name != "set-00" || first.Count == nil || *first.Count != 1 || len(first.Resources) != maxProjectedResourcesPerPodSet || !first.ResourcesTruncated {
		t.Fatalf("first assignment = %+v", first)
	}
	if first.Resources[0].Name != "example.com/resource-00" || first.Resources[0].Flavor != "flavor-00" ||
		first.Resources[0].FlavorRef == nil || first.Resources[0].FlavorRef.Name != "flavor-00" || first.Resources[0].Usage != "1" {
		t.Fatalf("first correlated resource = %+v", first.Resources[0])
	}
	if first.Resources[1].Name != "example.com/resource-01" || first.Resources[1].Flavor != "" ||
		first.Resources[1].FlavorRef != nil || first.Resources[1].Usage != "2" {
		t.Fatalf("usage-only resource = %+v", first.Resources[1])
	}
}

func TestResourceAssignmentsPrioritizeExtendedResourcesBeforeTruncation(t *testing.T) {
	state := map[string]any{
		"resourceUsage": map[string]any{
			"cpu": "1", "ephemeral-storage": "1Gi", "hugepages-1Gi": "1Gi", "memory": "2Gi",
			"nvidia.com/gpu": "1", "nvidia.com/mig-1g.10gb": "1", "rdma/hca_shared_devices_a": "1", "example.com/fpga": "1",
		},
	}
	resources, truncated := resourceAssignments(state)
	if !truncated || len(resources) != maxProjectedResourcesPerPodSet {
		t.Fatalf("resources len/truncated = %d/%t", len(resources), truncated)
	}
	for _, name := range []string{"example.com/fpga", "nvidia.com/gpu", "nvidia.com/mig-1g.10gb", "rdma/hca_shared_devices_a"} {
		assertResourceAssignment(t, resources, name, "", "1")
	}
	if resources[0].Name != "example.com/fpga" || resources[4].Name != "cpu" {
		t.Fatalf("resource significance order = %+v", resources)
	}
	for _, resource := range resources {
		if resource.Name == "memory" {
			t.Fatalf("lexical core resource should be truncated after extended resources: %+v", resources)
		}
	}
}

func TestForResourceBoundsUTF8MessagesAndSchedulingContribution(t *testing.T) {
	u := loadFixture(t, "admission-check-rejected.yaml")
	longMessage := strings.Repeat("界", maxPrimaryMessageBytes)
	conditions, _, _ := unstructured.NestedSlice(u.Object, "status", "conditions")
	for _, item := range conditions {
		item.(map[string]any)["message"] = longMessage
	}
	setConditions(t, u, conditions)

	checks := make([]any, 0, maxAdmissionChecks)
	for i := 0; i < maxAdmissionChecks; i++ {
		checks = append(checks, map[string]any{
			"name":               strings.Repeat(string(rune('a'+i)), 316),
			"state":              "Rejected",
			"message":            longMessage,
			"lastTransitionTime": "2026-08-30T10:03:00Z",
		})
	}
	if err := unstructured.SetNestedSlice(u.Object, checks, "status", "admissionChecks"); err != nil {
		t.Fatal(err)
	}
	preemptionSpecs := make([]any, 0, 8)
	preemptionStates := make([]any, 0, 8)
	for i := 0; i < 8; i++ {
		name := fmt.Sprintf("%s-%02d", strings.Repeat(string(rune('a'+i)), 60), i)
		preemptionSpecs = append(preemptionSpecs, map[string]any{"name": name})
		preemptionStates = append(preemptionStates, map[string]any{
			"name":               name,
			"position":           "Closed",
			"lastTransitionTime": "2026-08-30T10:03:00Z",
		})
	}
	if err := unstructured.SetNestedSlice(u.Object, preemptionSpecs, "spec", "preemptionGates"); err != nil {
		t.Fatal(err)
	}
	if err := unstructured.SetNestedSlice(u.Object, preemptionStates, "status", "preemptionGates"); err != nil {
		t.Fatal(err)
	}

	assignments := make([]any, 0, maxProjectedPodSetAssignments)
	for i := 0; i < maxProjectedPodSetAssignments; i++ {
		flavors := make(map[string]any, maxProjectedResourcesPerPodSet)
		usage := make(map[string]any, maxProjectedResourcesPerPodSet)
		for resourceIndex := 0; resourceIndex < maxProjectedResourcesPerPodSet; resourceIndex++ {
			name := fmt.Sprintf("%s.example.com/resource-%02d", strings.Repeat(string(rune('k'+resourceIndex)), 220), resourceIndex)
			flavors[name] = strings.Repeat(string(rune('s'+resourceIndex)), 250)
			usage[name] = "999999999999999999m"
		}
		assignments = append(assignments, map[string]any{
			"name":          fmt.Sprintf("%s-%02d", strings.Repeat("p", 55), i),
			"count":         int64(999),
			"flavors":       flavors,
			"resourceUsage": usage,
		})
	}
	if err := unstructured.SetNestedSlice(u.Object, assignments, "status", "admission", "podSetAssignments"); err != nil {
		t.Fatal(err)
	}

	for _, tier := range []resourcecontext.ContextTier{resourcecontext.TierBasic, resourcecontext.TierDiagnostic} {
		summary := ForResource(u, tier)
		observation := onlyObservation(t, summary)
		if len(observation.Gates) != maxAdmissionChecks+8 {
			t.Fatalf("gates = %d, want maximum admission and preemption evidence", len(observation.Gates))
		}
		if len(observation.PrimaryCondition.Message) > maxPrimaryMessageBytes || !utf8.ValidString(observation.PrimaryCondition.Message) ||
			!strings.HasSuffix(observation.PrimaryCondition.Message, "…") {
			t.Fatalf("primary message bytes=%d value=%q", len(observation.PrimaryCondition.Message), observation.PrimaryCondition.Message)
		}
		for _, gate := range observation.Gates {
			if gate.Kind != resourcecontext.SchedulingGateAdmissionCheck {
				continue
			}
			messageLimit := maxAdmissionMessageBytes
			if tier == resourcecontext.TierDiagnostic {
				messageLimit = maxPrimaryMessageBytes
			}
			if len(gate.Message) > messageLimit || !utf8.ValidString(gate.Message) || !strings.HasSuffix(gate.Message, "…") {
				t.Fatalf("gate message bytes=%d value=%q", len(gate.Message), gate.Message)
			}
		}
		wire, err := json.Marshal(resourcecontext.ResourceContext{Tier: tier, Scheduling: summary})
		if err != nil {
			t.Fatal(err)
		}
		const outputBudgetBytes = 64 << 10
		if len(wire) > outputBudgetBytes {
			t.Fatalf("scheduling projection = %d bytes, contribution budget = %d", len(wire), outputBudgetBytes)
		}
		t.Logf("%s scheduling contribution with maximal evidence: %d bytes", tier, len(wire))
	}
}

func onlyObservation(t *testing.T, summary *resourcecontext.SchedulingSummary) resourcecontext.SchedulingObservation {
	t.Helper()
	if summary == nil || len(summary.Observations) != 1 {
		t.Fatalf("scheduling summary = %+v, want exactly one observation", summary)
	}
	return summary.Observations[0]
}

func setConditions(t *testing.T, u *unstructured.Unstructured, conditions []any) {
	t.Helper()
	if err := unstructured.SetNestedSlice(u.Object, conditions, "status", "conditions"); err != nil {
		t.Fatal(err)
	}
}

func conditionsWithoutType(conditions []any, conditionType string) []any {
	result := make([]any, 0, len(conditions))
	for _, item := range conditions {
		condition, _ := item.(map[string]any)
		if condition["type"] != conditionType {
			result = append(result, item)
		}
	}
	return result
}

func conditionTypes(conditions []resourcecontext.ConditionSummary) []string {
	result := make([]string, 0, len(conditions))
	for _, condition := range conditions {
		result = append(result, condition.Type)
	}
	return result
}

func gateNames(gates []resourcecontext.SchedulingGate) []string {
	result := make([]string, 0, len(gates))
	for _, gate := range gates {
		result = append(result, gate.Name)
	}
	return result
}

func assertResourceAssignment(t *testing.T, resources []resourcecontext.KueueResourceAssignment, name, flavor, usage string) {
	t.Helper()
	for _, resource := range resources {
		if resource.Name != name {
			continue
		}
		if flavor == "" && (resource.Flavor != "" || resource.FlavorRef != nil) {
			t.Fatalf("resource %q flavor = %q/%+v, want none", name, resource.Flavor, resource.FlavorRef)
		}
		if flavor != "" && (resource.Flavor != flavor || resource.FlavorRef == nil || resource.FlavorRef.Name != flavor ||
			resource.FlavorRef.Kind != "ResourceFlavor" || resource.FlavorRef.Group != "kueue.x-k8s.io") {
			t.Fatalf("resource %q flavor = %q/%+v, want %q", name, resource.Flavor, resource.FlavorRef, flavor)
		}
		if resource.Usage != usage {
			t.Fatalf("resource %q usage = %q, want %q", name, resource.Usage, usage)
		}
		return
	}
	t.Fatalf("missing resource assignment %q in %+v", name, resources)
}

func loadFixture(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	jsonData, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatal(err)
	}
	obj, _, err := unstructured.UnstructuredJSONScheme.Decode(jsonData, nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, ok := obj.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("fixture decoded as %T", obj)
	}
	return u
}
