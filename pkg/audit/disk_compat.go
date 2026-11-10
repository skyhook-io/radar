package audit

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	volumehelpers "k8s.io/component-helpers/storage/volume"
	"k8s.io/klog/v2"

	"github.com/skyhook-io/radar/pkg/gkedisk"
)

const checkPDVolumeHyperdiskOnlyNode = "pdVolumeHyperdiskOnlyNode"

// PDPlacementEvidence is what the Persistent Disk placement check needs from
// the cluster-scoped storage and node inventories. Callers leave it nil when
// those inventories are unavailable to the reader.
type PDPlacementEvidence struct {
	// HyperdiskOnlyNodes are the nodes whose machine series can't attach any
	// Persistent Disk type. Empty means the check doesn't apply.
	HyperdiskOnlyNodes []*corev1.Node
	// ClaimDiskTypes maps "namespace/claim" to the Compute Engine disk type of
	// the claim's bound volume. Claims on other storage are absent.
	ClaimDiskTypes map[string]string
	// ClaimVolumes maps "namespace/claim" to the claim's bound volume, whose
	// node affinity (a zonal disk's zone) also limits where the pod can run.
	ClaimVolumes map[string]*corev1.PersistentVolume
}

type pdPlacementSubject struct {
	kind, group, namespace, name string
	claims                       map[string]string // claim → disk type
	reachable                    map[string]string // Hyperdisk-only node → machine type
	placed                       []string          // "pod on node (machine type)"
}

// checkPDVolumeHyperdiskPlacement flags workloads that mount a Persistent
// Disk volume while their scheduling constraints still admit a node that can
// attach only Hyperdisk. A pod scheduled there never starts: the attach is
// rejected and retried forever. Already-placed pods are the live outage and
// raise the finding to danger.
func checkPDVolumeHyperdiskPlacement(tr *evalTracker, input *CheckInput) []Finding {
	ev := input.PDPlacement
	if ev == nil || len(ev.HyperdiskOnlyNodes) == 0 || input.Pods == nil {
		return nil
	}
	nodes := make(map[string]*corev1.Node, len(ev.HyperdiskOnlyNodes))
	for _, n := range ev.HyperdiskOnlyNodes {
		nodes[n.Name] = n
	}
	replicaSets := make(map[string]*appsv1.ReplicaSet, len(input.ReplicaSets))
	for _, rs := range input.ReplicaSets {
		replicaSets[rs.Namespace+"/"+rs.Name] = rs
	}
	templates := pdPlacementTemplates(input)

	subjects := map[string]*pdPlacementSubject{}
	var order []string
	for _, pod := range input.Pods {
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		claims := map[string]string{}
		var volumes []*corev1.PersistentVolume
		for _, vol := range pod.Spec.Volumes {
			if vol.PersistentVolumeClaim == nil {
				continue
			}
			key := pod.Namespace + "/" + vol.PersistentVolumeClaim.ClaimName
			if pv := ev.ClaimVolumes[key]; pv != nil {
				volumes = append(volumes, pv)
			}
			if diskType := ev.ClaimDiskTypes[key]; gkedisk.IsPersistentDisk(diskType) {
				claims[vol.PersistentVolumeClaim.ClaimName] = diskType
			}
		}
		if len(claims) == 0 {
			continue
		}
		kind, group, name := pdPlacementOwner(pod, replicaSets)
		key := ResourceKey(group, kind, pod.Namespace, name)
		s := subjects[key]
		if s == nil {
			s = &pdPlacementSubject{kind: kind, group: group, namespace: pod.Namespace, name: name, claims: map[string]string{}, reachable: map[string]string{}}
			subjects[key] = s
			order = append(order, key)
		}
		for claim, diskType := range claims {
			s.claims[claim] = diskType
		}
		if node := nodes[pod.Spec.NodeName]; node != nil {
			s.placed = append(s.placed, fmt.Sprintf("%s on %s (%s)", pod.Name, node.Name, node.Labels[gkedisk.InstanceTypeLabel]))
		}
		// Where a replacement pod can land is governed by the owner's current
		// template, not by the running pod, which may predate a template change.
		scheduled := pod
		if spec := templates[key]; spec != nil {
			scheduled = &corev1.Pod{Spec: *spec}
		}
		affinity := nodeaffinity.GetRequiredNodeAffinity(scheduled)
		for _, node := range ev.HyperdiskOnlyNodes {
			if _, seen := s.reachable[node.Name]; seen || !podAdmitsNode(scheduled, affinity, volumes, node) {
				continue
			}
			s.reachable[node.Name] = node.Labels[gkedisk.InstanceTypeLabel]
		}
	}

	var findings []Finding
	for _, key := range order {
		s := subjects[key]
		tr.record(checkPDVolumeHyperdiskOnlyNode, s.namespace)
		if len(s.placed) == 0 && len(s.reachable) == 0 {
			continue
		}
		severity := SeverityWarning
		if len(s.placed) > 0 {
			severity = SeverityDanger
		}
		findings = append(findings, Finding{
			Kind:      s.kind,
			Group:     s.group,
			Namespace: s.namespace,
			Name:      s.name,
			CheckID:   checkPDVolumeHyperdiskOnlyNode,
			Category:  CategoryReliability,
			Severity:  severity,
			Message:   pdPlacementMessage(s),
		})
	}
	return findings
}

// podAdmitsNode reports whether the node-level filters let pod onto node: the
// pod's nodeSelector and required node affinity match, every bound volume's
// node affinity and topology labels match, and it tolerates the node's
// NoSchedule and NoExecute taints. Inter-pod affinity and topology spread are
// not evaluated: they depend on where other pods happen to run. A selector
// that fails to parse admits nothing, so a malformed spec never produces a
// finding.
func podAdmitsNode(pod *corev1.Pod, affinity nodeaffinity.RequiredNodeAffinity, volumes []*corev1.PersistentVolume, node *corev1.Node) bool {
	if ok, err := affinity.Match(node); err != nil || !ok {
		return false
	}
	for _, pv := range volumes {
		if volumehelpers.CheckNodeAffinity(pv, node.Labels) != nil || !volumeTopologyLabelsAdmit(pv, node) {
			return false
		}
	}
	_, untolerated := corev1helpers.FindMatchingUntoleratedTaint(klog.Background(), node.Spec.Taints, pod.Spec.Tolerations, func(t *corev1.Taint) bool {
		return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
	}, false)
	return !untolerated
}

// volumeTopologyLabels are the zone and region labels older in-tree volumes
// carry instead of spec.nodeAffinity; the scheduler's VolumeZone filter
// requires a node to match each one the volume sets.
var volumeTopologyLabels = []string{
	corev1.LabelTopologyZone, corev1.LabelTopologyRegion,
	corev1.LabelFailureDomainBetaZone, corev1.LabelFailureDomainBetaRegion,
}

func volumeTopologyLabelsAdmit(pv *corev1.PersistentVolume, node *corev1.Node) bool {
	for _, key := range volumeTopologyLabels {
		want, ok := pv.Labels[key]
		if !ok {
			continue
		}
		actual, present := node.Labels[key]
		if !present {
			switch key {
			case corev1.LabelFailureDomainBetaZone:
				actual = node.Labels[corev1.LabelTopologyZone]
			case corev1.LabelFailureDomainBetaRegion:
				actual = node.Labels[corev1.LabelTopologyRegion]
			}
		}
		// Multi-zone volumes list their zones joined by "__".
		if !slices.Contains(strings.Split(want, "__"), actual) {
			return false
		}
	}
	return true
}

// pdPlacementTemplates indexes the pod templates of the workloads a finding
// can be attributed to, by the same key the subjects use.
func pdPlacementTemplates(input *CheckInput) map[string]*corev1.PodSpec {
	out := map[string]*corev1.PodSpec{}
	for _, d := range input.Deployments {
		out[ResourceKey("apps", "Deployment", d.Namespace, d.Name)] = &d.Spec.Template.Spec
	}
	for _, sts := range input.StatefulSets {
		out[ResourceKey("apps", "StatefulSet", sts.Namespace, sts.Name)] = &sts.Spec.Template.Spec
	}
	for _, ds := range input.DaemonSets {
		out[ResourceKey("apps", "DaemonSet", ds.Namespace, ds.Name)] = &ds.Spec.Template.Spec
	}
	return out
}

// pdPlacementOwner attributes a pod to the workload that recreates it, so a
// StatefulSet's replicas yield one finding. Unresolvable owners fall back to
// the pod itself.
func pdPlacementOwner(pod *corev1.Pod, replicaSets map[string]*appsv1.ReplicaSet) (kind, group, name string) {
	ref := metav1.GetControllerOf(pod)
	if ref == nil || schema.FromAPIVersionAndKind(ref.APIVersion, ref.Kind).Group != "apps" {
		return "Pod", "", pod.Name
	}
	switch ref.Kind {
	case "StatefulSet", "DaemonSet":
		return ref.Kind, "apps", ref.Name
	case "ReplicaSet":
		rs := replicaSets[pod.Namespace+"/"+ref.Name]
		if rs == nil || rs.UID != ref.UID {
			return "ReplicaSet", "apps", ref.Name
		}
		if parent := metav1.GetControllerOf(rs); parent != nil && parent.Kind == "Deployment" &&
			schema.FromAPIVersionAndKind(parent.APIVersion, parent.Kind).Group == "apps" {
			return "Deployment", "apps", parent.Name
		}
		return "ReplicaSet", "apps", ref.Name
	}
	return "Pod", "", pod.Name
}

func pdPlacementMessage(s *pdPlacementSubject) string {
	claimNames := make([]string, 0, len(s.claims))
	diskTypes := map[string]bool{}
	for claim, diskType := range s.claims {
		claimNames = append(claimNames, claim)
		diskTypes[diskType] = true
	}
	sort.Strings(claimNames)
	types := make([]string, 0, len(diskTypes))
	for t := range diskTypes {
		types = append(types, t)
	}
	sort.Strings(types)
	volumes := fmt.Sprintf("%s volume", strings.Join(types, "/"))
	if len(claimNames) > 1 {
		volumes += "s"
	}
	volumes += " (" + joinCapped(claimNames, 3) + ")"

	if len(s.placed) > 0 {
		sort.Strings(s.placed)
		return fmt.Sprintf("Mounts %s, but already runs on a node whose machine series attaches only Hyperdisk: %s. The volume can't attach there, so the pod can't start.",
			volumes, joinCapped(s.placed, 3))
	}
	reachable := make([]string, 0, len(s.reachable))
	for node, machineType := range s.reachable {
		reachable = append(reachable, fmt.Sprintf("%s (%s)", node, machineType))
	}
	sort.Strings(reachable)
	return fmt.Sprintf("Mounts %s, and its node selector, node affinity and tolerations don't keep it off Hyperdisk-only nodes: %s. A pod rescheduled there can't attach the volume and won't start.",
		volumes, joinCapped(reachable, 3))
}

func joinCapped(items []string, limit int) string {
	if len(items) <= limit {
		return strings.Join(items, ", ")
	}
	return strings.Join(slices.Clone(items[:limit]), ", ") + fmt.Sprintf(", +%d more", len(items)-limit)
}
