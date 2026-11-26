package prometheus

import "testing"

// Retire runs when a local context switch leaves the current cluster. The next
// context must not start from the previous cluster's manual endpoint or
// headers; it gets only what its own settings configure.
func TestRetireKeepsManualConfigurationOnItsOwnMember(t *testing.T) {
	old := testClient(t, "before", nil)
	old.Configure("http://127.0.0.1:9", map[string]string{"Authorization": "Bearer test-token"})
	_ = old.Close()
	next := testClient(t, "after", nil)
	gotURL, gotHeaders := next.CurrentConfig()
	if gotURL != "" || len(gotHeaders) != 0 {
		t.Fatalf("after Retire/Reinitialize got url=%q headers=%v, want neither carried over", gotURL, gotHeaders)
	}
}
