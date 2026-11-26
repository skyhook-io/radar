package topology

import (
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/runtime"
)

// answerWithCoverage is RelationsAnswerWithExtras with only the coverage of
// its extras.
func answerWithCoverage(session relations.Session, access, traverse ownership.Access, ov *ownership.View, subject resourceid.Ref, obj runtime.Object, provider ResourceProvider, dp DynamicProvider) (*Relationships, map[string]relations.Coverage) {
	rel, extras := RelationsAnswerWithExtras(session, access, traverse, ov, subject, obj, provider, dp)
	if extras == nil {
		return rel, nil
	}
	return rel, extras.Coverage
}

// graphRelationships classifies a built topology's edges for one resource, as
// GetRelationshipsWithObject does without a fetched object or an index.
func graphRelationships(kind, namespace, name string, topo *Topology, provider ResourceProvider, dp DynamicProvider) *Relationships {
	return GetRelationshipsWithObject(kind, namespace, name, nil, topo, provider, dp, nil)
}

// graphRelationshipsWithIndex is graphRelationships through idx.
func graphRelationshipsWithIndex(kind, namespace, name string, topo *Topology, provider ResourceProvider, dp DynamicProvider, idx *RelationshipsIndex) *Relationships {
	return GetRelationshipsWithObject(kind, namespace, name, nil, topo, provider, dp, idx)
}
