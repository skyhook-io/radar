package server

import "testing"

// A trace subject is directly addressed, like a resource GET. The in-app
// namespace switcher pick is a VIEW filter for lists - it must never narrow
// the trace's namespace ceiling. When it did, opening a detail page raced the
// pick save and rendered "RBAC denies access to this namespace" on a fully
// readable resource, and the pick redacted cross-namespace fan-out (a parent
// Gateway in its own namespace) the caller was allowed to see.
func TestTraceNamespaceCeilingIgnoresViewPick(t *testing.T) {
	s := newTestServer(t)
	r := reqAs("alice")
	s.setActiveNamespaceForUser(r, []string{"other-ns"})

	if got := s.traceNamespaceCeiling(reqAs("alice")); got != nil {
		t.Errorf("trace ceiling must be the RBAC ceiling (nil = unrestricted), got the view pick %v", got)
	}
}
