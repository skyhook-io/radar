package prometheus

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// AuthGate is fail-open by default — if the server hasn't wired its canRead in,
// the OSS no-auth code path keeps working. A regression that locks out anonymous
// users would break the local-first deployment model.
func TestCanReadFailOpenWhenNoGate(t *testing.T) {
	t.Cleanup(func() { SetAuthGate(nil) })
	SetAuthGate(nil)

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if !canRead(req, "apps", "deployments", "ns", "get") {
		t.Fatal("canRead should return true when no gate is installed")
	}
}

// Installed gate is invoked with the request and routing args, and its return
// value is what canRead returns. Guards against future refactors that drop the
// canRead call in handlers or pass the wrong args.
func TestCanReadInvokesGate(t *testing.T) {
	t.Cleanup(func() { SetAuthGate(nil) })

	var got struct {
		req       *http.Request
		group     string
		resource  string
		namespace string
		verb      string
		calls     int
	}
	SetAuthGate(func(r *http.Request, group, resource, namespace, verb string) bool {
		got.req = r
		got.group = group
		got.resource = resource
		got.namespace = namespace
		got.verb = verb
		got.calls++
		return false
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if canRead(req, "apps", "deployments", "default", "get") {
		t.Fatal("canRead should return false when gate denies")
	}
	if got.calls != 1 {
		t.Fatalf("gate called %d times, want 1", got.calls)
	}
	if got.req != req {
		t.Errorf("gate got request %p, want %p", got.req, req)
	}
	if got.group != "apps" || got.resource != "deployments" || got.namespace != "default" || got.verb != "get" {
		t.Errorf("gate received unexpected args: group=%q resource=%q namespace=%q verb=%q",
			got.group, got.resource, got.namespace, got.verb)
	}
}

// SetAuthGate(nil) must clear a previously-installed gate. Otherwise a test that
// installs a deny-all could permanently lock the production process.
func TestSetAuthGateNilClearsPreviousGate(t *testing.T) {
	t.Cleanup(func() { SetAuthGate(nil) })

	SetAuthGate(func(_ *http.Request, _, _, _, _ string) bool { return false })

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if canRead(req, "apps", "deployments", "ns", "get") {
		t.Fatal("precondition: gate should deny")
	}

	SetAuthGate(nil)
	if !canRead(req, "apps", "deployments", "ns", "get") {
		t.Fatal("canRead should return true after SetAuthGate(nil) clears the gate")
	}
}
