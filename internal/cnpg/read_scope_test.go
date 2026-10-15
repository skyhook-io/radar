package cnpg

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/client-go/kubernetes/fake"
)

func TestKindReadAssessmentScope(t *testing.T) {
	for _, namespace := range []string{"", "alpha"} {
		t.Run("collector="+namespace, func(t *testing.T) {
			core, err := k8score.NewResourceCache(k8score.CacheConfig{
				Client:         fake.NewClientset(),
				ResourceScopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: namespace}},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(core.Stop)
			cache := &k8s.ResourceCache{ResourceCache: core}
			for _, tc := range []struct {
				name                          string
				target, scopedRead, uncovered []string
				scopedComplete                bool
			}{
				{"all", nil, []string{"alpha"}, nil, false},
				{"no target", []string{}, []string{}, nil, true},
				{"covered", []string{"alpha"}, []string{"alpha"}, nil, true},
				{"mixed", []string{"alpha", "beta"}, []string{"alpha"}, []string{"beta"}, false},
				{"excluded", []string{"beta"}, []string{}, []string{"beta"}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got := AssessKindRead(cache, "pods", tc.target)
					read, uncovered, complete := tc.scopedRead, tc.uncovered, tc.scopedComplete
					if namespace == "" {
						read, uncovered, complete = tc.target, nil, true
					}
					if got.Readiness != k8score.KindReady || got.Complete != complete || !slices.Equal(got.Read, read) || (got.Read == nil) != (read == nil) || !slices.Equal(got.Uncovered, uncovered) {
						t.Fatalf("target=%#v: assessment=%+v, want read=%#v uncovered=%v complete=%v", tc.target, got, read, uncovered, complete)
					}
				})
			}
		})
	}
}

func TestKindReadAssessmentMissingCache(t *testing.T) {
	for _, cache := range []*k8s.ResourceCache{nil, {}} {
		got := AssessKindRead(cache, "pods", []string{"alpha"})
		if got.Readiness != k8score.KindUnavailable || got.Complete || got.Read != nil {
			t.Fatalf("missing cache established coverage: %+v", got)
		}
	}
}
