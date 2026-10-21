package topology

import (
	appsv1 "k8s.io/api/apps/v1"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/rollouts"
)

// revisionAnnotations is where each kind that keeps old ReplicaSets for
// rollback records its current revision, and each of its ReplicaSets its own.
var revisionAnnotations = map[resourceid.GroupKind]string{
	{Group: "apps", Kind: "Deployment"}:     "deployment.kubernetes.io/revision",
	{Group: "argoproj.io", Kind: "Rollout"}: rollouts.RevisionAnnotation,
}

// SplitOlderRevisions moves the ReplicaSets among rel's Children that only
// keep rollback history into OlderRevisions: those that run no Pods, are
// meant to run none, and aren't the owner's current revision. A Deployment
// keeps up to ten old revisions, which would bury the ReplicaSet that runs its
// Pods. owner and annotations are the object rel describes; only a Deployment
// or an Argo Rollout that records its current revision splits. read returns a
// child ReplicaSet and whether any Pod still names it as owner, Pods still
// terminating included; a child it can't read (ok false) stays among Children.
func SplitOlderRevisions(rel *Relationships, owner resourceid.GroupKind, annotations map[string]string, read func(namespace, name string) (rs *appsv1.ReplicaSet, runsPods, ok bool)) {
	annotation := revisionAnnotations[owner]
	current := annotations[annotation]
	if rel == nil || annotation == "" || current == "" || read == nil {
		return
	}
	var children []ResourceRef
	for _, c := range rel.Children {
		if c.Group == "apps" && c.Kind == "ReplicaSet" {
			if rs, runsPods, ok := read(c.Namespace, c.Name); ok && !runsPods && inactiveReplicaSet(rs, annotation, current) {
				rel.OlderRevisions = append(rel.OlderRevisions, c)
				continue
			}
		}
		children = append(children, c)
	}
	rel.Children = children
}

// inactiveReplicaSet reports whether rs is meant to run no Pods, counts none,
// terminating ones included, and isn't its owner's current revision.
func inactiveReplicaSet(rs *appsv1.ReplicaSet, annotation, current string) bool {
	if rs.Spec.Replicas == nil || *rs.Spec.Replicas != 0 || rs.Status.Replicas != 0 {
		return false
	}
	if t := rs.Status.TerminatingReplicas; t != nil && *t != 0 {
		return false
	}
	return rs.Annotations[annotation] != current
}
