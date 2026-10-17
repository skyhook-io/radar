package cachesource

import (
	"fmt"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"

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

var localDestination = map[string]any{"destination": map[string]any{"namespace": "shop", "server": "https://kubernetes.default.svc"}}

// The status shapes Argo CD 3 and Flux 2.7 controllers write, read entry by
// entry; a list Radar can't trust in full is unreadable as a whole.
func TestMembersReadTheShapesControllersWrite(t *testing.T) {
	app, ks, hr := ownership.ArgoApplication, ownership.FluxKustomization, ownership.FluxHelmRelease
	for _, tc := range []struct {
		name  string
		obj   *unstructured.Unstructured
		state ownership.MembershipState
		want  []memberEntry
	}{
		{"Argo CD entries, the core group left out", manager(app, localDestination, map[string]any{
			"reconciledAt": "2026-10-17T01:37:54Z",
			"resources": []any{
				map[string]any{"kind": "Service", "name": "ui", "namespace": "shop", "status": "Synced", "version": "v1"},
				map[string]any{"group": "apps", "kind": "Deployment", "name": "ui", "namespace": "shop", "status": "Synced", "version": "v1"},
				map[string]any{"group": "rbac.authorization.k8s.io", "kind": "ClusterRole", "name": "reader", "status": "Synced", "version": "v1"},
			},
		}), ownership.MembersRecorded, []memberEntry{{"", "Service", "shop", "ui"}, {"apps", "Deployment", "shop", "ui"}, {"rbac.authorization.k8s.io", "ClusterRole", "", "reader"}}},
		{"Argo CD, reconciled with nothing to list", manager(app, localDestination, map[string]any{"reconciledAt": "2026-10-17T01:37:54Z"}), ownership.MembersRecorded, nil},
		{"Argo CD, never reconciled", manager(app, localDestination, nil), ownership.MembersUnreadable, nil},
		{"Argo CD, an explicit null list before reconciling", manager(app, localDestination, map[string]any{"resources": nil}), ownership.MembersUnreadable, nil},
		{"Argo CD, an entry without a name", manager(app, localDestination, map[string]any{"reconciledAt": "x", "resources": []any{map[string]any{"kind": "Service"}}}), ownership.MembersUnreadable, nil},
		{"Argo CD, an entry's namespace not a string", manager(app, localDestination, map[string]any{"reconciledAt": "x", "resources": []any{map[string]any{"kind": "Service", "name": "ui", "namespace": int64(3)}}}), ownership.MembersUnreadable, nil},
		{"Argo CD, resources not a list", manager(app, localDestination, map[string]any{"reconciledAt": "x", "resources": "ui"}), ownership.MembersUnreadable, nil},
		{"Argo CD, the in-cluster destination by name", manager(app, map[string]any{"destination": map[string]any{"name": "in-cluster", "namespace": "shop"}}, map[string]any{"resources": []any{}}), ownership.MembersRecorded, nil},
		{"Argo CD, another cluster", manager(app, map[string]any{"destination": map[string]any{"server": "https://prod.example.com:6443"}}, map[string]any{"resources": []any{map[string]any{"kind": "Service", "name": "ui"}}}), ownership.MembersRemote, []memberEntry{{"", "Service", "", "ui"}}},
		{"Argo CD, another cluster, never reconciled", manager(app, map[string]any{"destination": map[string]any{"name": "prod"}}, nil), ownership.MembersUnreadable, nil},
		{"Argo CD, no destination", manager(app, map[string]any{"destination": map[string]any{}}, map[string]any{"resources": []any{}}), ownership.MembersUnreadable, nil},
		{"a Kustomization's inventory", manager(ks, map[string]any{"prune": true, "targetNamespace": "shop"}, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_podinfo__Service", "v": "v1"},
			map[string]any{"id": "shop_podinfo_apps_Deployment", "v": "v1"},
			map[string]any{"id": "_reader_rbac.authorization.k8s.io_ClusterRole", "v": "v1"},
		}}}), ownership.MembersRecorded, []memberEntry{{"", "Service", "shop", "podinfo"}, {"apps", "Deployment", "shop", "podinfo"}, {"rbac.authorization.k8s.io", "ClusterRole", "", "reader"}}},
		{"a HelmRelease's inventory", manager(hr, map[string]any{"releaseName": "web"}, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_web-podinfo__Service", "v": "v1"},
		}}}), ownership.MembersRecorded, []memberEntry{{"", "Service", "shop", "web-podinfo"}}},
		{"an empty inventory", manager(ks, nil, map[string]any{"inventory": map[string]any{"entries": []any{}}}), ownership.MembersRecorded, nil},
		{"an inventory without entries", manager(ks, nil, map[string]any{"inventory": map[string]any{}}), ownership.MembersRecorded, nil},
		{"a HelmRelease from a controller that records no inventory", manager(hr, map[string]any{"releaseName": "web"}, map[string]any{"observedGeneration": int64(2)}), ownership.MembersUnreadable, nil},
		{"an ID that doesn't parse", manager(ks, nil, map[string]any{"inventory": map[string]any{"entries": []any{map[string]any{"id": "podinfo", "v": "v1"}}}}), ownership.MembersUnreadable, nil},
		{"entries not a list", manager(ks, nil, map[string]any{"inventory": map[string]any{"entries": "podinfo"}}), ownership.MembersUnreadable, nil},
		{"a Kustomization applying elsewhere", manager(ks, map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod"}}}, map[string]any{"inventory": map[string]any{"entries": []any{}}}), ownership.MembersRemote, nil},
		{"a HelmRelease applying elsewhere", manager(hr, map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod"}}}, map[string]any{"inventory": map[string]any{"entries": []any{
			map[string]any{"id": "shop_web-podinfo__Service", "v": "v1"},
		}}}), ownership.MembersRemote, []memberEntry{{"", "Service", "shop", "web-podinfo"}}},
		{"a HelmRelease applying elsewhere, without an inventory", manager(hr, map[string]any{"kubeConfig": map[string]any{"secretRef": map[string]any{"name": "prod"}}}, nil), ownership.MembersUnreadable, nil},
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
			if got.state != tc.state || !slices.Equal(got.entries, tc.want) {
				t.Fatalf("got %v %v, want %v %v", got.state, got.entries, tc.state, tc.want)
			}
		})
	}
}

// Without the membership index, Recorders can't read a manager kind, and
// says so; Inventory reads the manager itself and still answers.
func TestRecordersWithoutTheMembershipIndexAreUnread(t *testing.T) {
	w := ownershiptest.Fixture()
	r := renderWith(t, w, nil)
	s := begin(t, r.source())
	web := resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "alpha", Name: "web"}
	got := s.Recorders(web)
	want := []ownership.Gap{
		{Kind: ownership.ArgoApplication, Reason: readreason.ReadError},
		{Kind: ownership.FluxHelmRelease, Reason: readreason.ReadError},
		{Kind: ownership.FluxKustomization, Reason: readreason.ReadError},
	}
	if len(got.Recorders) != 0 || len(got.Unreadable) != 0 || !slices.Equal(got.Gaps, want) {
		t.Fatalf("Recorders = %+v, want only gaps %+v", got, want)
	}
	app := resourceid.Ref{Group: ownership.ArgoApplication.Group, Kind: ownership.ArgoApplication.Kind, Namespace: "argocd", Name: "web"}
	if inv := s.Inventory(app); inv.Membership != ownership.MembersRecorded || !slices.Contains(inv.Members, web) {
		t.Fatalf("Inventory = %+v", inv)
	}
}

// Recorders is Inventory inverted: over a fully collected World, every member
// a manager lists names it, as a recorder or, for a remote manager, as
// elsewhere; and every manager so named lists the member. This holds of the
// source whatever the oracle says.
func TestRecordersInvertInventory(t *testing.T) {
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
				inv := s.Inventory(m)
				remote[m] = inv.Membership == ownership.MembersRemote
				for _, member := range inv.Members {
					listed[m] = append(listed[m], member)
					rec := s.Recorders(member)
					named := slices.ContainsFunc(rec.Recorders, func(o ownership.Observation) bool { return o.Ref == m })
					if remote[m] {
						named = slices.Contains(rec.Elsewhere, m)
					}
					if !named && len(rec.Gaps) == 0 {
						t.Errorf("%v (%v) lists %v, but Recorders(%v) = %+v", m, inv.Membership, member, member, rec)
					}
				}
			}
			for _, ref := range q.Refs {
				rec := s.Recorders(ref)
				var named []resourceid.Ref
				for _, o := range rec.Recorders {
					named = append(named, o.Ref)
				}
				for _, m := range named {
					if remote[m] || !slices.Contains(listed[m], ref) {
						t.Errorf("Recorders(%v) names %v as a recorder; its Inventory lists %v", ref, m, listed[m])
					}
				}
				for _, m := range rec.Elsewhere {
					if !remote[m] || !slices.Contains(listed[m], ref) {
						t.Errorf("Recorders(%v) names %v as elsewhere; its Inventory lists %v", ref, m, listed[m])
					}
				}
			}
		})
	}
}

// A built-in kind's scope is its own, unless discovery is ambiguous about the
// kind or serves it with the other scope: then no entry names it for certain,
// and its entries stay as recorded. The oracle says the same.
func TestRecordersOfABuiltInDiscoveryDisputes(t *testing.T) {
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
				Membership: ownership.MembersRecorded, Members: []resourceid.Ref{reader}, Destination: "alpha",
			}
			w.Objects = []ownershiptest.Object{app}
			scan, _ := ownershiptest.Scan(w).Begin()
			cache := begin(t, render(t, w).source())
			want := []ownership.Gap{{Kind: clusterRole, Reason: readreason.ReadError}}
			for source, s := range map[string]ownership.Session{"scan": scan, "cache": cache} {
				if got := s.Recorders(reader); len(got.Recorders) != 0 || !slices.Equal(got.Gaps, want) {
					t.Errorf("%s: Recorders(%v) = %+v, want only %+v", source, reader, got, want)
				}
				if got := s.Inventory(app.Ref); got.Membership != ownership.MembersRecorded || !slices.Equal(got.Members, []resourceid.Ref{reader}) {
					t.Errorf("%s: Inventory = %v %v, want the entry as recorded", source, got.Membership, got.Members)
				}
			}
		})
	}
}

// An answer is complete only when no remote manager may record the object
// and no manager's list is unreadable.
func TestRecordersAreCompleteOnlyWithoutDoubt(t *testing.T) {
	cfg := resourceid.Ref{Kind: "ConfigMap", Namespace: "alpha", Name: "cfg"}
	app := func(name string, state ownership.MembershipState, members ...resourceid.Ref) ownershiptest.Object {
		return ownershiptest.Object{
			Ref:        resourceid.Ref{Group: ownership.ArgoApplication.Group, Kind: ownership.ArgoApplication.Kind, Namespace: "argocd", Name: name},
			APIVersion: "argoproj.io/v1alpha1", UID: types.UID("uid-" + name), ResourceVersion: "1",
			Membership: state, Members: members, Destination: "alpha",
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
			got := begin(t, render(t, w).source()).Recorders(cfg)
			if got.Complete() != tc.complete || len(got.Recorders) != 1 {
				t.Fatalf("Recorders = %+v: complete %v, want %v", got, got.Complete(), tc.complete)
			}
		})
	}
}
