package issues

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/karpenter"
	"github.com/skyhook-io/radar/pkg/subject"
)

type fakeKarpenterIssueProvider struct {
	*fakeProvider
	inventories map[karpenterNodeClassType]karpenterResourceInventory
	calls       map[karpenterNodeClassType]int
}

func (p *fakeKarpenterIssueProvider) karpenterResourceDiscovery(group, kind string) karpenterResourceCoverage {
	if inventory, ok := p.inventories[karpenterNodeClassType{group: group, kind: kind}]; ok {
		return inventory.Coverage
	}
	return karpenterCoverageUnknown
}

func (p *fakeKarpenterIssueProvider) karpenterResources(group, kind string) karpenterResourceInventory {
	key := karpenterNodeClassType{group: group, kind: kind}
	if p.calls == nil {
		p.calls = make(map[karpenterNodeClassType]int)
	}
	p.calls[key]++
	if inventory, ok := p.inventories[key]; ok {
		return inventory
	}
	return karpenterResourceInventory{Coverage: karpenterCoverageUnknown}
}

func TestComposeKarpenterIssuesAreCuratedOnceWithStableDiagnosis(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	claimGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodeclaims"}
	classGVR := schema.GroupVersionResource{Group: "capacity.acme.io", Version: "v1", Resource: "fleetnodeclasses"}
	pool := karpenterIssueTestPool("compute", "capacity.acme.io", "FleetNodeClass", "general", "False", "NodeClassNotReady", "the class is blocked")
	class := karpenterIssueTestObject("capacity.acme.io/v1", "FleetNodeClass", "general", "False", "AuthFailed", "cloud credentials are invalid")
	// Real Karpenter records launch failures at status=Unknown (not False).
	claim := karpenterIssueTestClaim("compute-x1", pool, "Launched", "Unknown", "LaunchFailed", "instance launch was rejected")

	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{
		poolGVR:  {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}},
		claimGVR: {kind: karpenter.NodeClaimKind, items: []*unstructured.Unstructured{claim}},
		classGVR: {kind: "FleetNodeClass", items: []*unstructured.Unstructured{class}},
	})

	out := Compose(p, Filters{Limit: NoLimit})
	if len(out) != 3 {
		t.Fatalf("Compose() returned %d Karpenter issues, want one per curated cause: %+v", len(out), out)
	}
	byReason := make(map[string]Issue, len(out))
	for _, issue := range out {
		if byReason[issue.Reason].ID != "" {
			t.Fatalf("duplicate curated issue for reason %q: %+v", issue.Reason, out)
		}
		byReason[issue.Reason] = issue
		if issue.Severity != SeverityWarning || issue.Source != SourceCondition || issue.Category != issuesapi.CategoryNodeProvisioningFail {
			t.Errorf("issue classification = severity:%q source:%q category:%q, want warning/condition/node_provisioning_failed", issue.Severity, issue.Source, issue.Category)
		}
		if issue.Cause == "" || issue.Action == "" {
			t.Errorf("issue %q omitted structured diagnosis: %+v", issue.Reason, issue)
		}
		if issue.RemediationKind != "" || issue.RemediationTarget != "" {
			t.Errorf("issue %q exposed a mutation remediation: %+v", issue.Reason, issue)
		}
	}

	poolIssue := byReason[ReasonKarpenterNodePoolNotReady]
	if poolIssue.Cause != "The NodePool Ready condition is False (NodeClassNotReady)." || poolIssue.Message != "the class is blocked" {
		t.Errorf("NodePool diagnosis = cause:%q message:%q", poolIssue.Cause, poolIssue.Message)
	}
	assertKarpenterIssueID(t, poolIssue, Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "compute"})

	classIssue := byReason[ReasonKarpenterNodeClassNotReady]
	if classIssue.Group != "capacity.acme.io" || classIssue.Kind != "FleetNodeClass" || classIssue.Name != "general" {
		t.Errorf("arbitrary NodeClass subject = %+v", classIssue)
	}
	if classIssue.Cause != "The FleetNodeClass Ready condition is False (AuthFailed)." || classIssue.Message != "cloud credentials are invalid" {
		t.Errorf("NodeClass diagnosis = cause:%q message:%q", classIssue.Cause, classIssue.Message)
	}
	assertKarpenterIssueID(t, classIssue, Ref{Group: "capacity.acme.io", Kind: "FleetNodeClass", Name: "general"})

	claimIssue := byReason[ReasonKarpenterNodeClaimProvisioningFailed]
	if claimIssue.Kind != karpenter.NodeClaimKind || claimIssue.Owner.Kind != karpenter.NodePoolKind || claimIssue.Owner.Name != "compute" {
		t.Errorf("NodeClaim issue ownership = %+v", claimIssue)
	}
	if claimIssue.Cause != "NodeClaim provisioning failed at the launched stage (LaunchFailed)." || claimIssue.Message != "instance launch was rejected" {
		t.Errorf("NodeClaim diagnosis = cause:%q message:%q", claimIssue.Cause, claimIssue.Message)
	}
	assertKarpenterIssueID(t, claimIssue, Ref{Group: karpenter.Group, Kind: karpenter.NodePoolKind, Name: "compute"})
}

func TestComposeKarpenterSharedNodeClassLinksEveryReferringPool(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	claimGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodeclaims"}
	classGVR := schema.GroupVersionResource{Group: "capacity.acme.io", Version: "v1", Resource: "fleetnodeclasses"}
	pools := []*unstructured.Unstructured{
		karpenterIssueTestPool("batch", "capacity.acme.io", "FleetNodeClass", "shared", "True", "Ready", ""),
		karpenterIssueTestPool("compute", "capacity.acme.io", "FleetNodeClass", "shared", "True", "Ready", ""),
	}
	class := karpenterIssueTestObject("capacity.acme.io/v1", "FleetNodeClass", "shared", "False", "QuotaExceeded", "instance quota is exhausted")
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{
		poolGVR:  {kind: karpenter.NodePoolKind, items: pools},
		claimGVR: {kind: karpenter.NodeClaimKind},
		classGVR: {kind: "FleetNodeClass", items: []*unstructured.Unstructured{class}},
	})

	out := Compose(p, Filters{Grouped: true, Limit: NoLimit})
	if len(out) != 1 || out[0].Reason != ReasonKarpenterNodeClassNotReady {
		t.Fatalf("shared NodeClass issues = %+v, want one shared issue", out)
	}
	ctx := out[0].DiagnosticContext
	if ctx == nil || len(ctx.Facts) != 1 || ctx.Facts[0].Type != karpenterFactReferencedByNodePools || len(ctx.Facts[0].Refs) != 2 {
		t.Fatalf("shared NodeClass context = %+v, want both NodePool refs", ctx)
	}
	if ctx.Facts[0].Refs[0].Name != "batch" || ctx.Facts[0].Refs[1].Name != "compute" {
		t.Errorf("NodePool refs = %+v, want deterministic batch,compute order", ctx.Facts[0].Refs)
	}
	for _, poolName := range []string{"batch", "compute"} {
		// A caller viewing a NodePool can read NodePools (the endpoint gate
		// upstream guarantees it) — the related-access question is the class.
		related := RelatedIssues(p, RelatedIssueOptions{CanReadRelated: func(ref Ref) bool {
			if ref.Group == karpenter.Group && ref.Kind == karpenter.NodePoolKind {
				return true
			}
			return ref.Group == "capacity.acme.io" && ref.Kind == "FleetNodeClass" && ref.Name == "shared"
		}}, karpenter.Group, karpenter.NodePoolKind, "", poolName)
		if len(related) != 1 || related[0].ID != out[0].ID {
			t.Errorf("RelatedIssues(NodePool %q) = %+v, want the shared NodeClass issue", poolName, related)
		}
		if denied := RelatedIssues(p, RelatedIssueOptions{}, karpenter.Group, karpenter.NodePoolKind, "", poolName); len(denied) != 0 {
			t.Errorf("RelatedIssues(NodePool %q) leaked a NodeClass issue without related-resource access: %+v", poolName, denied)
		}
	}
}

func TestComposeKarpenterMissingNodeClassRequiresAuthoritativeCoverage(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	pools := []*unstructured.Unstructured{
		karpenterIssueTestPool("missing-object", "installed.acme.io", "InstalledNodeClass", "absent", "True", "Ready", ""),
		karpenterIssueTestPool("missing-kind", "missing.acme.io", "MissingNodeClass", "default", "True", "Ready", ""),
		karpenterIssueTestPool("unknown-coverage", "partial.acme.io", "PartialNodeClass", "default", "True", "Ready", ""),
	}
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{poolGVR: {kind: karpenter.NodePoolKind, items: pools}})
	p.inventories[karpenterNodeClassType{group: "installed.acme.io", kind: "InstalledNodeClass"}] = karpenterResourceInventory{
		GVR: schema.GroupVersionResource{Group: "installed.acme.io", Version: "v1", Resource: "installednodeclasses"}, Coverage: karpenterCoverageAuthoritative,
	}
	p.inventories[karpenterNodeClassType{group: "missing.acme.io", kind: "MissingNodeClass"}] = karpenterResourceInventory{Coverage: karpenterCoverageNotInstalled}
	p.inventories[karpenterNodeClassType{group: "partial.acme.io", kind: "PartialNodeClass"}] = karpenterResourceInventory{Coverage: karpenterCoverageUnknown}

	out := Compose(p, Filters{Limit: NoLimit})
	if len(out) != 2 {
		t.Fatalf("missing NodeClass issues = %+v, want only authoritative missing-object and not-installed assertions", out)
	}
	byName := map[string]Issue{}
	for _, issue := range out {
		byName[issue.Name] = issue
	}
	if byName["missing-object"].Reason != ReasonKarpenterNodeClassNotFound {
		t.Errorf("missing-object issue = %+v", byName["missing-object"])
	}
	if !byName["missing-object"].FirstSeen.IsZero() || !byName["missing-object"].OnsetUnknown || byName["missing-object"].IssueTiming != "" || byName["missing-object"].IssueTimingBasis != "" {
		t.Errorf("missing-object fabricated age evidence: firstSeen:%s timing:%q/%q", byName["missing-object"].FirstSeen, byName["missing-object"].IssueTiming, byName["missing-object"].IssueTimingBasis)
	}
	if byName["missing-kind"].Reason != ReasonKarpenterNodeClassKindNotInstalled {
		t.Errorf("missing-kind issue = %+v", byName["missing-kind"])
	}
	if _, exists := byName["unknown-coverage"]; exists {
		t.Fatalf("partial coverage produced a false missing assertion: %+v", byName["unknown-coverage"])
	}
}

func TestComposeKarpenterDeniedNodeClassDoesNotAssertMissing(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	pool := karpenterIssueTestPool("compute", "capacity.acme.io", "FleetNodeClass", "absent", "True", "Ready", "")
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{poolGVR: {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}}})
	p.inventories[karpenterNodeClassType{group: "capacity.acme.io", kind: "FleetNodeClass"}] = karpenterResourceInventory{Coverage: karpenterCoverageAuthoritative}

	out := Compose(p, Filters{
		Limit:                NoLimit,
		CanReadClusterScoped: func(string, string) bool { return true },
		CanReadRelated: func(ref Ref) bool {
			return ref.Group != "capacity.acme.io" || ref.Kind != "FleetNodeClass"
		},
	})
	if len(out) != 0 {
		t.Fatalf("denied NodeClass produced an issue: %+v", out)
	}
	key := karpenterNodeClassType{group: "capacity.acme.io", kind: "FleetNodeClass"}
	if p.calls[key] != 0 {
		t.Fatalf("denied NodeClass inventory was read %d time(s), want permission gate before read", p.calls[key])
	}
	out = Compose(p, Filters{
		Limit:                NoLimit,
		CanReadClusterScoped: func(string, string) bool { return true },
		CanReadRelated:       func(Ref) bool { return true },
	})
	if len(out) != 1 || out[0].Reason != ReasonKarpenterNodeClassNotFound {
		t.Fatalf("authorized missing NodeClass issue omitted: %+v", out)
	}
}

func TestComposeKarpenterDeniedUninstalledNodeClassUsesReadablePoolEvidence(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	pool := karpenterIssueTestPool("compute", "missing.acme.io", "MissingNodeClass", "default", "True", "Ready", "")
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{poolGVR: {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}}})
	key := karpenterNodeClassType{group: "missing.acme.io", kind: "MissingNodeClass"}
	p.inventories[key] = karpenterResourceInventory{Coverage: karpenterCoverageNotInstalled}

	out := Compose(p, Filters{
		Limit: NoLimit,
		CanReadClusterScoped: func(kind, group string) bool {
			return group == karpenter.Group && kind == karpenter.NodePoolKind
		},
		CanReadRelated: func(Ref) bool { return false },
	})
	if len(out) != 1 || out[0].Reason != ReasonKarpenterNodeClassKindNotInstalled || out[0].Kind != karpenter.NodePoolKind || out[0].Name != "compute" {
		t.Fatalf("authoritative absent kind was hidden by its unclassifiable SAR: %+v", out)
	}
	if p.calls[key] != 0 {
		t.Fatalf("absent denied kind attempted an inventory list %d time(s)", p.calls[key])
	}
}

func TestComposeKarpenterClusterIssuesRequireMixedScopeOptIn(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	pool := karpenterIssueTestPool("compute", "", "", "", "False", "ValidationFailed", "invalid pool configuration")
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{poolGVR: {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}}})

	filters := Filters{
		Namespaces: []string{"team-a"},
		Limit:      NoLimit,
		CanReadClusterScoped: func(kind, group string) bool {
			return kind == karpenter.NodePoolKind && group == karpenter.Group
		},
	}
	if out := Compose(p, filters); len(out) != 0 {
		t.Fatalf("namespace-filtered issue query included cluster-scoped Karpenter rows: %+v", out)
	}
	filters.IncludeClusterScopedKarpenter = true
	out := Compose(p, filters)
	if len(out) != 1 || out[0].Reason != ReasonKarpenterNodePoolNotReady {
		t.Fatalf("mixed-scope projection omitted authorized Karpenter issue: %+v", out)
	}
}

func TestComposeKarpenterStaleClaimFailureDoesNotAssertIssue(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	claimGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodeclaims"}
	pool := karpenterIssueTestPool("compute", "", "", "", "True", "Ready", "")
	claim := karpenterIssueTestClaim("compute-a", pool, "Launched", "False", "LaunchFailed", "old generation failed")
	claim.SetGeneration(2)
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{
		poolGVR:  {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}},
		claimGVR: {kind: karpenter.NodeClaimKind, items: []*unstructured.Unstructured{claim}},
	})

	if out := Compose(p, Filters{Limit: NoLimit}); len(out) != 0 {
		t.Fatalf("stale NodeClaim failure produced an issue: %+v", out)
	}
}

func TestComposeKarpenterInProgressClaimIsNotAFailure(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	claimGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodeclaims"}
	pool := karpenterIssueTestPool("compute", "", "", "", "True", "Ready", "")
	// Registered=Unknown/NodeNotFound is the normal shape while a launched
	// instance boots — it must not read as a provisioning failure.
	claim := karpenterIssueTestClaim("compute-a", pool, "Registered", "Unknown", "NodeNotFound", "node has not joined yet")
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{
		poolGVR:  {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}},
		claimGVR: {kind: karpenter.NodeClaimKind, items: []*unstructured.Unstructured{claim}},
	})

	if out := Compose(p, Filters{Limit: NoLimit}); len(out) != 0 {
		t.Fatalf("in-progress NodeClaim produced an issue: %+v", out)
	}
}

func TestComposeKarpenterNodeRegistrationUnhealthySurfacesOnReadyPool(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	pool := karpenterIssueTestPool("compute", "", "", "", "True", "Ready", "")
	status := pool.Object["status"].(map[string]any)
	status["conditions"] = append(status["conditions"].([]any), map[string]any{
		"type": karpenter.NodeRegistrationHealthyConditionType, "status": "False",
		"reason": "RegistrationFailed", "message": "machines launched but never registered",
		"observedGeneration": int64(1),
		"lastTransitionTime": time.Now().Add(-10 * time.Minute).UTC().Format(time.RFC3339),
	})
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{
		poolGVR: {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}},
	})

	out := Compose(p, Filters{Limit: NoLimit})
	if len(out) != 1 {
		t.Fatalf("issues = %+v, want exactly the registration-health issue", out)
	}
	issue := out[0]
	if issue.Reason != ReasonKarpenterNodeRegistrationUnhealthy || issue.Kind != karpenter.NodePoolKind || issue.Name != "compute" {
		t.Fatalf("issue = %+v", issue)
	}
	if issue.Message != "machines launched but never registered" {
		t.Fatalf("issue message = %q", issue.Message)
	}
}

func TestComposeKarpenterFailedClaimsGroupUnderOwningPool(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	claimGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodeclaims"}
	pool := karpenterIssueTestPool("compute", "", "", "", "True", "Ready", "")
	claims := []*unstructured.Unstructured{
		karpenterIssueTestClaim("compute-a", pool, "Launched", "False", "LaunchFailed", "launch failed"),
		karpenterIssueTestClaim("compute-b", pool, "Launched", "False", "LaunchFailed", "another launch failed"),
		karpenterIssueTestClaim("compute-c", pool, "Registered", "False", "RegistrationFailed", "registration failed"),
	}
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{
		poolGVR:  {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}},
		claimGVR: {kind: karpenter.NodeClaimKind, items: claims},
	})

	out := Compose(p, Filters{Grouped: true, Limit: NoLimit})
	if len(out) != 2 {
		t.Fatalf("grouped failed claims = %+v, want one issue per stable failure stage/reason", out)
	}
	counts := map[string]int{}
	ids := map[string]bool{}
	for _, issue := range out {
		if issue.Kind != karpenter.NodePoolKind || issue.Group != karpenter.Group || issue.Name != "compute" || issue.Reason != ReasonKarpenterNodeClaimProvisioningFailed {
			t.Errorf("claim rollup subject = %+v", issue)
		}
		if issue.Cause == "" || issue.Action == "" {
			t.Errorf("claim rollup lost its structured diagnosis: %+v", issue)
		}
		if issue.Affected != (issuesapi.Affected{}) {
			t.Errorf("claim affected buckets = %+v, want empty unsupported-kind buckets", issue.Affected)
		}
		counts[issue.Cause] = issue.Count
		ids[issue.ID] = true
	}
	if counts["NodeClaim provisioning failed at the launched stage (LaunchFailed)."] != 2 || counts["NodeClaim provisioning failed at the registered stage (RegistrationFailed)."] != 1 {
		t.Errorf("claim grouping by stable failure = %+v", counts)
	}
	if len(ids) != 2 {
		t.Errorf("stable failure groups reused an issue ID: %+v", out)
	}
}

func TestKarpenterMissingNodeClassIdentityIncludesReferencedTarget(t *testing.T) {
	pool := karpenterIssueTestPool("compute", "capacity.acme.io", "FleetNodeClass", "first", "True", "Ready", "")
	first := karpenterMissingNodeClassIssue(pool, karpenter.NodeClassRef{Group: "capacity.acme.io", Kind: "FleetNodeClass", Name: "first"}, ReasonKarpenterNodeClassNotFound)
	second := karpenterMissingNodeClassIssue(pool, karpenter.NodeClassRef{Group: "capacity.acme.io", Kind: "FleetNodeClass", Name: "second"}, ReasonKarpenterNodeClassNotFound)
	if first.ID == second.ID || first.Fingerprint == second.Fingerprint {
		t.Fatalf("different missing NodeClass targets reused identity: first=%+v second=%+v", first, second)
	}
}

func TestRelatedIssuesRequiresAccessToReferencedMissingNodeClass(t *testing.T) {
	pool := karpenterIssueTestPool("compute", "capacity.acme.io", "FleetNodeClass", "missing", "True", "Ready", "")
	issue := karpenterMissingNodeClassIssue(pool, karpenter.NodeClassRef{
		Group: "capacity.acme.io", Kind: "FleetNodeClass", Name: "missing",
	}, ReasonKarpenterNodeClassNotFound)
	grouped := GroupIssues([]Issue{issue})
	if got := RelatedIssuesFrom([]Issue{issue}, grouped, RelatedIssueOptions{CanReadRelated: func(Ref) bool { return false }}, karpenter.Group, karpenter.NodePoolKind, "", "compute"); len(got) != 0 {
		t.Fatalf("NodePool lookup leaked a denied missing NodeClass issue: %+v", got)
	}
	if got := RelatedIssuesFrom([]Issue{issue}, grouped, RelatedIssueOptions{CanReadRelated: func(Ref) bool { return true }}, karpenter.Group, karpenter.NodePoolKind, "", "compute"); len(got) != 1 {
		t.Fatalf("NodePool lookup omitted an authorized missing NodeClass issue: %+v", got)
	}
}

func TestComposeKarpenterUnknownInventoryFallsBackToGenericConditions(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	pool := karpenterIssueTestPool("compute", "", "", "", "False", "Drifted", "generic fallback remains available")
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{poolGVR: {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}}})
	p.inventories[karpenterNodeClassType{group: karpenter.Group, kind: karpenter.NodePoolKind}] = karpenterResourceInventory{Coverage: karpenterCoverageUnknown}

	out := Compose(p, Filters{Limit: NoLimit})
	if len(out) != 1 || out[0].Reason != "Ready: Drifted" {
		t.Fatalf("unknown curated coverage suppressed generic fallback: %+v", out)
	}
}

func newFakeKarpenterIssueProvider(resources map[schema.GroupVersionResource]struct {
	kind  string
	items []*unstructured.Unstructured
}) *fakeKarpenterIssueProvider {
	dynamic := make(map[schema.GroupVersionResource][]*unstructured.Unstructured, len(resources))
	kinds := make(map[schema.GroupVersionResource]string, len(resources))
	namespaced := make(map[schema.GroupVersionResource]bool, len(resources))
	inventories := make(map[karpenterNodeClassType]karpenterResourceInventory, len(resources))
	for gvr, resource := range resources {
		dynamic[gvr] = resource.items
		kinds[gvr] = resource.kind
		namespaced[gvr] = false
		inventories[karpenterNodeClassType{group: gvr.Group, kind: resource.kind}] = karpenterResourceInventory{
			GVR: gvr, Items: resource.items, Coverage: karpenterCoverageAuthoritative,
		}
	}
	return &fakeKarpenterIssueProvider{
		fakeProvider: &fakeProvider{dynamic: dynamic, kinds: kinds, namespaced: namespaced},
		inventories:  inventories,
	}
}

func karpenterIssueTestPool(name, classGroup, classKind, className, readyStatus, reason, message string) *unstructured.Unstructured {
	object := karpenterIssueTestObject(karpenter.APIVersionV1, karpenter.NodePoolKind, name, readyStatus, reason, message)
	object.SetUID(types.UID(name + "-uid"))
	object.SetGeneration(1)
	if classGroup != "" {
		object.Object["spec"] = map[string]any{"template": map[string]any{"spec": map[string]any{"nodeClassRef": map[string]any{
			"group": classGroup, "kind": classKind, "name": className,
		}}}}
	}
	return object
}

func karpenterIssueTestObject(apiVersion, kind, name, readyStatus, reason, message string) *unstructured.Unstructured {
	condition := map[string]any{
		"type": "Ready", "status": readyStatus, "reason": reason, "observedGeneration": int64(1),
		"lastTransitionTime": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
	}
	if message != "" {
		condition["message"] = message
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   map[string]any{"name": name, "generation": int64(1), "creationTimestamp": time.Now().Add(-time.Hour).UTC().Format(time.RFC3339)},
		"status":     map[string]any{"conditions": []any{condition}},
	}}
}

func karpenterIssueTestClaim(name string, pool *unstructured.Unstructured, conditionType, status, reason, message string) *unstructured.Unstructured {
	claim := karpenterIssueTestObject(karpenter.APIVersionV1, karpenter.NodeClaimKind, name, "True", "Ready", "")
	claim.Object["status"] = map[string]any{"conditions": []any{map[string]any{
		"type": conditionType, "status": status, "reason": reason, "message": message, "observedGeneration": int64(1),
		"lastTransitionTime": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
	}}}
	claim.SetUID(types.UID(name + "-uid"))
	claim.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: karpenter.APIVersionV1,
		Kind:       karpenter.NodePoolKind,
		Name:       pool.GetName(),
		UID:        pool.GetUID(),
	}})
	return claim
}

func assertKarpenterIssueID(t *testing.T, issue Issue, issueSubject Ref) {
	t.Helper()
	want := subject.StableID(
		subject.ScopeForKind(issueSubject.Kind),
		resourceKey(issueSubject.Group, issueSubject.Kind, issueSubject.Namespace, issueSubject.Name),
		discriminator(&issue),
	)
	if issue.ID != want {
		t.Errorf("issue %q ID = %q, want StableID %q", issue.Reason, issue.ID, want)
	}
}

func TestComposeRedactsReferencedByNodePoolsForDeniedCallers(t *testing.T) {
	poolGVR := schema.GroupVersionResource{Group: karpenter.Group, Version: "v1", Resource: "nodepools"}
	classGVR := schema.GroupVersionResource{Group: "capacity.acme.io", Version: "v1", Resource: "fleetnodeclasses"}
	pool := karpenterIssueTestPool("compute", "capacity.acme.io", "FleetNodeClass", "general", "True", "Ready", "")
	class := karpenterIssueTestObject("capacity.acme.io/v1", "FleetNodeClass", "general", "False", "AuthFailed", "cloud credentials are invalid")
	p := newFakeKarpenterIssueProvider(map[schema.GroupVersionResource]struct {
		kind  string
		items []*unstructured.Unstructured
	}{
		poolGVR:  {kind: karpenter.NodePoolKind, items: []*unstructured.Unstructured{pool}},
		classGVR: {kind: "FleetNodeClass", items: []*unstructured.Unstructured{class}},
	})

	findClassIssue := func(issues []Issue) *Issue {
		for i := range issues {
			if issues[i].Kind == "FleetNodeClass" {
				return &issues[i]
			}
		}
		return nil
	}

	// Reader of both kinds keeps the referencing-NodePool context.
	allowed := findClassIssue(Compose(p, Filters{Limit: NoLimit, CanReadRelated: func(Ref) bool { return true }}))
	if allowed == nil || allowed.DiagnosticContext == nil || len(allowed.DiagnosticContext.Facts) == 0 {
		t.Fatalf("authorized caller lost the referenced-by context: %+v", allowed)
	}

	// NodeClass-readable / NodePool-denied caller keeps the issue but must not
	// receive NodePool names or their count.
	denyNodePools := func(ref Ref) bool {
		return !(ref.Kind == karpenter.NodePoolKind && ref.Group == karpenter.Group)
	}
	denied := findClassIssue(Compose(p, Filters{Limit: NoLimit, CanReadRelated: denyNodePools}))
	if denied == nil {
		t.Fatal("NodeClass issue itself must survive NodePool denial")
	}
	if denied.DiagnosticContext != nil {
		for _, fact := range denied.DiagnosticContext.Facts {
			if fact.Type == karpenterFactReferencedByNodePools {
				t.Fatalf("referenced-by NodePool fact leaked to a denied caller: %+v", fact)
			}
		}
	}
}
