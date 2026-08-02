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
	// Key ABSENCE, not a nil value: a serialized `"runtime": null` also reads
	// as nil here, and the omission contract is what consumers branch on.
	if disruption := capacityValueAt(t, pool, "disruption").(map[string]any); mapHasKey(disruption, "runtime") {
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

func mapHasKey(object map[string]any, key string) bool {
	_, ok := object[key]
	return ok
}

// assertCapacityJSONKeys pins the exact key path a consumer reads. Decoding
// into the Go struct proves nothing about the wire: a renamed json tag still
// round-trips through its own type, and the frontend never sees the struct.
func assertCapacityJSONKeys(t *testing.T, root map[string]any, paths ...[]string) {
	t.Helper()
	for _, path := range paths {
		capacityValueAt(t, root, path...)
	}
}

// TestOverviewGroupSurfaceWireKeys pins the M1 group/manager surface at the
// JSON-key level — the pre-M1 constructor test below covers only the older
// fields, so every key added since could be renamed without a failing test.
func TestOverviewGroupSurfaceWireKeys(t *testing.T) {
	asOf := time.Date(2026, time.July, 13, 9, 30, 0, 0, time.UTC)
	minSize, maxSize, target, ready, total := 1, 9, 3, 3, 3
	unattributed := 2

	response := NewOverviewResponse(asOf)
	response.Summary.Managers = []ManagerSummary{{
		Manager:    ManagerGKEAutoscaler,
		GroupCount: 1,
		Status:     ManagerDegraded,
		Detail:     "scale-up backoff on gke-pool-a-1234-grp",
	}}
	response.Summary.UnattributedNodeCount = &unattributed
	requests := NewQuantityObservation(asOf)
	allocatable := NewQuantityObservation(asOf)
	negative := NewQuantityObservation(asOf)
	response.Summary.ClusterScheduling = &SchedulingCapacity{
		ScheduledRequests:        &requests,
		Allocatable:              &allocatable,
		NegativePriorityRequests: &negative,
	}
	child := AutoscalerChildObservation{
		ID: "gke-pool-a-1234-grp", Name: "https://example/instanceGroups/gke-pool-a-1234-grp",
		MinSize: &minSize, MaxSize: &maxSize, Target: &target,
		Health: "Healthy", ReadyNodes: &ready, TotalNodes: &total,
		Backoff: &AutoscalerBackoff{ErrorClass: "OutOfResource", ErrorCode: "QUOTA_EXCEEDED", ErrorMessage: "quota"},
		AsOf:    &asOf,
	}
	groupAllocatable := NewQuantityObservation(asOf)
	groupRequests := NewQuantityObservation(asOf)
	response.Groups = []CapacityGroupSummary{{
		ID: "gke-nodepool/pool-a", Name: "pool-a", Platform: "gke",
		Manager: ManagerGKEAutoscaler, ManagerValidated: true,
		NodeCount: 3, ReadyNodeCount: 3,
		Allocatable: &groupAllocatable, ScheduledRequests: &groupRequests,
		Scaling: []ScalingFact{
			{Code: "bounds", Summary: "1–9 nodes"},
			{Code: "manager_detection_unavailable", Summary: "manager detection unavailable"},
		},
		Children:     []AutoscalerChildObservation{child},
		ChildrenMeta: BoundedResultMeta{Total: 1, Returned: 1},
	}}
	response.OrphanAutoscalerGroups = []AutoscalerChildObservation{child}
	response.OrphanAutoscalerGroupsMeta = BoundedResultMeta{Total: 1, Returned: 1}

	got := marshalCapacityObject(t, response)
	assertCapacityJSONKeys(t, got,
		[]string{"summary", "managers"},
		[]string{"summary", "unattributedNodeCount"},
		[]string{"summary", "clusterScheduling", "scheduledRequests"},
		[]string{"summary", "clusterScheduling", "allocatable"},
		[]string{"summary", "clusterScheduling", "negativePriorityRequests"},
		[]string{"groups"},
		[]string{"orphanAutoscalerGroups"},
		[]string{"orphanAutoscalerGroupsMeta", "total"},
		[]string{"orphanAutoscalerGroupsMeta", "returned"},
	)

	manager := capacityValueAt(t, got, "summary", "managers").([]any)[0].(map[string]any)
	for key, want := range map[string]any{
		"manager": "gke_autoscaler", "groupCount": float64(1), "status": "degraded",
		"detail": "scale-up backoff on gke-pool-a-1234-grp",
	} {
		if manager[key] != want {
			t.Fatalf("summary.managers[0].%s = %#v, want %#v", key, manager[key], want)
		}
	}

	group := capacityValueAt(t, got, "groups").([]any)[0].(map[string]any)
	for key, want := range map[string]any{
		"id": "gke-nodepool/pool-a", "name": "pool-a", "platform": "gke", "manager": "gke_autoscaler",
		"managerValidated": true, "nodeCount": float64(3), "readyNodeCount": float64(3),
	} {
		if group[key] != want {
			t.Fatalf("groups[0].%s = %#v, want %#v", key, group[key], want)
		}
	}
	for _, key := range []string{"allocatable", "scheduledRequests", "scaling", "children", "childrenMeta"} {
		if !mapHasKey(group, key) {
			t.Fatalf("groups[0].%s missing: %#v", key, group)
		}
	}
	fact := group["scaling"].([]any)[1].(map[string]any)
	if fact["code"] != "manager_detection_unavailable" || fact["summary"] != "manager detection unavailable" {
		t.Fatalf("scaling fact = %#v", fact)
	}
	childMeta := group["childrenMeta"].(map[string]any)
	if childMeta["total"] != float64(1) || childMeta["returned"] != float64(1) {
		t.Fatalf("groups[0].childrenMeta = %#v", childMeta)
	}

	for label, object := range map[string]map[string]any{
		"groups[0].children[0]":     group["children"].([]any)[0].(map[string]any),
		"orphanAutoscalerGroups[0]": capacityValueAt(t, got, "orphanAutoscalerGroups").([]any)[0].(map[string]any),
	} {
		for key, want := range map[string]any{
			"id": "gke-pool-a-1234-grp", "name": "https://example/instanceGroups/gke-pool-a-1234-grp",
			"minSize": float64(1), "maxSize": float64(9), "target": float64(3),
			"health": "Healthy", "readyNodes": float64(3), "totalNodes": float64(3),
		} {
			if object[key] != want {
				t.Fatalf("%s.%s = %#v, want %#v", label, key, object[key], want)
			}
		}
		backoff := object["backoff"].(map[string]any)
		if backoff["errorClass"] != "OutOfResource" || backoff["errorCode"] != "QUOTA_EXCEEDED" || backoff["errorMessage"] != "quota" {
			t.Fatalf("%s.backoff = %#v", label, backoff)
		}
		if !mapHasKey(object, "asOf") {
			t.Fatalf("%s.asOf missing: %#v", label, object)
		}
	}

	// poolCount is emitted only when NodePool coverage was observed; a zero
	// would read as "this fleet has no NodePools".
	summary := capacityValueAt(t, got, "summary").(map[string]any)
	if mapHasKey(summary, "poolCount") {
		t.Fatalf("summary.poolCount must be omitted when unset: %#v", summary)
	}
	count := 4
	response.Summary.PoolCount = &count
	if got := marshalCapacityObject(t, response); capacityValueAt(t, got, "summary", "poolCount") != float64(4) {
		t.Fatalf("summary.poolCount = %#v, want 4", capacityValueAt(t, got, "summary", "poolCount"))
	}
}

func TestDemandGroupNominatedPodCountWireKey(t *testing.T) {
	asOf := time.Date(2026, time.July, 13, 9, 30, 0, 0, time.UTC)
	group := NewDemandGroup(asOf)
	if mapHasKey(marshalCapacityObject(t, group), "nominatedPodCount") {
		t.Fatal("nominatedPodCount must be omitted when no pod holds a nomination — zero and unobserved differ")
	}
	nominated := 2
	group.NominatedPodCount = &nominated
	if got := marshalCapacityObject(t, group); capacityValueAt(t, got, "nominatedPodCount") != float64(2) {
		t.Fatalf("nominatedPodCount = %#v, want 2", capacityValueAt(t, got, "nominatedPodCount"))
	}
}

func TestActivityAggregateWireKeys(t *testing.T) {
	asOf := time.Date(2026, time.July, 13, 9, 30, 0, 0, time.UTC)
	response := NewActivityResponse(asOf)
	if mapHasKey(marshalCapacityObject(t, response), "aggregate") {
		t.Fatal("aggregate must be omitted on non-first pages")
	}

	aggregate := NewActivityAggregate()
	aggregate.Total = 4
	aggregate.ByType[ActivityProvision] = ActivityTypeCounts{
		Total:   4,
		ByState: map[ActivityState]int{ActivityCompleted: 2, ActivityBlocked: 1, ActivityEnded: 1},
	}
	response.Aggregate = &aggregate

	got := marshalCapacityObject(t, response)
	assertCapacityJSONKeys(t, got,
		[]string{"aggregate", "total"},
		[]string{"aggregate", "byType"},
		[]string{"aggregate", "byType", string(ActivityProvision), "total"},
		[]string{"aggregate", "byType", string(ActivityProvision), "byState"},
	)
	byState := capacityValueAt(t, got, "aggregate", "byType", string(ActivityProvision), "byState").(map[string]any)
	if byState[string(ActivityCompleted)] != float64(2) || byState[string(ActivityBlocked)] != float64(1) {
		t.Fatalf("aggregate byState = %#v", byState)
	}
	// The frontend switches on this literal; renaming it silently drops the
	// bucket into the default badge.
	if byState["ended"] != float64(1) {
		t.Fatalf("aggregate byState is missing the \"ended\" key: %#v", byState)
	}
}

// TestActivityStateWireValues pins the exact strings the frontend switches on.
// A Go-side rename round-trips through its own type without failing anything,
// but silently changes what every consumer sees.
func TestActivityStateWireValues(t *testing.T) {
	for state, want := range map[ActivityState]string{
		ActivityOpen:      "open",
		ActivityCompleted: "completed",
		ActivityFailed:    "failed",
		ActivityObserved:  "observed",
		ActivityBlocked:   "blocked",
		ActivityEnded:     "ended",
		ActivityUnknown:   "unknown",
	} {
		if string(state) != want {
			t.Fatalf("activity state = %q, want %q", state, want)
		}
		episode := NewActivityEpisode()
		episode.State = state
		if got := marshalCapacityObject(t, episode)["state"]; got != want {
			t.Fatalf("serialized state = %#v, want %q", got, want)
		}
	}
}
