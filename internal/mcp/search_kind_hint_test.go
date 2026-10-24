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

func TestSearchFindingsHint(t *testing.T) {
	if got := searchFindingsHint(search.Result{}); got != "" {
		t.Fatalf("a whole search must give no hint, got %q", got)
	}
	access := searchFindingsHint(search.Result{AccessIncomplete: true})
	if !strings.Contains(access, "couldn't be confirmed in this search") || strings.Contains(access, "ran out of time") {
		t.Errorf("access-only hint = %q", access)
	}
	timed := searchFindingsHint(search.Result{IncompleteFindings: search.FindingsTimeLimit})
	if !strings.Contains(timed, "ran out of time") || strings.Contains(timed, "couldn't be confirmed") {
		t.Errorf("time-only hint = %q", timed)
	}
	both := searchFindingsHint(search.Result{AccessIncomplete: true, IncompleteFindings: search.FindingsTimeLimit})
	if !strings.Contains(both, "couldn't be confirmed") || !strings.Contains(both, "ran out of time") {
		t.Errorf("both causes must be reported, got %q", both)
	}
}
