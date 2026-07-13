package opencost

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildApplicationCostResponse_PartialAndScaledToZero(t *testing.T) {
	inputs := []ApplicationWorkloadCostInput{
		{ApplicationWorkloadRef: ApplicationWorkloadRef{Namespace: "default", Kind: "Deployment", Name: "api"}, DesiredReplicas: 2},
		{ApplicationWorkloadRef: ApplicationWorkloadRef{Namespace: "default", Kind: "Deployment", Name: "scaled"}, DesiredReplicas: 0},
		{ApplicationWorkloadRef: ApplicationWorkloadRef{Namespace: "default", Kind: "StatefulSet", Name: "missing"}, DesiredReplicas: 1},
	}
	unavailable := []ApplicationWorkloadStatus{{
		ApplicationWorkloadRef: ApplicationWorkloadRef{Namespace: "default", Kind: "Deployment", Name: "private"},
		Reason:                 ReasonAccessDenied,
	}}
	unsupported := []ApplicationWorkloadRef{{Namespace: "default", Kind: "Job", Name: "import"}}
	got := BuildApplicationCostResponse(inputs, unavailable, unsupported, map[string]*WorkloadCostResponse{
		"default": {
			Available: true,
			Namespace: "default",
			Workloads: []WorkloadCost{{
				Name:                 "api",
				Kind:                 "Deployment",
				HourlyCost:           0.2,
				CPUCost:              0.12,
				MemoryCost:           0.08,
				Replicas:             2,
				CPUUsageCost:         0.03,
				MemoryUsageCost:      0.02,
				CPUUsageAvailable:    true,
				MemoryUsageAvailable: true,
			}},
		},
	})

	if !got.Available {
		t.Fatalf("expected available partial response, got %+v", got)
	}
	if !got.Partial {
		t.Fatalf("expected Partial=true, got %+v", got)
	}
	if got.Coverage.Total != 5 || got.Coverage.Included != 2 {
		t.Fatalf("coverage = %+v, want total=5 included=2", got.Coverage)
	}
	if len(got.Coverage.Unavailable) != 2 {
		t.Fatalf("unexpected unavailable coverage: %+v", got.Coverage.Unavailable)
	}
	reasonsByName := map[string]string{}
	for _, status := range got.Coverage.Unavailable {
		reasonsByName[status.Name] = status.Reason
	}
	if reasonsByName["missing"] != ReasonNoMetrics || reasonsByName["private"] != ReasonAccessDenied {
		t.Fatalf("unexpected unavailable reasons: %+v", got.Coverage.Unavailable)
	}
	if len(got.Coverage.Unsupported) != 1 || got.Coverage.Unsupported[0].Kind != "Job" {
		t.Fatalf("unexpected unsupported coverage: %+v", got.Coverage.Unsupported)
	}
	if got.Totals.HourlyCost != 0.2 || got.Totals.CPUCost != 0.12 || got.Totals.MemoryCost != 0.08 || got.Totals.Replicas != 2 {
		t.Fatalf("totals = %+v", got.Totals)
	}
	if got.Totals.CPUAllocationUse != 25 || got.Totals.MemoryAllocationUse != 25 {
		t.Fatalf("allocation use = cpu:%v memory:%v, want 25/25", got.Totals.CPUAllocationUse, got.Totals.MemoryAllocationUse)
	}
	if !got.Totals.CPUUsageAvailable || !got.Totals.MemoryUsageAvailable {
		t.Fatalf("usage availability = cpu:%t memory:%t, want both true", got.Totals.CPUUsageAvailable, got.Totals.MemoryUsageAvailable)
	}

	var scaled *ApplicationWorkloadCost
	for i := range got.Workloads {
		if got.Workloads[i].Name == "scaled" {
			scaled = &got.Workloads[i]
			break
		}
	}
	if scaled == nil || !scaled.Available || !scaled.ScaledToZero || scaled.Current == nil || scaled.Current.HourlyCost != 0 {
		t.Fatalf("scaled-to-zero row not preserved as valid zero: %+v", scaled)
	}
	var private *ApplicationWorkloadCost
	for i := range got.Workloads {
		if got.Workloads[i].Name == "private" {
			private = &got.Workloads[i]
			break
		}
	}
	if private == nil || private.Available || private.Reason != ReasonAccessDenied {
		t.Fatalf("prevalidated unavailable row not preserved: %+v", private)
	}
}

func TestApplicationCostTotals_IncompleteUsageSuppressesAggregatePercentage(t *testing.T) {
	total := ApplicationCostTotals{}
	addApplicationCostTotal(&total, WorkloadCost{
		CPUCost: 1, MemoryCost: 1, CPUUsageCost: 0.5, MemoryUsageCost: 0.5,
		CPUUsageAvailable: true, MemoryUsageAvailable: true,
	})
	addApplicationCostTotal(&total, WorkloadCost{
		CPUCost: 1, MemoryCost: 1, CPUUsageCost: 0.25, MemoryUsageCost: 0.5,
		CPUUsageAvailable: false, MemoryUsageAvailable: true,
	})
	finalizeApplicationCostTotals(&total)

	if total.CPUUsageAvailable || total.CPUAllocationUse != 0 {
		t.Fatalf("incomplete CPU aggregate = available:%t use:%v, want false/0", total.CPUUsageAvailable, total.CPUAllocationUse)
	}
	if !total.MemoryUsageAvailable || total.MemoryAllocationUse != 50 {
		t.Fatalf("complete memory aggregate = available:%t use:%v, want true/50", total.MemoryUsageAvailable, total.MemoryAllocationUse)
	}
}

func TestApplicationCostTotals_UsageAvailabilityAlwaysSerialized(t *testing.T) {
	body, err := json.Marshal(ApplicationCostTotals{})
	if err != nil {
		t.Fatal(err)
	}
	jsonBody := string(body)
	if !strings.Contains(jsonBody, `"cpuUsageAvailable":false`) || !strings.Contains(jsonBody, `"memoryUsageAvailable":false`) {
		t.Fatalf("required availability fields missing from %s", jsonBody)
	}
}

func TestBuildApplicationCostResponse_AllPrevalidatedUnavailable(t *testing.T) {
	got := BuildApplicationCostResponse(nil, []ApplicationWorkloadStatus{{
		ApplicationWorkloadRef: ApplicationWorkloadRef{Namespace: "default", Kind: "Deployment", Name: "private"},
		Reason:                 ReasonAccessDenied,
	}}, nil, nil)

	if got.Available {
		t.Fatalf("expected unavailable response, got %+v", got)
	}
	if got.Reason != ReasonAccessDenied {
		t.Fatalf("Reason = %q, want %q", got.Reason, ReasonAccessDenied)
	}
	if got.Coverage.Total != 1 || len(got.Workloads) != 1 || got.Workloads[0].Reason != ReasonAccessDenied {
		t.Fatalf("unexpected response: %+v", got)
	}
}
