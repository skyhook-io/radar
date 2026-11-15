package topology

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/ownership/ownershiptest"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func neighborhoodFixtureSession(tb testing.TB, f *charFixture) *projectionSession {
	tb.Helper()
	w := ownershiptest.World{}
	resources, err := f.dynamic.disc.GetAPIResources()
	if err != nil {
		tb.Fatal(err)
	}
	for _, r := range resources {
		w.Resources = append(w.Resources, ownership.ServedResource{Group: r.Group, Version: r.Version, Kind: r.Kind, Resource: r.Name, Namespaced: r.Namespaced, Verbs: append(append([]string(nil), r.Verbs...), "delete")})
	}
	s := &projectionSession{objects: map[resourceid.Ref]runtime.Object{}}
	for _, o := range f.objects {
		obj := o.obj.(runtime.Object)
		if obj.GetObjectKind().GroupVersionKind().Kind == "" {
			builtin, _ := resourceid.BuiltinForName(o.kind)
			obj.GetObjectKind().SetGroupVersionKind(schema.GroupVersionKind{Group: o.group, Kind: o.kind, Version: builtin.Version})
		}
		ref := resourceid.NewRef(o.group, o.kind, o.obj.GetNamespace(), o.obj.GetName())
		s.objects[ref] = obj
		observation := ownershiptest.Object{Ref: ref, UID: o.obj.GetUID(), APIVersion: obj.GetObjectKind().GroupVersionKind().GroupVersion().String(), OwnerReferences: o.obj.GetOwnerReferences(), Labels: o.obj.GetLabels(), Annotations: o.obj.GetAnnotations()}
		if u, ok := obj.(*unstructured.Unstructured); ok && ownership.IsRecordingManager(ref.GroupKind()) {
			var list gitops.MemberList
			var readable bool
			if ref.GroupKind() == ownership.ArgoApplication {
				list, readable = gitops.ArgoMembers(u)
			} else {
				list, readable = gitops.FluxMembers(u)
			}
			if readable {
				observation.Membership = ownership.MembersRecorded
				if ref.GroupKind() == ownership.ArgoApplication && !gitops.IsInClusterDestination(u) || ref.GroupKind() != ownership.ArgoApplication && !gitops.FluxTargetsLocalCluster(u) {
					observation.Membership = ownership.MembersRemote
				}
				observation.DestinationNamespace = list.DestinationNamespace
				for _, e := range list.Entries {
					observation.Members = append(observation.Members, resourceid.NewRef(e.Group, e.Kind, e.Namespace, e.Name))
				}
			}
		}
		w.Objects = append(w.Objects, observation)
	}
	s.ReadSession, err = ownershiptest.Scan(w).Begin()
	if err != nil {
		tb.Fatal(err)
	}
	return s
}

func neighborhoodPodFixture() *charFixture {
	f := charCluster()
	// Keep the same mixed inventory while making one owner fan out to 2,000 Pods.
	f.provider.pods = nil
	for i := 0; i < 2000; i++ {
		pod := charPod("shop", fmt.Sprintf("big-8f9-%04d", i), "node-a", map[string]string{"app": "big"}, charPodSpec("", ""), charController("apps/v1", "ReplicaSet", "big-8f9", "uid-shop-big-8f9"))
		f.provider.pods = append(f.provider.pods, pod)
	}
	objects := f.objects[:0]
	for _, o := range f.objects {
		if o.kind != "Pod" || o.group != "" {
			objects = append(objects, o)
		}
	}
	f.objects = objects
	for _, pod := range f.provider.pods {
		f.objects = append(f.objects, charObject{kind: "Pod", resource: "pods", obj: pod})
	}
	return f
}

func addNeighborhoodDiscoveryLoad(f *charFixture) {
	for i := 0; i < 600; i++ {
		f.dynamic.disc.AddAPIResource(k8score.APIResource{Group: fmt.Sprintf("operator-%03d.example.io", i), Version: "v1", Kind: fmt.Sprintf("Other%d", i), Name: fmt.Sprintf("others%d", i), Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}})
	}
}

func BenchmarkWalkNeighborhood(b *testing.B) {
	for _, tc := range []struct {
		name    string
		fixture func() *charFixture
		root    ResourceRef
	}{
		{"2000Pods", neighborhoodPodFixture, ResourceRef{Kind: "ReplicaSet", Group: "apps", Namespace: "shop", Name: "big-8f9"}},
		{"MixedCRDs", charCluster, ResourceRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"}},
	} {
		b.Run(tc.name, func(b *testing.B) {
			f := tc.fixture()
			addNeighborhoodDiscoveryLoad(f)
			s := neighborhoodFixtureSession(b, f)
			opts := NeighborhoodOptions{Hops: 2, Profile: ProfileAll}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.reads = nil
				sub, err := WalkNeighborhood(context.Background(), s, tc.root, opts, f.provider, f.dynamic)
				if err != nil || len(sub.Nodes) == 0 {
					b.Fatalf("walk: %v %+v", err, sub)
				}
			}
		})
	}
}

func TestWalkNeighborhoodMixedDisplayGolden(t *testing.T) {
	f := charCluster()
	s := neighborhoodFixtureSession(t, f)
	var nodes []Node
	for _, o := range f.objects {
		sub, err := WalkNeighborhood(context.Background(), s, o.ref(), NeighborhoodOptions{MaxNodes: 1}, f.provider, f.dynamic)
		if err != nil || len(sub.Nodes) != 1 {
			t.Fatalf("%+v: %v %+v", o.ref(), err, sub)
		}
		nodes = append(nodes, sub.Nodes[0])
	}
	sub, err := WalkNeighborhood(context.Background(), s, ResourceRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"}, NeighborhoodOptions{Hops: 2, MaxNodes: 200, Profile: ProfileAll}, f.provider, f.dynamic)
	if err != nil {
		t.Fatal(err)
	}
	charCompareGolden(t, "neighborhood-mixed-display", charJSON(t, struct {
		Isolated     []Node
		Neighborhood *Subgraph
	}{nodes, sub}))
}

func TestNeighborhoodResponseComparison(t *testing.T) {
	f := charCluster()
	s := neighborhoodFixtureSession(t, f)
	opts := RelationshipBuildOptions()
	opts.IncludeSecrets = true
	graph := charBuild(t, f, opts)
	type comparison struct {
		Subject   ResourceRef `json:"subject"`
		Old, Walk *Subgraph
	}
	var records []comparison
	for _, o := range f.objects {
		options := NeighborhoodOptions{Hops: 2, Profile: ProfileAll, Allow: func(*Node) bool { return true }}
		old := BuildNeighborhood(graph, o.ref(), options)
		walk, err := WalkNeighborhood(context.Background(), s, o.ref(), options, f.provider, f.dynamic)
		if err != nil {
			t.Fatal(err)
		}
		a, _ := json.Marshal(old)
		z, _ := json.Marshal(walk)
		t.Logf("%s: old %d nodes/%d edges/%d bytes; walk %d nodes/%d edges/%d bytes", o.key(), len(old.Nodes), len(old.Edges), len(a), len(walk.Nodes), len(walk.Edges), len(z))
		records = append(records, comparison{o.ref(), old, walk})
	}
	if dir := os.Getenv("NEIGHBORHOOD_COMPARISON_DIR"); dir != "" {
		if err := os.WriteFile(filepath.Join(dir, "responses.json"), charJSON(t, records), 0644); err != nil {
			t.Fatal(err)
		}
	}
}

type neighborhoodDiscoveryCounter struct {
	DynamicProvider
	queries []resourceid.GroupKind
}

func (d *neighborhoodDiscoveryCounter) GetGVRWithGroup(name, group string) (schema.GroupVersionResource, bool) {
	d.queries = append(d.queries, resourceid.GroupKind{Group: group, Kind: name})
	return d.DynamicProvider.GetGVRWithGroup(name, group)
}
func (d *neighborhoodDiscoveryCounter) GetGVR(name string) (schema.GroupVersionResource, bool) {
	d.queries = append(d.queries, resourceid.GroupKind{Kind: name})
	return d.DynamicProvider.GetGVR(name)
}
func (d *neighborhoodDiscoveryCounter) GetKindForGVR(gvr schema.GroupVersionResource) string {
	d.queries = append(d.queries, resourceid.GroupKind{Group: gvr.Group, Kind: gvr.Resource})
	return d.DynamicProvider.GetKindForGVR(gvr)
}
func (d *neighborhoodDiscoveryCounter) IsCRD(name string) bool {
	d.queries = append(d.queries, resourceid.GroupKind{Kind: name})
	return d.DynamicProvider.IsCRD(name)
}

func TestNeighborhoodDisplayDiscoveryOnlyAdmittedKinds(t *testing.T) {
	f := charCluster()
	s := neighborhoodFixtureSession(t, f)
	for _, withCRD := range []bool{false, true} {
		t.Run(fmt.Sprint(withCRD), func(t *testing.T) {
			objects := map[resourceid.Ref]runtime.Object{}
			for ref, obj := range s.objects {
				if ref.Kind == "Deployment" && ref.Name == "web" || withCRD && ref.Kind == "Certificate" {
					objects[ref] = obj
				}
			}
			d := &neighborhoodDiscoveryCounter{DynamicProvider: f.dynamic}
			nodes, err := neighborhoodDisplayNodes(objects, f.provider, d)
			if err != nil || len(nodes) != len(objects) {
				t.Fatalf("display: %v %+v", err, nodes)
			}
			for _, q := range d.queries {
				if !withCRD || q.Kind != "Certificate" {
					t.Fatalf("discovery for absent kind: %+v (all: %+v)", q, d.queries)
				}
			}
			resolved := slices.DeleteFunc(slices.Clone(d.queries), func(q resourceid.GroupKind) bool { return q.Group == "" })
			if withCRD && len(resolved) != 1 {
				t.Fatalf("expected one admitted kind lookup, got %+v", d.queries)
			}
		})
	}
}
