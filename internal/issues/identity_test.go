package issues

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/subject"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
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

func TestRollbackOutcomeKeepsDistinctIdentity(t *testing.T) {
	t.Cleanup(k8s.ResetTestDynamicState)
	now := time.Now().UTC().Truncate(time.Second)
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1", "kind": "Application",
		"metadata": map[string]any{"name": "demo", "namespace": "argocd"},
		"status": map[string]any{
			"health": map[string]any{"status": "Healthy"},
			"sync":   map[string]any{"status": "Synced"},
			"operationState": map[string]any{
				"phase": "Succeeded", "finishedAt": now.Add(-time.Minute).Format(time.RFC3339),
				"operation": map[string]any{
					"info": []any{map[string]any{"name": "radarhq.io/rollback", "value": "true"}},
					"sync": map[string]any{"revision": "requested"},
				},
				"syncResult": map[string]any{"revision": "applied"},
			},
		},
	}}
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ApplicationList"}, app)
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Application", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, 2*time.Second) {
		t.Fatalf("Application cache did not sync: %v", err)
	}
	detections := k8s.DetectGitOpsProblems(cache, k8s.GetResourceDiscovery(), "argocd")
	if len(detections) != 1 || detections[0].Reason != "RollbackRevisionMismatch" {
		t.Fatalf("want rollback mismatch detection, got %+v", detections)
	}
	rollback := fromProblem(detections[0], now, SourceProblem)
	if rollback.Category != issuesapi.CategoryGitOpsOperationFailed || rollback.Cause == "" || rollback.Action == "" {
		t.Fatalf("rollback category or diagnosis missing: %+v", rollback)
	}
	for _, reason := range []string{"SyncError", "StuckDriftLoop", "OperationFailed"} {
		t.Run(reason, func(t *testing.T) {
			current := fromProblem(k8s.Detection{
				Kind: "Application", Group: gvr.Group, Namespace: "argocd", Name: "demo",
				Reason: reason, Severity: "critical", Cause: "current failure", Action: "fix current failure",
			}, now, SourceProblem)
			if current.Category != rollback.Category {
				t.Fatalf("want same category, got %q / %q", current.Category, rollback.Category)
			}
			if current.ID == rollback.ID {
				t.Errorf("rollback and %s share issue ID %q", reason, rollback.ID)
			}
			grouped := GroupIssues([]Issue{rollback, current})
			if len(grouped) != 2 {
				t.Fatalf("rollback and %s collapsed into %d groups: %+v", reason, len(grouped), grouped)
			}
			for _, issue := range grouped {
				if issue.ID == rollback.ID && (issue.Reason != rollback.Reason || issue.Severity != SeverityWarning || issue.Cause != rollback.Cause || issue.Action != rollback.Action) {
					t.Errorf("grouping changed rollback warning or recovery advice: %+v", issue)
				}
			}
		})
	}
	later := k8s.DetectGitOpsProblems(cache, k8s.GetResourceDiscovery(), "argocd")
	if len(later) != 1 {
		t.Fatalf("want one rollback detection on next poll, got %+v", later)
	}
	assertSameID(t, rollback, fromProblem(later[0], now.Add(time.Hour), SourceProblem))
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
