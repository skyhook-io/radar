package topology

import (
	"context"
	"fmt"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"slices"
	"strings"
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

// recordGitOpsMembers gives a GitOps manager's observation the member list it
// records, as the caches index it.
func recordGitOpsMembers(observation *ownershiptest.Object, obj runtime.Object) {
	ref := observation.Ref
	u, ok := obj.(*unstructured.Unstructured)
	if !ok || !ownership.IsRecordingManager(ref.GroupKind()) {
		return
	}
	var list gitops.MemberList
	var readable bool
	if ref.GroupKind() == ownership.ArgoApplication {
		list, readable = gitops.ArgoMembers(u)
	} else {
		list, readable = gitops.FluxMembers(u)
	}
	if !readable {
		return
	}
	observation.Membership = ownership.MembersRecorded
	if ref.GroupKind() == ownership.ArgoApplication && !gitops.IsInClusterDestination(u) || ref.GroupKind() != ownership.ArgoApplication && !gitops.FluxTargetsLocalCluster(u) {
		observation.Membership = ownership.MembersRemote
	}
	observation.DestinationNamespace = list.DestinationNamespace
	for _, e := range list.Entries {
		observation.Members = append(observation.Members, resourceid.NewRef(e.Group, e.Kind, e.Namespace, e.Name))
	}
}

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
		recordGitOpsMembers(&observation, obj)
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
		hops    int
	}{
		{"2000Pods", neighborhoodPodFixture, ResourceRef{Kind: "ReplicaSet", Group: "apps", Namespace: "shop", Name: "big-8f9"}, 1},
		{"2000Pods2Hops", neighborhoodPodFixture, ResourceRef{Kind: "ReplicaSet", Group: "apps", Namespace: "shop", Name: "big-8f9"}, 2},
		{"MixedCRDs", charCluster, ResourceRef{Kind: "Deployment", Group: "apps", Namespace: "shop", Name: "web"}, 1},
	} {
		b.Run(tc.name, func(b *testing.B) {
			f := tc.fixture()
			addNeighborhoodDiscoveryLoad(f)
			s := neighborhoodFixtureSession(b, f)
			opts := NeighborhoodOptions{Hops: tc.hops, Profile: ProfileAll}
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

// Every fixture subject's two-hop neighborhood is well formed: each node
// once, and every edge once between nodes it returns.
func TestWalkNeighborhoodWellFormed(t *testing.T) {
	f := charCluster()
	s := neighborhoodFixtureSession(t, f)
	for _, o := range f.objects {
		options := NeighborhoodOptions{Hops: 2, Profile: ProfileAll, Allow: func(*Node) bool { return true }}
		walk, err := WalkNeighborhood(context.Background(), s, o.ref(), options, f.provider, f.dynamic)
		if err != nil {
			t.Fatal(err)
		}
		if len(walk.Nodes) == 0 {
			t.Fatalf("missing fixture subject %+v", o.ref())
		}
		ids := map[string]bool{}
		for _, n := range walk.Nodes {
			if ids[n.ID] {
				t.Fatalf("duplicate node %s", n.ID)
			}
			ids[n.ID] = true
		}
		edges := map[string]bool{}
		for _, e := range walk.Edges {
			if edges[e.ID] || !ids[e.Source] || !ids[e.Target] {
				t.Fatalf("duplicate or dangling edge %+v", e)
			}
			edges[e.ID] = true
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
				if !withCRD || q.Kind != "Certificate" && q.Kind != "certificates" {
					t.Fatalf("discovery for absent kind: %+v (all: %+v)", q, d.queries)
				}
			}
			resolved := slices.DeleteFunc(slices.Clone(d.queries), func(q resourceid.GroupKind) bool { return q.Group == "" || q.Kind == "certificates" })
			if withCRD && len(resolved) != 1 {
				t.Fatalf("expected one admitted kind lookup, got %+v", d.queries)
			}
		})
	}
}

func TestWalkNeighborhoodPodGroupKeepsEachPodsSources(t *testing.T) {
	f := charCluster()
	f.provider.pods = nil
	f.objects = nil
	cm := &corev1.ConfigMap{ObjectMeta: charMeta("shop", "settings", nil)}
	f.provider.configMaps = []*corev1.ConfigMap{cm}
	f.objects = append(f.objects, charObject{kind: "ConfigMap", resource: "configmaps", obj: cm})
	for _, name := range []string{"a", "b"} {
		rs := charReplicaSet("shop", name, 6, charController("apps/v1", "Deployment", "app", "uid-app"), "shared")
		f.objects = append(f.objects, charObject{group: "apps", kind: "ReplicaSet", resource: "replicasets", obj: rs})
		for i := 0; i < 6; i++ {
			pod := charPod("shop", fmt.Sprintf("%s-%d", name, i), "", map[string]string{"app": "shared"}, charPodSpec("settings", ""), charController("apps/v1", "ReplicaSet", name, string(rs.UID)))
			f.objects = append(f.objects, charObject{kind: "Pod", resource: "pods", obj: pod})
		}
	}
	orphan := charPod("shop", "orphan", "", map[string]string{"app": "shared"}, charPodSpec("settings", ""), metav1.OwnerReference{})
	orphan.OwnerReferences = nil
	f.objects = append(f.objects, charObject{kind: "Pod", resource: "pods", obj: orphan})
	for _, denyReplicaSets := range []bool{false, true} {
		t.Run(fmt.Sprint(denyReplicaSets), func(t *testing.T) {
			sub, err := WalkNeighborhood(context.Background(), neighborhoodFixtureSession(t, f), ResourceRef{Kind: "ConfigMap", Namespace: "shop", Name: "settings"}, NeighborhoodOptions{Hops: 2, Profile: ProfileAll, Allow: func(n *Node) bool { return !denyReplicaSets || n.Kind != KindReplicaSet }}, f.provider, f.dynamic)
			if err != nil {
				t.Fatal(err)
			}
			checked := 0
			for _, n := range sub.Nodes {
				if n.Kind == KindReplicaSet && denyReplicaSets {
					t.Fatalf("withheld ReplicaSet admitted: %+v", n)
				}
				if n.Kind != KindPodGroup {
					continue
				}
				for _, pod := range n.Data["pods"].([]map[string]any) {
					name := pod["name"].(string)
					if denyReplicaSets {
						// Every Pod's one admitted source is the ConfigMap, so
						// the group is uniform and names no withheld owner.
						if ids, ok := pod["ownerIds"]; ok {
							t.Fatalf("pod %s names sources %v", name, ids)
						}
						checked++
						continue
					}
					want := []string{"configmap/shop/settings"}
					if strings.HasPrefix(name, "a-") {
						want = append(want, "replicaset/shop/a")
					}
					if strings.HasPrefix(name, "b-") {
						want = append(want, "replicaset/shop/b")
					}
					got, ok := pod["ownerIds"].([]string)
					if !ok || !slices.Equal(got, want) {
						t.Fatalf("pod %s sources %v, want %v", name, pod["ownerIds"], want)
					}
					checked++
				}
			}
			if checked != 13 {
				t.Fatalf("checked %d grouped Pods, want 13", checked)
			}
		})
	}
}

func TestNeighborhoodDisplayPreservesDiscoveryAliasWinner(t *testing.T) {
	d := newCharDynamic()
	d.add("example.io", "v1", "Other", "widgets", true)
	obj := charUnstructured("example.io/v1", "Widgets", "shop", "child", nil, nil, nil, charController("apps/v1", "Deployment", "web", "uid-shop-web"))
	d.add("example.io", "v1", "Widgets", "otherthings", true, obj)
	dep := charDeployment("shop", "web", 1, nil, charPodSpec("", ""))
	objects := map[resourceid.Ref]runtime.Object{
		resourceid.NewRef("example.io", "Widgets", "shop", "child"): obj,
		resourceid.NewRef("apps", "Deployment", "shop", "web"):      dep,
	}
	got, err := neighborhoodDisplayNodes(objects, nil, d)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := got[resourceid.NewRef("example.io", "Widgets", "shop", "child")]; ok {
		t.Fatal("rendered an object under the losing discovery alias")
	}
	if len(got) != 1 {
		t.Fatalf("display nodes: %+v", got)
	}
}

func TestWalkNeighborhoodEdgeIDsDistinguishRelationshipTypes(t *testing.T) {
	cm := projectionObject(&corev1.ConfigMap{ObjectMeta: charMeta("shop", "settings", nil)}, "", "ConfigMap")
	pod := projectionObject(charPod("shop", "app", "", nil, charPodSpec("settings", ""), charController("v1", "ConfigMap", "settings", "uid-shop-settings")), "", "Pod")
	sub := walkTestNeighborhood(t, newProjectionSession(t, cm, pod), cm, NeighborhoodOptions{Profile: ProfileAll})
	if len(sub.Nodes) != 2 || len(sub.Edges) != 2 || sub.Edges[0].ID == sub.Edges[1].ID {
		t.Fatalf("lost distinct relations on one pair: %+v", sub)
	}
}

type deploymentCountingProvider struct {
	*mockProvider
	deploymentLists int
}

func (p *deploymentCountingProvider) Deployments() ([]*appsv1.Deployment, error) {
	p.deploymentLists++
	return p.mockProvider.Deployments()
}

// Istio's service domain is read from every cached Deployment; a walk whose
// profile can't reach Istio's service hosts doesn't pay for it.
func TestWalkNeighborhoodReadsIstioDomainOnlyForIstioHosts(t *testing.T) {
	f := charCluster()
	for _, tc := range []struct {
		root  ResourceRef
		lists int
	}{
		{ResourceRef{Kind: "NetworkPolicy", Group: "networking.k8s.io", Namespace: "shop", Name: "default-deny"}, 0},
		{ResourceRef{Kind: "Service", Namespace: "shop", Name: "web"}, 1},
	} {
		p := &deploymentCountingProvider{mockProvider: f.provider}
		if _, err := WalkNeighborhood(context.Background(), neighborhoodFixtureSession(t, f), tc.root, NeighborhoodOptions{}, p, f.dynamic); err != nil {
			t.Fatal(err)
		}
		if p.deploymentLists != tc.lists {
			t.Errorf("%s: listed Deployments %d times, want %d", tc.root.Kind, p.deploymentLists, tc.lists)
		}
	}
}
