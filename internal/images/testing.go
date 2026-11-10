package images

import (
	"net/http"
	"testing"
)

// testRegistryTransport replaces the network guard for authenticated
// inspection, so tests in other packages can serve a fake registry. It is
// honored only in test binaries.
var testRegistryTransport http.RoundTripper

// SetTestRegistryTransport makes authenticated image inspection send every
// registry request to rt and returns the previous value. It panics outside a
// test binary.
func SetTestRegistryTransport(rt http.RoundTripper) http.RoundTripper {
	if !testing.Testing() {
		panic("images.SetTestRegistryTransport called outside a test")
	}
	previous := testRegistryTransport
	testRegistryTransport = rt
	return previous
}
