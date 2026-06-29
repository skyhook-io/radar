package issues

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/subject"
)

// ScopeForKind + IssueID determinism/keying now live in (and are tested by)
// pkg/subject — issues consumes them via enrichIdentity. This test pins the
// issues-level behavior: enrichIdentity derives the owner-else-self subject and
// keys the ID off it, using the shared resolver.
func TestEnrichIdentity_SubjectIsOwnerElseSelf(t *testing.T) {
	// A pod with a resolved owner groups under the owner — the ID is keyed on
	// the workload, not the pod. The expected value uses subject.StableID (what
	// enrichIdentity now calls), confirming the migration re-keys nothing.
	pod := Issue{
		Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "web-abc-1", Reason: "ImagePullBackOff",
		Owner: Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"},
	}
	classifyIssue(&pod)
	enrichIdentity(&pod)
	if pod.GroupingScope != issuesapi.ScopeWorkload {
		t.Errorf("scope = %q, want workload", pod.GroupingScope)
	}
	if want := subject.StableID(subject.Scope(issuesapi.ScopeWorkload), resourceKey("apps", "Deployment", "ns", "web"), string(issuesapi.CategoryImagePullFailed)); pod.ID != want {
		t.Errorf("ID = %q, want owner-keyed %q", pod.ID, want)
	}

	// A standalone pod (no owner) is its own subject.
	solo := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "solo", Reason: "CrashLoopBackOff"}
	classifyIssue(&solo)
	enrichIdentity(&solo)
	if want := subject.StableID(subject.Scope(issuesapi.ScopeWorkload), resourceKey("", "Pod", "ns", "solo"), string(issuesapi.CategoryCrashLoop)); solo.ID != want {
		t.Errorf("standalone pod ID = %q, want self-keyed %q", solo.ID, want)
	}
}

// TestEnrichIdentity_DistinctCausesDoNotCollapse pins that two distinct causes
// on the same subject+category (a workload missing both a ConfigMap and a
// Secret — both missing_config_ref) get DISTINCT ids via the stable
// Fingerprint, instead of collapsing to one row; same cause folds; and a
// single-cause category stays category-keyed (no re-key).
func TestEnrichIdentity_DistinctCausesDoNotCollapse(t *testing.T) {
	owner := Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}
	mk := func(reason, fp string) Issue {
		i := Issue{Source: SourceMissingRef, Kind: "Pod", Namespace: "ns", Name: "web-x", Reason: reason, Owner: owner, Fingerprint: fp}
		classifyIssue(&i)
		enrichIdentity(&i)
		return i
	}
	cm := mk("Missing ConfigMap", `Missing ConfigMap|references ConfigMap "foo"`)
	sec := mk("Missing Secret", `Missing Secret|references Secret "bar"`)
	if cm.Category != issuesapi.CategoryMissingConfigRef || sec.Category != issuesapi.CategoryMissingConfigRef {
		t.Fatalf("precondition: both should classify missing_config_ref, got %q/%q", cm.Category, sec.Category)
	}
	if cm.ID == sec.ID {
		t.Errorf("distinct missing-ref causes must get distinct IDs, both = %q", cm.ID)
	}
	if cm2 := mk("Missing ConfigMap", `Missing ConfigMap|references ConfigMap "foo"`); cm.ID != cm2.ID {
		t.Errorf("same cause must fold to one ID: %q vs %q", cm.ID, cm2.ID)
	}

	// A single-cause category (no fingerprint) stays category-only keyed — the
	// bulk of issues must NOT re-key from this change.
	cl := Issue{Source: SourceProblem, Kind: "Pod", Namespace: "ns", Name: "web-x", Reason: "CrashLoopBackOff", Owner: owner}
	classifyIssue(&cl)
	enrichIdentity(&cl)
	if want := subject.StableID(subject.Scope(issuesapi.ScopeWorkload), resourceKey("apps", "Deployment", "ns", "web"), string(issuesapi.CategoryCrashLoop)); cl.ID != want {
		t.Errorf("single-cause category must stay category-keyed (no re-key): %q want %q", cl.ID, want)
	}
}
