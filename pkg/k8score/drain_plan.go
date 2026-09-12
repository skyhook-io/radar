package k8score

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes"
)

// DrainOutcome is what a drain is expected to do with one pod.
type DrainOutcome string

const (
	DrainOutcomeEvict DrainOutcome = "evict"
	// DrainOutcomeSkip: the pod would be left alone (DaemonSet, mirror, unmanaged, emptyDir, terminal).
	DrainOutcomeSkip DrainOutcome = "skip"
	// DrainOutcomeMayBlock: the pod would be evicted, but its PodDisruptionBudget would make the
	// Eviction API refuse it right now (no disruptions allowed, status not yet reconciled, or more
	// than one budget covering the pod). Evidence, not a verdict: only the Eviction API and live
	// state decide.
	DrainOutcomeMayBlock DrainOutcome = "may-block"
)

// PodDrainDecision is the per-pod result of ClassifyPodForDrain.
type PodDrainDecision struct {
	Namespace string       `json:"namespace"`
	Name      string       `json:"name"`
	Outcome   DrainOutcome `json:"outcome"`
	Reason    string       `json:"reason"`
	EmptyDir  bool         `json:"emptyDir"`      // the pod uses emptyDir volumes; evicting it discards that data
	PDB       string       `json:"pdb,omitempty"` // namespace/name of the PDB behind a may-block outcome
	// PDBChecked is true when PodDisruptionBudgets were consulted for this decision. It is false
	// for decisions that never reach the budget check (skips, Pending or terminating pods), during
	// drain execution (the Eviction API decides), and when the budgets of the pod's namespace could
	// not be listed (see DrainPlan.PDBError).
	PDBChecked bool `json:"pdbChecked"`
}

// ClassifyPodForDrain decides, without touching the cluster, what DrainNode would do with pod
// under opts. The rules mirror kubectl drain's filters: terminal and mirror pods are skipped,
// DaemonSet pods are never evicted (kubectl refuses the drain without --ignore-daemonsets;
// DrainNode always ignores them), pods without a controller owner need Force, pods using
// emptyDir need DeleteEmptyDirData. Budget evaluation follows the Eviction API: pods that are
// Pending or already terminating bypass PodDisruptionBudgets entirely; otherwise see
// pdbBlocking for the cases reported as may-block.
func ClassifyPodForDrain(pod corev1.Pod, opts DrainOptions, pdbs []policyv1.PodDisruptionBudget) PodDrainDecision {
	d := PodDrainDecision{Namespace: pod.Namespace, Name: pod.Name, EmptyDir: hasLocalStorage(pod)}
	skip := func(reason string) PodDrainDecision {
		d.Outcome, d.Reason = DrainOutcomeSkip, reason
		return d
	}

	if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
		return skip(fmt.Sprintf("pod is %s; nothing to evict", string(pod.Status.Phase)))
	}
	if _, isMirror := pod.Annotations[corev1.MirrorPodAnnotationKey]; isMirror {
		return skip("static (mirror) pod managed by the kubelet; cannot be evicted")
	}
	controller := metav1.GetControllerOf(&pod)
	if controller != nil && controller.Kind == "DaemonSet" {
		if opts.IgnoreDaemonSets {
			return skip("managed by a DaemonSet; the DaemonSet controller would recreate it on this node")
		}
		return skip("managed by a DaemonSet; DaemonSet pods are never evicted by a drain (kubectl refuses without --ignore-daemonsets)")
	}
	if controller == nil && !opts.Force {
		return skip("not managed by a controller; would be lost, enable force to evict anyway")
	}
	if !opts.DeleteEmptyDirData && d.EmptyDir {
		return skip("uses emptyDir volumes; their data is lost on eviction, enable deleteEmptyDirData to evict")
	}

	if bypassesPDB(pod) {
		d.Outcome = DrainOutcomeEvict
		if pod.DeletionTimestamp != nil {
			d.Reason = "already terminating; the eviction is admitted regardless of PodDisruptionBudgets"
		} else {
			d.Reason = "still pending; the eviction is admitted regardless of PodDisruptionBudgets"
		}
		return d
	}

	d.PDBChecked = pdbs != nil
	if name, reason, blocked := pdbBlocking(pod, pdbs); blocked {
		d.Outcome = DrainOutcomeMayBlock
		d.PDB = name
		d.Reason = reason
		return d
	}

	d.Outcome = DrainOutcomeEvict
	switch {
	case controller == nil:
		d.Reason = "not managed by a controller; evicted because force is set"
	default:
		d.Reason = fmt.Sprintf("managed by %s %s; the controller reschedules it", controller.Kind, controller.Name)
	}
	return d
}

// bypassesPDB reports whether the Eviction API ignores PodDisruptionBudgets for the pod
// (Succeeded, Failed or Pending phase, or a deletion already in progress), as in
// canIgnorePDB of the API server's eviction handler.
func bypassesPDB(pod corev1.Pod) bool {
	return pod.DeletionTimestamp != nil ||
		pod.Status.Phase == corev1.PodSucceeded ||
		pod.Status.Phase == corev1.PodFailed ||
		pod.Status.Phase == corev1.PodPending
}

// isPodReady mirrors k8s.io/kubernetes/pkg/api/v1/pod.IsPodReady: the Ready condition is True.
func isPodReady(pod corev1.Pod) bool {
	for _, c := range pod.Status.Conditions {
		if c.Type == corev1.PodReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// matchingPDBs returns the PDBs in the pod's namespace whose selector selects the pod.
// A nil selector matches nothing, an empty selector matches everything (policy/v1 semantics).
func matchingPDBs(pod corev1.Pod, pdbs []policyv1.PodDisruptionBudget) []*policyv1.PodDisruptionBudget {
	podLabels := labels.Set(pod.Labels)
	var out []*policyv1.PodDisruptionBudget
	for i := range pdbs {
		p := &pdbs[i]
		if p.Namespace != pod.Namespace || p.Spec.Selector == nil {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil || !selector.Matches(podLabels) {
			continue
		}
		out = append(out, p)
	}
	return out
}

// pdbBlocking reproduces the decisions of the API server's eviction handler on the given
// snapshot and returns the budget name and an operator-facing reason when the eviction would
// be refused right now:
//   - more than one budget selects the pod: the eviction subresource refuses such pods;
//   - the pod is not Ready: allowed under unhealthyPodEvictionPolicy AlwaysAllow, or under the
//     default IfHealthyBudget when currentHealthy >= desiredHealthy > 0; otherwise the budget applies;
//   - the budget's status is not reconciled yet (observedGeneration < generation): refused (429);
//   - disruptionsAllowed is 0: refused (429).
func pdbBlocking(pod corev1.Pod, pdbs []policyv1.PodDisruptionBudget) (name, reason string, blocked bool) {
	matched := matchingPDBs(pod, pdbs)
	if len(matched) == 0 {
		return "", "", false
	}
	if len(matched) > 1 {
		names := make([]string, 0, len(matched))
		for _, p := range matched {
			names = append(names, p.Namespace+"/"+p.Name)
		}
		return names[0], fmt.Sprintf("covered by %d PodDisruptionBudgets (%s); the Eviction API refuses pods covered by more than one budget", len(matched), strings.Join(names, ", ")), true
	}
	p := matched[0]
	name = p.Namespace + "/" + p.Name

	if !isPodReady(pod) {
		if p.Spec.UnhealthyPodEvictionPolicy != nil && *p.Spec.UnhealthyPodEvictionPolicy == policyv1.AlwaysAllow {
			return "", "", false
		}
		if p.Status.CurrentHealthy >= p.Status.DesiredHealthy && p.Status.DesiredHealthy > 0 {
			return "", "", false
		}
	}
	if p.Status.ObservedGeneration < p.Generation {
		return name, fmt.Sprintf("PodDisruptionBudget %s has not been reconciled yet (observedGeneration %d < generation %d); evictions are refused until its status is up to date", name, p.Status.ObservedGeneration, p.Generation), true
	}
	if p.Status.DisruptionsAllowed <= 0 {
		return name, fmt.Sprintf("PodDisruptionBudget %s currently allows no disruptions; the eviction may be refused until the budget recovers", name), true
	}
	return "", "", false
}

// DrainPlanOptions echoes the options a plan was evaluated with, so the caller never has to
// guess which defaults applied.
type DrainPlanOptions struct {
	IgnoreDaemonSets   bool   `json:"ignoreDaemonSets"`
	DeleteEmptyDirData bool   `json:"deleteEmptyDirData"`
	Force              bool   `json:"force"`
	GracePeriodSeconds *int64 `json:"gracePeriodSeconds,omitempty"`
}

// DrainPlanSummary counts pods per outcome.
type DrainPlanSummary struct {
	Evict    int `json:"evict"`
	Skip     int `json:"skip"`
	MayBlock int `json:"mayBlock"`
}

// DrainPlan is a read-only estimate of what draining a node would do. It is computed from a
// snapshot: pods, budgets and permissions can change before the drain runs, which re-lists and
// re-evaluates live state.
type DrainPlan struct {
	Node          string             `json:"node"`
	GeneratedAt   time.Time          `json:"generatedAt"`
	Estimate      bool               `json:"estimate"` // always true; execution re-evaluates
	Options       DrainPlanOptions   `json:"options"`
	Summary       DrainPlanSummary   `json:"summary"`
	Pods          []PodDrainDecision `json:"pods"`
	PDBsEvaluated bool               `json:"pdbsEvaluated"`      // false when at least one namespace\'s PDBs could not be listed (per pod: pdbChecked)
	PDBError      string             `json:"pdbError,omitempty"` // why, when PDBsEvaluated is false
}

// PlanNodeDrain lists the pods on nodeName and classifies each one with ClassifyPodForDrain.
// It performs only reads with the given client, so it runs under the caller's RBAC identity and
// never cordons or evicts. PDBs are listed per namespace and only for namespaces holding pods
// whose decision depends on them; a namespace whose PDBs cannot be read is classified without
// PDB knowledge and reported as such instead of pretending no PDB exists.
func PlanNodeDrain(ctx context.Context, client kubernetes.Interface, nodeName string, opts DrainOptions) (*DrainPlan, error) {
	if _, err := client.CoreV1().Nodes().Get(ctx, nodeName, metav1.GetOptions{}); err != nil {
		return nil, err
	}
	podList, err := client.CoreV1().Pods("").List(ctx, metav1.ListOptions{
		FieldSelector: "spec.nodeName=" + nodeName,
	})
	if err != nil {
		return nil, fmt.Errorf("list pods on node: %w", err)
	}

	plan := &DrainPlan{
		Node:        nodeName,
		GeneratedAt: time.Now().UTC(),
		Estimate:    true,
		Options: DrainPlanOptions{
			IgnoreDaemonSets:   opts.IgnoreDaemonSets,
			DeleteEmptyDirData: opts.DeleteEmptyDirData,
			Force:              opts.Force,
			GracePeriodSeconds: opts.GracePeriodSeconds,
		},
		Pods:          make([]PodDrainDecision, 0, len(podList.Items)),
		PDBsEvaluated: true,
	}

	// First pass without budgets: skip decisions and PDB bypasses are final and never need a
	// PDB, so budgets are listed only for namespaces where a pod could reach the budget check.
	firstPass := make([]PodDrainDecision, len(podList.Items))
	needsPDB := map[string]bool{}
	for i, pod := range podList.Items {
		firstPass[i] = ClassifyPodForDrain(pod, opts, nil)
		if firstPass[i].Outcome != DrainOutcomeSkip && !bypassesPDB(pod) {
			needsPDB[pod.Namespace] = true
		}
	}

	// PDBs per namespace: nil marks "could not list", an empty slice marks "listed, none".
	pdbsByNamespace := map[string][]policyv1.PodDisruptionBudget{}
	for _, pod := range podList.Items {
		if _, seen := pdbsByNamespace[pod.Namespace]; seen || !needsPDB[pod.Namespace] {
			continue
		}
		list, err := client.PolicyV1().PodDisruptionBudgets(pod.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			pdbsByNamespace[pod.Namespace] = nil
			plan.PDBsEvaluated = false
			if plan.PDBError == "" {
				plan.PDBError = fmt.Sprintf("list PodDisruptionBudgets in %s: %v", pod.Namespace, err)
			}
			continue
		}
		pdbs := list.Items
		if pdbs == nil {
			pdbs = []policyv1.PodDisruptionBudget{}
		}
		pdbsByNamespace[pod.Namespace] = pdbs
	}

	for i, pod := range podList.Items {
		d := firstPass[i]
		if needsPDB[pod.Namespace] && pdbsByNamespace[pod.Namespace] != nil {
			d = ClassifyPodForDrain(pod, opts, pdbsByNamespace[pod.Namespace])
		}
		plan.Pods = append(plan.Pods, d)
		switch d.Outcome {
		case DrainOutcomeEvict:
			plan.Summary.Evict++
		case DrainOutcomeSkip:
			plan.Summary.Skip++
		case DrainOutcomeMayBlock:
			plan.Summary.MayBlock++
		}
	}
	sort.Slice(plan.Pods, func(i, j int) bool {
		if plan.Pods[i].Namespace != plan.Pods[j].Namespace {
			return plan.Pods[i].Namespace < plan.Pods[j].Namespace
		}
		return plan.Pods[i].Name < plan.Pods[j].Name
	})
	return plan, nil
}
