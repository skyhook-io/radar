package issues

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/issuesapi"
)

// The Radar Hub alert worker keys an issue's open/resolved lifecycle on
// issue.ID (radar-hub IssueSourceKey = clusterID + ":" + issue.ID): a matching
// issue that keeps one ID across polls is one continuous alert, a new ID is a
// new alert. So if the SAME underlying problem yields a DIFFERENT ID across
// polls, the worker sees spurious resolve→reopen churn and fires false
// notifications. These tests pin ID stability across the field variation a
// detector emits poll-to-poll. (Reason→category mappings are pinned in
// category_test.go; owner-else-self keying in identity_test.go; StableID
// determinism in pkg/subject — this file pins the poll-to-poll identity
// contract those compose into.)

func classifiedIssue(i Issue) Issue {
	classifyIssue(&i)
	enrichIdentity(&i)
	return i
}

// assertSameID asserts every variant resolves to one ID and one category — the
// poll-to-poll stability the lifecycle keys on.
func assertSameID(t *testing.T, variants ...Issue) {
	t.Helper()
	id, cat := variants[0].ID, variants[0].Category
	for _, v := range variants {
		if v.ID != id {
			t.Errorf("ID drift (reason %q): got %q, want %q", v.Reason, v.ID, id)
		}
		if v.Category != cat {
			t.Errorf("category drift (reason %q): got %q, want %q", v.Reason, v.Category, cat)
		}
	}
}

// A crashing container cycles its reason across polls (CrashLoopBackOff while
// backing off, Error/Failed at the instant it exits); the kubelet flaps an
// image pull across the pull-error family. Each family is one incident and must
// fold to one ID and one category, or every cycle reads as a new alert.
func TestIDStable_ReasonFamilyOscillation(t *testing.T) {
	families := []struct {
		name    string
		owner   Ref
		reasons []string
		want    issuesapi.Category
	}{
		{
			name:    "crashloop",
			owner:   Ref{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "api"},
			reasons: []string{"CrashLoopBackOff", "Error", "Failed"},
			want:    issuesapi.CategoryCrashLoop,
		},
		{
			name:    "image_pull",
			owner:   Ref{Group: "apps", Kind: "StatefulSet", Namespace: "prod", Name: "db"},
			reasons: []string{"ImagePullBackOff", "ErrImagePull", "ImageInspectError", "InvalidImageName"},
			want:    issuesapi.CategoryImagePullFailed,
		},
	}
	for _, f := range families {
		t.Run(f.name, func(t *testing.T) {
			variants := make([]Issue, len(f.reasons))
			for i, r := range f.reasons {
				variants[i] = classifiedIssue(Issue{Source: SourceProblem, Kind: "Pod", Namespace: f.owner.Namespace, Name: "pod-x", Reason: r, Owner: f.owner})
			}
			assertSameID(t, variants...)
			if variants[0].Category != f.want {
				t.Errorf("category = %q, want %q", variants[0].Category, f.want)
			}
		})
	}
}

// Symptom fields churn every poll (count climbs, message rephrases, restarts
// accrue, first-seen shifts) but MUST NOT enter the ID — identity is the cause,
// not the symptom.
func TestIDStable_SymptomFieldsDoNotRekey(t *testing.T) {
	base := Issue{
		Source: SourceProblem, Kind: "Pod", Namespace: "prod", Name: "api-1", Reason: "CrashLoopBackOff",
		Owner: Ref{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "api"},
	}
	v1 := base
	v1.Count, v1.Message, v1.RestartCount, v1.FirstSeen = 1, "back-off 10s restarting", 3, time.Unix(1700000000, 0)
	v2 := base
	v2.Count, v2.Message, v2.RestartCount, v2.FirstSeen = 47, "back-off 5m0s restarting", 219, time.Unix(1700009999, 0)
	assertSameID(t, classifiedIssue(v1), classifiedIssue(v2))
}

// The real alerting scenario: a crashlooping pod is replaced (new pod Name)
// while the workload stays broken. The ID keys on the owner, not the pod, so
// successive pods under one Deployment fold to a single continuous alert
// instead of resolve+reopen on every restart.
func TestIDStable_PodReplacementUnderSameOwner(t *testing.T) {
	owner := Ref{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "api"}
	mk := func(name string) Issue {
		return classifiedIssue(Issue{Source: SourceProblem, Kind: "Pod", Namespace: "prod", Name: name, Reason: "CrashLoopBackOff", Owner: owner})
	}
	assertSameID(t, mk("api-7d9-aaaaa"), mk("api-7d9-bbbbb"), mk("api-84f-ccccc"))
}

// Boundary: a crashlooping pod momentarily in ContainerCreating classifies as
// container_waiting — a DISTINCT ID from its crashloop identity. Intentional
// (genuinely distinct phases), and precisely why the worker's resolve grace
// must absorb a brief flap rather than treat it as resolve+reopen. Pinned so a
// classify change that merges the two is a conscious decision, not a silent
// alerting regression.
func TestID_CrashLoopVsContainerWaitingIsDistinct(t *testing.T) {
	owner := Ref{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "api"}
	mk := func(reason string) Issue {
		return classifiedIssue(Issue{Source: SourceProblem, Kind: "Pod", Namespace: "prod", Name: "api-1", Reason: reason, Owner: owner})
	}
	crash := mk("CrashLoopBackOff")
	waiting := mk("ContainerCreating")
	if waiting.Category != issuesapi.CategoryContainerWaiting {
		t.Fatalf("precondition: ContainerCreating → container_waiting, got %q", waiting.Category)
	}
	if crash.ID == waiting.ID {
		t.Error("crashloop and container_waiting now share an ID — alert lifecycle would silently merge two distinct phases")
	}
}
