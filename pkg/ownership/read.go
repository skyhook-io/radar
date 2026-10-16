package ownership

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

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
	// APIVersion is the version the store holds the object at.
	APIVersion string
	UID        types.UID
	// ResourceVersion is opaque: compare it only for equality, between
	// observations of the same object.
	ResourceVersion string
	// OwnerReferences are the object's own, in order and unvalidated.
	OwnerReferences   []metav1.OwnerReference
	DeletionTimestamp *metav1.Time
	Finalizers        []string
}

// Gap is what a read could not establish: Kind in Namespace, or cluster-wide
// for a cluster-scoped kind, and why. Reasons describe Radar's caches; a host
// refines them with what only it knows, such as whether Kubernetes rejected
// Radar's credentials or that a namespace is outside Radar's
// --namespace-scope.
type Gap struct {
	Kind      resourceid.GroupKind
	Namespace string
	Reason    readreason.Code
}
