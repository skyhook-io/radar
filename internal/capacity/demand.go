package capacity

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/scheduling"
	"github.com/skyhook-io/radar/pkg/subject"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/klog/v2"
)

const (
	DefaultDemandPodRefLimit          = 50
	DefaultDemandPoolEvaluationLimit  = 25
	defaultDemandEvidenceLimit        = 10
	defaultDemandUnknownLimit         = 10
	defaultDemandSchedulerReasonLimit = 10
	defaultDemandConstraintLimit      = 50
	defaultDemandTolerationLimit      = 50
)

type DemandPoolInput struct {
	NodePool          *unstructured.Unstructured
	NodeClassReady    *bool
	ProvisionedKnown  bool
	UnknownPredicates []string
	// ObservedMemberShapes holds the distinct member capacity vectors ever
	// observed for this pool (node allocatable / claim capacity). A pod whose
	// requests fit none of them cannot be proven placeable — instance shapes
	// aren't simulated — so compatibility degrades to unknown.
	ObservedMemberShapes []corev1.ResourceList
}

type DemandInput struct {
	GeneratedAt         time.Time
	Pods                []*corev1.Pod
	Pools               []DemandPoolInput
	PodRefLimit         int
	PoolEvaluationLimit int
	ResolvePodOwner     func(*corev1.Pod) *subject.Ref
}

type demandRequirement struct {
	Key        string
	Operator   corev1.NodeSelectorOperator
	Values     []string
	SourcePath string
}

type demandAffinityTerm struct {
	Requirements []demandRequirement
	Unknown      []string
	Canonical    canonicalAffinityTerm
	sortKey      string
	Empty        bool
}

type demandSchedulingModel struct {
	NodeSelector        []demandRequirement
	AffinityTerms       []demandAffinityTerm
	HasRequiredAffinity bool
	RequiredEmpty       bool
	Tolerations         []corev1.Toleration
	Unknown             []string
	Signature           capacityapi.SchedulingSignature
}

type canonicalRequirement struct {
	Key      string   `json:"key"`
	Operator string   `json:"operator"`
	Values   []string `json:"values"`
}

type canonicalAffinityTerm struct {
	Requirements []canonicalRequirement `json:"requirements"`
	MatchFields  []canonicalRequirement `json:"matchFields"`
}

type canonicalToleration struct {
	Key               string `json:"key"`
	Operator          string `json:"operator"`
	Value             string `json:"value"`
	Effect            string `json:"effect"`
	TolerationSeconds *int64 `json:"tolerationSeconds,omitempty"`
}

type canonicalSchedulingModel struct {
	NodeSelector     []canonicalRequirement   `json:"nodeSelector"`
	RequiredAffinity *[]canonicalAffinityTerm `json:"requiredAffinity,omitempty"`
	Tolerations      []canonicalToleration    `json:"tolerations"`
	Opaque           []string                 `json:"opaque"`
}

type canonicalDemandGroup struct {
	Namespace           string                     `json:"namespace"`
	Owner               subject.Ref                `json:"owner"`
	Requests            capacityapi.ResourceVector `json:"requests"`
	SchedulingSignature string                     `json:"schedulingSignature"`
}

type demandGroupAccumulator struct {
	group    capacityapi.DemandGroup
	requests corev1.ResourceList
	model    demandSchedulingModel
	pods     []*corev1.Pod
}

type DemandGroupModel struct {
	Group      capacityapi.DemandGroup
	requests   corev1.ResourceList
	scheduling demandSchedulingModel
	podRefs    []subject.Ref
}

func (m DemandGroupModel) PodRefs() []subject.Ref {
	return append([]subject.Ref(nil), m.podRefs...)
}

type demandEvidenceCollection struct {
	values []capacityapi.EvaluationEvidence
	total  int
}

type demandSchedulerReasonAggregate struct {
	code      string
	source    string
	message   string
	count     int
	firstSeen time.Time
}

func (c *demandEvidenceCollection) add(predicate, sourcePath string, observed, expected []string, explanation string) {
	c.total++
	if len(c.values) >= defaultDemandEvidenceLimit {
		return
	}
	c.values = append(c.values, demandEvidence(predicate, sourcePath, observed, expected, explanation))
}

func (c *demandEvidenceCollection) merge(other demandEvidenceCollection) {
	c.total += other.total
	remaining := defaultDemandEvidenceLimit - len(c.values)
	if remaining <= 0 {
		return
	}
	values := other.values
	if len(values) > remaining {
		values = values[:remaining]
	}
	c.values = append(c.values, values...)
}

func BuildDemandGroups(input DemandInput) []capacityapi.DemandGroup {
	models := BuildDemandGroupModels(input)
	ClassifyDemandGroupModels(models, input.Pools)
	return EvaluateDemandGroupModels(models, input.Pools, input.PoolEvaluationLimit)
}

func BuildDemandGroupModels(input DemandInput) []DemandGroupModel {
	refLimit := input.PodRefLimit
	if refLimit <= 0 {
		refLimit = DefaultDemandPodRefLimit
	}

	orderedPods := make([]*corev1.Pod, 0, len(input.Pods))
	for _, pod := range input.Pods {
		if pod != nil && pod.Spec.NodeName == "" && !isTerminal(pod) && pod.DeletionTimestamp == nil && !isDaemonSetPod(pod) {
			orderedPods = append(orderedPods, pod)
		}
	}
	sort.Slice(orderedPods, func(i, j int) bool { return demandPodKey(orderedPods[i]) < demandPodKey(orderedPods[j]) })

	groups := map[string]*demandGroupAccumulator{}
	for _, pod := range orderedPods {
		owner := DemandOwnerForPod(pod, input.ResolvePodOwner)
		requests := EffectivePodRequests(pod)
		model := demandSchedulingModelForPod(pod, requests)
		fingerprint := stableDemandFingerprint(canonicalDemandGroup{
			Namespace:           pod.Namespace,
			Owner:               owner,
			Requests:            ResourceVector(requests),
			SchedulingSignature: model.Signature.Fingerprint,
		})
		group := groups[fingerprint]
		if group == nil {
			value := capacityapi.NewDemandGroup(input.GeneratedAt)
			value.ID = "demand-" + fingerprint
			value.Fingerprint = "sha256:" + fingerprint
			value.Namespace = pod.Namespace
			value.Owner = copySubjectRef(owner)
			value.PerPodRequests = QuantityObservation(requests, capacityapi.CertaintyExact, capacityapi.GranularityAggregate, input.GeneratedAt, "pod.effectiveRequests")
			value.SchedulingSignature = model.Signature
			group = &demandGroupAccumulator{group: value, requests: requests.DeepCopy(), model: model}
			groups[fingerprint] = group
		}
		group.pods = append(group.pods, pod)
	}

	result := make([]DemandGroupModel, 0, len(groups))
	for _, aggregate := range groups {
		sort.Slice(aggregate.pods, func(i, j int) bool { return demandPodKey(aggregate.pods[i]) < demandPodKey(aggregate.pods[j]) })
		aggregate.group.PodCount = len(aggregate.pods)
		aggregate.group.PodsMeta.Total = len(aggregate.pods)
		returned := len(aggregate.pods)
		if returned > refLimit {
			returned = refLimit
			aggregate.group.PodsMeta.Truncated = true
		}
		aggregate.group.PodsMeta.Returned = returned
		for _, pod := range aggregate.pods[:returned] {
			aggregate.group.Pods = append(aggregate.group.Pods, identityForPod(pod))
		}
		if nominated := countNominatedPods(aggregate.pods); nominated > 0 {
			aggregate.group.NominatedPodCount = &nominated
		}
		aggregate.group.FirstSeen = demandFirstSeen(aggregate.pods, input.GeneratedAt)
		aggregate.group.LastSeen = input.GeneratedAt
		aggregate.group.AggregateRequests = QuantityObservation(
			multiplyResourceList(aggregate.requests, int64(len(aggregate.pods))),
			capacityapi.CertaintyExact,
			capacityapi.GranularityAggregate,
			input.GeneratedAt,
			"pod.effectiveRequests",
		)
		populateDemandSchedulerState(&aggregate.group, aggregate.pods, input.GeneratedAt)
		result = append(result, DemandGroupModel{
			Group:      aggregate.group,
			requests:   aggregate.requests,
			scheduling: aggregate.model,
			podRefs:    demandPodRefs(aggregate.pods),
		})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].Group.ID < result[j].Group.ID })
	return result
}

// countNominatedPods counts pods carrying a scheduler node nomination
// (status.nominatedNodeName) — the scheduler is preempting to make room for
// them. Nomination is best-effort and can go stale, so this only annotates a
// demand group; it never feeds State or pool eligibility.
func countNominatedPods(pods []*corev1.Pod) int {
	count := 0
	for _, pod := range pods {
		if pod != nil && pod.Status.NominatedNodeName != "" {
			count++
		}
	}
	return count
}

func demandPodRefs(pods []*corev1.Pod) []subject.Ref {
	refs := make([]subject.Ref, 0, len(pods))
	for _, pod := range pods {
		if pod == nil {
			continue
		}
		refs = append(refs, subject.Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name})
	}
	return refs
}

func EvaluateDemandGroupModels(models []DemandGroupModel, pools []DemandPoolInput, evaluationLimit int) []capacityapi.DemandGroup {
	orderedPools := sortedDemandPoolInputs(pools)
	result := make([]capacityapi.DemandGroup, 0, len(models))
	for _, model := range models {
		group := model.Group
		// Evaluation is a perspective, never a state owner: recomputing State
		// from a ?pool=-narrowed set would turn fleet-wide awaiting_capacity
		// into blocked. Classification owns State against the whole fleet.
		group.PoolEvaluations, group.PoolEvaluationsMeta, group.PoolEvaluationCounts = evaluateDemandPools(
			model.scheduling,
			model.requests,
			orderedPools,
			evaluationLimit,
		)
		result = append(result, group)
	}
	return result
}

// instanceShapeUnknowns records an unknown when no single observed member
// shape fits the per-pod requests across every requested resource at once —
// comparing whole vectors, not per-resource maxima, so a pool whose biggest
// CPU and biggest memory live on different instance types cannot fabricate a
// composite "largest instance" that fits. Instance shapes are not simulated
// (no provider offering catalogue), so such a pod cannot be proven placeable —
// a bare "declared compatible" there would be an overclaim in exactly the case
// the honesty framing exists for. With no observed members nothing can be
// inferred, so nothing is added.
func instanceShapeUnknowns(requests corev1.ResourceList, shapes []corev1.ResourceList) []string {
	if len(shapes) == 0 || len(requests) == 0 {
		return nil
	}
	for _, shape := range shapes {
		fits := true
		for name, request := range requests {
			if request.IsZero() {
				continue
			}
			// Pod slots are not an instance-shape dimension: a single pod
			// occupies exactly one slot on any real member, and launching
			// claims often omit pods from status.capacity — failing on it
			// would degrade every scale-from-zero pool to unknown.
			if name == corev1.ResourcePods {
				continue
			}
			// A resource absent from the shape (a GPU request against a
			// GPU-less member) cannot prove the fit any more than an
			// undersized one can.
			capacity, ok := shape[name]
			if !ok || request.Cmp(capacity) > 0 {
				fits = false
				break
			}
		}
		if fits {
			return nil
		}
	}
	var resources []string
	for name := range requests {
		resources = append(resources, string(name))
	}
	sort.Strings(resources)
	return []string{"instanceShape." + strings.Join(resources, ".")}
}

const observedShapeLimit = 64

// ObservedMemberShapesByPool collects, per pool, the distinct SCHEDULABLE
// vectors of observed members. Every recorded shape is affirmative evidence —
// a pod that fits one is spared the instanceShape unknown — so only genuinely
// schedulable readings may enter:
//
//   - Nodes contribute status.allocatable, the live schedulable truth.
//   - Claims contribute ONLY status.allocatable. A claim carrying just
//     status.capacity is skipped entirely: capacity is the raw machine, and
//     admitting it would let a pod that fits the machine but not the node clear
//     the shape check.
//   - A claim already bound to an observed node is skipped — the node's own
//     allocatable is in hand, and a claim's reading must never outweigh it.
//
// Every exclusion moves the verdict toward unknown, never toward compatible.
// Bounded per pool for the same reason: a truncated shape set can only make the
// evaluation more conservative.
func ObservedMemberShapesByPool(nodes []*corev1.Node, claims []*unstructured.Unstructured) map[string][]corev1.ResourceList {
	shapes := map[string][]corev1.ResourceList{}
	seen := map[string]map[string]bool{}
	observedNodes := make(map[string]bool, len(nodes))
	record := func(pool string, resources corev1.ResourceList) {
		if pool == "" || len(resources) == 0 || len(shapes[pool]) >= observedShapeLimit {
			return
		}
		key := shapeKey(resources)
		if seen[pool] == nil {
			seen[pool] = map[string]bool{}
		}
		if seen[pool][key] {
			return
		}
		seen[pool][key] = true
		copied := corev1.ResourceList{}
		for name, quantity := range resources {
			copied[name] = quantity.DeepCopy()
		}
		shapes[pool] = append(shapes[pool], copied)
	}
	for _, node := range nodes {
		if node == nil {
			continue
		}
		observedNodes[node.Name] = true
		record(node.Labels[karpenter.NodePoolLabelKey], node.Status.Allocatable)
	}
	for _, claim := range claims {
		if claim == nil {
			continue
		}
		if nodeName := karpenter.ClaimNodeName(claim); nodeName != "" && observedNodes[nodeName] {
			continue
		}
		allocatable := karpenter.NodeClaimAllocatable(claim)
		if len(allocatable) == 0 {
			continue
		}
		record(claim.GetLabels()[karpenter.NodePoolLabelKey], allocatable)
	}
	return shapes
}

func shapeKey(resources corev1.ResourceList) string {
	names := make([]string, 0, len(resources))
	for name := range resources {
		names = append(names, string(name))
	}
	sort.Strings(names)
	var builder strings.Builder
	for _, name := range names {
		quantity := resources[corev1.ResourceName(name)]
		builder.WriteString(name)
		builder.WriteByte('=')
		builder.WriteString(quantity.String())
		builder.WriteByte(';')
	}
	return builder.String()
}

func ClassifyDemandGroupModels(models []DemandGroupModel, pools []DemandPoolInput) {
	if pools == nil {
		return
	}
	orderedPools := sortedDemandPoolInputs(pools)
	for index := range models {
		if models[index].Group.State != capacityapi.DemandAwaitingCapacity {
			continue
		}
		counts := capacityapi.PoolEvaluationCounts{}
		for _, pool := range orderedPools {
			switch evaluateDemandPool(models[index].scheduling, models[index].requests, pool).Result {
			case capacityapi.PoolDeclaredCompatible:
				counts.DeclaredCompatible++
			case capacityapi.PoolIncompatible:
				counts.Incompatible++
			default:
				counts.Unknown++
			}
		}
		models[index].Group.State = demandStateWithPoolEvaluations(models[index].Group.State, counts, len(orderedPools))
	}
}

func SummarizeDemandGroupModels(models []DemandGroupModel) capacityapi.DemandSummary {
	summary := capacityapi.NewDemandSummary()
	for _, model := range models {
		counts := summary.ByState[model.Group.State]
		counts.GroupCount++
		counts.PodCount += model.Group.PodCount
		summary.ByState[model.Group.State] = counts
		summary.Total.GroupCount++
		summary.Total.PodCount += model.Group.PodCount
	}
	return summary
}

func demandStateWithPoolEvaluations(state capacityapi.DemandState, counts capacityapi.PoolEvaluationCounts, poolCount int) capacityapi.DemandState {
	if state != capacityapi.DemandAwaitingCapacity {
		return state
	}
	if counts.DeclaredCompatible > 0 {
		return capacityapi.DemandAwaitingCapacity
	}
	if counts.Unknown > 0 {
		return capacityapi.DemandUnknown
	}
	if poolCount == 0 || counts.Incompatible == poolCount {
		return capacityapi.DemandBlocked
	}
	return capacityapi.DemandUnknown
}

func demandSchedulingModelForPod(pod *corev1.Pod, requests corev1.ResourceList) demandSchedulingModel {
	model := demandSchedulingModel{
		NodeSelector:  []demandRequirement{},
		AffinityTerms: []demandAffinityTerm{},
		Tolerations:   append([]corev1.Toleration{}, pod.Spec.Tolerations...),
		Unknown:       []string{},
		Signature: capacityapi.SchedulingSignature{
			Constraints: []capacityapi.SchedulingConstraint{},
			Tolerations: []capacityapi.Toleration{},
		},
	}

	selectorKeys := make([]string, 0, len(pod.Spec.NodeSelector))
	for key := range pod.Spec.NodeSelector {
		selectorKeys = append(selectorKeys, key)
	}
	sort.Strings(selectorKeys)
	for _, key := range selectorKeys {
		requirement := demandRequirement{
			Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{pod.Spec.NodeSelector[key]},
			SourcePath: "spec.nodeSelector." + key,
		}
		model.NodeSelector = append(model.NodeSelector, requirement)
		appendDemandConstraint(&model.Signature, "nodeSelector", requirement)
	}

	var canonicalAffinity *[]canonicalAffinityTerm
	if affinity := pod.Spec.Affinity; affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		model.HasRequiredAffinity = true
		rawTerms := affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
		model.RequiredEmpty = len(rawTerms) == 0
		for termIndex, rawTerm := range rawTerms {
			term := demandAffinityTerm{
				Requirements: []demandRequirement{},
				Unknown:      []string{},
				Empty:        len(rawTerm.MatchExpressions) == 0 && len(rawTerm.MatchFields) == 0,
			}
			for expressionIndex, expression := range rawTerm.MatchExpressions {
				requirement := demandRequirement{
					Key: expression.Key, Operator: expression.Operator, Values: sortedUniqueStrings(expression.Values),
					SourcePath: fmt.Sprintf("spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms[%d].matchExpressions[%d]", termIndex, expressionIndex),
				}
				term.Requirements = append(term.Requirements, requirement)
				appendDemandConstraint(&model.Signature, "requiredNodeAffinity", requirement)
			}
			term.Canonical.Requirements = canonicalRequirements(term.Requirements)
			for _, field := range rawTerm.MatchFields {
				term.Canonical.MatchFields = append(term.Canonical.MatchFields, canonicalRequirement{
					Key: field.Key, Operator: string(field.Operator), Values: sortedUniqueStrings(field.Values),
				})
			}
			if len(rawTerm.MatchFields) > 0 {
				term.Unknown = append(term.Unknown, "requiredNodeAffinity.matchFields")
			}
			term.Canonical.MatchFields = sortedCanonicalRequirements(term.Canonical.MatchFields)
			term.sortKey = stableDemandFingerprint(term.Canonical)
			model.AffinityTerms = append(model.AffinityTerms, term)
		}
		sort.Slice(model.AffinityTerms, func(i, j int) bool {
			return model.AffinityTerms[i].sortKey < model.AffinityTerms[j].sortKey
		})
		terms := make([]canonicalAffinityTerm, 0, len(model.AffinityTerms))
		for _, term := range model.AffinityTerms {
			terms = append(terms, term.Canonical)
		}
		canonicalAffinity = &terms
	}

	keyedTolerations := make([]struct {
		value corev1.Toleration
		key   string
	}, 0, len(model.Tolerations))
	for _, toleration := range model.Tolerations {
		keyedTolerations = append(keyedTolerations, struct {
			value corev1.Toleration
			key   string
		}{value: toleration, key: canonicalTolerationKey(toleration)})
	}
	sort.Slice(keyedTolerations, func(i, j int) bool { return keyedTolerations[i].key < keyedTolerations[j].key })
	model.Tolerations = model.Tolerations[:0]
	for _, toleration := range keyedTolerations {
		model.Tolerations = append(model.Tolerations, toleration.value)
	}
	canonicalTolerations := make([]canonicalToleration, 0, len(model.Tolerations))
	for _, toleration := range model.Tolerations {
		canonical := canonicalTolerationFor(toleration)
		canonicalTolerations = append(canonicalTolerations, canonical)
		model.Signature.TolerationsMeta.Total++
		if len(model.Signature.Tolerations) < defaultDemandTolerationLimit {
			model.Signature.Tolerations = append(model.Signature.Tolerations, capacityapi.Toleration{
				Key: toleration.Key, Operator: string(toleration.Operator), Value: toleration.Value,
				Effect: string(toleration.Effect), TolerationSeconds: toleration.TolerationSeconds,
			})
		}
		if toleration.Operator != "" && toleration.Operator != corev1.TolerationOpEqual && toleration.Operator != corev1.TolerationOpExists {
			model.Unknown = append(model.Unknown, "toleration.operator")
		}
	}

	opaque := demandOpaqueConstraints(pod, requests, &model.Unknown)
	model.Unknown = sortedUniqueStrings(model.Unknown)
	canonical := canonicalSchedulingModel{
		NodeSelector: canonicalRequirements(model.NodeSelector), RequiredAffinity: canonicalAffinity,
		Tolerations: canonicalTolerations, Opaque: opaque,
	}
	model.Signature.Fingerprint = "sha256:" + stableDemandFingerprint(canonical)
	model.Signature.ConstraintsMeta.Returned = len(model.Signature.Constraints)
	model.Signature.ConstraintsMeta.Truncated = model.Signature.ConstraintsMeta.Returned < model.Signature.ConstraintsMeta.Total
	model.Signature.TolerationsMeta.Returned = len(model.Signature.Tolerations)
	model.Signature.TolerationsMeta.Truncated = model.Signature.TolerationsMeta.Returned < model.Signature.TolerationsMeta.Total
	return model
}

func sortedDemandPoolInputs(inputs []DemandPoolInput) []DemandPoolInput {
	ordered := make([]DemandPoolInput, 0, len(inputs))
	for _, input := range inputs {
		if input.NodePool != nil && input.NodePool.GetName() != "" {
			ordered = append(ordered, input)
		}
	}
	sort.Slice(ordered, func(i, j int) bool { return demandPoolKey(ordered[i].NodePool) < demandPoolKey(ordered[j].NodePool) })
	return ordered
}

func evaluateDemandPools(model demandSchedulingModel, requests corev1.ResourceList, ordered []DemandPoolInput, evaluationLimit int) ([]capacityapi.PoolEvaluation, capacityapi.BoundedResultMeta, capacityapi.PoolEvaluationCounts) {
	if evaluationLimit == 0 {
		evaluationLimit = DefaultDemandPoolEvaluationLimit
	}
	evaluations := make([]capacityapi.PoolEvaluation, 0, len(ordered))
	counts := capacityapi.PoolEvaluationCounts{}
	for _, input := range ordered {
		evaluation := evaluateDemandPool(model, requests, input)
		switch evaluation.Result {
		case capacityapi.PoolDeclaredCompatible:
			counts.DeclaredCompatible++
		case capacityapi.PoolIncompatible:
			counts.Incompatible++
		default:
			counts.Unknown++
		}
		evaluations = append(evaluations, evaluation)
	}
	capacity := len(evaluations)
	if evaluationLimit > 0 && capacity > evaluationLimit {
		capacity = evaluationLimit
	}
	// Bounding must keep the most decision-relevant evaluations: dropping a
	// declared-compatible pool while the counts still report one would leave
	// "which pool could take this?" unanswerable.
	result := make([]capacityapi.PoolEvaluation, 0, capacity)
	for rank := range 3 {
		for _, evaluation := range evaluations {
			if evaluationLimit >= 0 && len(result) >= evaluationLimit {
				break
			}
			if poolEvaluationBoundingRank(evaluation.Result) != rank {
				continue
			}
			result = append(result, evaluation)
		}
	}
	meta := capacityapi.BoundedResultMeta{Total: len(ordered), Returned: len(result), Truncated: len(result) < len(ordered)}
	return result, meta, counts
}

func poolEvaluationBoundingRank(result capacityapi.PoolEvaluationResult) int {
	switch result {
	case capacityapi.PoolDeclaredCompatible:
		return 0
	case capacityapi.PoolIncompatible:
		return 2
	default:
		return 1
	}
}

func evaluateDemandPool(model demandSchedulingModel, requests corev1.ResourceList, input DemandPoolInput) capacityapi.PoolEvaluation {
	pool := input.NodePool
	evaluation := capacityapi.NewPoolEvaluation()
	evaluation.Pool = subject.Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: pool.GetName()}
	unknown := append([]string{}, model.Unknown...)
	unknown = append(unknown, input.UnknownPredicates...)
	evidence := demandEvidenceCollection{values: []capacityapi.EvaluationEvidence{}}

	spec := karpenter.NormalizeNodePoolSpec(pool)
	switch karpenter.NodePoolReadiness(pool) {
	case karpenter.ReadinessNotReady:
		evidence.add(
			"nodePoolReady", "status.conditions[type=Ready]", []string{"not_ready"}, []string{"ready"},
			"The NodePool reports that it is not ready.",
		)
	case karpenter.ReadinessUnknown:
		unknown = append(unknown, "nodePool.readiness")
	}
	if spec.NodeClassRef != nil {
		switch {
		case input.NodeClassReady == nil:
			unknown = append(unknown, "nodeClass.readiness")
		case !*input.NodeClassReady:
			evidence.add(
				"nodeClassReady", "nodeClass.status.conditions[type=Ready]", []string{"not_ready"}, []string{"ready"},
				"The NodeClass reports that it is not ready.",
			)
		}
	}

	for _, taint := range sortedTaints(spec.Taints) {
		switch taint.Effect {
		case corev1.TaintEffectPreferNoSchedule:
			continue
		case corev1.TaintEffectNoSchedule, corev1.TaintEffectNoExecute:
			if !tolerationsTolerateTaint(model.Tolerations, taint) {
				// A toleration operator we cannot evaluate makes the miss
				// unprovable — but only for taints that toleration could bind
				// to. An exotic operator pinned to a different key cannot
				// tolerate this taint, so the miss stays proven.
				if unevaluableTolerationCouldMatch(model.Tolerations, taint) {
					unknown = append(unknown, "nodePool.taints")
					continue
				}
				evidence.add(
					"permanentTaint", "spec.template.spec.taints", []string{formatTaint(taint)}, []string{"matching toleration"},
					"The Pod does not tolerate a permanent NodePool taint.",
				)
			}
		default:
			unknown = append(unknown, "nodePool.taints")
		}
	}

	selectorEvidence, selectorUnknown := evaluateDemandSelectors(model, spec, pool.GetName())
	evidence.merge(selectorEvidence)
	unknown = append(unknown, selectorUnknown...)

	limitEvidence, limitUnknown := evaluateDemandLimits(pool, spec, requests, input.ProvisionedKnown)
	evidence.merge(limitEvidence)
	unknown = append(unknown, limitUnknown...)
	unknown = append(unknown, instanceShapeUnknowns(requests, input.ObservedMemberShapes)...)

	if spec.Replicas != nil {
		if *spec.Replicas == 0 {
			evidence.add(
				"staticCapacity", "spec.replicas", []string{"0"}, []string{"available static capacity"},
				"The static NodePool has zero configured replicas.",
			)
		} else {
			// Headroom on a provisioned static fleet cannot be proven from
			// declarations alone, so a non-zero static pool stays unknown.
			unknown = append(unknown, "staticCapacity")
		}
	}

	evaluation.Evidence = evidence.values
	evaluation.UnknownPredicates = sortedUniqueStrings(unknown)
	switch {
	case evidence.total > 0:
		evaluation.Result = capacityapi.PoolIncompatible
	case len(evaluation.UnknownPredicates) > 0:
		evaluation.Result = capacityapi.PoolEvaluationUnknown
	default:
		evaluation.Result = capacityapi.PoolDeclaredCompatible
	}
	evaluation.EvidenceMeta = capacityapi.BoundedResultMeta{
		Total: evidence.total, Returned: len(evaluation.Evidence), Truncated: len(evaluation.Evidence) < evidence.total,
	}
	evaluation.UnknownPredicatesMeta.Total = len(evaluation.UnknownPredicates)
	if len(evaluation.UnknownPredicates) > defaultDemandUnknownLimit {
		evaluation.UnknownPredicates = evaluation.UnknownPredicates[:defaultDemandUnknownLimit]
		evaluation.UnknownPredicatesMeta.Truncated = true
	}
	evaluation.UnknownPredicatesMeta.Returned = len(evaluation.UnknownPredicates)
	return evaluation
}

func evaluateDemandSelectors(model demandSchedulingModel, spec karpenter.NodePoolSpec, poolName string) (demandEvidenceCollection, []string) {
	poolRequirements := map[string][]demandRequirement{}
	fixedLabels := map[string]bool{}
	poolRequirements[karpenter.NodePoolLabelKey] = append(poolRequirements[karpenter.NodePoolLabelKey], demandRequirement{
		Key: karpenter.NodePoolLabelKey, Operator: corev1.NodeSelectorOpIn, Values: []string{poolName}, SourcePath: "metadata.name",
	})
	fixedLabels[karpenter.NodePoolLabelKey] = true
	for key, value := range spec.TemplateLabels {
		poolRequirements[key] = append(poolRequirements[key], demandRequirement{
			Key: key, Operator: corev1.NodeSelectorOpIn, Values: []string{value}, SourcePath: "spec.template.metadata.labels." + key,
		})
		fixedLabels[key] = true
	}
	for index, requirement := range spec.Requirements {
		poolRequirements[requirement.Key] = append(poolRequirements[requirement.Key], demandRequirement{
			Key: requirement.Key, Operator: requirement.Operator, Values: sortedUniqueStrings(requirement.Values),
			SourcePath: fmt.Sprintf("spec.template.spec.requirements[%d]", index),
		})
	}

	podKeys := map[string]bool{}
	for _, requirement := range model.NodeSelector {
		podKeys[requirement.Key] = true
	}
	for _, term := range model.AffinityTerms {
		for _, requirement := range term.Requirements {
			podKeys[requirement.Key] = true
		}
	}
	requirementCountByKey := map[string]int{}
	for _, requirement := range spec.Requirements {
		requirementCountByKey[requirement.Key]++
	}
	baseUnknown := []string{}
	for _, requirement := range spec.Requirements {
		if requirement.MinValues == nil {
			continue
		}
		// When the pod imposes nothing on this key and the pool's sole
		// requirement is an In set that already meets its own minValues, the
		// merged value set is exactly that declaration — no uncertainty to
		// report. Anything narrower (pod constrains the key, multiple pool
		// requirements on it, non-In operators, or an In set smaller than
		// minValues) keeps the honest unknown: cardinality of a merged set is
		// where overclaims live.
		if !podKeys[requirement.Key] && requirementCountByKey[requirement.Key] == 1 &&
			requirement.Operator == corev1.NodeSelectorOpIn &&
			int64(len(sortedUniqueStrings(requirement.Values))) >= *requirement.MinValues {
			continue
		}
		baseUnknown = append(baseUnknown, "nodePool.requirements."+requirement.Key+".minValues")
	}
	if model.RequiredEmpty {
		evidence := demandEvidenceCollection{}
		evidence.add(
			"requiredNodeAffinity", "spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms",
			[]string{"no terms"}, []string{"at least one satisfiable term"}, "Required node affinity contains no schedulable term.",
		)
		return evidence, baseUnknown
	}

	terms := model.AffinityTerms
	if !model.HasRequiredAffinity {
		terms = []demandAffinityTerm{{Requirements: []demandRequirement{}, Unknown: []string{}}}
	}
	compatibleTerm := false
	unknownTerm := false
	allEvidence := demandEvidenceCollection{}
	allUnknown := append([]string{}, baseUnknown...)
	for _, term := range terms {
		podRequirements := append([]demandRequirement{}, model.NodeSelector...)
		podRequirements = append(podRequirements, term.Requirements...)
		termEvidence, termUnknown := evaluateDemandSelectorTerm(podRequirements, poolRequirements, fixedLabels)
		termUnknown = append(termUnknown, term.Unknown...)
		if term.Empty {
			termEvidence.add(
				"requiredNodeAffinity",
				"spec.affinity.nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution.nodeSelectorTerms",
				[]string{"empty term"},
				[]string{"at least one requirement"},
				"An empty required node-affinity term does not match any Node.",
			)
		}
		switch {
		case termEvidence.total > 0:
			allEvidence.merge(termEvidence)
		case len(termUnknown) > 0:
			unknownTerm = true
			allUnknown = append(allUnknown, termUnknown...)
		default:
			compatibleTerm = true
		}
	}
	if compatibleTerm {
		return demandEvidenceCollection{}, baseUnknown
	}
	if unknownTerm {
		return demandEvidenceCollection{}, allUnknown
	}
	return allEvidence, baseUnknown
}

func evaluateDemandSelectorTerm(podRequirements []demandRequirement, poolRequirements map[string][]demandRequirement, fixedLabels map[string]bool) (demandEvidenceCollection, []string) {
	byKey := map[string][]demandRequirement{}
	for _, requirement := range podRequirements {
		byKey[requirement.Key] = append(byKey[requirement.Key], requirement)
	}
	keys := make([]string, 0, len(byKey))
	for key := range byKey {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	evidence := demandEvidenceCollection{}
	unknown := []string{}
	for _, key := range keys {
		podForKey := byKey[key]
		poolForKey := poolRequirements[key]
		combined := append(append([]demandRequirement{}, podForKey...), poolForKey...)
		feasible, supported := demandRequirementsFeasible(combined)
		if !supported {
			unknown = append(unknown, "nodeSelector.operator."+key)
			continue
		}
		if !feasible {
			evidence.add(
				"selectorRequirement", firstRequirementPath(podForKey), formatRequirements(podForKey), formatRequirements(poolForKey),
				"The Pod's required node labels contradict the NodePool's declared labels or requirements.",
			)
			continue
		}
		// kubernetes.io/hostname names one specific node — it is not an
		// offering dimension. Every provisioned node carries a fresh, unique
		// hostname, so pinning to known hostnames can never be satisfied by
		// new capacity, while Exists/NotIn are satisfied by any new node.
		if key == corev1.LabelHostname {
			pinned := false
			absentRequired := false
			for _, requirement := range podForKey {
				switch requirement.Operator {
				case corev1.NodeSelectorOpIn:
					pinned = true
				case corev1.NodeSelectorOpDoesNotExist:
					absentRequired = true
				}
			}
			switch {
			case pinned:
				evidence.add(
					"selectorRequirement", firstRequirementPath(podForKey), formatRequirements(podForKey),
					[]string{"a new, unique hostname per provisioned node"},
					"The Pod is pinned to specific node hostnames — every provisioned node carries a new, unique hostname, so no NodePool can satisfy this with new capacity.",
				)
			case absentRequired:
				evidence.add(
					"selectorRequirement", firstRequirementPath(podForKey), formatRequirements(podForKey),
					[]string{"kubernetes.io/hostname present on every node"},
					"Every node carries the kubernetes.io/hostname label, so requiring it to be absent cannot be satisfied by any node.",
				)
			}
			continue
		}
		if requirementNeedsPresentLabel(podForKey) && !requirementNeedsPresentLabel(poolForKey) {
			// Provider/well-known labels can appear on nodes without the pool
			// declaring them (the offering catalogue supplies them) — honest
			// unknown. An arbitrary label the pool never declares is different:
			// Karpenter only applies labels from pool requirements/template
			// labels, so it will never provision a node carrying it. This is
			// the classic misconfiguration and must read incompatible.
			if isProviderOfferingLabel(key) {
				unknown = append(unknown, "providerOfferings."+key)
				continue
			}
			evidence.add(
				"selectorRequirement", firstRequirementPath(podForKey), formatRequirements(podForKey),
				[]string{"NodePool declares label " + key},
				"The Pod requires a node label this NodePool never declares — Karpenter only applies labels from the pool's requirements or template labels.",
			)
			continue
		}
		// A pool In requirement bounds the label's values, and feasibility
		// already proved the pod's need intersects that declared set — the
		// declaration itself answers compatibility, no offering catalogue
		// needed. Without a value bound (undeclared, Exists, NotIn) the actual
		// value comes from the provider, so it stays an honest unknown.
		if isProviderOfferingLabel(key) && !fixedLabels[key] && !requirementBoundsValues(poolForKey) {
			unknown = append(unknown, "providerOfferings."+key)
		}
	}
	return evidence, unknown
}

func requirementBoundsValues(requirements []demandRequirement) bool {
	for _, requirement := range requirements {
		if requirement.Operator == corev1.NodeSelectorOpIn && len(requirement.Values) > 0 {
			return true
		}
	}
	return false
}

func demandRequirementsFeasible(requirements []demandRequirement) (bool, bool) {
	absentAllowed := true
	presentAllowed := true
	var finite map[string]struct{}
	excluded := map[string]struct{}{}
	var greaterThan *int64
	var lessThan *int64

	for _, requirement := range requirements {
		values := sortedUniqueStrings(requirement.Values)
		switch requirement.Operator {
		case corev1.NodeSelectorOpIn:
			if len(values) == 0 {
				return false, false
			}
			absentAllowed = false
			set := stringSet(values)
			if finite == nil {
				finite = set
			} else {
				finite = intersectStringSets(finite, set)
			}
		case corev1.NodeSelectorOpNotIn:
			if len(values) == 0 {
				return false, false
			}
			for _, value := range values {
				excluded[value] = struct{}{}
			}
		case corev1.NodeSelectorOpExists:
			if len(values) != 0 {
				return false, false
			}
			absentAllowed = false
		case corev1.NodeSelectorOpDoesNotExist:
			if len(values) != 0 {
				return false, false
			}
			presentAllowed = false
		case corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
			if len(values) != 1 {
				return false, false
			}
			value, err := strconv.ParseInt(values[0], 10, 64)
			if err != nil {
				return false, false
			}
			absentAllowed = false
			if requirement.Operator == corev1.NodeSelectorOpGt && (greaterThan == nil || value > *greaterThan) {
				greaterThan = &value
			}
			if requirement.Operator == corev1.NodeSelectorOpLt && (lessThan == nil || value < *lessThan) {
				lessThan = &value
			}
		default:
			return false, false
		}
	}
	if absentAllowed {
		return true, true
	}
	if !presentAllowed {
		return false, true
	}
	if finite != nil {
		for value := range finite {
			if _, blocked := excluded[value]; blocked {
				continue
			}
			if demandNumericValueAllowed(value, greaterThan, lessThan) {
				return true, true
			}
		}
		return false, true
	}
	if len(excluded) > 0 && (greaterThan != nil || lessThan != nil) {
		return false, false
	}
	if greaterThan != nil && *greaterThan == math.MaxInt64 {
		return false, true
	}
	if lessThan != nil && *lessThan == math.MinInt64 {
		return false, true
	}
	if greaterThan != nil && lessThan != nil && *greaterThan+1 >= *lessThan {
		return false, true
	}
	return true, true
}

func evaluateDemandLimits(pool *unstructured.Unstructured, spec karpenter.NodePoolSpec, requests corev1.ResourceList, provisionedKnown bool) (demandEvidenceCollection, []string) {
	if len(spec.Limits) == 0 {
		return demandEvidenceCollection{}, nil
	}
	if !provisionedKnown {
		return demandEvidenceCollection{}, []string{"nodePool.limits"}
	}
	provisioned := karpenter.NodePoolStatusResources(pool)
	names := make([]string, 0, len(spec.Limits))
	for name := range spec.Limits {
		names = append(names, string(name))
	}
	sort.Strings(names)
	evidence := demandEvidenceCollection{}
	unknown := []string{}
	for _, resourceName := range names {
		name := corev1.ResourceName(resourceName)
		limit := spec.Limits[name]
		current := provisioned[name]
		requested := requests[name]
		withRequest := current.DeepCopy()
		withRequest.Add(requested)
		if requested.Sign() > 0 && withRequest.Cmp(limit) > 0 {
			evidence.add(
				"configuredLimit", "spec.limits."+resourceName,
				[]string{"provisioned=" + current.String(), "perPodRequest=" + requested.String()}, []string{"limit=" + limit.String()},
				"The NodePool's configured limit has no room for the Pod's minimum requested resource.",
			)
		} else if requested.Sign() == 0 && current.Cmp(limit) >= 0 {
			unknown = append(unknown, "nodePool.limits."+resourceName+".capacityShape")
		}
	}
	return evidence, unknown
}

func demandOpaqueConstraints(pod *corev1.Pod, requests corev1.ResourceList, unknown *[]string) []string {
	opaque := []string{}
	if affinity := pod.Spec.Affinity; affinity != nil {
		if affinity.PodAffinity != nil && len(affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
			*unknown = append(*unknown, "interPodAffinity")
			opaque = append(opaque, stableOpaqueConstraint("podAffinity", affinity.PodAffinity.RequiredDuringSchedulingIgnoredDuringExecution))
		}
		if affinity.PodAntiAffinity != nil && len(affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution) > 0 {
			*unknown = append(*unknown, "interPodAntiAffinity")
			opaque = append(opaque, stableOpaqueConstraint("podAntiAffinity", affinity.PodAntiAffinity.RequiredDuringSchedulingIgnoredDuringExecution))
		}
	}
	for _, constraint := range pod.Spec.TopologySpreadConstraints {
		if constraint.WhenUnsatisfiable == corev1.DoNotSchedule {
			*unknown = append(*unknown, "topologySpreadConstraints")
			opaque = append(opaque, stableOpaqueConstraint("topologySpread", constraint))
		}
	}
	for _, volume := range pod.Spec.Volumes {
		if volume.PersistentVolumeClaim != nil {
			*unknown = append(*unknown, "persistentVolumeTopology")
			opaque = append(opaque, "persistentVolumeClaim:"+volume.PersistentVolumeClaim.ClaimName)
		}
		if volume.Ephemeral != nil {
			*unknown = append(*unknown, "persistentVolumeTopology")
			opaque = append(opaque, stableOpaqueConstraint("ephemeralVolume", volume.Ephemeral))
		}
	}
	for _, container := range append(append([]corev1.Container{}, pod.Spec.InitContainers...), pod.Spec.Containers...) {
		for _, port := range container.Ports {
			if port.HostPort > 0 {
				*unknown = append(*unknown, "hostPort")
				opaque = append(opaque, fmt.Sprintf("hostPort:%s:%s:%d", port.HostIP, port.Protocol, port.HostPort))
			}
		}
	}
	for name := range requests {
		if strings.Contains(string(name), "/") {
			*unknown = append(*unknown, "providerResource."+string(name))
			opaque = append(opaque, "providerResource:"+string(name))
		}
	}
	if pod.Spec.RuntimeClassName != nil {
		*unknown = append(*unknown, "runtimeClass")
		opaque = append(opaque, "runtimeClass:"+*pod.Spec.RuntimeClassName)
	}
	if pod.Spec.SchedulerName != "" && pod.Spec.SchedulerName != corev1.DefaultSchedulerName {
		*unknown = append(*unknown, "schedulerName")
		opaque = append(opaque, "schedulerName:"+pod.Spec.SchedulerName)
	}
	if len(pod.Spec.SchedulingGates) > 0 {
		opaque = append(opaque, stableOpaqueConstraint("schedulingGates", pod.Spec.SchedulingGates))
	}
	if len(pod.Spec.ResourceClaims) > 0 {
		*unknown = append(*unknown, "dynamicResourceAllocation")
		opaque = append(opaque, stableOpaqueConstraint("resourceClaims", pod.Spec.ResourceClaims))
	}
	return sortedUniqueStrings(opaque)
}

func populateDemandSchedulerState(group *capacityapi.DemandGroup, pods []*corev1.Pod, observedAt time.Time) {
	type reasonKey struct {
		source        string
		code          string
		messageDigest [sha256.Size]byte
		messageLength int
	}
	reasons := map[reasonKey]*demandSchedulerReasonAggregate{}
	retained := make([]*demandSchedulerReasonAggregate, 0, defaultDemandSchedulerReasonLimit)
	addReason := func(source, code, message string, firstSeen time.Time) {
		key := reasonKey{source: source, code: code, messageDigest: sha256.Sum256([]byte(message)), messageLength: len(message)}
		reason := reasons[key]
		if reason == nil {
			reason = &demandSchedulerReasonAggregate{source: source, code: code, firstSeen: firstSeen}
			reasons[key] = reason
			if len(retained) < defaultDemandSchedulerReasonLimit {
				reason.message = message
				retained = append(retained, reason)
			} else {
				worst := 0
				for index := 1; index < len(retained); index++ {
					if demandSchedulerReasonLess(retained[worst].code, retained[worst].message, retained[index].code, retained[index].message) {
						worst = index
					}
				}
				if demandSchedulerReasonLess(code, message, retained[worst].code, retained[worst].message) {
					retained[worst].message = ""
					reason.message = message
					retained[worst] = reason
				}
			}
		}
		reason.count++
		if firstSeen.Before(reason.firstSeen) {
			reason.firstSeen = firstSeen
		}
	}

	sawHeld := false
	sawAwaitingCapacity := false
	sawBlocked := false
	sawUnknown := false
	for _, pod := range pods {
		if len(pod.Spec.SchedulingGates) > 0 {
			gateNames := make([]string, 0, len(pod.Spec.SchedulingGates))
			for _, gate := range pod.Spec.SchedulingGates {
				gateNames = append(gateNames, gate.Name)
			}
			sort.Strings(gateNames)
			sawHeld = true
			addReason("pod.spec.schedulingGates", corev1.PodReasonSchedulingGated, strings.Join(gateNames, ", "), observedAt)
			continue
		}
		for _, condition := range pod.Status.Conditions {
			if condition.Type != corev1.PodScheduled {
				continue
			}
			firstSeen := condition.LastTransitionTime.Time
			if firstSeen.IsZero() {
				firstSeen = pod.CreationTimestamp.Time
			}
			if firstSeen.IsZero() {
				firstSeen = observedAt
			}

			switch {
			case condition.Status != corev1.ConditionFalse:
				sawUnknown = true
			case condition.Reason == corev1.PodReasonSchedulingGated:
				sawHeld = true
				addReason("pod.status.conditions.PodScheduled", condition.Reason, condition.Message, firstSeen)
			case condition.Reason != corev1.PodReasonUnschedulable:
				sawUnknown = true
				code := condition.Reason
				if code == "" {
					code = "PodScheduledFalse"
				}
				addReason("pod.status.conditions.PodScheduled", code, condition.Message, firstSeen)
			default:
				_, parsed := scheduling.ParseMessage(condition.Message)
				if len(parsed) == 0 {
					sawUnknown = true
					addReason("pod.status.conditions.PodScheduled", condition.Reason, condition.Message, firstSeen)
					continue
				}
				switch demandStateForSchedulingReasons(parsed) {
				case capacityapi.DemandAwaitingCapacity:
					sawAwaitingCapacity = true
				case capacityapi.DemandBlocked:
					sawBlocked = true
				default:
					sawUnknown = true
				}
				for _, parsedReason := range parsed {
					addReason("pod.status.conditions.PodScheduled", string(parsedReason.Class), parsedReason.Raw, firstSeen)
				}
			}
		}
	}
	switch {
	case sawBlocked:
		group.State = capacityapi.DemandBlocked
	case sawHeld:
		group.State = capacityapi.DemandHeld
	case sawUnknown:
		group.State = capacityapi.DemandUnknown
	case sawAwaitingCapacity:
		group.State = capacityapi.DemandAwaitingCapacity
	default:
		group.State = capacityapi.DemandWaitingForScheduler
	}
	group.SchedulerReasons = make([]capacityapi.SchedulerReason, 0, len(retained))
	for _, reason := range retained {
		group.SchedulerReasons = append(group.SchedulerReasons, capacityapi.SchedulerReason{
			Code: reason.code, Source: reason.source, Message: reason.message,
			Count: reason.count, FirstSeen: reason.firstSeen, LastSeen: observedAt,
		})
	}
	sort.Slice(group.SchedulerReasons, func(i, j int) bool {
		left := group.SchedulerReasons[i]
		right := group.SchedulerReasons[j]
		if left.Code != right.Code {
			return left.Code < right.Code
		}
		if left.Source != right.Source {
			return left.Source < right.Source
		}
		return left.Message < right.Message
	})
	group.SchedulerReasonsMeta.Total = len(reasons)
	group.SchedulerReasonsMeta.Returned = len(group.SchedulerReasons)
	group.SchedulerReasonsMeta.Truncated = group.SchedulerReasonsMeta.Returned < group.SchedulerReasonsMeta.Total
}

func demandSchedulerReasonLess(leftCode, leftMessage, rightCode, rightMessage string) bool {
	if leftCode != rightCode {
		return leftCode < rightCode
	}
	return leftMessage < rightMessage
}

func demandStateForSchedulingReasons(reasons []scheduling.Reason) capacityapi.DemandState {
	hasCapacityReason := false
	hasExternalBlocker := false
	hasUnknown := false
	for _, reason := range reasons {
		switch reason.Class {
		case scheduling.InsufficientResource,
			scheduling.UntoleratedTaint,
			scheduling.NodeAffinitySelector,
			scheduling.VolumeCount,
			scheduling.NoPorts,
			scheduling.NodeUnschedulable:
			hasCapacityReason = true
		case scheduling.VolumeBinding:
			hasExternalBlocker = true
		case scheduling.Other, "":
			hasUnknown = true
		default:
			hasUnknown = true
		}
	}
	switch {
	case hasExternalBlocker:
		return capacityapi.DemandBlocked
	case hasUnknown:
		return capacityapi.DemandUnknown
	case hasCapacityReason:
		return capacityapi.DemandAwaitingCapacity
	default:
		return capacityapi.DemandUnknown
	}
}

func capacityConstraint(predicate string, requirement demandRequirement) capacityapi.SchedulingConstraint {
	return capacityapi.SchedulingConstraint{
		Predicate: predicate, Key: requirement.Key, Operator: string(requirement.Operator),
		Values: append([]string{}, requirement.Values...), SourcePath: requirement.SourcePath,
	}
}

func appendDemandConstraint(signature *capacityapi.SchedulingSignature, predicate string, requirement demandRequirement) {
	signature.ConstraintsMeta.Total++
	if len(signature.Constraints) < defaultDemandConstraintLimit {
		signature.Constraints = append(signature.Constraints, capacityConstraint(predicate, requirement))
	}
}

func canonicalRequirements(requirements []demandRequirement) []canonicalRequirement {
	result := make([]canonicalRequirement, 0, len(requirements))
	for _, requirement := range requirements {
		result = append(result, canonicalRequirement{
			Key: requirement.Key, Operator: string(requirement.Operator), Values: sortedUniqueStrings(requirement.Values),
		})
	}
	return sortedCanonicalRequirements(result)
}

func sortedCanonicalRequirements(requirements []canonicalRequirement) []canonicalRequirement {
	sort.Slice(requirements, func(i, j int) bool {
		left := requirements[i].Key + "\x00" + requirements[i].Operator + "\x00" + strings.Join(requirements[i].Values, "\x00")
		right := requirements[j].Key + "\x00" + requirements[j].Operator + "\x00" + strings.Join(requirements[j].Values, "\x00")
		return left < right
	})
	return requirements
}

func canonicalTolerationFor(toleration corev1.Toleration) canonicalToleration {
	// The scheduler treats an omitted operator as Equal; fingerprint the
	// semantic form so implicit and explicit Equal coalesce into one group.
	operator := toleration.Operator
	if operator == "" {
		operator = corev1.TolerationOpEqual
	}
	return canonicalToleration{
		Key: toleration.Key, Operator: string(operator), Value: toleration.Value,
		Effect: string(toleration.Effect), TolerationSeconds: toleration.TolerationSeconds,
	}
}

func canonicalTolerationKey(toleration corev1.Toleration) string {
	return stableDemandFingerprint(canonicalTolerationFor(toleration))
}

func stableOpaqueConstraint(name string, value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return name
	}
	sum := sha256.Sum256(payload)
	return name + ":sha256:" + hex.EncodeToString(sum[:])
}

func stableDemandFingerprint(value any) string {
	payload, err := json.Marshal(value)
	if err != nil {
		return ""
	}
	sum := sha256.Sum256(payload)
	return hex.EncodeToString(sum[:])
}

func DemandSnapshotFingerprint(models []DemandGroupModel, _ []DemandPoolInput) string {
	groupIDs := make([]string, 0, len(models))
	for _, model := range models {
		groupIDs = append(groupIDs, model.Group.ID)
	}
	sort.Strings(groupIDs)
	return stableDemandFingerprint(groupIDs)
}

func demandEvidence(predicate, sourcePath string, observed, expected []string, explanation string) capacityapi.EvaluationEvidence {
	return capacityapi.EvaluationEvidence{
		Predicate: predicate, SourcePath: sourcePath,
		ObservedValues: append([]string{}, observed...), ExpectedValues: append([]string{}, expected...),
		Confidence: capacityapi.ConfidenceHigh, Explanation: explanation,
	}
}

func requirementNeedsPresentLabel(requirements []demandRequirement) bool {
	for _, requirement := range requirements {
		switch requirement.Operator {
		case corev1.NodeSelectorOpIn, corev1.NodeSelectorOpExists, corev1.NodeSelectorOpGt, corev1.NodeSelectorOpLt:
			return true
		}
	}
	return false
}

func demandNumericValueAllowed(value string, greaterThan, lessThan *int64) bool {
	if greaterThan == nil && lessThan == nil {
		return true
	}
	numeric, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return false
	}
	return (greaterThan == nil || numeric > *greaterThan) && (lessThan == nil || numeric < *lessThan)
}

func isProviderOfferingLabel(key string) bool {
	return key == karpenter.CapacityTypeLabelKey ||
		strings.HasPrefix(key, "kubernetes.io/") ||
		strings.HasPrefix(key, "node.kubernetes.io/") ||
		strings.HasPrefix(key, "topology.kubernetes.io/") ||
		strings.HasPrefix(key, "karpenter.sh/") ||
		strings.HasPrefix(key, "karpenter.k8s.aws/") || strings.HasPrefix(key, "eks.amazonaws.com/") ||
		strings.HasPrefix(key, "karpenter.azure.com/") || strings.HasPrefix(key, "cloud.google.com/") ||
		strings.HasPrefix(key, "topology.gke.io/") || strings.HasPrefix(key, "topology.ebs.csi.aws.com/") ||
		strings.HasPrefix(key, "topology.disk.csi.azure.com/")
}

func unevaluableTolerationCouldMatch(tolerations []corev1.Toleration, taint corev1.Taint) bool {
	for _, toleration := range tolerations {
		switch toleration.Operator {
		case "", corev1.TolerationOpEqual, corev1.TolerationOpExists:
			continue
		}
		if toleration.Key != "" && toleration.Key != taint.Key {
			continue
		}
		if toleration.Effect != "" && toleration.Effect != taint.Effect {
			continue
		}
		return true
	}
	return false
}

func tolerationsTolerateTaint(tolerations []corev1.Toleration, taint corev1.Taint) bool {
	for i := range tolerations {
		if tolerations[i].ToleratesTaint(klog.Background(), &taint, false) {
			return true
		}
	}
	return false
}

func sortedTaints(taints []corev1.Taint) []corev1.Taint {
	result := append([]corev1.Taint{}, taints...)
	sort.Slice(result, func(i, j int) bool { return formatTaint(result[i]) < formatTaint(result[j]) })
	return result
}

func formatTaint(taint corev1.Taint) string {
	return taint.Key + "=" + taint.Value + ":" + string(taint.Effect)
}

func formatRequirements(requirements []demandRequirement) []string {
	result := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		result = append(result, requirement.Key+" "+string(requirement.Operator)+" ["+strings.Join(requirement.Values, ",")+"]")
	}
	sort.Strings(result)
	return result
}

func firstRequirementPath(requirements []demandRequirement) string {
	paths := make([]string, 0, len(requirements))
	for _, requirement := range requirements {
		paths = append(paths, requirement.SourcePath)
	}
	sort.Strings(paths)
	if len(paths) == 0 {
		return "spec"
	}
	return paths[0]
}

func stringSet(values []string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

func intersectStringSets(left, right map[string]struct{}) map[string]struct{} {
	result := map[string]struct{}{}
	for value := range left {
		if _, ok := right[value]; ok {
			result[value] = struct{}{}
		}
	}
	return result
}

func sortedUniqueStrings(values []string) []string {
	result := append([]string{}, values...)
	sort.Strings(result)
	if len(result) < 2 {
		return result
	}
	write := 1
	for read := 1; read < len(result); read++ {
		if result[read] != result[write-1] {
			result[write] = result[read]
			write++
		}
	}
	return result[:write]
}

func multiplyResourceList(resources corev1.ResourceList, multiplier int64) corev1.ResourceList {
	result := corev1.ResourceList{}
	for name, quantity := range resources {
		value := quantity.DeepCopy()
		value.Mul(multiplier)
		result[name] = value
	}
	return result
}

func copySubjectRef(ref subject.Ref) *subject.Ref {
	copy := ref
	return &copy
}

func identityForPod(pod *corev1.Pod) capacityapi.ResourceIdentity {
	return capacityapi.ResourceIdentity{
		Ref: subject.Ref{Kind: "Pod", Namespace: pod.Namespace, Name: pod.Name}, APIVersion: "v1", UID: string(pod.UID),
	}
}

func demandFirstSeen(pods []*corev1.Pod, fallback time.Time) time.Time {
	first := fallback
	for _, pod := range pods {
		created := pod.CreationTimestamp.Time
		if created.IsZero() {
			continue
		}
		if first.IsZero() || created.Before(first) {
			first = created
		}
	}
	return first
}

func demandPodKey(pod *corev1.Pod) string {
	return pod.Namespace + "\x00" + pod.Name + "\x00" + string(pod.UID)
}

func demandPoolKey(pool *unstructured.Unstructured) string {
	if pool == nil {
		return "\xff"
	}
	return pool.GetName() + "\x00" + string(pool.GetUID())
}
