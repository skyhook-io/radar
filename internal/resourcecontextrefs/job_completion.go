package resourcecontextrefs

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

// The running container is the evidence that distinguishes a stuck sidecar from work.
const runningPastCompletionNote = "A container finished while a sibling keeps running. " +
	"Inspect the running container's image, command, and logs to tell a stuck long-lived sidecar " +
	"(which would recur every run) from a container that is still doing work (a slow-but-healthy Job)."

// RunningPastCompletionFromShape maps the k8s observation onto the wire type.
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
