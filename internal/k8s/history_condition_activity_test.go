package k8s

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/timeline"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestComputeDiff_ConditionCapacityActivity(t *testing.T) {
	nodeClaim := func(conditions [][3]string) *unstructured.Unstructured {
		var items []any
		for _, condition := range conditions {
			items = append(items, map[string]any{
				"type": condition[0], "status": condition[1], "reason": condition[2],
			})
		}
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "karpenter.sh/v1", "kind": "NodeClaim",
			"status": map[string]any{"conditions": items},
		}}
	}
	tests := []struct {
		name       string
		conditions [][3]string
		wantType   capacityapi.ActivityType
		wantState  capacityapi.ActivityState
		wantReason string
	}{
		{"ready", [][3]string{{"Ready", "True", "Ready"}}, capacityapi.ActivityProvision, capacityapi.ActivityCompleted, "nodeclaim_ready"},
		{"launch failure", [][3]string{{"Launched", "Unknown", "InsufficientInstanceCapacity"}}, capacityapi.ActivityLaunchFailure, capacityapi.ActivityFailed, "launch_failed"},
		{"registration failure", [][3]string{{"Registered", "Unknown", "RegistrationFailed"}}, capacityapi.ActivityRegistrationFailure, capacityapi.ActivityFailed, "registration_failed"},
		{"initialization failure", [][3]string{{"Initialized", "Unknown", "InitializationFailed"}}, capacityapi.ActivityInitializationFailure, capacityapi.ActivityFailed, "initialization_failed"},
		{"ready failure", [][3]string{{"Ready", "Unknown", "LaunchFailed"}}, capacityapi.ActivityProvision, capacityapi.ActivityFailed, "nodeclaim_not_ready"},
		{"still provisioning", [][3]string{{"Ready", "False", "NotInitialized"}}, "", "", ""},
		{"specific stage wins", [][3]string{{"Ready", "Unknown", "RegistrationFailed"}, {"Registered", "Unknown", "RegistrationFailed"}}, capacityapi.ActivityRegistrationFailure, capacityapi.ActivityFailed, "registration_failed"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			diff := ComputeDiffFromUnstructured("NodeClaim",
				nodeClaim([][3]string{{"Ready", "False", "Pending"}}), nodeClaim(tt.conditions))
			if diff == nil {
				t.Fatal("condition update produced no diff")
			}
			records := capacity.BuildActivityRecords([]timeline.TimelineEvent{{
				ID: "condition-update", Seq: 1, Timestamp: time.Now().UTC(), Source: timeline.SourceInformer,
				Kind: "NodeClaim", APIVersion: "karpenter.sh/v1", Name: "claim", UID: "claim-uid",
				EventType: timeline.EventTypeUpdate, Diff: diff,
			}})
			if tt.wantType == "" {
				if len(records) != 0 {
					t.Fatalf("non-failure condition produced activity: %+v", records)
				}
				return
			}
			if len(records) != 1 {
				t.Fatalf("condition produced %d activities, want 1: %+v", len(records), records)
			}
			got := records[0].Episode
			if got.Type != tt.wantType || got.State != tt.wantState || got.PrimaryReasonCode != tt.wantReason {
				t.Fatalf("condition activity = %s/%s/%s, want %s/%s/%s",
					got.Type, got.State, got.PrimaryReasonCode, tt.wantType, tt.wantState, tt.wantReason)
			}
		})
	}
}
