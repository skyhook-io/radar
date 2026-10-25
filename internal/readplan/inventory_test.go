package readplan

import (
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/pkg/readreason"
)

func TestForInventory(t *testing.T) {
	ready := Partition{Extent: NamespaceSet, Namespaces: []string{"alpha"}, Verdict: Verdict{State: Readable}}
	pending := Partition{Extent: NamespaceSet, Namespaces: []string{"beta"}, Verdict: Verdict{State: Gap, Code: readreason.KindSyncPending}}
	outside := Partition{Extent: NamespaceSet, Namespaces: []string{"gamma"}, Verdict: Verdict{State: OutsideCollection, Code: readreason.RadarNamespaceNotCollected}}
	unknown := Partition{Extent: UnknownNamespaces, Verdict: Verdict{State: Gap, Code: readreason.ReadError}}
	for _, tc := range []struct {
		name              string
		plan              Plan
		target            []string
		explicit, cluster bool
		want              Inventory
	}{
		{name: "whole namespace scope", plan: Plan{WholeKindReady: true, Partitions: []Partition{{Extent: AllNamespaces, Verdict: Verdict{State: Readable}}}},
			want: Inventory{Namespaces: []string{}, All: true, CanReadTarget: true, WholeKindReady: true}},
		{name: "cluster scope", cluster: true, plan: Plan{Partitions: []Partition{{Extent: Cluster, Verdict: Verdict{State: Readable}}}},
			want: Inventory{Namespaces: []string{}, All: true, CanReadTarget: true}},
		{name: "finite partial scope", target: []string{"alpha", "beta"}, explicit: true, plan: Plan{Partitions: []Partition{ready, pending}},
			want: Inventory{Namespaces: []string{"alpha"}, CanReadTarget: true, Gaps: []Partition{pending}}},
		{name: "explicit outside stays a gap", target: []string{"alpha", "beta", "gamma"}, explicit: true, plan: Plan{Partitions: []Partition{ready, pending, outside}},
			want: Inventory{Namespaces: []string{"alpha"}, CanReadTarget: true, Gaps: []Partition{pending, outside}}},
		{name: "implicit finite collection retains loading", target: []string{"alpha", "beta", "gamma"}, plan: Plan{Partitions: []Partition{ready, pending, outside}},
			want: Inventory{Namespaces: []string{"alpha"}, CanReadTarget: true, Gaps: []Partition{pending}, Collection: &CollectionScope{Namespaces: []string{"alpha", "beta"}, Outside: []string{"gamma"}}}},
		{name: "all target collection retains loading", plan: Plan{CollectionLimited: true, Partitions: []Partition{ready, pending}},
			want: Inventory{Namespaces: []string{"alpha"}, CanReadTarget: true, Gaps: []Partition{pending}, Collection: &CollectionScope{Namespaces: []string{"alpha", "beta"}}}},
		{name: "loading collection is intended even without a read", plan: Plan{CollectionLimited: true, Partitions: []Partition{pending}},
			want: Inventory{Namespaces: []string{}, Gaps: []Partition{pending}, Collection: &CollectionScope{Namespaces: []string{"beta"}}}},
		{name: "unknown extent retained", plan: Plan{Partitions: []Partition{ready, unknown}},
			want: Inventory{Namespaces: []string{"alpha"}, CanReadTarget: true, Gaps: []Partition{unknown}}},
		{name: "unknown empty collection", plan: Plan{CollectionLimited: true, Partitions: []Partition{unknown}},
			want: Inventory{Namespaces: []string{}, Gaps: []Partition{unknown}, Collection: &CollectionScope{Namespaces: []string{}}}},
		{name: "empty finite target", target: []string{},
			want: Inventory{Namespaces: []string{}, CanReadTarget: true}},
		{name: "empty plan does not imply all namespaces", want: Inventory{Namespaces: []string{}}},
		{name: "cluster gap extent retained", cluster: true, plan: Plan{Partitions: []Partition{{Extent: Cluster, Verdict: Verdict{State: Gap, Code: readreason.KindSyncPending}}}},
			want: Inventory{Namespaces: []string{}, Gaps: []Partition{{Extent: Cluster, Verdict: Verdict{State: Gap, Code: readreason.KindSyncPending}}}}},
		{name: "all namespace gap extent retained", plan: Plan{Partitions: []Partition{{Extent: AllNamespaces, Verdict: Verdict{State: Gap, Code: readreason.RadarForbidden}}}},
			want: Inventory{Namespaces: []string{}, Gaps: []Partition{{Extent: AllNamespaces, Verdict: Verdict{State: Gap, Code: readreason.RadarForbidden}}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := ForInventory(tc.plan, tc.target, tc.explicit, tc.cluster)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("inventory = %+v, want %+v", got, tc.want)
			}
		})
	}
}

func TestCollectionNarrowKeepsIntendedRatherThanReadableScope(t *testing.T) {
	p := Plan{CollectionLimited: true, Partitions: []Partition{
		{Extent: NamespaceSet, Namespaces: []string{"alpha"}, Verdict: Verdict{State: Readable}},
		{Extent: NamespaceSet, Namespaces: []string{"beta"}, Verdict: Verdict{State: Gap, Code: readreason.KindSyncPending}},
		{Extent: NamespaceSet, Namespaces: []string{"gamma"}, Verdict: Verdict{State: OutsideCollection, Code: readreason.RadarNamespaceNotCollected}},
	}}
	inventory := ForInventory(p, nil, false, false)
	for _, tc := range []struct {
		name         string
		target, want []string
	}{
		{"all scope", nil, []string{"alpha", "beta"}},
		{"finite keeps access-withheld namespace", []string{"alpha", "beta", "gamma", "denied"}, []string{"alpha", "beta", "denied"}},
		{"empty stays finite", []string{}, []string{}},
		{"outside only stays finite", []string{"gamma"}, []string{}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			before := append([]string(nil), tc.target...)
			got := inventory.Collection.Narrow(tc.target)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("narrow = %#v, want %#v", got, tc.want)
			}
			if len(tc.target) > 0 {
				if len(got) > 0 {
					got[0] = "changed"
				}
				if !reflect.DeepEqual(before, tc.target) {
					t.Fatal("target mutated")
				}
			}
		})
	}
	all := inventory.Collection.Narrow(nil)
	all[0] = "changed"
	if inventory.Collection.Namespaces[0] != "alpha" {
		t.Fatal("narrowed result aliases collection")
	}
	empty := ForInventory(Plan{CollectionLimited: true}, nil, false, false)
	if got := empty.Collection.Narrow(nil); got == nil || len(got) != 0 {
		t.Fatalf("empty collection became all namespaces: %#v", got)
	}
}

func TestInventoryOwnsProjectedNamespaces(t *testing.T) {
	p := Plan{Partitions: []Partition{
		{Extent: NamespaceSet, Namespaces: []string{"alpha"}, Verdict: Verdict{State: Readable}},
		{Extent: NamespaceSet, Namespaces: []string{"beta"}, Verdict: Verdict{State: Gap, Code: readreason.KindSyncPending}},
		{Extent: NamespaceSet, Namespaces: []string{"gamma"}, Verdict: Verdict{State: OutsideCollection, Code: readreason.RadarNamespaceNotCollected}},
	}}
	got := ForInventory(p, nil, false, false)
	got.Namespaces[0], got.Gaps[0].Namespaces[0] = "changed", "changed"
	got.Collection.Namespaces[0], got.Collection.Outside[0] = "changed", "changed"
	for i, want := range []string{"alpha", "beta", "gamma"} {
		if p.Partitions[i].Namespaces[0] != want {
			t.Fatal("projection mutated planner partitions")
		}
	}
}
