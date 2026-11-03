package topology

import (
	"fmt"
	"log"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/jobset"
)

// jobSetNodeID uses the generic CRD pass's ID form for a custom resource. The
// generic pass recognizes an already drawn JobSet by its resource key, not this
// ID, and only adds an owner's edge to it.
func jobSetNodeID(namespace, name string) string {
	return fmt.Sprintf("jobset/%s/%s/%s", namespace, name, jobset.Group)
}

// jobSetRoots indexes the drawn JobSets by namespace/name so a Job's
// controller reference can be checked against the exact incarnation.
type jobSetRoots map[string]*unstructured.Unstructured

// addJobSetNodes draws every JobSet in scope, with or without children: a
// JobSet is a root nothing else in the graph points at.
func (b *Builder) addJobSetNodes(nodes []Node, warnings []string, opts BuildOptions) ([]Node, []string, jobSetRoots) {
	roots := jobSetRoots{}
	if b.dynamic == nil {
		return nodes, warnings, roots
	}
	gvr, ok := b.dynamic.GetGVRWithGroup(jobset.Kind, jobset.Group)
	if !ok {
		return nodes, warnings, roots
	}
	items, err := b.dynamic.ListNamespaces(gvr, opts.Namespaces)
	if err != nil {
		log.Printf("WARNING [topology] Failed to list JobSets: %v", err)
		warnings = append(warnings, fmt.Sprintf("Failed to list JobSets: %v", err))
	}
	for _, item := range items {
		ns := item.GetNamespace()
		if !opts.MatchesNamespaceFilter(ns) {
			continue
		}
		roots[ns+"/"+item.GetName()] = item
		nodes = append(nodes, jobSetNode(item))
	}
	return nodes, warnings, roots
}

func jobSetNode(item *unstructured.Unstructured) Node {
	status, phase := jobSetTopologyStatus(jobset.Lifecycle(item))
	data := map[string]any{
		"namespace":  item.GetNamespace(),
		"labels":     item.GetLabels(),
		"apiVersion": item.GetAPIVersion(),
		"phase":      phase,
	}
	if jobset.Supported(item) {
		_, declared := jobset.Declared(item)
		data["declaredJobs"] = declared
	}
	return Node{
		ID:     jobSetNodeID(item.GetNamespace(), item.GetName()),
		Kind:   KindJobSet,
		Name:   item.GetName(),
		Status: status,
		Data:   data,
	}
}

// jobSetTopologyStatus grades the shared lifecycle and labels it the way the
// JobSet status badge does (testdata/lifecycle.json in pkg/jobset holds both
// to the same vectors): only a failed root is unhealthy, labeled with its
// Failed condition's reason; startup and recreation are degraded; finished,
// active, pending and suspended work is neutral, so a JobSet with no children
// yet never reads as broken.
func jobSetTopologyStatus(state jobset.State) (HealthStatus, string) {
	switch state.Phase {
	case jobset.PhaseFinished:
		if state.Outcome == jobset.OutcomeFailed {
			if state.Reason != "" {
				return StatusUnhealthy, state.Reason
			}
			return StatusUnhealthy, "Failed"
		}
		return StatusNeutral, "Completed"
	case jobset.PhaseSuspended:
		return StatusNeutral, "Suspended"
	case jobset.PhaseActive:
		switch state.Condition {
		case "StartupPolicyInProgress":
			return StatusDegraded, "Starting"
		case "RestartingJobSet":
			return StatusDegraded, "Restarting"
		}
		return StatusNeutral, "Active"
	case jobset.PhasePending:
		return StatusNeutral, "Pending"
	}
	return StatusUnknown, "Unknown"
}

// controllerOf returns the drawn JobSet that controls job, judged by
// jobset.Controls, the predicate the JobSet member list uses.
func (roots jobSetRoots) controllerOf(job *batchv1.Job) (*unstructured.Unstructured, bool) {
	owner := metav1.GetControllerOf(job)
	if owner == nil || owner.Kind != jobset.Kind {
		return nil, false
	}
	root, ok := roots[job.Namespace+"/"+owner.Name]
	if !ok || !jobset.Controls(root, job) {
		return nil, false
	}
	return root, true
}
