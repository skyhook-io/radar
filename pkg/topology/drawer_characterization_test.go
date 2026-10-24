package topology

import (
	"reflect"
	"sort"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"

	k8score "github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/cachesource"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// charSource serves the fixture to the ownership source through Radar's own
// caches: its typed objects from a fake clientset, with every kind the typed
// cache holds collected, and its custom resources from a fake dynamic client,
// each kind watched at its fixture version. Discovery
// serves the fixture's resources as Kubernetes does, with delete, so the
// garbage collector's kinds are the ones searched for dependents.
func charSource(t *testing.T, f *charFixture) ownership.Source {
	t.Helper()
	var typed []runtime.Object
	for _, o := range f.objects {
		if _, custom := o.obj.(*unstructured.Unstructured); !custom {
			typed = append(typed, o.obj.(runtime.Object).DeepCopyObject())
		}
	}
	// Every kind Radar's typed cache holds, as production collects them.
	enabled := map[string]bool{}
	for _, kl := range k8score.AllKindListers() {
		if b, ok := resourceid.BuiltinForKind(kl.Kind()); ok && b.Group == kl.Group() && b.Kind != "Event" {
			enabled[b.Resource] = true
		}
	}
	typedCache, err := k8score.NewResourceCache(k8score.CacheConfig{Client: fake.NewSimpleClientset(typed...), ResourceTypes: enabled})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(typedCache.Stop)

	disc, err := k8score.NewResourceDiscovery(fake.NewSimpleClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range f.dynamic.disc.Catalog().Resources {
		r.Verbs = []string{"delete", "get", "list", "watch"}
		disc.AddAPIResource(r)
	}
	listKinds := map[schema.GroupVersionResource]string{}
	for _, gvr := range f.dynamic.watched {
		listKinds[gvr] = f.dynamic.disc.GetKindForGVR(gvr) + "List"
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	// Filed under their own resource: the fake client would otherwise guess
	// it from the kind, and guess Gateway's as gatewaies.
	for _, gvr := range f.dynamic.watched {
		for _, u := range f.dynamic.resources[gvr] {
			if err := client.Tracker().Create(gvr, u.DeepCopy(), u.GetNamespace()); err != nil {
				t.Fatal(err)
			}
		}
	}
	dynamicCache, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{
		DynamicClient: client,
		Discovery:     disc,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dynamicCache.Stop)
	for _, gvr := range f.dynamic.watched {
		if err := dynamicCache.EnsureWatching(gvr); err != nil {
			t.Fatalf("watch %v: %v", gvr, err)
		}
		if !dynamicCache.WaitForSync(gvr, 5*time.Second) {
			t.Fatalf("%v did not sync", gvr)
		}
	}
	deadline := time.Now().Add(5 * time.Second)
	for !typedCache.IsSyncComplete() {
		if time.Now().After(deadline) {
			t.Fatal("the typed cache did not sync")
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cachesource.New(func() cachesource.Caches {
		return cachesource.Caches{Typed: typedCache, Dynamic: dynamicCache, Discovery: disc}
	})
}

// charDrawer records what the drawer and MCP get_resource show for every
// fixture object: Related Resources from the relationship cache, with
// ownership answered from owner references for a caller who may read
// everything.
func charDrawer(t *testing.T, f *charFixture) []byte {
	t.Helper()
	topo := charBuild(t, f, charSSEOptions())
	idx := IndexByResource(topo)
	s, err := charSource(t, f).Begin()
	if err != nil {
		t.Fatal(err)
	}
	v := ownership.NewView(s, ownership.Unrestricted())
	type record struct {
		Root          string         `json:"root"`
		Relationships *Relationships `json:"relationships"`
	}
	records := make([]record, 0, len(f.objects))
	for _, o := range f.objects {
		ref := o.ref()
		rel := GetRelationshipsWithObject(o.resource, ref.Namespace, ref.Name, o.obj, topo, f.provider, f.dynamic, idx)
		if rel == nil {
			// REST answers ownership for an object the graph links to nothing.
			rel = &Relationships{}
		}
		ApplyOwnership(rel, v, ownership.Observe(resourceid.Ref{Group: o.group, Kind: o.kind, Namespace: ref.Namespace, Name: ref.Name}, o.obj))
		canonicalRefLists(reflect.ValueOf(rel))
		records = append(records, record{Root: o.key(), Relationships: rel})
	}
	if err := s.ValidateCaches(); err != nil {
		t.Fatalf("the caches changed while the drawer answers were read: %v", err)
	}
	sort.SliceStable(records, func(i, j int) bool { return records[i].Root < records[j].Root })
	return charJSON(t, records)
}

func TestDrawerCharacterization(t *testing.T) {
	charCompareGolden(t, "drawer-relationships", charDrawer(t, charCluster()))
}

// Ownership answers don't depend on how the graph was built: every build
// option gives the same Owner, Owners, Children and Deployment.
func TestDrawerOwnershipIgnoresBuildOptions(t *testing.T) {
	f := charCluster()
	s, err := charSource(t, f).Begin()
	if err != nil {
		t.Fatal(err)
	}
	v := ownership.NewView(s, ownership.Unrestricted())
	answers := func(opts BuildOptions) map[string][]any {
		topo := charBuild(t, f, opts)
		idx := IndexByResource(topo)
		out := map[string][]any{}
		for _, o := range f.objects {
			ref := o.ref()
			rel := GetRelationshipsWithObject(o.resource, ref.Namespace, ref.Name, o.obj, topo, f.provider, f.dynamic, idx)
			if rel == nil {
				rel = &Relationships{}
			}
			ApplyOwnership(rel, v, ownership.Observe(resourceid.Ref{Group: o.group, Kind: o.kind, Namespace: ref.Namespace, Name: ref.Name}, o.obj))
			canonicalRefLists(reflect.ValueOf(rel))
			out[o.key()] = []any{rel.Owner, rel.Owners, rel.Children, rel.Deployment, rel.Ownership}
		}
		return out
	}
	want := answers(charSSEOptions())
	for _, tc := range charCases() {
		if got := answers(tc.opts()); !reflect.DeepEqual(got, want) {
			for key := range want {
				if !reflect.DeepEqual(got[key], want[key]) {
					t.Errorf("%s: %s answers %v, relationship cache %v", tc.name, key, got[key], want[key])
				}
			}
		}
	}
}
