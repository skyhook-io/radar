package resourcecontextrefs

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
)

const containerCompletionSplitNote = "A regular container exited successfully while one or more siblings keep running. " +
	"The reported age is measured from the most recent successful exit's finish time. " +
	"Inspect the running containers' images, commands, and logs: they may include long-lived sidecars, " +
	"or they may still be doing legitimate work."

// ContainerCompletionSplitFromShape maps the k8s observation onto the wire type.
func ContainerCompletionSplitFromShape(shape *k8s.ContainerCompletionSplitShape) *resourcecontext.ContainerCompletionSplit {
	if shape == nil {
		return nil
	}
	return &resourcecontext.ContainerCompletionSplit{
		Pod:               shape.Pod,
		Job:               shape.Job,
		ExitedContainer:   shape.ExitedContainer,
		RunningContainers: shape.RunningContainers,
		SinceSeconds:      shape.SinceSeconds,
		Note:              containerCompletionSplitNote,
	}
}
