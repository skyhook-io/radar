package cachesource

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
)

// BenchmarkDependentCandidatesWithManyUnwatchedKinds is a search on a cluster
// serving 300 custom kinds Radar doesn't watch, as most clusters do: each is a
// gap, and the session works out why once.
func BenchmarkDependentCandidatesWithManyUnwatchedKinds(b *testing.B) {
	f := newFixture(b, k8score.CacheConfig{}, func(f *fixture) {
		fd := f.typed.Discovery().(*fakediscovery.FakeDiscovery)
		var crds []metav1.APIResource
		for i := range 300 {
			crds = append(crds, metav1.APIResource{Name: fmt.Sprint("things", i), Kind: fmt.Sprint("Thing", i), Namespaced: true, Verbs: []string{"delete", "get", "list", "watch"}})
		}
		fd.Resources = append(fd.Resources, &metav1.APIResourceList{GroupVersion: "things.example.io/v1", APIResources: crds})
	})
	s, err := f.source().Begin()
	if err != nil {
		b.Fatal(err)
	}
	if got := s.DependentCandidates("uid-rs", ownership.Everywhere(), 0); len(got.Gaps) < 300 {
		b.Fatalf("%d gaps, want one per unwatched kind", len(got.Gaps))
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.DependentCandidates("uid-rs", ownership.Everywhere(), 0)
	}
}
