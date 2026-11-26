package server

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/internal/integration"
)

func TestMergeTraefikGroupAccess(t *testing.T) {
	full := integration.KindAccess{State: integration.KindCoverageFull, All: true}
	partial := integration.AccessFromScope([]string{"shop", "web"}, true)
	partial.Denied = []string{"ops"}
	denied := integration.KindAccess{State: integration.KindCoverageDenied}
	syncing := integration.KindAccess{State: integration.KindCoverageSyncing}
	absent := integration.KindAccess{State: integration.KindCoverageNotInstalled}

	for _, tc := range []struct {
		name  string
		in    []integration.KindAccess
		state string
		ns    []string // allowed namespaces on a partial answer
	}{
		{"every group read", []integration.KindAccess{full, full}, integration.KindCoverageFull, nil},
		{"one group denied keeps the other's routes", []integration.KindAccess{denied, full}, integration.KindCoveragePartial, []string{}},
		{"one group syncing keeps the other's routes", []integration.KindAccess{full, syncing}, integration.KindCoveragePartial, []string{}},
		{"a group not installed doesn't narrow", []integration.KindAccess{absent, full}, integration.KindCoverageFull, nil},
		{"partial in one, full in the other", []integration.KindAccess{full, partial}, integration.KindCoveragePartial, []string{"shop", "web"}},
		{"nothing read waits while syncing", []integration.KindAccess{denied, syncing}, integration.KindCoverageSyncing, nil},
		{"nothing read reports the failure", []integration.KindAccess{absent, denied}, integration.KindCoverageDenied, nil},
		{"no group installed", []integration.KindAccess{absent, absent}, integration.KindCoverageNotInstalled, nil},
	} {
		got := mergeTraefikGroupAccess(tc.in)
		if got.State != tc.state {
			t.Errorf("%s: state %q, want %q", tc.name, got.State, tc.state)
			continue
		}
		if tc.state == integration.KindCoveragePartial {
			if allowed := got.Coverage().AllowedNamespaces; !slices.Equal(allowed, tc.ns) {
				t.Errorf("%s: allowed %v, want %v", tc.name, allowed, tc.ns)
			}
		}
	}
	if got := mergeTraefikGroupAccess([]integration.KindAccess{full, partial}); !slices.Equal(got.Denied, []string{"ops"}) {
		t.Errorf("unread namespaces carry over: %+v", got)
	}
}
