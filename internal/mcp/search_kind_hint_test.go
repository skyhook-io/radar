package mcp

import (
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/search"
)

func TestSearchKindHint(t *testing.T) {
	if got := searchKindHint(nil); got != "" {
		t.Fatalf("no unsearched kinds must give no hint, got %q", got)
	}
	got := searchKindHint([]search.UnsearchedKind{
		{Kind: "role", Reason: search.UnsearchedNotIndexed},
		{Kind: "Certificate", Group: "cert-manager.io", Reason: search.UnsearchedUnreadable, Namespaces: []string{"b"}},
		{Kind: "IngressClass", Group: "networking.k8s.io", Reason: search.UnsearchedClusterScoped},
	})
	for _, want := range []string{
		"Search has no cached kind matching kind:role; use list_resources kind=role or get_resource. For a short name, try the Kind or its plural.",
		"Radar couldn't read Certificate from its cache for namespaces b; it may still be loading, or Radar may not be allowed to list it. An empty result for it doesn't mean none exist.",
		"IngressClass is cluster-scoped and this search was limited to namespaces (by the request or your namespace scope); use list_resources kind=IngressClass.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("hint %q does not contain %q", got, want)
		}
	}
}
