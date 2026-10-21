package cachesource

import (
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/ownership"
)

// Argo CD's status.resources describes the destination it last compared,
// which lags a changed spec: an Application moved here still lists the other
// cluster's objects until Argo CD compares again, and one moved away still
// lists this cluster's.
func TestMembersFollowTheComparedDestination(t *testing.T) {
	remote := map[string]any{"name": "prod", "namespace": "shop"}
	local := map[string]any{"server": "https://kubernetes.default.svc", "namespace": "shop"}
	for _, tc := range []struct {
		name           string
		spec, compared map[string]any
		state          ownership.MembershipState
	}{
		{"moved here, compared elsewhere", local, remote, ownership.MembersRemote},
		{"moved elsewhere, compared here", remote, local, ownership.MembersRecorded},
		{"compared where the spec says", remote, remote, ownership.MembersRemote},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := manager(ownership.ArgoApplication, map[string]any{"destination": tc.spec}, map[string]any{
				"reconciledAt": "2026-10-17T01:37:54Z",
				"sync":         map[string]any{"comparedTo": map[string]any{"destination": tc.compared}},
				"resources":    []any{map[string]any{"kind": "Service", "name": "ui", "namespace": "shop", "version": "v1"}},
			})
			got := membersOf(ownership.ArgoApplication, app)
			if got.state != tc.state || !slices.Equal(got.Entries, []gitops.Member{{Kind: "Service", Namespace: "shop", Name: "ui"}}) {
				t.Fatalf("got %v %v, want %v", got.state, got.Entries, tc.state)
			}
			if here := appliesHere(ownership.ArgoApplication, app); here != (tc.state == ownership.MembersRecorded) {
				t.Fatalf("appliesHere = %v, want %v", here, !here)
			}
		})
	}
}
