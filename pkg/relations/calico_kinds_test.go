package relations_test

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/relations"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
)

// The Calico families answer for every policy kind the topology reads, in
// both groups.
func TestCalicoFamiliesCoverEveryCalicoPolicyKind(t *testing.T) {
	for _, k := range topology.CalicoPolicyKinds() {
		for _, group := range topology.CalicoAPIGroups() {
			gk := resourceid.GroupKind{Group: group, Kind: k.Kind}
			family := relations.CalicoPolicies
			if k.Staged {
				family = relations.CalicoStagedPolicies
			}
			if !family.Covers(gk) {
				t.Errorf("%s doesn't cover %v", family.Name, gk)
			}
		}
	}
}
