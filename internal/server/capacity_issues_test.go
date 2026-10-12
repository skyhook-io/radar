package server

import (
	"testing"
	"time"

	capacitymodel "github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/issues"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/subject"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestCapacityVisibleIssuesKeepsClusterAndAuthorizedNamespaces(t *testing.T) {
	in := []issues.Issue{
		{ID: "cluster", Kind: "NodePool", Name: "default"},
		{ID: "allowed", Kind: "Pod", Namespace: "team-a", Name: "pending"},
		{ID: "hidden", Kind: "Pod", Namespace: "team-b", Name: "private"},
	}

	allowCluster := func(string, string) bool { return true }
	if got := capacityIssueIDs(capacityVisibleIssues(in, nil, allowCluster, nil, nil, nil)); !sameCapacityIssueIDs(got, "cluster", "allowed", "hidden") {
		t.Fatalf("unrestricted issue IDs = %v", got)
	}
	if got := capacityIssueIDs(capacityVisibleIssues(in, []string{"team-a"}, allowCluster, nil, nil, nil)); !sameCapacityIssueIDs(got, "cluster", "allowed") {
		t.Fatalf("team-a issue IDs = %v", got)
	}
	if got := capacityIssueIDs(capacityVisibleIssues(in, []string{}, allowCluster, nil, nil, nil)); !sameCapacityIssueIDs(got, "cluster") {
		t.Fatalf("no-namespace-access issue IDs = %v", got)
	}
	if got := capacityIssueIDs(capacityVisibleIssues(in, nil, func(string, string) bool { return false }, nil, nil, nil)); !sameCapacityIssueIDs(got, "allowed", "hidden") {
		t.Fatalf("cluster-denied issue IDs = %v", got)
	}
}

func TestCapacityVisibleIssuesRequiresReferencedNodeClassAccess(t *testing.T) {
	issue := issues.Issue{
		ID: "missing-class", Group: "karpenter.sh", Kind: "NodePool", Name: "compute",
		DiagnosticContext: &issuesapi.DiagnosticContext{Facts: []issuesapi.DiagnosticFact{{
			Type: "karpenter_referenced_nodeclass",
			Refs: []issuesapi.Ref{{Group: "karpenter.k8s.aws", Kind: "EC2NodeClass", Name: "missing"}},
		}}},
	}
	allowCluster := func(string, string) bool { return true }
	if got := capacityVisibleIssues([]issues.Issue{issue}, nil, allowCluster, nil, func(issues.Ref) bool { return false }, nil); len(got) != 0 {
		t.Fatalf("denied NodeClass leaked through NodePool issue: %+v", got)
	}
	if got := capacityVisibleIssues([]issues.Issue{issue}, nil, allowCluster, nil, func(issues.Ref) bool { return true }, nil); len(got) != 1 {
		t.Fatalf("authorized NodeClass issue omitted: %+v", got)
	}
}

func TestCapacityIssueProjectionAssociatesPoolNodeClassAndClaims(t *testing.T) {
	poolRef := subject.Ref{Group: "karpenter.sh", Kind: "NodePool", Name: "default"}
	nodeClassRef := subject.Ref{Group: "example.io", Kind: "CustomNodeClass", Name: "shared"}
	claimRef := subject.Ref{Group: "karpenter.sh", Kind: "NodeClaim", Name: "default-abc"}
	model := &capacitymodel.Model{Pools: []capacitymodel.PoolModel{{
		Observation: capacityapi.PoolObservation{
			Resource: capacityapi.ResourceIdentity{Ref: poolRef},
			NodeClass: &capacityapi.NodeClassObservation{Reference: capacityapi.NodeClassReference{
				Group: nodeClassRef.Group, Kind: nodeClassRef.Kind, Name: nodeClassRef.Name,
			}},
			Issues: []issuesapi.Issue{},
		},
		Claims: []capacityapi.PoolMember{{Resource: capacityapi.ResourceIdentity{Ref: claimRef}}},
	}}}
	flat := []issues.Issue{
		{ID: "pool", Severity: issues.SeverityWarning, Group: poolRef.Group, Kind: poolRef.Kind, Name: poolRef.Name},
		{ID: "class", Severity: issues.SeverityWarning, Group: nodeClassRef.Group, Kind: nodeClassRef.Kind, Name: nodeClassRef.Name},
		{ID: "claim", Severity: issues.SeverityWarning, Group: claimRef.Group, Kind: claimRef.Kind, Name: claimRef.Name, Owner: issues.Ref(poolRef)},
		{ID: "other", Severity: issues.SeverityWarning, Group: "karpenter.sh", Kind: "NodePool", Name: "other"},
	}

	newCapacityIssueProjection(flat, true).attachPools(model)
	got := capacityIssueIDs(model.Pools[0].Observation.Issues)
	if !sameCapacityIssueIDs(got, "pool", "class", "claim") {
		t.Fatalf("pool issue IDs = %v", got)
	}
	if summaries := model.Summaries(); len(summaries) != 1 || summaries[0].IssueCount != 3 {
		t.Fatalf("pool summaries = %+v", summaries)
	}
}

func TestCapacityIssueProjectionAttachesOnlyCapacityRelevantDemandIssues(t *testing.T) {
	visibleOwner := subject.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: "shop", Name: "checkout-abc"}
	hiddenOwner := subject.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "checkout"}
	pods := []*corev1.Pod{
		{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout-a"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}},
		{ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "checkout-b"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}},
	}
	groups := capacitymodel.BuildDemandGroupModels(capacitymodel.DemandInput{
		GeneratedAt: time.Now(),
		Pods:        pods,
		PodRefLimit: 1,
		ResolvePodOwner: func(*corev1.Pod) *subject.Ref {
			copy := visibleOwner
			return &copy
		},
	})
	flat := []issues.Issue{
		{ID: "capacity", Severity: issues.SeverityWarning, Kind: "Pod", Namespace: "shop", Name: "checkout-b", Owner: issues.Ref(hiddenOwner), CapacityRelevant: true, DiagnosticContext: &issuesapi.DiagnosticContext{}},
		{ID: "runtime", Severity: issues.SeverityWarning, Kind: "Pod", Namespace: "shop", Name: "checkout-a", Owner: issues.Ref(hiddenOwner)},
	}

	newCapacityIssueProjection(flat, true).attachDemand(groups)
	if len(groups[0].Group.Issues) != 1 || !groups[0].Group.Issues[0].CapacityRelevant {
		t.Fatalf("demand issues = %#v", groups[0].Group.Issues)
	}
	issue := groups[0].Group.Issues[0]
	if issue.Kind != visibleOwner.Kind || issue.Name != visibleOwner.Name || issue.DiagnosticContext != nil {
		t.Fatalf("demand issue exposed unapproved owner context: %#v", issue)
	}
	if issue.ID == "capacity" {
		t.Fatalf("demand issue retained identity derived from the hidden owner: %#v", issue)
	}
	if groups[0].Group.PodsMeta.Returned != 1 || groups[0].Group.PodsMeta.Total != 2 {
		t.Fatalf("test did not exercise a pod beyond the inline limit: %#v", groups[0].Group.PodsMeta)
	}
}

func TestCapacityIssueProjectionRebindsIdentityWhenDemandHasNoVisibleOwner(t *testing.T) {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "shop", Name: "orphan"},
		Spec:       corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}},
	}
	groups := capacitymodel.BuildDemandGroupModels(capacitymodel.DemandInput{
		GeneratedAt: time.Now(),
		Pods:        []*corev1.Pod{pod},
	})
	flat := []issues.Issue{{
		ID: "hidden-owner-id", Severity: issues.SeverityWarning,
		Kind: "Pod", Namespace: "shop", Name: "orphan",
		Owner:            issues.Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "hidden"},
		CapacityRelevant: true,
	}}

	newCapacityIssueProjection(flat, true).attachDemand(groups)
	if len(groups[0].Group.Issues) != 1 {
		t.Fatalf("demand issues = %#v", groups[0].Group.Issues)
	}
	issue := groups[0].Group.Issues[0]
	if issue.Kind != "Pod" || issue.Name != "orphan" || issue.ID == "hidden-owner-id" {
		t.Fatalf("ownerless demand issue retained hidden subject identity: %#v", issue)
	}
}

func TestCapacityIssueMemoCoalescesAndClears(t *testing.T) {
	memo := newCapacityIssueMemo(time.Minute)
	builds := 0
	build := func() []issues.Issue {
		builds++
		return []issues.Issue{{ID: "snapshot"}}
	}
	for range 2 {
		if got, valid := memo.load("cluster-a", nil, nil, nil, build, func() bool { return true }); len(got) != 1 || !valid {
			t.Fatalf("memo load = %#v", got)
		}
	}
	if builds != 1 {
		t.Fatalf("builds = %d, want 1", builds)
	}
	memo.clear()
	if got, valid := memo.load("cluster-a", nil, nil, nil, build, func() bool { return true }); len(got) != 1 || !valid || builds != 2 {
		t.Fatalf("clear did not invalidate memo; builds = %d", builds)
	}
}

func TestCapacityIssueMemoDoesNotStoreInvalidatedBuild(t *testing.T) {
	memo := newCapacityIssueMemo(time.Minute)
	builds := 0
	build := func() []issues.Issue {
		builds++
		return []issues.Issue{{ID: "snapshot"}}
	}
	if got, valid := memo.load("cluster-a", nil, nil, nil, build, func() bool { return false }); valid || got != nil {
		t.Fatalf("invalidated build returned cacheable data: valid=%v got=%#v", valid, got)
	}
	if got, valid := memo.load("cluster-a", nil, nil, nil, build, func() bool { return true }); !valid || len(got) != 1 || builds != 2 {
		t.Fatalf("invalidated build was cached: valid=%v builds=%d got=%#v", valid, builds, got)
	}
}

func capacityIssueIDs(in []issuesapi.Issue) []string {
	out := make([]string, 0, len(in))
	for _, issue := range in {
		out = append(out, issue.ID)
	}
	return out
}

func sameCapacityIssueIDs(got []string, want ...string) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[string]bool, len(got))
	for _, id := range got {
		seen[id] = true
	}
	for _, id := range want {
		if !seen[id] {
			return false
		}
	}
	return true
}

func TestCapacityVisibleIssuesAuthorizesEvidence(t *testing.T) {
	allowCluster := func(string, string) bool { return true }
	issue := issues.Issue{ID: "derived", Kind: "NodePool", Group: "karpenter.sh", Name: "default", RequiredReads: []issues.EvidenceRead{
		{Resource: "pods", Namespace: "team-a", Verb: "list"},
	}}
	if got := capacityVisibleIssues([]issues.Issue{issue}, nil, allowCluster, nil, nil, nil); len(got) != 0 {
		t.Fatalf("a finding with inventory dependencies passed without an authorizer: %+v", got)
	}
	if got := capacityVisibleIssues([]issues.Issue{issue}, nil, allowCluster, nil, nil, func(issues.EvidenceRead) bool { return false }); len(got) != 0 {
		t.Fatalf("denied evidence passed: %+v", got)
	}
	if got := capacityVisibleIssues([]issues.Issue{issue}, nil, allowCluster, nil, nil, func(issues.EvidenceRead) bool { return true }); len(got) != 1 {
		t.Fatalf("authorized evidence withheld: %+v", got)
	}
	issue.Message = "pods team-a/a and team-a/b are pending"
	issue.Degraded = &issues.DegradedFinding{Message: "pods are pending"}
	if got := capacityVisibleIssues([]issues.Issue{issue}, nil, allowCluster, nil, nil, func(issues.EvidenceRead) bool { return false }); len(got) != 1 || got[0].Message != "pods are pending" {
		t.Fatalf("a finding with a degraded form = %+v, want it degraded", got)
	}
}

// The memo is composed for every user, so an owner the caller can't read is
// cleared at read time, as Compose does, rather than becoming the grouped issue.
func TestCapacityVisibleIssuesClearsUnreadableOwners(t *testing.T) {
	mirror := issues.Issue{ID: "mirror", Kind: "Pod", Namespace: "kube-system", Name: "kube-proxy-node-1", Owner: issues.Ref{Kind: "Node", Name: "node-1"}}
	denyNodes := func(kind, _ string) bool { return kind != "Node" }
	got := capacityVisibleIssues([]issues.Issue{mirror}, nil, denyNodes, nil, nil, nil)
	if len(got) != 1 || got[0].Owner.Kind != "" {
		t.Fatalf("unreadable Node owner kept: %+v", got)
	}
	if grouped := issues.GroupIssues(got); len(grouped) != 1 || grouped[0].Kind != "Pod" {
		t.Fatalf("grouped subject = %+v, want the Pod", grouped)
	}
	if got := capacityVisibleIssues([]issues.Issue{mirror}, nil, func(string, string) bool { return true }, nil, nil, nil); got[0].Owner.Kind != "Node" {
		t.Fatalf("readable Node owner cleared: %+v", got)
	}
}

func TestCapacityVisibleIssuesChecksOwnersLikeCompose(t *testing.T) {
	pending := issues.Issue{ID: "pending", Kind: "Pod", Namespace: "team-a", Name: "worker", Owner: issues.Ref{Kind: "Secret", Namespace: "team-a", Name: "parent"}, CapacityRelevant: true}
	allowCluster := func(string, string) bool { return true }
	denySecrets := func(ref issues.Ref) bool { return ref.Kind != "Secret" }
	got := capacityVisibleIssues([]issues.Issue{pending}, []string{"team-a"}, allowCluster, denySecrets, nil, nil)
	if len(got) != 1 || got[0].Owner.Kind != "" {
		t.Fatalf("an owner of a kind the caller can't get was kept: %+v", got)
	}
	if grouped := issues.GroupIssues(got); len(grouped) != 1 || grouped[0].Kind != "Pod" {
		t.Fatalf("grouped subject = %+v, want the Pod", grouped)
	}
}
