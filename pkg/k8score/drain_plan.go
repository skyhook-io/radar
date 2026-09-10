package k8score

import (
	"context"
	"fmt"
	"sort"
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
	// DrainOutcomeEvict: the pod would be evicted.
	DrainOutcomeEvict DrainOutcome = "evict"
	// DrainOutcomeSkip: the pod would be left alone (DaemonSet, mirror, unmanaged, emptyDir, terminal).
	DrainOutcomeSkip DrainOutcome = "skip"
	// DrainOutcomeMayBlock: the pod would be evicted, but a PodDisruptionBudget currently allows no
	// disruptions, so the eviction may be refused until the budget recovers. Evidence, not a verdict:
	// only the Eviction API and live state decide.
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
	// PDBChecked is true when the PodDisruptionBudgets of the pod's namespace were available to
	// this decision (a skip decided earlier in the filter chain never needs them). It is false
	// during drain execution (the Eviction API decides) and when they could not be listed for the
	// pod's namespace (see DrainPlan.PDBError).
	PDBChecked bool `json:"pdbChecked"`
}

// ClassifyPodForDrain decides, without touching the cluster, what DrainNode would do with pod
// under opts. The rules mirror kubectl drain's filters: terminal and mirror pods are skipped,
// DaemonSet pods are never evicted (kubectl refuses the drain without --ignore-daemonsets;
// DrainNode always ignores them), pods without a controller owner need Force, pods using
// emptyDir need DeleteEmptyDirData. A pod that would be evicted but is covered by a PDB with
// no disruptions allowed is reported as may-block. A pod that is already terminating is
// reported as evict without consulting PDBs: the Eviction API admits it regardless of budgets.
func ClassifyPodForDrain(pod corev1.Pod, opts DrainOptions, pdbs []policyv1.PodDisruptionBudget) PodDrainDecision {
	d := PodDrainDecision{Namespace: pod.Namespace, Name: pod.Name, PDBChecked: pdbs != nil, EmptyDir: hasLocalStorage(pod)}
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

	if pod.DeletionTimestamp != nil {
		d.Outcome = DrainOutcomeEvict
		d.Reason = "already terminating; the eviction is admitted regardless of PodDisruptionBudgets"
		return d
	}

	if name, blocked := pdbBlocking(pod, pdbs); blocked {
		d.Outcome = DrainOutcomeMayBlock
		d.PDB = name
		d.Reason = fmt.Sprintf("PodDisruptionBudget %s currently allows no disruptions; the eviction may be refused until the budget recovers", name)
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

// pdbBlocking returns the first PDB in the pod's namespace that selects the pod and has
// no disruptions allowed. A nil selector matches nothing, an empty selector matches everything,
// matching the policy/v1 semantics.
func pdbBlocking(pod corev1.Pod, pdbs []policyv1.PodDisruptionBudget) (string, bool) {
	podLabels := labels.Set(pod.Labels)
	for i := range pdbs {
		p := &pdbs[i]
		if p.Namespace != pod.Namespace || p.Spec.Selector == nil || p.Status.DisruptionsAllowed > 0 {
			continue
		}
		selector, err := metav1.LabelSelectorAsSelector(p.Spec.Selector)
		if err != nil || !selector.Matches(podLabels) {
			continue
		}
		return p.Namespace + "/" + p.Name, true
	}
	return "", false
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
// never cordons or evicts. PDBs are listed per namespace; a namespace whose PDBs cannot be read
// is classified without PDB knowledge and reported as such instead of pretending no PDB exists.
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

	// PDBs per namespace: nil marks "could not list", an empty slice marks "listed, none".
	pdbsByNamespace := map[string][]policyv1.PodDisruptionBudget{}
	for _, pod := range podList.Items {
		if _, seen := pdbsByNamespace[pod.Namespace]; seen {
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

	for _, pod := range podList.Items {
		d := ClassifyPodForDrain(pod, opts, pdbsByNamespace[pod.Namespace])
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
