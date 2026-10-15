package cnpg

import (
	"slices"
	"testing"
	"time"

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

func TestKindReadAssessmentMultipleNamespaces(t *testing.T) {
	core, err := k8score.NewResourceCache(k8score.CacheConfig{
		Client:                  fake.NewClientset(),
		ResourceScopes:          map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "alpha"}},
		ResourceScopeNamespaces: map[string][]string{"pods": {"alpha", "beta"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(core.Stop)
	cache := &k8s.ResourceCache{ResourceCache: core}
	for _, tc := range []struct {
		name                    string
		target, read, uncovered []string
		complete                bool
	}{
		{"mixed", []string{"beta", "gamma"}, []string{"beta"}, []string{"gamma"}, false},
		{"requested order", []string{"beta", "alpha"}, []string{"beta", "alpha"}, nil, true},
		{"all", nil, []string{"alpha", "beta"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := AssessKindRead(cache, "pods", tc.target)
			if got.Readiness != k8score.KindReady || got.Complete != tc.complete || !slices.Equal(got.Read, tc.read) || !slices.Equal(got.Uncovered, tc.uncovered) {
				t.Fatalf("assessment=%+v, want read=%v uncovered=%v complete=%v", got, tc.read, tc.uncovered, tc.complete)
			}
		})
	}
}

func TestKindReadAssessmentBeforeInitialSync(t *testing.T) {
	for _, readiness := range []k8score.KindReadiness{k8score.KindPending, k8score.KindFailed} {
		t.Run(map[k8score.KindReadiness]string{k8score.KindPending: "pending", k8score.KindFailed: "failed"}[readiness], func(t *testing.T) {
			timeout := time.Minute
			if readiness == k8score.KindFailed {
				timeout = time.Millisecond
			}
			core, err := k8score.NewResourceCache(k8score.CacheConfig{
				Client:              fake.NewClientset(),
				ResourceScopes:      map[string]k8score.ResourceScope{"services": {Enabled: true}, "configmaps": {Enabled: true, Namespace: "alpha"}},
				DeferredTypes:       map[string]bool{"configmaps": true},
				DebugSyncDelays:     map[string]time.Duration{"configmaps": time.Hour},
				DeferredSyncTimeout: timeout,
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(core.Stop)
			deadline := time.Now().Add(5 * time.Second)
			for core.KindReadinessFor("configmaps") != readiness && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			if got := core.KindReadinessFor("configmaps"); got != readiness {
				t.Fatalf("readiness=%v, want %v", got, readiness)
			}
			cache := &k8s.ResourceCache{ResourceCache: core}
			for _, tc := range []struct {
				name              string
				target, uncovered []string
			}{
				{"covered", []string{"alpha"}, nil},
				{"mixed", []string{"alpha", "beta"}, []string{"beta"}},
			} {
				t.Run(tc.name, func(t *testing.T) {
					got := AssessKindRead(cache, "configmaps", tc.target)
					if got.Readiness != readiness || got.Read != nil || got.Complete || !slices.Equal(got.Uncovered, tc.uncovered) {
						t.Fatalf("assessment=%+v, want readiness=%v unread with uncovered=%v", got, readiness, tc.uncovered)
					}
				})
			}
		})
	}
}
