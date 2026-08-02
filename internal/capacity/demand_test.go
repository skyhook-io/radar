package capacity

import (
	"fmt"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/subject"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
)

func TestBuildDemandGroupsFiltersGroupsBoundsAndStabilizes(t *testing.T) {
	asOf := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	first := demandTestPod("web-a", "500m")
	first.CreationTimestamp = metav1.NewTime(asOf.Add(-10 * time.Minute))
	first.Spec.NodeSelector = map[string]string{"workload-tier": "frontend"}
	second := demandTestPod("web-b", "500m")
	second.CreationTimestamp = metav1.NewTime(asOf.Add(-5 * time.Minute))
	second.Spec.NodeSelector = map[string]string{"workload-tier": "frontend"}
	differentRequest := demandTestPod("web-large", "1")
	differentRequest.Spec.NodeSelector = map[string]string{"workload-tier": "frontend"}
	assigned := demandTestPod("assigned", "500m")
	assigned.Spec.NodeName = "node-a"
	terminal := demandTestPod("finished", "500m")
	terminal.Status.Phase = corev1.PodSucceeded
	deleting := demandTestPod("deleting", "500m")
	deletedAt := metav1.NewTime(asOf.Add(-time.Minute))
	deleting.DeletionTimestamp = &deletedAt

	owner := subject.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
	input := DemandInput{
		GeneratedAt: asOf,
		Pods:        []*corev1.Pod{differentRequest, assigned, second, deleting, terminal, first},
		PodRefLimit: 1,
		ResolvePodOwner: func(*corev1.Pod) *subject.Ref {
			copy := owner
			return &copy
		},
	}
	groups := BuildDemandGroups(input)
	if len(groups) != 2 {
		t.Fatalf("BuildDemandGroups() returned %d groups, want 2: %#v", len(groups), groups)
	}

	group := demandGroupWithPodCount(t, groups, 2)
	if group.Owner == nil || *group.Owner != owner {
		t.Fatalf("owner = %#v, want %#v", group.Owner, owner)
	}
	if group.PodsMeta != (capacityapi.BoundedResultMeta{Total: 2, Returned: 1, Truncated: true}) || len(group.Pods) != 1 {
		t.Fatalf("bounded pod refs = %#v, pods = %#v", group.PodsMeta, group.Pods)
	}
	if group.Pods[0].Ref.Name != "web-a" {
		t.Fatalf("first bounded pod ref = %q, want web-a", group.Pods[0].Ref.Name)
	}
	if !group.FirstSeen.Equal(first.CreationTimestamp.Time) || !group.LastSeen.Equal(asOf) {
		t.Fatalf("observation times = %s..%s", group.FirstSeen, group.LastSeen)
	}
	if group.PerPodRequests.Resources["cpu"] != "500m" || group.PerPodRequests.Resources["pods"] != "1" {
		t.Fatalf("per-pod requests = %#v", group.PerPodRequests.Resources)
	}
	if group.AggregateRequests.Resources["cpu"] != "1" || group.AggregateRequests.Resources["pods"] != "2" {
		t.Fatalf("aggregate requests = %#v", group.AggregateRequests.Resources)
	}
	if group.State != capacityapi.DemandWaitingForScheduler || group.ID == "" || group.Fingerprint == "" {
		t.Fatalf("group identity/state = %#v", group)
	}

	reversed := append([]*corev1.Pod{}, input.Pods...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	input.Pods = reversed
	secondBuild := BuildDemandGroups(input)
	if !reflect.DeepEqual(demandGroupIDs(groups), demandGroupIDs(secondBuild)) {
		t.Fatalf("group IDs changed with input order: %v vs %v", demandGroupIDs(groups), demandGroupIDs(secondBuild))
	}
	if !reflect.DeepEqual(groups, secondBuild) {
		t.Fatal("demand response changed with input order")
	}
}

func TestDemandGroupModelKeepsAllPodRefsBeyondWireLimit(t *testing.T) {
	first := demandTestPod("web-a", "500m")
	second := demandTestPod("web-b", "500m")
	models := BuildDemandGroupModels(DemandInput{
		GeneratedAt: capacityTestTime(),
		Pods:        []*corev1.Pod{first, second},
		PodRefLimit: 1,
		ResolvePodOwner: func(*corev1.Pod) *subject.Ref {
			return &subject.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}
		},
	})
	if len(models) != 1 {
		t.Fatalf("BuildDemandGroupModels() returned %d groups, want 1", len(models))
	}
	if got := models[0].PodRefs(); len(got) != 2 || got[0].Name != "web-a" || got[1].Name != "web-b" {
		t.Fatalf("PodRefs() = %#v, want both grouped pods", got)
	}
}

func TestBuildDemandGroupModelsCountsNominatedPods(t *testing.T) {
	controller := true
	ownerRef := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-abc", Controller: &controller}
	nominatedA := demandTestPod("nominated-a", "500m")
	nominatedA.OwnerReferences = []metav1.OwnerReference{ownerRef}
	nominatedA.Status.NominatedNodeName = "ip-10-0-0-1"
	nominatedB := demandTestPod("nominated-b", "500m")
	nominatedB.OwnerReferences = []metav1.OwnerReference{ownerRef}
	nominatedB.Status.NominatedNodeName = "ip-10-0-0-2"
	plain := demandTestPod("plain-c", "500m")
	plain.OwnerReferences = []metav1.OwnerReference{ownerRef}

	models := BuildDemandGroupModels(DemandInput{
		GeneratedAt: capacityTestTime(),
		Pods:        []*corev1.Pod{nominatedA, nominatedB, plain},
	})
	if len(models) != 1 {
		t.Fatalf("BuildDemandGroupModels() returned %d groups, want 1: %#v", len(models), models)
	}
	got := models[0].Group.NominatedPodCount
	if got == nil {
		t.Fatal("NominatedPodCount = nil, want 2")
	}
	if *got != 2 {
		t.Fatalf("NominatedPodCount = %d, want 2", *got)
	}

	none := demandTestPod("plain-solo", "500m")
	none.OwnerReferences = []metav1.OwnerReference{ownerRef}
	noneModels := BuildDemandGroupModels(DemandInput{
		GeneratedAt: capacityTestTime(),
		Pods:        []*corev1.Pod{none},
	})
	if count := noneModels[0].Group.NominatedPodCount; count != nil {
		t.Fatalf("NominatedPodCount = %d, want nil when no pod is nominated", *count)
	}
}

func TestBuildDemandGroupsCanonicalizesAffinityAndTolerationOrder(t *testing.T) {
	first := demandTestPod("worker-a", "250m")
	first.Spec.NodeSelector = map[string]string{"workload-tier": "batch", "team": "compute"}
	first.Spec.Tolerations = []corev1.Toleration{
		{Key: "spot", Operator: corev1.TolerationOpExists, Effect: corev1.TaintEffectNoSchedule},
		{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "batch", Effect: corev1.TaintEffectNoSchedule},
	}
	first.Spec.Affinity = requiredNodeAffinity(
		[]corev1.NodeSelectorRequirement{{Key: "zone-group", Operator: corev1.NodeSelectorOpIn, Values: []string{"b", "a"}}},
		[]corev1.NodeSelectorRequirement{{Key: "instance-family", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"legacy"}}},
	)
	second := first.DeepCopy()
	second.Name = "worker-b"
	second.UID = "worker-b"
	second.Spec.Tolerations[0], second.Spec.Tolerations[1] = second.Spec.Tolerations[1], second.Spec.Tolerations[0]
	terms := second.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	terms[0], terms[1] = terms[1], terms[0]

	groups := BuildDemandGroups(DemandInput{
		GeneratedAt: time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC),
		Pods:        []*corev1.Pod{first, second},
		ResolvePodOwner: func(*corev1.Pod) *subject.Ref {
			return &subject.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "workers"}
		},
	})
	if len(groups) != 1 {
		t.Fatalf("semantically identical scheduling constraints produced %d groups", len(groups))
	}
	if len(groups[0].SchedulingSignature.Constraints) != 4 || len(groups[0].SchedulingSignature.Tolerations) != 2 {
		t.Fatalf("signature = %#v", groups[0].SchedulingSignature)
	}
}

func TestBuildDemandGroupsAddsOneConstraintForOneSelector(t *testing.T) {
	pod := demandTestPod("frontend", "500m")
	pod.Spec.NodeSelector = map[string]string{"workload-tier": "frontend"}
	ready := true
	pool := demandTestPool("backend", &ready, demandPoolSpec(
		[]any{demandRequirementObject("workload-tier", corev1.NodeSelectorOpIn, "backend")}, nil, nil, nil, nil,
	), nil)

	group := BuildDemandGroups(DemandInput{
		GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{{NodePool: pool}},
	})[0]
	if group.SchedulingSignature.ConstraintsMeta != (capacityapi.BoundedResultMeta{Total: 1, Returned: 1}) || len(group.SchedulingSignature.Constraints) != 1 {
		t.Fatalf("selector constraints = %+v, %#v", group.SchedulingSignature.ConstraintsMeta, group.SchedulingSignature.Constraints)
	}
	evaluation := group.PoolEvaluations[0]
	if evaluation.EvidenceMeta != (capacityapi.BoundedResultMeta{Total: 1, Returned: 1}) || len(evaluation.Evidence) != 1 {
		t.Fatalf("selector evidence = %+v, %#v", evaluation.EvidenceMeta, evaluation.Evidence)
	}
}

func TestSummarizeDemandGroupModelsCountsPodsAndGroupsByState(t *testing.T) {
	models := []DemandGroupModel{
		{Group: capacityapi.DemandGroup{State: capacityapi.DemandWaitingForScheduler, PodCount: 2}},
		{Group: capacityapi.DemandGroup{State: capacityapi.DemandWaitingForScheduler, PodCount: 3}},
		{Group: capacityapi.DemandGroup{State: capacityapi.DemandHeld, PodCount: 1}},
		{Group: capacityapi.DemandGroup{State: capacityapi.DemandAwaitingCapacity, PodCount: 5}},
		{Group: capacityapi.DemandGroup{State: capacityapi.DemandBlocked, PodCount: 4}},
		{Group: capacityapi.DemandGroup{State: capacityapi.DemandUnknown, PodCount: 6}},
	}

	got := SummarizeDemandGroupModels(models)
	want := capacityapi.DemandSummary{
		Total: capacityapi.DemandCounts{PodCount: 21, GroupCount: 6},
		ByState: map[capacityapi.DemandState]capacityapi.DemandCounts{
			capacityapi.DemandWaitingForScheduler: {PodCount: 5, GroupCount: 2},
			capacityapi.DemandHeld:                {PodCount: 1, GroupCount: 1},
			capacityapi.DemandAwaitingCapacity:    {PodCount: 5, GroupCount: 1},
			capacityapi.DemandBlocked:             {PodCount: 4, GroupCount: 1},
			capacityapi.DemandUnknown:             {PodCount: 6, GroupCount: 1},
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("summary = %#v, want %#v", got, want)
	}
	if empty := SummarizeDemandGroupModels(nil); !reflect.DeepEqual(empty, capacityapi.NewDemandSummary()) {
		t.Fatalf("empty summary = %#v, want explicit zero buckets", empty)
	}
}

func TestDemandSnapshotFingerprintTracksOnlyKeysetMembership(t *testing.T) {
	pod := demandTestPod("worker", "500m")
	ready := true
	nodeClassReady := true
	pool := demandTestPool("general", &ready, demandPoolSpecWithNodeClass(), map[string]any{"cpu": "1"})
	pools := []DemandPoolInput{{NodePool: pool, NodeClassReady: &nodeClassReady, ProvisionedKnown: true}}
	models := BuildDemandGroupModels(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}})
	baseline := DemandSnapshotFingerprint(models, pools)

	later := BuildDemandGroupModels(DemandInput{GeneratedAt: capacityTestTime().Add(time.Minute), Pods: []*corev1.Pod{pod}})
	if got := DemandSnapshotFingerprint(later, pools); got != baseline {
		t.Fatalf("observation time changed fingerprint: %q != %q", got, baseline)
	}

	specChanged := pool.DeepCopy()
	if err := unstructured.SetNestedField(specChanged.Object, int64(10), "spec", "weight"); err != nil {
		t.Fatal(err)
	}
	if got := DemandSnapshotFingerprint(models, []DemandPoolInput{{NodePool: specChanged}}); got != baseline {
		t.Errorf("NodePool spec changed keyset fingerprint: %q != %q", got, baseline)
	}

	notReady := false
	readinessChanged := demandTestPool("general", &notReady, demandPoolSpecWithNodeClass(), map[string]any{"cpu": "1"})
	if got := DemandSnapshotFingerprint(models, []DemandPoolInput{{NodePool: readinessChanged}}); got != baseline {
		t.Errorf("NodePool readiness changed keyset fingerprint: %q != %q", got, baseline)
	}

	resourcesChanged := demandTestPool("general", &ready, demandPoolSpecWithNodeClass(), map[string]any{"cpu": "2"})
	if got := DemandSnapshotFingerprint(models, []DemandPoolInput{{NodePool: resourcesChanged}}); got != baseline {
		t.Errorf("provisioned resources changed keyset fingerprint: %q != %q", got, baseline)
	}

	classNotReady := false
	if got := DemandSnapshotFingerprint(models, []DemandPoolInput{{NodePool: pool, NodeClassReady: &classNotReady}}); got != baseline {
		t.Errorf("NodeClass readiness changed keyset fingerprint: %q != %q", got, baseline)
	}

	reasonPod := pod.DeepCopy()
	reasonPod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/1 nodes are available: 1 Insufficient cpu.", LastTransitionTime: metav1.NewTime(capacityTestTime().Add(-time.Minute)),
	}}
	reasonModels := BuildDemandGroupModels(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{reasonPod}})
	reasonFingerprint := DemandSnapshotFingerprint(reasonModels, pools)
	reasonPod.Status.Conditions[0].LastTransitionTime = metav1.NewTime(capacityTestTime().Add(-2 * time.Minute))
	reasonModels = BuildDemandGroupModels(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{reasonPod}})
	if got := DemandSnapshotFingerprint(reasonModels, pools); got != reasonFingerprint {
		t.Error("scheduler evidence changed keyset fingerprint")
	}

	second := demandTestPod("second", "500m")
	second.Spec.NodeSelector = map[string]string{"workload": "batch"}
	withSecondGroup := BuildDemandGroupModels(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod, second}})
	if got := DemandSnapshotFingerprint(withSecondGroup, pools); got == baseline {
		t.Error("adding a demand group did not change keyset fingerprint")
	}
}

func TestDemandPoolEvaluationDeterministicCompatibility(t *testing.T) {
	pod := demandTestPod("frontend", "500m")
	pod.Spec.NodeSelector = map[string]string{"workload-tier": "frontend"}
	compatibleRequirement := demandRequirementObject("workload-tier", corev1.NodeSelectorOpIn, "frontend")
	conflictingRequirement := demandRequirementObject("workload-tier", corev1.NodeSelectorOpIn, "backend")

	ready := true
	notReady := false
	inputs := []DemandPoolInput{
		{NodePool: demandTestPool("selector-conflict", &ready, demandPoolSpec([]any{conflictingRequirement}, nil, nil, nil, nil), nil)},
		{NodePool: demandTestPool("permanent-taint", &ready, demandPoolSpec([]any{compatibleRequirement}, []any{demandTaintObject("dedicated", "batch", corev1.TaintEffectNoSchedule)}, nil, nil, nil), nil)},
		{NodePool: demandTestPool("startup-taint", &ready, demandPoolSpec([]any{compatibleRequirement}, nil, []any{demandTaintObject("dedicated", "batch", corev1.TaintEffectNoSchedule)}, nil, nil), nil)},
		{NodePool: demandTestPool("not-ready", &notReady, demandPoolSpec([]any{compatibleRequirement}, nil, nil, nil, nil), nil)},
		{NodePool: demandTestPool("readiness-unknown", nil, demandPoolSpec([]any{compatibleRequirement}, nil, nil, nil, nil), nil)},
	}

	group := BuildDemandGroups(DemandInput{GeneratedAt: time.Now().UTC(), Pods: []*corev1.Pod{pod}, Pools: inputs})[0]
	assertDemandEvaluation(t, group.PoolEvaluations, "selector-conflict", capacityapi.PoolIncompatible, "selectorRequirement")
	assertDemandEvaluation(t, group.PoolEvaluations, "permanent-taint", capacityapi.PoolIncompatible, "permanentTaint")
	startup := assertDemandEvaluation(t, group.PoolEvaluations, "startup-taint", capacityapi.PoolDeclaredCompatible, "")
	for _, evidence := range startup.Evidence {
		if evidence.Predicate == "permanentTaint" {
			t.Fatal("startup taint was treated as a permanent placement constraint")
		}
	}
	assertDemandEvaluation(t, group.PoolEvaluations, "not-ready", capacityapi.PoolIncompatible, "nodePoolReady")
	unknown := assertDemandEvaluation(t, group.PoolEvaluations, "readiness-unknown", capacityapi.PoolEvaluationUnknown, "")
	if unknown.Evidence == nil {
		t.Fatal("unknown pool evaluation evidence must encode as an empty array")
	}
	assertDemandUnknown(t, unknown, "nodePool.readiness")
}

func TestDemandPoolEvaluationUsesExplicitLimitAndStaticEvidence(t *testing.T) {
	pod := demandTestPod("worker", "1")
	ready := true
	replicas := int64(2)
	zeroReplicas := int64(0)
	limitedSpec := demandPoolSpec(nil, nil, nil, nil, map[string]any{"cpu": "4"})
	staticSpec := demandPoolSpec(nil, nil, nil, &replicas, nil)
	frozenSpec := demandPoolSpec(nil, nil, nil, &zeroReplicas, nil)
	inputs := []DemandPoolInput{
		{NodePool: demandTestPool("limit-exhausted", &ready, limitedSpec, map[string]any{"cpu": "3500m"}), ProvisionedKnown: true},
		{NodePool: demandTestPool("limit-unknown", &ready, limitedSpec, nil)},
		{NodePool: demandTestPool("static-zero", &ready, frozenSpec, nil)},
		{NodePool: demandTestPool("static-unknown", &ready, staticSpec, nil)},
	}

	evaluations := BuildDemandGroups(DemandInput{GeneratedAt: time.Now().UTC(), Pods: []*corev1.Pod{pod}, Pools: inputs})[0].PoolEvaluations
	assertDemandEvaluation(t, evaluations, "limit-exhausted", capacityapi.PoolIncompatible, "configuredLimit")
	limitUnknown := assertDemandEvaluation(t, evaluations, "limit-unknown", capacityapi.PoolEvaluationUnknown, "")
	assertDemandUnknown(t, limitUnknown, "nodePool.limits")
	assertDemandEvaluation(t, evaluations, "static-zero", capacityapi.PoolIncompatible, "staticCapacity")
	staticUnknown := assertDemandEvaluation(t, evaluations, "static-unknown", capacityapi.PoolEvaluationUnknown, "")
	assertDemandUnknown(t, staticUnknown, "staticCapacity")
}

func TestDemandUnsupportedConstraintsForceUnknown(t *testing.T) {
	pod := demandTestPod("gpu", "500m")
	pod.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"] = resource.MustParse("1")
	pod.Spec.Containers[0].Ports = []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}}
	pod.Spec.Volumes = []corev1.Volume{{
		Name: "data", VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: "data-gpu"}},
	}}
	pod.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchFields: []corev1.NodeSelectorRequirement{{Key: "metadata.name", Operator: corev1.NodeSelectorOpIn, Values: []string{"node-a"}}},
		}}},
	}}
	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)

	evaluation := BuildDemandGroups(DemandInput{
		GeneratedAt: time.Now().UTC(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{{NodePool: pool}},
	})[0].PoolEvaluations[0]
	if evaluation.Result != capacityapi.PoolEvaluationUnknown {
		t.Fatalf("result = %q, want unknown", evaluation.Result)
	}
	for _, predicate := range []string{"providerResource.nvidia.com/gpu", "hostPort", "persistentVolumeTopology", "requiredNodeAffinity.matchFields"} {
		assertDemandUnknown(t, evaluation, predicate)
	}
}

func TestDemandKnownIncompatibilityWinsOverUnrelatedUnknown(t *testing.T) {
	pod := demandTestPod("constrained", "500m")
	pod.Spec.NodeSelector = map[string]string{"workload-tier": "frontend"}
	pod.Spec.Containers[0].Ports = []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}}
	ready := true
	pool := demandTestPool("backend", &ready, demandPoolSpec(
		[]any{demandRequirementObject("workload-tier", corev1.NodeSelectorOpIn, "backend")}, nil, nil, nil, nil,
	), nil)

	evaluation := BuildDemandGroups(DemandInput{
		GeneratedAt: time.Now().UTC(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{{NodePool: pool}},
	})[0].PoolEvaluations[0]
	if evaluation.Result != capacityapi.PoolIncompatible {
		t.Fatalf("result = %q, want incompatible: %#v", evaluation.Result, evaluation)
	}
	assertDemandUnknown(t, evaluation, "hostPort")
	foundEvidence := false
	for _, evidence := range evaluation.Evidence {
		foundEvidence = foundEvidence || evidence.Predicate == "selectorRequirement"
	}
	if !foundEvidence {
		t.Fatalf("known selector contradiction absent from %#v", evaluation.Evidence)
	}
}

func TestDemandSchedulerConditionRetainsReasonWithoutOverclaimingLifecycleState(t *testing.T) {
	asOf := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	transition := metav1.NewTime(asOf.Add(-2 * time.Minute))
	pod := demandTestPod("unschedulable", "500m")
	pod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable",
		Message: "0/3 nodes matched", LastTransitionTime: transition,
	}}

	group := BuildDemandGroups(DemandInput{GeneratedAt: asOf, Pods: []*corev1.Pod{pod}})[0]
	if group.State != capacityapi.DemandUnknown {
		t.Fatalf("state = %q, want unknown until claim/event evidence classifies the lifecycle", group.State)
	}
	if len(group.SchedulerReasons) != 1 {
		t.Fatalf("scheduler reasons = %#v", group.SchedulerReasons)
	}
	reason := group.SchedulerReasons[0]
	if reason.Code != "Other" || reason.Source != "pod.status.conditions.PodScheduled" || reason.Message != "0/3 nodes matched" || reason.Count != 1 {
		t.Fatalf("scheduler reason = %#v", reason)
	}
	if !reason.FirstSeen.Equal(transition.Time) || !reason.LastSeen.Equal(asOf) {
		t.Fatalf("scheduler reason times = %s..%s", reason.FirstSeen, reason.LastSeen)
	}
}

func TestDemandSchedulerStateUsesSchedulingGatesAsCurrentTruth(t *testing.T) {
	asOf := time.Date(2026, time.July, 13, 12, 0, 0, 0, time.UTC)
	pod := demandTestPod("gated", "500m")
	pod.Spec.SchedulingGates = []corev1.PodSchedulingGate{{Name: "example.com/release"}, {Name: "example.com/quota"}}
	pod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "pod has unbound immediate PersistentVolumeClaims",
	}}

	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)
	group := BuildDemandGroups(DemandInput{
		GeneratedAt: asOf,
		Pods:        []*corev1.Pod{pod},
		Pools: []DemandPoolInput{{
			NodePool:         pool,
			ProvisionedKnown: true,
		}},
	})[0]
	if group.State != capacityapi.DemandHeld {
		t.Fatalf("state = %q, want held from current spec despite stale scheduler condition", group.State)
	}
	if len(group.SchedulerReasons) != 1 {
		t.Fatalf("scheduler reasons = %#v, want one scheduling-gate reason", group.SchedulerReasons)
	}
	reason := group.SchedulerReasons[0]
	if reason.Code != corev1.PodReasonSchedulingGated || reason.Source != "pod.spec.schedulingGates" || reason.Message != "example.com/quota, example.com/release" {
		t.Fatalf("scheduling-gate reason = %#v", reason)
	}
	if !reason.FirstSeen.Equal(asOf) || !reason.LastSeen.Equal(asOf) {
		t.Fatalf("scheduling-gate observation time = %s..%s, want %s", reason.FirstSeen, reason.LastSeen, asOf)
	}
	if group.PoolEvaluationCounts.DeclaredCompatible != 1 || len(group.PoolEvaluations) != 1 || group.PoolEvaluations[0].Result != capacityapi.PoolDeclaredCompatible {
		t.Fatalf("scheduling gate changed declared compatibility: %#v", group.PoolEvaluations)
	}
}

func TestDemandSchedulerStateUsesStructuredReasonsConservatively(t *testing.T) {
	tests := []struct {
		name      string
		reason    string
		message   string
		wantState capacityapi.DemandState
		wantCodes []string
	}{
		{name: "scheduler has not attempted placement", wantState: capacityapi.DemandWaitingForScheduler},
		{name: "scheduling gate is held", reason: corev1.PodReasonSchedulingGated, message: "scheduling is gated", wantState: capacityapi.DemandHeld, wantCodes: []string{"SchedulingGated"}},
		{name: "CPU capacity", reason: corev1.PodReasonUnschedulable, message: "0/3 nodes are available: 3 Insufficient cpu.", wantState: capacityapi.DemandAwaitingCapacity, wantCodes: []string{"InsufficientResource"}},
		{name: "extended resource capacity", reason: corev1.PodReasonUnschedulable, message: "0/3 nodes are available: 3 Insufficient nvidia.com/gpu.", wantState: capacityapi.DemandAwaitingCapacity, wantCodes: []string{"InsufficientResource"}},
		{name: "placement may scale from zero", reason: corev1.PodReasonUnschedulable, message: "0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.", wantState: capacityapi.DemandAwaitingCapacity, wantCodes: []string{"NodeAffinitySelector"}},
		{name: "operator taint may have another pool", reason: corev1.PodReasonUnschedulable, message: "0/3 nodes are available: 3 node(s) had untolerated taint {dedicated: gpu}.", wantState: capacityapi.DemandAwaitingCapacity, wantCodes: []string{"UntoleratedTaint"}},
		{name: "PVC binding", reason: corev1.PodReasonUnschedulable, message: "pod has unbound immediate PersistentVolumeClaims", wantState: capacityapi.DemandBlocked, wantCodes: []string{"VolumeBinding"}},
		{name: "capacity plus PVC", reason: corev1.PodReasonUnschedulable, message: "0/3 nodes are available: 2 Insufficient cpu, 1 node(s) had no available persistent volumes to bind.", wantState: capacityapi.DemandBlocked, wantCodes: []string{"InsufficientResource", "VolumeBinding"}},
		{name: "opaque plugin", reason: corev1.PodReasonUnschedulable, message: "0/3 nodes are available: 3 node(s) rejected by a future scheduler plugin.", wantState: capacityapi.DemandUnknown, wantCodes: []string{"Other"}},
		{name: "capacity plus opaque", reason: corev1.PodReasonUnschedulable, message: "0/3 nodes are available: 2 Insufficient memory, 1 node(s) rejected by a future scheduler plugin.", wantState: capacityapi.DemandUnknown, wantCodes: []string{"InsufficientResource", "Other"}},
		{name: "opaque reason prevents placement confidence", reason: corev1.PodReasonUnschedulable, message: "0/3 nodes are available: 1 Insufficient memory, 1 node(s) didn't match node selector, 1 node(s) rejected by a future scheduler plugin.", wantState: capacityapi.DemandUnknown, wantCodes: []string{"InsufficientResource", "NodeAffinitySelector", "Other"}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			pod := demandTestPod("pending", "500m")
			if test.reason != "" {
				pod.Status.Conditions = []corev1.PodCondition{{
					Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: test.reason, Message: test.message,
				}}
			}
			group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}})[0]
			if group.State != test.wantState {
				t.Fatalf("state = %q, want %q: %#v", group.State, test.wantState, group.SchedulerReasons)
			}
			gotCodes := make([]string, 0, len(group.SchedulerReasons))
			for _, reason := range group.SchedulerReasons {
				gotCodes = append(gotCodes, reason.Code)
			}
			sort.Strings(gotCodes)
			wantCodes := append([]string{}, test.wantCodes...)
			sort.Strings(wantCodes)
			if !reflect.DeepEqual(gotCodes, wantCodes) {
				t.Errorf("reason codes = %v, want %v: %#v", gotCodes, wantCodes, group.SchedulerReasons)
			}
		})
	}
}

func TestDemandStateJoinsSchedulerEvidenceWithPoolCompatibility(t *testing.T) {
	pod := demandTestPod("gpu-worker", "500m")
	pod.Spec.NodeSelector = map[string]string{"accelerator": "gpu"}
	pod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/3 nodes are available: 3 node(s) didn't match Pod's node affinity/selector.",
	}}
	ready := true
	compatible := DemandPoolInput{NodePool: demandTestPool("gpu", &ready, demandPoolSpec(
		[]any{demandRequirementObject("accelerator", corev1.NodeSelectorOpIn, "gpu")}, nil, nil, nil, nil,
	), nil)}
	incompatible := DemandPoolInput{NodePool: demandTestPool("cpu", &ready, demandPoolSpec(
		[]any{demandRequirementObject("accelerator", corev1.NodeSelectorOpIn, "cpu")}, nil, nil, nil, nil,
	), nil)}

	group := BuildDemandGroups(DemandInput{
		GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{compatible},
	})[0]
	if group.State != capacityapi.DemandAwaitingCapacity || group.PoolEvaluationCounts.DeclaredCompatible != 1 {
		t.Fatalf("compatible scale-from-zero demand = %#v", group)
	}

	group = BuildDemandGroups(DemandInput{
		GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{incompatible},
	})[0]
	if group.State != capacityapi.DemandBlocked || group.PoolEvaluationCounts.Incompatible != 1 {
		t.Fatalf("demand with no compatible pool = %#v", group)
	}

	capacityPod := demandTestPod("capacity", "500m")
	capacityPod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/0 nodes are available: 1 Insufficient cpu.",
	}}
	group = BuildDemandGroups(DemandInput{
		GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{capacityPod}, Pools: []DemandPoolInput{},
	})[0]
	if group.State != capacityapi.DemandBlocked {
		t.Fatalf("capacity demand with no pools state = %q, want blocked", group.State)
	}
}

func TestBuildDemandGroupsExcludesDaemonSetPods(t *testing.T) {
	controller := true
	daemon := demandTestPod("daemon", "100m")
	daemon.OwnerReferences = []metav1.OwnerReference{{
		APIVersion: "apps/v1", Kind: "DaemonSet", Name: "agent", Controller: &controller,
	}}
	workload := demandTestPod("workload", "500m")

	groups := BuildDemandGroupModels(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{daemon, workload}})
	if len(groups) != 1 || groups[0].Group.PodsMeta.Total != 1 || groups[0].Group.Pods[0].Ref.Name != "workload" {
		t.Fatalf("demand groups = %#v, want only workload pod", groups)
	}
}

func TestDemandSchedulerReasonsAggregatePodsAndMixedEvidence(t *testing.T) {
	asOf := capacityTestTime()
	first := demandTestPod("first", "500m")
	second := demandTestPod("second", "500m")
	firstTransition := metav1.NewTime(asOf.Add(-5 * time.Minute))
	secondTransition := metav1.NewTime(asOf.Add(-2 * time.Minute))
	first.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/2 nodes are available: 2 Insufficient cpu.", LastTransitionTime: firstTransition,
	}}
	second.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/2 nodes are available: 2 Insufficient cpu.", LastTransitionTime: secondTransition,
	}}
	owner := subject.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "workers"}
	groups := BuildDemandGroups(DemandInput{
		GeneratedAt: asOf,
		Pods:        []*corev1.Pod{second, first},
		ResolvePodOwner: func(*corev1.Pod) *subject.Ref {
			copy := owner
			return &copy
		},
	})
	if len(groups) != 1 || groups[0].State != capacityapi.DemandAwaitingCapacity || len(groups[0].SchedulerReasons) != 1 {
		t.Fatalf("aggregated demand = %#v", groups)
	}
	reason := groups[0].SchedulerReasons[0]
	if reason.Code != "InsufficientResource" || reason.Count != 2 || !reason.FirstSeen.Equal(firstTransition.Time) || !reason.LastSeen.Equal(asOf) {
		t.Fatalf("aggregated scheduler reason = %#v", reason)
	}

	second.Status.Conditions[0].Message = "0/2 nodes are available: 2 node(s) rejected by a future scheduler plugin."
	mixed := BuildDemandGroups(DemandInput{
		GeneratedAt: asOf,
		Pods:        []*corev1.Pod{first, second},
		ResolvePodOwner: func(*corev1.Pod) *subject.Ref {
			copy := owner
			return &copy
		},
	})
	if len(mixed) != 1 || mixed[0].State != capacityapi.DemandUnknown {
		t.Fatalf("capacity plus opaque pod evidence state = %#v", mixed)
	}
}

func TestDemandPoolEvaluationIncludesNodeClassReadiness(t *testing.T) {
	pod := demandTestPod("worker", "500m")
	poolReady := true
	classReady := true
	classNotReady := false
	inputs := []DemandPoolInput{
		{NodePool: demandTestPool("class-ready", &poolReady, demandPoolSpecWithNodeClass(), nil), NodeClassReady: &classReady},
		{NodePool: demandTestPool("class-not-ready", &poolReady, demandPoolSpecWithNodeClass(), nil), NodeClassReady: &classNotReady},
		{NodePool: demandTestPool("class-unobserved", &poolReady, demandPoolSpecWithNodeClass(), nil)},
		{NodePool: demandTestPool("no-class-ref", &poolReady, demandPoolSpec(nil, nil, nil, nil, nil), nil)},
	}

	evaluations := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: inputs})[0].PoolEvaluations
	assertDemandEvaluation(t, evaluations, "class-ready", capacityapi.PoolDeclaredCompatible, "")
	assertDemandEvaluation(t, evaluations, "class-not-ready", capacityapi.PoolIncompatible, "nodeClassReady")
	unobserved := assertDemandEvaluation(t, evaluations, "class-unobserved", capacityapi.PoolEvaluationUnknown, "")
	assertDemandUnknown(t, unobserved, "nodeClass.readiness")
	assertDemandEvaluation(t, evaluations, "no-class-ref", capacityapi.PoolDeclaredCompatible, "")
}

func TestDemandTreatsNodePoolNameAsImplicitNodeLabel(t *testing.T) {
	pod := demandTestPod("worker", "500m")
	pod.Spec.NodeSelector = map[string]string{"karpenter.sh/nodepool": "general"}
	ready := true
	inputs := []DemandPoolInput{
		{NodePool: demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)},
		{NodePool: demandTestPool("batch", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)},
	}

	evaluations := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: inputs})[0].PoolEvaluations
	assertDemandEvaluation(t, evaluations, "general", capacityapi.PoolDeclaredCompatible, "")
	assertDemandEvaluation(t, evaluations, "batch", capacityapi.PoolIncompatible, "selectorRequirement")
}

func TestDemandDoesNotRejectUnrequestedSaturatedLimit(t *testing.T) {
	pod := demandTestPod("cpu-only", "500m")
	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, map[string]any{
		"cpu": "4", "memory": "8Gi",
	}), map[string]any{"cpu": "1", "memory": "8Gi"})

	evaluation := BuildDemandGroups(DemandInput{
		GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{{NodePool: pool, ProvisionedKnown: true}},
	})[0].PoolEvaluations[0]
	if evaluation.Result != capacityapi.PoolEvaluationUnknown {
		t.Fatalf("evaluation = %#v, want unknown because node shape overhead is not modeled", evaluation)
	}
	assertDemandUnknown(t, evaluation, "nodePool.limits.memory.capacityShape")
}

func TestDemandBoundsPoolEvaluationsWithoutChangingCounts(t *testing.T) {
	pod := demandTestPod("worker", "500m")
	ready := true
	pools := make([]DemandPoolInput, 0, DefaultDemandPoolEvaluationLimit+5)
	for index := range DefaultDemandPoolEvaluationLimit + 5 {
		pools = append(pools, DemandPoolInput{NodePool: demandTestPool(fmt.Sprintf("pool-%02d", index), &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)})
	}

	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: pools})[0]
	wantMeta := capacityapi.BoundedResultMeta{Total: len(pools), Returned: DefaultDemandPoolEvaluationLimit, Truncated: true}
	if group.PoolEvaluationsMeta != wantMeta || len(group.PoolEvaluations) != DefaultDemandPoolEvaluationLimit {
		t.Fatalf("bounded evaluations = %d %+v, want %+v", len(group.PoolEvaluations), group.PoolEvaluationsMeta, wantMeta)
	}
	if group.PoolEvaluationCounts.DeclaredCompatible != len(pools) {
		t.Fatalf("evaluation counts = %+v", group.PoolEvaluationCounts)
	}

	full := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: pools, PoolEvaluationLimit: -1})[0]
	if full.PoolEvaluationsMeta.Truncated || full.PoolEvaluationsMeta.Returned != len(pools) || len(full.PoolEvaluations) != len(pools) {
		t.Fatalf("internal full evaluations = %d %+v", len(full.PoolEvaluations), full.PoolEvaluationsMeta)
	}
}

func TestDemandTolerationOperatorDefaultCoalescesGroups(t *testing.T) {
	controller := true
	ownerRef := metav1.OwnerReference{APIVersion: "apps/v1", Kind: "ReplicaSet", Name: "web-abc", Controller: &controller}
	implicit := demandTestPod("implicit-equal", "500m")
	implicit.OwnerReferences = []metav1.OwnerReference{ownerRef}
	implicit.Spec.Tolerations = []corev1.Toleration{{Key: "dedicated", Value: "batch", Effect: corev1.TaintEffectNoSchedule}}
	explicit := demandTestPod("explicit-equal", "500m")
	explicit.OwnerReferences = []metav1.OwnerReference{ownerRef}
	explicit.Spec.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOpEqual, Value: "batch", Effect: corev1.TaintEffectNoSchedule}}

	groups := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{implicit, explicit}})
	if len(groups) != 1 || groups[0].PodCount != 2 {
		t.Fatalf("groups = %#v, want one group holding both pods — an omitted toleration operator is Equal", groups)
	}
}

func TestDemandHostnamePinIsIncompatibleNotUnknown(t *testing.T) {
	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)

	pinned := demandTestPod("pinned", "500m")
	pinned.Spec.NodeSelector = map[string]string{corev1.LabelHostname: "ip-10-0-0-1"}
	evaluation := BuildDemandGroups(DemandInput{
		GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pinned}, Pools: []DemandPoolInput{{NodePool: pool, ProvisionedKnown: true}},
	})[0].PoolEvaluations[0]
	if evaluation.Result != capacityapi.PoolIncompatible {
		t.Fatalf("evaluation = %#v, want incompatible — a provisioned node always carries a new hostname", evaluation)
	}

	excluded := demandTestPod("excluded", "500m")
	excluded.Spec.Affinity = requiredNodeAffinity([]corev1.NodeSelectorRequirement{{
		Key: corev1.LabelHostname, Operator: corev1.NodeSelectorOpNotIn, Values: []string{"ip-10-0-0-1"},
	}})
	evaluation = BuildDemandGroups(DemandInput{
		GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{excluded}, Pools: []DemandPoolInput{{NodePool: pool, ProvisionedKnown: true}},
	})[0].PoolEvaluations[0]
	if evaluation.Result != capacityapi.PoolDeclaredCompatible {
		t.Fatalf("evaluation = %#v, want declared_compatible — a fresh hostname is never in the excluded set", evaluation)
	}
	for _, predicate := range evaluation.UnknownPredicates {
		if predicate == "providerOfferings."+corev1.LabelHostname {
			t.Fatalf("hostname degraded to an offering unknown: %#v", evaluation.UnknownPredicates)
		}
	}
}

func TestDemandPoolBoundingKeepsDeclaredCompatiblePools(t *testing.T) {
	pod := demandTestPod("worker", "500m")
	pod.Spec.NodeSelector = map[string]string{"example.com/team": "a"}
	ready := true
	pools := make([]DemandPoolInput, 0, DefaultDemandPoolEvaluationLimit+6)
	for index := range DefaultDemandPoolEvaluationLimit + 5 {
		pools = append(pools, DemandPoolInput{NodePool: demandTestPool(fmt.Sprintf("pool-%02d", index), &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)})
	}
	match := demandTestPool("zz-match", &ready, demandPoolSpec([]any{
		demandRequirementObject("example.com/team", corev1.NodeSelectorOpIn, "a"),
	}, nil, nil, nil, nil), nil)
	pools = append(pools, DemandPoolInput{NodePool: match})

	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: pools})[0]
	if group.PoolEvaluationCounts.DeclaredCompatible != 1 {
		t.Fatalf("evaluation counts = %+v, want exactly one declared-compatible pool", group.PoolEvaluationCounts)
	}
	if len(group.PoolEvaluations) != DefaultDemandPoolEvaluationLimit || !group.PoolEvaluationsMeta.Truncated {
		t.Fatalf("bounded evaluations = %d %+v", len(group.PoolEvaluations), group.PoolEvaluationsMeta)
	}
	first := group.PoolEvaluations[0]
	if first.Pool.Name != "zz-match" || first.Result != capacityapi.PoolDeclaredCompatible {
		t.Fatalf("first bounded evaluation = %+v, want the declared-compatible pool to survive bounding", first)
	}
}

func TestDemandEvaluationPerspectiveNeverRewritesFleetWideState(t *testing.T) {
	ready := true
	compatible := demandTestPool("compatible", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)
	tainted := demandTestPool("tainted", &ready, demandPoolSpec(nil, []any{
		demandTaintObject("dedicated", "batch", corev1.TaintEffectNoSchedule),
	}, nil, nil, nil), nil)
	pod := demandTestPod("worker", "500m")
	pod.Status.Conditions = []corev1.PodCondition{{
		Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: corev1.PodReasonUnschedulable,
		Message: "0/3 nodes are available: 3 Insufficient cpu.",
	}}

	models := BuildDemandGroupModels(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}})
	fleet := []DemandPoolInput{{NodePool: compatible}, {NodePool: tainted}}
	ClassifyDemandGroupModels(models, fleet)
	if models[0].Group.State != capacityapi.DemandAwaitingCapacity {
		t.Fatalf("fleet-wide state = %q, want awaiting_capacity", models[0].Group.State)
	}

	narrowed := EvaluateDemandGroupModels(models, []DemandPoolInput{{NodePool: tainted}}, 0)
	if narrowed[0].State != capacityapi.DemandAwaitingCapacity {
		t.Fatalf("state under ?pool= = %q — the narrowed evaluation perspective rewrote fleet-wide state", narrowed[0].State)
	}
	if narrowed[0].PoolEvaluationCounts.Incompatible != 1 || len(narrowed[0].PoolEvaluations) != 1 {
		t.Fatalf("narrowed evaluations = %+v", narrowed[0].PoolEvaluationCounts)
	}
}

func TestDemandUnrelatedUnevaluableTolerationKeepsTaintMissProven(t *testing.T) {
	ready := true
	tainted := demandPoolSpec(nil, []any{demandTaintObject("dedicated", "batch", corev1.TaintEffectNoSchedule)}, nil, nil, nil)

	pod := demandTestPod("worker", "500m")
	pod.Spec.Tolerations = []corev1.Toleration{{Key: "unrelated", Operator: corev1.TolerationOperator("Extended"), Value: "x"}}
	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{
		{NodePool: demandTestPool("tainted", &ready, tainted, nil)},
	}})[0]
	assertDemandEvaluation(t, group.PoolEvaluations, "tainted", capacityapi.PoolIncompatible, "permanentTaint")

	mismatchedEffect := demandTestPod("effect", "500m")
	mismatchedEffect.Spec.Tolerations = []corev1.Toleration{{
		Key: "dedicated", Operator: corev1.TolerationOperator("Extended"), Value: "batch", Effect: corev1.TaintEffectNoExecute,
	}}
	group = BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{mismatchedEffect}, Pools: []DemandPoolInput{
		{NodePool: demandTestPool("tainted", &ready, tainted, nil)},
	}})[0]
	assertDemandEvaluation(t, group.PoolEvaluations, "tainted", capacityapi.PoolIncompatible, "permanentTaint")

	wildcardKey := demandTestPod("wildcard", "500m")
	wildcardKey.Spec.Tolerations = []corev1.Toleration{{Operator: corev1.TolerationOperator("Extended")}}
	group = BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{wildcardKey}, Pools: []DemandPoolInput{
		{NodePool: demandTestPool("tainted", &ready, tainted, nil)},
	}})[0]
	evaluation := assertDemandEvaluation(t, group.PoolEvaluations, "tainted", capacityapi.PoolEvaluationUnknown, "")
	assertDemandUnknown(t, evaluation, "nodePool.taints")
}

func TestDemandBoundsEvaluationEvidenceAndSignature(t *testing.T) {
	pod := demandTestPod("worker", "500m")
	pod.Spec.NodeSelector = map[string]string{}
	for index := range defaultDemandConstraintLimit + 5 {
		pod.Spec.NodeSelector[fmt.Sprintf("capacity.example.com/key-%02d", index)] = "value"
	}
	taints := make([]any, 0, defaultDemandEvidenceLimit+5)
	for index := range defaultDemandEvidenceLimit + 5 {
		taints = append(taints, map[string]any{"key": fmt.Sprintf("dedicated-%02d", index), "effect": string(corev1.TaintEffectNoSchedule)})
	}
	unknown := make([]string, 0, defaultDemandUnknownLimit+5)
	for index := range defaultDemandUnknownLimit + 5 {
		unknown = append(unknown, fmt.Sprintf("futurePredicate.%02d", index))
	}
	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, taints, nil, nil, nil), nil)

	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{{NodePool: pool, UnknownPredicates: unknown}}})[0]
	if !group.SchedulingSignature.ConstraintsMeta.Truncated || group.SchedulingSignature.ConstraintsMeta.Total != defaultDemandConstraintLimit+5 || len(group.SchedulingSignature.Constraints) != defaultDemandConstraintLimit {
		t.Fatalf("constraint boundedness = %+v, len=%d", group.SchedulingSignature.ConstraintsMeta, len(group.SchedulingSignature.Constraints))
	}
	evaluation := group.PoolEvaluations[0]
	// Evidence: every untolerated taint plus every undeclared custom label
	// (undeclared non-provider labels are incompatibility evidence, not unknowns).
	wantEvidenceTotal := (defaultDemandEvidenceLimit + 5) + (defaultDemandConstraintLimit + 5)
	if !evaluation.EvidenceMeta.Truncated || evaluation.EvidenceMeta.Total != wantEvidenceTotal || len(evaluation.Evidence) != defaultDemandEvidenceLimit {
		t.Fatalf("evidence boundedness = %+v, len=%d", evaluation.EvidenceMeta, len(evaluation.Evidence))
	}
	if !evaluation.UnknownPredicatesMeta.Truncated || evaluation.UnknownPredicatesMeta.Total < defaultDemandUnknownLimit+5 || len(evaluation.UnknownPredicates) != defaultDemandUnknownLimit {
		t.Fatalf("unknown boundedness = %+v, len=%d", evaluation.UnknownPredicatesMeta, len(evaluation.UnknownPredicates))
	}
}

func TestDemandRequiredNodeAffinityORTerms(t *testing.T) {
	ready := true
	pool := demandTestPool("environment", &ready, demandPoolSpec(
		[]any{demandRequirementObject("environment", corev1.NodeSelectorOpIn, "prod")}, nil, nil, nil, nil,
	), nil)

	compatible := demandTestPod("compatible", "100m")
	compatible.Spec.Affinity = requiredNodeAffinity(
		[]corev1.NodeSelectorRequirement{{Key: "environment", Operator: corev1.NodeSelectorOpIn, Values: []string{"dev"}}},
		[]corev1.NodeSelectorRequirement{{Key: "environment", Operator: corev1.NodeSelectorOpIn, Values: []string{"prod"}}},
	)
	compatibleEvaluation := BuildDemandGroups(DemandInput{
		GeneratedAt: time.Now().UTC(), Pods: []*corev1.Pod{compatible}, Pools: []DemandPoolInput{{NodePool: pool}},
	})[0].PoolEvaluations[0]
	if compatibleEvaluation.Result != capacityapi.PoolDeclaredCompatible {
		t.Fatalf("one compatible OR term produced %q: %#v", compatibleEvaluation.Result, compatibleEvaluation)
	}

	incompatible := demandTestPod("incompatible", "100m")
	incompatible.Spec.Affinity = requiredNodeAffinity(
		[]corev1.NodeSelectorRequirement{{Key: "environment", Operator: corev1.NodeSelectorOpIn, Values: []string{"dev"}}},
		[]corev1.NodeSelectorRequirement{{Key: "environment", Operator: corev1.NodeSelectorOpIn, Values: []string{"stage"}}},
	)
	incompatibleEvaluation := BuildDemandGroups(DemandInput{
		GeneratedAt: time.Now().UTC(), Pods: []*corev1.Pod{incompatible}, Pools: []DemandPoolInput{{NodePool: pool}},
	})[0].PoolEvaluations[0]
	if incompatibleEvaluation.Result != capacityapi.PoolIncompatible {
		t.Fatalf("all contradictory OR terms produced %q: %#v", incompatibleEvaluation.Result, incompatibleEvaluation)
	}

	emptyTerm := demandTestPod("empty-term", "100m")
	emptyTerm.Spec.Affinity = requiredNodeAffinity([]corev1.NodeSelectorRequirement{})
	emptyEvaluation := BuildDemandGroups(DemandInput{
		GeneratedAt: time.Now().UTC(), Pods: []*corev1.Pod{emptyTerm}, Pools: []DemandPoolInput{{NodePool: pool}},
	})[0].PoolEvaluations[0]
	if emptyEvaluation.Result != capacityapi.PoolIncompatible {
		t.Fatalf("empty affinity term produced %q: %#v", emptyEvaluation.Result, emptyEvaluation)
	}
}

func TestDemandRequirementsFeasibleConservativelySolvesIntersections(t *testing.T) {
	tests := []struct {
		name      string
		reqs      []demandRequirement
		feasible  bool
		supported bool
	}{
		{
			name: "combined exclusions exhaust finite set",
			reqs: []demandRequirement{
				{Operator: corev1.NodeSelectorOpIn, Values: []string{"a", "b"}},
				{Operator: corev1.NodeSelectorOpNotIn, Values: []string{"a"}},
				{Operator: corev1.NodeSelectorOpNotIn, Values: []string{"b"}},
			},
			feasible: false, supported: true,
		},
		{
			name: "not-in permits absence",
			reqs: []demandRequirement{
				{Operator: corev1.NodeSelectorOpNotIn, Values: []string{"a"}},
				{Operator: corev1.NodeSelectorOpDoesNotExist},
			},
			feasible: true, supported: true,
		},
		{
			name: "numeric interval has no integer",
			reqs: []demandRequirement{
				{Operator: corev1.NodeSelectorOpGt, Values: []string{"5"}},
				{Operator: corev1.NodeSelectorOpLt, Values: []string{"6"}},
			},
			feasible: false, supported: true,
		},
		{
			name: "numeric interval with exclusions is not overclaimed",
			reqs: []demandRequirement{
				{Operator: corev1.NodeSelectorOpGt, Values: []string{"5"}},
				{Operator: corev1.NodeSelectorOpLt, Values: []string{"8"}},
				{Operator: corev1.NodeSelectorOpNotIn, Values: []string{"6", "7"}},
			},
			feasible: false, supported: false,
		},
		{
			name:     "unknown operator",
			reqs:     []demandRequirement{{Operator: corev1.NodeSelectorOperator("Approximately"), Values: []string{"a"}}},
			feasible: false, supported: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			feasible, supported := demandRequirementsFeasible(test.reqs)
			if feasible != test.feasible || supported != test.supported {
				t.Fatalf("demandRequirementsFeasible() = %v, %v; want %v, %v", feasible, supported, test.feasible, test.supported)
			}
		})
	}
}

func demandTestPod(name, cpu string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: name, UID: types.UID(name)},
		Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Resources: corev1.ResourceRequirements{Requests: resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: cpu})},
		}}},
		Status: corev1.PodStatus{Phase: corev1.PodPending},
	}
}

func requiredNodeAffinity(terms ...[]corev1.NodeSelectorRequirement) *corev1.Affinity {
	selectorTerms := make([]corev1.NodeSelectorTerm, 0, len(terms))
	for _, term := range terms {
		selectorTerms = append(selectorTerms, corev1.NodeSelectorTerm{MatchExpressions: term})
	}
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: selectorTerms},
	}}
}

func demandPoolSpec(requirements, taints, startupTaints []any, replicas *int64, limits map[string]any) map[string]any {
	templateSpec := map[string]any{}
	if requirements != nil {
		templateSpec["requirements"] = requirements
	}
	if taints != nil {
		templateSpec["taints"] = taints
	}
	if startupTaints != nil {
		templateSpec["startupTaints"] = startupTaints
	}
	spec := map[string]any{"template": map[string]any{"spec": templateSpec}}
	if replicas != nil {
		spec["replicas"] = *replicas
	}
	if limits != nil {
		spec["limits"] = limits
	}
	return spec
}

func demandPoolSpecWithNodeClass() map[string]any {
	spec := demandPoolSpec(nil, nil, nil, nil, nil)
	template := spec["template"].(map[string]any)
	templateSpec := template["spec"].(map[string]any)
	templateSpec["nodeClassRef"] = map[string]any{
		"group": "karpenter.k8s.aws", "kind": "EC2NodeClass", "name": "default",
	}
	return spec
}

func demandRequirementObject(key string, operator corev1.NodeSelectorOperator, values ...string) map[string]any {
	rawValues := make([]any, 0, len(values))
	for _, value := range values {
		rawValues = append(rawValues, value)
	}
	return map[string]any{"key": key, "operator": string(operator), "values": rawValues}
}

func demandTaintObject(key, value string, effect corev1.TaintEffect) map[string]any {
	return map[string]any{"key": key, "value": value, "effect": string(effect)}
}

func demandTestPool(name string, ready *bool, spec, resources map[string]any) *unstructured.Unstructured {
	object := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata":   map[string]any{"name": name, "generation": int64(1)},
		"spec":       spec,
	}}
	status := map[string]any{}
	if ready != nil {
		conditionStatus := "False"
		if *ready {
			conditionStatus = "True"
		}
		status["conditions"] = []any{map[string]any{
			"type": "Ready", "status": conditionStatus, "reason": "Test", "observedGeneration": int64(1),
			"lastTransitionTime": "2026-07-13T12:00:00Z",
		}}
	}
	if resources != nil {
		status["resources"] = resources
	}
	if len(status) > 0 {
		object.Object["status"] = status
	}
	return object
}

func demandGroupWithPodCount(t *testing.T, groups []capacityapi.DemandGroup, podCount int) capacityapi.DemandGroup {
	t.Helper()
	for _, group := range groups {
		if group.PodCount == podCount {
			return group
		}
	}
	t.Fatalf("no demand group with %d pods in %#v", podCount, groups)
	return capacityapi.DemandGroup{}
}

func demandGroupIDs(groups []capacityapi.DemandGroup) []string {
	result := make([]string, 0, len(groups))
	for _, group := range groups {
		result = append(result, group.ID)
	}
	sort.Strings(result)
	return result
}

func assertDemandEvaluation(t *testing.T, evaluations []capacityapi.PoolEvaluation, pool string, result capacityapi.PoolEvaluationResult, evidencePredicate string) capacityapi.PoolEvaluation {
	t.Helper()
	for _, evaluation := range evaluations {
		if evaluation.Pool.Name != pool {
			continue
		}
		if evaluation.Result != result {
			t.Fatalf("pool %s result = %q, want %q: %#v", pool, evaluation.Result, result, evaluation)
		}
		if evidencePredicate != "" {
			for _, evidence := range evaluation.Evidence {
				if evidence.Predicate == evidencePredicate {
					return evaluation
				}
			}
			t.Fatalf("pool %s has no %q evidence: %#v", pool, evidencePredicate, evaluation)
		}
		return evaluation
	}
	t.Fatalf("no evaluation for pool %s in %#v", pool, evaluations)
	return capacityapi.PoolEvaluation{}
}

func assertDemandUnknown(t *testing.T, evaluation capacityapi.PoolEvaluation, predicate string) {
	t.Helper()
	for _, unknown := range evaluation.UnknownPredicates {
		if unknown == predicate {
			return
		}
	}
	t.Fatalf("unknown predicate %q absent from %#v", predicate, evaluation.UnknownPredicates)
}

func TestDemandUndeclaredCustomLabelIsIncompatibleNotUnknown(t *testing.T) {
	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)
	pod := demandTestPod("worker", "500m")
	// The classic Karpenter misconfiguration: Karpenter only applies labels
	// declared in pool requirements/template labels, so this can never match.
	pod.Spec.NodeSelector = map[string]string{"team": "ml"}
	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{{NodePool: pool}}})[0]
	if group.PoolEvaluations[0].Result != capacityapi.PoolIncompatible {
		t.Fatalf("undeclared custom label = %q, want incompatible", group.PoolEvaluations[0].Result)
	}

	// Provider/well-known labels stay honestly unknown — the offering
	// catalogue can supply them without pool declaration.
	pod.Spec.NodeSelector = map[string]string{"node.kubernetes.io/instance-type": "m7g.large"}
	group = BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{{NodePool: pool}}})[0]
	if group.PoolEvaluations[0].Result != capacityapi.PoolEvaluationUnknown {
		t.Fatalf("provider offering label = %q, want unknown", group.PoolEvaluations[0].Result)
	}
}

func TestDemandPoolBoundedProviderLabelIsDeclaredCompatible(t *testing.T) {
	ready := true
	pod := demandTestPod("spot-worker", "500m")
	pod.Spec.NodeSelector = map[string]string{"karpenter.sh/capacity-type": "spot"}

	bounded := demandTestPool("bounded", &ready, demandPoolSpec(
		[]any{demandRequirementObject("karpenter.sh/capacity-type", corev1.NodeSelectorOpIn, "spot", "on-demand")}, nil, nil, nil, nil,
	), nil)
	disjoint := demandTestPool("disjoint", &ready, demandPoolSpec(
		[]any{demandRequirementObject("karpenter.sh/capacity-type", corev1.NodeSelectorOpIn, "on-demand")}, nil, nil, nil, nil,
	), nil)
	unbounded := demandTestPool("unbounded", &ready, demandPoolSpec(
		[]any{demandRequirementObject("karpenter.sh/capacity-type", corev1.NodeSelectorOpExists)}, nil, nil, nil, nil,
	), nil)

	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{
		{NodePool: bounded}, {NodePool: disjoint}, {NodePool: unbounded},
	}})[0]
	// The pool's In requirement bounds the label's values and the pod's need
	// intersects them — the declaration answers compatibility on its own.
	assertDemandEvaluation(t, group.PoolEvaluations, "bounded", capacityapi.PoolDeclaredCompatible, "")
	assertDemandEvaluation(t, group.PoolEvaluations, "disjoint", capacityapi.PoolIncompatible, "selectorRequirement")
	unbound := assertDemandEvaluation(t, group.PoolEvaluations, "unbounded", capacityapi.PoolEvaluationUnknown, "")
	assertDemandUnknown(t, unbound, "providerOfferings.karpenter.sh/capacity-type")
}

func TestDemandUnevaluableTolerationKeepsTaintMissUnknown(t *testing.T) {
	ready := true
	tainted := demandPoolSpec(nil, []any{demandTaintObject("dedicated", "batch", corev1.TaintEffectNoSchedule)}, nil, nil, nil)

	pod := demandTestPod("worker", "500m")
	pod.Spec.Tolerations = []corev1.Toleration{{Key: "dedicated", Operator: corev1.TolerationOperator("Extended"), Value: "batch"}}
	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{
		{NodePool: demandTestPool("tainted", &ready, tainted, nil)},
	}})[0]
	// The operator cannot be evaluated, so the taint miss is unprovable —
	// hard permanentTaint evidence would outrank the unknown and overclaim.
	evaluation := assertDemandEvaluation(t, group.PoolEvaluations, "tainted", capacityapi.PoolEvaluationUnknown, "")
	assertDemandUnknown(t, evaluation, "nodePool.taints")
	assertDemandUnknown(t, evaluation, "toleration.operator")

	// Without any toleration the miss is a proven incompatibility.
	bare := demandTestPod("bare", "500m")
	group = BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{bare}, Pools: []DemandPoolInput{
		{NodePool: demandTestPool("tainted", &ready, tainted, nil)},
	}})[0]
	assertDemandEvaluation(t, group.PoolEvaluations, "tainted", capacityapi.PoolIncompatible, "permanentTaint")
}

func TestDemandMinValuesOnlyBlocksWhenMergedSetIsUncertain(t *testing.T) {
	ready := true
	pod := demandTestPod("worker", "500m")
	minTwo := int64(2)
	minThree := int64(3)

	haSpec := demandPoolSpec([]any{
		map[string]any{"key": "node.kubernetes.io/instance-type", "operator": "In", "values": []any{"m5.large", "m5.xlarge", "c5.large"}, "minValues": minTwo},
	}, nil, nil, nil, nil)
	misconfiguredSpec := demandPoolSpec([]any{
		map[string]any{"key": "node.kubernetes.io/instance-type", "operator": "In", "values": []any{"m5.large", "m5.xlarge"}, "minValues": minThree},
	}, nil, nil, nil, nil)

	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{
		{NodePool: demandTestPool("ha", &ready, haSpec, nil)},
		{NodePool: demandTestPool("misconfigured", &ready, misconfiguredSpec, nil)},
	}})[0]
	// The pod imposes nothing on instance-type, so the HA pool's own In set
	// answers its minValues — the common diversity pattern must not be locked
	// out of declared compatibility.
	assertDemandEvaluation(t, group.PoolEvaluations, "ha", capacityapi.PoolDeclaredCompatible, "")
	misconfigured := assertDemandEvaluation(t, group.PoolEvaluations, "misconfigured", capacityapi.PoolEvaluationUnknown, "")
	assertDemandUnknown(t, misconfigured, "nodePool.requirements.node.kubernetes.io/instance-type.minValues")

	// A pod that constrains the key narrows the merged set — honest unknown.
	pinned := demandTestPod("pinned", "500m")
	pinned.Spec.NodeSelector = map[string]string{"node.kubernetes.io/instance-type": "m5.large"}
	group = BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pinned}, Pools: []DemandPoolInput{
		{NodePool: demandTestPool("ha", &ready, haSpec, nil)},
	}})[0]
	pinnedEvaluation := assertDemandEvaluation(t, group.PoolEvaluations, "ha", capacityapi.PoolEvaluationUnknown, "")
	assertDemandUnknown(t, pinnedEvaluation, "nodePool.requirements.node.kubernetes.io/instance-type.minValues")
}

func TestDemandInstanceShapeMissingResourceIsUnknownNotFit(t *testing.T) {
	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)
	cpuOnlyShape := []corev1.ResourceList{resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "48", corev1.ResourcePods: "110"})}

	pod := demandTestPod("worker", "8")
	pod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("4Gi")
	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{{NodePool: pool, ObservedMemberShapes: cpuOnlyShape}}})[0]
	if group.PoolEvaluations[0].Result != capacityapi.PoolEvaluationUnknown {
		t.Fatalf("memory request vs memory-less shape = %q, want unknown (absent resource cannot prove the fit)", group.PoolEvaluations[0].Result)
	}

	// A claim-derived shape without pods (launching claims omit it) must not
	// fail the fit — every request carries a synthetic pods=1, and pod slots
	// are not an instance-shape dimension.
	podlessShape := []corev1.ResourceList{resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "48", corev1.ResourceMemory: "96Gi"})}
	fits := demandTestPod("fits-podless", "8")
	fits.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("4Gi")
	group = BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{fits}, Pools: []DemandPoolInput{{NodePool: pool, ObservedMemberShapes: podlessShape}}})[0]
	if group.PoolEvaluations[0].Result != capacityapi.PoolDeclaredCompatible {
		t.Fatalf("podless claim shape = %q, want declared compatible", group.PoolEvaluations[0].Result)
	}

	// An explicit zero request for an absent resource still fits.
	zero := demandTestPod("zero", "8")
	zero.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("0")
	group = BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{zero}, Pools: []DemandPoolInput{{NodePool: pool, ObservedMemberShapes: cpuOnlyShape}}})[0]
	if group.PoolEvaluations[0].Result != capacityapi.PoolDeclaredCompatible {
		t.Fatalf("zero request vs absent resource = %q, want declared compatible", group.PoolEvaluations[0].Result)
	}
}

func TestDemandInstanceShapeExceedingObservedCapacityIsUnknown(t *testing.T) {
	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)
	shapes := []corev1.ResourceList{resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "48", corev1.ResourceMemory: "96Gi", corev1.ResourcePods: "110"})}

	oversized := demandTestPod("oversized", "96")
	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{oversized}, Pools: []DemandPoolInput{{NodePool: pool, ObservedMemberShapes: shapes}}})[0]
	evaluation := group.PoolEvaluations[0]
	if evaluation.Result != capacityapi.PoolEvaluationUnknown {
		t.Fatalf("96-CPU pod vs 48-CPU largest member = %q, want unknown (never a bare compatible)", evaluation.Result)
	}
	found := false
	for _, predicate := range evaluation.UnknownPredicates {
		if strings.HasPrefix(predicate, "instanceShape.") {
			found = true
		}
	}
	if !found {
		t.Fatalf("unknown predicates = %v, want an instanceShape predicate", evaluation.UnknownPredicates)
	}

	fits := demandTestPod("fits", "8")
	group = BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{fits}, Pools: []DemandPoolInput{{NodePool: pool, ObservedMemberShapes: shapes}}})[0]
	if group.PoolEvaluations[0].Result != capacityapi.PoolDeclaredCompatible {
		t.Fatalf("fitting pod = %q, want declared compatible", group.PoolEvaluations[0].Result)
	}

	// No observed members -> nothing can be inferred; no fabricated unknown.
	group = BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{demandTestPod("fresh", "96")}, Pools: []DemandPoolInput{{NodePool: pool}}})[0]
	if group.PoolEvaluations[0].Result != capacityapi.PoolDeclaredCompatible {
		t.Fatalf("no observed members = %q, want declared compatible", group.PoolEvaluations[0].Result)
	}
}

func TestDemandInstanceShapeComparesWholeVectorsNotPerResourceMaxima(t *testing.T) {
	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)
	// Biggest CPU and biggest memory live on DIFFERENT instance shapes — a
	// composite per-resource max would fabricate an instance that fits.
	shapes := []corev1.ResourceList{
		resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "48", corev1.ResourceMemory: "32Gi", corev1.ResourcePods: "110"}),
		resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "8", corev1.ResourceMemory: "128Gi", corev1.ResourcePods: "110"}),
	}
	pod := demandTestPod("wide", "32")
	pod.Spec.Containers[0].Resources.Requests[corev1.ResourceMemory] = resource.MustParse("64Gi")
	group := BuildDemandGroups(DemandInput{GeneratedAt: capacityTestTime(), Pods: []*corev1.Pod{pod}, Pools: []DemandPoolInput{{NodePool: pool, ObservedMemberShapes: shapes}}})[0]
	if group.PoolEvaluations[0].Result != capacityapi.PoolEvaluationUnknown {
		t.Fatalf("32cpu+64Gi pod vs {48cpu/32Gi, 8cpu/128Gi} shapes = %q, want unknown", group.PoolEvaluations[0].Result)
	}
}

// TestObservedMemberShapesOnlyRecordSchedulableEvidence pins the direction of
// every exclusion. A recorded shape is AFFIRMATIVE evidence — a pod that fits
// one is spared the instanceShape unknown — so a reading that is not the
// schedulable truth must never become one.
func TestObservedMemberShapesOnlyRecordSchedulableEvidence(t *testing.T) {
	ready := true
	pool := demandTestPool("general", &ready, demandPoolSpec(nil, nil, nil, nil, nil), nil)
	labelled := func(claim *unstructured.Unstructured) *unstructured.Unstructured {
		claim.SetLabels(map[string]string{karpenter.NodePoolLabelKey: "general"})
		return claim
	}
	evaluate := func(cpu string, shapes []corev1.ResourceList) capacityapi.PoolEvaluationResult {
		group := BuildDemandGroups(DemandInput{
			GeneratedAt: capacityTestTime(),
			Pods:        []*corev1.Pod{demandTestPod("candidate", cpu)},
			Pools:       []DemandPoolInput{{NodePool: pool, ObservedMemberShapes: shapes}},
		})[0]
		return group.PoolEvaluations[0].Result
	}

	// An allocatable-bearing claim is real schedulable evidence and is recorded
	// at its allocatable value, never its capacity.
	withAllocatable := labelled(capacityTestClaim("claim-a", "claim-uid", pool, map[string]any{
		"capacity":    map[string]any{"cpu": "8", "memory": "32Gi", "pods": "110"},
		"allocatable": map[string]any{"cpu": "7", "memory": "30Gi", "pods": "110"},
	}))
	shapes := ObservedMemberShapesByPool(nil, []*unstructured.Unstructured{withAllocatable})["general"]
	if len(shapes) != 1 {
		t.Fatalf("shapes = %#v, want the claim's allocatable vector", shapes)
	}
	if got := shapes[0][corev1.ResourceCPU]; got.String() != "7" {
		t.Fatalf("shape cpu = %s, want the allocatable 7 (capacity 8 overstates what the scheduler can place)", got.String())
	}
	if got := evaluate("8", shapes); got != capacityapi.PoolEvaluationUnknown {
		t.Fatalf("8-CPU pod vs 7-CPU allocatable = %q, want unknown", got)
	}
	if got := evaluate("7", shapes); got != capacityapi.PoolDeclaredCompatible {
		t.Fatalf("7-CPU pod vs 7-CPU allocatable = %q, want declared compatible", got)
	}

	// A capacity-only claim proves nothing schedulable. Recording it would let
	// a pod that fits the raw machine — but not the node the kubelet will
	// actually offer — clear the shape check.
	capacityOnly := labelled(capacityTestClaim("claim-b", "claim-b-uid", pool, map[string]any{
		"capacity": map[string]any{"cpu": "16", "memory": "64Gi", "pods": "110"},
	}))
	if got := ObservedMemberShapesByPool(nil, []*unstructured.Unstructured{capacityOnly})["general"]; len(got) != 0 {
		t.Fatalf("capacity-only claim recorded a schedulable shape: %#v", got)
	}
	// With that claim as the pool's only member, a 16-CPU pod stays unknown —
	// no shape existed to prove it, and none was fabricated.
	mixed := ObservedMemberShapesByPool(nil, []*unstructured.Unstructured{withAllocatable, capacityOnly})["general"]
	if got := evaluate("16", mixed); got != capacityapi.PoolEvaluationUnknown {
		t.Fatalf("16-CPU pod against a capacity-only claim = %q, want unknown", got)
	}

	// A claim bound to an observed node is redundant with that node's live
	// allocatable, and its own reading must never outweigh it.
	node := groupsTestNodeAlloc("ip-10-0-0-1", map[string]string{karpenter.NodePoolLabelKey: "general"},
		resourceList(map[corev1.ResourceName]string{corev1.ResourceCPU: "4", corev1.ResourceMemory: "16Gi"}))
	bound := labelled(capacityTestClaim("claim-c", "claim-c-uid", pool, map[string]any{
		"nodeName":    "ip-10-0-0-1",
		"allocatable": map[string]any{"cpu": "32", "memory": "128Gi", "pods": "110"},
	}))
	linked := ObservedMemberShapesByPool([]*corev1.Node{node}, []*unstructured.Unstructured{bound})["general"]
	if len(linked) != 1 {
		t.Fatalf("linked node+claim recorded %d shapes, want only the node's: %#v", len(linked), linked)
	}
	if got := linked[0][corev1.ResourceCPU]; got.String() != "4" {
		t.Fatalf("linked shape cpu = %s, want the live node's 4", got.String())
	}
	if got := evaluate("32", linked); got != capacityapi.PoolEvaluationUnknown {
		t.Fatalf("32-CPU pod against a 4-CPU live node = %q, want unknown — the claim's estimate must not outweigh the node", got)
	}

	// An unbound claim (still launching, no node yet) is not redundant with
	// anything, so its allocatable still counts.
	unbound := labelled(capacityTestClaim("claim-d", "claim-d-uid", pool, map[string]any{
		"allocatable": map[string]any{"cpu": "12", "memory": "48Gi", "pods": "110"},
	}))
	inFlight := ObservedMemberShapesByPool([]*corev1.Node{node}, []*unstructured.Unstructured{unbound})["general"]
	if len(inFlight) != 2 {
		t.Fatalf("unbound claim shapes = %#v, want the node's and the claim's", inFlight)
	}
}
