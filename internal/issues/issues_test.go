package issues

import (
	"sort"
	"strings"
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes/fake"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// fakeProvider — minimal Provider for unit testing. Each field
// pre-stages what the corresponding method returns. Test cases assemble
// one of these and pass it to Compose.
type fakeProvider struct {
	problems        []k8s.Detection
	missingRefs     []k8s.Detection
	scheduling      []k8s.Detection
	capiProblems    []k8s.Detection
	gitopsProblems  []k8s.Detection
	dynamic         map[schema.GroupVersionResource][]*unstructured.Unstructured
	kinds           map[schema.GroupVersionResource]string
	namespaced      map[schema.GroupVersionResource]bool
	selectedPods    map[string][]Ref
	podsOnNode      map[string][]Ref
	podsMountingPVC map[string][]Ref
	secretProducer  map[string]secretProducerResult
	change          map[string]*issuesapi.ChangeContext
}

type secretProducerResult struct {
	name string
	pods []Ref
}

func (f *fakeProvider) DetectProblems(_ []string) []k8s.Detection       { return f.problems }
func (f *fakeProvider) DetectMissingRefs(_ []string) []k8s.Detection    { return f.missingRefs }
func (f *fakeProvider) DetectScheduling(_ []string) []k8s.Detection     { return f.scheduling }
func (f *fakeProvider) DetectCAPIProblems(_ []string) []k8s.Detection   { return f.capiProblems }
func (f *fakeProvider) DetectGitOpsProblems(_ []string) []k8s.Detection { return f.gitopsProblems }
func (f *fakeProvider) WatchedDynamic() []schema.GroupVersionResource {
	out := make([]schema.GroupVersionResource, 0, len(f.dynamic))
	for g := range f.dynamic {
		out = append(out, g)
	}
	return out
}
func (f *fakeProvider) ListDynamic(gvr schema.GroupVersionResource, _ string) ([]*unstructured.Unstructured, error) {
	return f.dynamic[gvr], nil
}
func (f *fakeProvider) ListDynamicAllNamespaces(gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	return f.dynamic[gvr], nil
}
func (f *fakeProvider) KindForGVR(gvr schema.GroupVersionResource) string {
	return f.kinds[gvr]
}
func (f *fakeProvider) NamespacedForGVR(gvr schema.GroupVersionResource) (bool, bool) {
	namespaced, ok := f.namespaced[gvr]
	return namespaced, ok
}
func (f *fakeProvider) SelectedPodsForService(namespace, name string) []Ref {
	return f.selectedPods[namespace+"/"+name]
}
func (f *fakeProvider) PodsMountingPVC(namespace, pvcName string) []Ref {
	return f.podsMountingPVC[namespace+"/"+pvcName]
}
func (f *fakeProvider) PodsOnNode(nodeName string) []Ref {
	return f.podsOnNode[nodeName]
}
func (f *fakeProvider) PodsDependingOnSecretProducer(_, _, namespace, name string) (string, []Ref) {
	r := f.secretProducer[namespace+"/"+name]
	return r.name, r.pods
}

func (f *fakeProvider) ChangeContextForIssue(i Issue) *issuesapi.ChangeContext {
	if f.change == nil {
		return nil
	}
	return f.change[resourceKey(i.Group, i.Kind, i.Namespace, i.Name)]
}

func TestCompose_NormalizesProblemSeverity(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Deployment", Namespace: "ns", Name: "a", Severity: "critical", Reason: "down"},
			{Kind: "Deployment", Namespace: "ns", Name: "b", Severity: "high", Reason: "slow"},
			{Kind: "Deployment", Namespace: "ns", Name: "c", Severity: "medium", Reason: "warn"},
			// "alert" is the intermediate insights tier; it must map UP to
			// critical, not down to warning (a future alert-emitting detector
			// must not be silently downgraded).
			{Kind: "Deployment", Namespace: "ns", Name: "d", Severity: "alert", Reason: "stuck"},
		},
	}
	out := Compose(p, Filters{})
	if len(out) != 4 {
		t.Fatalf("got %d issues", len(out))
	}
	bySev := map[Severity]int{}
	for _, i := range out {
		bySev[i.Severity]++
	}
	if bySev[SeverityCritical] != 2 || bySev[SeverityWarning] != 2 {
		t.Fatalf("severity normalization wrong (alert→critical, high/medium→warning): %+v", bySev)
	}
}

func TestCompose_PopulatesCategoryAndGroup(t *testing.T) {
	// Every composed row carries the derived symptom category + its rollup
	// group, classified from the detection signal across all sources.
	p := &fakeProvider{
		problems:    []k8s.Detection{{Kind: "Pod", Namespace: "ns", Name: "img", Severity: "high", Reason: "ImagePullBackOff"}},
		scheduling:  []k8s.Detection{{Kind: "Pod", Namespace: "ns", Name: "sched", Severity: "high", Reason: "Unschedulable"}},
		missingRefs: []k8s.Detection{{Kind: "Pod", Namespace: "ns", Name: "ref", Severity: "high", Reason: "Missing ConfigMap"}},
	}
	got := map[string]Issue{}
	for _, i := range Compose(p, Filters{}) {
		got[i.Name] = i
	}
	checks := []struct {
		name     string
		category issuesapi.Category
		group    issuesapi.CategoryGroup
	}{
		{"img", issuesapi.CategoryImagePullFailed, issuesapi.GroupStartup},
		{"sched", issuesapi.CategoryUnschedulable, issuesapi.GroupScheduling},
		{"ref", issuesapi.CategoryMissingConfigRef, issuesapi.GroupConfiguration},
	}
	for _, c := range checks {
		if got[c.name].Category != c.category || got[c.name].CategoryGroup != c.group {
			t.Errorf("%s: category=%q group=%q, want %q/%q",
				c.name, got[c.name].Category, got[c.name].CategoryGroup, c.category, c.group)
		}
	}
}

func TestCompose_ClassifiesArgoWorkflowForbiddenMessage(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{{
			Kind:       "Pod",
			Namespace:  "ns",
			Name:       "daily-import-step-123",
			Severity:   "critical",
			Reason:     "Completed",
			Message:    `workflowtaskresults.argoproj.io is forbidden: User "system:serviceaccount:ns:default" cannot create resource "workflowtaskresults" in API group "argoproj.io" in the namespace "ns"`,
			OwnerKind:  "Workflow",
			OwnerName:  "daily-import",
			OwnerGroup: "argoproj.io",
		}},
	}
	out := Compose(p, Filters{Grouped: true})
	if len(out) != 1 {
		t.Fatalf("got %d issues: %+v", len(out), out)
	}
	if out[0].Kind != "Workflow" || out[0].Name != "daily-import" || out[0].Group != "argoproj.io" {
		t.Fatalf("subject = %s/%s/%s, want argoproj.io/Workflow/daily-import", out[0].Group, out[0].Kind, out[0].Name)
	}
	if out[0].Reason != "RBACForbidden" {
		t.Fatalf("reason = %q, want RBACForbidden", out[0].Reason)
	}
	if out[0].Category != issuesapi.CategoryRBACForbidden || out[0].CategoryGroup != issuesapi.GroupSecurity {
		t.Fatalf("category = %q/%q, want %q/%q", out[0].Category, out[0].CategoryGroup, issuesapi.CategoryRBACForbidden, issuesapi.GroupSecurity)
	}
}

func TestCompose_PreservesExplicitJobFailureOverEmbeddedForbiddenMessage(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{{
			Kind:      "Job",
			Namespace: "ns",
			Name:      "daily-import",
			Severity:  "critical",
			Reason:    "BackoffLimitExceeded",
			Message:   `workflowtaskresults.argoproj.io is forbidden: User "system:serviceaccount:ns:default" cannot create resource "workflowtaskresults"`,
		}},
	}
	out := Compose(p, Filters{})
	if len(out) != 1 {
		t.Fatalf("got %d issues: %+v", len(out), out)
	}
	if out[0].Reason != "BackoffLimitExceeded" {
		t.Fatalf("reason = %q, want BackoffLimitExceeded", out[0].Reason)
	}
	if out[0].Category != issuesapi.CategoryJobFailed {
		t.Fatalf("category = %q, want %q", out[0].Category, issuesapi.CategoryJobFailed)
	}
}

func TestCompose_GroupsMemberPodsUnderOwner(t *testing.T) {
	// Two pods of the same Deployment failing the same way share one issue
	// ID (the future collapse target); a third pod failing differently gets
	// its own. Owner + scope are propagated onto every member row.
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Namespace: "ns", Name: "web-a", Severity: "critical", Reason: "ImagePullBackOff", OwnerKind: "Deployment", OwnerName: "web"},
			{Kind: "Pod", Namespace: "ns", Name: "web-b", Severity: "critical", Reason: "ImagePullBackOff", OwnerKind: "Deployment", OwnerName: "web"},
			{Kind: "Pod", Namespace: "ns", Name: "web-c", Severity: "critical", Reason: "CrashLoopBackOff", OwnerKind: "Deployment", OwnerName: "web"},
		},
	}
	got := map[string]Issue{}
	for _, i := range Compose(p, Filters{}) {
		got[i.Name] = i
	}
	if got["web-a"].ID == "" || got["web-a"].ID != got["web-b"].ID {
		t.Errorf("same owner+category pods must share an ID: a=%q b=%q", got["web-a"].ID, got["web-b"].ID)
	}
	if got["web-c"].ID == got["web-a"].ID {
		t.Error("a different category under the same owner must get a distinct ID")
	}
	if want := (Ref{Group: "apps", Kind: "Deployment", Namespace: "ns", Name: "web"}); got["web-a"].Owner != want {
		t.Errorf("owner not propagated: got %+v, want %+v", got["web-a"].Owner, want)
	}
	if got["web-a"].GroupingScope != issuesapi.ScopeWorkload {
		t.Errorf("scope = %q, want workload", got["web-a"].GroupingScope)
	}
}

func TestCompose_GroupedKindFilterMatchesSubject(t *testing.T) {
	// A crashlooping Deployment is evidenced by Pod rows that fold under the
	// Deployment subject. On the GROUPED surface, kind=Deployment must return
	// that issue (the public filter sees the subject), and kind=Pod must NOT —
	// filtering the flat Pod evidence before grouping would invert both.
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Namespace: "ns", Name: "web-a", Severity: "critical", Reason: "CrashLoopBackOff", OwnerGroup: "apps", OwnerKind: "Deployment", OwnerName: "web"},
			{Kind: "Pod", Namespace: "ns", Name: "web-b", Severity: "critical", Reason: "CrashLoopBackOff", OwnerGroup: "apps", OwnerKind: "Deployment", OwnerName: "web"},
		},
	}
	out := Compose(p, Filters{Grouped: true, Kinds: []string{"Deployment"}})
	if len(out) != 1 {
		t.Fatalf("kind=Deployment (grouped) must match the pod-evidenced Deployment issue, got %d: %+v", len(out), out)
	}
	if out[0].Kind != "Deployment" || out[0].Name != "web" {
		t.Errorf("subject = %s/%s, want Deployment/web", out[0].Kind, out[0].Name)
	}
	if out[0].Count != 2 {
		t.Errorf("count = %d, want 2 (both pods folded)", out[0].Count)
	}
	if got := Compose(p, Filters{Grouped: true, Kinds: []string{"Pod"}}); len(got) != 0 {
		t.Errorf("kind=Pod (grouped) must NOT match a Deployment-subject issue, got %d", len(got))
	}
}

func TestCompose_GroupedDiagnosisRequiresEveryMemberToAgree(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{
				Kind: "Pod", Namespace: "ns", Name: "web-a", Severity: "critical", Reason: "CrashLoopBackOff",
				OwnerKind: "Deployment", OwnerName: "web",
				Cause: "command not found", Action: "fix the container command",
			},
			{Kind: "Pod", Namespace: "ns", Name: "web-b", Severity: "critical", Reason: "CrashLoopBackOff", OwnerKind: "Deployment", OwnerName: "web"},
		},
	}
	out := Compose(p, Filters{Grouped: true})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(out), out)
	}
	if out[0].Cause != "" || out[0].Action != "" {
		t.Fatalf("mixed parsed/unparsed rollup must omit diagnosis, got cause=%q action=%q", out[0].Cause, out[0].Action)
	}
}

func TestCompose_GroupedDiagnosisKeepsIdenticalMemberDiagnosis(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{
				Kind: "Pod", Namespace: "ns", Name: "web-a", Severity: "critical", Reason: "CrashLoopBackOff",
				OwnerKind: "Deployment", OwnerName: "web",
				Cause: "command not found", Action: "fix the container command",
			},
			{
				Kind: "Pod", Namespace: "ns", Name: "web-b", Severity: "critical", Reason: "CrashLoopBackOff",
				OwnerKind: "Deployment", OwnerName: "web",
				Cause: "command not found", Action: "fix the container command",
			},
		},
	}
	out := Compose(p, Filters{Grouped: true})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(out), out)
	}
	if out[0].Cause != "command not found" || out[0].Action != "fix the container command" {
		t.Fatalf("identical member diagnosis should be promoted, got cause=%q action=%q", out[0].Cause, out[0].Action)
	}
}

func TestCompose_GroupedSingleIssueKeepsDiagnosis(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{{
			Kind: "Pod", Namespace: "ns", Name: "standalone", Severity: "critical", Reason: "CrashLoopBackOff",
			Cause: "command not found", Action: "fix the container command",
		}},
	}
	out := Compose(p, Filters{Grouped: true})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(out), out)
	}
	if out[0].Cause == "" || out[0].Action == "" {
		t.Fatalf("single-resource issue should keep diagnosis, got %+v", out[0])
	}
}

func TestCompose_DropsInfoSeverityFromQueue(t *testing.T) {
	// info-severity problems are inert/posture (deprecated-RBAC residue,
	// singleton-StatefulSet headless-DNS trivia) — excluded from the live issue
	// stream, which stays critical|warning.
	p := &fakeProvider{
		missingRefs: []k8s.Detection{
			{Kind: "StatefulSet", Group: "apps", Namespace: "ns", Name: "inert", Severity: "info", Reason: "Missing headless Service"},
			{Kind: "Pod", Namespace: "ns", Name: "real", Severity: "critical", Reason: "Missing ConfigMap"},
		},
	}
	out := Compose(p, Filters{})
	if len(out) != 1 || out[0].Name != "real" {
		t.Fatalf("only the critical row should surface; info must be dropped. got %+v", out)
	}
}

func TestCompose_PodSchedulingWinsOverProblem(t *testing.T) {
	// A pod stuck post-bind trips both sources: DetectProblems flags it
	// Pending>5m and DetectScheduling names the actual CNI/volume blocker.
	// The scheduling row is richer, so the generic problem row for the SAME
	// pod must be dropped — without collapsing unrelated rows.
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Namespace: "ns", Name: "stuck", Severity: "high", Reason: "Pending"},
			{Kind: "Pod", Namespace: "ns", Name: "other", Severity: "high", Reason: "CrashLoopBackOff"},
			{Kind: "Deployment", Namespace: "ns", Name: "stuck", Severity: "critical", Reason: "down"},
		},
		scheduling: []k8s.Detection{
			{Kind: "Pod", Namespace: "ns", Name: "stuck", Severity: "high", Reason: "VolumeMount"},
		},
	}
	out := Compose(p, Filters{})

	var stuckPodRows []Issue
	for _, i := range out {
		if i.Kind == "Pod" && i.Name == "stuck" {
			stuckPodRows = append(stuckPodRows, i)
		}
	}
	if len(stuckPodRows) != 1 {
		t.Fatalf("expected exactly 1 row for Pod ns/stuck (scheduling wins), got %d: %+v", len(stuckPodRows), out)
	}
	if stuckPodRows[0].Source != SourceScheduling || stuckPodRows[0].Reason != "VolumeMount" {
		t.Errorf("the surviving Pod row should be the scheduling one, got %+v", stuckPodRows[0])
	}
	// The unrelated problem-source pod and the same-name Deployment must
	// survive — dedup keys on (source=problem, kind=Pod, ns/name) only.
	var sawOtherPod, sawDeploy bool
	for _, i := range out {
		if i.Kind == "Pod" && i.Name == "other" {
			sawOtherPod = true
		}
		if i.Kind == "Deployment" && i.Name == "stuck" {
			sawDeploy = true
		}
	}
	if !sawOtherPod {
		t.Errorf("unrelated problem-source Pod must not be dropped: %+v", out)
	}
	if !sawDeploy {
		t.Errorf("same-name Deployment must not be dropped by Pod dedup: %+v", out)
	}
}

func TestCompose_SuppressesWorkloadDegradedWhenChildSymptomExists(t *testing.T) {
	// A degraded Deployment surfaces the parent workload_degraded row AND its
	// crashlooping pods. The parent rollup is redundant when a
	// specific child symptom names the root cause on the same subject — keep
	// the crashloop, drop the workload_degraded.
	p := &fakeProvider{
		problems: []k8s.Detection{
			// Parent rollup on the Deployment itself.
			{Kind: "Deployment", Namespace: "ns", Name: "web", Group: "apps", Severity: "critical", Reason: "1/3 available"},
			// Child symptom on a member pod, owned by the same Deployment.
			{Kind: "Pod", Namespace: "ns", Name: "web-abc", Severity: "critical", Reason: "CrashLoopBackOff", OwnerKind: "Deployment", OwnerName: "web"},
		},
	}
	out := Compose(p, Filters{})

	var sawDegraded, sawCrashloop bool
	for _, i := range out {
		if i.Category == issuesapi.CategoryWorkloadDegraded {
			sawDegraded = true
		}
		if i.Category == issuesapi.CategoryCrashLoop {
			sawCrashloop = true
		}
	}
	if sawDegraded {
		t.Errorf("workload_degraded must be suppressed when a child symptom exists: %+v", out)
	}
	if !sawCrashloop {
		t.Errorf("the specific child crashloop row must survive: %+v", out)
	}
}

func TestCompose_SuppressedParentDonatesIssueTimingToChildren(t *testing.T) {
	// A suppressed parent rollup may be the subject's only issue_timing carrier
	// (e.g. surge rollout stalls with Available still True, so pods derive no
	// owner-condition issue_timing). Its issue_timing survives on child rows as
	// owner_condition — EXCEPT after-healthy onto restart-cycling children:
	// their crashes flap the very Available condition the parent's verdict came
	// from, so that donation would launder flap-poisoned evidence.

	t.Run("after-healthy donates to non-cycling child", func(t *testing.T) {
		p := &fakeProvider{
			problems: []k8s.Detection{
				{Kind: "Deployment", Namespace: "ns", Name: "web", Group: "apps", Severity: "critical", Reason: "1/3 available", IssueTiming: "started_after_resource_was_healthy", IssueTimingBasis: "condition"},
				{Kind: "Pod", Namespace: "ns", Name: "web-abc", Severity: "critical", Reason: "ImagePullBackOff", OwnerKind: "Deployment", OwnerName: "web"},
			},
		}
		for _, i := range Compose(p, Filters{}) {
			if i.Category == issuesapi.CategoryWorkloadDegraded {
				t.Fatalf("parent must still be suppressed: %+v", i)
			}
			if i.Category == issuesapi.CategoryImagePullFailed {
				if i.IssueTiming != "started_after_resource_was_healthy" || i.IssueTimingBasis != "owner_condition" {
					t.Errorf("image-pull child must inherit after-healthy as owner_condition, got (%q, %q)", i.IssueTiming, i.IssueTimingBasis)
				}
			}
		}
	})

	t.Run("after-healthy blocked for crashloop child", func(t *testing.T) {
		p := &fakeProvider{
			problems: []k8s.Detection{
				{Kind: "Deployment", Namespace: "ns", Name: "web", Group: "apps", Severity: "critical", Reason: "1/3 available", IssueTiming: "started_after_resource_was_healthy", IssueTimingBasis: "condition"},
				{Kind: "Pod", Namespace: "ns", Name: "web-abc", Severity: "critical", Reason: "CrashLoopBackOff", OwnerKind: "Deployment", OwnerName: "web"},
			},
		}
		for _, i := range Compose(p, Filters{}) {
			if i.Category == issuesapi.CategoryCrashLoop && i.IssueTiming != "" {
				t.Errorf("crashloop child must not inherit flap-derived after-healthy, got (%q, %q)", i.IssueTiming, i.IssueTimingBasis)
			}
		}
	})

	t.Run("at-creation donates to crashloop child", func(t *testing.T) {
		// at-creation comes from the never-healthy guard (Available LTT pinned
		// at creation), which a flap would have destroyed — so it's safe for
		// restart-cycling children.
		p := &fakeProvider{
			problems: []k8s.Detection{
				{Kind: "Deployment", Namespace: "ns", Name: "web", Group: "apps", Severity: "critical", Reason: "1/3 available", IssueTiming: "started_at_resource_creation", IssueTimingBasis: "condition"},
				{Kind: "Pod", Namespace: "ns", Name: "web-abc", Severity: "critical", Reason: "CrashLoopBackOff", OwnerKind: "Deployment", OwnerName: "web"},
			},
		}
		for _, i := range Compose(p, Filters{}) {
			if i.Category == issuesapi.CategoryCrashLoop {
				if i.IssueTiming != "started_at_resource_creation" || i.IssueTimingBasis != "owner_condition" {
					t.Errorf("crashloop child must inherit at-creation as owner_condition, got (%q, %q)", i.IssueTiming, i.IssueTimingBasis)
				}
			}
		}
	})
}

func TestCompose_PVCPendingDedupesOverMissingStorageClass(t *testing.T) {
	// A PVC referencing a missing StorageClass trips two detectors — the
	// phase-Pending problem and the missing-StorageClass ref — which both
	// classify pvc_pending but carry different fingerprints (distinct IDs).
	// One incident, one row: the missing-ref row names the cause and wins.
	p := &fakeProvider{
		problems: []k8s.Detection{
			{
				Kind: "PersistentVolumeClaim", Namespace: "ns", Name: "stuck-pvc", Severity: "high", Reason: "Pending",
				Cause: "Storage provisioner failed to create a volume.", Action: "Check the CSI controller logs.",
				IssueTiming: "started_at_resource_creation", IssueTimingBasis: "phase",
			},
		},
		missingRefs: []k8s.Detection{
			{Kind: "PersistentVolumeClaim", Namespace: "ns", Name: "stuck-pvc", Severity: "critical", Reason: "Missing StorageClass", Fingerprint: "Missing StorageClass|abc", IssueTiming: "started_at_resource_creation", IssueTimingBasis: "phase"},
		},
	}
	out := Compose(p, Filters{})
	var pvcRows []Issue
	for _, i := range out {
		if i.Category == issuesapi.CategoryPVCPending {
			pvcRows = append(pvcRows, i)
		}
	}
	if len(pvcRows) != 1 {
		t.Fatalf("want exactly 1 pvc_pending row, got %d: %+v", len(pvcRows), pvcRows)
	}
	if pvcRows[0].Reason != "Missing StorageClass" {
		t.Errorf("the cause-naming missing-ref row must win, got reason %q", pvcRows[0].Reason)
	}
	if pvcRows[0].Source != SourceMissingRef {
		t.Errorf("missing-ref row must win over enriched phase row, got source %q", pvcRows[0].Source)
	}
	if pvcRows[0].IssueTiming != "started_at_resource_creation" || pvcRows[0].IssueTimingBasis != "phase" {
		t.Errorf("surviving row must keep at-creation/phase timing, got (%q, %q)", pvcRows[0].IssueTiming, pvcRows[0].IssueTimingBasis)
	}
}

func TestCompose_KeepsCriticalParentWhenOnlyChildIsWarning(t *testing.T) {
	// Suppression must never DOWNGRADE severity. A critical "0/5 available"
	// Deployment whose only child symptom is a warning (pods stuck waiting)
	// must KEEP the critical parent — suppressing it would silently drop the
	// incident from critical to warning. The severity gate in
	// dedupeWorkloadDegradedOverChild only suppresses on an equal-or-worse child.
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Deployment", Namespace: "ns", Name: "web", Group: "apps", Severity: "critical", Reason: "0/5 available"},
			// Child classifies to container_waiting at WARNING severity.
			{Kind: "Pod", Namespace: "ns", Name: "web-abc", Severity: "warning", Reason: "ContainerCreating", OwnerKind: "Deployment", OwnerName: "web"},
		},
	}
	out := Compose(p, Filters{})

	var sawDegraded, sawChild bool
	var degradedSev Severity
	for _, i := range out {
		if i.Category == issuesapi.CategoryWorkloadDegraded {
			sawDegraded = true
			degradedSev = i.Severity
		}
		if i.Category == issuesapi.CategoryContainerWaiting {
			sawChild = true
		}
	}
	if !sawDegraded {
		t.Errorf("critical workload_degraded must NOT be suppressed by a warning-only child (would downgrade the incident): %+v", out)
	}
	if degradedSev != SeverityCritical {
		t.Errorf("parent severity must remain critical, got %q", degradedSev)
	}
	if !sawChild {
		t.Errorf("the warning child row should also survive: %+v", out)
	}
}

func TestCompose_KeepsWorkloadDegradedWhenNoChildSymptom(t *testing.T) {
	// A degraded Deployment with no specific child symptom (e.g. pods not yet
	// failing in a classifiable way) must KEEP its workload_degraded row — the
	// dedup only suppresses the parent when a child names the cause.
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Deployment", Namespace: "ns", Name: "web", Group: "apps", Severity: "critical", Reason: "1/3 available"},
			// An unrelated pod under a DIFFERENT owner — must not suppress web's row.
			{Kind: "Pod", Namespace: "ns", Name: "api-xyz", Severity: "critical", Reason: "CrashLoopBackOff", OwnerKind: "Deployment", OwnerName: "api"},
		},
	}
	out := Compose(p, Filters{})
	var sawDegraded bool
	for _, i := range out {
		if i.Kind == "Deployment" && i.Name == "web" && i.Category == issuesapi.CategoryWorkloadDegraded {
			sawDegraded = true
		}
	}
	if !sawDegraded {
		t.Errorf("workload_degraded must survive when no child symptom exists for its subject: %+v", out)
	}
}

func TestCompose_SuppressesRolloutStalledWhenChildSymptomExists(t *testing.T) {
	// rollout_stalled is also a parent rollup — same suppression rule.
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Deployment", Namespace: "ns", Name: "web", Group: "apps", Severity: "critical", Reason: "Rollout stuck"},
			{Kind: "Pod", Namespace: "ns", Name: "web-abc", Severity: "critical", Reason: "ImagePullBackOff", OwnerKind: "Deployment", OwnerName: "web"},
		},
	}
	out := Compose(p, Filters{})
	for _, i := range out {
		if i.Category == issuesapi.CategoryRolloutStalled {
			t.Errorf("rollout_stalled must be suppressed when a child symptom exists: %+v", out)
		}
	}
}

func TestCompose_SchedulingComposedByDefault(t *testing.T) {
	countSource := func(in []Issue, s Source) int {
		n := 0
		for _, i := range in {
			if i.Source == s {
				n++
			}
		}
		return n
	}
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Deployment", Namespace: "prod", Name: "api", Severity: "critical", Reason: "Unavailable"},
		},
		scheduling: []k8s.Detection{
			{Kind: "Pod", Namespace: "prod", Name: "web-x", Severity: "high", Reason: "Unschedulable", Message: "no node has kubernetes.io/arch=arm64"},
		},
	}

	// Both curated sources compose unconditionally; each row carries its
	// source label for CEL/UI grouping.
	out := Compose(p, Filters{})
	if countSource(out, SourceScheduling) != 1 || countSource(out, SourceProblem) != 1 {
		t.Fatalf("Compose should include problem + scheduling, got %+v", out)
	}
}

func TestCompose_MissingRefsComposedByDefault(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Service", Namespace: "prod", Name: "api", Severity: "warning", Reason: "Selector matches no pods"},
		},
		missingRefs: []k8s.Detection{
			{Kind: "Pod", Namespace: "prod", Name: "web", Severity: "critical", Reason: "Missing PVC"},
		},
	}

	out := Compose(p, Filters{})
	if !hasIssueSource(out, SourceProblem) || !hasIssueSource(out, SourceMissingRef) {
		t.Fatalf("Compose should include problem + missing_ref, got %+v", out)
	}
}

func TestCompose_DiagnosticContextMissingRefCandidate(t *testing.T) {
	p := &fakeProvider{
		missingRefs: []k8s.Detection{
			{Kind: "Pod", Namespace: "prod", Name: "web", Severity: "critical", Reason: "Missing ConfigMap"},
		},
	}

	out := Compose(p, Filters{})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(out), out)
	}
	ctx := out[0].DiagnosticContext
	if ctx == nil || ctx.Role != issuesapi.DiagnosticRoleCandidate {
		t.Fatalf("diagnostic context = %+v, want candidate", ctx)
	}
	if len(ctx.Facts) != 1 || ctx.Facts[0].Type != factExplicitReference {
		t.Fatalf("facts = %+v, want explicit_reference", ctx.Facts)
	}
}

func TestCompose_DiagnosticContextGroupedOwnerRollup(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Namespace: "prod", Name: "web-a", Severity: "critical", Reason: "ImagePullBackOff", OwnerKind: "Deployment", OwnerName: "web"},
			{Kind: "Pod", Namespace: "prod", Name: "web-b", Severity: "critical", Reason: "ImagePullBackOff", OwnerKind: "Deployment", OwnerName: "web"},
		},
	}

	out := Compose(p, Filters{Grouped: true})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(out), out)
	}
	ctx := out[0].DiagnosticContext
	if out[0].Kind != "Deployment" || ctx == nil || ctx.Role != issuesapi.DiagnosticRoleRollup {
		t.Fatalf("issue/context = %+v / %+v, want Deployment rollup", out[0], ctx)
	}
	if len(ctx.Facts) != 1 || ctx.Facts[0].Type != factOwnerRollup || len(ctx.Facts[0].Refs) != 2 {
		t.Fatalf("facts = %+v, want owner_rollup with pod refs", ctx.Facts)
	}
}

func TestCompose_DiagnosticContextServiceAffectedByBackendIssues(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Service", Namespace: "prod", Name: "api", Severity: "critical", Reason: "0/1 selected pods ready"},
			{Kind: "Pod", Namespace: "prod", Name: "api-abc", Severity: "critical", Reason: "CrashLoopBackOff", OwnerKind: "Deployment", OwnerName: "api"},
		},
		selectedPods: map[string][]Ref{
			"prod/api": {{Kind: "Pod", Namespace: "prod", Name: "api-abc"}},
		},
	}

	out := Compose(p, Filters{Grouped: true})
	var svc Issue
	for _, issue := range out {
		if issue.Kind == "Service" && issue.Name == "api" {
			svc = issue
		}
	}
	if svc.Kind == "" {
		t.Fatalf("service issue not found: %+v", out)
	}
	ctx := svc.DiagnosticContext
	if ctx == nil || ctx.Role != issuesapi.DiagnosticRoleAffected {
		t.Fatalf("diagnostic context = %+v, want affected", ctx)
	}
	if len(ctx.Facts) != 1 || ctx.Facts[0].Type != factSelectedBackend || len(ctx.Facts[0].RelatedIssues) != 1 {
		t.Fatalf("facts = %+v, want selected backend related issue", ctx.Facts)
	}
	if got := ctx.Facts[0].RelatedIssues[0].Ref; got.Kind != "Deployment" || got.Name != "api" {
		t.Fatalf("related issue ref = %+v, want Deployment/api", got)
	}
}

func TestCompose_DiagnosticContextServiceAffectedByBackendIssuesFlatMode(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Service", Namespace: "prod", Name: "api", Severity: "critical", Reason: "0/1 selected pods ready"},
			{Kind: "Pod", Namespace: "prod", Name: "api-abc", Severity: "critical", Reason: "CrashLoopBackOff", OwnerKind: "Deployment", OwnerName: "api"},
		},
		selectedPods: map[string][]Ref{
			"prod/api": {{Kind: "Pod", Namespace: "prod", Name: "api-abc"}},
		},
	}

	out := Compose(p, Filters{})
	var svc Issue
	for _, issue := range out {
		if issue.Kind == "Service" && issue.Name == "api" {
			svc = issue
		}
	}
	if svc.Kind == "" {
		t.Fatalf("service issue not found: %+v", out)
	}
	ctx := svc.DiagnosticContext
	if ctx == nil || len(ctx.Facts) != 1 || len(ctx.Facts[0].RelatedIssues) != 1 {
		t.Fatalf("diagnostic context = %+v, want selected backend related issue", ctx)
	}
	if got := ctx.Facts[0].RelatedIssues[0].Ref; got.Kind != "Pod" || got.Name != "api-abc" {
		t.Fatalf("related issue ref = %+v, want flat Pod/api-abc", got)
	}
}

func TestCompose_DiagnosticContextServiceConfigCandidate(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Service", Namespace: "prod", Name: "api", Severity: "warning", Reason: "Selector matches no pods"},
		},
	}

	out := Compose(p, Filters{})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(out), out)
	}
	ctx := out[0].DiagnosticContext
	if ctx == nil || ctx.Role != issuesapi.DiagnosticRoleCandidate {
		t.Fatalf("diagnostic context = %+v, want candidate", ctx)
	}
	if len(ctx.Facts) != 1 || ctx.Facts[0].Type != factServiceConfig {
		t.Fatalf("facts = %+v, want service_config_mismatch", ctx.Facts)
	}
}

func TestCompose_DiagnosticContextServiceEnvCandidate(t *testing.T) {
	cases := []struct {
		name    string
		reason  string
		message string
	}{
		{
			name:    "port mismatch",
			reason:  "Service port mismatch",
			message: "CHECKOUT_ADDR points at cart:8080, but cart exposes 80",
		},
		{
			name:    "missing service",
			reason:  "Missing referenced Service",
			message: "AD_SERVICE_ADDR points at missing Service ad:8080",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{
				problems: []k8s.Detection{
					{Kind: "Deployment", Namespace: "prod", Name: "checkout", Severity: "critical", Reason: tc.reason, Message: tc.message},
				},
			}

			out := Compose(p, Filters{})
			if len(out) != 1 {
				t.Fatalf("got %d issues, want 1: %+v", len(out), out)
			}
			ctx := out[0].DiagnosticContext
			if ctx == nil || ctx.Role != issuesapi.DiagnosticRoleCandidate {
				t.Fatalf("diagnostic context = %+v, want candidate", ctx)
			}
			if len(ctx.Facts) != 1 || ctx.Facts[0].Type != factServiceEnvReference || ctx.Facts[0].Message == "" {
				t.Fatalf("facts = %+v, want service_env_reference with message", ctx.Facts)
			}
		})
	}
}

func TestCompose_DiagnosticContextProbeAndInitCandidates(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Namespace: "prod", Name: "probe", Severity: "critical", Reason: "ReadinessProbeInvalid", Message: "readiness probe references named port \"http\""},
			{Kind: "Pod", Namespace: "prod", Name: "init", Severity: "high", Reason: "InitContainerStalled", Message: "init container \"wait\" has been running for 10m"},
		},
	}

	out := Compose(p, Filters{})
	byName := map[string]Issue{}
	for _, issue := range out {
		byName[issue.Name] = issue
	}
	probeCtx := byName["probe"].DiagnosticContext
	if probeCtx == nil || probeCtx.Role != issuesapi.DiagnosticRoleCandidate || len(probeCtx.Facts) != 1 || probeCtx.Facts[0].Type != factProbeTarget {
		t.Fatalf("probe diagnostic context = %+v, want probe target candidate", probeCtx)
	}
	initCtx := byName["init"].DiagnosticContext
	if initCtx == nil || initCtx.Role != issuesapi.DiagnosticRoleCandidate || len(initCtx.Facts) != 1 || initCtx.Facts[0].Type != factBlockedInit {
		t.Fatalf("init diagnostic context = %+v, want blocked init candidate", initCtx)
	}
}

func TestCompose_DiagnosticContextRestartCauseFact(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{
				Kind:                 "Pod",
				Namespace:            "prod",
				Name:                 "frontend-abc",
				Severity:             "critical",
				Reason:               "LivenessProbeFailed",
				RestartCount:         4,
				LastTerminatedReason: "Error",
			},
		},
	}

	out := Compose(p, Filters{})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1: %+v", len(out), out)
	}
	ctx := out[0].DiagnosticContext
	if ctx == nil {
		t.Fatalf("diagnostic context missing")
	}
	var saw bool
	for _, fact := range ctx.Facts {
		if fact.Type == factRestartCause && strings.Contains(fact.Message, "restartCount=4") && strings.Contains(fact.Message, "probeFailure=LivenessProbeFailed") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("facts = %+v, want restart_cause evidence", ctx.Facts)
	}
}

func TestCompose_AttachesPositiveChangeContext(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Deployment", Group: "apps", Namespace: "prod", Name: "checkout", Severity: "critical", Reason: "Deployment unavailable"},
			{Kind: "Deployment", Group: "apps", Namespace: "prod", Name: "cart", Severity: "critical", Reason: "Deployment unavailable"},
		},
		change: map[string]*issuesapi.ChangeContext{
			resourceKey("apps", "Deployment", "prod", "checkout"): {
				Changed:  true,
				What:     "pod_template",
				When:     "2m",
				Evidence: "generation=3, 2 owned ReplicaSets",
			},
		},
	}

	out := Compose(p, Filters{})
	byName := map[string]Issue{}
	for _, issue := range out {
		byName[issue.Name] = issue
	}
	if ctx := byName["checkout"].ChangeContext; ctx == nil || !ctx.Changed || ctx.What != "pod_template" || ctx.Evidence == "" {
		t.Fatalf("checkout change context = %+v, want positive pod_template evidence", ctx)
	}
	if ctx := byName["cart"].ChangeContext; ctx != nil {
		t.Fatalf("cart change context = %+v, want nil when provider has no positive evidence", ctx)
	}
}

func TestDeploymentChangeContextUsesReplicaSetHistory(t *testing.T) {
	defer k8s.ResetTestState()

	trueVal := true
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "checkout",
			Namespace:  "prod",
			UID:        "deploy-uid",
			Generation: 3,
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 3},
	}
	oldRS := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "checkout-old",
			Namespace:         "prod",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-20 * time.Minute)),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "checkout",
				UID:        "deploy-uid",
				Controller: &trueVal,
			}},
		},
	}
	newRS := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "checkout-new",
			Namespace:         "prod",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "checkout",
				UID:        "deploy-uid",
				Controller: &trueVal,
			}},
		},
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "prod"}},
		deploy,
		oldRS,
		newRS,
	)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	ctx := deploymentChangeContext(k8s.GetResourceCache(), "prod", "checkout")
	if ctx == nil || !ctx.Changed || ctx.What != "pod_template" || !strings.Contains(ctx.Evidence, "2 owned ReplicaSets") || !strings.Contains(ctx.Evidence, "checkout-new") {
		t.Fatalf("deployment change context = %+v, want newest ReplicaSet evidence", ctx)
	}
}

func TestClusterDNSContextRequiresTriggerSignal(t *testing.T) {
	defer k8s.ResetTestState()

	client := fake.NewClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"},
			Data: map[string]string{
				"Corefile": ".:53 {\n  template ANY svc.cluster.local {\n    rcode NXDOMAIN\n  }\n}\n",
			},
		},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	provider := &CacheProvider{cache: k8s.GetResourceCache()}

	if got := provider.ClusterContextForIssues([]string{"prod"}, nil); got != nil {
		t.Fatalf("static suspicious CoreDNS config should not produce cluster context without symptoms/change evidence: %+v", got)
	}
}

func TestClusterDNSContextScansAllNamespacesForSymptoms(t *testing.T) {
	defer k8s.ResetTestState()

	client := fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "prod"}},
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"},
			Data: map[string]string{
				"Corefile": ".:53 {\n  template ANY svc.cluster.local {\n    rcode NXDOMAIN\n  }\n}\n",
			},
		},
		&corev1.Event{
			ObjectMeta: metav1.ObjectMeta{Name: "dns-failure", Namespace: "prod"},
			Type:       corev1.EventTypeWarning,
			Reason:     "Failed",
			Message:    "lookup checkout.prod.svc.cluster.local: no such host",
		},
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	provider := &CacheProvider{cache: k8s.GetResourceCache()}

	got := provider.ClusterContextForIssues(nil, nil)
	if got == nil || got.DNS == nil || len(got.DNS.Signals) == 0 {
		t.Fatalf("cluster DNS context = %+v, want namespace DNS symptom signal", got)
	}
	if !strings.Contains(strings.Join(got.DNS.Signals, " "), "namespace prod") {
		t.Fatalf("cluster DNS signals = %+v, want prod namespace symptom", got.DNS.Signals)
	}
}

func TestClusterDNSContextFiresOnSuspiciousCoreDNSAndRecentRollout(t *testing.T) {
	defer k8s.ResetTestState()

	trueVal := true
	deploy := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{
			Name:       "coredns",
			Namespace:  "kube-system",
			UID:        "coredns-uid",
			Generation: 3,
			Labels:     map[string]string{"k8s-app": "kube-dns"},
		},
		Status: appsv1.DeploymentStatus{ObservedGeneration: 3},
	}
	oldRS := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "coredns-old",
			Namespace:         "kube-system",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-20 * time.Minute)),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "coredns",
				UID:        "coredns-uid",
				Controller: &trueVal,
			}},
		},
	}
	newRS := &appsv1.ReplicaSet{
		ObjectMeta: metav1.ObjectMeta{
			Name:              "coredns-new",
			Namespace:         "kube-system",
			CreationTimestamp: metav1.NewTime(time.Now().Add(-2 * time.Minute)),
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "coredns",
				UID:        "coredns-uid",
				Controller: &trueVal,
			}},
		},
	}
	client := fake.NewClientset(
		&corev1.ConfigMap{
			ObjectMeta: metav1.ObjectMeta{Name: "coredns", Namespace: "kube-system"},
			Data: map[string]string{
				"Corefile": ".:53 {\n  template ANY svc.cluster.local {\n    rcode NXDOMAIN\n  }\n}\n",
			},
		},
		deploy,
		oldRS,
		newRS,
	)
	if err := k8s.InitTestResourceCache(client); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}
	provider := &CacheProvider{cache: k8s.GetResourceCache()}

	got := provider.ClusterContextForIssues([]string{"prod"}, nil)
	if got == nil || got.DNS == nil || len(got.DNS.Findings) != 1 || len(got.DNS.Signals) == 0 {
		t.Fatalf("cluster DNS context = %+v, want DNS finding with rollout signal", got)
	}
	if !strings.Contains(got.DNS.Signals[0], "CoreDNS Deployment") || !strings.Contains(got.DNS.Hint, "CoreDNS NXDOMAIN override") {
		t.Fatalf("cluster DNS context = %+v, want directive CoreDNS rollout hint", got.DNS)
	}

	// A caller that can't read kube-system CoreDNS sources must not learn its
	// suspicious state via the cluster-context side channel.
	if denied := provider.ClusterContextForIssues([]string{"prod"}, func(string, string) bool { return false }); denied != nil {
		t.Fatalf("cluster DNS context must be suppressed when caller can't read CoreDNS: %+v", denied)
	}

	configMapOnly := provider.ClusterContextForIssues([]string{"prod"}, func(group, resource string) bool {
		return group == "" && resource == "configmaps"
	})
	if configMapOnly != nil {
		t.Fatalf("rollout-only DNS context must be suppressed without Deployment/ReplicaSet access: %+v", configMapOnly)
	}
}

func TestCompose_GenericCRDConditionFallback(t *testing.T) {
	// KEDA ScaledObject — a CRD with NO curated detector, so it exercises the
	// generic status.conditions fallback. (Argo/Flux are now handled by the
	// dedicated GitOps detector and would not reach this path.)
	gvr := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	app := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "keda.sh/v1alpha1",
		"kind":       "ScaledObject",
		"metadata":   map[string]any{"name": "my-app", "namespace": "apps"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{
					"type":               "Ready",
					"status":             "False",
					"reason":             "ScalerFailed",
					"message":            "drift detected",
					"lastTransitionTime": time.Now().Add(-5 * time.Minute).UTC().Format(time.RFC3339),
				},
			},
		},
	}}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {app}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "ScaledObject"},
	}
	out := Compose(p, Filters{})
	if len(out) != 1 {
		t.Fatalf("got %d issues, want 1", len(out))
	}
	hit := out[0]
	if hit.Source != SourceCondition {
		t.Fatalf("source: %s", hit.Source)
	}
	if hit.Group != "keda.sh" {
		t.Fatalf("group not propagated: %+v", hit)
	}
	if hit.Severity != SeverityWarning {
		t.Fatalf("severity: %s", hit.Severity)
	}
	if hit.Reason == "" || hit.Message != "drift detected" {
		t.Fatalf("reason/message: %+v", hit)
	}
}

func TestCompose_CRDConditionNoiseFloorSuppression(t *testing.T) {
	// The generic CRD detector must NOT warn on objects that
	// are suspended, mid-reconcile, or whose controller hasn't observed the
	// current spec — only on genuinely-failed ones. Each subtest stages one
	// CRD object and asserts whether it surfaces.
	// KEDA ScaledObject stands in for a generic CRD here — the suspend /
	// observedGeneration / transient-reason noise floor is shared logic that
	// must apply to any non-curated CRD. (Flux is now handled by the dedicated
	// GitOps detector, with its own narrower transient gating.)
	gvr := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	mk := func(meta, spec, status map[string]any) *unstructured.Unstructured {
		obj := map[string]any{
			"apiVersion": "keda.sh/v1alpha1",
			"kind":       "ScaledObject",
			"metadata":   meta,
		}
		if spec != nil {
			obj["spec"] = spec
		}
		if status != nil {
			obj["status"] = status
		}
		return &unstructured.Unstructured{Object: obj}
	}
	falseReady := func(reason string) map[string]any {
		return map[string]any{"conditions": []any{
			map[string]any{"type": "Ready", "status": "False", "reason": reason, "message": "m"},
		}}
	}

	cases := []struct {
		name    string
		obj     *unstructured.Unstructured
		wantHit bool
	}{
		{
			name:    "mid-reconcile (transient reason) skipped",
			obj:     mk(map[string]any{"name": "reconciling", "namespace": "flux"}, nil, falseReady("Progressing")),
			wantHit: false,
		},
		{
			name:    "dependency-not-ready skipped",
			obj:     mk(map[string]any{"name": "dep", "namespace": "flux"}, nil, falseReady("DependencyNotReady")),
			wantHit: false,
		},
		{
			name:    "suspended object skipped",
			obj:     mk(map[string]any{"name": "paused", "namespace": "flux"}, map[string]any{"suspend": true}, falseReady("SomeFailure")),
			wantHit: false,
		},
		{
			name: "observedGeneration lag skipped",
			obj: mk(
				map[string]any{"name": "lagging", "namespace": "flux", "generation": int64(5)},
				nil,
				map[string]any{
					"observedGeneration": int64(3),
					"conditions": []any{
						map[string]any{"type": "Ready", "status": "False", "reason": "BuildFailed", "message": "m"},
					},
				},
			),
			wantHit: false,
		},
		{
			name:    "genuinely failed kept",
			obj:     mk(map[string]any{"name": "broken", "namespace": "flux"}, nil, falseReady("BuildFailed")),
			wantHit: true,
		},
		{
			// ArtifactFailed/ChartNotReady are in the shared health-display
			// transient set (soften the badge to degraded) but are genuine stuck
			// failures — the Issues queue must surface, not suppress them.
			name:    "artifact-failed is a genuine failure, not transient — kept",
			obj:     mk(map[string]any{"name": "artifact", "namespace": "flux"}, nil, falseReady("ArtifactFailed")),
			wantHit: true,
		},
		{
			name:    "chart-not-ready is a genuine failure, not transient — kept",
			obj:     mk(map[string]any{"name": "chart", "namespace": "flux"}, nil, falseReady("ChartNotReady")),
			wantHit: true,
		},
		{
			name: "failed with current generation kept",
			obj: mk(
				map[string]any{"name": "broken2", "namespace": "flux", "generation": int64(5)},
				map[string]any{"suspend": false},
				map[string]any{
					"observedGeneration": int64(5),
					"conditions": []any{
						map[string]any{"type": "Ready", "status": "False", "reason": "BuildFailed", "message": "m"},
					},
				},
			),
			wantHit: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := &fakeProvider{
				dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {tc.obj}},
				kinds:   map[schema.GroupVersionResource]string{gvr: "ScaledObject"},
			}
			out := Compose(p, Filters{})
			got := len(out) > 0
			if got != tc.wantHit {
				t.Fatalf("hit=%v want %v: %+v", got, tc.wantHit, out)
			}
		})
	}
}

func TestCompose_CAPIGroupSkippedByGenericFallback(t *testing.T) {
	// Curated CAPI checker owns this group — generic fallback should
	// skip it to avoid double-reporting.
	gvr := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: "v1beta1", Resource: "clusters"}
	cl := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "c1"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "X"},
			},
		},
	}}
	p := &fakeProvider{
		dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {cl}},
		kinds:   map[schema.GroupVersionResource]string{gvr: "Cluster"},
	}
	out := Compose(p, Filters{})
	if len(out) != 0 {
		t.Fatalf("CAPI should be skipped by generic fallback: %+v", out)
	}
}

func TestCompose_CAPIProviderCRDsUseGenericFallback(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "infrastructure.cluster.x-k8s.io", Version: "v1beta1", Resource: "awsmachines"}
	m := &unstructured.Unstructured{Object: map[string]any{
		"metadata": map[string]any{"name": "m1", "namespace": "capi-system"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "InstanceNotFound", "message": "EC2 instance not found"},
			},
		},
	}}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {m}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "AWSMachine"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: true},
	}
	out := Compose(p, Filters{})
	if len(out) != 1 {
		t.Fatalf("provider CRD condition should use generic fallback, got %+v", out)
	}
	if out[0].Source != SourceCondition || out[0].Kind != "AWSMachine" || out[0].Group != "infrastructure.cluster.x-k8s.io" {
		t.Fatalf("wrong provider CRD issue: %+v", out[0])
	}
}

func TestCompose_DropsUnauthorizedClusterScopedIssues(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Deployment", Namespace: "team-a", Name: "api", Severity: "critical", Reason: "down"},
			{Kind: "Node", Name: "worker-1", Severity: "critical", Reason: "not ready"},
		},
	}
	out := Compose(p, Filters{
		CanReadClusterScoped: func(kind, group string) bool {
			if kind != "Node" || group != "" {
				t.Fatalf("unexpected cluster-scoped check: kind=%q group=%q", kind, group)
			}
			return false
		},
	})
	if len(out) != 1 {
		t.Fatalf("expected only namespaced issue, got %+v", out)
	}
	if out[0].Kind != "Deployment" || out[0].Namespace != "team-a" {
		t.Fatalf("wrong issue retained: %+v", out)
	}
}

func TestCompose_DropsUnauthorizedClusterScopedCRDConditions(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	np := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata":   map[string]any{"name": "default"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "Drifted"},
			},
		},
	}}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {np}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "NodePool"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: false},
	}
	out := Compose(p, Filters{
		CanReadClusterScoped: func(kind, group string) bool {
			if kind != "NodePool" || group != "karpenter.sh" {
				t.Fatalf("unexpected cluster-scoped check: kind=%q group=%q", kind, group)
			}
			return false
		},
	})
	if len(out) != 0 {
		t.Fatalf("cluster-scoped CRD condition leaked despite denied access: %+v", out)
	}
}

func TestCompose_NamespaceFilterDropsClusterScopedGenericCRDConditions(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}
	np := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "karpenter.sh/v1",
		"kind":       "NodePool",
		"metadata":   map[string]any{"name": "default"},
		"status": map[string]any{
			"conditions": []any{
				map[string]any{"type": "Ready", "status": "False", "reason": "Drifted"},
			},
		},
	}}
	p := &fakeProvider{
		dynamic:    map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {np}},
		kinds:      map[schema.GroupVersionResource]string{gvr: "NodePool"},
		namespaced: map[schema.GroupVersionResource]bool{gvr: false},
	}
	out := Compose(p, Filters{
		Namespaces: []string{"prod"},
		CanReadClusterScoped: func(kind, group string) bool {
			t.Fatalf("namespace-scoped issue query should not authorize cluster-scoped generic CRD rows: kind=%q group=%q", kind, group)
			return true
		},
	})
	if len(out) != 0 {
		t.Fatalf("cluster-scoped CRD condition should not appear under namespace filter: %+v", out)
	}
}

func TestCompose_SeveritySortedDescending(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Name: "warn1", Severity: "high"},
			{Kind: "Pod", Name: "crit1", Severity: "critical"},
			{Kind: "Pod", Name: "warn2", Severity: "medium"},
		},
	}
	out := Compose(p, Filters{})
	if out[0].Name != "crit1" {
		t.Fatalf("critical should sort first, got %+v", out[0])
	}
}

func TestCompose_DirectBlockersSortBeforeGenericProblems(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Namespace: "ns", Name: "crashy", Severity: "critical", Reason: "CrashLoopBackOff"},
		},
		missingRefs: []k8s.Detection{
			{Kind: "Pod", Namespace: "ns", Name: "missing-config", Severity: "critical", Reason: "Missing ConfigMap"},
		},
		scheduling: []k8s.Detection{
			{Kind: "ReplicaSet", Namespace: "ns", Name: "quota-blocked", Severity: "critical", Reason: "QuotaExceeded"},
		},
	}
	out := Compose(p, Filters{})
	if len(out) != 3 {
		t.Fatalf("got %d issues: %+v", len(out), out)
	}
	if out[0].Source != SourceScheduling || out[0].Reason != "QuotaExceeded" {
		t.Fatalf("active scheduling/admission blocker should sort first at same severity, got %+v", out[0])
	}
	if out[1].Source != SourceMissingRef {
		t.Fatalf("missing ref should sort before generic problem at same severity, got %+v", out)
	}
}

func TestCompose_SeverityFilter(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Name: "a", Severity: "critical"},
			{Kind: "Pod", Name: "b", Severity: "medium"},
		},
	}
	out := Compose(p, Filters{Severities: []Severity{SeverityCritical}})
	if len(out) != 1 || out[0].Name != "a" {
		t.Fatalf("severity filter wrong: %+v", out)
	}
}

func TestCompose_KindFilter(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Pod", Name: "p", Severity: "critical"},
			{Kind: "Deployment", Name: "d", Severity: "critical"},
		},
	}
	out := Compose(p, Filters{Kinds: []string{"Pod"}})
	if len(out) != 1 || out[0].Kind != "Pod" {
		t.Fatalf("kind filter wrong: %+v", out)
	}
}

func TestCompose_LimitTruncates(t *testing.T) {
	probs := make([]k8s.Detection, 0, 50)
	for i := 0; i < 50; i++ {
		probs = append(probs, k8s.Detection{Kind: "Pod", Name: "p", Severity: "critical"})
	}
	p := &fakeProvider{problems: probs}
	out := Compose(p, Filters{Limit: 10})
	if len(out) != 10 {
		t.Fatalf("limit not honored: %d", len(out))
	}
}

func TestCompose_DeterministicOrderForTies(t *testing.T) {
	// Same severity + same issue_timing → tiebreak on (namespace, name, id), matching
	// the UI comparator. All hits are critical, all DurationSeconds=0, so
	// FirstSeen ties; here same-ns rows order by name (a, b, z).
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Service", Namespace: "ns", Name: "z", Severity: "critical"},
			{Kind: "Pod", Namespace: "ns", Name: "a", Severity: "critical"},
			{Kind: "Pod", Namespace: "ns", Name: "b", Severity: "critical"},
		},
	}
	out := Compose(p, Filters{})
	got := []string{out[0].Kind + "/" + out[0].Name, out[1].Kind + "/" + out[1].Name, out[2].Kind + "/" + out[2].Name}
	want := []string{"Pod/a", "Pod/b", "Service/z"} // Pod < Service alphabetically
	if got[0] != want[0] || got[1] != want[1] || got[2] != want[2] {
		t.Fatalf("tiebreak order: got %v want %v", got, want)
	}
}

// silence unused-import lint when sort isn't used elsewhere
var _ = sort.Strings

func hasIssueSource(issues []Issue, source Source) bool {
	for _, issue := range issues {
		if issue.Source == source {
			return true
		}
	}
	return false
}

// flattenNamespacedProblems exists to keep CacheProvider's per-
// namespace fan-out from leaking + duplicating cluster-scoped
// problems (Node, etc.). These tests pin that contract.

func TestFlattenNamespacedProblems_DropsClusterScopedEntries(t *testing.T) {
	// Each per-namespace list as returned by k8s.DetectProblems
	// includes the cluster-scoped Node block — without filtering, a
	// namespace-bounded caller asking for {ns1, ns2} would see Node
	// problems twice AND see them at all (RBAC violation if the user
	// lacks `list nodes` at cluster scope).
	perNs := [][]k8s.Detection{
		{
			{Kind: "Pod", Namespace: "ns1", Name: "p1", Severity: "critical"},
			{Kind: "Node", Name: "node-1", Severity: "high"}, // empty Namespace
		},
		{
			{Kind: "Pod", Namespace: "ns2", Name: "p2", Severity: "critical"},
			{Kind: "Node", Name: "node-1", Severity: "high"}, // dup leak
		},
	}
	out := flattenNamespacedProblems(perNs)
	if len(out) != 2 {
		t.Fatalf("want 2 namespaced problems, got %d: %+v", len(out), out)
	}
	for _, p := range out {
		if p.Kind == "Node" {
			t.Errorf("Node problem leaked through namespace-scoped flatten: %+v", p)
		}
		if p.Namespace == "" {
			t.Errorf("cluster-scoped problem leaked: %+v", p)
		}
	}
}

func TestFlattenNamespacedProblems_PreservesNamespacedAcrossSlices(t *testing.T) {
	// Namespaced rows from different per-namespace calls all survive
	// — no over-zealous dedup.
	perNs := [][]k8s.Detection{
		{{Kind: "Pod", Namespace: "ns1", Name: "a"}},
		{{Kind: "Pod", Namespace: "ns2", Name: "a"}}, // same name, different ns
		{{Kind: "Service", Namespace: "ns3", Name: "svc"}},
	}
	out := flattenNamespacedProblems(perNs)
	if len(out) != 3 {
		t.Fatalf("want 3 problems preserved, got %d: %+v", len(out), out)
	}
}

func TestFlattenNamespacedProblems_EmptyInputReturnsNil(t *testing.T) {
	if out := flattenNamespacedProblems(nil); len(out) != 0 {
		t.Errorf("nil input should produce empty output, got %+v", out)
	}
	if out := flattenNamespacedProblems([][]k8s.Detection{}); len(out) != 0 {
		t.Errorf("empty input should produce empty output, got %+v", out)
	}
}

// countingProvider wraps fakeProvider and tallies ListDynamic calls per
// GVR. Used by TestDetectGenericCRDIssues_SkipsListWhenKindFiltered to
// pin that detectGenericCRDIssues short-circuits the per-GVR
// ListDynamic call when f.Kinds excludes the GVR's kind — on clusters
// with hundreds of watched CRDs, scanning every one for a pods-only
// summaryContext request was the dominant cost.
type countingProvider struct {
	fakeProvider
	listCalls map[schema.GroupVersionResource]int
}

func (c *countingProvider) ListDynamic(gvr schema.GroupVersionResource, ns string) ([]*unstructured.Unstructured, error) {
	if c.listCalls == nil {
		c.listCalls = map[schema.GroupVersionResource]int{}
	}
	c.listCalls[gvr]++
	return c.fakeProvider.ListDynamic(gvr, ns)
}

func (c *countingProvider) ListDynamicAllNamespaces(gvr schema.GroupVersionResource) ([]*unstructured.Unstructured, error) {
	if c.listCalls == nil {
		c.listCalls = map[schema.GroupVersionResource]int{}
	}
	c.listCalls[gvr]++
	return c.fakeProvider.ListDynamicAllNamespaces(gvr)
}

// TestDetectGenericCRDIssues_SkipsListWhenKindFiltered pins the
// "scan all CRDs before kindFilter applies" perf fix in
// detectGenericCRDIssues. Pre-fix, a Compose call with Kinds=["Pod"]
// still iterated every watched CRD GVR and ran ListDynamic on each;
// applyFilters then discarded the non-matching rows at the end.
//
// On a cluster with hundreds of watched CRDs this dominated the
// summaryContext per-row index build for list_resources kind=pods.
// The fix routes f.Kinds awareness into detectGenericCRDIssues so
// non-matching GVRs skip the ListDynamic call entirely.
func TestDetectGenericCRDIssues_SkipsListWhenKindFiltered(t *testing.T) {
	podGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
	// ScaledObject is non-curated, so it flows through the generic path being
	// tested here (Argo Application is now owned by the GitOps detector).
	soGVR := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	npGVR := schema.GroupVersionResource{Group: "karpenter.sh", Version: "v1", Resource: "nodepools"}

	p := &countingProvider{
		fakeProvider: fakeProvider{
			dynamic: map[schema.GroupVersionResource][]*unstructured.Unstructured{
				podGVR: {}, // empty — only counts the call.
				soGVR: {{Object: map[string]any{
					"metadata": map[string]any{"name": "a", "namespace": "apps"},
					"status": map[string]any{
						"conditions": []any{
							map[string]any{"type": "Ready", "status": "False", "reason": "ScalerFailed"},
						},
					},
				}}},
				npGVR: {}, // empty — only counts the call.
			},
			kinds: map[schema.GroupVersionResource]string{
				podGVR: "Pod",
				soGVR:  "ScaledObject",
				npGVR:  "NodePool",
			},
		},
	}

	// kindFilter restricts to ScaledObject — the other two GVRs must NOT
	// be listed. detectGenericCRDIssues lowercases the kind comparison
	// (mirrors applyFilters), so the canonical "ScaledObject" matches the
	// emitted Kind for the keda.sh GVR.
	_ = detectGenericCRDIssues(p, Filters{Kinds: []string{"ScaledObject"}})

	if got := p.listCalls[podGVR]; got != 0 {
		t.Errorf("Pod GVR ListDynamic calls = %d, want 0 (kind filter must skip non-matching GVRs)", got)
	}
	if got := p.listCalls[npGVR]; got != 0 {
		t.Errorf("NodePool GVR ListDynamic calls = %d, want 0 (kind filter must skip non-matching GVRs)", got)
	}
	if got := p.listCalls[soGVR]; got == 0 {
		t.Errorf("ScaledObject GVR ListDynamic calls = %d, want >= 1 (matching kind must still be scanned)", got)
	}

	// Sanity: empty Kinds filter scans every GVR (no per-kind shortcut
	// when caller didn't ask for one). Pins that the fix is filter-aware
	// rather than always-skip.
	p.listCalls = nil
	_ = detectGenericCRDIssues(p, Filters{})
	for gvr, want := range map[schema.GroupVersionResource]bool{podGVR: true, soGVR: true, npGVR: true} {
		if got := p.listCalls[gvr] > 0; got != want {
			t.Errorf("no kind filter: GVR %s called=%v, want %v", gvr.Resource, got, want)
		}
	}
}

func TestNodeBlastRadiusContext(t *testing.T) {
	// A MemoryPressure node: OOM is the attributable symptom; a crashloop on the
	// same node is app-dominant and must NOT be attributed to memory pressure, and
	// an image-pull failure is node-independent.
	node := Issue{Kind: "Node", Name: "node-1", Category: issuesapi.CategoryNodeNotReady, Severity: SeverityCritical, Reason: "MemoryPressure"}
	oom := Issue{ID: "oom-1", Kind: "Pod", Namespace: "prod", Name: "web-abc", Category: issuesapi.CategoryOOMKilled, Severity: SeverityCritical}
	crash := Issue{ID: "crash-1", Kind: "Pod", Namespace: "prod", Name: "api-xyz", Category: issuesapi.CategoryCrashLoop, Severity: SeverityCritical}
	pull := Issue{ID: "pull-1", Kind: "Pod", Namespace: "prod", Name: "cache-9", Category: issuesapi.CategoryImagePullFailed, Severity: SeverityWarning}

	p := &fakeProvider{podsOnNode: map[string][]Ref{"node-1": {
		{Kind: "Pod", Namespace: "prod", Name: "web-abc"},
		{Kind: "Pod", Namespace: "prod", Name: "api-xyz"},
		{Kind: "Pod", Namespace: "prod", Name: "cache-9"},
	}}}

	out := enrichDiagnosticContext([]Issue{node}, []Issue{node, oom, crash, pull}, nil, p)
	ctx := out[0].DiagnosticContext
	if ctx == nil {
		t.Fatal("node issue got no diagnostic context")
	}
	var fact *issuesapi.DiagnosticFact
	for i := range ctx.Facts {
		if ctx.Facts[i].Type == factNodeBlastRadius {
			fact = &ctx.Facts[i]
		}
	}
	if fact == nil {
		t.Fatalf("no node_blast_radius fact, got %+v", ctx.Facts)
	}
	if fact.Confidence != issuesapi.ConfidenceMedium {
		t.Errorf("confidence = %q, want medium", fact.Confidence)
	}
	if ctx.Role != issuesapi.DiagnosticRoleCandidate {
		t.Errorf("role = %q, want candidate", ctx.Role)
	}
	// Only the OOM (attributable to MemoryPressure) links — not the app-dominant
	// crashloop, not the node-independent image pull.
	if len(fact.RelatedIssues) != 1 || fact.RelatedIssues[0].Category != issuesapi.CategoryOOMKilled || fact.RelatedIssues[0].Ref.Name != "web-abc" {
		t.Fatalf("expected only the OOM linked under MemoryPressure, got %+v", fact.RelatedIssues)
	}
}

func TestNodeBlastRadiusContext_NotReadyLinksNothing(t *testing.T) {
	// A dead-kubelet NotReady node: pod statuses are stale, so crashloop/OOM rows
	// are pre-existing app problems, not node-caused — nothing should link.
	node := Issue{Kind: "Node", Name: "dead", Category: issuesapi.CategoryNodeNotReady, Severity: SeverityCritical, Reason: "NotReady"}
	oom := Issue{ID: "oom-9", Kind: "Pod", Namespace: "prod", Name: "p1", Category: issuesapi.CategoryOOMKilled, Severity: SeverityCritical}
	p := &fakeProvider{podsOnNode: map[string][]Ref{"dead": {{Kind: "Pod", Namespace: "prod", Name: "p1"}}}}
	out := enrichDiagnosticContext([]Issue{node}, []Issue{node, oom}, nil, p)
	if out[0].DiagnosticContext != nil {
		t.Fatalf("a NotReady (dead-kubelet) node must not attribute pod issues, got %+v", out[0].DiagnosticContext)
	}
}

func TestNodeBlastRadiusContext_MultiPressure(t *testing.T) {
	// A node with BOTH MemoryPressure and DiskPressure groups into one issue that
	// keeps a single representative Reason — the link must still cover BOTH
	// pressures' pod categories (OOM under memory, container_waiting under disk).
	grouped := Issue{Kind: "Node", Name: "node-1", Category: issuesapi.CategoryNodeNotReady, Severity: SeverityCritical, Reason: "MemoryPressure"}
	flatMem := Issue{Kind: "Node", Name: "node-1", Category: issuesapi.CategoryNodeNotReady, Severity: SeverityCritical, Reason: "MemoryPressure"}
	flatDisk := Issue{Kind: "Node", Name: "node-1", Category: issuesapi.CategoryNodeNotReady, Severity: SeverityCritical, Reason: "DiskPressure"}
	oom := Issue{ID: "oom-1", Kind: "Pod", Namespace: "prod", Name: "a", Category: issuesapi.CategoryOOMKilled, Severity: SeverityCritical}
	waiting := Issue{ID: "wait-1", Kind: "Pod", Namespace: "prod", Name: "b", Category: issuesapi.CategoryContainerWaiting, Severity: SeverityWarning, Reason: "ContainerCreating"}

	p := &fakeProvider{podsOnNode: map[string][]Ref{"node-1": {
		{Kind: "Pod", Namespace: "prod", Name: "a"},
		{Kind: "Pod", Namespace: "prod", Name: "b"},
	}}}

	out := enrichDiagnosticContext([]Issue{grouped}, []Issue{flatMem, flatDisk, oom, waiting}, nil, p)
	var fact *issuesapi.DiagnosticFact
	for i := range out[0].DiagnosticContext.Facts {
		if out[0].DiagnosticContext.Facts[i].Type == factNodeBlastRadius {
			fact = &out[0].DiagnosticContext.Facts[i]
		}
	}
	if fact == nil {
		t.Fatal("no node_blast_radius fact for the multi-pressure node")
	}
	gotCats := map[issuesapi.Category]bool{}
	for _, r := range fact.RelatedIssues {
		gotCats[r.Category] = true
	}
	if !gotCats[issuesapi.CategoryOOMKilled] || !gotCats[issuesapi.CategoryContainerWaiting] {
		t.Fatalf("multi-pressure node must link BOTH memory (OOM) and disk (container_waiting) pods, got %+v", fact.RelatedIssues)
	}
}

func TestNodeBlastRadiusContext_NoPodsNoFact(t *testing.T) {
	node := Issue{Kind: "Node", Name: "lonely", Category: issuesapi.CategoryNodeNotReady, Severity: SeverityCritical, Reason: "MemoryPressure"}
	p := &fakeProvider{}
	out := enrichDiagnosticContext([]Issue{node}, []Issue{node}, nil, p)
	if out[0].DiagnosticContext != nil {
		t.Fatalf("a node with no on-node issues should get no context, got %+v", out[0].DiagnosticContext)
	}
}

func TestPVCBlastRadiusContext(t *testing.T) {
	pvc := Issue{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data", Category: issuesapi.CategoryPVCPending, Severity: SeverityCritical, Reason: "Pending"}
	// Unschedulable WITH volume-binding evidence in the message → linked.
	blocked := Issue{ID: "unsched-1", Kind: "Pod", Namespace: "prod", Name: "db-0", Category: issuesapi.CategoryUnschedulable, Severity: SeverityCritical,
		Message: "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims"}
	// Unrelated crashloop on the same pod → never PVC-attributable.
	crashing := Issue{ID: "crash-2", Kind: "Pod", Namespace: "prod", Name: "db-0", Category: issuesapi.CategoryCrashLoop, Severity: SeverityCritical}
	// A DIFFERENT pod that also mounts the PVC but is unschedulable for CPU →
	// must NOT be attributed to the PVC. Its message even contains the bare claim
	// name "data" (unquoted) to guard against a substring false-match — only the
	// QUOTED name or volume-binding phrasing counts as evidence.
	cpuBlocked := Issue{ID: "unsched-2", Kind: "Pod", Namespace: "prod", Name: "db-1", Category: issuesapi.CategoryUnschedulable, Severity: SeverityCritical,
		Message: "0/3 nodes are available: 3 Insufficient cpu for the data tier"}

	p := &fakeProvider{podsMountingPVC: map[string][]Ref{"prod/data": {
		{Kind: "Pod", Namespace: "prod", Name: "db-0"},
		{Kind: "Pod", Namespace: "prod", Name: "db-1"},
	}}}

	out := enrichDiagnosticContext([]Issue{pvc}, []Issue{pvc, blocked, crashing, cpuBlocked}, nil, p)
	ctx := out[0].DiagnosticContext
	if ctx == nil {
		t.Fatal("PVC issue got no diagnostic context")
	}
	var fact *issuesapi.DiagnosticFact
	for i := range ctx.Facts {
		if ctx.Facts[i].Type == factPVCBlastRadius {
			fact = &ctx.Facts[i]
		}
	}
	if fact == nil {
		t.Fatalf("no pvc_blast_radius fact, got %+v", ctx.Facts)
	}
	if fact.Confidence != issuesapi.ConfidenceHigh {
		t.Errorf("confidence = %q, want high (declared claimName edge)", fact.Confidence)
	}
	// Only the volume-evidenced unschedulable links; the unrelated crashloop and
	// the CPU-unschedulable pod (no volume evidence) must not be attributed.
	if len(fact.RelatedIssues) != 1 || fact.RelatedIssues[0].Ref.Name != "db-0" || fact.RelatedIssues[0].Category != issuesapi.CategoryUnschedulable {
		t.Fatalf("expected only the volume-evidenced unschedulable (db-0) linked, got %+v", fact.RelatedIssues)
	}
}

func TestBlastRadiusCountsFoldedPods(t *testing.T) {
	// Five pods of one Deployment all mount the PVC and all fail to mount it.
	// GroupIssues folds them under one issue ID, so they collapse to ONE related
	// row — but it must carry Count = the distinct affected pods, not silently
	// drop pods 2..5 and show no count (the pre-fix behavior).
	pvc := Issue{Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data", Category: issuesapi.CategoryPVCPending, Severity: SeverityCritical, Reason: "Pending"}
	const groupID = "dep/prod/web/volume_mount_failed"
	var flat []Issue
	var pods []Ref
	for _, name := range []string{"web-a", "web-b", "web-c", "web-d", "web-e"} {
		flat = append(flat, Issue{ID: groupID, Kind: "Pod", Namespace: "prod", Name: name, Category: issuesapi.CategoryVolumeMountFailed, Severity: SeverityCritical, Reason: "FailedMount"})
		pods = append(pods, Ref{Kind: "Pod", Namespace: "prod", Name: name})
	}
	// The grouped representative the folded pods resolve to (subject = the owning Deployment).
	grouped := Issue{ID: groupID, Kind: "Deployment", Namespace: "prod", Name: "web", Category: issuesapi.CategoryVolumeMountFailed, Severity: SeverityCritical, Reason: "FailedMount"}

	p := &fakeProvider{podsMountingPVC: map[string][]Ref{"prod/data": pods}}
	out := enrichDiagnosticContext([]Issue{pvc}, append([]Issue{pvc}, flat...), []Issue{grouped}, p)

	var fact *issuesapi.DiagnosticFact
	for i := range out[0].DiagnosticContext.Facts {
		if out[0].DiagnosticContext.Facts[i].Type == factPVCBlastRadius {
			fact = &out[0].DiagnosticContext.Facts[i]
		}
	}
	if fact == nil {
		t.Fatal("no pvc_blast_radius fact")
	}
	if len(fact.RelatedIssues) != 1 {
		t.Fatalf("five folded pods must collapse to one related row, got %d", len(fact.RelatedIssues))
	}
	if fact.RelatedIssues[0].Ref.Kind != "Deployment" || fact.RelatedIssues[0].Ref.Name != "web" {
		t.Errorf("related ref = %+v, want the grouped Deployment", fact.RelatedIssues[0].Ref)
	}
	if fact.RelatedIssues[0].Count != 5 {
		t.Errorf("Count = %d, want 5 distinct affected pods", fact.RelatedIssues[0].Count)
	}
}

func TestAPIServiceHPABlastRadius(t *testing.T) {
	// A down metrics APIService links the HPAs that can't fetch metrics — but NOT
	// an HPA merely capped at maxReplicas, and NOT a non-metrics aggregated API.
	// Production shape: the HPA detector sets Reason to the problem class
	// ("cannot-scale" / "maxed") and puts the controller condition reason
	// (FailedGet*Metric: ...) in Message — the gate matches on Reason+Message.
	apisvc := Issue{Kind: "APIService", Group: "apiregistration.k8s.io", Name: "v1beta1.metrics.k8s.io", Category: issuesapi.CategoryAPIServiceUnavailable, Severity: SeverityCritical, Reason: "FailedDiscoveryCheck"}
	starved := Issue{ID: "hpa-1", Kind: "HorizontalPodAutoscaler", Namespace: "prod", Name: "web", Category: issuesapi.CategoryHPALimitedOrFailed, Severity: SeverityWarning,
		Reason: "cannot-scale", Message: "FailedGetResourceMetric: unable to get metrics for resource cpu"}
	capped := Issue{ID: "hpa-2", Kind: "HorizontalPodAutoscaler", Namespace: "prod", Name: "api", Category: issuesapi.CategoryHPALimitedOrFailed, Severity: SeverityWarning,
		Reason: "maxed", Message: "3/3 replicas: HPA is capped at maxReplicas=3"}

	p := &fakeProvider{}
	out := enrichDiagnosticContext([]Issue{apisvc}, []Issue{apisvc, starved, capped}, nil, p)
	ctx := out[0].DiagnosticContext
	if ctx == nil {
		t.Fatal("metrics APIService got no diagnostic context")
	}
	var fact *issuesapi.DiagnosticFact
	for i := range ctx.Facts {
		if ctx.Facts[i].Type == factAPIServiceHPA {
			fact = &ctx.Facts[i]
		}
	}
	if fact == nil {
		t.Fatalf("no apiservice_hpa fact, got %+v", ctx.Facts)
	}
	if fact.Confidence != issuesapi.ConfidenceMedium {
		t.Errorf("confidence = %q, want medium (no declared HPA→APIService edge)", fact.Confidence)
	}
	// Only the metrics-starved HPA links; the maxReplicas-capped one is not
	// metrics-caused and must not be attributed to the APIService outage.
	if len(fact.RelatedIssues) != 1 || fact.RelatedIssues[0].Ref.Name != "web" {
		t.Fatalf("expected only the metrics-starved HPA (web) linked, got %+v", fact.RelatedIssues)
	}
}

func TestAPIServiceHPA_NonMetricsAPILinksNothing(t *testing.T) {
	// A non-metrics aggregated APIService outage doesn't starve HPAs — no link,
	// even with a metrics-starved HPA present.
	apisvc := Issue{Kind: "APIService", Group: "apiregistration.k8s.io", Name: "v1.packages.operators.coreos.com", Category: issuesapi.CategoryAPIServiceUnavailable, Severity: SeverityCritical, Reason: "FailedDiscoveryCheck"}
	starved := Issue{ID: "hpa-1", Kind: "HorizontalPodAutoscaler", Namespace: "prod", Name: "web", Category: issuesapi.CategoryHPALimitedOrFailed, Severity: SeverityWarning,
		Reason: "cannot-scale", Message: "FailedGetResourceMetric: unable to get metrics for resource cpu"}
	p := &fakeProvider{}
	out := enrichDiagnosticContext([]Issue{apisvc}, []Issue{apisvc, starved}, nil, p)
	if out[0].DiagnosticContext != nil {
		t.Fatalf("a non-metrics APIService must not link HPAs, got %+v", out[0].DiagnosticContext)
	}
}

func TestAPIServiceHPA_MetricFamilyMustMatch(t *testing.T) {
	// The resource-metrics API (metrics.k8s.io) is down, but the only failing HPA
	// fails on an EXTERNAL metric — a different failure domain. Linking them would
	// point the operator at the wrong API, so nothing must link.
	apisvc := Issue{Kind: "APIService", Group: "apiregistration.k8s.io", Name: "v1beta1.metrics.k8s.io", Category: issuesapi.CategoryAPIServiceUnavailable, Severity: SeverityCritical, Reason: "FailedDiscoveryCheck"}
	external := Issue{ID: "hpa-x", Kind: "HorizontalPodAutoscaler", Namespace: "prod", Name: "queue", Category: issuesapi.CategoryHPALimitedOrFailed, Severity: SeverityWarning,
		Reason: "cannot-scale", Message: "FailedGetExternalMetric: unable to get external metric prod/queue_depth"}
	p := &fakeProvider{}
	out := enrichDiagnosticContext([]Issue{apisvc}, []Issue{apisvc, external}, nil, p)
	if out[0].DiagnosticContext != nil {
		t.Fatalf("resource-metrics outage must not link an external-metric HPA, got %+v", out[0].DiagnosticContext)
	}
}

func TestAPIServiceHPA_MissingRequestNotAttributed(t *testing.T) {
	// metrics.k8s.io is down, but this HPA's FailedGetResourceMetric is really the
	// missing-resource-request config case — the outage doesn't cause it, so it
	// must not be attributed to the APIService.
	apisvc := Issue{Kind: "APIService", Group: "apiregistration.k8s.io", Name: "v1beta1.metrics.k8s.io", Category: issuesapi.CategoryAPIServiceUnavailable, Severity: SeverityCritical, Reason: "FailedDiscoveryCheck"}
	noReq := Issue{ID: "hpa-r", Kind: "HorizontalPodAutoscaler", Namespace: "prod", Name: "web", Category: issuesapi.CategoryHPALimitedOrFailed, Severity: SeverityWarning,
		Reason: "cannot-scale", Message: "FailedGetResourceMetric: failed to get cpu utilization: missing request for cpu"}
	p := &fakeProvider{}
	out := enrichDiagnosticContext([]Issue{apisvc}, []Issue{apisvc, noReq}, nil, p)
	if out[0].DiagnosticContext != nil {
		t.Fatalf("a missing-request HPA failure must not be attributed to the metrics API, got %+v", out[0].DiagnosticContext)
	}
}

func TestAPIServiceHPA_ContainerResourceMetric(t *testing.T) {
	// ContainerResource HPA metrics fail with FailedGetContainerResourceMetric and
	// are also served by metrics.k8s.io — they must link under the resource family.
	apisvc := Issue{Kind: "APIService", Group: "apiregistration.k8s.io", Name: "v1beta1.metrics.k8s.io", Category: issuesapi.CategoryAPIServiceUnavailable, Severity: SeverityCritical, Reason: "FailedDiscoveryCheck"}
	hpa := Issue{ID: "hpa-c", Kind: "HorizontalPodAutoscaler", Namespace: "prod", Name: "web", Category: issuesapi.CategoryHPALimitedOrFailed, Severity: SeverityWarning,
		Reason: "cannot-scale", Message: "FailedGetContainerResourceMetric: unable to get container metric for cpu"}
	p := &fakeProvider{}
	out := enrichDiagnosticContext([]Issue{apisvc}, []Issue{apisvc, hpa}, nil, p)
	if out[0].DiagnosticContext == nil {
		t.Fatal("a FailedGetContainerResourceMetric HPA must link to the down metrics.k8s.io API")
	}
}

func TestMetricsAPIFamily_ExactGroupMatch(t *testing.T) {
	// A spoofed / nested name must NOT classify as a metrics API — the group is
	// exact-matched, not substring-matched.
	cases := map[string]string{
		"v1beta1.metrics.k8s.io":              "resource",
		"v1beta1.custom.metrics.k8s.io":       "custom",
		"v1beta1.external.metrics.k8s.io":     "external",
		"v1.external.metrics.k8s.io.evil.com": "",
		"v1.foo.metrics.k8s.io":               "",
		"v1.apps":                             "",
	}
	for name, want := range cases {
		got := metricsAPIFamily(Issue{Kind: "APIService", Name: name, Category: issuesapi.CategoryAPIServiceUnavailable})
		if got != want {
			t.Errorf("metricsAPIFamily(%q) = %q, want %q", name, got, want)
		}
	}
}

func hi(id string) issuesapi.IncidentParent {
	return issuesapi.IncidentParent{ID: id, Confidence: issuesapi.ConfidenceHigh}
}
func med(id string) issuesapi.IncidentParent {
	return issuesapi.IncidentParent{ID: id, Confidence: issuesapi.ConfidenceMedium}
}

func TestBestIncidentParent(t *testing.T) {
	cases := []struct {
		name   string
		ps     []issuesapi.IncidentParent
		wantID string // "" = expect omit
	}{
		{"single high", []issuesapi.IncidentParent{hi("A")}, "A"},
		{"high beats medium", []issuesapi.IncidentParent{med("B"), hi("A")}, "A"},
		{"same root twice collapses", []issuesapi.IncidentParent{med("A"), med("A")}, "A"},
		{"distinct same-tier omit (no severity tiebreak)", []issuesapi.IncidentParent{med("A"), med("B")}, ""},
		{"distinct high tie omit", []issuesapi.IncidentParent{hi("A"), hi("B")}, ""},
		{"empty omit", nil, ""},
	}
	for _, tc := range cases {
		got, ok := bestIncidentParent(tc.ps)
		if tc.wantID == "" {
			if ok {
				t.Errorf("%s: expected omit, got %q", tc.name, got.ID)
			}
			continue
		}
		if !ok || got.ID != tc.wantID {
			t.Errorf("%s: got (%q, %v), want %q", tc.name, got.ID, ok, tc.wantID)
		}
	}
}

func findByID(out []Issue, id string) *Issue {
	for i := range out {
		if out[i].ID == id {
			return &out[i]
		}
	}
	return nil
}

func TestIncidentParent_PVCHighPointer(t *testing.T) {
	pvc := Issue{ID: "pvc-1", Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data", Category: issuesapi.CategoryPVCPending, Severity: SeverityCritical, Reason: "Pending"}
	sym := Issue{ID: "sym-1", Kind: "Pod", Namespace: "prod", Name: "db-0", Category: issuesapi.CategoryUnschedulable, Severity: SeverityCritical,
		Message: "0/3 nodes are available: pod has unbound immediate PersistentVolumeClaims"}
	p := &fakeProvider{podsMountingPVC: map[string][]Ref{"prod/data": {{Kind: "Pod", Namespace: "prod", Name: "db-0"}}}}
	out := enrichDiagnosticContext([]Issue{pvc, sym}, []Issue{pvc, sym}, []Issue{pvc, sym}, p)
	got := findByID(out, "sym-1")
	if got.IncidentParent == nil {
		t.Fatal("symptom pod got no IncidentParent")
	}
	if got.IncidentParent.ID != "pvc-1" || got.IncidentParent.Confidence != issuesapi.ConfidenceHigh || got.IncidentParent.FactType != factPVCBlastRadius {
		t.Errorf("IncidentParent = %+v, want pvc-1/high/pvc_blast_radius", *got.IncidentParent)
	}
	// The root itself must NOT get a parent.
	if findByID(out, "pvc-1").IncidentParent != nil {
		t.Error("the PVC root must not get an IncidentParent")
	}
}

func TestIncidentParent_FlatViewNoPointer(t *testing.T) {
	// Ungrouped mode (?view=flat, per-resource regroup): grouped is nil, so the
	// whole-row coverage gate can't be evaluated (members share an ID, Count 0).
	// incident_parent must NOT be assigned — it's a grouped-model property.
	pvc := Issue{ID: "pvc-1", Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data", Category: issuesapi.CategoryPVCPending, Severity: SeverityCritical, Reason: "Pending"}
	pod1 := Issue{ID: "g", Kind: "Pod", Namespace: "prod", Name: "db-0", Category: issuesapi.CategoryUnschedulable, Severity: SeverityCritical, Message: "pod has unbound immediate PersistentVolumeClaims"}
	pod2 := Issue{ID: "g", Kind: "Pod", Namespace: "prod", Name: "db-1", Category: issuesapi.CategoryUnschedulable, Severity: SeverityCritical, Message: "pod has unbound immediate PersistentVolumeClaims"}
	p := &fakeProvider{podsMountingPVC: map[string][]Ref{"prod/data": {{Kind: "Pod", Namespace: "prod", Name: "db-0"}, {Kind: "Pod", Namespace: "prod", Name: "db-1"}}}}
	out := enrichDiagnosticContext([]Issue{pvc, pod1, pod2}, []Issue{pvc, pod1, pod2}, nil, p) // grouped == nil → ungrouped path
	for _, i := range out {
		if i.IncidentParent != nil {
			t.Fatalf("ungrouped mode must not assign incident_parent, got one on %s/%s", i.Kind, i.Name)
		}
	}
}

func TestIncidentParent_CoverageGate(t *testing.T) {
	// A grouped Deployment unschedulable issue (3 member pods) gets the pointer
	// only when ALL 3 are explained by the PVC; if only 2 mount it, the row is
	// mixed-cause and the pointer is omitted (whole-row coverage).
	mk := func(podsMounting int) []Issue {
		pvc := Issue{ID: "pvc-1", Kind: "PersistentVolumeClaim", Namespace: "prod", Name: "data", Category: issuesapi.CategoryPVCPending, Severity: SeverityCritical, Reason: "Pending"}
		dep := Issue{ID: "g", Kind: "Deployment", Namespace: "prod", Name: "web", Category: issuesapi.CategoryUnschedulable, Severity: SeverityCritical, Count: 3}
		flat := []Issue{pvc}
		var mounting []Ref
		for n, name := range []string{"web-a", "web-b", "web-c"} {
			flat = append(flat, Issue{ID: "g", Kind: "Pod", Namespace: "prod", Name: name, Category: issuesapi.CategoryUnschedulable, Severity: SeverityCritical,
				Message: "pod has unbound immediate PersistentVolumeClaims"})
			if n < podsMounting {
				mounting = append(mounting, Ref{Kind: "Pod", Namespace: "prod", Name: name})
			}
		}
		p := &fakeProvider{podsMountingPVC: map[string][]Ref{"prod/data": mounting}}
		return enrichDiagnosticContext([]Issue{pvc, dep}, flat, []Issue{dep}, p)
	}
	if got := findByID(mk(3), "g").IncidentParent; got == nil || got.ID != "pvc-1" {
		t.Errorf("full coverage (3/3) should link, got %+v", got)
	}
	if got := findByID(mk(2), "g").IncidentParent; got != nil {
		t.Errorf("partial coverage (2/3) must omit the pointer, got %+v", *got)
	}
}

func TestIncidentParent_NodeMediumPointer(t *testing.T) {
	node := Issue{ID: "node-1", Kind: "Node", Name: "n1", Category: issuesapi.CategoryNodeNotReady, Severity: SeverityCritical, Reason: "MemoryPressure"}
	sym := Issue{ID: "oom-1", Kind: "Pod", Namespace: "prod", Name: "web-a", Category: issuesapi.CategoryOOMKilled, Severity: SeverityCritical}
	p := &fakeProvider{podsOnNode: map[string][]Ref{"n1": {{Kind: "Pod", Namespace: "prod", Name: "web-a"}}}}
	out := enrichDiagnosticContext([]Issue{node, sym}, []Issue{node, sym}, []Issue{node, sym}, p)
	got := findByID(out, "oom-1")
	if got.IncidentParent == nil || got.IncidentParent.ID != "node-1" || got.IncidentParent.Confidence != issuesapi.ConfidenceMedium {
		t.Fatalf("expected medium IncidentParent → node-1, got %+v", got.IncidentParent)
	}
}

func TestIncidentParent_SelectedBackendNoPointer(t *testing.T) {
	// A Service with no endpoints because its backend pods crashloop — the pods
	// are the CAUSE, not a symptom of the Service, so NO reverse pointer.
	svc := Issue{ID: "svc-1", Kind: "Service", Namespace: "prod", Name: "api", Category: issuesapi.CategoryServiceNoEndpoints, Severity: SeverityCritical, Reason: "0/2 selected pods ready"}
	pod := Issue{ID: "crash-1", Kind: "Pod", Namespace: "prod", Name: "api-x", Category: issuesapi.CategoryCrashLoop, Severity: SeverityCritical}
	p := &fakeProvider{selectedPods: map[string][]Ref{"prod/api": {{Kind: "Pod", Namespace: "prod", Name: "api-x"}}}}
	out := enrichDiagnosticContext([]Issue{svc, pod}, []Issue{svc, pod}, nil, p)
	if got := findByID(out, "crash-1"); got.IncidentParent != nil {
		t.Fatalf("selected_backend must not create a reverse pointer (inverted direction), got %+v", *got.IncidentParent)
	}
}

func TestSecretProducerContext(t *testing.T) {
	// A not-ready Certificate links the pod whose missing-ref names its Secret,
	// but NOT a pod that references the Secret yet fails for an unrelated reason.
	cert := Issue{ID: "cert-1", Kind: "Certificate", Group: "cert-manager.io", Namespace: "prod", Name: "web-tls", Category: issuesapi.CategoryCertificateNotReady, Severity: SeverityCritical, Reason: "Issuing"}
	blocked := Issue{ID: "mr-1", Kind: "Pod", Namespace: "prod", Name: "web-a", Category: issuesapi.CategoryMissingConfigRef, Severity: SeverityCritical,
		Message: `Secret "web-tls-secret" referenced in volume does not exist`}
	unrelated := Issue{ID: "cw-1", Kind: "Pod", Namespace: "prod", Name: "web-b", Category: issuesapi.CategoryContainerWaiting, Severity: SeverityWarning,
		Reason: "ContainerCreating", Message: "waiting on something else entirely"}
	// References Secret web-tls-secret but actually fails on a ConfigMap of the SAME
	// name — the quoted name appears, but not in a `secret "…"` context, so it must
	// NOT be attributed to the Certificate.
	sameName := Issue{ID: "cm-1", Kind: "Pod", Namespace: "prod", Name: "web-c", Category: issuesapi.CategoryMissingConfigRef, Severity: SeverityCritical,
		Message: `envFrom references ConfigMap "web-tls-secret" which does not exist`}

	p := &fakeProvider{secretProducer: map[string]secretProducerResult{
		"prod/web-tls": {name: "web-tls-secret", pods: []Ref{
			{Kind: "Pod", Namespace: "prod", Name: "web-a"},
			{Kind: "Pod", Namespace: "prod", Name: "web-b"},
			{Kind: "Pod", Namespace: "prod", Name: "web-c"},
		}},
	}}
	out := enrichDiagnosticContext([]Issue{cert, blocked, unrelated, sameName}, []Issue{cert, blocked, unrelated, sameName}, []Issue{cert, blocked, unrelated, sameName}, p)

	root := findByID(out, "cert-1")
	var fact *issuesapi.DiagnosticFact
	for i := range root.DiagnosticContext.Facts {
		if root.DiagnosticContext.Facts[i].Type == factSecretNotReady {
			fact = &root.DiagnosticContext.Facts[i]
		}
	}
	if fact == nil || len(fact.RelatedIssues) != 1 || fact.RelatedIssues[0].Ref.Name != "web-a" {
		t.Fatalf("expected only the Secret-naming pod linked, got %+v", fact)
	}
	if got := findByID(out, "mr-1"); got.IncidentParent == nil || got.IncidentParent.ID != "cert-1" || got.IncidentParent.Confidence != issuesapi.ConfidenceHigh {
		t.Fatalf("blocked pod should point to the Certificate (high), got %+v", got.IncidentParent)
	}
	if got := findByID(out, "cw-1"); got.IncidentParent != nil {
		t.Fatalf("unrelated container-waiting pod must not be attributed, got %+v", *got.IncidentParent)
	}
	if got := findByID(out, "cm-1"); got.IncidentParent != nil {
		t.Fatalf("same-named ConfigMap failure must not be attributed to the Secret, got %+v", *got.IncidentParent)
	}
}

func TestSymptomNamesSecret(t *testing.T) {
	mk := func(msg, ns string) Issue { return Issue{Namespace: ns, Message: msg} }
	cases := []struct {
		msg, ns string
		want    bool
	}{
		{`references Secret "foo" which does not exist`, "prod", true},
		{`MountVolume.SetUp failed: secret "foo" not found`, "prod", true},
		{`secrets "foo" not found`, "prod", true},                          // plural kubelet path
		{`couldn't find key tls.crt in Secret prod/foo`, "prod", true},     // namespaced missing-key
		{`references ConfigMap "foo" which does not exist`, "prod", false}, // same name, wrong kind
		{`waiting on something else`, "prod", false},
	}
	for _, c := range cases {
		if got := symptomNamesSecret(mk(c.msg, c.ns), "foo"); got != c.want {
			t.Errorf("symptomNamesSecret(%q) = %v, want %v", c.msg, got, c.want)
		}
	}
}
