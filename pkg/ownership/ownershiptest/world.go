// Package ownershiptest checks ownership sources against what a cluster is
// meant to contain. A World states the cluster and how much of it Radar has
// collected; Scan answers from the World by brute force, deliberately sharing
// no read, scope or coverage code with real sources, so a source that agrees
// with it on every question is right about what it read and what it couldn't.
package ownershiptest

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// World is a cluster as a test intends it.
type World struct {
	// Resources are what discovery serves.
	Resources []ownership.ServedResource
	// Partial: discovery missed some groups, so kinds beyond Resources may
	// exist.
	Partial bool
	Objects []Object
	// Collection says how much of each kind Radar holds; a kind not listed is
	// collected in full.
	Collection map[resourceid.GroupKind]Collection
}

// Object is one object of a World.
type Object struct {
	Ref resourceid.Ref
	// APIVersion is the version Radar reads the object at.
	APIVersion        string
	UID               types.UID
	ResourceVersion   string
	OwnerReferences   []metav1.OwnerReference
	DeletionTimestamp *metav1.Time
	Finalizers        []string
	// Membership and Members are what a recording manager's status says of
	// its members, as the objects it means. DestinationNamespace is an Argo
	// CD Application's destination namespace.
	Membership           ownership.MembershipState
	Members              []resourceid.Ref
	DestinationNamespace string
	// Form picks one of the ways a status can say the same thing, such as
	// an empty list written or left out; a renderer must vary only that,
	// and Scan ignores it.
	Form uint8
}

// OwnerRef returns a reference to o. With controller set, it is the
// controller reference and blocks o's deletion, as controllers set it.
func (o Object) OwnerRef(controller bool) metav1.OwnerReference {
	ref := metav1.OwnerReference{APIVersion: o.APIVersion, Kind: o.Ref.Kind, Name: o.Ref.Name, UID: o.UID}
	if controller {
		yes := true
		ref.Controller, ref.BlockOwnerDeletion = &yes, &yes
	}
	return ref
}

// CollectionState is how far Radar got collecting a kind.
type CollectionState uint8

const (
	// Collected: loaded in full wherever Radar collects the kind.
	Collected CollectionState = iota
	// Loading: Radar watches the kind but hasn't finished its initial load.
	Loading
	// NotCollected: Radar doesn't watch the kind.
	NotCollected
	// Refused: Kubernetes refused Radar's own identity the kind. For a kind
	// Radar holds in its typed cache, that cache knows only that its access
	// probe left the kind off.
	Refused
	// Unwatchable: the kind's API can't be list/watched, so Radar can't
	// collect it.
	Unwatchable
)

// Collection is how much of one kind Radar holds.
type Collection struct {
	State CollectionState
	// Namespaces limits a namespaced kind's collection to these namespaces;
	// nil collects them all.
	Namespaces []string
	// OnDemand: Radar starts this kind's watches as reads need them, as it
	// does for kinds outside its typed cache, so a namespace it doesn't hold
	// yet is unwatched rather than outside what it collects.
	OnDemand bool
}

func (w World) collection(gk resourceid.GroupKind) Collection {
	return w.Collection[gk]
}
