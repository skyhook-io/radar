package topology

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
)

// Which manager lists Radar couldn't search stays internal: the state counts
// it, and for the resource itself OwnershipCoverage.Managers says what its
// own search missed.
func TestManagerAnswerCarriesNoSearchCoverage(t *testing.T) {
	b := ownershiptest.NewBuilder()
	cm := b.Add("", "ConfigMap", "alpha", "c")
	b.Manager(ownership.ArgoApplication, "argocd", "broken", ownership.MembersUnreadable, "alpha")
	b.Manager(ownership.FluxKustomization, "flux-system", "elsewhere", ownership.MembersRemote, "", cm.Ref)
	w := b.World()
	rel := &Relationships{}
	ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, cm.Ref))
	if rel.Ownership.Manager == nil || rel.Ownership.Manager.State != "unknown" {
		t.Fatalf("manager %+v; want unknown, its search incomplete", rel.Ownership.Manager)
	}
	if rel.Ownership.Managers == nil || rel.Ownership.Managers.Unreadable != 1 || rel.Ownership.Managers.Remote != 1 {
		t.Fatalf("managers coverage %+v; want the resource's own search gaps kept", rel.Ownership.Managers)
	}
	raw, err := json.Marshal(rel.Ownership.Manager)
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{`"unreadable"`, `"remote"`, `"notChecked"`, `"undecided"`} {
		if strings.Contains(string(raw), key) {
			t.Errorf("manager answer carries %s: %s", key, raw)
		}
	}
}

// An ancestor kept only because its own search had gaps leaves the answer
// unknown, but isn't sent as an object with nothing to say.
func TestManagerAnswerDropsAncestorsKeptOnlyForGaps(t *testing.T) {
	b := ownershiptest.NewBuilder()
	d := b.Add("apps", "Deployment", "alpha", "web")
	rs := b.Add("apps", "ReplicaSet", "alpha", "web-1", d.OwnerRef(true))
	pod := b.Add("", "Pod", "alpha", "web-1-a", rs.OwnerRef(true))
	b.Manager(ownership.FluxKustomization, "flux-system", "elsewhere", ownership.MembersRemote, "", d.Ref)
	w := b.World()
	v := ownershipView(t, w, ownership.Unrestricted())
	if internal := v.Manager(pod.Ref); internal.State != ownership.ManagerUnknown || len(internal.Between) == 0 {
		t.Fatalf("scenario: internally %+v; want unknown with the Deployment in Between", internal)
	}
	rel := &Relationships{}
	ApplyOwnership(rel, ownershipView(t, w, ownership.Unrestricted()), fetched(t, w, pod.Ref))
	if got := rel.Ownership.Manager; got == nil || got.State != "unknown" || len(got.Between) != 0 {
		t.Fatalf("manager %+v; want unknown without a between entry", got)
	}
}
