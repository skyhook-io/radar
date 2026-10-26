package ownership

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ReadState is what one read established.
type ReadState uint8

const (
	// Unavailable: whether the object exists is unknown; the read's Gap says
	// why. It is the zero value, so an unset read never claims an answer.
	Unavailable ReadState = iota
	// Present: the store holds the object.
	Present
	// Absent: the store serving the object's kind and namespace had loaded in
	// full and does not hold it.
	Absent
	// Excluded: the kind is outside ownership. Radar keeps none of an Event's
	// owner references, so it can't say what owns one.
	Excluded
)

func (s ReadState) String() string {
	switch s {
	case Present:
		return "present"
	case Absent:
		return "absent"
	case Excluded:
		return "excluded"
	default:
		return "unavailable"
	}
}

// ObjectRead is the result of reading one object.
type ObjectRead struct {
	State ReadState
	// Object is set when State is Present.
	Object Observation
	// Gap is set when State is Unavailable.
	Gap Gap
}

// Observation is what ownership reads from one object, copied out of the
// cache so it stays fixed while rules run.
type Observation struct {
	Ref resourceid.Ref
	UID types.UID
	// ResourceVersion is opaque: compare it only for equality, between
	// observations of the same object.
	ResourceVersion string
	// OwnerReferences are the object's own, in order and unvalidated.
	OwnerReferences []metav1.OwnerReference
	// Claims are the managers the object's own metadata names, read when it
	// was captured.
	Claims []gitops.Claim
}

// Observe copies what ownership needs out of m, the object at ref, which may
// be a cached object that must not be kept.
func Observe(ref resourceid.Ref, m metav1.Object) Observation {
	o := Observation{
		Ref:             ref,
		UID:             m.GetUID(),
		ResourceVersion: m.GetResourceVersion(),
	}
	if refs := m.GetOwnerReferences(); len(refs) > 0 {
		o.OwnerReferences = make([]metav1.OwnerReference, len(refs))
		for i := range refs {
			refs[i].DeepCopyInto(&o.OwnerReferences[i])
		}
	}
	o.Claims = gitops.Claims(ref, m.GetLabels(), m.GetAnnotations())
	return o
}

// Gap is what a read could not establish: Kind in Namespace, or cluster-wide
// for a cluster-scoped kind, and why. Reasons describe Radar's caches; a host
// refines them with what only it knows, such as why its access probe left a
// typed kind uncollected or that a namespace is outside Radar's
// --namespace-scope.
type Gap struct {
	Kind      resourceid.GroupKind
	Namespace string
	Reason    readreason.Code
}
