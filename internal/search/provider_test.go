package search

import (
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/client-go/kubernetes/fake"
	"testing"
)

func TestCacheProviderTypedCoverageUsesReadinessAndScope(t *testing.T) {
	cache, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:         fake.NewClientset(),
		ResourceScopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "team-a"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(cache.Stop)
	p := &CacheProvider{cache: &k8s.ResourceCache{ResourceCache: cache}}
	for _, tc := range []struct {
		kind       string
		namespaces []string
		reason     string
	}{
		{"Pod", []string{"team-a"}, ""},
		{"Pod", nil, "namespace_scope"},
		{"Pod", []string{"team-b"}, "namespace_scope"},
		{"Pod", []string{"team-a", "team-b"}, "namespace_scope"},
		{"Secret", []string{"team-a"}, "cold"},
	} {
		if got := p.TypedCoverage(tc.kind, tc.namespaces); got != tc.reason {
			t.Errorf("%s %v: got %q want %q", tc.kind, tc.namespaces, got, tc.reason)
		}
	}
}
