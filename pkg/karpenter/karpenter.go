// Package karpenter normalizes Karpenter resources and their relationships.
package karpenter

import (
	"strconv"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apiMeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

const (
	Group                = "karpenter.sh"
	NodePoolKind         = "NodePool"
	NodeClaimKind        = "NodeClaim"
	NodePoolLabelKey     = "karpenter.sh/nodepool"
	CapacityTypeLabelKey = "karpenter.sh/capacity-type"
	APIVersionV1         = "karpenter.sh/v1"
	APIVersionV1Beta1    = "karpenter.sh/v1beta1"

	NodeRegistrationHealthyConditionType = "NodeRegistrationHealthy"
)

type NodeClassRef struct {
	Group      string
	Version    string
	Kind       string
	Name       string
	APIVersion string
}

type Readiness string

const (
	ReadinessReady    Readiness = "ready"
	ReadinessNotReady Readiness = "not_ready"
	ReadinessUnknown  Readiness = "unknown"
)

type Requirement struct {
	Key       string
	Operator  corev1.NodeSelectorOperator
	Values    []string
	MinValues *int64
}

type DisruptionBudget struct {
	Nodes    string
	Schedule string
	Duration string
	Reasons  []string
}

type Disruption struct {
	ConsolidationPolicy string
	ConsolidateAfter    string
	Budgets             []DisruptionBudget
}

type NodePoolSpec struct {
	Replicas               *int64
	Weight                 *int64
	Limits                 corev1.ResourceList
	TemplateLabels         map[string]string
	NodeClassRef           *NodeClassRef
	Requirements           []Requirement
	Taints                 []corev1.Taint
	StartupTaints          []corev1.Taint
	ExpireAfter            string
	TerminationGracePeriod string
	Disruption             Disruption
}

type RelationshipSource string

const (
	RelationshipNone     RelationshipSource = ""
	RelationshipOwnerRef RelationshipSource = "owner_ref"
	RelationshipLabel    RelationshipSource = "label"
)

func NodeClassRefForNodePool(pool *unstructured.Unstructured) (NodeClassRef, bool) {
	if pool == nil {
		return NodeClassRef{}, false
	}
	return nodeClassRef(pool, "spec", "template", "spec", "nodeClassRef")
}

func NodeClassRefForNodeClaim(claim *unstructured.Unstructured) (NodeClassRef, bool) {
	if claim == nil {
		return NodeClassRef{}, false
	}
	return nodeClassRef(claim, "spec", "nodeClassRef")
}

func nodeClassRef(obj *unstructured.Unstructured, fields ...string) (NodeClassRef, bool) {
	ref, found, err := unstructured.NestedMap(obj.Object, fields...)
	if err != nil || !found {
		return NodeClassRef{}, false
	}
	kind, _ := ref["kind"].(string)
	name, _ := ref["name"].(string)
	if kind == "" || name == "" {
		return NodeClassRef{}, false
	}

	switch obj.GetAPIVersion() {
	case APIVersionV1:
		group, _ := ref["group"].(string)
		if group == "" {
			return NodeClassRef{}, false
		}
		return NodeClassRef{Group: group, Kind: kind, Name: name}, true
	case APIVersionV1Beta1:
		apiVersion, _ := ref["apiVersion"].(string)
		gv, err := schema.ParseGroupVersion(apiVersion)
		if err != nil || gv.Group == "" || gv.Version == "" {
			return NodeClassRef{}, false
		}
		return NodeClassRef{
			Group:      gv.Group,
			Version:    gv.Version,
			Kind:       kind,
			Name:       name,
			APIVersion: apiVersion,
		}, true
	default:
		return NodeClassRef{}, false
	}
}

func Conditions(obj *unstructured.Unstructured) []metav1.Condition {
	if obj == nil {
		return nil
	}
	raw, found, err := unstructured.NestedSlice(obj.Object, "status", "conditions")
	if err != nil || !found {
		return nil
	}
	conditions := make([]metav1.Condition, 0, len(raw))
	for _, item := range raw {
		conditionMap, ok := item.(map[string]any)
		if !ok {
			continue
		}
		var condition metav1.Condition
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(conditionMap, &condition); err != nil || condition.Type == "" {
			continue
		}
		conditions = append(conditions, condition)
	}
	return conditions
}

func ReadyCondition(obj *unstructured.Unstructured) *metav1.Condition {
	return apiMeta.FindStatusCondition(Conditions(obj), "Ready")
}

func ResourceReadiness(obj *unstructured.Unstructured) Readiness {
	ready := ReadyCondition(obj)
	if ready == nil {
		return ReadinessUnknown
	}
	if obj.GetGeneration() > 0 && ready.ObservedGeneration > 0 && ready.ObservedGeneration < obj.GetGeneration() {
		return ReadinessUnknown
	}
	switch ready.Status {
	case metav1.ConditionTrue:
		return ReadinessReady
	case metav1.ConditionFalse:
		return ReadinessNotReady
	default:
		return ReadinessUnknown
	}
}

func NodePoolConditions(pool *unstructured.Unstructured) []metav1.Condition {
	return Conditions(pool)
}

func NodePoolCondition(pool *unstructured.Unstructured, conditionType string) *metav1.Condition {
	return apiMeta.FindStatusCondition(Conditions(pool), conditionType)
}

func NodePoolReadyCondition(pool *unstructured.Unstructured) *metav1.Condition {
	return ReadyCondition(pool)
}

func NodePoolReadiness(pool *unstructured.Unstructured) Readiness {
	return ResourceReadiness(pool)
}

func NodeClaimConditions(claim *unstructured.Unstructured) []metav1.Condition {
	return Conditions(claim)
}

// nodeClaimFailureReasons are condition reasons that mark a lifecycle stage as
// failed rather than in progress. Karpenter keeps a failing stage at
// status=Unknown (e.g. Launched=Unknown/LaunchFailed) and reserves
// status=False for hard invariant failures (Registered=False/MultipleNodesFound),
// so status alone cannot distinguish "still working on it" from "broken".
var nodeClaimFailureReasons = map[string]bool{
	"LaunchFailed":              true,
	"CreateError":               true,
	"InsufficientCapacityError": true,
	"NodeClassNotReady":         true,
	"MultipleNodesFound":        true,
	"UnregisteredTaintMissing":  true,
	// Cloud providers write their error mapping verbatim into
	// Launched=Unknown — the AWS provider's vocabulary carries no
	// fail/error substring for most of them. This mirrors the full set the
	// AWS provider's ToReasonMessage can return; the ones it produces that DO
	// carry the substring (SpotSLRCreationFailed, AMIAuthorizationFailure,
	// InternalError, LaunchFailed) are matched either way.
	"Unauthorized":                      true,
	"LaunchTemplateNotFound":            true,
	"SecurityGroupSubnetVPCMismatch":    true,
	"InstanceProfileNameInvalid":        true,
	"RequestLimitExceeded":              true,
	"VCPULimitExceeded":                 true,
	"InsufficientInstanceCapacity":      true,
	"InsufficientFreeAddressesInSubnet": true,
	"InvalidAMIID":                      true,
	"AMINotFound":                       true,
	"FreeTierIneligible":                true,
	"FleetQuotaExceeded":                true,
	"AccountPendingVerification":        true,
	"SpotQuotaExceeded":                 true,
}

// IsFailedLifecycleCondition reports whether a NodeClaim lifecycle condition
// (Launched/Registered/Initialized/Ready) records a failed stage. False always
// counts; Unknown counts only with a failure reason — other Unknown reasons
// (NodeNotFound, StartupTaintsExist, ...) are normal in-progress states.
func IsFailedLifecycleCondition(condition metav1.Condition) bool {
	switch condition.Status {
	case metav1.ConditionFalse:
		return true
	case metav1.ConditionUnknown:
		return IsFailureReason(condition.Reason)
	default:
		return false
	}
}

// IsFailedLifecycleConditionForVersion applies the API version's condition
// dialect. v1 reserves False for hard invariant failures, but v1beta1 used
// False for ordinary unmet stages (Initialized=False/NotInitialized is a
// claim still provisioning), so on v1beta1 a False only reads as failed when
// its reason carries failure vocabulary.
func IsFailedLifecycleConditionForVersion(apiVersion string, condition metav1.Condition) bool {
	if condition.Status == metav1.ConditionFalse && apiVersion == APIVersionV1Beta1 {
		return IsFailureReason(condition.Reason)
	}
	return IsFailedLifecycleCondition(condition)
}

// IsFailureReason reports whether a condition reason carries failure
// vocabulary — a known NodeClaim failure reason or generic fail/error text.
// Callers use it where a bare False is NOT evidence of failure (the Ready
// rollup, which is False for every claim still provisioning).
func IsFailureReason(reason string) bool {
	if nodeClaimFailureReasons[reason] {
		return true
	}
	lowered := strings.ToLower(reason)
	return strings.Contains(lowered, "fail") || strings.Contains(lowered, "error")
}

func NodeClaimReadyCondition(claim *unstructured.Unstructured) *metav1.Condition {
	return ReadyCondition(claim)
}

func NodeClaimReadiness(claim *unstructured.Unstructured) Readiness {
	return ResourceReadiness(claim)
}

func NodePoolStatusResources(pool *unstructured.Unstructured) corev1.ResourceList {
	if pool == nil {
		return nil
	}
	return resourceList(pool.Object, "status", "resources")
}

func NodeClaimCapacity(claim *unstructured.Unstructured) corev1.ResourceList {
	if claim == nil {
		return nil
	}
	return resourceList(claim.Object, "status", "capacity")
}

// NodeClaimAllocatable is what the node will actually offer the scheduler once
// it registers — capacity minus the kubelet/system reservations. karpenter.sh/v1
// publishes it; it is absent on claims that have not been launched yet.
func NodeClaimAllocatable(claim *unstructured.Unstructured) corev1.ResourceList {
	if claim == nil {
		return nil
	}
	return resourceList(claim.Object, "status", "allocatable")
}

func NormalizeNodePoolSpec(pool *unstructured.Unstructured) NodePoolSpec {
	if pool == nil {
		return NodePoolSpec{}
	}
	normalized := NodePoolSpec{
		Replicas:       nestedInt64(pool.Object, "spec", "replicas"),
		Weight:         nestedInt64(pool.Object, "spec", "weight"),
		Limits:         resourceList(pool.Object, "spec", "limits"),
		TemplateLabels: nestedStringMap(pool.Object, "spec", "template", "metadata", "labels"),
		Requirements:   requirements(pool.Object),
		Taints:         taints(pool.Object, "spec", "template", "spec", "taints"),
		StartupTaints:  taints(pool.Object, "spec", "template", "spec", "startupTaints"),
		Disruption: Disruption{
			ConsolidationPolicy: nestedString(pool.Object, "spec", "disruption", "consolidationPolicy"),
			ConsolidateAfter:    nestedString(pool.Object, "spec", "disruption", "consolidateAfter"),
			Budgets:             disruptionBudgets(pool.Object),
		},
	}
	if ref, ok := NodeClassRefForNodePool(pool); ok {
		normalized.NodeClassRef = &ref
	}
	// Karpenter moved expiry from disruption in v1beta1 to the node template in v1.
	switch pool.GetAPIVersion() {
	case APIVersionV1:
		normalized.ExpireAfter = nestedString(pool.Object, "spec", "template", "spec", "expireAfter")
		normalized.TerminationGracePeriod = nestedString(pool.Object, "spec", "template", "spec", "terminationGracePeriod")
	case APIVersionV1Beta1:
		normalized.ExpireAfter = nestedString(pool.Object, "spec", "disruption", "expireAfter")
	}
	return normalized
}

func ResolveNodePoolForClaim(claim metav1.Object, poolsByName map[string]*unstructured.Unstructured) (*unstructured.Unstructured, RelationshipSource) {
	if claim == nil {
		return nil, RelationshipNone
	}
	for _, owner := range claim.GetOwnerReferences() {
		if !isNodePoolOwner(owner) {
			continue
		}
		pool := poolsByName[owner.Name]
		if pool == nil {
			return nil, RelationshipNone
		}
		if owner.UID != "" && owner.UID != pool.GetUID() {
			// A same-name NodePool may have been recreated; the label must not relink
			// a claim whose authoritative owner UID identifies the deleted instance.
			return nil, RelationshipNone
		}
		return pool, RelationshipOwnerRef
	}

	poolName := claim.GetLabels()[NodePoolLabelKey]
	if poolName == "" {
		return nil, RelationshipNone
	}
	pool := poolsByName[poolName]
	if pool == nil {
		return nil, RelationshipNone
	}
	return pool, RelationshipLabel
}

func ClaimNodeName(claim *unstructured.Unstructured) string {
	if claim == nil {
		return ""
	}
	return nestedString(claim.Object, "status", "nodeName")
}

func ClaimProviderID(claim *unstructured.Unstructured) string {
	if claim == nil {
		return ""
	}
	return nestedString(claim.Object, "status", "providerID")
}

func isNodePoolOwner(owner metav1.OwnerReference) bool {
	if owner.Kind != NodePoolKind {
		return false
	}
	gv, err := schema.ParseGroupVersion(owner.APIVersion)
	return err == nil && gv.Group == Group
}

func nestedString(obj map[string]any, fields ...string) string {
	value, found, err := unstructured.NestedString(obj, fields...)
	if err != nil || !found {
		return ""
	}
	return value
}

func nestedInt64(obj map[string]any, fields ...string) *int64 {
	value, found, err := unstructured.NestedInt64(obj, fields...)
	if err != nil || !found {
		return nil
	}
	return &value
}

func nestedStringMap(obj map[string]any, fields ...string) map[string]string {
	value, found, err := unstructured.NestedStringMap(obj, fields...)
	if err != nil || !found {
		return nil
	}
	return value
}

func resourceList(obj map[string]any, fields ...string) corev1.ResourceList {
	raw, found, err := unstructured.NestedMap(obj, fields...)
	if err != nil || !found {
		return nil
	}
	result := make(corev1.ResourceList, len(raw))
	for name, value := range raw {
		text, ok := quantityText(value)
		if !ok {
			continue
		}
		quantity, err := resource.ParseQuantity(text)
		if err != nil {
			continue
		}
		result[corev1.ResourceName(name)] = quantity
	}
	return result
}

func quantityText(value any) (string, bool) {
	switch value := value.(type) {
	case string:
		return value, value != ""
	case int64:
		return strconv.FormatInt(value, 10), true
	case int32:
		return strconv.FormatInt(int64(value), 10), true
	case int:
		return strconv.Itoa(value), true
	case float64:
		return strconv.FormatFloat(value, 'g', -1, 64), true
	default:
		return "", false
	}
}

func requirements(obj map[string]any) []Requirement {
	raw, found, err := unstructured.NestedSlice(obj, "spec", "template", "spec", "requirements")
	if err != nil || !found {
		return nil
	}
	result := make([]Requirement, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key := nestedString(entry, "key")
		operator := nestedString(entry, "operator")
		if key == "" || operator == "" {
			continue
		}
		values, _, _ := unstructured.NestedStringSlice(entry, "values")
		result = append(result, Requirement{
			Key:       key,
			Operator:  corev1.NodeSelectorOperator(operator),
			Values:    values,
			MinValues: nestedInt64(entry, "minValues"),
		})
	}
	return result
}

func taints(obj map[string]any, fields ...string) []corev1.Taint {
	raw, found, err := unstructured.NestedSlice(obj, fields...)
	if err != nil || !found {
		return nil
	}
	result := make([]corev1.Taint, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key := nestedString(entry, "key")
		if key == "" {
			continue
		}
		result = append(result, corev1.Taint{
			Key:    key,
			Value:  nestedString(entry, "value"),
			Effect: corev1.TaintEffect(nestedString(entry, "effect")),
		})
	}
	return result
}

func disruptionBudgets(obj map[string]any) []DisruptionBudget {
	raw, found, err := unstructured.NestedSlice(obj, "spec", "disruption", "budgets")
	if err != nil || !found {
		return nil
	}
	result := make([]DisruptionBudget, 0, len(raw))
	for _, item := range raw {
		entry, ok := item.(map[string]any)
		if !ok {
			continue
		}
		reasons, _, _ := unstructured.NestedStringSlice(entry, "reasons")
		nodes := nestedString(entry, "nodes")
		if numeric := nestedInt64(entry, "nodes"); nodes == "" && numeric != nil {
			nodes = strconv.FormatInt(*numeric, 10)
		}
		result = append(result, DisruptionBudget{
			Nodes:    nodes,
			Schedule: nestedString(entry, "schedule"),
			Duration: nestedString(entry, "duration"),
			Reasons:  reasons,
		})
	}
	return result
}
