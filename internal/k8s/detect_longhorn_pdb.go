package k8s

import (
	"crypto/sha512"
	"encoding/hex"
	"maps"
	"strings"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

var longhornInstanceManagerGVR = schema.GroupVersionResource{Group: "longhorn.io", Version: "v1beta2", Resource: "instancemanagers"}

const longhornPDBMessage = "Longhorn is protecting this instance manager with a zero-disruption budget. Eviction is blocked now. If longhorn-manager is running on the node, it may remove the budget during drain when its drain policy and storage state permit. This does not establish that storage is healthy or that a drain can complete."
const longhornPDBAction = "If draining stalls, check that longhorn-manager is running on the node, then check Longhorn's node drain policy, instance managers and replica health. Let Longhorn manage this budget rather than relaxing it manually."

// longhornPDBContext is an optional, read-only snapshot. Its only purpose is
// provenance for an Issues explanation, never eviction permission or health.
type longhornPDBContext struct {
	managers map[string]*unstructured.Unstructured
	pods     map[string][]*corev1.Pod
	pdbs     map[string][]longhornPDBSelection
}

type longhornPDBSelection struct {
	pdb      *policyv1.PodDisruptionBudget
	selector labels.Selector
}

func longhornPDBSelectors(pdbs []*policyv1.PodDisruptionBudget) map[string][]longhornPDBSelection {
	byNamespace := map[string][]longhornPDBSelection{}
	for _, pdb := range pdbs {
		if pdb.DeletionTimestamp != nil || pdb.Spec.Selector == nil {
			continue
		}
		// An invalid selector remains an uncertain overlap, as in the uncached
		// check. Compile each budget once rather than once per instance manager.
		selector, _ := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
		byNamespace[pdb.Namespace] = append(byNamespace[pdb.Namespace], longhornPDBSelection{pdb: pdb, selector: selector})
	}
	return byNamespace
}

func collectLonghornPDBContext(cache *ResourceCache, pdbs []*policyv1.PodDisruptionBudget) longhornPDBContext {
	ctx := longhornPDBContext{managers: map[string]*unstructured.Unstructured{}, pods: map[string][]*corev1.Pod{}}
	dynamic := GetDynamicResourceCache()
	if dynamic == nil || cache == nil || cache.Pods() == nil || !cache.IsKindReady("pods") || !cache.IsKindReady("poddisruptionbudgets") {
		return ctx
	}
	seen := map[string]bool{}
	for _, pdb := range pdbs {
		if !longhornPDBShape(pdb) {
			continue
		}
		ns := pdb.Namespace
		if seen[ns] {
			continue
		}
		seen[ns] = true
		// WarmupCommonCRDs owns starting this watch. Detection never starts a
		// watch, probes permissions, or makes synchronous API calls per PDB.
		observation := dynamic.NamespaceObservation(longhornInstanceManagerGVR, ns)
		if observation.State != k8score.DynamicObservationSynced || observation.Truncated {
			continue
		}
		ims, truncated, err := dynamic.ListWatchedNamespaceReadOnly(longhornInstanceManagerGVR, ns, 10000)
		if err != nil || truncated {
			continue
		}
		if len(ims) == 0 {
			continue
		}
		pods, err := cache.Pods().Pods(ns).List(labels.Everything())
		if err != nil {
			continue
		}
		ctx.pods[ns] = pods
		for _, im := range ims {
			ctx.managers[im.GetNamespace()+"/"+im.GetName()] = im
		}
	}
	if len(ctx.managers) > 0 {
		ctx.pdbs = longhornPDBSelectors(pdbs)
	}
	return ctx
}

func longhornPDBShape(pdb *policyv1.PodDisruptionBudget) bool {
	return pdb != nil && pdb.DeletionTimestamp == nil && len(pdb.OwnerReferences) == 0 &&
		pdb.Spec.MinAvailable != nil && pdb.Spec.MinAvailable.Type == intstr.Int && pdb.Spec.MinAvailable.IntVal == 1 &&
		pdb.Spec.MaxUnavailable == nil && pdb.Spec.UnhealthyPodEvictionPolicy == nil &&
		pdb.Spec.Selector != nil && len(pdb.Spec.Selector.MatchExpressions) == 0 &&
		pdb.Spec.Selector.MatchLabels["longhorn.io/managed-by"] == "longhorn-manager" &&
		pdb.Spec.Selector.MatchLabels["longhorn.io/component"] == "instance-manager"
}

// Source contract: longhorn-manager v1.10.2, v1.11.3, v1.12.2 and v1.13.0,
// generateInstanceManagerPDBManifest and types.GetInstanceManagerLabels.
// All three process-map schemas use this identity shape. Process maps are
// deliberately not interpreted: engines/frontends/replicas are Longhorn's
// deletion gates, not evidence Radar may grant an eviction.
func isLonghornManagedPDB(pdb *policyv1.PodDisruptionBudget, ctx longhornPDBContext) bool {
	if !longhornPDBShape(pdb) || !pdbStructurallyBlocksEvictions(pdb) ||
		pdb.Status.ExpectedPods != 1 || pdb.Status.CurrentHealthy != 1 || pdb.Status.DesiredHealthy != 1 {
		return false
	}
	im := ctx.managers[pdb.Namespace+"/"+pdb.Name]
	if im == nil || im.GetAPIVersion() != "longhorn.io/v1beta2" || im.GetKind() != "InstanceManager" ||
		im.GetNamespace() != pdb.Namespace || im.GetName() != pdb.Name || im.GetUID() == "" || im.GetDeletionTimestamp() != nil {
		return false
	}
	node, _, nodeErr := unstructured.NestedString(im.Object, "spec", "nodeID")
	image, _, imageErr := unstructured.NestedString(im.Object, "spec", "image")
	imType, _, typeErr := unstructured.NestedString(im.Object, "spec", "type")
	engine, _, engineErr := unstructured.NestedString(im.Object, "spec", "dataEngine")
	state, _, stateErr := unstructured.NestedString(im.Object, "status", "currentState")
	if nodeErr != nil || imageErr != nil || typeErr != nil || engineErr != nil || stateErr != nil ||
		node == "" || state != "running" || (imType != "aio" && imType != "engine" && imType != "replica") ||
		(engine != "" && engine != "all" && engine != "v1" && engine != "v2") {
		return false
	}
	if !maps.Equal(pdb.Spec.Selector.MatchLabels, longhornInstanceManagerLabels(node, image, imType, engine)) {
		return false
	}
	selector, err := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
	if err != nil {
		return false
	}
	var selected *corev1.Pod
	for _, pod := range ctx.pods[pdb.Namespace] {
		if pod.Namespace == pdb.Namespace && selector.Matches(labels.Set(pod.Labels)) {
			if selected != nil {
				return false
			}
			selected = pod
		}
	}
	if selected == nil || selected.Name != im.GetName() || selected.DeletionTimestamp != nil ||
		selected.Spec.NodeName != node || selected.Status.Phase != corev1.PodRunning {
		return false
	}
	owner := metav1.GetControllerOf(selected)
	if owner == nil || len(selected.OwnerReferences) != 1 || owner.APIVersion != im.GetAPIVersion() ||
		owner.Kind != im.GetKind() || owner.Name != im.GetName() || owner.UID != im.GetUID() {
		return false
	}
	ready := false
	for _, condition := range selected.Status.Conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			ready = true
		}
	}
	if !ready {
		return false
	}
	for _, other := range ctx.pdbs[pdb.Namespace] {
		if other.pdb == pdb {
			continue
		}
		if other.selector == nil || other.selector.Matches(labels.Set(selected.Labels)) {
			return false
		}
	}
	return true
}

func longhornInstanceManagerLabels(node, image, imType, engine string) map[string]string {
	result := map[string]string{
		"longhorn.io/managed-by":            "longhorn-manager",
		"longhorn.io/component":             "instance-manager",
		"longhorn.io/instance-manager-type": imType,
	}
	if node != "" {
		result["longhorn.io/node"] = node
	}
	if image != "" {
		canonical := strings.NewReplacer(":", "-", "/", "-").Replace(image)
		checksum := sha512.Sum512([]byte(strings.TrimSpace(canonical)))
		result["longhorn.io/instance-manager-image"] = "imi-" + hex.EncodeToString(checksum[:])[:8]
	}
	if engine != "" && engine != "all" {
		result["longhorn.io/data-engine"] = engine
	}
	return result
}

func explainLonghornPDB(detection *Detection) {
	detection.RequiredReads = []issuesapi.EvidenceRead{
		{Group: "longhorn.io", Resource: "instancemanagers", Namespace: detection.Namespace, Verb: "list"},
		{Resource: "pods", Namespace: detection.Namespace, Verb: "list"},
		{Group: "policy", Resource: "poddisruptionbudgets", Namespace: detection.Namespace, Verb: "list"},
	}
	detection.Degraded = &issuesapi.DegradedFinding{Message: detection.Message, Action: detection.Action, KeepSubjectOnset: true}
	detection.Message = longhornPDBMessage
	detection.Action = longhornPDBAction
}
