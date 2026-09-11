package issues

import (
	"fmt"
	"sort"
	"strings"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
)

const (
	ReasonKarpenterNodePoolNotReady            = "NodePoolNotReady"
	ReasonKarpenterNodeRegistrationUnhealthy   = "NodePoolNodeRegistrationUnhealthy"
	ReasonKarpenterNodeClassNotReady           = "NodeClassNotReady"
	ReasonKarpenterNodeClassNotFound           = "NodeClassNotFound"
	ReasonKarpenterNodeClassKindNotInstalled   = "NodeClassKindNotInstalled"
	ReasonKarpenterNodeClaimProvisioningFailed = "NodeClaimProvisioningFailed"
	karpenterFactReferencedByNodePools         = "karpenter_referenced_by_nodepools"
	karpenterFactReferencedNodeClass           = "karpenter_referenced_nodeclass"
)

type karpenterResourceCoverage uint8

const (
	karpenterCoverageUnknown karpenterResourceCoverage = iota
	karpenterCoverageAuthoritative
	karpenterCoverageNotInstalled
)

type karpenterResourceInventory struct {
	GVR      schema.GroupVersionResource
	Items    []*unstructured.Unstructured
	Coverage karpenterResourceCoverage
}

type karpenterIssueProvider interface {
	karpenterResourceDiscovery(group, kind string) karpenterResourceCoverage
	karpenterResources(group, kind string) karpenterResourceInventory
}

type karpenterNodeClassType struct {
	group string
	kind  string
}

type karpenterNodeClassReference struct {
	ref   karpenter.NodeClassRef
	pools []Ref
}

func detectKarpenterIssues(p Provider, f Filters) ([]Issue, map[string]bool) {
	kp, ok := p.(karpenterIssueProvider)
	if !ok || (len(f.Namespaces) > 0 && !f.IncludeClusterScopedKarpenter) || !canReadKarpenterKind(f, karpenter.Group, karpenter.NodePoolKind) {
		return nil, nil
	}

	poolsInventory := kp.karpenterResources(karpenter.Group, karpenter.NodePoolKind)
	if poolsInventory.Coverage != karpenterCoverageAuthoritative {
		return nil, nil
	}

	owned := make(map[string]bool)
	poolsByName := make(map[string]*unstructured.Unstructured, len(poolsInventory.Items))
	var out []Issue
	for _, pool := range poolsInventory.Items {
		if pool == nil || pool.GetName() == "" {
			continue
		}
		poolsByName[pool.GetName()] = pool
		owned[resourceKey(karpenter.Group, karpenter.NodePoolKind, pool.GetNamespace(), pool.GetName())] = true
		if issue, ok := karpenterNotReadyIssue(poolsInventory.GVR, karpenter.NodePoolKind, pool, ReasonKarpenterNodePoolNotReady,
			"Inspect the NodePool conditions and its referenced NodeClass, then resolve the controller-reported error."); ok {
			out = append(out, issue)
		}
		if issue, ok := karpenterRegistrationUnhealthyIssue(poolsInventory.GVR, pool); ok {
			out = append(out, issue)
		}
	}

	nodeClassRefs := referencedNodeClasses(poolsInventory.Items)
	types := make([]karpenterNodeClassType, 0, len(nodeClassRefs))
	for refType := range nodeClassRefs {
		types = append(types, refType)
	}
	sort.Slice(types, func(i, j int) bool {
		return types[i].group+"\x00"+types[i].kind < types[j].group+"\x00"+types[j].kind
	})
	for _, refType := range types {
		refs := nodeClassRefs[refType]
		discoveryCoverage := kp.karpenterResourceDiscovery(refType.group, refType.kind)
		switch discoveryCoverage {
		case karpenterCoverageNotInstalled:
			for _, ref := range refs {
				for _, pool := range ref.pools {
					if poolObject := poolsByName[pool.Name]; poolObject != nil {
						out = append(out, karpenterMissingNodeClassIssue(poolObject, ref.ref, ReasonKarpenterNodeClassKindNotInstalled))
					}
				}
			}
		case karpenterCoverageAuthoritative:
			refs = readableNodeClassRefs(refs, f.CanReadRelated)
			if len(refs) == 0 {
				continue
			}
			if !canReadKarpenterKind(f, refType.group, refType.kind) {
				continue
			}
			inventory := kp.karpenterResources(refType.group, refType.kind)
			if inventory.Coverage != karpenterCoverageAuthoritative {
				continue
			}
			classesByName := make(map[string]*unstructured.Unstructured, len(inventory.Items))
			for _, class := range inventory.Items {
				if class != nil && class.GetName() != "" {
					classesByName[class.GetName()] = class
				}
			}
			for _, ref := range refs {
				class := classesByName[ref.ref.Name]
				if class == nil {
					for _, pool := range ref.pools {
						if poolObject := poolsByName[pool.Name]; poolObject != nil {
							out = append(out, karpenterMissingNodeClassIssue(poolObject, ref.ref, ReasonKarpenterNodeClassNotFound))
						}
					}
					continue
				}
				owned[resourceKey(ref.ref.Group, ref.ref.Kind, class.GetNamespace(), class.GetName())] = true
				if issue, ok := karpenterNotReadyIssue(inventory.GVR, ref.ref.Kind, class, ReasonKarpenterNodeClassNotReady,
					"Inspect the NodeClass conditions and cloud-provider configuration, credentials, and quotas."); ok {
					issue.DiagnosticContext = referencedByNodePoolsContext(ref.pools)
					out = append(out, issue)
				}
			}
		}
	}

	if canReadKarpenterKind(f, karpenter.Group, karpenter.NodeClaimKind) {
		claimsInventory := kp.karpenterResources(karpenter.Group, karpenter.NodeClaimKind)
		if claimsInventory.Coverage == karpenterCoverageAuthoritative {
			for _, claim := range claimsInventory.Items {
				if claim == nil || claim.GetName() == "" {
					continue
				}
				owned[resourceKey(karpenter.Group, karpenter.NodeClaimKind, claim.GetNamespace(), claim.GetName())] = true
				condition := failingNodeClaimCondition(claim)
				if condition == nil {
					continue
				}
				issue := newKarpenterConditionIssue(claimsInventory.GVR, karpenter.NodeClaimKind, claim, condition,
					ReasonKarpenterNodeClaimProvisioningFailed,
					nodeClaimFailureCause(condition),
					"Inspect the NodeClaim conditions and Karpenter controller logs, then resolve the provider launch or node registration failure.")
				issue.Fingerprint = nodeClaimFailureFingerprint(condition)
				if pool, _ := karpenter.ResolveNodePoolForClaim(claim, poolsByName); pool != nil {
					issue.Owner = Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: pool.GetName()}
				}
				enrichIdentity(&issue)
				out = append(out, issue)
			}
		}
	}

	return out, owned
}

func canReadKarpenterKind(f Filters, group, kind string) bool {
	return f.CanReadClusterScoped == nil || f.CanReadClusterScoped(kind, group)
}

func referencedNodeClasses(pools []*unstructured.Unstructured) map[karpenterNodeClassType][]karpenterNodeClassReference {
	byTypeAndName := make(map[karpenterNodeClassType]map[string]*karpenterNodeClassReference)
	for _, pool := range pools {
		if pool == nil || pool.GetName() == "" {
			continue
		}
		ref, ok := karpenter.NodeClassRefForNodePool(pool)
		if !ok {
			continue
		}
		refType := karpenterNodeClassType{group: ref.Group, kind: ref.Kind}
		byName := byTypeAndName[refType]
		if byName == nil {
			byName = make(map[string]*karpenterNodeClassReference)
			byTypeAndName[refType] = byName
		}
		entry := byName[ref.Name]
		if entry == nil {
			entry = &karpenterNodeClassReference{ref: ref}
			byName[ref.Name] = entry
		}
		entry.pools = append(entry.pools, Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: pool.GetName()})
	}

	result := make(map[karpenterNodeClassType][]karpenterNodeClassReference, len(byTypeAndName))
	for refType, byName := range byTypeAndName {
		names := make([]string, 0, len(byName))
		for name := range byName {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			entry := byName[name]
			sortRefs(entry.pools)
			result[refType] = append(result[refType], *entry)
		}
	}
	return result
}

func readableNodeClassRefs(refs []karpenterNodeClassReference, canRead func(Ref) bool) []karpenterNodeClassReference {
	if canRead == nil {
		return refs
	}
	out := make([]karpenterNodeClassReference, 0, len(refs))
	for _, ref := range refs {
		if canRead(Ref{Group: ref.ref.Group, Kind: ref.ref.Kind, Name: ref.ref.Name}) {
			out = append(out, ref)
		}
	}
	return out
}

func karpenterNotReadyIssue(gvr schema.GroupVersionResource, kind string, obj *unstructured.Unstructured, reason, action string) (Issue, bool) {
	if karpenter.ResourceReadiness(obj) != karpenter.ReadinessNotReady {
		return Issue{}, false
	}
	condition := karpenter.ReadyCondition(obj)
	if condition == nil || isTransientCRDCondition(obj, condition.Reason) {
		return Issue{}, false
	}
	cause := fmt.Sprintf("The %s Ready condition is False.", kind)
	if condition.Reason != "" {
		cause = fmt.Sprintf("The %s Ready condition is False (%s).", kind, condition.Reason)
	}
	return newKarpenterConditionIssue(gvr, kind, obj, condition, reason, cause, action), true
}

// karpenterRegistrationUnhealthyIssue surfaces NodePool NodeRegistrationHealthy=False.
// Karpenter deliberately keeps this condition outside the pool's aggregate Ready,
// so a pool can look Ready while every launched node fails to register — and the
// failing NodeClaims themselves are deleted at the registration timeout, leaving
// this condition as the only durable trace.
func karpenterRegistrationUnhealthyIssue(gvr schema.GroupVersionResource, pool *unstructured.Unstructured) (Issue, bool) {
	condition := karpenter.NodePoolCondition(pool, karpenter.NodeRegistrationHealthyConditionType)
	if condition == nil || condition.Status != metav1.ConditionFalse {
		return Issue{}, false
	}
	if pool.GetGeneration() > 0 && condition.ObservedGeneration > 0 && condition.ObservedGeneration < pool.GetGeneration() {
		return Issue{}, false
	}
	cause := "The NodePool's NodeRegistrationHealthy condition is False: recently launched nodes failed to register before the registration timeout. This signal is outside the pool's Ready condition, so the pool can report Ready while launches keep failing."
	action := "Inspect recent NodeClaims and the Karpenter controller logs for launch or registration errors (user data, networking, instance profile/credentials), then fix the NodeClass or pool template."
	return newKarpenterConditionIssue(gvr, karpenter.NodePoolKind, pool, condition, ReasonKarpenterNodeRegistrationUnhealthy, cause, action), true
}

func newKarpenterConditionIssue(gvr schema.GroupVersionResource, kind string, obj *unstructured.Unstructured, condition *metav1.Condition, reason, cause, action string) Issue {
	message := cause
	var transitionAt time.Time
	var transitionKnown bool
	if condition != nil {
		if condition.Message != "" {
			message = condition.Message
		}
		if !condition.LastTransitionTime.IsZero() {
			transitionAt = condition.LastTransitionTime.Time
			transitionKnown = true
		}
	}
	issue := newConditionIssue(gvr, kind, obj.GetNamespace(), obj.GetName(), SeverityWarning, reason, message, transitionAt, transitionKnown, reason, obj.GetCreationTimestamp().Time)
	issue.Cause = cause
	issue.Action = action
	classifyIssue(&issue)
	enrichIdentity(&issue)
	return issue
}

// RedactIssueRelatedRefs strips referenced-by-NodePool facts — the names AND
// the count — from an issue whose caller cannot read every referenced
// NodePool. CanReadIssueRelatedRefs below is the mirror gate: it drops issues
// whose PRIMARY diagnosis depends on an unreadable NodeClass; here the issue
// itself (e.g. NodeClass not ready) is legitimately visible to a
// NodeClass-readable caller — only the identities of the referencing NodePools
// are not. A nil canRead means the compose is user-agnostic (e.g. the shared
// capacity-issues memo, whose per-user gates apply at read time) — no
// redaction there; every per-user surface wires an authorizer.
func RedactIssueRelatedRefs(issue Issue, canRead func(Ref) bool) Issue {
	if canRead == nil || issue.DiagnosticContext == nil {
		return issue
	}
	facts := issue.DiagnosticContext.Facts
	kept := make([]issuesapi.DiagnosticFact, 0, len(facts))
	changed := false
	for _, fact := range facts {
		if fact.Type == karpenterFactReferencedByNodePools && !diagnosticRefsReadable(fact.Refs, canRead) {
			changed = true
			continue
		}
		kept = append(kept, fact)
	}
	if !changed {
		return issue
	}
	if len(kept) == 0 {
		issue.DiagnosticContext = nil
		return issue
	}
	redacted := *issue.DiagnosticContext
	redacted.Facts = kept
	issue.DiagnosticContext = &redacted
	return issue
}

func redactUnreadableRelatedRefs(list []Issue, canRead func(Ref) bool) []Issue {
	if canRead == nil {
		return list
	}
	for i := range list {
		list[i] = RedactIssueRelatedRefs(list[i], canRead)
	}
	return list
}

func diagnosticRefsReadable(refs []issuesapi.Ref, canRead func(Ref) bool) bool {
	for _, ref := range refs {
		if !canRead(Ref(ref)) {
			return false
		}
	}
	return true
}

func CanReadIssueRelatedRefs(issue Issue, canRead func(Ref) bool) bool {
	if issue.DiagnosticContext == nil {
		return true
	}
	for _, fact := range issue.DiagnosticContext.Facts {
		if fact.Type != karpenterFactReferencedNodeClass {
			continue
		}
		if canRead == nil {
			return false
		}
		for _, ref := range fact.Refs {
			if !canRead(Ref(ref)) {
				return false
			}
		}
	}
	return true
}

func karpenterMissingNodeClassIssue(pool *unstructured.Unstructured, ref karpenter.NodeClassRef, reason string) Issue {
	message := fmt.Sprintf("Referenced %s %q was not found.", ref.Kind, ref.Name)
	cause := fmt.Sprintf("The NodePool references %s %q, but the authoritative cache contains no such object.", ref.Kind, ref.Name)
	action := fmt.Sprintf("Create the referenced %s or update spec.template.spec.nodeClassRef to an existing object.", ref.Kind)
	if reason == ReasonKarpenterNodeClassKindNotInstalled {
		message = fmt.Sprintf("Referenced NodeClass API %s/%s is not installed.", ref.Group, ref.Kind)
		cause = fmt.Sprintf("The NodePool references %s/%s, but that API kind is not installed.", ref.Group, ref.Kind)
		action = "Install the provider's NodeClass CRD and controller, or update spec.template.spec.nodeClassRef to an installed kind."
	}
	issue := newConditionIssue(
		schema.GroupVersionResource{Group: karpenter.Group},
		karpenter.NodePoolKind,
		pool.GetNamespace(),
		pool.GetName(),
		SeverityWarning,
		reason,
		message,
		time.Time{},
		false,
		reason+"\x00"+resourceKey(ref.Group, ref.Kind, "", ref.Name),
		pool.GetCreationTimestamp().Time,
	)
	issue.FirstSeen = time.Time{}
	issue.OnsetUnknown = true
	issue.LastSeen = time.Now()
	issue.Cause = cause
	issue.Action = action
	if reason == ReasonKarpenterNodeClassNotFound {
		issue.DiagnosticContext = &issuesapi.DiagnosticContext{
			Role: issuesapi.DiagnosticRoleContext,
			Facts: []issuesapi.DiagnosticFact{{
				Type:    karpenterFactReferencedNodeClass,
				Message: fmt.Sprintf("References %s %q.", ref.Kind, ref.Name),
				Refs:    []issuesapi.Ref{{Group: ref.Group, Kind: ref.Kind, Name: ref.Name}},
			}},
		}
	}
	// newConditionIssue already classified + enriched with this fingerprint;
	// the fields set above (Cause/Action/DiagnosticContext) don't affect
	// identity, so no re-classify/re-enrich is needed here.
	return issue
}

func failingNodeClaimCondition(claim *unstructured.Unstructured) *metav1.Condition {
	conditions := karpenter.NodeClaimConditions(claim)
	for _, conditionType := range []string{"Launched", "Registered", "Initialized", "Ready"} {
		for i := range conditions {
			condition := &conditions[i]
			if condition.Type != conditionType || !karpenter.IsFailedLifecycleConditionForVersion(claim.GetAPIVersion(), *condition) {
				continue
			}
			if claim.GetGeneration() > 0 && condition.ObservedGeneration > 0 && condition.ObservedGeneration < claim.GetGeneration() {
				continue
			}
			if isTransientCRDCondition(claim, condition.Reason) {
				continue
			}
			return condition
		}
	}
	return nil
}

func nodeClaimFailureCause(condition *metav1.Condition) string {
	stage := strings.ToLower(condition.Type)
	if stage == "ready" {
		stage = "readiness"
	}
	cause := fmt.Sprintf("NodeClaim provisioning failed at the %s stage.", stage)
	if condition.Reason != "" {
		cause = fmt.Sprintf("NodeClaim provisioning failed at the %s stage (%s).", stage, condition.Reason)
	}
	return cause
}

func nodeClaimFailureFingerprint(condition *metav1.Condition) string {
	if condition == nil {
		return ReasonKarpenterNodeClaimProvisioningFailed
	}
	return ReasonKarpenterNodeClaimProvisioningFailed + "\x00" + strings.ToLower(condition.Type) + "\x00" + condition.Reason
}

func referencedByNodePoolsContext(pools []Ref) *issuesapi.DiagnosticContext {
	refs := append([]Ref(nil), pools...)
	sortRefs(refs)
	return &issuesapi.DiagnosticContext{
		Role: issuesapi.DiagnosticRoleContext,
		Facts: []issuesapi.DiagnosticFact{{
			Type:    karpenterFactReferencedByNodePools,
			Message: fmt.Sprintf("Referenced by %d NodePool(s).", len(refs)),
			Refs:    refs,
		}},
	}
}
