package ownership

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// OwnerState is what one owner reference resolves to, judged as Kubernetes'
// garbage collector judges it.
type OwnerState uint8

const (
	// OwnerUnknown: whether the owner exists can't be told; the resolution's
	// Gap says why.
	OwnerUnknown OwnerState = iota
	// OwnerFound: the object the reference names exists with the reference's
	// UID.
	OwnerFound
	// OwnerGone: no object has the reference's coordinates. The collector
	// treats the reference as dangling. That includes a List kind the
	// collector's RESTMapper makes up, which maps to a resource no API serves.
	OwnerGone
	// OwnerReplaced: an object has the coordinates under another UID, a
	// later incarnation. The collector treats the reference as dangling too.
	OwnerReplaced
	// OwnerUnresolvable: the collector can't resolve the reference — its
	// apiVersion doesn't parse, it names no object, or it names a namespaced
	// owner for a cluster-scoped object — so it never collects the dependent
	// through it. A kind or version its RESTMapper doesn't map is unknown
	// instead (kind_unknown): the collector retries those, since the next
	// discovery may serve them. The RESTMapper maps a kind's all-lowercase
	// spelling too, and the internal version to any serving one.
	OwnerUnresolvable
	// OwnerExcluded: the reference names an Event, which ownership doesn't
	// read.
	OwnerExcluded
)

func (s OwnerState) String() string {
	switch s {
	case OwnerFound:
		return "found"
	case OwnerGone:
		return "gone"
	case OwnerReplaced:
		return "replaced"
	case OwnerUnresolvable:
		return "unresolvable"
	case OwnerExcluded:
		return "excluded"
	default:
		return "unknown"
	}
}

// OwnerResolution is one owner reference resolved.
type OwnerResolution struct {
	State OwnerState
	// Ref is where the reference points. It is unset when the reference is
	// unresolvable, and when Radar can't map its kind or version to a scope
	// (OwnerUnknown with a gap saying why).
	Ref resourceid.Ref
	// Owner is the object at Ref, when Found or Replaced.
	Owner Observation
	// Gap says why, when Unknown.
	Gap Gap
}

// ResolveOwner resolves one of dependent's owner references in s. The
// reference names an owner by group, kind and name; the owner is in the
// dependent's namespace when its kind is namespaced, cluster-wide otherwise,
// and only its UID tells one incarnation from the next.
func ResolveOwner(s ReadSession, dependent Observation, ref metav1.OwnerReference) OwnerResolution {
	gv, err := schema.ParseGroupVersion(ref.APIVersion)
	if err != nil || ref.APIVersion == "" || ref.Kind == "" || ref.Name == "" {
		return OwnerResolution{State: OwnerUnresolvable}
	}
	catalog := s.Catalog()
	gk, spelled := catalog.referencedKind(resourceid.GroupKind{Group: gv.Group, Kind: ref.Kind})
	if IsEvent(gk) {
		// Events are outside ownership, whatever discovery lists.
		return OwnerResolution{State: OwnerExcluded, Ref: resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: dependent.Ref.Namespace, Name: ref.Name}}
	}
	unknown := OwnerResolution{Gap: Gap{Kind: gk, Reason: readreason.ReadError}}
	if !spelled || catalog.Ambiguous(gk) {
		return unknown
	}
	kind, known := catalog.Kind(gk)
	if !known || !catalog.mapsAt(gk, gv.Version) {
		if namespaced, ambiguous, made := catalog.listKind(gk, gv.Version); made {
			switch {
			case ambiguous:
				return unknown
			case namespaced && dependent.Ref.Namespace == "":
				return OwnerResolution{State: OwnerUnresolvable}
			}
			gone := resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Name: ref.Name}
			if namespaced {
				gone.Namespace = dependent.Ref.Namespace
			}
			return OwnerResolution{State: OwnerGone, Ref: gone}
		}
		// The collector retries a reference its RESTMapper can't map, since
		// its next discovery may serve the kind, and the catalog can be older
		// than the collector's discovery. So what discovery didn't list is
		// unknown, never unresolvable.
		if !catalog.Complete() {
			return unknown
		}
		return OwnerResolution{Gap: Gap{Kind: gk, Reason: readreason.KindUnknown}}
	}
	if kind.Namespaced && dependent.Ref.Namespace == "" {
		return OwnerResolution{State: OwnerUnresolvable}
	}
	owner := resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Name: ref.Name}
	if kind.Namespaced {
		owner.Namespace = dependent.Ref.Namespace
	}
	read := s.Object(owner)
	r := OwnerResolution{Ref: owner}
	switch read.State {
	case Present:
		r.State, r.Owner = OwnerReplaced, read.Object
		if read.Object.UID == ref.UID {
			r.State = OwnerFound
		}
	case Absent:
		r.State = OwnerGone
	case Excluded:
		r.State = OwnerExcluded
	default:
		r.Gap = read.Gap
	}
	return r
}
