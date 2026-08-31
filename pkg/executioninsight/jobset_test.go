package executioninsight

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

func TestForResourceAggregatesPartialObservedStatusWithoutInventingCoverage(t *testing.T) {
	got := ForResource(loadFixture(t, "partial-running.yaml"), resourcecontext.TierBasic)
	if got == nil {
		t.Fatal("execution summary is nil")
	}
	if got.Stage != resourcecontext.ExecutionRunning {
		t.Fatalf("stage = %q, want running", got.Stage)
	}
	if got.State != nil {
		t.Fatalf("running state should not invent a condition: %+v", got.State)
	}
	if got.Counts.DeclaredRoles != 3 || got.Counts.DeclaredJobs != 7 {
		t.Fatalf("declared counts = %+v, want 3 roles / 7 Jobs", got.Counts)
	}
	assertInt64Pointer(t, "observedRoles", got.Counts.ObservedRoles, 2)
	assertInt64Pointer(t, "readyJobs", got.Counts.ReadyJobs, 3)
	assertInt64Pointer(t, "activeJobs", got.Counts.ActiveJobs, 4)
	assertInt64Pointer(t, "succeededJobs", got.Counts.SucceededJobs, 0)
	assertInt64Pointer(t, "failedJobs", got.Counts.FailedJobs, 1)
	assertInt64Pointer(t, "suspendedJobs", got.Counts.SuspendedJobs, 0)
	if got.Restarts == nil {
		t.Fatal("restart summary is nil")
	}
	assertInt64Pointer(t, "globalRestarts", got.Restarts.Global, 2)
	if got.Restarts.GlobalCountTowardsMax != nil || got.Restarts.Individual != nil || got.Restarts.IndividualRoles != nil {
		t.Fatalf("unreported restart fields became observed zero: %+v", got.Restarts)
	}
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
	if got.Stage != resourcecontext.ExecutionPending {
		t.Fatalf("stage = %q, want pending; a child failure may be recoverable by JobSet policy", got.Stage)
	}
	assertInt64Pointer(t, "failedJobs", got.Counts.FailedJobs, 1)
}

func TestObservedZeroDiffersFromUnreportedStatus(t *testing.T) {
	unreported := newJobSet("jobset.x-k8s.io/v1alpha2", "JobSet")
	unreported.Object["spec"] = map[string]any{"replicatedJobs": []any{
		map[string]any{"name": "workers"},
	}}
	unreportedSummary := ForResource(unreported, resourcecontext.TierBasic)
	if unreportedSummary.Stage != resourcecontext.ExecutionSubmitted {
		t.Fatalf("unreported stage = %q, want submitted", unreportedSummary.Stage)
	}
	if unreportedSummary.Counts.DeclaredJobs != 1 {
		t.Fatalf("documented default replicas = %d, want 1", unreportedSummary.Counts.DeclaredJobs)
	}
	if unreportedSummary.Counts.ObservedRoles != nil || unreportedSummary.Counts.ActiveJobs != nil {
		t.Fatalf("unreported status became observed zero: %+v", unreportedSummary.Counts)
	}

	observed := unreported.DeepCopy()
	observed.Object["status"] = map[string]any{"replicatedJobsStatus": []any{
		map[string]any{
			"name": "workers", "ready": int64(0), "active": int64(0),
			"succeeded": int64(0), "failed": int64(0), "suspended": int64(0),
		},
	}}
	observedSummary := ForResource(observed, resourcecontext.TierBasic)
	if observedSummary.Stage != resourcecontext.ExecutionPending {
		t.Fatalf("observed stage = %q, want pending", observedSummary.Stage)
	}
	assertInt64Pointer(t, "observedRoles", observedSummary.Counts.ObservedRoles, 1)
	assertInt64Pointer(t, "activeJobs", observedSummary.Counts.ActiveJobs, 0)
}

func TestIndividualRestartAggregateDisclosesRoleCoverage(t *testing.T) {
	obj := loadFixture(t, "partial-running.yaml")
	statuses := obj.Object["status"].(map[string]any)["replicatedJobsStatus"].([]any)
	statuses[0].(map[string]any)["jobRestarts"] = []any{int64(2)}
	statuses[0].(map[string]any)["jobRestartsCountTowardsMax"] = []any{int64(1)}

	got := ForResource(obj, resourcecontext.TierBasic)
	if got.Restarts == nil {
		t.Fatal("restart summary is nil")
	}
	assertInt64Pointer(t, "individual restarts", got.Restarts.Individual, 2)
	assertInt64Pointer(t, "individual restart roles", got.Restarts.IndividualRoles, 1)
	assertInt64Pointer(t, "individual counted restarts", got.Restarts.IndividualCountTowardsMax, 1)
	assertInt64Pointer(t, "individual counted roles", got.Restarts.IndividualCountedRoles, 1)
	assertInt64Pointer(t, "observed roles", got.Counts.ObservedRoles, 2)
	if *got.Restarts.IndividualRoles == *got.Counts.ObservedRoles {
		t.Fatal("fixture should preserve partial per-Job restart coverage")
	}
}

func TestJobSetStagePrecedence(t *testing.T) {
	for _, test := range []struct {
		name          string
		terminalState string
		suspend       bool
		conditions    []any
		active        int64
		ready         int64
		want          resourcecontext.ExecutionStage
	}{
		{name: "terminal failure wins", terminalState: "Failed", suspend: true, active: 2, want: resourcecontext.ExecutionFailed},
		{name: "terminal completion wins", terminalState: "Completed", active: 2, want: resourcecontext.ExecutionCompleted},
		{name: "failed condition", conditions: []any{condition("Failed", "True", "ReachedMaxRestarts")}, active: 2, want: resourcecontext.ExecutionFailed},
		{name: "completed condition", conditions: []any{condition("Completed", "True", "AllJobsCompleted")}, active: 2, want: resourcecontext.ExecutionCompleted},
		{name: "suspended condition", conditions: []any{condition("Suspended", "True", "SuspendedJobs")}, active: 2, want: resourcecontext.ExecutionSuspended},
		{name: "suspend wins over restart", suspend: true, conditions: []any{condition("RestartingJobSet", "True", "FailurePolicy_retry")}, active: 2, want: resourcecontext.ExecutionSuspended},
		{name: "restarting wins over live counts", conditions: []any{condition("RestartingJobSet", "True", "FailurePolicy_retry")}, active: 2, want: resourcecontext.ExecutionRestarting},
		{name: "startup wins over live counts", conditions: []any{condition("StartupPolicyInProgress", "True", "InOrderStartupPolicyInProgress")}, active: 1, want: resourcecontext.ExecutionStarting},
		{name: "active is running", active: 1, want: resourcecontext.ExecutionRunning},
		{name: "ready is running", ready: 1, want: resourcecontext.ExecutionRunning},
	} {
		t.Run(test.name, func(t *testing.T) {
			obj := jobSetWithObservedCounts(test.active, test.ready)
			obj.Object["spec"].(map[string]any)["suspend"] = test.suspend
			status := obj.Object["status"].(map[string]any)
			if test.terminalState != "" {
				status["terminalState"] = test.terminalState
			}
			if len(test.conditions) > 0 {
				status["conditions"] = test.conditions
			}
			got := ForResource(obj, resourcecontext.TierBasic)
			if got.Stage != test.want {
				t.Fatalf("stage = %q, want %q", got.Stage, test.want)
			}
		})
	}
}

func TestAuthoritativeConditionDetailIsTieredAndBounded(t *testing.T) {
	obj := loadFixture(t, "terminal-failed.yaml")
	basic := ForResource(obj, resourcecontext.TierBasic)
	diagnostic := ForResource(obj, resourcecontext.TierDiagnostic)

	if basic.State == nil || basic.State.Condition != "Failed" || basic.State.Status != "True" || basic.State.Reason != "ReachedMaxRestarts" {
		t.Fatalf("basic state lost authoritative condition/reason: %+v", basic.State)
	}
	if basic.State.Message != "" || basic.State.LastTransitionTime != "" {
		t.Fatalf("basic tier leaked diagnostic detail: %+v", basic.State)
	}
	if diagnostic.State == nil || !strings.Contains(diagnostic.State.Message, "restart limit") || diagnostic.State.LastTransitionTime != "2026-08-31T10:15:00Z" {
		t.Fatalf("diagnostic state missing message/time: %+v", diagnostic.State)
	}
	if diagnostic.Restarts == nil {
		t.Fatal("restart summary is nil")
	}
	assertInt64Pointer(t, "global restarts", diagnostic.Restarts.Global, 0)
	assertInt64Pointer(t, "global restarts counted", diagnostic.Restarts.GlobalCountTowardsMax, 0)
	assertInt64Pointer(t, "individual restarts", diagnostic.Restarts.Individual, 3)
	assertInt64Pointer(t, "individual restart roles", diagnostic.Restarts.IndividualRoles, 1)
	assertInt64Pointer(t, "individual restarts counted", diagnostic.Restarts.IndividualCountTowardsMax, 2)
	assertInt64Pointer(t, "individual counted roles", diagnostic.Restarts.IndividualCountedRoles, 1)

	long := strings.Repeat("界", 300)
	obj.Object["status"].(map[string]any)["conditions"].([]any)[0].(map[string]any)["message"] = long
	diagnostic = ForResource(obj, resourcecontext.TierDiagnostic)
	if len(diagnostic.State.Message) > maxStateMessageBytes || !strings.HasSuffix(diagnostic.State.Message, "…") {
		t.Fatalf("diagnostic message was not UTF-8 safely bounded: %d bytes, %q", len(diagnostic.State.Message), diagnostic.State.Message)
	}
}

func TestExecutionSummaryOutputBudget(t *testing.T) {
	obj := loadFixture(t, "terminal-failed.yaml")
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
