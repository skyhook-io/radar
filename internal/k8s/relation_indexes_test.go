package k8s

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Relation indexes register by the resource each source kind is served as,
// from a hand-kept table: each must be the built-in kind's resource or the
// one the CRD fallback catalog serves the kind as, or the index lands on a
// resource no store reads and every reverse read is a read error.
func TestRelationIndexResourcesMatchTheCatalogs(t *testing.T) {
	expected := map[schema.GroupResource]bool{}
	for _, f := range relations.Families("") {
		if f.ReferrersIndex == nil {
			continue
		}
		for _, source := range f.Sources {
			var want schema.GroupResource
			if b, ok := resourceid.BuiltinForKind(source.Kind.Kind); ok && b.Group == source.Kind.Group {
				want = schema.GroupResource{Group: b.Group, Resource: b.Resource}
			} else {
				i := slices.IndexFunc(supportedCRDFallbacks, func(c supportedCRDResource) bool {
					return c.Group == source.Kind.Group && c.Kind == source.Kind.Kind
				})
				if i < 0 {
					t.Errorf("%s source %v has no supportedCRDFallbacks entry", f.Name, source.Kind)
					continue
				}
				want = schema.GroupResource{Group: source.Kind.Group, Resource: supportedCRDFallbacks[i].Resource}
			}
			expected[want] = true
			if relations.ReferrerIndexers[want][f.ReferrersIndex.Name] == nil {
				t.Errorf("%s source %v is served as %v, which lacks %s", f.Name, source.Kind, want, f.ReferrersIndex.Name)
			}
		}
	}
	for gr := range relations.ReferrerIndexers {
		if !expected[gr] {
			t.Errorf("an index is registered on %v, which no source kind is served as", gr)
		}
	}
}
