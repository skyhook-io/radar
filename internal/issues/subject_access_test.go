package issues

import (
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

// A Node's issues are composed across namespaces. The caller's subject check
// must remove another namespace's rows before grouping, or their names reach
// the caller as members and blast-radius links. A Secret's own issue needs
// Secret access, not just namespace access.
func TestSubjectAccessRunsBeforeGroupingAndDiagnostics(t *testing.T) {
	p := &fakeProvider{
		problems: []k8s.Detection{
			{Kind: "Node", Name: "node-1", Severity: "critical", Reason: "MemoryPressure"},
			{Kind: "Pod", Namespace: "team-a", Name: "web", Severity: "critical", Reason: "OOMKilled"},
			{Kind: "Pod", Namespace: "team-b", Name: "api", Severity: "critical", Reason: "OOMKilled"},
			{Kind: "Secret", Namespace: "team-a", Name: "creds", Severity: "high", Reason: "Terminating"},
		},
		podsOnNode: map[string][]Ref{"node-1": {
			{Kind: "Pod", Namespace: "team-a", Name: "web"},
			{Kind: "Pod", Namespace: "team-b", Name: "api"},
		}},
	}
	allowCluster := func(string, string) bool { return true }
	teamAWithoutSecrets := func(ref Ref) bool { return ref.Namespace == "team-a" && ref.Kind != "Secret" }

	everything := Compose(p, Filters{Limit: NoLimit, Grouped: true, CanReadClusterScoped: allowCluster})
	if !hasIssueIn(everything, "team-b") || !hasIssueKind(everything, "Secret") {
		t.Fatalf("fixture: an unrestricted compose should see team-b and the Secret: %+v", everything)
	}

	scoped := Compose(p, Filters{Limit: NoLimit, Grouped: true, CanReadClusterScoped: allowCluster, CanReadSubject: teamAWithoutSecrets})
	if hasIssueIn(scoped, "team-b") || hasIssueKind(scoped, "Secret") {
		t.Fatalf("rows the caller can't open survived: %+v", scoped)
	}
	if !hasIssueIn(scoped, "team-a") || !hasIssueKind(scoped, "Node") {
		t.Fatalf("readable rows dropped: %+v", scoped)
	}

	unrestricted := RelatedIssues(p, RelatedIssueOptions{CanReadClusterScoped: allowCluster}, "", "Node", "", "node-1")
	if !linksNamespace(unrestricted, "team-b") {
		t.Fatalf("fixture: an unrestricted node lookup should link the team-b Pod: %+v", unrestricted)
	}
	node := RelatedIssues(p, RelatedIssueOptions{CanReadClusterScoped: allowCluster, CanReadSubject: teamAWithoutSecrets}, "", "Node", "", "node-1")
	if !linksNamespace(node, "team-a") {
		t.Fatalf("the readable team-a Pod is no longer linked: %+v", node)
	}
	if len(node) == 0 {
		t.Fatal("the node's own issue was dropped")
	}
	for _, issue := range node {
		for _, m := range issue.Members {
			if m.Namespace == "team-b" {
				t.Fatalf("member from an unreadable namespace: %+v", m)
			}
		}
		if issue.DiagnosticContext == nil {
			continue
		}
		for _, fact := range issue.DiagnosticContext.Facts {
			for _, ref := range fact.Refs {
				if ref.Namespace == "team-b" {
					t.Fatalf("%s links an unreadable namespace: %+v", fact.Type, ref)
				}
			}
			for _, related := range fact.RelatedIssues {
				if related.Ref.Namespace == "team-b" {
					t.Fatalf("%s links an unreadable namespace's issue: %+v", fact.Type, related)
				}
			}
		}
	}
}

func linksNamespace(list []Issue, namespace string) bool {
	for _, issue := range list {
		if issue.DiagnosticContext == nil {
			continue
		}
		for _, fact := range issue.DiagnosticContext.Facts {
			for _, ref := range fact.Refs {
				if ref.Namespace == namespace {
					return true
				}
			}
		}
	}
	return false
}

func hasIssueIn(list []Issue, namespace string) bool {
	for _, i := range list {
		if i.Namespace == namespace {
			return true
		}
		for _, m := range i.Members {
			if m.Namespace == namespace {
				return true
			}
		}
	}
	return false
}

func hasIssueKind(list []Issue, kind string) bool {
	for _, i := range list {
		if i.Kind == kind {
			return true
		}
	}
	return false
}

// A precomposed pair can come from a memo shared by callers with different
// access, so the match itself applies the cluster-scoped gate.
func TestRelatedIssuesFromGatesClusterScopedMatches(t *testing.T) {
	ns := Issue{ID: "ns", Kind: "Namespace", Name: "team-a", Reason: "Terminating"}
	lookup := func(canRead func(kind, group string) bool) int {
		return len(RelatedIssuesFrom([]Issue{ns}, []Issue{ns}, RelatedIssueOptions{CanReadClusterScoped: canRead}, "", "Namespace", "", "team-a"))
	}
	if lookup(func(string, string) bool { return false }) != 0 {
		t.Fatal("a cluster-scoped issue matched for a caller who can't list its kind")
	}
	if lookup(func(string, string) bool { return true }) != 1 {
		t.Fatal("a readable cluster-scoped issue was dropped")
	}
	if lookup(nil) != 1 {
		t.Fatal("internal composition without a gate must keep the issue")
	}
}

func TestConcernsMatchesTheSubjectOrItsOwnerByGroup(t *testing.T) {
	pod := Issue{Kind: "Pod", Namespace: "shop", Name: "web-1", Owner: Ref{Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "web"}}
	if !Concerns(pod, "", "Pod", "shop", "web-1") {
		t.Fatal("a row is about its own resource")
	}
	if !Concerns(pod, "apps", "Deployment", "shop", "web") || !Concerns(pod, "", "Deployment", "shop", "web") {
		t.Fatal("a member row is about the workload it folds into")
	}
	if Concerns(pod, "example.com", "Deployment", "shop", "web") {
		t.Fatal("a custom kind sharing the workload's kind and name matched")
	}
	service := Issue{Kind: "Service", Namespace: "shop", Name: "web"}
	if Concerns(service, "serving.knative.dev", "Service", "shop", "web") {
		t.Fatal("a Knative Service matched a core Service's issue")
	}
}

// A row's owner becomes the grouped issue's subject, so an owner the caller
// can't read must not: the kubelet makes a mirror Pod's Node its controller,
// and a namespace reader who can't list Nodes would otherwise see a Node
// issue, and lose the Pod's own.
func TestUnreadableOwnerLeavesTheRowItsOwnSubject(t *testing.T) {
	for _, tc := range []struct {
		name                    string
		owner                   k8s.Detection
		canReadNodes, canSecret bool
		wantKind, wantNamespace string
	}{
		{"unreadable cluster-scoped owner", k8s.Detection{OwnerKind: "Node", OwnerName: "node-1"}, false, false, "Pod", "team-a"},
		{"readable cluster-scoped owner", k8s.Detection{OwnerKind: "Node", OwnerName: "node-1"}, true, false, "Node", ""},
		{"unreadable namespaced owner", k8s.Detection{OwnerKind: "Secret", OwnerName: "parent"}, true, false, "Pod", "team-a"},
		{"readable namespaced owner", k8s.Detection{OwnerKind: "Secret", OwnerName: "parent"}, true, true, "Secret", "team-a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pod := tc.owner
			pod.Kind, pod.Namespace, pod.Name, pod.Severity, pod.Reason = "Pod", "team-a", "worker", "critical", "CrashLoopBackOff"
			p := &fakeProvider{problems: []k8s.Detection{pod}}
			canReadClusterScoped := func(kind, _ string) bool { return kind != "Node" || tc.canReadNodes }
			canReadSubject := func(ref Ref) bool { return ref.Namespace == "team-a" && (ref.Kind != "Secret" || tc.canSecret) }

			grouped := Compose(p, Filters{Grouped: true, Limit: NoLimit, SkipPodTemplateContext: true, CanReadSubject: canReadSubject, CanReadClusterScoped: canReadClusterScoped})
			if len(grouped) != 1 || grouped[0].Kind != tc.wantKind || grouped[0].Namespace != tc.wantNamespace {
				t.Fatalf("grouped = %+v, want one %s issue in %q", grouped, tc.wantKind, tc.wantNamespace)
			}
			related := RelatedIssues(p, RelatedIssueOptions{SkipPodTemplateContext: true, CanReadSubject: canReadSubject, CanReadClusterScoped: canReadClusterScoped}, "", "Pod", "team-a", "worker")
			if len(related) != 1 || related[0].ID != grouped[0].ID {
				t.Fatalf("the Pod's own lookup = %+v, want the grouped issue %s", related, grouped[0].ID)
			}
		})
	}
}
