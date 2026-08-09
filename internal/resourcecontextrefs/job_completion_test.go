package resourcecontextrefs

import (
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
)

func TestContainerCompletionSplitFromShape(t *testing.T) {
	if got := ContainerCompletionSplitFromShape(nil); got != nil {
		t.Fatalf("nil shape must map to nil, got %+v", got)
	}

	out := ContainerCompletionSplitFromShape(&k8s.ContainerCompletionSplitShape{
		Pod: "audit-log-archiver-1-pod", Job: "audit-log-archiver-1",
		ExitedContainer: "archiver", RunningContainers: []string{"fluent-bit-sidecar", "uploader"}, SinceSeconds: 360,
	})
	if out == nil {
		t.Fatal("want a mapped observation, got nil")
	}
	if out.Pod != "audit-log-archiver-1-pod" || out.ExitedContainer != "archiver" ||
		!slices.Equal(out.RunningContainers, []string{"fluent-bit-sidecar", "uploader"}) || out.SinceSeconds != 360 {
		t.Fatalf("fields not mapped through: %+v", out)
	}

	// The Note must stay a fact-only disambiguation hint: causal claims and
	// remediation belong to the time-gated issue.
	note := strings.ToLower(out.Note)
	for _, forbidden := range []string{"blocked", "cannot", "initcontainer", "restartpolicy", "kep-753", "should be"} {
		if strings.Contains(note, forbidden) {
			t.Errorf("Note is not fact-only; contains %q: %q", forbidden, out.Note)
		}
	}
	for _, want := range []string{"exited successfully", "finish time", "inspect", "legitimate work", "sidecar"} {
		if !strings.Contains(note, want) {
			t.Errorf("Note missing disambiguation hint %q: %q", want, out.Note)
		}
	}
}
