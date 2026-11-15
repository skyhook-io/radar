package topology

import (
	"context"
	"encoding/json"
	"slices"

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
		if !s.access.allowed(o.Ref) {
			continue
		}
		if captured, ok := s.objects[o.Ref]; ok {
			o.Object = captured.object
		} else {
			obj, got := s.Borrow(o.Ref)
			if got.State != ownership.Present {
				continue
			}
			o.Object = obj
		}
		objects = append(objects, o)
	}
	read.Objects = objects
	s.indexes[string(key)] = read
	return read
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
