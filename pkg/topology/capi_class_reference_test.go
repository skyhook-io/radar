package topology

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

func TestCAPIClusterClassReferenceNamespace(t *testing.T) {
	for _, version := range []string{"v1alpha4", "v1beta1", "v1beta2"} {
		for _, namespace := range []string{"", "classes", "missing"} {
			t.Run(version+"/"+namespace, func(t *testing.T) {
				clGVR := schema.GroupVersionResource{Group: "cluster.x-k8s.io", Version: version, Resource: "clusters"}
				ccGVR := clGVR
				ccGVR.Resource = "clusterclasses"
				cl := genericIdentityObject(clGVR, "Cluster", "app", "cluster")
				top := map[string]any{}
				if version != "v1beta2" {
					top["class"] = "shared"
					top["classNamespace"] = namespace
				} else {
					top["classRef"] = map[string]any{"name": "shared", "namespace": namespace}
				}
				cl.Object["spec"] = map[string]any{"topology": top}
				d := &genericIdentityDynamic{watched: []schema.GroupVersionResource{clGVR, ccGVR}, kinds: map[schema.GroupVersionResource]string{clGVR: "Cluster", ccGVR: "ClusterClass"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{clGVR: {cl}, ccGVR: {genericIdentityObject(ccGVR, "ClusterClass", "app", "shared"), genericIdentityObject(ccGVR, "ClusterClass", "classes", "shared")}}, listCalls: map[schema.GroupVersionResource]int{}}
				topo, err := NewBuilder(&mockProvider{}).WithDynamic(d).Build(DefaultBuildOptions())
				if err != nil {
					t.Fatal(err)
				}
				var sources []string
				for _, e := range topo.Edges {
					if e.Target == "capicluster/app/cluster" && e.Type == EdgeConfigures {
						sources = append(sources, e.Source)
					}
				}
				wantNS := namespace
				if wantNS == "" {
					wantNS = "app"
				}
				if namespace == "missing" {
					if len(sources) != 0 {
						t.Errorf("missing class joined %v", sources)
					}
				} else if len(sources) != 1 || sources[0] != "clusterclass/"+wantNS+"/shared" {
					t.Errorf("class sources=%v want namespace %s", sources, wantNS)
				}
			})
		}
	}
}
