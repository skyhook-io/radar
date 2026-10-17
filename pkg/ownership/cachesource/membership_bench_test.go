package cachesource

import (
	"fmt"
	"runtime"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// application is an Argo CD Application listing entries objects, half of
// them recorded without their namespace.
func application(i, entries int) *unstructured.Unstructured {
	o := ownershiptest.Object{
		Ref:        resourceid.Ref{Group: ownership.ArgoApplication.Group, Kind: ownership.ArgoApplication.Kind, Namespace: "argocd", Name: fmt.Sprint("app-", i)},
		APIVersion: "argoproj.io/v1alpha1", UID: "uid", ResourceVersion: "1",
		Membership: ownership.MembersRecorded, Destination: fmt.Sprint("team-", i%50),
	}
	for j := range entries {
		o.Members = append(o.Members, resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: o.Destination, Name: fmt.Sprintf("app-%d-service-%d", i, j)})
	}
	u := &unstructured.Unstructured{}
	u.SetAPIVersion(o.APIVersion)
	u.SetKind(o.Ref.Kind)
	u.SetNamespace(o.Ref.Namespace)
	u.SetName(o.Ref.Name)
	renderApplication(u, o)
	return u
}

// BenchmarkMembershipIndexRetainedBytes reports the heap the members index
// keeps per listed entry: 500 Applications of 40 entries each, held with and
// without it. Run with -bench MembershipIndexRetained -run '^$' -benchtime 3x.
func BenchmarkMembershipIndexRetainedBytes(b *testing.B) {
	const apps, entries = 500, 40
	objects := make([]*unstructured.Unstructured, apps)
	for i := range objects {
		objects[i] = application(i, entries)
	}
	retained := func(indexers cache.Indexers) uint64 {
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexers)
		for _, u := range objects {
			_ = store.Add(u)
		}
		runtime.GC()
		runtime.ReadMemStats(&after)
		runtime.KeepAlive(store)
		return after.HeapAlloc - before.HeapAlloc
	}
	namespaceOnly := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}
	withMembers := cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc, membersIndex: managerKeys(ownership.ArgoApplication)}
	var extra float64
	for range b.N {
		extra += float64(retained(withMembers)) - float64(retained(namespaceOnly))
	}
	b.ReportMetric(extra/float64(b.N)/(apps*entries), "B/entry")
}

// BenchmarkMembershipIndexUpdate reports what the index adds to one store
// update of an Application, as Argo CD writes after each reconcile.
func BenchmarkMembershipIndexUpdate(b *testing.B) {
	for _, entries := range []int{40, 400} {
		for name, indexers := range map[string]cache.Indexers{
			"without": {cache.NamespaceIndex: cache.MetaNamespaceIndexFunc},
			"with":    {cache.NamespaceIndex: cache.MetaNamespaceIndexFunc, membersIndex: managerKeys(ownership.ArgoApplication)},
		} {
			b.Run(fmt.Sprintf("%d-entries-%s", entries, name), func(b *testing.B) {
				store := cache.NewIndexer(cache.MetaNamespaceKeyFunc, indexers)
				app := application(0, entries)
				_ = store.Add(app)
				b.ReportAllocs()
				for range b.N {
					_ = store.Update(app)
				}
			})
		}
	}
}

// BenchmarkRecorders is one search for an object's recorders among 300
// Applications of 40 entries each, a few of them unreadable.
func BenchmarkRecorders(b *testing.B) {
	t := &testing.T{}
	w := ownershiptest.World{Resources: ownershiptest.Resources(), Collection: map[resourceid.GroupKind]ownershiptest.Collection{}}
	for _, k := range ownershiptest.Kinds {
		w.Collection[k.GroupKind] = ownershiptest.Collection{OnDemand: !k.Typed}
	}
	for i := range 300 {
		o := ownershiptest.Object{
			Ref:        resourceid.Ref{Group: ownership.ArgoApplication.Group, Kind: ownership.ArgoApplication.Kind, Namespace: "argocd", Name: fmt.Sprint("app-", i)},
			APIVersion: "argoproj.io/v1alpha1", UID: types.UID(fmt.Sprint("uid-app-", i)), ResourceVersion: "1",
			Membership: ownership.MembersRecorded, Destination: fmt.Sprint("team-", i%50),
		}
		if i%100 == 0 {
			o.Membership = ownership.MembersUnreadable
		}
		for j := range 40 {
			o.Members = append(o.Members, resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: o.Destination, Name: fmt.Sprintf("app-%d-service-%d", i, j)})
		}
		w.Objects = append(w.Objects, o)
	}
	s := begin(t, render(t, w).source())
	target := resourceid.Ref{Group: "apps", Kind: "Deployment", Namespace: "team-7", Name: "app-7-service-3"}
	if got := s.Recorders(target); len(got.Recorders) != 1 || len(got.Unreadable) != 3 || len(got.Gaps) != 0 {
		b.Fatalf("Recorders = %+v", got)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		s.Recorders(target)
	}
}
