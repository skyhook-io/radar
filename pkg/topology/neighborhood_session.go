package topology

import (
	"context"
	"encoding/json"
	"slices"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

type neighborhoodObject struct {
	object runtime.Object
	read   ownership.ObjectRead
}

// Frontier answers share indexed reads and captured objects for this request.
// The cache APIs return full key buckets, so candidate scanning is not bounded
// by the output budget.
type neighborhoodSession struct {
	relations.Session
	ctx     context.Context
	access  *neighborhoodAccess
	objects map[resourceid.Ref]neighborhoodObject
	indexes map[string]relations.IndexedRead
}

func (s *neighborhoodSession) Borrow(r resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	return s.borrow(r, true)
}
func (s *neighborhoodSession) borrow(r resourceid.Ref, authorize bool) (runtime.Object, ownership.ObjectRead) {
	if s.ctx.Err() != nil || authorize && !s.access.allowed(r) {
		return nil, ownership.ObjectRead{State: ownership.Absent}
	}
	if o, ok := s.objects[r]; ok {
		return o.object, o.read
	}
	obj, read := s.Session.Borrow(r)
	if read.State != ownership.Present {
		for _, alias := range s.access.aliases(r) {
			if alias == r {
				continue
			}
			if _, served := s.Catalog().Kind(alias.GroupKind()); !served {
				continue
			}
			candidate, got := s.Session.Borrow(alias)
			if got.State == ownership.Present {
				obj, read = candidate, got
				break
			}
		}
	}
	s.objects[r] = neighborhoodObject{obj, read}
	return obj, read
}
func (s *neighborhoodSession) Object(r resourceid.Ref) ownership.ObjectRead {
	_, read := s.Borrow(r)
	return read
}
func (s *neighborhoodSession) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) relations.IndexedRead {
	if s.ctx.Err() != nil {
		return relations.IndexedRead{}
	}
	key, _ := json.Marshal(struct {
		Kind  resourceid.GroupKind
		Index string
		Keys  []string
		Scope ownership.SearchScope
	}{gk, index, keys, scope})
	if read, ok := s.indexes[string(key)]; ok {
		return read
	}
	read := s.Session.Indexed(gk, index, keys, scope)
	objects := make([]relations.IndexedObject, 0, len(read.Objects))
	for _, o := range read.Objects {
		if s.ctx.Err() != nil {
			return relations.IndexedRead{}
		}
		// The relation search matches each object before checking the
		// caller's access to it, and the walk admits every node it includes,
		// so a hidden object here is read only to tell whether it relates.
		// An object this request already read keeps that read, even an
		// absence the informer has since filled: the walk reads it from here.
		// One it hasn't is recorded as found here, not read again by name.
		if prior, read := s.objects[o.Ref]; read {
			if prior.read.State != ownership.Present {
				continue
			}
			o.Object = prior.object
		} else {
			m, err := apimeta.Accessor(o.Object)
			if err != nil {
				continue
			}
			s.objects[o.Ref] = neighborhoodObject{o.Object, ownership.ObjectRead{State: ownership.Present, Object: ownership.Observe(o.Ref, m)}}
		}
		objects = append(objects, o)
	}
	read.Objects = objects
	s.indexes[string(key)] = read
	return read
}

// Members reads a manager's member list from the object this request read,
// so its members agree with the rest of the answer even if the manager has
// changed since. A manager the request hasn't read is read as the session
// reads it.
func (s *neighborhoodSession) Members(manager resourceid.Ref) ownership.MembersRead {
	if captured, ok := s.captured(manager); ok {
		return captured.Members(manager)
	}
	return s.Session.Members(manager)
}

// Membership is Members' state, read the same way.
func (s *neighborhoodSession) Membership(manager resourceid.Ref) ownership.MembersRead {
	if captured, ok := s.captured(manager); ok {
		return captured.Membership(manager)
	}
	return s.Session.Membership(manager)
}

func (s *neighborhoodSession) captured(r resourceid.Ref) (relations.Session, bool) {
	o, ok := s.objects[r]
	if !ok || o.read.State != ownership.Present {
		return nil, false
	}
	return relations.CaptureSubject(s.Session, r, o.object)
}

func (s *neighborhoodSession) DependentCandidates(uid types.UID, scope ownership.SearchScope) ownership.DependentCandidatesRead {
	read := s.Session.DependentCandidates(uid, scope)
	read.Candidates = slices.DeleteFunc(slices.Clone(read.Candidates), func(o ownership.Observation) bool { return !s.access.allowed(o.Ref) })
	return read
}

type neighborhoodAuxiliarySession struct{ *neighborhoodSession }

func (s neighborhoodAuxiliarySession) Borrow(r resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	return s.borrow(r, false)
}

// jobUIDs fills uids with the UID of the Job now cached under each name a
// Pod's controller reference gives, so Pods of an earlier same-named run
// group apart from the current run's, as the graph builder groups them. The
// Job is read with Radar's identity: its UID only splits Pods already
// admitted.
func (s *neighborhoodSession) jobUIDs(pods []*corev1.Pod, uids map[string]types.UID) map[string]types.UID {
	for _, pod := range pods {
		ref := metav1.GetControllerOf(pod)
		if ref == nil || ref.Kind != "Job" || !ownerGroupMatches(ref.Kind, ref.APIVersion) {
			continue
		}
		key := pod.Namespace + "/" + ref.Name
		if _, read := uids[key]; read {
			continue
		}
		obj, got := s.borrow(resourceid.NewRef("batch", "Job", pod.Namespace, ref.Name), false)
		if job, ok := obj.(*batchv1.Job); ok && got.State == ownership.Present {
			uids[key] = job.UID
		} else {
			uids[key] = ""
		}
	}
	return uids
}
