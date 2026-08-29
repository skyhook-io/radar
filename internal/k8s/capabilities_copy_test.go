package k8s

import "testing"

func TestCopyCapabilitiesSeparatesRequestMutationsFromCache(t *testing.T) {
	cached := &Capabilities{Exec: true, LocalTerminal: true}
	requestCaps := copyCapabilities(cached)

	requestCaps.Exec = false
	requestCaps.LocalTerminal = false
	requestCaps.Username = "request-user"

	if !cached.Exec || !cached.LocalTerminal || cached.Username != "" {
		t.Fatalf("request mutation changed cached capabilities: %+v", cached)
	}
}
