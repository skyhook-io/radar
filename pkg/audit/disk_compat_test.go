package audit

import (
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func pdNode(name, machineType string, taints ...corev1.Taint) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			"node.kubernetes.io/instance-type": machineType,
			"kubernetes.io/hostname":           name,
		}},
		Spec: corev1.NodeSpec{Taints: taints},
	}
}

func pdPod(name, node string, owner *metav1.OwnerReference, claims ...string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "redis"},
		Spec:       corev1.PodSpec{NodeName: node},
		Status:     corev1.PodStatus{Phase: corev1.PodRunning},
	}
	if owner != nil {
		pod.OwnerReferences = []metav1.OwnerReference{*owner}
	}
	for _, c := range claims {
		pod.Spec.Volumes = append(pod.Spec.Volumes, corev1.Volume{
			Name:         c,
			VolumeSource: corev1.VolumeSource{PersistentVolumeClaim: &corev1.PersistentVolumeClaimVolumeSource{ClaimName: c}},
		})
	}
	return pod
}

func controllerRef(apiVersion, kind, name string, uid types.UID) *metav1.OwnerReference {
	yes := true
	return &metav1.OwnerReference{APIVersion: apiVersion, Kind: kind, Name: name, UID: uid, Controller: &yes}
}

func runPDPlacement(input *CheckInput) ([]Finding, *evalTracker) {
	tr := newEvalTracker()
	return checkPDVolumeHyperdiskPlacement(tr, input), tr
}

// The prod shape before the outage: a Redis StatefulSet on pd-ssd with no
// node constraints, in a cluster that also runs an N4D pool.
func TestPDPlacement_UnpinnedStatefulSetReachesHyperdiskOnlyNode(t *testing.T) {
	sts := controllerRef("apps/v1", "StatefulSet", "redis-master", "sts-uid")
	input := &CheckInput{
		Pods: []*corev1.Pod{pdPod("redis-master-0", "e2-node", sts, "redis-data-redis-master-0")},
		PDPlacement: &PDPlacementEvidence{
			HyperdiskOnlyNodes: []*corev1.Node{pdNode("n4d-node", "n4d-standard-4")},
			ClaimDiskTypes:     map[string]string{"redis/redis-data-redis-master-0": "pd-ssd"},
		},
	}
	findings, tr := runPDPlacement(input)
	if len(findings) != 1 {
		t.Fatalf("want 1 finding, got %+v", findings)
	}
	f := findings[0]
	if f.Kind != "StatefulSet" || f.Group != "apps" || f.Name != "redis-master" || f.Severity != SeverityWarning || f.Category != CategoryReliability {
		t.Errorf("finding = %+v", f)
	}
	for _, want := range []string{"pd-ssd volume (redis-data-redis-master-0)", "n4d-node (n4d-standard-4)"} {
		if !strings.Contains(f.Message, want) {
			t.Errorf("message %q missing %q", f.Message, want)
		}
	}
	if tr.counts[checkPDVolumeHyperdiskOnlyNode]["redis"] != 1 {
		t.Errorf("subject not recorded as evaluated: %+v", tr.counts)
	}
}

func TestPDPlacement_PodAlreadyOnHyperdiskOnlyNodeIsDanger(t *testing.T) {
	sts := controllerRef("apps/v1", "StatefulSet", "redis-master", "sts-uid")
	input := &CheckInput{
		Pods: []*corev1.Pod{pdPod("redis-master-0", "n4d-node", sts, "data")},
		PDPlacement: &PDPlacementEvidence{
			HyperdiskOnlyNodes: []*corev1.Node{pdNode("n4d-node", "n4d-standard-4")},
			ClaimDiskTypes:     map[string]string{"redis/data": "pd-ssd"},
		},
	}
	findings, _ := runPDPlacement(input)
	if len(findings) != 1 || findings[0].Severity != SeverityDanger {
		t.Fatalf("want one danger finding, got %+v", findings)
	}
	if !strings.Contains(findings[0].Message, "redis-master-0 on n4d-node (n4d-standard-4)") {
		t.Errorf("message = %q", findings[0].Message)
	}
}

func TestPDPlacement_ConstraintsThatKeepPodsOff(t *testing.T) {
	ev := &PDPlacementEvidence{
		HyperdiskOnlyNodes: []*corev1.Node{pdNode("n4d-node", "n4d-standard-4",
			corev1.Taint{Key: "pool", Value: "hyperdisk", Effect: corev1.TaintEffectNoSchedule})},
		ClaimDiskTypes: map[string]string{"redis/data": "pd-ssd"},
	}
	untainted := &PDPlacementEvidence{
		HyperdiskOnlyNodes: []*corev1.Node{pdNode("n4d-node", "n4d-standard-4")},
		ClaimDiskTypes:     ev.ClaimDiskTypes,
	}

	pinned := pdPod("redis-master-0", "e2-node", nil, "data")
	pinned.Spec.NodeSelector = map[string]string{"node.kubernetes.io/instance-type": "e2-standard-2"}

	affinity := pdPod("redis-master-0", "e2-node", nil, "data")
	affinity.Spec.Affinity = &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "node.kubernetes.io/instance-type", Operator: corev1.NodeSelectorOpNotIn, Values: []string{"n4d-standard-4"}}},
		}}},
	}}

	cases := []struct {
		name string
		pod  *corev1.Pod
		ev   *PDPlacementEvidence
	}{
		{"nodeSelector pins to e2", pinned, untainted},
		{"required affinity excludes n4d", affinity, untainted},
		{"untolerated NoSchedule taint", pdPod("redis-master-0", "e2-node", nil, "data"), ev},
	}
	for _, tc := range cases {
		findings, tr := runPDPlacement(&CheckInput{Pods: []*corev1.Pod{tc.pod}, PDPlacement: tc.ev})
		if len(findings) != 0 {
			t.Errorf("%s: want no finding, got %+v", tc.name, findings)
		}
		if tr.counts[checkPDVolumeHyperdiskOnlyNode]["redis"] != 1 {
			t.Errorf("%s: a constrained subject still counts as evaluated and passing", tc.name)
		}
	}

	tolerating := pdPod("redis-master-0", "e2-node", nil, "data")
	tolerating.Spec.Tolerations = []corev1.Toleration{{Key: "pool", Operator: corev1.TolerationOpEqual, Value: "hyperdisk", Effect: corev1.TaintEffectNoSchedule}}
	if findings, _ := runPDPlacement(&CheckInput{Pods: []*corev1.Pod{tolerating}, PDPlacement: ev}); len(findings) != 1 {
		t.Errorf("a pod tolerating the taint can still land there: %+v", findings)
	}
}

// A zonal disk can only follow its pod to nodes in the disk's zone; an N4D
// node elsewhere is unreachable for this workload.
func TestPDPlacement_VolumeZoneKeepsPodOffOtherZones(t *testing.T) {
	zoneA := &corev1.PersistentVolume{Spec: corev1.PersistentVolumeSpec{NodeAffinity: &corev1.VolumeNodeAffinity{
		Required: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{{
			MatchExpressions: []corev1.NodeSelectorRequirement{{Key: "topology.gke.io/zone", Operator: corev1.NodeSelectorOpIn, Values: []string{"us-east1-a"}}},
		}}},
	}}}
	n4dIn := func(zone string) *corev1.Node {
		n := pdNode("n4d-"+zone, "n4d-standard-4")
		n.Labels["topology.gke.io/zone"] = zone
		return n
	}
	input := func(node *corev1.Node) *CheckInput {
		return &CheckInput{
			Pods: []*corev1.Pod{pdPod("redis-master-0", "e2-node", nil, "data")},
			PDPlacement: &PDPlacementEvidence{
				HyperdiskOnlyNodes: []*corev1.Node{node},
				ClaimDiskTypes:     map[string]string{"redis/data": "pd-ssd"},
				ClaimVolumes:       map[string]*corev1.PersistentVolume{"redis/data": zoneA},
			},
		}
	}
	if findings, _ := runPDPlacement(input(n4dIn("us-east1-b"))); len(findings) != 0 {
		t.Errorf("N4D node outside the disk's zone is unreachable: %+v", findings)
	}
	if findings, _ := runPDPlacement(input(n4dIn("us-east1-a"))); len(findings) != 1 {
		t.Errorf("N4D node in the disk's zone is reachable: %+v", findings)
	}
}

// Older in-tree volumes carry their zone as a label instead of node affinity.
func TestPDPlacement_LegacyVolumeZoneLabel(t *testing.T) {
	cases := []struct {
		name, pvLabel, nodeLabel, allowed, nodeValue string
		want                                         int
	}{
		{"same zone label, outside", corev1.LabelTopologyZone, corev1.LabelTopologyZone, "us-east1-a__us-east1-c", "us-east1-b", 0},
		{"same zone label, inside", corev1.LabelTopologyZone, corev1.LabelTopologyZone, "us-east1-a__us-east1-c", "us-east1-c", 1},
		{"beta zone on stable node, outside", corev1.LabelFailureDomainBetaZone, corev1.LabelTopologyZone, "us-east1-a__us-east1-c", "us-east1-b", 0},
		{"beta zone on stable node, inside", corev1.LabelFailureDomainBetaZone, corev1.LabelTopologyZone, "us-east1-a__us-east1-c", "us-east1-c", 1},
		{"beta region on stable node, outside", corev1.LabelFailureDomainBetaRegion, corev1.LabelTopologyRegion, "us-east1", "us-west1", 0},
		{"beta region on stable node, inside", corev1.LabelFailureDomainBetaRegion, corev1.LabelTopologyRegion, "us-east1", "us-east1", 1},
	}
	for _, tc := range cases {
		pv := &corev1.PersistentVolume{ObjectMeta: metav1.ObjectMeta{Labels: map[string]string{tc.pvLabel: tc.allowed}}}
		node := pdNode("n4d-node", "n4d-standard-4")
		node.Labels[tc.nodeLabel] = tc.nodeValue
		findings, _ := runPDPlacement(&CheckInput{
			Pods: []*corev1.Pod{pdPod("app-0", "e2-node", nil, "data")},
			PDPlacement: &PDPlacementEvidence{
				HyperdiskOnlyNodes: []*corev1.Node{node},
				ClaimDiskTypes:     map[string]string{"redis/data": "pd-balanced"},
				ClaimVolumes:       map[string]*corev1.PersistentVolume{"redis/data": pv},
			},
		})
		if len(findings) != tc.want {
			t.Errorf("%s: want %d findings, got %+v", tc.name, tc.want, findings)
		}
	}
}

// Replacement pods follow the StatefulSet's current template, so a pin added
// to the template clears the warning, and one removed from it raises it, even
// while the running pod still has the old spec.
func TestPDPlacement_ConstraintsComeFromOwnerTemplate(t *testing.T) {
	pinned := corev1.PodSpec{NodeSelector: map[string]string{"node.kubernetes.io/instance-type": "e2-standard-2"}}
	sts := func(spec corev1.PodSpec) *appsv1.StatefulSet {
		return &appsv1.StatefulSet{
			ObjectMeta: metav1.ObjectMeta{Name: "redis-master", Namespace: "redis"},
			Spec:       appsv1.StatefulSetSpec{Template: corev1.PodTemplateSpec{Spec: spec}},
		}
	}
	ref := controllerRef("apps/v1", "StatefulSet", "redis-master", "sts-uid")
	ev := &PDPlacementEvidence{
		HyperdiskOnlyNodes: []*corev1.Node{pdNode("n4d-node", "n4d-standard-4")},
		ClaimDiskTypes:     map[string]string{"redis/data": "pd-ssd"},
	}

	oldPinnedPod := pdPod("redis-master-0", "e2-node", ref, "data")
	oldPinnedPod.Spec.NodeSelector = pinned.NodeSelector
	findings, _ := runPDPlacement(&CheckInput{Pods: []*corev1.Pod{oldPinnedPod}, StatefulSets: []*appsv1.StatefulSet{sts(corev1.PodSpec{})}, PDPlacement: ev})
	if len(findings) != 1 {
		t.Errorf("template no longer pins, so replacements can reach N4D: %+v", findings)
	}

	unpinnedPod := pdPod("redis-master-0", "e2-node", ref, "data")
	findings, _ = runPDPlacement(&CheckInput{Pods: []*corev1.Pod{unpinnedPod}, StatefulSets: []*appsv1.StatefulSet{sts(pinned)}, PDPlacement: ev})
	if len(findings) != 0 {
		t.Errorf("template pin covers replacements: %+v", findings)
	}
}

func TestPDPlacement_NotApplicable(t *testing.T) {
	pod := pdPod("app-0", "e2-node", nil, "data")
	cases := []struct {
		name  string
		input *CheckInput
	}{
		{"no evidence", &CheckInput{Pods: []*corev1.Pod{pod}}},
		{"no Hyperdisk-only nodes", &CheckInput{Pods: []*corev1.Pod{pod}, PDPlacement: &PDPlacementEvidence{
			ClaimDiskTypes: map[string]string{"redis/data": "pd-ssd"},
		}}},
		{"Hyperdisk volume", &CheckInput{Pods: []*corev1.Pod{pod}, PDPlacement: &PDPlacementEvidence{
			HyperdiskOnlyNodes: []*corev1.Node{pdNode("n4d-node", "n4d-standard-4")},
			ClaimDiskTypes:     map[string]string{"redis/data": "hyperdisk-balanced"},
		}}},
		{"claim not on a GCE disk", &CheckInput{Pods: []*corev1.Pod{pod}, PDPlacement: &PDPlacementEvidence{
			HyperdiskOnlyNodes: []*corev1.Node{pdNode("n4d-node", "n4d-standard-4")},
			ClaimDiskTypes:     map[string]string{},
		}}},
	}
	for _, tc := range cases {
		findings, tr := runPDPlacement(tc.input)
		if len(findings) != 0 || len(tr.counts[checkPDVolumeHyperdiskOnlyNode]) != 0 {
			t.Errorf("%s: want nothing evaluated, got findings %+v counts %+v", tc.name, findings, tr.counts)
		}
	}
}

// Replicas of one Deployment produce one finding on the Deployment.
func TestPDPlacement_DeploymentReplicasAttributeToDeployment(t *testing.T) {
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{
		Name: "web-abc", Namespace: "redis", UID: "rs-uid",
		OwnerReferences: []metav1.OwnerReference{*controllerRef("apps/v1", "Deployment", "web", "dep-uid")},
	}}
	rsRef := controllerRef("apps/v1", "ReplicaSet", "web-abc", "rs-uid")
	input := &CheckInput{
		Pods:        []*corev1.Pod{pdPod("web-abc-1", "e2-a", rsRef, "shared"), pdPod("web-abc-2", "e2-b", rsRef, "shared")},
		ReplicaSets: []*appsv1.ReplicaSet{rs},
		PDPlacement: &PDPlacementEvidence{
			HyperdiskOnlyNodes: []*corev1.Node{pdNode("c4-node", "c4-standard-8")},
			ClaimDiskTypes:     map[string]string{"redis/shared": "pd-balanced"},
		},
	}
	findings, tr := runPDPlacement(input)
	if len(findings) != 1 || findings[0].Kind != "Deployment" || findings[0].Name != "web" {
		t.Fatalf("want one Deployment finding, got %+v", findings)
	}
	if tr.counts[checkPDVolumeHyperdiskOnlyNode]["redis"] != 1 {
		t.Errorf("replicas must count as one subject: %+v", tr.counts)
	}
}
