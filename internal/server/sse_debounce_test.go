package server

import (
	"testing"
	"time"
)

type fakeResourceCache struct{ count int }

func (f fakeResourceCache) GetResourceCount() int { return f.count }

func TestTopologyDebounceForFallsBackToResourceCountBeforeFirstBroadcast(t *testing.T) {
	cases := []struct {
		name     string
		count    int
		expected time.Duration
	}{
		{"empty cluster", 0, 1 * time.Second},
		{"small (count/5 ≤ 500)", 2500, 1 * time.Second},
		{"medium (count/5 = 600)", 3000, 2 * time.Second},
		{"large (count/5 = 3000)", 15000, 5 * time.Second},
		{"huge (count/5 = 6000)", 30000, 15 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// lastBroadcastMaxEstimated=0 → fallback path
			got := topologyDebounceFor(0, fakeResourceCache{count: tc.count})
			if got != tc.expected {
				t.Errorf("got %v, want %v", got, tc.expected)
			}
		})
	}
}

func TestTopologyDebounceForUsesEstimatedNodesOnceBroadcast(t *testing.T) {
	cases := []struct {
		name      string
		estimated int64
		expected  time.Duration
	}{
		{"under 500", 100, 1 * time.Second},
		{"500 boundary", 500, 1 * time.Second},
		{"501–2000", 1500, 2 * time.Second},
		{"2001–5000", 4000, 5 * time.Second},
		{"over 5000", 8000, 15 * time.Second},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Resource count should be ignored once lastBroadcastMaxEstimated > 0
			got := topologyDebounceFor(tc.estimated, fakeResourceCache{count: 999999})
			if got != tc.expected {
				t.Errorf("got %v, want %v", got, tc.expected)
			}
		})
	}
}

// TestTopologyDebounceForResetsAfterNamespaceSwitch verifies the property we
// care about: a brief stint on a big namespace does NOT keep debounce sticky
// after the user switches back to a small one. (The fix for the perfstats
// historical-max bug: lastBroadcastMaxEstimated reflects only the current
// broadcast cycle's builds, so one cycle of stale debounce after a switch
// then it adapts.)
func TestTopologyDebounceForResetsAfterNamespaceSwitch(t *testing.T) {
	// User was viewing a 5000-pod namespace → debounce was 15s
	big := topologyDebounceFor(5500, fakeResourceCache{count: 999999})
	if big != 15*time.Second {
		t.Errorf("expected 15s for 5500 estimated, got %v", big)
	}
	// After the next broadcast post-switch, lastBroadcastMaxEstimated reflects
	// the new small namespace's build (eg. 80 nodes). Debounce should drop.
	small := topologyDebounceFor(80, fakeResourceCache{count: 999999})
	if small != 1*time.Second {
		t.Errorf("expected 1s post-switch (estimated=80), got %v", small)
	}
}
