package k8score

import (
	"testing"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

// TestKueueVisibilityAPIDoesNotShadowQueueCRDs uses the shape Kueue's
// aggregated visibility API serves live (v0.18): Kind PendingWorkloadsSummary,
// no verbs, two plurals that collide with the real CRDs, two versions.
func TestKueueVisibilityAPIDoesNotShadowQueueCRDs(t *testing.T) {
	visibility := func(version, name string, namespaced bool) APIResource {
		return APIResource{Group: "visibility.kueue.x-k8s.io", Version: version, Kind: "PendingWorkloadsSummary", Name: name, Namespaced: namespaced, IsCRD: true, Verbs: []string{}}
	}
	crd := func(kind, name string, namespaced bool) APIResource {
		return APIResource{Group: "kueue.x-k8s.io", Version: "v1beta2", Kind: kind, Name: name, Namespaced: namespaced, IsCRD: true, Verbs: []string{"get", "list", "watch"}}
	}
	aggregated := []APIResource{visibility("v1beta2", "localqueues", true), visibility("v1beta2", "clusterqueues", false), visibility("v1beta1", "localqueues", true), visibility("v1beta1", "clusterqueues", false)}
	real := []APIResource{crd("LocalQueue", "localqueues", true), crd("ClusterQueue", "clusterqueues", false)}
	for _, order := range [][]APIResource{append(append([]APIResource{}, aggregated...), real...), append(append([]APIResource{}, real...), aggregated...)} {
		d := &ResourceDiscovery{resourceMap: map[string]APIResource{}, gvrMap: map[string]schema.GroupVersionResource{}}
		for _, r := range order {
			d.AddAPIResource(r)
		}
		for _, name := range []string{"localqueues", "clusterqueues", "LocalQueue", "ClusterQueue"} {
			gvr, ok := d.GetGVR(name)
			if !ok || gvr.Group != "kueue.x-k8s.io" {
				t.Fatalf("%s resolved to %v (%v)", name, gvr, ok)
			}
		}
		if gvr, ok := d.GetGVRWithGroup("localqueues", "visibility.kueue.x-k8s.io"); !ok || gvr.Version != "v1beta2" {
			t.Fatalf("visibility group lookup: %v %v", gvr, ok)
		}
		versions := map[string]bool{}
		for _, r := range d.Catalog().Resources {
			if r.Group == "visibility.kueue.x-k8s.io" && r.Name == "localqueues" {
				versions[r.Version] = true
			}
		}
		if !versions["v1beta1"] || !versions["v1beta2"] {
			t.Fatalf("catalog must keep every served version: %v", versions)
		}
	}
}
