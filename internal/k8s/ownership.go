package k8s

import (
	"errors"

	appsv1 "k8s.io/api/apps/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

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
// a context switch refuses them with it. An object that changed between the
// view's reads is worth one more try; caches replaced or not ready leave the
// answer unavailable, as does a retry that fails too.
func AnswerOwnership(rel *topology.Relationships, fetched ownership.Observation, owner metav1.Object, access func(*ownership.Catalog) ownership.Access) {
	answerOwnership(ownershipSource, topology.ApplyOwnership, splitHistory(fetched.Ref, owner), rel, fetched, access)
}

// splitHistory moves the ReplicaSets among rel's Children that only keep
// subject's rollback history to OlderRevisions, reading them and the Pods
// still naming them through the typed cache the session reads. A ReplicaSet is
// judged only as the incarnation the view captured as a child: one replaced
// at its name since stays among Children.
func splitHistory(subject resourceid.Ref, owner metav1.Object) func(*topology.Relationships, *ownership.View, ownership.ReadSession) {
	return func(rel *topology.Relationships, v *ownership.View, s ownership.ReadSession) {
		typed, ok := cachesource.TypedCache(s)
		if !ok || owner == nil {
			return
		}
		// Built on the first ReplicaSet the split asks about: only a
		// Deployment or Rollout with a revision asks, and another kind's
		// children, such as a DaemonSet's thousands of Pods, aren't gathered
		// twice.
		var captured map[resourceid.Ref]types.UID
		topology.SplitOlderRevisions(rel, subject.GroupKind(), owner.GetAnnotations(), func(namespace, name string) (*appsv1.ReplicaSet, bool, bool) {
			if captured == nil {
				captured = map[resourceid.Ref]types.UID{}
				for _, c := range v.Children(subject, 0).Children {
					captured[c.Ref] = c.UID
				}
			}
			uid, isChild := captured[resourceid.Ref{Group: "apps", Kind: "ReplicaSet", Namespace: namespace, Name: name}]
			obj, found, ok := typed.ReadableObject(k8score.ReplicaSets, namespace, name)
			rs, isReplicaSet := obj.(*appsv1.ReplicaSet)
			if !isChild || !ok || !found || !isReplicaSet || rs.UID != uid {
				return nil, false, false
			}
			pods, ok := typed.ReadableDependents(k8score.Pods, []string{namespace}, string(uid))
			if !ok {
				return nil, false, false
			}
			return rs, len(pods) > 0, true
		})
	}
}

// AnswerOwner is AnswerOwnership for rel's Owner alone, which is all a
// resource context reads: it searches for no children and reads no member
// list, so it costs a few reads however much the object owns.
func AnswerOwner(rel *topology.Relationships, fetched ownership.Observation, access func(*ownership.Catalog) ownership.Access) {
	answerOwnership(ownershipSource, topology.ApplyOwner, nil, rel, fetched, access)
}

// answerOwnership applies the view's answer to rel, then present (when set)
// with the view and its session, before validating: a refused answer takes
// what present read with it.
func answerOwnership(source ownership.Source, apply func(*topology.Relationships, *ownership.View, ownership.Observation), present func(*topology.Relationships, *ownership.View, ownership.ReadSession), rel *topology.Relationships, fetched ownership.Observation, access func(*ownership.Catalog) ownership.Access) {
	reason := readreason.KindSyncPending
	for range 2 {
		session, err := source.Begin()
		if err != nil {
			topology.OwnershipUnavailable(rel, string(readreason.ClusterConnecting))
			return
		}
		if len(session.Catalog().DependentKinds()) == 0 {
			// Discovery hasn't answered yet, as while Radar starts, so no
			// kind could be searched or resolved.
			topology.OwnershipUnavailable(rel, string(readreason.ClusterConnecting))
			return
		}
		v := ownership.NewView(session, access(session.Catalog()))
		apply(rel, v, fetched)
		if present != nil {
			present(rel, v, session)
		}
		err = v.ValidateAnswers()
		switch {
		case err == nil:
			return
		case errors.Is(err, ownership.ErrCachesReplaced):
			topology.OwnershipUnavailable(rel, string(readreason.ConnectionChanged))
			return
		default:
			// Objects changed while they were read; reading again resolves it.
			reason = readreason.ReadError
		}
	}
	topology.OwnershipUnavailable(rel, string(reason))
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
	return ownership.Observe(ref, gvk.GroupVersion().String(), m), true
}
