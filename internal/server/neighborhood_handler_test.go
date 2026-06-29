package server

import (
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
)

func TestParseNeighborhoodOptions_Defaults(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.Profile != topology.ProfileAuto {
		t.Errorf("default profile = %q, want auto", opts.Profile)
	}
	if opts.Hops != 1 {
		t.Errorf("default hops = %d, want 1", opts.Hops)
	}
	if opts.MaxNodes != 25 {
		t.Errorf("default max_nodes = %d, want 25", opts.MaxNodes)
	}
}

func TestParseNeighborhoodOptions_Custom(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?profile=all&hops=2&max_nodes=10", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.Profile != topology.ProfileAll {
		t.Errorf("profile = %q, want all", opts.Profile)
	}
	if opts.Hops != 2 {
		t.Errorf("hops = %d, want 2", opts.Hops)
	}
	if opts.MaxNodes != 10 {
		t.Errorf("max_nodes = %d, want 10", opts.MaxNodes)
	}
}

func TestParseNeighborhoodOptions_MaxNodesClamp(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?max_nodes=99999", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.MaxNodes != 200 {
		t.Errorf("max_nodes clamp = %d, want 200", opts.MaxNodes)
	}
}

// TestParseNeighborhoodOptions_HopsClamp pins that REST applies the hops=2
// clamp at the handler level too, matching MaxNodes. BFS clamps internally,
// but the handler-level clamp keeps opts.Hops correct if anything inspects
// or logs it before BFS, and matches the doc on parseNeighborhoodOptions.
func TestParseNeighborhoodOptions_HopsClamp(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?hops=99", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.Hops != 2 {
		t.Errorf("hops clamp = %d, want 2", opts.Hops)
	}
}

func TestParseNeighborhoodOptions_InvalidValues(t *testing.T) {
	r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?hops=abc&max_nodes=-5", nil)
	opts := parseNeighborhoodOptions(r)
	if opts.Hops != 1 {
		t.Errorf("invalid hops should fall back to default 1, got %d", opts.Hops)
	}
	if opts.MaxNodes != 25 {
		t.Errorf("invalid max_nodes should fall back to default 25, got %d", opts.MaxNodes)
	}
}

// TestParseNeighborhoodOptions_ProfileNormalization pins that REST exposes
// only auto/all. Unknown semantic bucket names fall back to ProfileAuto
// rather than silently expanding to all edge types.
func TestParseNeighborhoodOptions_ProfileNormalization(t *testing.T) {
	cases := []struct {
		query string
		want  topology.Profile
	}{
		{"profile=all", topology.ProfileAll},
		{"profile=All", topology.ProfileAll},             // case-insensitive
		{"profile=%20%20all%20%20", topology.ProfileAll}, // whitespace trim
		{"profile=management", topology.ProfileAuto},     // unsupported bucket → auto
		{"profile=networking", topology.ProfileAuto},     // unsupported bucket → auto
		{"profile=policy", topology.ProfileAuto},         // unsupported bucket → auto
		{"profile=security", topology.ProfileAuto},       // unsupported bucket → auto
		{"profile=garbage", topology.ProfileAuto},        // unknown → auto
		{"profile=", topology.ProfileAuto},               // empty → auto
		{"", topology.ProfileAuto},                       // missing → auto
	}
	for _, c := range cases {
		t.Run(c.query, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/api/ai/neighborhood/Pod/prod/cart?"+c.query, nil)
			opts := parseNeighborhoodOptions(r)
			if opts.Profile != c.want {
				t.Errorf("profile = %q, want %q", opts.Profile, c.want)
			}
		})
	}
}
