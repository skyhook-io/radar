// Package workloadrestart binds a restart marker to the workload the caller reviewed.
package workloadrestart

import (
	"errors"

	"k8s.io/apimachinery/pkg/types"
)

type Preconditions struct {
	UID             types.UID `json:"uid"`
	ResourceVersion string    `json:"resourceVersion,omitempty"`
}

type Result struct {
	RestartedAt string `json:"restartedAt"`
	Changed     bool   `json:"changed"`
}

var (
	ErrChanged   = errors.New("the workload changed after review")
	ErrNotStored = errors.New("Kubernetes did not store the requested restart marker")
)
