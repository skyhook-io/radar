package executioninsight

import (
	"encoding/json"
	"strings"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

func TestForResourceRequiresExactRayServiceGVK(t *testing.T) {
	for _, test := range []struct {
		name       string
		apiVersion string
		kind       string
	}{
		{name: "supported", apiVersion: "ray.io/v1", kind: "RayService"},
		{name: "deprecated version", apiVersion: "ray.io/v1alpha1", kind: "RayService"},
		{name: "same kind other group", apiVersion: "example.io/v1", kind: "RayService"},
		{name: "same group other kind", apiVersion: "ray.io/v1", kind: "RayCluster"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := ForResource(newRayService(test.apiVersion, test.kind), resourcecontext.TierBasic)
			if test.name == "supported" && got == nil {
				t.Fatal("supported GVK returned nil")
			}
			if test.name != "supported" && got != nil {
				t.Fatalf("unsupported GVK returned %+v", got)
			}
		})
	}
}

func TestRayServiceStagePrecedence(t *testing.T) {
	for _, test := range []struct {
		name       string
		suspend    bool
		conditions []any
		want       resourcecontext.ExecutionStage
		wantState  string
	}{
		{
			name: "validation failure wins over stale persisted suspended",
			conditions: []any{
				condition("Suspended", "True", "SuspendComplete"),
				condition("Ready", "False", "ValidationFailed"),
			},
			want: resourcecontext.ExecutionFailed, wantState: "Ready",
		},
		{
			name: "initializing timeout wins over stale persisted suspending",
			conditions: []any{
				condition("Suspending", "True", "SuspendInProgress"),
				condition("Ready", "False", "InitializingTimeout"),
			},
			want: resourcecontext.ExecutionFailed, wantState: "Ready",
		},
		{
			name:       "persisted suspended state",
			conditions: []any{condition("Suspended", "True", "SuspendComplete")},
			want:       resourcecontext.ExecutionSuspended, wantState: "Suspended",
		},
		{
			name:       "persisted suspending state",
			conditions: []any{condition("Suspending", "True", "SuspendInProgress")},
			want:       resourcecontext.ExecutionSuspended, wantState: "Suspending",
		},
		{
			name: "validation failure wins over bare suspend intent", suspend: true,
			conditions: []any{condition("Ready", "False", "ValidationFailed")},
			want:       resourcecontext.ExecutionFailed, wantState: "Ready",
		},
		{
			name: "initializing timeout wins over bare suspend intent", suspend: true,
			conditions: []any{condition("Ready", "False", "InitializingTimeout")},
			want:       resourcecontext.ExecutionFailed, wantState: "Ready",
		},
		{
			name: "bare suspend intent wins over stale rollout state", suspend: true,
			conditions: []any{condition("UpgradeInProgress", "True", "BothActivePendingClustersExist")},
			want:       resourcecontext.ExecutionSuspended,
		},
		{
			name: "rollback wins over upgrade and ready",
			conditions: []any{
				condition("Ready", "True", "NonZeroServeEndpoints"),
				condition("UpgradeInProgress", "True", "BothActivePendingClustersExist"),
				condition("RollbackInProgress", "True", "DesiredClusterSpecChanged"),
			},
			want: resourcecontext.ExecutionUpdating, wantState: "RollbackInProgress",
		},
		{
			name: "upgrade wins over ready",
			conditions: []any{
				condition("Ready", "True", "NonZeroServeEndpoints"),
				condition("UpgradeInProgress", "True", "BothActivePendingClustersExist"),
			},
			want: resourcecontext.ExecutionUpdating, wantState: "UpgradeInProgress",
		},
		{
			name:       "ready service is running",
			conditions: []any{condition("Ready", "True", "NonZeroServeEndpoints")},
			want:       resourcecontext.ExecutionRunning, wantState: "Ready",
		},
		{
			name:       "initializing service is starting",
			conditions: []any{condition("Ready", "False", "Initializing")},
			want:       resourcecontext.ExecutionStarting, wantState: "Ready",
		},
		{
			name:       "zero endpoints is pending",
			conditions: []any{condition("Ready", "False", "ZeroServeEndpoints")},
			want:       resourcecontext.ExecutionPending, wantState: "Ready",
		},
		{
			name:       "unknown ready state is pending",
			conditions: []any{condition("Ready", "Unknown", "NoActiveCluster")},
			want:       resourcecontext.ExecutionPending, wantState: "Ready",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			obj := newRayService("ray.io/v1", "RayService")
			obj.Object["spec"] = map[string]any{"suspend": test.suspend}
			obj.Object["status"] = map[string]any{"conditions": test.conditions}
			got := ForResource(obj, resourcecontext.TierBasic)
			if got.Stage != test.want {
				t.Fatalf("stage = %q, want %q", got.Stage, test.want)
			}
			if test.wantState == "" {
				if got.State != nil {
					t.Fatalf("state = %+v, want nil", got.State)
				}
			} else if got.State == nil || got.State.Condition != test.wantState {
				t.Fatalf("state = %+v, want condition %q", got.State, test.wantState)
			}
		})
	}
}

func TestRayServiceDoesNotUseDeprecatedStateFallbacks(t *testing.T) {
	obj := newRayService("ray.io/v1", "RayService")
	obj.Object["status"] = map[string]any{
		"serviceStatus": "Running",
		"activeServiceStatus": map[string]any{
			"rayClusterName":   "service-active",
			"rayClusterStatus": map[string]any{"state": "failed"},
		},
	}
	got := ForResource(obj, resourcecontext.TierBasic)
	if got.Stage != resourcecontext.ExecutionPending {
		t.Fatalf("stage = %q, want pending without an authoritative condition", got.Stage)
	}
	if got.Runtimes == nil || got.Runtimes.Active == nil {
		t.Fatal("reported active runtime was omitted")
	}
	if got.Runtimes.Active.RuntimeState != nil {
		t.Fatalf("deprecated RayCluster state became runtime evidence: %+v", got.Runtimes.Active.RuntimeState)
	}

	withoutStatus := newRayService("ray.io/v1", "RayService")
	if stage := ForResource(withoutStatus, resourcecontext.TierBasic).Stage; stage != resourcecontext.ExecutionSubmitted {
		t.Fatalf("stage without status = %q, want submitted", stage)
	}
}

func TestRayServiceProjectsSeparateControllerAndRuntimeEvidence(t *testing.T) {
	obj := loadFixture(t, "rayservice-rollback.yaml")
	basic := ForResource(obj, resourcecontext.TierBasic)
	diagnostic := ForResource(obj, resourcecontext.TierDiagnostic)

	if basic.Controller != "rayservice" || basic.Stage != resourcecontext.ExecutionUpdating {
		t.Fatalf("summary = %+v, want rayservice/updating", basic)
	}
	if basic.State == nil || basic.State.Condition != "RollbackInProgress" || basic.State.Reason != "DesiredClusterSpecChanged" {
		t.Fatalf("controller state = %+v, want rollback condition", basic.State)
	}
	if basic.Counts != nil || basic.Restarts != nil {
		t.Fatalf("RayService invented execution aggregates: counts=%+v restarts=%+v", basic.Counts, basic.Restarts)
	}
	if basic.Runtimes == nil || basic.Runtimes.Active == nil || basic.Runtimes.Pending == nil {
		t.Fatalf("runtime slots = %+v, want active and pending", basic.Runtimes)
	}

	active := basic.Runtimes.Active
	if active.ClusterName != "image-service-raycluster-old" {
		t.Fatalf("active cluster = %q", active.ClusterName)
	}
	if active.RuntimeState == nil || active.RuntimeState.Condition != "ReplicaFailure" || active.RuntimeState.Reason != "FailedCreateWorkerPod" {
		t.Fatalf("active runtime state = %+v, want native replica failure", active.RuntimeState)
	}
	assertInt64Pointer(t, "active target capacity", active.TargetCapacityPercent, 100)
	assertInt64Pointer(t, "active traffic", active.TrafficRoutedPercent, 65)

	pending := basic.Runtimes.Pending
	if pending.ClusterName != "image-service-raycluster-new" {
		t.Fatalf("pending cluster = %q", pending.ClusterName)
	}
	if pending.RuntimeState == nil || pending.RuntimeState.Condition != "HeadPodReady" || pending.RuntimeState.Status != "False" {
		t.Fatalf("pending runtime state = %+v, want not-ready head", pending.RuntimeState)
	}
	assertInt64Pointer(t, "pending target capacity", pending.TargetCapacityPercent, 0)
	assertInt64Pointer(t, "pending traffic", pending.TrafficRoutedPercent, 0)

	if basic.State.Message != "" || active.RuntimeState.Message != "" || pending.RuntimeState.Message != "" {
		t.Fatalf("basic tier leaked diagnostic messages: %+v", basic)
	}
	if diagnostic.State == nil || !strings.Contains(diagnostic.State.Message, "returning") ||
		diagnostic.State.LastTransitionTime != "2026-08-31T11:10:00Z" {
		t.Fatalf("diagnostic controller state = %+v", diagnostic.State)
	}
	if diagnostic.Runtimes.Active == nil || !strings.Contains(diagnostic.Runtimes.Active.RuntimeState.Message, "worker Pod") {
		t.Fatalf("diagnostic active runtime state = %+v", diagnostic.Runtimes.Active)
	}
}

func TestRayClusterConditionPrecedenceUsesOnlyNativeConditions(t *testing.T) {
	for _, test := range []struct {
		name       string
		conditions map[string]executionCondition
		want       string
	}{
		{
			name: "failure before suspension and readiness",
			conditions: map[string]executionCondition{
				"ReplicaFailure":      {Type: "ReplicaFailure", Status: "True"},
				"RayClusterSuspended": {Type: "RayClusterSuspended", Status: "True"},
				"HeadPodReady":        {Type: "HeadPodReady", Status: "True"},
			},
			want: "ReplicaFailure",
		},
		{
			name: "suspended before not ready",
			conditions: map[string]executionCondition{
				"RayClusterSuspended": {Type: "RayClusterSuspended", Status: "True"},
				"HeadPodReady":        {Type: "HeadPodReady", Status: "False"},
			},
			want: "RayClusterSuspended",
		},
		{
			name: "not ready before provisioned",
			conditions: map[string]executionCondition{
				"HeadPodReady":          {Type: "HeadPodReady", Status: "False"},
				"RayClusterProvisioned": {Type: "RayClusterProvisioned", Status: "True"},
			},
			want: "HeadPodReady",
		},
		{
			name: "ready before provisioned",
			conditions: map[string]executionCondition{
				"HeadPodReady":          {Type: "HeadPodReady", Status: "True"},
				"RayClusterProvisioned": {Type: "RayClusterProvisioned", Status: "True"},
			},
			want: "HeadPodReady",
		},
		{
			name: "false failure is not evidence",
			conditions: map[string]executionCondition{
				"ReplicaFailure": {Type: "ReplicaFailure", Status: "False"},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, ok := selectRayClusterCondition(test.conditions)
			if test.want == "" {
				if ok {
					t.Fatalf("selected %+v, want no high-signal condition", got)
				}
				return
			}
			if !ok || got.Type != test.want {
				t.Fatalf("selected %+v, want %q", got, test.want)
			}
		})
	}
}

func TestRayServiceOmitsUnnamedAndUnreportedRuntimeFacts(t *testing.T) {
	obj := newRayService("ray.io/v1", "RayService")
	obj.Object["status"] = map[string]any{
		"conditions": []any{condition("Ready", "False", "Initializing")},
		"activeServiceStatus": map[string]any{
			"rayClusterName": "active",
		},
		"pendingServiceStatus": map[string]any{
			"targetCapacity":       int64(0),
			"trafficRoutedPercent": int64(0),
		},
	}
	got := ForResource(obj, resourcecontext.TierBasic)
	if got.Runtimes == nil || got.Runtimes.Active == nil {
		t.Fatal("named active runtime was omitted")
	}
	if got.Runtimes.Pending != nil {
		t.Fatalf("unnamed pending status became a runtime: %+v", got.Runtimes.Pending)
	}
	if got.Runtimes.Active.TargetCapacityPercent != nil || got.Runtimes.Active.TrafficRoutedPercent != nil {
		t.Fatalf("unreported percentages became zero: %+v", got.Runtimes.Active)
	}
}

func TestRayServiceRetainsNamedPartialRuntimeWithoutInventingState(t *testing.T) {
	obj := newRayService("ray.io/v1", "RayService")
	obj.Object["status"] = map[string]any{
		"conditions": []any{
			condition("Ready", "True", "NonZeroServeEndpoints"),
			condition("UpgradeInProgress", "True", "BothActivePendingClustersExist"),
		},
		"activeServiceStatus": map[string]any{
			"rayClusterName": "serve-old",
			"rayClusterStatus": map[string]any{
				"conditions": []any{condition("HeadPodReady", "True", "HeadPodReady")},
			},
		},
		"pendingServiceStatus": map[string]any{
			"rayClusterName": "serve-new",
		},
	}

	got := ForResource(obj, resourcecontext.TierBasic)
	if got.Stage != resourcecontext.ExecutionUpdating {
		t.Fatalf("stage = %q, want updating", got.Stage)
	}
	if got.Runtimes == nil || got.Runtimes.Active == nil || got.Runtimes.Pending == nil {
		t.Fatalf("runtime slots = %+v, want both reported names", got.Runtimes)
	}
	if got.Runtimes.Active.RuntimeState == nil || got.Runtimes.Active.RuntimeState.Condition != "HeadPodReady" {
		t.Fatalf("active runtime state = %+v", got.Runtimes.Active.RuntimeState)
	}
	if got.Runtimes.Pending.ClusterName != "serve-new" || got.Runtimes.Pending.RuntimeState != nil {
		t.Fatalf("partial pending runtime = %+v, want name with state unavailable", got.Runtimes.Pending)
	}
}

func TestRayServiceExecutionSummaryOutputBudget(t *testing.T) {
	obj := loadFixture(t, "rayservice-rollback.yaml")
	long := strings.Repeat("controller evidence ", 1000)
	status := obj.Object["status"].(map[string]any)
	for _, raw := range status["conditions"].([]any) {
		raw.(map[string]any)["message"] = long
	}
	for _, slot := range []string{"activeServiceStatus", "pendingServiceStatus"} {
		runtimeStatus := status[slot].(map[string]any)["rayClusterStatus"].(map[string]any)
		for _, raw := range runtimeStatus["conditions"].([]any) {
			raw.(map[string]any)["message"] = long
		}
	}

	for _, test := range []struct {
		name   string
		tier   resourcecontext.ContextTier
		budget int
	}{
		{name: "basic", tier: resourcecontext.TierBasic, budget: 700},
		{name: "diagnostic", tier: resourcecontext.TierDiagnostic, budget: 1600},
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

func newRayService(apiVersion, kind string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata": map[string]any{
			"name":      "test",
			"namespace": "ml",
		},
	}}
}
