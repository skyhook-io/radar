package ownership_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// changeManager rewrites manager in w at a later version and points s at the
// result.
func changeManager(t *testing.T, s *countingSession, w ownershiptest.World, manager resourceid.Ref, change func(*ownershiptest.Object)) {
	t.Helper()
	for i := range w.Objects {
		if w.Objects[i].Ref == manager {
			change(&w.Objects[i])
			w.Objects[i].ResourceVersion += "-later"
		}
	}
	later, err := ownershiptest.Scan(w).Begin()
	if err != nil {
		t.Fatal(err)
	}
	s.now = later
}

// A caller who may read no manager of a kind learns nothing of how Radar
// collects it.
func TestRecordingManagersHideGapsOfKindsTheCallerCantRead(t *testing.T) {
	w := ownershiptest.Fixture()
	w.Collection[ownership.ArgoApplication] = ownershiptest.Collection{State: ownershiptest.NotCollected}
	v := view(t, w, access{denied: []resourceid.GroupKind{ownership.ArgoApplication}})
	for _, g := range v.RecordingManagers(at("", "ConfigMap", "alpha", "cfg")).Gaps {
		if g.Kind == ownership.ArgoApplication {
			t.Errorf("a kind the caller can't read shows in a gap: %+v", g)
		}
	}
}

// fixedManagers answers every RecordingManagers search with answer.
type fixedManagers struct {
	ownership.ReadSession
	answer ownership.RecordingManagersRead
}

func (s fixedManagers) RecordingManagers(resourceid.Ref) ownership.RecordingManagersRead {
	return s.answer
}

// A manager's list was searched unless a gap leaves the manager's kind unread
// where the manager is.
func TestRecordingManagersAnswerSearched(t *testing.T) {
	scan, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	app := at(ownership.ArgoApplication.Group, ownership.ArgoApplication.Kind, "alpha", "app")
	gap := func(namespace string, reason readreason.Code) ownership.Gap {
		return ownership.Gap{Kind: ownership.ArgoApplication, Namespace: namespace, Reason: reason}
	}
	for _, tc := range []struct {
		name   string
		answer ownership.RecordingManagersRead
		want   bool
	}{
		{"no gaps", ownership.RecordingManagersRead{}, true},
		{"its namespace loading", ownership.RecordingManagersRead{Gaps: []ownership.Gap{gap("alpha", readreason.KindSyncPending)}}, false},
		{"another namespace loading", ownership.RecordingManagersRead{Gaps: []ownership.Gap{gap("beta", readreason.KindSyncPending)}}, true},
		{"the namespaces Radar doesn't collect", ownership.RecordingManagersRead{Gaps: []ownership.Gap{gap("", readreason.RadarNamespaceNotCollected)}}, true},
		{"the whole kind loading", ownership.RecordingManagersRead{Gaps: []ownership.Gap{gap("", readreason.KindSyncPending)}}, false},
		{"another kind loading", ownership.RecordingManagersRead{Gaps: []ownership.Gap{{Kind: ownership.FluxKustomization, Reason: readreason.KindSyncPending}}}, true},
	} {
		v := ownership.NewView(fixedManagers{scan, tc.answer}, ownership.Unrestricted())
		if got := v.RecordingManagers(at("", "ConfigMap", "alpha", "cfg")).Searched(app); got != tc.want {
			t.Errorf("%s: Searched = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// wholeKindUndecided can't decide whether the caller may read Applications in
// some namespace, though it allows them in alpha, as a bounded check across
// namespaces may leave it.
type wholeKindUndecided struct{ ownership.Access }

func (a wholeKindUndecided) CanRead(gk resourceid.GroupKind, namespace string) ownership.Decision {
	if gk == ownership.ArgoApplication && namespace == "" {
		return ownership.Undecided
	}
	return a.Access.CanRead(gk, namespace)
}

// A gap the caller may not read is left out of the answer, but still means the
// search didn't read what it covers.
func TestRecordingManagersSearchedCountsGapsTheCallerCantRead(t *testing.T) {
	scan, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		t.Fatal(err)
	}
	found := ownership.RecordingManagersRead{Gaps: []ownership.Gap{{Kind: ownership.ArgoApplication, Reason: readreason.KindSyncPending}}}
	v := ownership.NewView(fixedManagers{scan, found}, wholeKindUndecided{ownership.Unrestricted()})
	got := v.RecordingManagers(at("", "ConfigMap", "alpha", "cfg"))
	if len(got.Gaps) != 0 || got.Searched(at(ownership.ArgoApplication.Group, ownership.ArgoApplication.Kind, "alpha", "app")) {
		t.Fatalf("gaps %v, searched alpha/app = %v; want no gap shown and the list unsearched", got.Gaps, got.Searched(at(ownership.ArgoApplication.Group, ownership.ArgoApplication.Kind, "alpha", "app")))
	}
}

// A manager the caller may not read takes no part in the view's answers,
// however it changes between two searches.
func TestRecordingManagersIgnoreManagersTheCallerCantRead(t *testing.T) {
	for decision, hidden := range map[string]access{
		"denied":    {denied: []resourceid.GroupKind{ownership.ArgoApplication}},
		"undecided": {undecided: []resourceid.GroupKind{ownership.ArgoApplication}},
	} {
		for _, next := range []string{"recording", "remote", "readable empty", "deleted"} {
			t.Run(decision+"/"+next, func(t *testing.T) {
				b := ownershiptest.NewBuilder()
				first := b.Add("", "ConfigMap", "alpha", "first")
				second := b.Add("", "ConfigMap", "alpha", "second")
				app := b.Manager(ownership.ArgoApplication, "hidden", "app", ownership.MembersUnreadable, "alpha")
				w := b.World()
				s := newCountingSession(t, w)
				v := ownership.NewView(s, hidden)
				if got := v.RecordingManagers(first.Ref); len(got.Managers)+len(got.Unreadable) != 0 {
					t.Fatalf("RecordingManagers = %+v; want the manager hidden", got)
				}
				if next == "deleted" {
					w.Objects = slices.DeleteFunc(w.Objects, func(o ownershiptest.Object) bool { return o.Ref == app.Ref })
					later, err := ownershiptest.Scan(w).Begin()
					if err != nil {
						t.Fatal(err)
					}
					s.now = later
				} else {
					changeManager(t, s, w, app.Ref, func(o *ownershiptest.Object) {
						o.Membership = ownership.MembersRecorded
						switch next {
						case "recording":
							o.Members = []resourceid.Ref{second.Ref}
						case "remote":
							o.Membership, o.Members = ownership.MembersRemote, []resourceid.Ref{second.Ref}
						}
					})
				}
				if got := v.RecordingManagers(second.Ref); len(got.Managers)+len(got.Unreadable)+len(got.Remote) != 0 {
					t.Fatalf("RecordingManagers = %+v; want the manager hidden", got)
				}
			})
		}
	}
}

// BenchmarkRecordersOfManyUnreadableManagers is one search returning n
// managers whose lists can't be read, answered by a fresh view: work linear
// in n.
func BenchmarkRecordersOfManyUnreadableManagers(b *testing.B) {
	scan, err := ownershiptest.Scan(ownershiptest.Fixture()).Begin()
	if err != nil {
		b.Fatal(err)
	}
	for _, n := range []int{1000, 10000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			var found ownership.RecordingManagersRead
			for i := range n {
				found.Unreadable = append(found.Unreadable, at(ownership.ArgoApplication.Group, ownership.ArgoApplication.Kind, "argocd", fmt.Sprintf("app-%06d", i)))
			}
			cfg := at("", "ConfigMap", "alpha", "cfg")
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if got := ownership.NewView(fixedManagers{scan, found}, ownership.Unrestricted()).RecordingManagers(cfg); len(got.Unreadable) != n {
					b.Fatal(len(got.Unreadable))
				}
			}
		})
	}
}
