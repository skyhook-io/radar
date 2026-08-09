package server

import (
	"fmt"
	"reflect"
	"testing"

	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/subject"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestCapacityProviderDistinguishesSelfManagedAndEKSAuto(t *testing.T) {
	selfManaged := capacityProvider(capacityapi.NewProvider(), []*unstructured.Unstructured{capacityContractNodePool("general")}, nil, nil, nil)
	if selfManaged.ControllerMode != capacityapi.ControllerSelfManaged {
		t.Fatalf("self-managed mode = %q", selfManaged.ControllerMode)
	}

	eksAutoPool := capacityContractNodePool("auto")
	eksAutoPool.Object["spec"] = map[string]any{
		"template": map[string]any{
			"spec": map[string]any{
				"nodeClassRef": map[string]any{"group": "eks.amazonaws.com", "kind": "NodeClass", "name": "default"},
			},
		},
	}
	eksAuto := capacityProvider(capacityapi.NewProvider(), []*unstructured.Unstructured{eksAutoPool}, nil, nil, nil)
	if eksAuto.ControllerMode != capacityapi.ControllerEKSAuto {
		t.Fatalf("EKS Auto mode = %q", eksAuto.ControllerMode)
	}
}

func TestCapacityActionsDeriveNotReadyFromCanonicalIssues(t *testing.T) {
	model := capacitymodel.Model{Pools: []capacitymodel.PoolModel{
		{Observation: capacityapi.PoolObservation{Resource: capacityapi.ResourceIdentity{Ref: subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "unknown"}}}},
		{Observation: capacityapi.PoolObservation{
			Resource: capacityapi.ResourceIdentity{Ref: subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "not-ready"}},
			Issues:   []issuesapi.Issue{{Reason: issues.ReasonKarpenterNodePoolNotReady, Severity: issuesapi.SeverityWarning}},
		}},
	}}

	actions := capacityActions(model, true)
	if len(actions) != 1 || actions[0].Code != "pool_not_ready" || actions[0].Count != 1 || len(actions[0].Pools) != 1 || actions[0].Pools[0].Ref.Name != "not-ready" {
		t.Fatalf("actions = %#v", actions)
	}
}

func TestCapacityActionsUseReadinessFallbackOnlyWhenIssueSnapshotUnavailable(t *testing.T) {
	ready := false
	model := capacitymodel.Model{Pools: []capacitymodel.PoolModel{{Observation: capacityapi.PoolObservation{
		Resource:  capacityapi.ResourceIdentity{Ref: subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "not-ready"}},
		Ready:     &ready,
		NodeClass: &capacityapi.NodeClassObservation{Ready: &ready},
	}}}}
	if actions := capacityActions(model, true); len(actions) != 0 {
		t.Fatalf("available canonical issues should suppress readiness fallbacks: %#v", actions)
	}
	actions := capacityActions(model, false)
	// Both raw readiness bits are false, but the pool's unreadiness is the
	// NodeClass cascade — the fallback collapses it to the root exactly as the
	// canonical path does.
	if len(actions) != 1 || actions[0].Code != "nodeclass_not_ready" {
		t.Fatalf("unavailable canonical issues should preserve the ROOT readiness action only: %#v", actions)
	}

	healthyClass := true
	poolOnly := capacitymodel.Model{Pools: []capacitymodel.PoolModel{{Observation: capacityapi.PoolObservation{
		Resource:  capacityapi.ResourceIdentity{Ref: subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "own-fault"}},
		Ready:     &ready,
		NodeClass: &capacityapi.NodeClassObservation{Ready: &healthyClass},
	}}}}
	if actions := capacityActions(poolOnly, false); len(actions) != 1 || actions[0].Code != "pool_not_ready" {
		t.Fatalf("pool-unready with a healthy NodeClass is its own incident and must keep its signal: %#v", actions)
	}
}

func TestCapacityActionsCountPoolsRatherThanIssueGroups(t *testing.T) {
	model := capacitymodel.Model{Pools: []capacitymodel.PoolModel{{Observation: capacityapi.PoolObservation{
		Resource: capacityapi.ResourceIdentity{Ref: subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "compute"}},
		Issues: []issuesapi.Issue{
			{Reason: issues.ReasonKarpenterNodeClaimProvisioningFailed, Severity: issuesapi.SeverityWarning},
			{Reason: issues.ReasonKarpenterNodeClaimProvisioningFailed, Severity: issuesapi.SeverityWarning},
		},
	}}}}
	actions := capacityActions(model, true)
	if len(actions) != 1 || actions[0].Count != 1 || len(actions[0].Pools) != 1 {
		t.Fatalf("actions should count one affected pool: %#v", actions)
	}
}

func TestCapacityDemandActionsSummarizeActionableStatesDeterministically(t *testing.T) {
	groups := []capacitymodel.DemandGroupModel{
		{Group: capacityapi.DemandGroup{ID: "unknown-b", State: capacityapi.DemandUnknown}},
		{Group: capacityapi.DemandGroup{ID: "waiting", State: capacityapi.DemandWaitingForScheduler}},
		{Group: capacityapi.DemandGroup{ID: "blocked-b", State: capacityapi.DemandBlocked}},
		{Group: capacityapi.DemandGroup{ID: "pressure", State: capacityapi.DemandAwaitingCapacity}},
		{Group: capacityapi.DemandGroup{ID: "blocked-a", State: capacityapi.DemandBlocked}},
		{Group: capacityapi.DemandGroup{ID: "unknown-a", State: capacityapi.DemandUnknown}},
	}

	actions := capacityDemandActions(groups)
	want := []capacityapi.ActionSummary{
		{
			Code: "pending_demand_blocked", Count: 2, HighestSeverity: "warning",
			Pools: []capacityapi.ResourceIdentity{},
		},
		{
			Code: "pending_demand_resource_pressure", Count: 1, HighestSeverity: "info",
			Pools: []capacityapi.ResourceIdentity{},
		},
		{
			Code: "pending_demand_unclassified", Count: 2, HighestSeverity: "info",
			Pools: []capacityapi.ResourceIdentity{},
		},
	}
	if !reflect.DeepEqual(actions, want) {
		t.Fatalf("capacityDemandActions() = %#v, want %#v", actions, want)
	}
}

func TestCapacityDemandActionsBoundSubjectsWithoutChangingCount(t *testing.T) {
	groups := make([]capacitymodel.DemandGroupModel, 0, capacityActionSubjectLimit+3)
	for index := capacityActionSubjectLimit + 2; index >= 0; index-- {
		groups = append(groups, capacitymodel.DemandGroupModel{Group: capacityapi.DemandGroup{
			ID: fmt.Sprintf("blocked-%02d", index), State: capacityapi.DemandBlocked,
		}})
	}

	actions := capacityDemandActions(groups)
	if len(actions) != 1 {
		t.Fatalf("capacityDemandActions() returned %d actions, want 1: %#v", len(actions), actions)
	}
	action := actions[0]
	if action.Count != capacityActionSubjectLimit+3 {
		t.Errorf("Count = %d, want %d", action.Count, capacityActionSubjectLimit+3)
	}
	if !action.Truncated {
		t.Error("Truncated = false, want true")
	}
}

func TestCapacityActionsCollapseTheNodeClassCascade(t *testing.T) {
	pool := func(name string, poolIssues []issuesapi.Issue) capacitymodel.PoolModel {
		return capacitymodel.PoolModel{Observation: capacityapi.PoolObservation{
			Resource: capacityapi.ResourceIdentity{Ref: subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: name}},
			Issues:   poolIssues,
		}}
	}
	issue := func(reason, message string) issuesapi.Issue {
		return issuesapi.Issue{Reason: reason, Message: message, Severity: issuesapi.SeverityWarning}
	}

	cascade := capacitymodel.Model{Pools: []capacitymodel.PoolModel{pool("gpu", []issuesapi.Issue{
		issue("NodeClassNotReady", "ValidationSucceeded=False"),
		issue("NodePoolNotReady", "NodeClassReady=False"),
	})}}
	codes := map[string]bool{}
	for _, action := range capacityActions(cascade, true) {
		codes[action.Code] = true
	}
	if !codes["nodeclass_not_ready"] {
		t.Fatalf("root cause missing from signals: %v", codes)
	}
	if codes["pool_not_ready"] {
		t.Fatal("the pool-not-ready echo of a visible NodeClass signal must collapse — one misconfiguration is one incident")
	}

	standalone := capacitymodel.Model{Pools: []capacitymodel.PoolModel{pool("plain", []issuesapi.Issue{
		issue("NodePoolNotReady", "NodeClassReady=False"),
	})}}
	codes = map[string]bool{}
	for _, action := range capacityActions(standalone, true) {
		codes[action.Code] = true
	}
	if !codes["pool_not_ready"] {
		t.Fatal("a standalone pool-unready has no root cause on screen — its signal must stay")
	}
}
