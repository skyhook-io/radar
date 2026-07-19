package capacityapi

import (
	"encoding/json"
	"math"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/subject"
)

func TestResponseConstructorsInitializeWireCollections(t *testing.T) {
	asOf := time.Date(2026, time.July, 13, 9, 30, 0, 0, time.UTC)

	overview := marshalCapacityObject(t, NewOverviewResponse(asOf))
	assertCapacityJSONObject(t, overview, "provider", "apiVersionsByKind")
	assertCapacityJSONArray(t, overview, "provider", "nodeClassKinds")
	assertCapacityJSONObject(t, overview, "coverage")
	assertCapacityJSONArray(t, overview, "summary", "actions")
	assertCapacityJSONArray(t, overview, "pools")

	pool := marshalCapacityObject(t, NewPoolObservation())
	for _, path := range [][]string{
		{"conditions"},
		{"configuration", "requirements"},
		{"configuration", "taints"},
		{"configuration", "startupTaints"},
		{"ledger", "limitPressure"},
		{"disruption", "budgets"},
		{"issues"},
		{"facts"},
	} {
		assertCapacityJSONArray(t, pool, path...)
	}
	assertCapacityJSONObject(t, pool, "configuration", "labels")
	assertCapacityJSONObject(t, pool, "coverage")
	if _, ok := pool["composition"]; ok {
		t.Fatal("composition must be omitted until Node coverage is observed")
	}
	if _, ok := pool["workloads"]; ok {
		t.Fatal("workloads must be omitted until Pod coverage is observed")
	}
	if disruption := capacityValueAt(t, pool, "disruption").(map[string]any); disruption["runtime"] != nil {
		t.Fatal("disruption runtime must be omitted until allowances and blockers are observed")
	}

	demand := marshalCapacityObject(t, NewDemandGroup(asOf))
	for _, path := range [][]string{
		{"pods"},
		{"perPodRequests", "sources"},
		{"schedulingSignature", "constraints"},
		{"schedulingSignature", "tolerations"},
		{"schedulerReasons"},
		{"poolEvaluations"},
		{"issues"},
	} {
		assertCapacityJSONArray(t, demand, path...)
	}
	assertCapacityJSONObject(t, demand, "perPodRequests", "resources")
	assertCapacityJSONObject(t, demand, "aggregateRequests", "resources")

	activity := marshalCapacityObject(t, NewActivityResponse(asOf))
	assertCapacityJSONArray(t, activity, "items")
	assertCapacityJSONArray(t, activity, "observation", "sources")
	assertCapacityJSONArray(t, activity, "observation", "gaps")

	demandResponse := marshalCapacityObject(t, NewDemandResponse(asOf))
	assertCapacityJSONArray(t, demandResponse, "items")
	if _, ok := demandResponse["summary"]; ok {
		t.Fatal("demand summary must be omitted until Pod coverage is observed")
	}
}

func TestNestedConstructorsInitializeWireCollections(t *testing.T) {
	asOf := time.Date(2026, time.July, 13, 9, 30, 0, 0, time.UTC)

	usage := marshalCapacityObject(t, NewUsageObservation(asOf))
	assertCapacityJSONObject(t, usage, "quantity", "resources")
	assertCapacityJSONObject(t, usage, "coveredAllocatable", "resources")
	assertCapacityJSONArray(t, usage, "utilization")

	poolSummary := marshalCapacityObject(t, NewPoolSummary())
	assertCapacityJSONArray(t, poolSummary, "ledger", "limitPressure")
	nodeMember := marshalCapacityObject(t, NewNodeMember())
	assertCapacityJSONArray(t, nodeMember, "conditions")
	if _, ok := nodeMember["podCount"]; ok {
		t.Fatal("node podCount must be omitted until Pod coverage is observed")
	}
	claimMember := marshalCapacityObject(t, NewClaimMember())
	assertCapacityJSONArray(t, claimMember, "conditions")
	if _, ok := claimMember["nodeName"]; ok {
		t.Fatal("claim nodeName must be omitted until status reports one")
	}
	workloadMember := marshalCapacityObject(t, NewWorkloadMember())
	assertCapacityJSONArray(t, workloadMember, "nodes")
	assertCapacityJSONObject(t, workloadMember, "nodesMeta")
	assertCapacityJSONArray(t, marshalCapacityObject(t, NewPoolEvaluation()), "evidence")
	assertCapacityJSONArray(t, marshalCapacityObject(t, NewPoolEvaluation()), "unknownPredicates")
	assertCapacityJSONArray(t, marshalCapacityObject(t, NewActivityEvidence()), "refs")
	assertCapacityJSONArray(t, marshalCapacityObject(t, NewActivityEpisode()), "evidence")
	assertCapacityJSONArray(t, marshalCapacityObject(t, NewActionSummary()), "pools")
	demandSummary := marshalCapacityObject(t, NewDemandSummary())
	assertCapacityJSONObject(t, demandSummary, "total")
	for _, state := range []DemandState{
		DemandWaitingForScheduler,
		DemandHeld,
		DemandAwaitingCapacity,
		DemandBlocked,
		DemandUnknown,
	} {
		counts := capacityValueAt(t, demandSummary, "byState", string(state)).(map[string]any)
		if counts["podCount"] != float64(0) || counts["groupCount"] != float64(0) {
			t.Fatalf("initial %s counts = %#v, want explicit zeros", state, counts)
		}
	}
	composition := marshalCapacityObject(t, NewPoolComposition())
	for _, field := range []string{"capacityTypesMeta", "instanceTypesMeta", "zonesMeta", "architecturesMeta", "imagesMeta"} {
		assertCapacityJSONObject(t, composition, field)
	}
}

func TestResourceIdentityUsesCanonicalSubjectRef(t *testing.T) {
	identity := ResourceIdentity{
		Ref: subject.Ref{
			Group:     "karpenter.sh",
			Kind:      "NodePool",
			Namespace: "capacity-system",
			Name:      "general-purpose",
		},
		APIVersion: "karpenter.sh/v1",
		UID:        "d98f72ec",
	}

	got := marshalCapacityObject(t, identity)
	ref := capacityValueAt(t, got, "ref").(map[string]any)
	for field, want := range map[string]string{
		"group":     "karpenter.sh",
		"kind":      "NodePool",
		"namespace": "capacity-system",
		"name":      "general-purpose",
	} {
		if got := ref[field]; got != want {
			t.Fatalf("ref.%s = %#v, want %q", field, got, want)
		}
	}
}

func TestSourceCoverageDistinguishesAvailableZeroFromMissing(t *testing.T) {
	zero := 0
	coverage := NewSourceCoverage(CoverageAvailable, CoverageScopeCluster)
	coverage.ItemCount = &zero

	got := marshalCapacityObject(t, coverage)
	if got["itemCount"] != float64(0) {
		t.Fatalf("itemCount = %#v, want numeric zero", got["itemCount"])
	}
	assertCapacityJSONArray(t, got, "impactFields")

	withoutCount := marshalCapacityObject(t, NewSourceCoverage(CoverageSyncing, CoverageScopeCluster))
	if _, ok := withoutCount["itemCount"]; ok {
		t.Fatal("itemCount must be omitted when the source has not produced a count")
	}
}

func TestKarpenterNumericFieldsUseInt64(t *testing.T) {
	large := int64(math.MaxInt32) + 42
	configuration := NewPoolConfiguration()
	configuration.Weight = &large
	configuration.Replicas = &large
	requirement := Requirement{Values: []string{}, MinValues: &large}

	gotConfiguration := marshalCapacityObject(t, configuration)
	if gotConfiguration["weight"] != float64(large) || gotConfiguration["replicas"] != float64(large) {
		t.Fatalf("large weight/replicas were not preserved: %#v", gotConfiguration)
	}
	gotRequirement := marshalCapacityObject(t, requirement)
	if gotRequirement["minValues"] != float64(large) {
		t.Fatalf("large minValues = %#v, want %d", gotRequirement["minValues"], large)
	}
}

func TestDerivedPercentagesPreserveQuantityInputsAndComparability(t *testing.T) {
	usage := NewUsageObservation(time.Unix(0, 0).UTC())
	usage.Utilization = append(usage.Utilization, ResourceUtilization{
		Resource:    "cpu",
		Usage:       "1250m",
		Allocatable: "4",
		Percent:     31.25,
	})
	gotUsage := marshalCapacityObject(t, usage)
	utilization := capacityValueAt(t, gotUsage, "utilization").([]any)[0].(map[string]any)
	if utilization["usage"] != "1250m" || utilization["allocatable"] != "4" || utilization["percent"] != 31.25 {
		t.Fatalf("utilization = %#v", utilization)
	}

	ledger := NewCapacityLedger()
	ledger.LimitPressure = append(ledger.LimitPressure, LimitPressure{
		Resource:    "example.com/fpga",
		Provisioned: "3",
		Limit:       "not-comparable",
	})
	gotLedger := marshalCapacityObject(t, ledger)
	pressure := capacityValueAt(t, gotLedger, "limitPressure").([]any)[0].(map[string]any)
	if _, ok := pressure["percent"]; ok {
		t.Fatal("percent must be omitted when resource quantities are not comparable")
	}
	if overLimit, ok := pressure["overLimit"]; !ok || overLimit != false {
		t.Fatalf("overLimit = %#v, present = %v; want explicit false", overLimit, ok)
	}
}

func marshalCapacityObject(t *testing.T, value any) map[string]any {
	t.Helper()
	payload, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal capacity value: %v", err)
	}
	var object map[string]any
	if err := json.Unmarshal(payload, &object); err != nil {
		t.Fatalf("decode capacity value: %v", err)
	}
	return object
}

func assertCapacityJSONArray(t *testing.T, root map[string]any, path ...string) {
	t.Helper()
	value := capacityValueAt(t, root, path...)
	if _, ok := value.([]any); !ok {
		t.Fatalf("%v = %#v, want JSON array", path, value)
	}
}

func assertCapacityJSONObject(t *testing.T, root map[string]any, path ...string) {
	t.Helper()
	value := capacityValueAt(t, root, path...)
	if _, ok := value.(map[string]any); !ok {
		t.Fatalf("%v = %#v, want JSON object", path, value)
	}
}

func capacityValueAt(t *testing.T, root map[string]any, path ...string) any {
	t.Helper()
	var current any = root
	for _, element := range path {
		object, ok := current.(map[string]any)
		if !ok {
			t.Fatalf("%v does not lead through a JSON object", path)
		}
		current, ok = object[element]
		if !ok {
			t.Fatalf("missing JSON path %v", path)
		}
	}
	return current
}
