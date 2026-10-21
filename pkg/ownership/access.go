package ownership

import (
	"sync"

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
	// namespaces Namespaces reports.
	CanRead(kind resourceid.GroupKind, namespace string) Decision
	// Namespaces are the namespaces in which the caller may read some
	// namespaced kinds, or every namespace when all is set. CanRead decides each
	// kind in them.
	Namespaces() (namespaces []string, all bool)
	// CanReadEverything reports whether CanRead allows every kind in every namespace.
	CanReadEverything() bool
	// CanReadAllKindsIn reports whether CanRead allows every kind in namespace.
	CanReadAllKindsIn(namespace string) bool
}

// Unrestricted is the access of a caller who may read everything, as anyone
// may when Radar runs without authentication.
func Unrestricted() Access { return unrestricted{} }

type unrestricted struct{}

func (unrestricted) CanRead(resourceid.GroupKind, string) Decision { return Allowed }
func (unrestricted) Namespaces() ([]string, bool)                  { return nil, true }
func (unrestricted) CanReadEverything() bool                       { return true }
func (unrestricted) CanReadAllKindsIn(string) bool                 { return true }

// maxNamespaceChecks bounds the checks one AnyNamespace call makes. A caller
// who may read every namespace of a large cluster would otherwise cost a
// permission request per namespace for each kind decided across them.
const maxNamespaceChecks = 16

// AnyNamespace decides whether check allows at least one of namespaces:
// allowed when one does, undecided when none does but one couldn't be
// decided or namespaces past the first maxNamespaceChecks went unchecked,
// denied otherwise, and for none. Each check can be a permission request, so
// it makes a few at a time.
func AnyNamespace(namespaces []string, check func(namespace string) Decision) Decision {
	const batch = 8
	result := Denied
	if len(namespaces) > maxNamespaceChecks {
		namespaces, result = namespaces[:maxNamespaceChecks], Undecided
	}
	for start := 0; start < len(namespaces); start += batch {
		group := namespaces[start:min(start+batch, len(namespaces))]
		decisions := make([]Decision, len(group))
		var wg sync.WaitGroup
		for i, namespace := range group {
			wg.Add(1)
			go func() {
				defer wg.Done()
				// These goroutines are outside the request's panic recovery:
				// a check that panics is undecided, not a crashed server.
				defer func() {
					if recover() != nil {
						decisions[i] = Undecided
					}
				}()
				decisions[i] = check(namespace)
			}()
		}
		wg.Wait()
		for _, d := range decisions {
			switch d {
			case Allowed:
				return Allowed
			case Undecided:
				result = Undecided
			}
		}
	}
	return result
}
