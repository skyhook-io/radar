package k8s

import (
	appsv1 "k8s.io/api/apps/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// ownershipSource reads ownership from the caches current when a session
// begins. A session pins them, so a context switch that replaces them fails
// its ValidateCaches instead of mixing two clusters' answers.
var ownershipSource = cachesource.New(func() cachesource.Caches {
	var c cachesource.Caches
	if rc := GetResourceCache(); rc != nil {
		c.Typed = rc.ResourceCache
	}
	if dc := GetDynamicResourceCache(); dc != nil {
		c.Dynamic = dc.DynamicResourceCache
	}
	if rd := GetResourceDiscovery(); rd != nil {
		c.Discovery = rd.ResourceDiscovery
	}
	return c
})

// AnswerOwnership answers rel's ownership for fetched, the object the caller
// fetched, from the caches, for the caller access describes once the
// session's catalog is known. owner is the fetched object itself: when it is a
// Deployment or an Argo Rollout, the ReplicaSets that only keep its rollback
// history move to OlderRevisions, read from the answer's own caches so that
// a context switch refuses them with it. Caches replaced or not ready leave
// the answer unavailable.
func AnswerOwnership(rel *topology.Relationships, fetched ownership.Observation, owner metav1.Object, access func(*ownership.Catalog) ownership.Access) {
	answerOwnership(ownershipSource, topology.ApplyOwnership, splitHistory(fetched.Ref, owner), rel, fetched, access)
}

// splitHistory moves the ReplicaSets among rel's Children that only keep
// subject's rollback history to OlderRevisions, reading them and the Pods
// still naming them through the typed cache the session reads.
func splitHistory(subject resourceid.Ref, owner metav1.Object) func(*topology.Relationships, *ownership.View, ownership.ReadSession) {
	return func(rel *topology.Relationships, _ *ownership.View, s ownership.ReadSession) {
		typed, ok := cachesource.TypedCache(s)
		if !ok || owner == nil {
			return
		}
		topology.SplitOlderRevisions(rel, subject.GroupKind(), owner.GetAnnotations(), func(namespace, name string) (*appsv1.ReplicaSet, bool, bool) {
			obj, found, ok := typed.ReadableObject(k8score.ReplicaSets, namespace, name)
			rs, isReplicaSet := obj.(*appsv1.ReplicaSet)
			if !ok || !found || !isReplicaSet {
				return nil, false, false
			}
			pods, ok := typed.ReadableDependents(k8score.Pods, []string{namespace}, string(rs.UID))
			if !ok {
				return nil, false, false
			}
			return rs, len(pods) > 0, true
		})
	}
}

// AnswerOwner is AnswerOwnership for rel's Owner and Manager, which is all a
// resource context reads. It searches for no children and builds no member
// list: the manager answer reads the recorder index along the controller
// chain and, for a claim its lists don't confirm, that manager's list state.
func AnswerOwner(rel *topology.Relationships, fetched ownership.Observation, access func(*ownership.Catalog) ownership.Access) {
	answerOwnership(ownershipSource, topology.ApplyOwner, nil, rel, fetched, access)
}

// answerOwnership applies the view's answer to rel, then present (when set)
// with the view and its session, before validating: a refused answer takes
// what present read with it.
func answerOwnership(source ownership.Source, apply func(*topology.Relationships, *ownership.View, ownership.Observation), present func(*topology.Relationships, *ownership.View, ownership.ReadSession), rel *topology.Relationships, fetched ownership.Observation, access func(*ownership.Catalog) ownership.Access) {
	session, err := source.Begin()
	if err != nil {
		topology.OwnershipUnavailable(rel, string(readreason.ClusterConnecting))
		return
	}
	if len(session.Catalog().DependentKinds()) == 0 {
		// Discovery hasn't answered yet, as while Radar starts, so no kind
		// could be searched or resolved.
		topology.OwnershipUnavailable(rel, string(readreason.ClusterConnecting))
		return
	}
	v := ownership.NewView(session, access(session.Catalog()))
	apply(rel, v, fetched)
	if present != nil {
		present(rel, v, session)
	}
	if session.ValidateCaches() != nil {
		// A cluster switch mid-answer: it may mix two clusters.
		topology.OwnershipUnavailable(rel, string(readreason.ConnectionChanged))
	}
}

// OwnershipObservation is what ownership reads from a fetched object, whose
// type meta must be set.
func OwnershipObservation(obj any) (ownership.Observation, bool) {
	o, ok := obj.(runtime.Object)
	if !ok {
		return ownership.Observation{}, false
	}
	m, err := apimeta.Accessor(o)
	gvk := o.GetObjectKind().GroupVersionKind()
	if err != nil || gvk.Kind == "" {
		return ownership.Observation{}, false
	}
	ref := resourceid.Ref{Group: gvk.Group, Kind: gvk.Kind, Namespace: m.GetNamespace(), Name: m.GetName()}
	return ownership.Observe(ref, m), true
}
