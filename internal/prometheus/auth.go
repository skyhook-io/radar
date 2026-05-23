package prometheus

import (
	"net/http"
	"sync/atomic"
)

// AuthGate is the per-request resource read check used by handlers that read
// K8s spec data via the shared informer cache. The cache is populated using
// Radar's service-account permissions, so without this gate any authenticated
// user could fetch any namespace's spec by guessing names. Server.canRead is
// the concrete implementation; passing it via SetAuthGate avoids an import
// cycle (server imports prometheus, not the other way around).
type AuthGate func(r *http.Request, group, resource, namespace, verb string) bool

var authGate atomic.Pointer[AuthGate]

// SetAuthGate installs the request-scoped authorization check. Pass nil to
// disable gating (only appropriate for tests).
func SetAuthGate(fn AuthGate) {
	if fn == nil {
		authGate.Store(nil)
		return
	}
	authGate.Store(&fn)
}

// canRead consults the installed AuthGate. Returns true when no gate is
// installed so the gate stays strictly additive — never accidentally locks
// out the OSS no-auth path.
func canRead(r *http.Request, group, resource, namespace, verb string) bool {
	g := authGate.Load()
	if g == nil {
		return true
	}
	return (*g)(r, group, resource, namespace, verb)
}
