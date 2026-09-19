package executioninsight

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func TestForResourceRequiresExactJobSetGVK(t *testing.T) {
	for _, test := range []struct {
		name       string
		apiVersion string
		kind       string
	}{
		{name: "supported", apiVersion: "jobset.x-k8s.io/v1alpha2", kind: "JobSet"},
		{name: "future version", apiVersion: "jobset.x-k8s.io/v1", kind: "JobSet"},
		{name: "same kind other group", apiVersion: "example.io/v1alpha2", kind: "JobSet"},
		{name: "same group other kind", apiVersion: "jobset.x-k8s.io/v1alpha2", kind: "Job"},
	} {
		t.Run(test.name, func(t *testing.T) {
			obj := newJobSet(test.apiVersion, test.kind)
			got := ForResource(obj, resourcecontext.TierBasic)
			if test.name == "supported" && got == nil {
				t.Fatal("supported GVK returned nil")
			}
			if test.name != "supported" && got != nil {
				t.Fatalf("unsupported GVK returned %+v", got)
			}
		})
	}
}

func TestForResourceAggregatesObservedRolesAcrossReconcileGap(t *testing.T) {
	got := ForResource(loadFixture(t, "partial-running.yaml"), resourcecontext.TierBasic)
	if got == nil {
		t.Fatal("execution summary is nil")
	}
	if got.Phase != resourcecontext.ExecutionActive {
		t.Fatalf("phase = %q, want active", got.Phase)
	}
	if got.PrimaryCondition != nil {
		t.Fatalf("running state should not invent a condition: %+v", got.PrimaryCondition)
	}
	if got.JobSet.DeclaredRoles != 3 || got.JobSet.DeclaredJobs != 7 {
		t.Fatalf("declared counts = %+v, want 3 roles / 7 Jobs", got.JobSet)
	}
	assertInt64Pointer(t, "observedRoles", got.JobSet.ObservedRoles, 2)
	if got.JobSet.Jobs.Ready != 3 {
		t.Fatalf("unexpected child-Job count: %d", got.JobSet.Jobs.Ready)
	}
	if got.JobSet.Jobs.Active != 4 {
		t.Fatalf("unexpected child-Job count: %d", got.JobSet.Jobs.Active)
	}
	if got.JobSet.Jobs.Succeeded != 0 {
		t.Fatalf("unexpected child-Job count: %d", got.JobSet.Jobs.Succeeded)
	}
	if got.JobSet.Jobs.Failed != 1 {
		t.Fatalf("unexpected child-Job count: %d", got.JobSet.Jobs.Failed)
	}
	if got.JobSet.Jobs.Suspended != 0 {
		t.Fatalf("unexpected child-Job count: %d", got.JobSet.Jobs.Suspended)
	}
	if got.JobSet.Restarts == nil {
		t.Fatal("restart summary is nil")
	}
	assertInt64Pointer(t, "globalRestarts", got.JobSet.Restarts.Global, 2)
	assertInt64Pointer(t, "global restarts counted", got.JobSet.Restarts.GlobalCountTowardsMax, 0)
	assertInt64Pointer(t, "unmaterialized individual restarts", got.JobSet.Restarts.Individual, 0)
	assertInt64Pointer(t, "unmaterialized counted individual restarts", got.JobSet.Restarts.IndividualCountTowardsMax, 0)
}

func TestFailedChildJobIsNotTerminalExecutionFailure(t *testing.T) {
	obj := newJobSet("jobset.x-k8s.io/v1alpha2", "JobSet")
	obj.Object["spec"] = map[string]any{"replicatedJobs": []any{
		map[string]any{"name": "workers", "replicas": int64(2)},
	}}
	obj.Object["status"] = map[string]any{"replicatedJobsStatus": []any{
		map[string]any{
			"name": "workers", "ready": int64(0), "active": int64(0),
			"succeeded": int64(0), "failed": int64(1), "suspended": int64(0),
		},
	}}

	got := ForResource(obj, resourcecontext.TierBasic)
	if got.Phase != resourcecontext.ExecutionActive {
		t.Fatalf("phase = %q, want active; a child failure may be recoverable by JobSet policy", got.Phase)
	}
	if got.JobSet.Jobs.Failed != 1 {
		t.Fatalf("unexpected child-Job count: %d", got.JobSet.Jobs.Failed)
	}
}

func TestObservedZeroDiffersFromUnreportedStatus(t *testing.T) {
	unreported := newJobSet("jobset.x-k8s.io/v1alpha2", "JobSet")
	unreported.Object["spec"] = map[string]any{"replicatedJobs": []any{
		map[string]any{"name": "workers"},
	}}
	unreportedSummary := ForResource(unreported, resourcecontext.TierBasic)
	if unreportedSummary.Phase != resourcecontext.ExecutionUnknown {
		t.Fatalf("unreported phase = %q, want unknown", unreportedSummary.Phase)
	}
	if unreportedSummary.JobSet.DeclaredJobs != 1 {
		t.Fatalf("documented default replicas = %d, want 1", unreportedSummary.JobSet.DeclaredJobs)
	}
	if unreportedSummary.JobSet.ObservedRoles != nil || unreportedSummary.JobSet.Jobs != nil {
		t.Fatalf("unreported status became observed zero: %+v", unreportedSummary.JobSet)
	}

	observed := unreported.DeepCopy()
	observed.Object["status"] = map[string]any{"replicatedJobsStatus": []any{
		map[string]any{
			"name": "workers", "ready": int64(0), "active": int64(0),
			"succeeded": int64(0), "failed": int64(0), "suspended": int64(0),
		},
	}}
	observedSummary := ForResource(observed, resourcecontext.TierBasic)
	if observedSummary.Phase != resourcecontext.ExecutionPending {
		t.Fatalf("observed phase = %q, want pending", observedSummary.Phase)
	}
	assertInt64Pointer(t, "observedRoles", observedSummary.JobSet.ObservedRoles, 1)
	if observedSummary.JobSet.Jobs.Active != 0 {
		t.Fatalf("unexpected child-Job count: %d", observedSummary.JobSet.Jobs.Active)
	}
}

func TestRestartCountersNeedObservedStatus(t *testing.T) {
	obj := newJobSet("jobset.x-k8s.io/v1alpha2", "JobSet")
	if got := ForResource(obj, resourcecontext.TierBasic); got.JobSet.Restarts != nil {
		t.Fatalf("unobserved JobSet has restart counts: %+v", got.JobSet.Restarts)
	}
	obj.Object["status"] = map[string]any{"restarts": int64(0)}
	got := ForResource(obj, resourcecontext.TierBasic)
	assertInt64Pointer(t, "global", got.JobSet.Restarts.Global, 0)
	assertInt64Pointer(t, "global counted", got.JobSet.Restarts.GlobalCountTowardsMax, 0)
	obj.Object["status"].(map[string]any)["restartsCountTowardsMax"] = int64(2)
	got = ForResource(obj, resourcecontext.TierBasic)
	assertInt64Pointer(t, "explicit global counted", got.JobSet.Restarts.GlobalCountTowardsMax, 2)
}

func TestIndividualRestartAggregateIncludesImplicitZeroRoles(t *testing.T) {
	obj := loadFixture(t, "partial-running.yaml")
	statuses := obj.Object["status"].(map[string]any)["replicatedJobsStatus"].([]any)
	statuses[0].(map[string]any)["jobRestarts"] = []any{int64(2)}
	statuses[0].(map[string]any)["jobRestartsCountTowardsMax"] = []any{int64(1)}

	got := ForResource(obj, resourcecontext.TierBasic)
	if got.JobSet.Restarts == nil {
		t.Fatal("restart summary is nil")
	}
	assertInt64Pointer(t, "individual restarts", got.JobSet.Restarts.Individual, 2)
	assertInt64Pointer(t, "individual counted restarts", got.JobSet.Restarts.IndividualCountTowardsMax, 1)
	assertInt64Pointer(t, "observed roles", got.JobSet.ObservedRoles, 2)
}

func TestJobSetPhasePrecedence(t *testing.T) {
	for _, test := range []struct {
		name, terminalState string
		suspend             bool
		conditions          []any
		active, ready       int64
		want                resourcecontext.ExecutionPhase
		outcome             resourcecontext.ExecutionOutcome
		primary             string
	}{
		{name: "terminal failure wins", terminalState: "Failed", suspend: true, active: 2, want: resourcecontext.ExecutionFinished, outcome: resourcecontext.ExecutionFailed},
		{name: "terminal completion with active cleanup", terminalState: "Completed", active: 2, want: resourcecontext.ExecutionFinished, outcome: resourcecontext.ExecutionSucceeded},
		{name: "unknown native terminal does not fall through", terminalState: "FutureTerminal", active: 2, conditions: []any{condition("Failed", "True", "Failure")}, want: resourcecontext.ExecutionUnknown},
		{name: "failed condition", conditions: []any{condition("Failed", "True", "ReachedMaxRestarts")}, active: 2, want: resourcecontext.ExecutionFinished, outcome: resourcecontext.ExecutionFailed, primary: "Failed"},
		{name: "completed condition", conditions: []any{condition("Completed", "True", "AllJobsCompleted")}, want: resourcecontext.ExecutionFinished, outcome: resourcecontext.ExecutionSucceeded, primary: "Completed"},
		{name: "observed suspension during resume lag", conditions: []any{condition("Suspended", "True", "SuspendedJobs")}, active: 2, want: resourcecontext.ExecutionSuspended, primary: "Suspended"},
		{name: "in-order resume progresses with retained suspension", conditions: []any{condition("Suspended", "True", "SuspendedJobs"), condition("StartupPolicyInProgress", "True", "InOrderStartupPolicyInProgress")}, active: 1, want: resourcecontext.ExecutionActive, primary: "StartupPolicyInProgress"},
		{name: "observed suspension beats retained startup when requested", suspend: true, conditions: []any{condition("Suspended", "True", "SuspendedJobs"), condition("StartupPolicyInProgress", "True", "InOrderStartupPolicyInProgress")}, want: resourcecontext.ExecutionSuspended, primary: "Suspended"},
		{name: "suspend request does not override observed restart", suspend: true, conditions: []any{condition("RestartingJobSet", "True", "FailurePolicy_retry")}, active: 2, want: resourcecontext.ExecutionActive, primary: "RestartingJobSet"},
		{name: "suspend request does not override active", suspend: true, active: 2, want: resourcecontext.ExecutionActive},
		{name: "suspend request does not override pending", suspend: true, want: resourcecontext.ExecutionPending},
		{name: "startup evidence", conditions: []any{condition("StartupPolicyInProgress", "True", "InOrderStartupPolicyInProgress")}, active: 1, want: resourcecontext.ExecutionActive, primary: "StartupPolicyInProgress"},
		{name: "active without ready", active: 1, want: resourcecontext.ExecutionActive},
		{name: "ready without active is not proof of running Pods", ready: 1, want: resourcecontext.ExecutionActive},
		{name: "false terminal condition", conditions: []any{condition("Failed", "False", "Recovered")}, active: 1, want: resourcecontext.ExecutionActive},
		{name: "unknown suspension condition", conditions: []any{condition("Suspended", "Unknown", "Reconciling")}, want: resourcecontext.ExecutionPending},
	} {
		t.Run(test.name, func(t *testing.T) {
			obj := jobSetWithObservedCounts(test.active, test.ready)
			obj.Object["spec"].(map[string]any)["suspend"] = test.suspend
			status := obj.Object["status"].(map[string]any)
			status["terminalState"] = test.terminalState
			status["conditions"] = test.conditions
			got := ForResource(obj, resourcecontext.TierBasic)
			if got.Phase != test.want || got.Outcome != test.outcome {
				t.Fatalf("phase/outcome = %s/%s, want %s/%s", got.Phase, got.Outcome, test.want, test.outcome)
			}
			if (got.Phase == resourcecontext.ExecutionFinished) != (got.Outcome != "") {
				t.Fatalf("invalid terminal invariant: %+v", got)
			}
			if got.NativeState != test.terminalState {
				t.Fatalf("native state = %q", got.NativeState)
			}
			if got.SuspendRequested == nil || *got.SuspendRequested != test.suspend {
				t.Fatalf("lost requested suspension: %+v", got.SuspendRequested)
			}
			if test.primary == "" {
				if got.PrimaryCondition != nil {
					t.Fatalf("invented condition: %+v", got.PrimaryCondition)
				}
			} else if got.PrimaryCondition == nil || got.PrimaryCondition.Type != test.primary {
				t.Fatalf("primary condition = %+v", got.PrimaryCondition)
			}
		})
	}
}

func TestUnreportedActivityRemainsUnknown(t *testing.T) {
	for _, status := range []map[string]any{nil, {}, {"restarts": int64(0)}} {
		obj := newJobSet("jobset.x-k8s.io/v1alpha2", "JobSet")
		obj.Object["spec"] = map[string]any{"suspend": true}
		if status != nil {
			obj.Object["status"] = status
		}
		got := ForResource(obj, resourcecontext.TierBasic)
		if got.Phase != resourcecontext.ExecutionUnknown || got.Outcome != "" || got.JobSet.Jobs != nil || got.SuspendRequested == nil || !*got.SuspendRequested {
			t.Fatalf("unreported activity became known: %+v", got)
		}
	}
}

func TestGenerationAndDeletionPreserveReportedEvidence(t *testing.T) {
	obj := loadFixture(t, "terminal-failed.yaml")
	obj.SetGeneration(4)
	obj.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["observedGeneration"] = int64(3)
	got := ForResource(obj, resourcecontext.TierBasic)
	if got.SubjectGeneration != 4 || got.PrimaryCondition.ObservedGeneration != 3 || got.Outcome != resourcecontext.ExecutionFailed {
		t.Fatalf("generation evidence lost: %+v", got)
	}
	before := ForResource(jobSetWithObservedCounts(1, 0), resourcecontext.TierBasic)
	deleting := jobSetWithObservedCounts(1, 0)
	now := metav1.Now()
	deleting.SetDeletionTimestamp(&now)
	if after := ForResource(deleting, resourcecontext.TierBasic); !reflect.DeepEqual(before, after) {
		t.Fatalf("deletion invented execution state: before=%+v after=%+v", before, after)
	}
}

func TestAuthoritativeConditionDetailIsTieredAndBounded(t *testing.T) {
	obj := loadFixture(t, "terminal-failed.yaml")
	basic := ForResource(obj, resourcecontext.TierBasic)
	diagnostic := ForResource(obj, resourcecontext.TierDiagnostic)

	if basic.PrimaryCondition == nil || basic.PrimaryCondition.Type != "Failed" || basic.PrimaryCondition.Status != "True" || basic.PrimaryCondition.Reason != "ReachedMaxRestarts" {
		t.Fatalf("basic state lost authoritative condition/reason: %+v", basic.PrimaryCondition)
	}
	if basic.PrimaryCondition.Message != "" || basic.PrimaryCondition.LastTransitionTime != "" {
		t.Fatalf("basic tier leaked diagnostic detail: %+v", basic.PrimaryCondition)
	}
	if diagnostic.PrimaryCondition == nil || !strings.Contains(diagnostic.PrimaryCondition.Message, "restart limit") || diagnostic.PrimaryCondition.LastTransitionTime != "2026-08-31T10:15:00Z" {
		t.Fatalf("diagnostic state missing message/time: %+v", diagnostic.PrimaryCondition)
	}
	if diagnostic.JobSet.Restarts == nil {
		t.Fatal("restart summary is nil")
	}
	assertInt64Pointer(t, "global restarts", diagnostic.JobSet.Restarts.Global, 0)
	assertInt64Pointer(t, "global restarts counted", diagnostic.JobSet.Restarts.GlobalCountTowardsMax, 0)
	assertInt64Pointer(t, "individual restarts", diagnostic.JobSet.Restarts.Individual, 3)
	assertInt64Pointer(t, "individual restarts counted", diagnostic.JobSet.Restarts.IndividualCountTowardsMax, 2)

	long := strings.Repeat("界", 300)
	obj.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["message"] = long
	diagnostic = ForResource(obj, resourcecontext.TierDiagnostic)
	if len(diagnostic.PrimaryCondition.Message) > maxStateMessageBytes || !strings.HasSuffix(diagnostic.PrimaryCondition.Message, "…") {
		t.Fatalf("diagnostic message was not UTF-8 safely bounded: %d bytes, %q", len(diagnostic.PrimaryCondition.Message), diagnostic.PrimaryCondition.Message)
	}
}

func TestExecutionSummaryOutputBudget(t *testing.T) {
	obj := loadFixture(t, "terminal-failed.yaml")
	obj.SetGeneration(1)
	obj.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["message"] = strings.Repeat("controller evidence ", 1000)

	for _, test := range []struct {
		name   string
		tier   resourcecontext.ContextTier
		budget int
	}{
		{name: "basic", tier: resourcecontext.TierBasic, budget: 500},
		{name: "diagnostic", tier: resourcecontext.TierDiagnostic, budget: 900},
	} {
		t.Run(test.name, func(t *testing.T) {
			wire, err := json.Marshal(resourcecontext.ResourceContext{
				Tier:      test.tier,
				Execution: ForResource(obj, test.tier),
			})
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			if len(wire) > test.budget {
				t.Fatalf("wire size = %d bytes, budget = %d: %s", len(wire), test.budget, wire)
			}
		})
	}
}

func newJobSet(apiVersion, kind string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "ml",
		},
	}}
}

func jobSetWithObservedCounts(active, ready int64) *unstructured.Unstructured {
	obj := newJobSet("jobset.x-k8s.io/v1alpha2", "JobSet")
	obj.Object["spec"] = map[string]any{"replicatedJobs": []any{
		map[string]any{"name": "workers", "replicas": int64(2)},
	}}
	obj.Object["status"] = map[string]any{"replicatedJobsStatus": []any{
		map[string]any{
			"name": "workers", "ready": ready, "active": active,
			"succeeded": int64(0), "failed": int64(0), "suspended": int64(0),
		},
	}}
	return obj
}

func condition(conditionType, status, reason string) map[string]any {
	return map[string]any{"type": conditionType, "status": status, "reason": reason}
}

func loadFixture(t *testing.T, name string) *unstructured.Unstructured {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	jsonBytes, err := yaml.YAMLToJSON(raw)
	if err != nil {
		t.Fatalf("convert fixture to JSON: %v", err)
	}
	decoded, _, err := unstructured.UnstructuredJSONScheme.Decode(jsonBytes, nil, nil)
	if err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	obj, ok := decoded.(*unstructured.Unstructured)
	if !ok {
		t.Fatalf("fixture decoded as %T", decoded)
	}
	return obj
}

func assertInt64Pointer(t *testing.T, field string, got *int64, want int64) {
	t.Helper()
	if got == nil || *got != want {
		t.Fatalf("%s = %v, want %d", field, got, want)
	}
}
