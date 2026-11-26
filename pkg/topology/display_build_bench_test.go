package topology

import (
	"fmt"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// BenchmarkDisplayTopologyBuild measures the builds the SSE broadcaster runs
// for each client group (the default options in each view mode) and the
// relationship-cache options the GitOps tree uses, at census fractions the
// large-cluster guard still draws.
func BenchmarkDisplayTopologyBuild(b *testing.B) {
	traffic := func() BuildOptions { o := DefaultBuildOptions(); o.ViewMode = ViewModeTraffic; return o }
	cases := []struct {
		name string
		opts func() BuildOptions
		dens []int
	}{
		{"resources", DefaultBuildOptions, []int{256}},
		{"traffic", traffic, []int{256}},
		{"relcache", relationshipCacheOptions, []int{256, 64, 16}},
	}
	for _, c := range cases {
		for _, den := range c.dens {
			shape := fieldShape().fraction(1, den)
			b.Run(fmt.Sprintf("%s/1-%d", c.name, den), func(b *testing.B) {
				benchmarkBuild(b, NewBuilder(newSyntheticProvider(shape)), c.opts())
			})
		}
	}
	b.Run("crds/64-namespaces", func(b *testing.B) {
		const namespaces = 64
		f := charCluster()
		addDisplayFamilyReplicas(f, namespaces)
		builder := NewBuilder(f.provider).WithDynamic(f.dynamic)
		opts := DefaultBuildOptions()
		topo, err := builder.Build(opts)
		if err != nil {
			b.Fatal(err)
		}
		drawn := map[displayProjection]bool{}
		for _, e := range topo.Edges {
			drawn[displayProjection{e.Source, e.Target, e.Type}] = true
		}
		for i := range namespaces {
			ns := fmt.Sprintf("replica-%d", i)
			for _, edge := range []displayProjection{
				{"gatewayclass//envoy", "gateway/" + ns + "/public", EdgeManages},
				{"gateway/" + ns + "/public", "httproute/" + ns + "/web", EdgeRoutesTo},
				{"httproute/" + ns + "/web", "service/" + ns + "/web", EdgeRoutesTo},
				{"clusterclass/" + ns + "/standard", "capicluster/" + ns + "/prod", EdgeConfigures},
				{"workflowtemplate/" + ns + "/main", "workflow/" + ns + "/report-1700", EdgeConfigures},
			} {
				if !drawn[edge] {
					b.Fatalf("missing %s edge %s -> %s", edge.kind, edge.source, edge.target)
				}
			}
		}
		benchmarkBuild(b, builder, opts)
	})
}

// BenchmarkScopedDisplayTopologyBuild measures one namespace of the full field
// census in each view mode: how a large cluster's Topology screen is viewed.
func BenchmarkScopedDisplayTopologyBuild(b *testing.B) {
	shape := fieldShape()
	provider := newSyntheticProvider(shape)
	for _, view := range []ViewMode{ViewModeResources, ViewModeTraffic} {
		b.Run(string(view), func(b *testing.B) {
			opts := DefaultBuildOptions()
			opts.ViewMode = view
			opts.Namespaces = []string{synthNames{shape: shape}.namespace(0)}
			benchmarkBuild(b, NewBuilder(provider), opts)
		})
	}
}

func benchmarkBuild(b *testing.B, builder *Builder, opts BuildOptions) {
	topo, err := builder.Build(opts)
	if err != nil {
		b.Fatalf("build: %v", err)
	}
	if len(topo.Nodes) == 0 {
		b.Fatal("the build drew nothing, so it measures the large-cluster guard, not the graph")
	}
	b.ReportAllocs()
	for b.Loop() {
		if _, err := builder.Build(opts); err != nil {
			b.Fatalf("build: %v", err)
		}
	}
	b.ReportMetric(float64(len(topo.Nodes)), "graphnodes")
	b.ReportMetric(float64(len(topo.Edges)), "graphedges")
}

// The golden fixture has a Gateway chain, but its Cluster and Workflow name
// no templates. Give the replicas those declarations without changing the originals.
func addDisplayFamilyReplicas(f *charFixture, count int) {
	originals := f.objects
	add := func(resource string, obj *unstructured.Unstructured) {
		gvk := obj.GroupVersionKind()
		f.dynamic.add(gvk.Group, gvk.Version, gvk.Kind, resource, true, obj)
		f.objects = append(f.objects, charObject{gvk.Group, gvk.Kind, resource, obj})
	}
	for i := range count {
		ns := fmt.Sprintf("replica-%d", i)
		for _, o := range originals {
			if svc, ok := o.obj.(*corev1.Service); ok && svc.Name == "web" && svc.Namespace == "shop" {
				copy := svc.DeepCopy()
				copy.ObjectMeta = charMeta(ns, svc.Name, svc.Labels)
				f.provider.services = append(f.provider.services, copy)
				f.objects = append(f.objects, charObject{o.group, o.kind, o.resource, copy})
				continue
			}
			switch o.group + "/" + o.kind {
			case "gateway.networking.k8s.io/Gateway", "gateway.networking.k8s.io/HTTPRoute", "cluster.x-k8s.io/Cluster", "argoproj.io/Workflow":
				obj := o.obj.(*unstructured.Unstructured).DeepCopy()
				obj.SetNamespace(ns)
				obj.SetUID(charMeta(ns, obj.GetName(), nil).UID)
				switch o.kind {
				case "Cluster":
					obj.Object["spec"] = map[string]any{"topology": map[string]any{"class": "standard"}}
				case "Workflow":
					obj.SetOwnerReferences(nil)
					obj.SetLabels(nil)
					obj.Object["spec"] = map[string]any{"workflowTemplateRef": map[string]any{"name": "main"}}
				}
				add(o.resource, obj)
			}
		}
		add("clusterclasses", charUnstructured("cluster.x-k8s.io/v1beta1", "ClusterClass", ns, "standard", nil, nil, nil))
		add("workflowtemplates", charUnstructured("argoproj.io/v1alpha1", "WorkflowTemplate", ns, "main", nil, nil, nil))
	}
}
