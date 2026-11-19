package k8score

import (
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// typedRelationIndexers are the relation families' reverse indexes on a
// typed kind that is a family's source; dynamic informers take theirs by
// GroupResource when they start.
func typedRelationIndexers(kind string) cache.Indexers {
	b, ok := resourceid.BuiltinForKind(kind)
	if !ok {
		return nil
	}
	return relations.ReferrerIndexers[schema.GroupResource{Group: b.Group, Resource: b.Resource}]
}
