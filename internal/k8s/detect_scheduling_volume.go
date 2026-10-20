package k8s

import (
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/pkg/k8score"
)

// volumePlacement is the cache-backed context behind a volume-topology
// verdict ("didn't match PersistentVolume's node affinity", or a
// ReadWriteOncePod claim already in use): which claim pins the Pod, where its
// PersistentVolume can attach, and, when the evidence is in the cache, the
// previous ReplicaSet's Pod that still holds the claim during a RollingUpdate.
type volumePlacement struct {
	claim      string
	accessMode corev1.PersistentVolumeAccessMode
	pv         string
	terms      []NodeSelectorTermFacts // PV required nodeAffinity; nil when unknown

	// Set only for the rollout deadlock.
	deployment string
	holderPod  string
	holderRS   string
}

// volumePlacementFor returns nil unless the verdict carries a volume-topology
// reason and the Pod's bound claim can be read from the cache.
func volumePlacementFor(cache *ResourceCache, pod *corev1.Pod, nsPods []*corev1.Pod, ownerKind, ownerName string, reasons []SchedulingReason) *volumePlacement {
	if cache == nil || !hasVolumePlacementReason(reasons) {
		return nil
	}
	pvcl := cache.PersistentVolumeClaims()
	if pvcl == nil {
		return nil
	}
	var fallback *volumePlacement
	pinned := 0
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim == nil {
			continue
		}
		pvc, err := pvcl.PersistentVolumeClaims(pod.Namespace).Get(v.PersistentVolumeClaim.ClaimName)
		if err != nil || pvc == nil || pvc.Status.Phase != corev1.ClaimBound || pvc.Spec.VolumeName == "" {
			continue
		}
		vp := &volumePlacement{claim: pvc.Name, pv: pvc.Spec.VolumeName, terms: pvRequiredTerms(cache, pvc.Spec.VolumeName)}
		if pvcSingleNodeAccessOnly(pvc) {
			vp.accessMode = singleNodeAccessMode(pvc)
			if ownerKind == "Deployment" && holderCanBlock(reasons) && vp.findRolloutHolder(cache, pod, nsPods, ownerName, rwopInUse(reasons)) {
				return vp
			}
		}
		if len(vp.terms) > 0 {
			pinned++
			fallback = vp
		}
	}
	// With several pinned volumes the verdict doesn't say which one conflicts.
	if pinned > 1 {
		return nil
	}
	return fallback
}

func rwopInUse(reasons []SchedulingReason) bool {
	for _, r := range reasons {
		if strings.Contains(strings.ToLower(r.Raw), "readwriteoncepod") {
			return true
		}
	}
	return false
}

func hasVolumePlacementReason(reasons []SchedulingReason) bool {
	for _, r := range reasons {
		if r.Class == SchedVolumeNodeAffinity || strings.Contains(strings.ToLower(r.Raw), "readwriteoncepod") {
			return true
		}
	}
	return false
}

// holderCanBlock reports whether the verdict has a rejection that only another
// pod causes (anti-affinity, an in-use ReadWriteOncePod claim) and none from
// the pod's own node selection. A pod whose new template selects nodes outside
// the volume's zone is stuck by its own spec, and a resource shortage may not
// clear when the old pod goes, so neither is called a deadlock.
func holderCanBlock(reasons []SchedulingReason) bool {
	holderCaused := false
	for _, r := range reasons {
		switch {
		case r.Class == SchedNodeAffinitySelector:
			return false
		case r.Class == SchedPodAntiAffinity, strings.Contains(strings.ToLower(r.Raw), "readwriteoncepod"):
			holderCaused = true
		}
	}
	return holderCaused
}

// findRolloutHolder looks for a running Pod of the same Deployment, from an
// older ReplicaSet, that mounts the same single-node claim. The pending pod must
// belong to the Deployment's current ReplicaSet. Under
// RollingUpdate that old Pod stays until the new one is Ready, so the new Pod
// can only go where the volume attaches, and the old Pod is in the way.
func (vp *volumePlacement) findRolloutHolder(cache *ResourceCache, pod *corev1.Pod, nsPods []*corev1.Pod, deployment string, rwop bool) bool {
	ref := controllerOwnerRef(pod.OwnerReferences)
	if ref == nil || ref.Kind != "ReplicaSet" {
		return false
	}
	depLister := cache.Deployments()
	if depLister == nil {
		return false
	}
	dep, err := depLister.Deployments(pod.Namespace).Get(deployment)
	if err != nil || dep == nil || dep.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		return false
	}
	rsLister := cache.ReplicaSets()
	if rsLister == nil {
		return false
	}
	replicaSets, _ := rsLister.ReplicaSets(pod.Namespace).List(labels.Everything())
	current := k8score.CurrentDeploymentRevisionPodHashes(replicaSets)[dep.UID]
	if current == "" || pod.Labels[appsv1.DefaultDeploymentUniqueLabelKey] != current {
		return false
	}
	for _, other := range nsPods {
		if other.UID == pod.UID || other.Spec.NodeName == "" || other.DeletionTimestamp != nil ||
			other.Status.Phase == corev1.PodSucceeded || other.Status.Phase == corev1.PodFailed {
			continue
		}
		otherRef := controllerOwnerRef(other.OwnerReferences)
		if otherRef == nil || otherRef.Kind != "ReplicaSet" || otherRef.Name == ref.Name || !podUsesClaim(other, vp.claim) {
			continue
		}
		if _, kind, name := podOwnerKindName(cache, other); kind != "Deployment" || name != deployment {
			continue
		}
		// The anti-affinity rejection must be between these two pods; one
		// against an unrelated pod isn't cleared by removing the old one.
		if !rwop && !requiredAntiAffinityBetween(pod, other) && !requiredAntiAffinityBetween(other, pod) {
			continue
		}
		vp.deployment, vp.holderPod, vp.holderRS = deployment, other.Name, otherRef.Name
		return true
	}
	return false
}

// requiredAntiAffinityBetween reports whether a's required pod anti-affinity
// selects b (same namespace, or a namespace the term lists).
func requiredAntiAffinityBetween(a, b *corev1.Pod) bool {
	if a.Spec.Affinity == nil || a.Spec.Affinity.PodAntiAffinity == nil {
		return false
	}
	for _, term := range a.Spec.Affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution {
		if term.LabelSelector == nil {
			continue
		}
		if len(term.Namespaces) > 0 && !slices.Contains(term.Namespaces, b.Namespace) || len(term.Namespaces) == 0 && term.NamespaceSelector == nil && a.Namespace != b.Namespace {
			continue
		}
		sel, err := metav1.LabelSelectorAsSelector(term.LabelSelector)
		if err == nil && sel.Matches(labels.Set(b.Labels)) {
			return true
		}
	}
	return false
}

func podUsesClaim(pod *corev1.Pod, claim string) bool {
	for _, v := range pod.Spec.Volumes {
		if v.PersistentVolumeClaim != nil && v.PersistentVolumeClaim.ClaimName == claim {
			return true
		}
	}
	return false
}

func singleNodeAccessMode(pvc *corev1.PersistentVolumeClaim) corev1.PersistentVolumeAccessMode {
	modes := pvc.Status.AccessModes
	if len(modes) == 0 {
		modes = pvc.Spec.AccessModes
	}
	if slices.Contains(modes, corev1.ReadWriteOncePod) {
		return corev1.ReadWriteOncePod
	}
	return corev1.ReadWriteOnce
}

// pvRequiredTerms reads the PV's required nodeAffinity. A term with
// matchFields is left out of the summary: it can't be checked against labels.
func pvRequiredTerms(cache *ResourceCache, pvName string) []NodeSelectorTermFacts {
	pvl := cache.PersistentVolumes()
	if pvl == nil {
		return nil
	}
	pv, err := pvl.Get(pvName)
	if err != nil || pv == nil || pv.Spec.NodeAffinity == nil || pv.Spec.NodeAffinity.Required == nil {
		return nil
	}
	var terms []NodeSelectorTermFacts
	for _, term := range pv.Spec.NodeAffinity.Required.NodeSelectorTerms {
		if len(term.MatchFields) > 0 || len(term.MatchExpressions) == 0 {
			return nil
		}
		var t NodeSelectorTermFacts
		for _, e := range term.MatchExpressions {
			t.Expressions = append(t.Expressions, MatchExpr{Key: e.Key, Operator: string(e.Operator), Values: e.Values})
		}
		terms = append(terms, t)
	}
	return terms
}

// lead is the sentence placed before the scheduler summary. It avoids bare
// commas outside brackets so the UI's clause splitter keeps it in one piece.
func (vp *volumePlacement) lead() string {
	where := ""
	if len(vp.terms) > 0 {
		where = fmt.Sprintf("PersistentVolume %s only attaches on nodes with %s", vp.pv, formatNodeSelectorTerms(vp.terms))
	}
	if vp.holderPod == "" {
		return fmt.Sprintf("PVC %q: %s", vp.claim, where)
	}
	msg := fmt.Sprintf("Rollout deadlock: pod %s from the previous ReplicaSet %s still holds %s PVC %q", vp.holderPod, vp.holderRS, vp.accessMode, vp.claim)
	if where != "" {
		msg += "; its " + where
	}
	return msg
}

func (vp *volumePlacement) deadlockAction() string {
	return fmt.Sprintf("Deployment %s uses RollingUpdate, so the old pod keeps the volume until this pod is Ready, and this pod can't be placed where the volume attaches. "+
		"Set strategy: Recreate (the usual fix for a single-replica %s workload), or scale ReplicaSet %s to 0. "+
		"Deleting the old pod doesn't help: its ReplicaSet recreates it.", vp.deployment, vp.accessMode, vp.holderRS)
}

func formatNodeSelectorTerms(terms []NodeSelectorTermFacts) string {
	var out []string
	for _, term := range terms {
		var exprs []string
		for _, e := range term.Expressions {
			switch {
			case e.Operator == "In" && len(e.Values) == 1:
				exprs = append(exprs, e.Key+"="+e.Values[0])
			case e.Operator == "Exists" || e.Operator == "DoesNotExist":
				exprs = append(exprs, e.Key+" "+e.Operator)
			default:
				exprs = append(exprs, fmt.Sprintf("%s %s [%s]", e.Key, e.Operator, strings.Join(e.Values, ", ")))
			}
		}
		out = append(out, strings.Join(exprs, " and "))
	}
	return strings.Join(out, " or ")
}
