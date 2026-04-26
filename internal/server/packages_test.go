package server

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/packages"
)

// Pins the error wording produced by k8s.ResourceCache.ListDynamicWithGroup
// when the requested CRD isn't installed. If that wording changes,
// graceful degradation breaks for clusters without ArgoCD/FluxCD —
// every Radar install would suddenly show error banners on /api/packages.
// See internal/k8s/cache.go ListDynamicWithGroup.
func TestIsMissingCRDErr_PinsK8scoreErrorString(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"with-group", fmt.Errorf("unknown resource kind: %s (group: %s)", "Application", "argoproj.io"), true},
		{"without-group", fmt.Errorf("unknown resource kind: %s", "Application"), true},
		{"case-insensitive", errors.New("UNKNOWN RESOURCE KIND: Application"), true},
		{"unrelated", errors.New("connection refused"), false},
		{"forbidden", errors.New("namespaces is forbidden"), false},
		{"nil", nil, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := isMissingCRDErr(c.err); got != c.want {
				t.Errorf("isMissingCRDErr(%v) = %v, want %v", c.err, got, c.want)
			}
		})
	}
}

func TestPackagesCacheKey_DistinguishesUserAndNamespaces(t *testing.T) {
	// Same user, different namespace sets → different keys.
	a := packagesCacheKeyFor("alice", []string{"prod"})
	b := packagesCacheKeyFor("alice", []string{"staging"})
	if a == b {
		t.Errorf("expected different keys for different namespaces, got %q", a)
	}
	// Different users, same namespaces → different keys.
	c := packagesCacheKeyFor("bob", []string{"prod"})
	if a == c {
		t.Errorf("expected different keys for different users, got %q", a)
	}
	// nil namespaces vs empty slice should be different (empty = no access).
	all := packagesCacheKeyFor("alice", nil)
	none := packagesCacheKeyFor("alice", []string{})
	if all == none {
		t.Errorf("nil (all namespaces) and empty slice (no access) must differ; both = %q", all)
	}
	// Order independence: same set in different orders → same key.
	x := packagesCacheKeyFor("alice", []string{"a", "b"})
	y := packagesCacheKeyFor("alice", []string{"b", "a"})
	if x != y {
		t.Errorf("namespace order should not affect key: %q vs %q", x, y)
	}
}

func TestSourcesUsed_StableCanonicalOrder(t *testing.T) {
	// Build rows whose Sources collectively cover all five codes,
	// added in non-canonical order. sourcesUsed should still emit
	// H, L, C, A, F in canonical order.
	rows := []packages.PackageRow{
		{Sources: []string{packages.SourceFluxCD, packages.SourceArgoCD}},
		{Sources: []string{packages.SourceCRDs}},
		{Sources: []string{packages.SourceLabels, packages.SourceHelm}},
	}
	got := sourcesUsed(rows)
	want := []string{packages.SourceHelm, packages.SourceLabels, packages.SourceCRDs, packages.SourceArgoCD, packages.SourceFluxCD}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("sourcesUsed = %v, want %v", got, want)
	}
}

func TestFilterByChartSubstring_CaseInsensitive(t *testing.T) {
	rows := []packages.PackageRow{
		{Chart: "cert-manager"},
		{Chart: "Karpenter"},
		{Chart: "external-dns"},
	}
	out := filterByChartSubstring(rows, "karpen")
	if len(out) != 1 || out[0].Chart != "Karpenter" {
		t.Errorf("expected Karpenter row only, got %+v", out)
	}
}
