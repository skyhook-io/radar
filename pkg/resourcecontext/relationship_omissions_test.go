package resourcecontext

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// A withheld Secret marks the field that lists Secrets, not every field the
// same relationship fills.
func TestWithheldTargetMarksOnlyItsField(t *testing.T) {
	extras := &topology.RelationshipExtras{
		Fields:        map[string]relations.Coverage{"ConfigRefs": {Narrowed: true}},
		WithheldKinds: map[string][]resourceid.GroupKind{"ConfigRefs": {{Kind: "Secret"}}},
	}
	omitted := newOmittedTracker()
	addRelationshipOmissions(extras, omitted)
	var denied []string
	for _, o := range omitted.items {
		if o.Reason == OmittedRBACDenied {
			denied = append(denied, o.Field)
		}
	}
	if len(denied) != 1 || denied[0] != "uses.secrets" {
		t.Fatalf("withheld fields = %v, want only uses.secrets", denied)
	}
}
