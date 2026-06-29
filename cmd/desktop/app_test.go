package main

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/timeline"
)

func TestFormatWindowTitle(t *testing.T) {
	cases := []struct {
		name        string
		contextName string
		want        string
	}{
		{"empty falls back to bare product name", "", "Radar"},
		{"user-named context passes through", "packagear-dev-eks", "Radar — packagear-dev-eks"},
		{
			"full EKS ARN collapses to short cluster name (matches in-page selector)",
			"arn:aws:eks:us-east-1:171782501968:cluster/packagear-prod-eks",
			"Radar — packagear-prod-eks",
		},
		{
			"GKE context collapses to cluster name",
			"gke_my-project_us-east1-b_prod-cluster",
			"Radar — prod-cluster",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := formatWindowTitle(tc.contextName); got != tc.want {
				t.Fatalf("formatWindowTitle(%q) = %q, want %q", tc.contextName, got, tc.want)
			}
		})
	}
}

// TestDesktopApp_UpdateWindowTitle_TracksContextSwitch asserts the invariant
// that successive updateWindowTitle calls always re-render the title from the
// most recent context name — including the empty/disconnected case.
func TestDesktopApp_UpdateWindowTitle_TracksContextSwitch(t *testing.T) {
	var got string
	a := &DesktopApp{
		setWindowTitle: func(title string) { got = title },
	}

	a.updateWindowTitle("packagear-prod-eks")
	if got != "Radar — packagear-prod-eks" {
		t.Fatalf("after initial set: got %q, want %q", got, "Radar — packagear-prod-eks")
	}

	a.updateWindowTitle("packagear-dev-eks")
	if got != "Radar — packagear-dev-eks" {
		t.Fatalf("after context switch: got %q, want %q", got, "Radar — packagear-dev-eks")
	}

	a.updateWindowTitle("")
	if got != "Radar" {
		t.Fatalf("after disconnect: got %q, want %q", got, "Radar")
	}
}

// TestDesktopApp_NewDesktopApp_GuardsNilCtx ensures the default title setter
// is a no-op before Wails has called startup() and populated a.ctx, instead
// of panicking inside wailsRuntime.WindowSetTitle on a nil ctx.
func TestDesktopApp_NewDesktopApp_GuardsNilCtx(t *testing.T) {
	a := NewDesktopApp(nil, timeline.StoreConfig{})
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("setWindowTitle panicked with nil ctx: %v", r)
		}
	}()
	a.updateWindowTitle("packagear-dev-eks")
}
