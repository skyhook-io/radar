package k8s

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/labels"
)

// Scheduling failure decomposition.
//
// The kube-scheduler already did the root-cause analysis — it just hands
// it back as one opaque string in the FailedScheduling event and the
// Pod's PodScheduled=False condition message, e.g.:
//
//	0/5 nodes are available: 2 Insufficient cpu, 3 node(s) had untolerated
//	taint {dedicated: gpu}. preemption: 0/5 nodes are available: 5 No
//	preemption victims found for incoming pod.
//
// parseSchedulerMessage turns that into structured, per-predicate reasons
// so callers (the issues engine, MCP diagnose, the Pod UI banner) can show
// "why won't this schedule" without the operator re-reading scheduler prose.
// It is a pure function — the node-fit resolver (resolveUnsatisfiableNodeSelector)
// later joins NodeAffinitySelector reasons against the live node cache to name
// the specific offending label (e.g. "no node has kubernetes.io/arch=arm64").
// Taint key/value come straight from the scheduler message (parseTaintPayload),
// not from a cache join.

// SchedReasonClass is the predicate family a scheduling failure falls into.
type SchedReasonClass string

const (
	SchedInsufficientResource SchedReasonClass = "InsufficientResource"
	SchedUntoleratedTaint     SchedReasonClass = "UntoleratedTaint"
	SchedNodeAffinitySelector SchedReasonClass = "NodeAffinitySelector"
	SchedPodAffinity          SchedReasonClass = "PodAffinity"
	SchedPodAntiAffinity      SchedReasonClass = "PodAntiAffinity"
	SchedTopologySpread       SchedReasonClass = "TopologySpread"
	SchedVolumeNodeAffinity   SchedReasonClass = "VolumeNodeAffinity"
	SchedVolumeBinding        SchedReasonClass = "VolumeBinding" // unbound PVC / no available PVs to bind
	SchedVolumeCount          SchedReasonClass = "VolumeCount"
	SchedNoPorts              SchedReasonClass = "NoPorts"
	SchedNodeUnschedulable    SchedReasonClass = "NodeUnschedulable" // cordoned / not-ready / unschedulable taint
	SchedOther                SchedReasonClass = "Other"
)

// SchedulingReason is one decomposed clause of a scheduler verdict. The
// side fields are populated only for their owning Class (Resource for
// SchedInsufficientResource; TaintKey/TaintValue for SchedUntoleratedTaint);
// other classes leave them zero. classifyClause is the sole producer and
// always sets Class + Raw.
type SchedulingReason struct {
	Class SchedReasonClass
	// NodeCount is how many nodes this clause rejected. 0 when the clause
	// is whole-message (e.g. unbound PVC) or the count couldn't be parsed.
	NodeCount int
	// Resource is set for SchedInsufficientResource: "cpu", "memory",
	// "ephemeral-storage", "pods", "nvidia.com/gpu", …
	Resource string
	// TaintKey / TaintValue are set for SchedUntoleratedTaint. TaintValue
	// is empty for valueless taints (e.g. {node.kubernetes.io/unreachable}).
	TaintKey   string
	TaintValue string
	// Raw is the original clause text, preserved so callers can fall back
	// to it for classes we don't further structure.
	Raw string
}

var (
	// "0/5 nodes are available" / "1/12 nodes are available"
	reNodesAvailable = regexp.MustCompile(`(\d+)/(\d+)\s+nodes? are available`)
	// leading integer count on a clause: "2 Insufficient cpu", "3 node(s) had…"
	reLeadingCount = regexp.MustCompile(`^\s*(\d+)\s+`)
	// "Insufficient <resource>" — resource may contain '.'/'-'/'/'
	reInsufficient = regexp.MustCompile(`Insufficient\s+([A-Za-z0-9./_-]+)`)
	// taint payload: "{key: value}" or "{key}"
	reTaint = regexp.MustCompile(`\{([^}]*)\}`)
)

// parseSchedulerMessage decomposes a scheduler verdict (from a
// FailedScheduling event message or a PodScheduled=False condition message)
// into structured reasons. totalNodes is the node count the scheduler
// considered (the denominator of "0/N nodes are available"); 0 when the
// message carries no such prefix. An empty/unrecognized message yields nil
// reasons so callers can fall back to the raw text.
func parseSchedulerMessage(msg string) (totalNodes int, reasons []SchedulingReason) {
	msg = strings.TrimSpace(msg)
	if msg == "" {
		return 0, nil
	}

	// Drop the "preemption: …" tail — it restates the same node set from
	// the preemption scheduler's point of view and only adds noise.
	if before, _, ok := strings.Cut(msg, ". preemption:"); ok {
		msg = before
	} else if before, _, ok := strings.Cut(msg, " preemption:"); ok {
		msg = before
	}

	if m := reNodesAvailable.FindStringSubmatch(msg); m != nil {
		totalNodes, _ = strconv.Atoi(m[2])
	}

	// Everything after the first ":" is the comma-separated clause list.
	// Messages without a colon (e.g. "pod has unbound immediate
	// PersistentVolumeClaims") are treated as a single clause.
	clauseStr := msg
	if _, rest, ok := strings.Cut(msg, ":"); ok {
		clauseStr = rest
	}
	clauseStr = strings.TrimRight(strings.TrimSpace(clauseStr), ".")
	if clauseStr == "" {
		return totalNodes, nil
	}

	for clause := range strings.SplitSeq(clauseStr, ", ") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		if r, ok := classifyClause(clause); ok {
			reasons = append(reasons, r)
		}
	}
	return totalNodes, reasons
}

// classifyClause maps one scheduler clause to a structured reason. The
// substring checks are ordered so the more specific phrasings win (e.g.
// "anti-affinity" before "affinity", "node affinity/selector" before the
// bare "affinity" used by pod-affinity).
func classifyClause(clause string) (SchedulingReason, bool) {
	r := SchedulingReason{Raw: clause}
	if m := reLeadingCount.FindStringSubmatch(clause); m != nil {
		r.NodeCount, _ = strconv.Atoi(m[1])
	}

	lower := strings.ToLower(clause)

	switch {
	case strings.Contains(clause, "Insufficient"):
		r.Class = SchedInsufficientResource
		if m := reInsufficient.FindStringSubmatch(clause); m != nil {
			r.Resource = m[1]
		}
	case strings.Contains(lower, "too many pods"):
		r.Class = SchedInsufficientResource
		r.Resource = "pods"
	case strings.Contains(lower, "untolerated taint"):
		r.Class = SchedUntoleratedTaint
		r.TaintKey, r.TaintValue = parseTaintPayload(clause)
		// A cordon / not-ready taint is really a node-availability problem,
		// not a pod-misconfiguration; classify it as such so the UI doesn't
		// tell the user to "add a toleration" for node.kubernetes.io/*.
		if isNodeLifecycleTaint(r.TaintKey) {
			r.Class = SchedNodeUnschedulable
		}
	case strings.Contains(lower, "volume node affinity"):
		// Must precede the bare "node affinity" check below — this clause
		// contains the substring "node affinity" but is a volume-topology
		// failure, not a pod node-affinity mismatch.
		r.Class = SchedVolumeNodeAffinity
	case strings.Contains(lower, "anti-affinity"):
		r.Class = SchedPodAntiAffinity
	case strings.Contains(lower, "node affinity") || strings.Contains(lower, "node selector"):
		r.Class = SchedNodeAffinitySelector
	case strings.Contains(lower, "pod affinity"):
		r.Class = SchedPodAffinity
	case strings.Contains(lower, "topology spread"):
		r.Class = SchedTopologySpread
	case strings.Contains(lower, "max volume count"):
		r.Class = SchedVolumeCount
	case strings.Contains(lower, "free ports"):
		r.Class = SchedNoPorts
	case strings.Contains(lower, "unbound") && strings.Contains(lower, "persistentvolumeclaim"),
		strings.Contains(lower, "persistent volumes to bind"):
		r.Class = SchedVolumeBinding
	case strings.Contains(lower, "unschedulable"), strings.Contains(lower, "were not ready"):
		r.Class = SchedNodeUnschedulable
	default:
		r.Class = SchedOther
	}
	return r, true
}

// parseTaintPayload extracts key/value from an "untolerated taint {k: v}"
// or "{k}" clause. Returns empty strings if no {…} payload is present.
func parseTaintPayload(clause string) (key, value string) {
	m := reTaint.FindStringSubmatch(clause)
	if m == nil {
		return "", ""
	}
	inner := strings.TrimSpace(m[1])
	if inner == "" {
		return "", ""
	}
	if k, v, ok := strings.Cut(inner, ":"); ok {
		return strings.TrimSpace(k), strings.TrimSpace(v)
	}
	return inner, ""
}

// isNodeLifecycleTaint reports whether a taint key is one the control plane
// sets to mark a node temporarily unusable (cordon, not-ready, pressure),
// as opposed to an operator-applied dedicated/workload taint.
func isNodeLifecycleTaint(key string) bool {
	return strings.HasPrefix(key, "node.kubernetes.io/") ||
		strings.HasPrefix(key, "node-role.kubernetes.io/") ||
		strings.HasPrefix(key, "node.cloudprovider.kubernetes.io/")
}

// ---- Node-fit resolution ------------------------------------------------
//
// The scheduler reports "N node(s) didn't match Pod's node affinity/selector"
// without naming WHICH label is unsatisfiable. resolveUnsatisfiableNodeSelector
// joins the pod's nodeSelector + required nodeAffinity against the fleet's
// node labels to name the specific offending key — turning the opaque verdict
// into "no node has kubernetes.io/arch=arm64 (6 nodes are amd64)". This is the
// step that makes arch/os/zone/instance-type mismatches self-explanatory.
//
// These functions are pure (operate on plain NodeFacts / PodPlacement); the
// detector populates them from the live node cache.

// NodeFacts is the minimal per-node view the fit resolver needs.
type NodeFacts struct {
	Name   string
	Labels map[string]string
}

// MatchExpr is a node-affinity match expression (key, operator, values).
type MatchExpr struct {
	Key      string
	Operator string // In, NotIn, Exists, DoesNotExist, Gt, Lt
	Values   []string
}

// NodeSelectorTermFacts is one required nodeAffinity term — a node satisfies
// the term if it matches ALL of the term's expressions.
type NodeSelectorTermFacts struct {
	Expressions []MatchExpr
}

// PodPlacement is the pod's scheduling constraints, extracted from its spec.
type PodPlacement struct {
	NodeSelector map[string]string
	// RequiredNodeAffinity is the flattened requiredDuringScheduling terms.
	// A node satisfies the affinity if it matches ANY term.
	RequiredNodeAffinity []NodeSelectorTermFacts
}

// resolveUnsatisfiableNodeSelector returns human-readable explanations of
// which label requirement no node satisfies, naming the offending key(s)
// and the values the fleet actually carries. Empty slice means the pod's
// label constraints are individually satisfiable (so the placement failure
// lies elsewhere — taints, resources, a term combination).
func resolveUnsatisfiableNodeSelector(p PodPlacement, nodes []NodeFacts) []string {
	var out []string

	for _, k := range sortedKeys(p.NodeSelector) {
		v := p.NodeSelector[k]
		if countNodesWithLabel(nodes, k, v) == 0 {
			out = append(out, explainMissingLabel(k, v, nodes))
		}
	}

	if len(p.RequiredNodeAffinity) > 0 && !anyTermMatches(p.RequiredNodeAffinity, nodes) {
		seen := map[string]bool{}
		var affinityMsgs []string
		for _, term := range p.RequiredNodeAffinity {
			for _, e := range term.Expressions {
				if countNodesMatchingExpr(nodes, e) == 0 {
					msg := explainMissingExpr(e, nodes)
					if !seen[msg] {
						seen[msg] = true
						affinityMsgs = append(affinityMsgs, msg)
					}
				}
			}
		}
		if len(affinityMsgs) == 0 {
			// Every expression is individually satisfiable but no single
			// node satisfies a whole term — a constraint combination.
			affinityMsgs = append(affinityMsgs, "no node satisfies the pod's required nodeAffinity term combination")
		}
		out = append(out, affinityMsgs...)
	}

	return out
}

func explainMissingLabel(key, val string, nodes []NodeFacts) string {
	present := distinctLabelValues(nodes, key)
	if len(present) == 0 {
		return fmt.Sprintf("no node carries label %s (pod requires %s=%s)", key, key, val)
	}
	return fmt.Sprintf("no node has %s=%s — %d node(s) carry %s: [%s]",
		key, val, countNodesWithLabelKey(nodes, key), key, strings.Join(present, ", "))
}

func explainMissingExpr(e MatchExpr, nodes []NodeFacts) string {
	present := distinctLabelValues(nodes, e.Key)
	switch e.Operator {
	case "In":
		if len(present) == 0 {
			return fmt.Sprintf("no node carries label %s (pod requires %s in [%s])", e.Key, e.Key, strings.Join(e.Values, ", "))
		}
		return fmt.Sprintf("no node has %s in [%s] — fleet %s: [%s]", e.Key, strings.Join(e.Values, ", "), e.Key, strings.Join(present, ", "))
	case "Exists":
		return fmt.Sprintf("no node carries label %s (pod requires it to exist)", e.Key)
	case "DoesNotExist":
		return fmt.Sprintf("every node carries label %s (pod requires it absent)", e.Key)
	case "NotIn":
		return fmt.Sprintf("every node has %s in [%s] (pod requires otherwise)", e.Key, strings.Join(e.Values, ", "))
	default:
		return fmt.Sprintf("no node satisfies nodeAffinity %s %s [%s]", e.Key, e.Operator, strings.Join(e.Values, ", "))
	}
}

func anyTermMatches(terms []NodeSelectorTermFacts, nodes []NodeFacts) bool {
	for _, n := range nodes {
		for _, term := range terms {
			if nodeMatchesTerm(n, term) {
				return true
			}
		}
	}
	return false
}

func nodeMatchesTerm(n NodeFacts, term NodeSelectorTermFacts) bool {
	for _, e := range term.Expressions {
		if !nodeMatchesExpr(n, e) {
			return false
		}
	}
	return true
}

func nodeMatchesExpr(n NodeFacts, e MatchExpr) bool {
	v, ok := n.Labels[e.Key]
	switch e.Operator {
	case "In":
		return ok && slices.Contains(e.Values, v)
	case "NotIn":
		return !ok || !slices.Contains(e.Values, v)
	case "Exists":
		return ok
	case "DoesNotExist":
		return !ok
	case "Gt", "Lt":
		if !ok || len(e.Values) == 0 {
			return false
		}
		nv, err1 := strconv.ParseInt(v, 10, 64)
		bound, err2 := strconv.ParseInt(e.Values[0], 10, 64)
		if err1 != nil || err2 != nil {
			return false
		}
		if e.Operator == "Gt" {
			return nv > bound
		}
		return nv < bound
	default:
		return false
	}
}

func countNodesMatchingExpr(nodes []NodeFacts, e MatchExpr) int {
	n := 0
	for _, node := range nodes {
		if nodeMatchesExpr(node, e) {
			n++
		}
	}
	return n
}

func countNodesWithLabel(nodes []NodeFacts, key, val string) int {
	n := 0
	for _, node := range nodes {
		if node.Labels[key] == val {
			n++
		}
	}
	return n
}

func countNodesWithLabelKey(nodes []NodeFacts, key string) int {
	n := 0
	for _, node := range nodes {
		if _, ok := node.Labels[key]; ok {
			n++
		}
	}
	return n
}

func distinctLabelValues(nodes []NodeFacts, key string) []string {
	seen := map[string]bool{}
	var out []string
	for _, node := range nodes {
		if v, ok := node.Labels[key]; ok && !seen[v] {
			seen[v] = true
			out = append(out, v)
		}
	}
	sort.Strings(out)
	return out
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// ---- Bind-time detection ------------------------------------------------

// DetectSchedulingProblems flags Pending pods the scheduler tried to place
// and rejected (PodScheduled=False). It reads the scheduler's own verdict
// from the condition message — current state, one row per pod, no event
// noise — decomposes it, and resolves node-affinity/selector misses against
// the live node cache so the Message names the specific offending constraint
// (arch/zone/taint/resources) instead of just "Pending". namespace="" scans
// all namespaces. Post-bind (ContainerCreating/CNI/volume) and admission
// (quota with no Pod) failures are handled by separate detectors.
func DetectSchedulingProblems(cache *ResourceCache, namespace string) []Detection {
	if cache == nil {
		return nil
	}
	var problems []Detection
	now := time.Now()
	nodes := schedulingNodeFacts(cache)

	for _, pods := range listPodsByNamespace(cache, namespace) {
		for _, pod := range pods {
			if pod.Status.Phase != corev1.PodPending {
				continue
			}
			cond := podScheduledCondition(pod)
			// PodScheduled=False with reason=Unschedulable is the scheduler's
			// definitive "I tried and couldn't place this" — present only after
			// a real scheduling attempt, so no age grace is needed. reason=
			// SchedulingGated is NOT a failure: the scheduler hasn't tried yet
			// because the pod carries scheduling gates (a controller will lift
			// them), so it must not surface as unschedulable.
			if cond == nil || cond.Status != corev1.ConditionFalse || cond.Reason != corev1.PodReasonUnschedulable {
				continue
			}
			ageDur := now.Sub(pod.CreationTimestamp.Time)
			dur := ageDur
			if !cond.LastTransitionTime.IsZero() {
				dur = now.Sub(cond.LastTransitionTime.Time)
			}
			ownerGroup, ownerKind, ownerName := podOwnerKindName(cache, pod)
			schedMessage, schedAction := diagnoseUnschedulable(pod, cond.Message, nodes)
			problems = append(problems, Detection{
				Kind:            "Pod",
				Namespace:       pod.Namespace,
				Name:            pod.Name,
				Severity:        schedulingSeverity(dur),
				Reason:          "Unschedulable",
				Action:          schedAction,
				Message:         schedMessage,
				Age:             FormatAge(ageDur),
				AgeSeconds:      int64(ageDur.Seconds()),
				Duration:        FormatAge(dur),
				DurationSeconds: int64(dur.Seconds()),
				OwnerGroup:      ownerGroup,
				OwnerKind:       ownerKind,
				OwnerName:       ownerName,
			})
		}
	}
	return problems
}

func podScheduledCondition(pod *corev1.Pod) *corev1.PodCondition {
	return podCondition(pod, corev1.PodScheduled)
}

func podCondition(pod *corev1.Pod, condType corev1.PodConditionType) *corev1.PodCondition {
	for i := range pod.Status.Conditions {
		if pod.Status.Conditions[i].Type == condType {
			return &pod.Status.Conditions[i]
		}
	}
	return nil
}

// schedulingSeverity ramps with how long the pod has been unschedulable: a
// momentary miss right after creation is usually transient; one stuck for
// many minutes is a real, operator-actionable failure.
func schedulingSeverity(d time.Duration) string {
	switch {
	case d >= 10*time.Minute:
		return "critical"
	case d >= 2*time.Minute:
		return "high"
	default:
		return "medium"
	}
}

// genericUnschedulableAction is the fallback next step when no reason class is
// confidently actionable (Other / unparseable verdict).
const genericUnschedulableAction = "Use the scheduler message to decide: free or add capacity, or fix the pod's nodeSelector / affinity / tolerations so it matches an available node."

// diagnoseUnschedulable parses the scheduler verdict ONCE and returns both the
// operator-facing message and a targeted next-step action, so the two can't be
// derived from different parses. Pure over its inputs.
func diagnoseUnschedulable(pod *corev1.Pod, schedMsg string, nodes []NodeFacts) (message, action string) {
	total, reasons := parseSchedulerMessage(schedMsg)
	message = renderUnschedulableMessage(pod, schedMsg, total, reasons, nodes)
	action = unschedulableAction(reasons)
	if action == "" {
		action = genericUnschedulableAction
	}
	return message, action
}

// describeUnschedulable builds the operator-facing message: lead with the
// resolved offending constraint (the value the bare scheduler verdict hides)
// when we can name it, then summarize the scheduler's per-predicate counts.
func describeUnschedulable(pod *corev1.Pod, schedMsg string, nodes []NodeFacts) string {
	msg, _ := diagnoseUnschedulable(pod, schedMsg, nodes)
	return msg
}

func renderUnschedulableMessage(pod *corev1.Pod, schedMsg string, total int, reasons []SchedulingReason, nodes []NodeFacts) string {
	var parts []string
	resolvedAffinity := false
	for _, r := range reasons {
		if r.Class == SchedNodeAffinitySelector {
			if resolved := resolveUnsatisfiableNodeSelector(extractPodPlacement(pod), nodes); len(resolved) > 0 {
				parts = append(parts, resolved...)
				resolvedAffinity = true
			}
			break
		}
	}
	if summary := summarizeReasons(reasons, resolvedAffinity); summary != "" {
		parts = append(parts, summary)
	}
	if len(parts) == 0 {
		if msg := strings.TrimSpace(schedMsg); msg != "" {
			return msg
		}
		return "Pod is unschedulable"
	}
	msg := strings.Join(parts, "; ")
	if total > 0 {
		msg = fmt.Sprintf("%s (0/%d nodes available)", msg, total)
	}
	return msg
}

// unschedulableAction turns the parsed scheduler reasons into a concrete next
// step. Leads with the dominant reason (the one that rejected the most nodes —
// "start here"), but when more than one class blocks placement it says so rather
// than implying the single fix is sufficient. Returns "" when nothing is
// confidently actionable so the caller can fall back to the generic action.
func unschedulableAction(reasons []SchedulingReason) string {
	// Lead with the dominant reason that is ALSO actionable — picking the
	// highest-NodeCount overall would fall back to generic when an unparsed
	// (SchedOther) clause happens to reject the most nodes, even though a
	// smaller, actionable clause (e.g. VolumeBinding) is present.
	base := ""
	bestCount := -1
	distinct := map[SchedReasonClass]bool{}
	for _, r := range reasons {
		distinct[r.Class] = true
		if a := actionForSchedClass(r); a != "" && r.NodeCount > bestCount {
			base, bestCount = a, r.NodeCount
		}
	}
	if base == "" {
		return ""
	}
	if len(distinct) > 1 {
		base += " More than one constraint blocks scheduling — the message lists them all."
	}
	return base
}

// actionForSchedClass maps one reason class to its targeted next step. Text is
// deliberately non-absolute (the verdict counts a candidate set, not provably
// every node) and class-accurate (max-pods isn't fixed by lowering CPU; node-
// role taints aren't broken nodes; volume binding ≠ volume node-affinity).
func actionForSchedClass(r SchedulingReason) string {
	switch r.Class {
	case SchedInsufficientResource:
		switch {
		case r.Resource == "pods":
			return "Candidate nodes have hit their max-pods limit — add nodes (or raise the kubelet's max-pods), or pack fewer pods per node."
		case r.Resource != "":
			return fmt.Sprintf("Candidate nodes don't have enough %s — lower the pod's %s request, free capacity, or add nodes.", r.Resource, r.Resource)
		default:
			return "Candidate nodes don't have enough capacity — lower the pod's requests, free capacity, or add nodes."
		}
	case SchedUntoleratedTaint:
		if r.TaintKey != "" {
			return fmt.Sprintf("Add a toleration for taint %q, or remove it from the target nodes.", r.TaintKey)
		}
		return "Add a toleration for the blocking taint(s), or remove them from the target nodes."
	case SchedNodeAffinitySelector:
		return "No node matches the pod's nodeSelector/affinity — relax it, or label (or add) a matching node."
	case SchedPodAffinity:
		return "The pod-affinity rule can't be satisfied — make sure matching pods run in the required topology, or relax the rule."
	case SchedPodAntiAffinity:
		return "The pod-anti-affinity rule can't be satisfied — relax it, or add nodes in the required topology."
	case SchedTopologySpread:
		return "The topology-spread constraint can't be satisfied — relax it, or add capacity in the under-filled topology."
	case SchedVolumeBinding:
		return "The pod's volume can't be provisioned or bound — check the PVC, its StorageClass/provisioner, and storage quota."
	case SchedVolumeNodeAffinity:
		return "The pod's bound volume can't be placed on any candidate node (its zone/topology doesn't match where the pod can run) — align the pod's placement with the volume's topology, free capacity there, or use a volume that can move."
	case SchedVolumeCount:
		return "Candidate nodes have hit their volume-attachment limit — attach fewer volumes per node, or add nodes."
	case SchedNoPorts:
		return "The requested hostPort is taken on every candidate node — change the port, or free it."
	case SchedNodeUnschedulable:
		return "Candidate nodes are unavailable (cordoned, not-ready, or reserved by a taint) — uncordon/recover a node, or tolerate the reservation taint."
	}
	return ""
}

// summarizeReasons renders the parsed predicate counts into a compact phrase.
// When skipAffinity is set, the generic node-affinity/selector clause is
// omitted because describeUnschedulable already emitted the resolved label.
//
// Clauses are ordered by how many nodes each rejected, descending — the
// scheduler emits them in an arbitrary predicate order, so leading with the
// widest-blast-radius constraint surfaces the dominant reason first ("2 node(s)
// node affinity/selector mismatch" before "1 node(s) pod anti-affinity
// conflict") instead of whichever predicate the scheduler happened to list
// first. Stable, so equal counts keep the scheduler's order; count-0
// whole-message clauses (e.g. unbound PVC) sink to the end.
func summarizeReasons(reasons []SchedulingReason, skipAffinity bool) string {
	ordered := make([]SchedulingReason, len(reasons))
	copy(ordered, reasons)
	sort.SliceStable(ordered, func(i, j int) bool { return ordered[i].NodeCount > ordered[j].NodeCount })

	var parts []string
	for _, r := range ordered {
		switch r.Class {
		case SchedInsufficientResource:
			res := r.Resource
			if res == "" {
				res = "resources"
			}
			parts = append(parts, fmt.Sprintf("%s insufficient %s", nodesPhrase(r.NodeCount), res))
		case SchedUntoleratedTaint:
			t := r.TaintKey
			if r.TaintValue != "" {
				t += "=" + r.TaintValue
			}
			parts = append(parts, fmt.Sprintf("%s untolerated taint %s", nodesPhrase(r.NodeCount), t))
		case SchedNodeAffinitySelector:
			if skipAffinity {
				continue
			}
			parts = append(parts, fmt.Sprintf("%s node affinity/selector mismatch", nodesPhrase(r.NodeCount)))
		case SchedPodAffinity:
			parts = append(parts, fmt.Sprintf("%s pod affinity unmet", nodesPhrase(r.NodeCount)))
		case SchedPodAntiAffinity:
			parts = append(parts, fmt.Sprintf("%s pod anti-affinity conflict", nodesPhrase(r.NodeCount)))
		case SchedTopologySpread:
			parts = append(parts, fmt.Sprintf("%s topology-spread unmet", nodesPhrase(r.NodeCount)))
		case SchedVolumeNodeAffinity:
			parts = append(parts, fmt.Sprintf("%s volume node-affinity conflict", nodesPhrase(r.NodeCount)))
		case SchedVolumeBinding:
			parts = append(parts, "unbound PersistentVolumeClaim")
		case SchedVolumeCount:
			parts = append(parts, fmt.Sprintf("%s at max volume count", nodesPhrase(r.NodeCount)))
		case SchedNoPorts:
			parts = append(parts, fmt.Sprintf("%s no free host ports", nodesPhrase(r.NodeCount)))
		case SchedNodeUnschedulable:
			parts = append(parts, fmt.Sprintf("%s cordoned/not-ready", nodesPhrase(r.NodeCount)))
		default:
			if r.Raw != "" {
				parts = append(parts, r.Raw)
			}
		}
	}
	return strings.Join(parts, ", ")
}

func nodesPhrase(n int) string {
	if n <= 0 {
		return "node(s)"
	}
	return fmt.Sprintf("%d node(s)", n)
}

// extractPodPlacement pulls the pod's node-targeting constraints (nodeSelector
// + required nodeAffinity matchExpressions) into the resolver's plain shape.
func extractPodPlacement(pod *corev1.Pod) PodPlacement {
	p := PodPlacement{NodeSelector: pod.Spec.NodeSelector}
	if pod.Spec.Affinity == nil || pod.Spec.Affinity.NodeAffinity == nil {
		return p
	}
	req := pod.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution
	if req == nil {
		return p
	}
	for _, term := range req.NodeSelectorTerms {
		var t NodeSelectorTermFacts
		for _, e := range term.MatchExpressions {
			t.Expressions = append(t.Expressions, MatchExpr{
				Key:      e.Key,
				Operator: string(e.Operator),
				Values:   e.Values,
			})
		}
		if len(t.Expressions) > 0 {
			p.RequiredNodeAffinity = append(p.RequiredNodeAffinity, t)
		}
	}
	return p
}

// schedulingNodeFacts snapshots the node cache into the resolver's plain
// NodeFacts shape (labels + taints + cordon state).
func schedulingNodeFacts(cache *ResourceCache) []NodeFacts {
	lister := cache.Nodes()
	if lister == nil {
		return nil
	}
	nodeList, _ := lister.List(labels.Everything())
	facts := make([]NodeFacts, 0, len(nodeList))
	for _, n := range nodeList {
		facts = append(facts, NodeFacts{Name: n.Name, Labels: n.Labels})
	}
	return facts
}

// ---- Admission-layer detection ------------------------------------------
//
// The layer where NO pod is ever created: the controller's pod template is
// rejected at admission, so there's no Pod to inspect — the Deployment just
// sits at "Progressing". Detected reactively from controller FailedCreate
// events naming the workload blocked right now (exceeded quota / LimitRange /
// PodSecurity / webhook). Proactive "quota near/at limit" is deliberately NOT
// surfaced here — a saturated quota is namespace capacity context, not a live
// failure, and belongs in the Namespace quota view, not the issue stream.

// admissionFailureWindow bounds how recently a FailedCreate must have fired
// to count as "still happening" — a stuck controller re-emits continuously,
// so a fresh LastTimestamp means the failure is active.
const admissionFailureWindow = 30 * time.Minute

// DetectAdmissionProblems flags pod-template rejections at admission time.
// namespace="" scans all namespaces.
func DetectAdmissionProblems(cache *ResourceCache, namespace string) []Detection {
	if cache == nil {
		return nil
	}
	return detectAdmissionFailures(cache, namespace)
}

func detectAdmissionFailures(cache *ResourceCache, namespace string) []Detection {
	if cache.Events() == nil {
		return detectAdmissionConditionProblems(cache, namespace, map[string]bool{})
	}
	var events []*corev1.Event
	if namespace != "" {
		events, _ = cache.Events().Events(namespace).List(labels.Everything())
	} else {
		events, _ = cache.Events().List(labels.Everything())
	}

	now := time.Now()
	// One row per blocked controller, showing the CURRENT blocker. A workload
	// emits a FailedCreate per attempt (each with a different generated pod name
	// → distinct cached events), and the active blocker can change within the
	// window (quota cleared, webhook now rejects). Informer List order is
	// arbitrary, so keep the LATEST event by LastTimestamp per object rather
	// than whichever happened to be iterated first.
	type admCandidate struct {
		ev     *corev1.Event
		reason string
	}
	latest := map[string]admCandidate{}
	var order []string
	for _, e := range events {
		if e.Reason != "FailedCreate" {
			continue
		}
		if t := eventLastTime(e); !t.IsZero() && now.Sub(t) > admissionFailureWindow {
			continue // stale — the controller stopped retrying
		}
		reason, ok := classifyAdmissionFailure(e.Message)
		if !ok {
			continue
		}
		obj := e.InvolvedObject
		// A blocked controller re-emits FailedCreate continuously, but a since-
		// recovered one's event lingers for the whole window — cross-check
		// current state so we don't flag a now-healthy workload as critical.
		if !admissionTargetStillBlocked(cache, obj) {
			continue
		}
		key := obj.Kind + "/" + obj.Namespace + "/" + obj.Name
		if cur, exists := latest[key]; exists {
			if eventLastTime(e).After(eventLastTime(cur.ev)) {
				latest[key] = admCandidate{ev: e, reason: reason}
			}
			continue
		}
		latest[key] = admCandidate{ev: e, reason: reason}
		order = append(order, key)
	}

	problems := make([]Detection, 0, len(order))
	for _, key := range order {
		c := latest[key]
		obj := c.ev.InvolvedObject
		ageDur := now.Sub(eventFirstTime(c.ev))
		problems = append(problems, Detection{
			Kind:            obj.Kind,
			Namespace:       obj.Namespace,
			Name:            obj.Name,
			Severity:        "critical",
			Reason:          c.reason,
			Message:         "pod creation blocked: " + strings.TrimSpace(c.ev.Message),
			Age:             FormatAge(ageDur),
			AgeSeconds:      int64(ageDur.Seconds()),
			Duration:        FormatAge(ageDur),
			DurationSeconds: int64(ageDur.Seconds()),
		})
	}
	seen := make(map[string]bool, len(problems))
	for _, p := range problems {
		seen[admissionProblemKey(p.Kind, p.Namespace, p.Name)] = true
	}
	problems = append(problems, detectAdmissionConditionProblems(cache, namespace, seen)...)
	return problems
}

// eventLastTime / eventFirstTime return the most-recent / earliest timestamp on
// an Event, falling back to EventTime (events API v1) when the legacy
// First/LastTimestamp fields are unset.
func eventLastTime(e *corev1.Event) time.Time {
	if !e.LastTimestamp.Time.IsZero() {
		return e.LastTimestamp.Time
	}
	return e.EventTime.Time
}

func eventFirstTime(e *corev1.Event) time.Time {
	if !e.FirstTimestamp.Time.IsZero() {
		return e.FirstTimestamp.Time
	}
	return e.EventTime.Time
}

// admissionTargetStillBlocked reports whether the controller named by a
// FailedCreate event still has unmet replicas, i.e. the rejection is active.
// A recovered workload has its replicas, so its lingering event is skipped.
// Unknown kinds / unreadable listers default to true — never drop genuine coverage.
func admissionTargetStillBlocked(cache *ResourceCache, obj corev1.ObjectReference) bool {
	// "Blocked" means the controller still can't CREATE the pods it needs,
	// NOT readiness. A workload whose pods were created but stay not-ready for
	// another reason (e.g. unschedulable after a quota was raised) has its pods
	// and is no longer admission-blocked. Deployments also need the updated
	// replica count checked so rolling updates blocked on new-pod creation do
	// not get masked by old replicas.
	switch obj.Kind {
	case "ReplicaSet":
		if l := cache.ReplicaSets(); l != nil {
			rs, err := l.ReplicaSets(obj.Namespace).Get(obj.Name)
			if err == nil {
				return rs.Status.Replicas < schedDesiredReplicas(rs.Spec.Replicas)
			}
			if apierrors.IsNotFound(err) {
				return false
			}
		}
	case "Deployment":
		if l := cache.Deployments(); l != nil {
			d, err := l.Deployments(obj.Namespace).Get(obj.Name)
			if err == nil {
				return deploymentNeedsPodCreation(d)
			}
			if apierrors.IsNotFound(err) {
				return false
			}
		}
	case "StatefulSet":
		if l := cache.StatefulSets(); l != nil {
			ss, err := l.StatefulSets(obj.Namespace).Get(obj.Name)
			if err == nil {
				return ss.Status.Replicas < schedDesiredReplicas(ss.Spec.Replicas)
			}
			if apierrors.IsNotFound(err) {
				return false
			}
		}
	case "DaemonSet":
		if l := cache.DaemonSets(); l != nil {
			ds, err := l.DaemonSets(obj.Namespace).Get(obj.Name)
			if err == nil {
				return ds.Status.CurrentNumberScheduled < ds.Status.DesiredNumberScheduled
			}
			if apierrors.IsNotFound(err) {
				return false
			}
		}
	case "Job":
		if l := cache.Jobs(); l != nil {
			j, err := l.Jobs(obj.Namespace).Get(obj.Name)
			if err == nil {
				// Only "blocked" if the Job has created NO pod yet — any of
				// Active/Succeeded/Failed > 0 means a pod was created (so the
				// rejection isn't admission-from-the-start), and a stale quota
				// event shouldn't surface for it. (Trade-off: a Job that ran
				// some pods, then gets quota-blocked mid-retry, is not flagged.)
				return j.Status.Active == 0 && j.Status.Succeeded == 0 && j.Status.Failed == 0
			}
			if apierrors.IsNotFound(err) {
				return false
			}
		}
	}
	return true
}

func schedDesiredReplicas(r *int32) int32 {
	if r == nil {
		return 1
	}
	return *r
}

func detectAdmissionConditionProblems(cache *ResourceCache, namespace string, seen map[string]bool) []Detection {
	var out []Detection
	now := time.Now()
	if seen == nil {
		seen = map[string]bool{}
	}

	if l := cache.ReplicaSets(); l != nil {
		var items []*appsv1.ReplicaSet
		if namespace != "" {
			items, _ = l.ReplicaSets(namespace).List(labels.Everything())
		} else {
			items, _ = l.List(labels.Everything())
		}
		for _, rs := range items {
			key := admissionProblemKey("ReplicaSet", rs.Namespace, rs.Name)
			if seen[key] || hasSeenDeploymentForReplicaSet(seen, rs) || rs.Status.Replicas >= schedDesiredReplicas(rs.Spec.Replicas) {
				continue
			}
			for _, c := range rs.Status.Conditions {
				if c.Type != appsv1.ReplicaSetReplicaFailure || c.Status != corev1.ConditionTrue {
					continue
				}
				if p, ok := admissionConditionProblem("ReplicaSet", rs.Namespace, rs.Name, c.Message, c.LastTransitionTime.Time, now); ok {
					out = append(out, p)
					seen[key] = true
					break
				}
			}
		}
	}

	if l := cache.Deployments(); l != nil {
		var items []*appsv1.Deployment
		if namespace != "" {
			items, _ = l.Deployments(namespace).List(labels.Everything())
		} else {
			items, _ = l.List(labels.Everything())
		}
		for _, d := range items {
			if !deploymentNeedsPodCreation(d) {
				continue
			}
			if hasSeenReplicaSetForDeployment(cache, seen, d.Namespace, d.Name) {
				continue
			}
			for _, c := range d.Status.Conditions {
				if c.Type != appsv1.DeploymentReplicaFailure || c.Status != corev1.ConditionTrue {
					continue
				}
				if p, ok := admissionConditionProblem("Deployment", d.Namespace, d.Name, c.Message, c.LastTransitionTime.Time, now); ok {
					key := admissionProblemKey(p.Kind, p.Namespace, p.Name)
					if !seen[key] {
						out = append(out, p)
						seen[key] = true
					}
				}
			}
		}
	}

	return out
}

func deploymentNeedsPodCreation(d *appsv1.Deployment) bool {
	desired := schedDesiredReplicas(d.Spec.Replicas)
	return desired > 0 && (d.Status.Replicas < desired || d.Status.UpdatedReplicas < desired)
}

func admissionConditionProblem(kind, namespace, name, message string, firstSeen, now time.Time) (Detection, bool) {
	reason, ok := classifyAdmissionFailure(message)
	if !ok {
		return Detection{}, false
	}
	if firstSeen.IsZero() {
		firstSeen = now
	}
	ageDur := now.Sub(firstSeen)
	return Detection{
		Kind:            kind,
		Namespace:       namespace,
		Name:            name,
		Severity:        "critical",
		Reason:          reason,
		Message:         "pod creation blocked: " + strings.TrimSpace(message),
		Age:             FormatAge(ageDur),
		AgeSeconds:      int64(ageDur.Seconds()),
		Duration:        FormatAge(ageDur),
		DurationSeconds: int64(ageDur.Seconds()),
	}, true
}

func admissionProblemKey(kind, namespace, name string) string {
	return kind + "/" + namespace + "/" + name
}

func hasSeenReplicaSetForDeployment(cache *ResourceCache, seen map[string]bool, namespace, deployment string) bool {
	if cache == nil || deployment == "" {
		return false
	}
	l := cache.ReplicaSets()
	if l == nil {
		return false
	}
	items, _ := l.ReplicaSets(namespace).List(labels.Everything())
	for _, rs := range items {
		if seen[admissionProblemKey("ReplicaSet", rs.Namespace, rs.Name)] && replicaSetOwnedByDeployment(rs, deployment) {
			return true
		}
	}
	return false
}

func hasSeenDeploymentForReplicaSet(seen map[string]bool, rs *appsv1.ReplicaSet) bool {
	deployment, ok := replicaSetDeploymentOwnerName(rs)
	if !ok {
		return false
	}
	return seen[admissionProblemKey("Deployment", rs.Namespace, deployment)]
}

func replicaSetOwnedByDeployment(rs *appsv1.ReplicaSet, deployment string) bool {
	name, ok := replicaSetDeploymentOwnerName(rs)
	return ok && name == deployment
}

func replicaSetDeploymentOwnerName(rs *appsv1.ReplicaSet) (string, bool) {
	if rs == nil {
		return "", false
	}
	owner := controllerOwnerRef(rs.OwnerReferences)
	if owner == nil || owner.Kind != "Deployment" || owner.Name == "" {
		return "", false
	}
	return owner.Name, true
}

// classifyAdmissionFailure maps a FailedCreate event message to a reason.
// Returns ok=false for FailedCreate messages that aren't admission denials
// (e.g. transient "object is being deleted") so we don't over-report.
func classifyAdmissionFailure(msg string) (string, bool) {
	lower := strings.ToLower(msg)
	switch {
	case strings.Contains(lower, "exceeded quota"), strings.Contains(lower, "failed quota"):
		return "QuotaExceeded", true
	case strings.Contains(lower, "violates podsecurity"), strings.Contains(lower, "violates pod security"):
		return "PodSecurityViolation", true
	case strings.Contains(lower, "admission webhook") && strings.Contains(lower, "denied"):
		return "WebhookDenied", true
	case strings.Contains(lower, "forbidden") && (strings.Contains(lower, "limitrange") ||
		strings.Contains(lower, "maximum") || strings.Contains(lower, "minimum")):
		return "LimitRangeViolation", true
	case strings.Contains(lower, "forbidden") &&
		strings.Contains(lower, "cannot create resource") &&
		strings.Contains(lower, `"pods"`):
		return "RBACForbidden", true
	default:
		return "", false
	}
}

// ---- Post-bind detection ------------------------------------------------
//
// The pod was scheduled (a node accepted it) but the kubelet can't bring it
// up — stuck in ContainerCreating because the CNI can't hand out an IP or the
// CSI can't attach/mount a volume. radar otherwise treats ContainerCreating
// as benign, so these silently sit as "Pending". The best failure detail lives
// in kubelet events (FailedCreatePodSandBox / FailedMount / FailedAttachVolume),
// but events expire; when no recent event remains, a narrow fallback catches
// the CNI/sandbox shape: scheduled, old, ContainerCreating, and no Pod IP.

const (
	postBindFailureWindow     = 10 * time.Minute
	postBindCriticalAfter     = 30 * time.Minute
	podReadyToStartContainers = corev1.PodConditionType("PodReadyToStartContainers")
)

var postBindSeverity = map[string]string{
	"IPExhaustion":          "critical",
	"SandboxCreationFailed": "high",
	"PostBindStartupStall":  "high",
	"VolumeMultiAttach":     "critical",
	"VolumeAttach":          "high",
	"VolumeMount":           "high",
}

// DetectPostBindProblems flags pods stuck in ContainerCreating due to CNI/IP
// or volume failures. namespace="" scans all namespaces.
func DetectPostBindProblems(cache *ResourceCache, namespace string) []Detection {
	now := time.Now()
	return detectPostBindProblems(cache, namespace, postBindStartupStallCounts(cache, []string{namespace}, now), now)
}

func DetectPostBindProblemsForNamespaces(cache *ResourceCache, namespaces []string) []Detection {
	if len(namespaces) == 0 {
		return DetectPostBindProblems(cache, "")
	}
	now := time.Now()
	nodeStallCounts := postBindStartupStallCounts(cache, namespaces, now)
	var out []Detection
	for _, ns := range namespaces {
		out = append(out, detectPostBindProblems(cache, ns, nodeStallCounts, now)...)
	}
	return out
}

func detectPostBindProblems(cache *ResourceCache, namespace string, nodeStallCounts map[string]int, now time.Time) []Detection {
	if cache == nil {
		return nil
	}
	stuck := stuckScheduledPods(cache, namespace)
	if len(stuck) == 0 {
		return nil
	}

	var events []*corev1.Event
	if eventLister := cache.Events(); eventLister != nil {
		if namespace != "" {
			events, _ = eventLister.Events(namespace).List(labels.Everything())
		} else {
			events, _ = eventLister.List(labels.Everything())
		}
	}

	// One row per stuck pod, showing the CURRENT blocker. The kubelet
	// re-emits a post-bind event per retry and the active cause can change
	// (NetworkNotReady → FailedMount). Informer List order is arbitrary, so
	// keep the LATEST event by LastTimestamp per pod rather than whichever was
	// iterated first — mirrors detectAdmissionFailures.
	type pbCandidate struct {
		ev     *corev1.Event
		reason string
	}
	latest := map[string]pbCandidate{}
	expiredLatest := map[string]pbCandidate{}
	var order []string
	for _, e := range events {
		if e.InvolvedObject.Kind != "Pod" {
			continue
		}
		reason, ok := classifyPostBindFailure(e.Reason, e.Message)
		if !ok {
			continue
		}
		key := e.InvolvedObject.Namespace + "/" + e.InvolvedObject.Name
		if _, isStuck := stuck[key]; !isStuck {
			continue
		}
		if t := eventLastTime(e); !t.IsZero() && now.Sub(t) > postBindFailureWindow {
			if cur, exists := expiredLatest[key]; !exists || t.After(eventLastTime(cur.ev)) {
				expiredLatest[key] = pbCandidate{ev: e, reason: reason}
			}
			continue
		}
		if cur, exists := latest[key]; exists {
			if eventLastTime(e).After(eventLastTime(cur.ev)) {
				latest[key] = pbCandidate{ev: e, reason: reason}
			}
			continue
		}
		latest[key] = pbCandidate{ev: e, reason: reason}
		order = append(order, key)
	}

	problems := make([]Detection, 0, len(order))
	for _, key := range order {
		c := latest[key]
		pod := stuck[key]
		ageDur := now.Sub(pod.CreationTimestamp.Time)
		severity := postBindProblemSeverity(c.reason, ageDur)
		ownerGroup, ownerKind, ownerName := podOwnerKindName(cache, pod)
		problems = append(problems, Detection{
			Kind:            "Pod",
			Namespace:       pod.Namespace,
			Name:            pod.Name,
			Severity:        severity,
			Reason:          c.reason,
			Message:         postBindEventMessage(pod, c.reason, c.ev.Message, nodeStallCounts),
			Age:             FormatAge(ageDur),
			AgeSeconds:      int64(ageDur.Seconds()),
			Duration:        FormatAge(ageDur),
			DurationSeconds: int64(ageDur.Seconds()),
			OwnerGroup:      ownerGroup,
			OwnerKind:       ownerKind,
			OwnerName:       ownerName,
		})
	}

	var fallbackKeys []string
	for key, pod := range stuck {
		if _, hasRecentEvent := latest[key]; hasRecentEvent {
			continue
		}
		if c, hasExpiredEvent := expiredLatest[key]; hasExpiredEvent && isVolumePostBindReason(c.reason) {
			continue
		}
		if !isPostBindStartupStallPod(pod, now) {
			continue
		}
		fallbackKeys = append(fallbackKeys, key)
	}
	sort.Strings(fallbackKeys)
	for _, key := range fallbackKeys {
		pod := stuck[key]
		ageDur := now.Sub(pod.CreationTimestamp.Time)
		ownerGroup, ownerKind, ownerName := podOwnerKindName(cache, pod)
		problems = append(problems, Detection{
			Kind:            "Pod",
			Namespace:       pod.Namespace,
			Name:            pod.Name,
			Severity:        postBindProblemSeverity("PostBindStartupStall", ageDur),
			Reason:          "PostBindStartupStall",
			Message:         postBindFallbackMessage(pod, ageDur, nodeStallCounts),
			Age:             FormatAge(ageDur),
			AgeSeconds:      int64(ageDur.Seconds()),
			Duration:        FormatAge(ageDur),
			DurationSeconds: int64(ageDur.Seconds()),
			OwnerGroup:      ownerGroup,
			OwnerKind:       ownerKind,
			OwnerName:       ownerName,
		})
	}
	return problems
}

// stuckScheduledPods returns Pending pods that the scheduler DID place
// (PodScheduled is not False) — i.e. owned by the post-bind layer, not the
// bind-time detector. Keyed "namespace/name".
func stuckScheduledPods(cache *ResourceCache, namespace string) map[string]*corev1.Pod {
	out := map[string]*corev1.Pod{}
	for _, pods := range listPodsByNamespace(cache, namespace) {
		for _, pod := range pods {
			if pod.Status.Phase != corev1.PodPending {
				continue
			}
			if cond := podScheduledCondition(pod); cond != nil && cond.Status == corev1.ConditionFalse {
				continue // unschedulable — the bind-time detector owns it
			}
			out[pod.Namespace+"/"+pod.Name] = pod
		}
	}
	return out
}

func postBindProblemSeverity(reason string, age time.Duration) string {
	if (reason == "SandboxCreationFailed" || reason == "PostBindStartupStall") && age >= postBindCriticalAfter {
		return "critical"
	}
	severity := postBindSeverity[reason]
	if severity == "" {
		return "high"
	}
	return severity
}

func isPostBindStartupStallPod(pod *corev1.Pod, now time.Time) bool {
	if pod == nil || pod.Status.Phase != corev1.PodPending || pod.Spec.NodeName == "" {
		return false
	}
	if cond := podScheduledCondition(pod); cond != nil && cond.Status == corev1.ConditionFalse {
		return false
	}
	if pod.CreationTimestamp.IsZero() || now.Sub(pod.CreationTimestamp.Time) <= postBindFailureWindow {
		return false
	}
	if podHasStatusIP(pod) {
		return false
	}
	for i := range pod.Status.ContainerStatuses {
		if w := pod.Status.ContainerStatuses[i].State.Waiting; w != nil && w.Reason == "ContainerCreating" {
			return true
		}
	}
	return false
}

func podHasStatusIP(pod *corev1.Pod) bool {
	if pod.Status.PodIP != "" {
		return true
	}
	for _, ip := range pod.Status.PodIPs {
		if ip.IP != "" {
			return true
		}
	}
	return false
}

func postBindStartupStallCounts(cache *ResourceCache, namespaces []string, now time.Time) map[string]int {
	counts := map[string]int{}
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}
	suppressed := expiredVolumePostBindPodKeys(cache, namespaces, now)
	seen := map[string]bool{}
	for _, namespace := range namespaces {
		for _, pods := range listPodsByNamespace(cache, namespace) {
			for _, pod := range pods {
				key := pod.Namespace + "/" + pod.Name
				if seen[key] {
					continue
				}
				seen[key] = true
				if suppressed[key] {
					continue
				}
				if !isPostBindStartupStallPod(pod, now) {
					continue
				}
				counts[pod.Spec.NodeName]++
			}
		}
	}
	return counts
}

func expiredVolumePostBindPodKeys(cache *ResourceCache, namespaces []string, now time.Time) map[string]bool {
	out := map[string]bool{}
	if cache == nil {
		return out
	}
	eventLister := cache.Events()
	if eventLister == nil {
		return out
	}
	if len(namespaces) == 0 {
		namespaces = []string{""}
	}
	latestTime := map[string]time.Time{}
	latestReason := map[string]string{}
	for _, namespace := range namespaces {
		var events []*corev1.Event
		if namespace != "" {
			events, _ = eventLister.Events(namespace).List(labels.Everything())
		} else {
			events, _ = eventLister.List(labels.Everything())
		}
		for _, e := range events {
			if e.InvolvedObject.Kind != "Pod" {
				continue
			}
			reason, ok := classifyPostBindFailure(e.Reason, e.Message)
			if !ok {
				continue
			}
			t := eventLastTime(e)
			if t.IsZero() || now.Sub(t) <= postBindFailureWindow {
				continue
			}
			key := e.InvolvedObject.Namespace + "/" + e.InvolvedObject.Name
			if cur, exists := latestTime[key]; !exists || t.After(cur) {
				latestTime[key] = t
				latestReason[key] = reason
			}
		}
	}
	for key, reason := range latestReason {
		if isVolumePostBindReason(reason) {
			out[key] = true
		}
	}
	return out
}

func postBindEventMessage(pod *corev1.Pod, reason, eventMessage string, nodeStallCounts map[string]int) string {
	msg := "stuck creating"
	if pod.Spec.NodeName != "" {
		msg += " on node " + pod.Spec.NodeName
	}
	if eventMessage = strings.TrimSpace(eventMessage); eventMessage != "" {
		msg += ": " + eventMessage
	}
	return appendPostBindNodeCorrelation(msg, pod, reason, nodeStallCounts)
}

func postBindFallbackMessage(pod *corev1.Pod, age time.Duration, nodeStallCounts map[string]int) string {
	parts := []string{fmt.Sprintf("container is ContainerCreating with no Pod IP after %s", FormatAge(age))}
	if cond := podCondition(pod, podReadyToStartContainers); cond != nil && cond.Status == corev1.ConditionFalse {
		parts = append(parts, "PodReadyToStartContainers=False")
	}
	msg := fmt.Sprintf("stuck before container start on node %s: %s; no matching recent kubelet event found; check kubelet, container runtime, and CNI on that node",
		pod.Spec.NodeName, strings.Join(parts, "; "))
	return appendPostBindNodeCorrelation(msg, pod, "PostBindStartupStall", nodeStallCounts)
}

func appendPostBindNodeCorrelation(msg string, pod *corev1.Pod, reason string, nodeStallCounts map[string]int) string {
	if !isNetworkPostBindReason(reason) || pod.Spec.NodeName == "" {
		return msg
	}
	if count := nodeStallCounts[pod.Spec.NodeName]; count > 1 {
		return fmt.Sprintf("%s; same node has %d visible pods stuck before container start", msg, count)
	}
	return msg
}

func isNetworkPostBindReason(reason string) bool {
	switch reason {
	case "IPExhaustion", "SandboxCreationFailed", "PostBindStartupStall":
		return true
	default:
		return false
	}
}

func isVolumePostBindReason(reason string) bool {
	switch reason {
	case "VolumeMultiAttach", "VolumeAttach", "VolumeMount":
		return true
	default:
		return false
	}
}

// classifyPostBindFailure maps a kubelet event (reason + message) to a
// post-bind failure class, distinguishing IP exhaustion from generic sandbox
// failures and multi-attach from generic volume-attach errors.
func classifyPostBindFailure(reason, msg string) (string, bool) {
	lower := strings.ToLower(msg)
	switch {
	case reason == "FailedCreatePodSandBox" || strings.Contains(lower, "failed to create pod sandbox"):
		if strings.Contains(lower, "assign an ip") ||
			strings.Contains(lower, "insufficientfreeaddresses") ||
			strings.Contains(lower, "no ip addresses available") ||
			strings.Contains(lower, "all ip addresses") {
			return "IPExhaustion", true
		}
		return "SandboxCreationFailed", true
	case reason == "FailedAttachVolume":
		if strings.Contains(lower, "multi-attach") {
			return "VolumeMultiAttach", true
		}
		return "VolumeAttach", true
	case reason == "FailedMount":
		return "VolumeMount", true
	default:
		return "", false
	}
}
