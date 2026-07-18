package meaningfulchanges

import "testing"

// Every kind the feed tracks must have a trackedKindGroups entry — a kind
// added to configKinds/specKinds/lifecycleOnlyKinds without one would
// silently stop correlating (TrackedKindForGroup returns false for it).
// White-box on purpose: iterates the real slices so new kinds fail here.
func TestTrackedKindGroups_CoversAllTrackedKinds(t *testing.T) {
	var all []string
	all = append(all, configKinds...)
	all = append(all, specKinds...)
	all = append(all, lifecycleOnlyKinds...)
	for _, kind := range all {
		if _, ok := trackedKindGroups[canonicalKind(kind)]; !ok {
			t.Errorf("tracked kind %q has no trackedKindGroups entry — correlation silently disabled for it", kind)
		}
	}
}
