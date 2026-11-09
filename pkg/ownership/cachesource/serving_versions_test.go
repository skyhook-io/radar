package cachesource

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestServingVersionsKeepsCatalogKindsSeparate(t *testing.T) {
	stable := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	beta := stable
	beta.Version = "v1beta1"
	s := &session{watched: map[schema.GroupResource][]schema.GroupVersionResource{stable.GroupResource(): {beta, stable}}}
	kinds := []ownership.CatalogKind{
		{GroupKind: resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, Resource: "widgets", Versions: []string{"v1"}},
		{GroupKind: resourceid.GroupKind{Group: "example.io", Kind: "BetaWidget"}, Resource: "widgets", Versions: []string{"v1beta1"}},
	}
	for range 2 {
		for i, kind := range kinds {
			want := []schema.GroupVersionResource{stable}
			if i == 1 {
				want[0] = beta
			}
			if got := s.servingVersions(kind); !slices.Equal(got, want) {
				t.Fatalf("%s: versions %v, want %v", kind.Kind, got, want)
			}
		}
	}
}

func BenchmarkServingVersions(b *testing.B) {
	stable := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	beta := stable
	beta.Version = "v1beta1"
	s := &session{watched: map[schema.GroupResource][]schema.GroupVersionResource{stable.GroupResource(): {beta, stable}}}
	kind := ownership.CatalogKind{GroupKind: resourceid.GroupKind{Group: "example.io", Kind: "Widget"}, Resource: "widgets", Versions: []string{"v1", "v1beta1"}}
	s.servingVersions(kind)
	b.ReportAllocs()
	b.ResetTimer()
	for b.Loop() {
		s.servingVersions(kind)
	}
}
