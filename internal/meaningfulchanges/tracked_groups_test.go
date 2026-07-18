package meaningfulchanges

import "testing"

// trackedKindGroups must cover EXACTLY TrackedKind's set (configKinds +
// specKinds): a kind added there without a group entry silently stops
// correlating, and a kind present here beyond that set becomes
// marker-eligible without its updates being recorded — Secret
// (lifecycleOnlyKinds, delete-only) would emit false no_recent_changes
// after a data rotation the feed cannot see. White-box on purpose:
// iterates the real slices so drift fails here.
func TestTrackedKindGroups_MatchesTrackedKindSet(t *testing.T) {
	tracked := map[string]bool{}
	for _, kind := range append(append([]string{}, configKinds...), specKinds...) {
		tracked[canonicalKind(kind)] = true
		if _, ok := trackedKindGroups[canonicalKind(kind)]; !ok {
			t.Errorf("tracked kind %q has no trackedKindGroups entry — correlation silently disabled for it", kind)
		}
	}
	for kind := range trackedKindGroups {
		if !tracked[kind] {
			t.Errorf("%q is in trackedKindGroups but not tracked by the feed — marker-eligible without recorded updates (false no_recent_changes)", kind)
		}
	}
}
