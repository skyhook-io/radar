package opencost

import "slices"

// namespaceScope is the set of namespaces a caller may see cost data for.
// A nil slice means unrestricted; an empty non-nil slice means no access at
// all. This mirrors auth.FilterNamespacesForUser's nil-vs-empty contract, so
// the local no-auth and cluster-admin paths stay unrestricted by construction.
type namespaceScope []string

func (n namespaceScope) unrestricted() bool { return n == nil }

func (n namespaceScope) allows(namespace string) bool {
	return n == nil || slices.Contains(n, namespace)
}
