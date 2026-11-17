package relations

import (
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// CaptureSubject pins every read of subject to fetched. A different cached
// UID refuses the capture; resourceVersion drift within an incarnation does not.
// A subject the cache doesn't hold, or can't read, keeps that read, so the
// answers that need the cache's copy still say it is missing.
func CaptureSubject(s Session, subject resourceid.Ref, fetched runtime.Object) (Session, bool) {
	meta, err := apimeta.Accessor(fetched)
	if err != nil {
		return nil, false
	}
	_, read := s.Borrow(subject)
	if read.State == ownership.Present && read.Object.UID != meta.GetUID() {
		return nil, false
	}
	if capture, ok := s.(interface {
		CaptureSubject(resourceid.Ref, runtime.Object) Session
	}); ok {
		s = capture.CaptureSubject(subject, fetched)
	}
	return capturedSubject{Session: s, ref: subject, object: fetched, observation: ownership.Observe(subject, meta), held: read.State == ownership.Present}, true
}

type capturedSubject struct {
	Session
	ref         resourceid.Ref
	object      runtime.Object
	observation ownership.Observation
	held        bool
}

func (s capturedSubject) Object(ref resourceid.Ref) ownership.ObjectRead {
	if ref == s.ref && s.held {
		return ownership.ObjectRead{State: ownership.Present, Object: s.observation}
	}
	return s.Session.Object(ref)
}

func (s capturedSubject) Borrow(ref resourceid.Ref) (runtime.Object, ownership.ObjectRead) {
	if ref == s.ref && s.held {
		return s.object, s.Object(ref)
	}
	return s.Session.Borrow(ref)
}

func (s capturedSubject) Indexed(gk resourceid.GroupKind, index string, keys []string, scope ownership.SearchScope) IndexedRead {
	read := s.Session.Indexed(gk, index, keys, scope)
	for i, o := range read.Objects {
		if o.Ref == s.ref {
			// Indexed results can share their backing array with a session.
			objects := append([]IndexedObject(nil), read.Objects...)
			objects[i].Object = s.object
			read.Objects = objects
			break
		}
	}
	return read
}
