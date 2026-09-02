package mcp

import (
	"sort"
	"strings"
	"testing"
)

func TestNormalizeWorkloadKindAcceptsRollout(t *testing.T) {
	for _, in := range []string{"rollout", "Rollout", "ROLLOUTS", "rollouts"} {
		if got := normalizeWorkloadKind(in); got != "rollouts" {
			t.Errorf("normalizeWorkloadKind(%q) = %q, want rollouts", in, got)
		}
	}
	if got := normalizeWorkloadKind("replicaset"); got != "" {
		t.Errorf("normalizeWorkloadKind(replicaset) = %q, want empty", got)
	}
}

// Overlapping verbs would give an agent two paths to one operation.
func TestRolloutActionsAllowlist(t *testing.T) {
	want := []string{"abort", "promote", "promote-full", "retry", "skip-step"}

	got := make([]string, 0, len(rolloutActions))
	for action, op := range rolloutActions {
		if op == nil {
			t.Errorf("action %q has a nil operation", action)
		}
		got = append(got, action)
	}
	sort.Strings(got)

	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("actions = %v, want %v", got, want)
	}

	for _, overlapping := range []string{"rollback", "restart", "scale", "undo"} {
		if _, present := rolloutActions[overlapping]; present {
			t.Errorf("action %q duplicates manage_workload", overlapping)
		}
	}
}

func TestRevisionCapableKind(t *testing.T) {
	for _, kind := range []string{"deployment", "deployments", "statefulset", "daemonset", "rollout", "Rollout"} {
		if !revisionCapableKind(kind) {
			t.Errorf("revisionCapableKind(%q) = false, want true", kind)
		}
	}
	for _, kind := range []string{"pod", "service", "replicaset", "job", "cronjob"} {
		if revisionCapableKind(kind) {
			t.Errorf("revisionCapableKind(%q) = true, want false", kind)
		}
	}
}

// include=revisions has to be in the known-token set, or attachResourceExtras
// reports it as an unknown include alongside the data it just attached.
func TestRevisionsIsAKnownIncludeToken(t *testing.T) {
	result := map[string]any{}
	attachResourceExtras(t.Context(), nil, result, map[string]bool{"revisions": true}, "pod", "", "default", "web")

	if msg, present := result["includeError"]; present {
		t.Errorf("revisions reported as unknown include: %v", msg)
	}
}
