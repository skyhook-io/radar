package mcp

import "testing"

func TestNormalizeMCPPackageSourceFilter(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"H", "H"},
		{"helm", "H"},
		{"helm_api", "H"},
		{"labels", "L"},
		{"workload-labels", "L"},
		{"crds", "C"},
		{"argocd", "A"},
		{"argo-cd", "A"},
		{"fluxcd", "F"},
		{"flux-cd", "F"},
		{"unknown", "unknown"},
	}
	for _, tt := range tests {
		if got := normalizeMCPPackageSourceFilter(tt.in); got != tt.want {
			t.Errorf("normalizeMCPPackageSourceFilter(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

func TestPackageSourceLegendCoversStableCodes(t *testing.T) {
	for _, code := range []string{"H", "L", "C", "A", "F"} {
		if packageSourceLegend[code] == "" {
			t.Fatalf("packageSourceLegend missing %s", code)
		}
	}
}
