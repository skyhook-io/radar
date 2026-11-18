package k8s

import (
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	policyv1 "k8s.io/api/policy/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/intstr"

	"github.com/skyhook-io/radar/pkg/cnpg"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type cnpgPDBVerdict string

const (
	// cnpgPDBNone: not a verified CloudNativePG primary budget, or unknown.
	cnpgPDBNone cnpgPDBVerdict = "none"
	// cnpgPDBExpected: the operator's primary budget on a healthy HA Cluster
	// in its normal idle layout.
	cnpgPDBExpected       cnpgPDBVerdict = "expected"
	cnpgPDBSingleInstance cnpgPDBVerdict = "singleInstance"
	// cnpgPDBBlocking: the operator's primary budget on an HA Cluster that is
	// not in that layout; the reason names the first failed condition.
	cnpgPDBBlocking cnpgPDBVerdict = "blocking"
)

// cnpgDrainTaints are CloudNativePG's default drain taints (operator
// configuration can change them); a primary on such a node is being moved.
var cnpgDrainTaints = []string{
	"node.kubernetes.io/unschedulable",
	"ToBeDeletedByClusterAutoscaler",
	"karpenter.sh/disrupted",
	"karpenter.sh/disruption",
}

const cnpgHealthyPhase = "Cluster in healthy state"

var cnpgClusterGVR = schema.GroupVersionResource{Group: cnpg.Group, Version: "v1", Resource: "clusters"}

const cnpgSingleInstancePDBMessage = "CloudNativePG keeps this single-instance Cluster's primary budget at 0 disruptions, so draining its node waits: there is no standby to switch over to."
const cnpgSingleInstancePDBAction = "Plan downtime for node maintenance: set spec.nodeMaintenanceWindow (inProgress: true, reusePVC: true) on the Cluster. CloudNativePG then removes this budget, and the database is unavailable until the node returns and the instance restarts on the same volume. Running more than one instance avoids this. Don't relax this budget by hand; the operator recreates it."

// classifyCNPGPrimaryPDB recognizes the budget CloudNativePG v1.30.1 creates
// for a Cluster's primary (pkg/specs/poddisruptionbudget.go) and whether the
// Cluster is in the idle HA layout in which the operator, not a drain, moves
// the primary (internal/controller/replicas.go setPrimaryOnSchedulableNode).
// pods are the namespace's pods the budget could select; node returns nil
// for a node Radar can't see. It never claims replication health.
func classifyCNPGPrimaryPDB(pdb *policyv1.PodDisruptionBudget, cluster *unstructured.Unstructured, pods []*corev1.Pod, pdbs []*policyv1.PodDisruptionBudget, node func(string) *corev1.Node) (cnpgPDBVerdict, string) {
	if !isCNPGPrimaryPDB(pdb, cluster) {
		return cnpgPDBNone, ""
	}
	instances, _, _ := unstructured.NestedInt64(cluster.Object, "spec", "instances")
	switch {
	case instances == 1:
		return cnpgPDBSingleInstance, ""
	case instances < 2:
		return cnpgPDBNone, ""
	}
	if reason := cnpgPrimaryHAGap(pdb, cluster, instances, pods, pdbs, node); reason != "" {
		return cnpgPDBBlocking, reason
	}
	return cnpgPDBExpected, ""
}

// ClassifyCNPGPrimaryPDB is classifyCNPGPrimaryPDB for the CNPG HA details,
// which read the inputs as the caller. "none" comes back as "".
func ClassifyCNPGPrimaryPDB(pdb *policyv1.PodDisruptionBudget, cluster *unstructured.Unstructured, pods []*corev1.Pod, pdbs []*policyv1.PodDisruptionBudget, node func(string) *corev1.Node) (string, string) {
	verdict, reason := classifyCNPGPrimaryPDB(pdb, cluster, pods, pdbs, node)
	if verdict == cnpgPDBNone {
		return "", ""
	}
	return string(verdict), reason
}

func isCNPGPrimaryPDB(pdb *policyv1.PodDisruptionBudget, cluster *unstructured.Unstructured) bool {
	if pdb == nil || cluster == nil || cluster.GetDeletionTimestamp() != nil || cluster.GetUID() == "" ||
		pdb.Namespace != cluster.GetNamespace() || pdb.Name != cluster.GetName()+"-primary" ||
		len(pdb.OwnerReferences) != 1 || !cnpg.ControlledBy(pdb.OwnerReferences, cnpg.Group, "Cluster", cluster.GetName(), cluster.GetUID()) {
		return false
	}
	spec := pdb.Spec
	if spec.MinAvailable == nil || spec.MinAvailable.Type != intstr.Int || spec.MinAvailable.IntVal != 1 || spec.MaxUnavailable != nil ||
		spec.Selector == nil || len(spec.Selector.MatchExpressions) != 0 ||
		!maps.Equal(spec.Selector.MatchLabels, map[string]string{"cnpg.io/cluster": cluster.GetName(), "cnpg.io/instanceRole": "primary"}) {
		return false
	}
	return pdbStructurallyBlocksEvictions(pdb) && pdb.Status.ExpectedPods == 1 && pdb.Status.CurrentHealthy == 1 && pdb.Status.DesiredHealthy == 1
}

func cnpgPrimaryHAGap(pdb *policyv1.PodDisruptionBudget, cluster *unstructured.Unstructured, instances int64, pods []*corev1.Pod, pdbs []*policyv1.PodDisruptionBudget, node func(string) *corev1.Node) string {
	ready, _, _ := unstructured.NestedInt64(cluster.Object, "status", "readyInstances")
	phase, _, _ := unstructured.NestedString(cluster.Object, "status", "phase")
	current, _, _ := unstructured.NestedString(cluster.Object, "status", "currentPrimary")
	target, _, _ := unstructured.NestedString(cluster.Object, "status", "targetPrimary")
	maintenance, _, _ := unstructured.NestedBool(cluster.Object, "spec", "nodeMaintenanceWindow", "inProgress")
	switch {
	case ready != instances:
		return "not all instances are ready"
	case phase != cnpgHealthyPhase:
		return "the cluster is not in a healthy state"
	case current == "" || current != target:
		return "a switchover is pending"
	case maintenance:
		return "a node maintenance window is in progress"
	}

	ns, name, uid := cluster.GetNamespace(), cluster.GetName(), cluster.GetUID()
	selector, _ := metav1.LabelSelectorAsSelector(pdb.Spec.Selector)
	var primary *corev1.Pod
	var members []*corev1.Pod
	for _, p := range pods {
		if p.Namespace != ns {
			continue
		}
		if selector.Matches(labels.Set(p.Labels)) {
			if primary != nil {
				return "the budget selects more than one pod"
			}
			primary = p
		}
		if cnpg.IsInstancePod(p, ns, name, uid) {
			members = append(members, p)
		}
	}
	if primary == nil || primary.Name != current || !cnpg.IsInstancePod(primary, ns, name, uid) ||
		!isPodReadyForProblem(primary) || primary.DeletionTimestamp != nil || primary.Spec.NodeName == "" {
		return "the budget does not select exactly the ready primary"
	}
	// Two budgets on one pod make the Eviction API refuse it whatever the
	// operator does; an invalid selector is an uncertain overlap, so it counts.
	for _, other := range pdbs {
		if other.Name == pdb.Name || other.Namespace != ns || other.Spec.Selector == nil {
			continue
		}
		if s, err := metav1.LabelSelectorAsSelector(other.Spec.Selector); err != nil || s.Matches(labels.Set(primary.Labels)) {
			return "another budget also selects the primary"
		}
	}
	primaryNode := primary.Spec.NodeName
	if !cnpgNodeUsable(node(primaryNode)) {
		return "the primary's node is cordoned, draining or not ready"
	}
	standby := false
	for _, p := range members {
		if p.Name == primary.Name {
			continue
		}
		// The operator waits rather than switch over while any standby shares
		// the primary's node.
		if p.Spec.NodeName == primaryNode {
			return "a standby shares the primary's node"
		}
		if p.DeletionTimestamp == nil && isPodReadyForProblem(p) && p.Spec.NodeName != "" && cnpgNodeUsable(node(p.Spec.NodeName)) {
			standby = true
		}
	}
	if !standby {
		return "no ready standby is on a schedulable node; after a switchover off a cordoned node this lasts until that node is uncordoned"
	}
	return ""
}

func cnpgNodeUsable(n *corev1.Node) bool {
	if n == nil || n.DeletionTimestamp != nil || n.Spec.Unschedulable {
		return false
	}
	for _, t := range n.Spec.Taints {
		if slices.Contains(cnpgDrainTaints, t.Key) {
			return false
		}
	}
	for _, c := range n.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

// cnpgPDBContext is a read-only snapshot of what the classifier needs. A
// namespace whose Clusters aren't fully observed has no entry, so its budgets
// stay unrecognised.
type cnpgPDBContext struct {
	clusters map[string]*unstructured.Unstructured // namespace/name
	pods     map[string][]*corev1.Pod
	pdbs     map[string][]*policyv1.PodDisruptionBudget
	node     func(string) *corev1.Node
}

// collectCNPGPDBContext reads the budgets in namespace ("" for all) itself.
func collectCNPGPDBContext(cache *ResourceCache, namespace string) cnpgPDBContext {
	ctx := cnpgPDBContext{clusters: map[string]*unstructured.Unstructured{}, pods: map[string][]*corev1.Pod{}, pdbs: map[string][]*policyv1.PodDisruptionBudget{}, node: func(string) *corev1.Node { return nil }}
	dynamic := GetDynamicResourceCache()
	if dynamic == nil || cache == nil || cache.PodDisruptionBudgets() == nil {
		return ctx
	}
	var pdbs []*policyv1.PodDisruptionBudget
	if namespace != "" {
		pdbs, _ = cache.PodDisruptionBudgets().PodDisruptionBudgets(namespace).List(labels.Everything())
	} else {
		pdbs, _ = cache.PodDisruptionBudgets().List(labels.Everything())
	}
	for _, pdb := range pdbs {
		ctx.pdbs[pdb.Namespace] = append(ctx.pdbs[pdb.Namespace], pdb)
	}
	seen := map[string]bool{}
	for _, pdb := range pdbs {
		ns := pdb.Namespace
		if seen[ns] || !cnpgPrimaryPDBCandidate(pdb) {
			continue
		}
		seen[ns] = true
		// WarmupCommonCRDs owns the Clusters watch; detection never starts one.
		observation := dynamic.NamespaceObservation(cnpgClusterGVR, ns)
		if observation.State != k8score.DynamicObservationSynced || observation.Truncated {
			continue
		}
		clusters, truncated, err := dynamic.ListWatchedNamespaceReadOnly(cnpgClusterGVR, ns, 10000)
		if err != nil || truncated {
			continue
		}
		for _, c := range clusters {
			ctx.clusters[ns+"/"+c.GetName()] = c
		}
		if cache.Pods() != nil && cache.IsKindReady("pods") && cache.KindCoversNamespace("pods", ns) {
			if pods, err := cache.Pods().Pods(ns).List(labels.Everything()); err == nil {
				ctx.pods[ns] = pods
			}
		}
	}
	if len(ctx.clusters) > 0 && cache.Nodes() != nil && cache.IsKindReady("nodes") {
		nodes := cache.Nodes()
		ctx.node = func(name string) *corev1.Node {
			n, err := nodes.Get(name)
			if err != nil {
				return nil
			}
			return n
		}
	}
	return ctx
}

func cnpgPrimaryPDBCandidate(pdb *policyv1.PodDisruptionBudget) bool {
	for _, ref := range pdb.OwnerReferences {
		if ref.Kind == "Cluster" && pdb.Name == ref.Name+"-primary" {
			return true
		}
	}
	return false
}

func (c cnpgPDBContext) classify(pdb *policyv1.PodDisruptionBudget) cnpgPDBVerdict {
	if !cnpgPrimaryPDBCandidate(pdb) {
		return cnpgPDBNone
	}
	cluster := c.clusters[pdb.Namespace+"/"+strings.TrimSuffix(pdb.Name, "-primary")]
	verdict, _ := classifyCNPGPrimaryPDB(pdb, cluster, c.pods[pdb.Namespace], c.pdbs[pdb.Namespace], c.node)
	return verdict
}

// applyCNPGPrimaryPDBVerdict keeps the generic warning as the form for a
// caller who can't make the exact reads behind the verdict, so a restricted
// caller learns nothing it can't read. The onset is the budget's own.
func applyCNPGPrimaryPDBVerdict(d *Detection, verdict cnpgPDBVerdict) {
	clusters := issuesapi.EvidenceRead{Group: cnpg.Group, Resource: "clusters", Namespace: d.Namespace, Verb: "list", Exact: true}
	generic := &issuesapi.DegradedFinding{Message: d.Message, Cause: d.Cause, Action: d.Action, KeepSubjectOnset: true}
	switch verdict {
	case cnpgPDBExpected:
		d.ExpectedWhenVerified = true
		d.Degraded = generic
		// Nodes first: a restricted caller fails on one cluster-wide check.
		d.RequiredReads = []issuesapi.EvidenceRead{{Resource: "nodes", Verb: "list", Exact: true}, clusters,
			{Resource: "pods", Namespace: d.Namespace, Verb: "list", Exact: true}}
	case cnpgPDBSingleInstance:
		d.Degraded = generic
		d.RequiredReads = []issuesapi.EvidenceRead{clusters}
		d.Message, d.Action = cnpgSingleInstancePDBMessage, cnpgSingleInstancePDBAction
	}
}
