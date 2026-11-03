package prometheus

import (
	"maps"
	"testing"
)

func TestRetirePreservesManualConfigurationForReinitialize(t *testing.T) {
	Initialize(nil, nil, "before")
	t.Cleanup(func() { Initialize(nil, nil, "") })

	wantURL := "http://127.0.0.1:9"
	wantHeaders := map[string]string{"Authorization": "Bearer test-token"}
	Configure(wantURL, wantHeaders)

	Retire()
	Reinitialize(nil, nil, "after")

	gotURL, gotHeaders := CurrentConfig()
	if gotURL != wantURL {
		t.Fatalf("manual URL after Retire/Reinitialize = %q, want %q", gotURL, wantURL)
	}
	if !maps.Equal(gotHeaders, wantHeaders) {
		t.Fatalf("headers after Retire/Reinitialize = %v, want %v", gotHeaders, wantHeaders)
	}
}
