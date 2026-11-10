package ownership

import (
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Decision is whether a caller may read something. The zero value denies, so
// an unset decision never grants.
type Decision uint8

const (
	Denied Decision = iota
	Allowed
	// Undecided: the permission check could not be made, so neither is
	// known.
	Undecided
)

func (d Decision) String() string {
	switch d {
	case Allowed:
		return "allowed"
	case Undecided:
		return "undecided"
	default:
		return "denied"
	}
}

// Access is what one caller may read. A View names only objects the caller
// may read, and says nothing of whether there are others.
type Access interface {
	// CanRead decides whether the caller may read objects of kind in namespace.
	// With namespace "" it decides objects of a cluster-scoped kind, or
	// whether the caller may read a namespaced kind in at least one of the
	// namespaces Namespaces reports; Undecided when that would take a check
	// per namespace. A kind no served resource names holds nothing to read,
	// so allow it and let the read say why: Denied would count its objects
	// as hidden from the caller.
	CanRead(kind resourceid.GroupKind, namespace string) Decision
	// Namespaces are the namespaces in which the caller may read some
	// namespaced kinds, or every namespace when all is set. CanRead decides each
	// kind in them.
	Namespaces() (namespaces []string, all bool)
}

// Unrestricted is the access of a caller who may read everything, as anyone
// may when Radar runs without authentication.
func Unrestricted() Access { return unrestricted{} }

type unrestricted struct{}

func (unrestricted) CanRead(resourceid.GroupKind, string) Decision { return Allowed }
func (unrestricted) Namespaces() ([]string, bool)                  { return nil, true }
