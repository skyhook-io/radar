package resourcecontextrefs

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

const containerCompletionSplitNote = "A regular container exited successfully while a sibling keeps running. " +
	"The reported age is measured from the exited container's finish time. " +
	"Inspect the running container's image, command, and logs: it may be a long-lived sidecar, " +
	"or it may still be doing legitimate work."

// ContainerCompletionSplitFromShape maps the k8s observation onto the wire type.
func ContainerCompletionSplitFromShape(shape *k8s.ContainerCompletionSplitShape) *resourcecontext.ContainerCompletionSplit {
	if shape == nil {
		return nil
	}
	return &resourcecontext.ContainerCompletionSplit{
		Pod:              shape.Pod,
		Job:              shape.Job,
		ExitedContainer:  shape.ExitedContainer,
		RunningContainer: shape.RunningContainer,
		SinceSeconds:     shape.SinceSeconds,
		Note:             containerCompletionSplitNote,
	}
}
