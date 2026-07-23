package resourcecontextrefs

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestRunningPastCompletionFromShape(t *testing.T) {
	if got := RunningPastCompletionFromShape(nil); got != nil {
		t.Fatalf("nil shape must map to nil, got %+v", got)
	}

	out := RunningPastCompletionFromShape(&k8s.RunningPastCompletionShape{
		Pod: "audit-log-archiver-1-pod", Job: "audit-log-archiver-1",
		CompletedContainer: "archiver", RunningContainer: "fluent-bit-sidecar",
		ActiveJobs: 2, SinceSeconds: 360,
	})
	if out == nil {
		t.Fatal("want a mapped observation, got nil")
	}
	if out.Pod != "audit-log-archiver-1-pod" || out.CompletedContainer != "archiver" ||
		out.RunningContainer != "fluent-bit-sidecar" || out.ActiveJobs != 2 || out.SinceSeconds != 360 {
		t.Fatalf("fields not mapped through: %+v", out)
	}

	// The Note must stay a fact-only disambiguation hint: it must NOT assert the Job is
	// blocked/cannot complete, and must NOT recommend remediation (that is the confident
	// SidecarBlocksJobCompletion issue's job). Regression guard for the review that
	// stripped the verdict/remediation wording.
	note := strings.ToLower(out.Note)
	for _, forbidden := range []string{"blocked", "cannot", "succeeded", "initcontainer", "restartpolicy", "kep-753", "should be"} {
		if strings.Contains(note, forbidden) {
			t.Errorf("Note is not fact-only; contains %q: %q", forbidden, out.Note)
		}
	}
	for _, want := range []string{"inspect", "still doing work", "sidecar"} {
		if !strings.Contains(note, want) {
			t.Errorf("Note missing disambiguation hint %q: %q", want, out.Note)
		}
	}
}
