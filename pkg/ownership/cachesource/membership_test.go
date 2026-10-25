package cachesource

import (
	"fmt"
	"runtime"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func manager(gk resourceid.GroupKind, spec, status map[string]any) *unstructured.Unstructured {
	u := &unstructured.Unstructured{Object: map[string]any{"metadata": map[string]any{"namespace": "ops", "name": "m"}}}
	if spec != nil {
		u.Object["spec"] = spec
	}
	if status != nil {
		u.Object["status"] = status
	}
	u.SetKind(gk.Kind)
	return u
}

func atGeneration(u *unstructured.Unstructured, generation int64) *unstructured.Unstructured {
	u.SetGeneration(generation)
	return u
}

var localDestination = map[string]any{"destination": map[string]any{"namespace": "shop", "server": "https://kubernetes.default.svc"}}

// The status shapes Argo CD 3 and Flux 2.8 controllers write, read entry by
// entry; a list Radar can't trust in full is unreadable as a whole.
func TestMembersReadTheShapesControllersWrite(t *testing.T) {
	app, ks, hr := ownership.ArgoApplication, ownership.FluxKustomization, ownership.FluxHelmRelease
	for _, tc := range []struct {
		name  string
		obj   *unstructured.Unstructured
		state ownership.MembershipState
		want  []gitops.Member
	}{
		{"Argo CD entries, the core group left out", manager(app, localDestination, map[string]any{
			"reconciledAt": "2026-10-17T01:37:54Z",
			"resources": []any{
				map[string]any{"kind": "Service", "name": "ui", "namespace": "shop", "status": "Synced", "version": "v1"},
				map[string]any{"group": "apps", "kind": "Deployment", "name": "ui", "namespace": "shop", "status": "Synced", "version": "v1"},
				map[string]any{"group": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "reader", "status": "Synced", "version": "v1"},
			},
		}), ownership.MembersRecorded, []gitops.Member{{Kind: "Service", Namespace: "shop", Name: "ui"}, {Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "ui"}, {Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "reader"}}},
		{"Argo CD, reconciled with nothing to list", manager(app, localDestination, map[string]any{"reconciledAt": "2026-10-17T01:37:54Z"}), ownership.MembersRecorded, nil},
		{"Argo CD, never reconciled", manager(app, localDestination, nil), ownership.MembersUnreadable, nil},
		{"Argo CD, an explicit null list before reconciling", manager(app, localDestination, map[string]any{"resources": nil}), ownership.MembersUnreadable, nil},
		{"Argo CD, an entry without a name", manager(app, localDestination, map[string]any{"reconciledAt": "x", "resources": []any{map[string]any{"kind": "Service"}}}), ownership.MembersUnreadable, nil},
		{"Argo CD, an entry's namespace not a string", manager(app, localDestination, map[string]any{"reconciledAt": "x", "resources": []any{map[string]any{"kind": "Service", "name": "ui", "namespace": int64(3)}}}), ownership.MembersUnreadable, nil},
		{"Argo CD, resources not a list", manager(app, localDestination, map[string]any{"reconciledAt": "x", "resources": "ui"}), ownership.MembersUnreadable, nil},
		{"Argo CD, the in-cluster destination by name", manager(app, map[string]any{"destination": map[string]any{"name": "in-cluster", "namespace": "shop"}}, map[string]any{"resources": []any{}}), ownership.MembersRecorded, nil},
		{"Argo CD, another cluster", manager(app, map[string]any{"destination": map[string]any{"server": "https://prod.example.com:6443"}}, map[string]any{"resources": []any{map[string]any{"kind": "Service", "name": "ui"}}}), ownership.MembersRemote, []gitops.Member{{Kind: "Service", Name: "ui"}}},
		// A manager applying to another cluster names nothing here, whether or
		// not its list can be read.
		{"Argo CD, another cluster, never reconciled", manager(app, map[string]any{"destination": map[string]any{"name": "prod"}}, nil), ownership.MembersRemote, nil},
		{"Argo CD, another cluster, a failed comparison", manager(app, map[string]any{"destination": map[string]any{"server": "https://prod.example.com:6443"}}, map[string]any{"reconciledAt": "x", "conditions": []any{map[string]any{"type": "ComparisonError"}}}), ownership.MembersRemote, nil},
		{"Argo CD, no destination", manager(app, map[string]any{"destination": map[string]any{}}, map[string]any{"resources": []any{}}), ownership.MembersUnreadable, nil},
		{"a Kustomization's inventory", manager(ks, map[string]any{"prune": true, "targetNamespace": "shop"}, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_podinfo__Service", "v": "v1"},
			map[string]any{"id": "shop_podinfo_apps_Deployment", "v": "v1"},
			map[string]any{"id": "_reader_rbac.authorization.k8s.io_ClusterRole", "v": "v1"},
		}}}), ownership.MembersRecorded, []gitops.Member{{Kind: "Service", Namespace: "shop", Name: "podinfo"}, {Group: "apps", Kind: "Deployment", Namespace: "shop", Name: "podinfo"}, {Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "reader"}}},
		{"a HelmRelease's inventory", manager(hr, map[string]any{"releaseName": "web"}, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_web-podinfo__Service", "v": "v1"},
		}}}), ownership.MembersRecorded, []gitops.Member{{Kind: "Service", Namespace: "shop", Name: "web-podinfo"}}},
		{"RBAC names with colons, as Flux writes them", manager(hr, map[string]any{"releaseName": "metrics-server"}, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "_system__metrics-server_rbac.authorization.k8s.io_ClusterRole", "v": "v1"},
			map[string]any{"id": "kube-system_metrics-server__system__auth-delegator_rbac.authorization.k8s.io_RoleBinding", "v": "v1"},
		}}}), ownership.MembersRecorded, []gitops.Member{{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:metrics-server"}, {Group: "rbac.authorization.k8s.io", Kind: "RoleBinding", Namespace: "kube-system", Name: "metrics-server:system:auth-delegator"}}},
		{"an empty inventory", manager(ks, nil, map[string]any{"inventory": map[string]any{"entries": []any{}}}), ownership.MembersRecorded, nil},
		{"an inventory without entries", manager(ks, nil, map[string]any{"inventory": map[string]any{}}), ownership.MembersRecorded, nil},
		{"a HelmRelease from a controller that records no inventory", manager(hr, map[string]any{"releaseName": "web"}, map[string]any{"observedGeneration": int64(2)}), ownership.MembersUnreadable, nil},
		{"an ID that doesn't parse", manager(ks, nil, map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "podinfo", "v": "v1"}}}}), ownership.MembersUnreadable, nil},
		{"entries not a list", manager(ks, nil, map[string]any{"inventory": map[string]any{"entries": "podinfo"}}), ownership.MembersUnreadable, nil},
		{"a Kustomization applying elsewhere", manager(ks, map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod"}}}, map[string]any{"inventory": map[string]any{"entries": []any{}}}), ownership.MembersRemote, nil},
		{"a HelmRelease applying elsewhere", manager(hr, map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod"}}}, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_web-podinfo__Service", "v": "v1"},
		}}}), ownership.MembersRemote, []gitops.Member{{Kind: "Service", Namespace: "shop", Name: "web-podinfo"}}},
		{"a HelmRelease applying elsewhere, without an inventory", manager(hr, map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod"}}}, nil), ownership.MembersRemote, nil},
		{"a Kustomization whose spec changed since its inventory", atGeneration(manager(ks, nil, map[string]any{"observedGeneration": int64(1), "inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_ui__Service", "v": "v1"},
		}}}), 2), ownership.MembersRecorded, []gitops.Member{{Kind: "Service", Namespace: "shop", Name: "ui"}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			gk := ownership.ArgoApplication
			switch tc.obj.GetKind() {
			case ks.Kind:
				gk = ks
			case hr.Kind:
				gk = hr
			}
			got := membersOf(gk, tc.obj)
			if got.state != tc.state || !slices.Equal(got.Entries, tc.want) {
				t.Fatalf("got %v %v, want %v %v", got.state, got.Entries, tc.state, tc.want)
			}
			if state := memberState(gk, tc.obj); state != tc.state {
				t.Fatalf("memberState = %v, membersOf %v", state, tc.state)
			}
		})
	}
}

// RecordingManagers is Members inverted: over a fully collected World, every member
// a manager lists names it, as a recorder or, for a remote manager, as
// elsewhere; and every manager so named lists the member. This holds of the
// source whatever the oracle says.
func TestRecordingManagersInvertMembers(t *testing.T) {
	worlds := map[string]ownershiptest.World{"fixture": ownershiptest.Fixture()}
	for seed := range uint64(10) {
		w := ownershiptest.Generate(seed)
		w.Partial = false
		for gk, c := range w.Collection {
			if c.State != ownershiptest.Unwatchable {
				w.Collection[gk] = ownershiptest.Collection{OnDemand: c.OnDemand}
			}
		}
		worlds[fmt.Sprint("generated ", seed)] = w
	}
	for name, w := range worlds {
		t.Run(name, func(t *testing.T) {
			s := begin(t, render(t, w).source())
			q := ownershiptest.QueriesFor(w)
			listed := map[resourceid.Ref][]resourceid.Ref{}
			remote := map[resourceid.Ref]bool{}
			for _, m := range q.Managers {
				inv := s.Members(m)
				remote[m] = inv.Membership == ownership.MembersRemote
				for _, member := range inv.Members {
					listed[m] = append(listed[m], member)
					rec := s.RecordingManagers(member)
					named := slices.ContainsFunc(rec.Managers, func(o ownership.Observation) bool { return o.Ref == m })
					if remote[m] {
						named = slices.Contains(rec.Remote, m)
					}
					if !named && len(rec.Gaps) == 0 {
						t.Errorf("%v (%v) lists %v, but RecordingManagers(%v) = %+v", m, inv.Membership, member, member, rec)
					}
				}
			}
			for _, ref := range q.Refs {
				rec := s.RecordingManagers(ref)
				var named []resourceid.Ref
				for _, o := range rec.Managers {
					named = append(named, o.Ref)
				}
				for _, m := range named {
					if remote[m] || !slices.Contains(listed[m], ref) {
						t.Errorf("RecordingManagers(%v) names %v as a recorder; its Members lists %v", ref, m, listed[m])
					}
				}
				for _, m := range rec.Remote {
					if !remote[m] || !slices.Contains(listed[m], ref) {
						t.Errorf("RecordingManagers(%v) names %v as remote; its Members lists %v", ref, m, listed[m])
					}
				}
			}
		})
	}
}

// A built-in kind's scope is its own, unless discovery is ambiguous about the
// kind or serves it with the other scope: then no entry names it for certain,
// and its entries stay as recorded. The oracle says the same.
func TestRecordingManagersOfABuiltInDiscoveryDisputes(t *testing.T) {
	reader := resourceid.Ref{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "reader"}
	clusterRole := reader.GroupKind()
	gc := []string{"create", "delete", "get", "list", "patch", "update", "watch"}
	for name, served := range map[string][]ownership.ServedResource{
		"ambiguous": {
			{Group: clusterRole.Group, Version: "v1", Kind: clusterRole.Kind, Resource: "clusterroles", Verbs: gc},
			{Group: clusterRole.Group, Version: "v1", Kind: clusterRole.Kind, Resource: "tenantroles", Namespaced: true, Verbs: gc},
		},
		"served with the other scope": {
			{Group: clusterRole.Group, Version: "v1", Kind: clusterRole.Kind, Resource: "clusterroles", Namespaced: true, Verbs: gc},
		},
	} {
		t.Run(name, func(t *testing.T) {
			w := ownershiptest.World{Resources: append(ownershiptest.Resources(), served...), Collection: map[resourceid.GroupKind]ownershiptest.Collection{
				clusterRole: {State: ownershiptest.NotCollected},
			}}
			for _, k := range ownershiptest.Kinds {
				w.Collection[k.GroupKind] = ownershiptest.Collection{OnDemand: !k.Typed}
			}
			app := ownershiptest.Object{
				Ref:        resourceid.Ref{Group: ownership.ArgoApplication.Group, Kind: ownership.ArgoApplication.Kind, Namespace: "argocd", Name: "rbac"},
				APIVersion: "argoproj.io/v1alpha1", UID: "uid-rbac", ResourceVersion: "1",
				Membership: ownership.MembersRecorded, Members: []resourceid.Ref{reader}, DestinationNamespace: "alpha",
			}
			w.Objects = []ownershiptest.Object{app}
			scan, _ := ownershiptest.Scan(w).Begin()
			cache := begin(t, render(t, w).source())
			// The ClusterRole can't be named, so no manager kind was searched.
			var want []ownership.Gap
			for _, gk := range ownership.RecordingManagerKinds() {
				want = append(want, ownership.Gap{Kind: gk, Reason: readreason.ReadError})
			}
			slices.SortFunc(want, compareGaps)
			for source, s := range map[string]ownership.ReadSession{"scan": scan, "cache": cache} {
				if got := s.RecordingManagers(reader); len(got.Managers) != 0 || !slices.Equal(got.Gaps, want) {
					t.Errorf("%s: RecordingManagers(%v) = %+v, want every manager kind unread: %+v", source, reader, got, want)
				}
				if got := s.Members(app.Ref); got.Membership != ownership.MembersRecorded || !slices.Equal(got.Members, []resourceid.Ref{reader}) {
					t.Errorf("%s: Members = %v %v, want the entry as recorded", source, got.Membership, got.Members)
				}
			}
		})
	}
}

// A manager kind two resources of its group serve can't be read as either, so
// a manager of that kind may record the object unseen.
func TestRecordingManagersLeaveAnAmbiguousManagerKindUnread(t *testing.T) {
	w := ownershiptest.World{Resources: ownershiptest.Resources(), Collection: map[resourceid.GroupKind]ownershiptest.Collection{}}
	for _, k := range ownershiptest.Kinds {
		w.Collection[k.GroupKind] = ownershiptest.Collection{OnDemand: !k.Typed}
	}
	w.Resources = append(w.Resources, ownership.ServedResource{Group: ownership.ArgoApplication.Group, Version: "v1alpha1", Kind: ownership.ArgoApplication.Kind,
		Resource: "otherapplications", Namespaced: true, Verbs: []string{"delete", "get", "list", "watch"}})
	got := begin(t, render(t, w).source()).RecordingManagers(resourceid.Ref{Kind: "ConfigMap", Namespace: "alpha", Name: "cfg"})
	if want := (ownership.Gap{Kind: ownership.ArgoApplication, Reason: readreason.ReadError}); !slices.Contains(got.Gaps, want) || got.Complete() {
		t.Fatalf("RecordingManagers = %+v, complete %v; want the gap %+v", got, got.Complete(), want)
	}
}

// An answer is complete only when no remote manager may record the object
// and no manager's list is unreadable.
func TestRecordingManagersAreCompleteOnlyWithoutDoubt(t *testing.T) {
	cfg := resourceid.Ref{Kind: "ConfigMap", Namespace: "alpha", Name: "cfg"}
	app := func(name string, state ownership.MembershipState, members ...resourceid.Ref) ownershiptest.Object {
		return ownershiptest.Object{
			Ref:        resourceid.Ref{Group: ownership.ArgoApplication.Group, Kind: ownership.ArgoApplication.Kind, Namespace: "argocd", Name: name},
			APIVersion: "argoproj.io/v1alpha1", UID: types.UID("uid-" + name), ResourceVersion: "1",
			Membership: state, Members: members, DestinationNamespace: "alpha",
		}
	}
	for _, tc := range []struct {
		name     string
		managers []ownershiptest.Object
		complete bool
	}{
		{"a local recorder", []ownershiptest.Object{app("local", ownership.MembersRecorded, cfg)}, true},
		{"a remote manager listing it too", []ownershiptest.Object{app("local", ownership.MembersRecorded, cfg), app("prod", ownership.MembersRemote, cfg)}, false},
		{"a remote manager listing something else", []ownershiptest.Object{app("local", ownership.MembersRecorded, cfg), app("prod", ownership.MembersRemote)}, true},
		{"an unreadable manager", []ownershiptest.Object{app("local", ownership.MembersRecorded, cfg), app("new", ownership.MembersUnreadable)}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := ownershiptest.World{Resources: ownershiptest.Resources(), Collection: map[resourceid.GroupKind]ownershiptest.Collection{}, Objects: tc.managers}
			for _, k := range ownershiptest.Kinds {
				w.Collection[k.GroupKind] = ownershiptest.Collection{OnDemand: !k.Typed}
			}
			got := begin(t, render(t, w).source()).RecordingManagers(cfg)
			if got.Complete() != tc.complete || len(got.Managers) != 1 {
				t.Fatalf("RecordingManagers = %+v: complete %v, want %v", got, got.Complete(), tc.complete)
			}
		})
	}
}

// Flux writes a colon in an RBAC object's name as a double underscore; its
// entry still names the object the API does, which a chart's ClusterRoles
// such as system:metrics-server need to be found at all.
func TestMembersOfRBACNamesWithColons(t *testing.T) {
	role := resourceid.Ref{Group: "rbac.authorization.k8s.io", Kind: "ClusterRole", Name: "system:metrics-server"}
	release := ownershiptest.Object{
		Ref:        resourceid.Ref{Group: ownership.FluxHelmRelease.Group, Kind: ownership.FluxHelmRelease.Kind, Namespace: "kube-system", Name: "metrics-server"},
		APIVersion: "helm.toolkit.fluxcd.io/v2", UID: "uid-metrics-server", ResourceVersion: "1",
		Membership: ownership.MembersRecorded, Members: []resourceid.Ref{role},
	}
	w := ownershiptest.World{Resources: ownershiptest.Resources(), Collection: map[resourceid.GroupKind]ownershiptest.Collection{}, Objects: []ownershiptest.Object{release}}
	for _, k := range ownershiptest.Kinds {
		w.Collection[k.GroupKind] = ownershiptest.Collection{OnDemand: !k.Typed}
	}
	s := begin(t, render(t, w).source())
	if got := s.Members(release.Ref); !slices.Equal(got.Members, []resourceid.Ref{role}) {
		t.Fatalf("Members = %v, want %v", got.Members, role)
	}
	got := s.RecordingManagers(role)
	if len(got.Managers) != 1 || got.Managers[0].Ref != release.Ref || !got.Complete() {
		t.Fatalf("RecordingManagers(%v) = %+v, want %v alone", role, got, release.Ref)
	}
}

// RecordingManagers reads each manager kind once, asking for the unreadable key and
// every placement of the object in the same read. That one read takes all of them
// in one pass under the store's lock (TestReadableIndexedReadsItsKeysTogether),
// so a manager changing during the search, from an unreadable list to one
// naming the object or from one placement of its entry to another, is found in
// one of its states; one read per key could land between two changes and find
// it in none.
func TestRecordingManagersReadEachManagerKindOnce(t *testing.T) {
	s := begin(t, render(t, ownershiptest.Fixture()).source()).(*session)
	reads := map[resourceid.GroupKind][][]string{}
	s.recordingManagersRead = func(kind resourceid.GroupKind, keys []string) {
		reads[kind] = append(reads[kind], slices.Clone(keys))
	}
	s.RecordingManagers(resourceid.Ref{Group: "example.io", Kind: "Widget", Namespace: "alpha", Name: "w1"})
	want := []string{
		k8score.UnreadableMembersKey,
		k8score.MemberKey(k8score.MemberAsRecorded, "example.io", "Widget", "alpha", "w1"),
		k8score.MemberKey(k8score.MemberInDestination, "example.io", "Widget", "alpha", "w1"),
	}
	for _, gk := range ownership.RecordingManagerKinds() {
		if got := reads[gk]; len(got) != 1 || !slices.Equal(got[0], want) {
			t.Errorf("%v read %q, want one read of %q", gk, got, want)
		}
	}
}

// A search classifies every manager whose list names the object. Opening a
// member of a 10,000-object Application or Kustomization must not cost what
// building its list would.
func TestMemberStateCostDoesNotGrowWithTheList(t *testing.T) {
	argo := func(n int) *unstructured.Unstructured {
		entries := make([]any, n)
		for i := range entries {
			entries[i] = map[string]any{"kind": "Service", "name": fmt.Sprint("svc-", i), "namespace": "shop"}
		}
		return manager(ownership.ArgoApplication, localDestination, map[string]any{"reconciledAt": "x", "resources": entries})
	}
	flux := func(n int) *unstructured.Unstructured {
		entries := make([]any, n)
		for i := range entries {
			entries[i] = map[string]any{"id": fmt.Sprintf("shop_svc-%d__Service", i), "v": "v1"}
		}
		return manager(ownership.FluxKustomization, nil, map[string]any{"inventory": map[string]any{"entries": entries}})
	}
	for _, tc := range []struct {
		gk           resourceid.GroupKind
		small, large *unstructured.Unstructured
	}{
		{ownership.ArgoApplication, argo(40), argo(10000)},
		{ownership.FluxKustomization, flux(40), flux(10000)},
	} {
		if state := memberState(tc.gk, tc.large); state != ownership.MembersRecorded {
			t.Fatalf("%v: %v", tc.gk, state)
		}
		small := testing.AllocsPerRun(20, func() { memberState(tc.gk, tc.small) })
		large := testing.AllocsPerRun(20, func() { memberState(tc.gk, tc.large) })
		if large > small {
			t.Errorf("%v: %v allocations for 10,000 entries, %v for 40", tc.gk.Kind, large, small)
		}
	}
}

// Opening a member of a large Application or Kustomization searches for the
// managers recording it; that costs the same however long the list naming it
// is, RBAC names Flux encodes included. Counting allocations can't tell:
// building a list sized up front is one allocation at any length.
func TestRecordingManagersCostDoesNotGrowWithTheManagersList(t *testing.T) {
	rbac := func(name string) resourceid.Ref {
		return resourceid.Ref{Group: "rbac.authorization.k8s.io", Kind: "RoleBinding", Namespace: "shop", Name: name}
	}
	service := func(name string) resourceid.Ref {
		return resourceid.Ref{Kind: "Service", Namespace: "shop", Name: name}
	}
	for _, tc := range []struct {
		manager resourceid.GroupKind
		member  func(string) resourceid.Ref
	}{
		{ownership.ArgoApplication, service},
		{ownership.FluxKustomization, func(name string) resourceid.Ref { return rbac("metrics-server:" + name) }},
	} {
		t.Run(tc.manager.Kind, func(t *testing.T) {
			target := tc.member("ui")
			bytesPerSearch := func(n int) uint64 {
				members := []resourceid.Ref{target}
				for i := 1; i < n; i++ {
					members = append(members, tc.member(fmt.Sprint("svc-", i)))
				}
				k, _ := ownershiptest.KindOf(tc.manager)
				m := ownershiptest.Object{Ref: resourceid.Ref{Group: k.Group, Kind: k.Kind, Namespace: "ops", Name: "big"}, APIVersion: k.APIVersion(),
					UID: "uid-big", ResourceVersion: "1", Membership: ownership.MembersRecorded, Members: members}
				w := ownershiptest.World{Resources: ownershiptest.Resources(), Collection: map[resourceid.GroupKind]ownershiptest.Collection{}, Objects: []ownershiptest.Object{m}}
				for _, item := range ownershiptest.Kinds {
					w.Collection[item.GroupKind] = ownershiptest.Collection{OnDemand: !item.Typed}
				}
				s := begin(t, render(t, w).source())
				if got := s.RecordingManagers(target); len(got.Managers) != 1 {
					t.Fatalf("RecordingManagers(%v) = %+v", target, got)
				}
				const searches = 20
				var before, after runtime.MemStats
				runtime.ReadMemStats(&before)
				for range searches {
					s.RecordingManagers(target)
				}
				runtime.ReadMemStats(&after)
				return (after.TotalAlloc - before.TotalAlloc) / searches
			}
			small, large := bytesPerSearch(40), bytesPerSearch(10000)
			if large > small+64<<10 {
				t.Fatalf("%d bytes per search when a 10,000-entry list names the object, %d for 40", large, small)
			}
		})
	}
}
