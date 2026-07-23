package resourcecontextrefs

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// runningPastCompletionNote is a fixed, fact-only disambiguation hint. It states NO
// verdict ("blocked"/"cannot complete") and recommends NO remediation — those belong
// to the time-gated SidecarBlocksJobCompletion issue. It only tells the reader how to
// tell a stuck sidecar from a slow-but-working Job: read the running container.
const runningPastCompletionNote = "A container finished while a sibling keeps running. " +
	"Inspect the running container's image, command, and logs to tell a stuck long-lived sidecar " +
	"(which would recur every run) from a container that is still doing work (a slow-but-healthy Job)."

// RunningPastCompletionFromShape maps the neutral k8s container-split observation onto
// the resource-context wire type. It is a lead an agent may act on only after checking
// the running container itself — never a causal claim.
func RunningPastCompletionFromShape(shape *k8s.RunningPastCompletionShape) *resourcecontext.RunningPastCompletion {
	if shape == nil {
		return nil
	}
	return &resourcecontext.RunningPastCompletion{
		Pod:                shape.Pod,
		Job:                shape.Job,
		CompletedContainer: shape.CompletedContainer,
		RunningContainer:   shape.RunningContainer,
		ActiveJobs:         shape.ActiveJobs,
		SinceSeconds:       shape.SinceSeconds,
		Note:               runningPastCompletionNote,
	}
}
