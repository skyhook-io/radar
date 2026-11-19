package k8score

import (
	"reflect"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func scanGroupResource(resources []APIResource, query, group string) (APIResource, bool) {
	query = strings.ToLower(query)
	var best *APIResource
	for i := range resources {
		r := &resources[i]
		if (strings.ToLower(r.Kind) == query || strings.ToLower(r.Name) == query) && r.Group == group {
			if best == nil || IsMoreStableVersion(r.Version, best.Version) {
				best = r
			}
		}
	}
	if best == nil {
		return APIResource{}, false
	}
	return *best, true
}

func TestGroupQualifiedLookupsMatchScanAcrossMutations(t *testing.T) {
	resources := []APIResource{
		{Group: "example.io", Version: "v1alpha1", Kind: "Widget", Name: "widgets", Namespaced: true},
		{Group: "example.io", Version: "v1beta1", Kind: "Widget", Name: "widgets", Namespaced: true},
		{Group: "example.io", Version: "v1beta2", Kind: "Widget", Name: "widgets"},
		{Group: "example.io", Version: "v1", Kind: "Widget", Name: "widgets", Namespaced: true},
		{Group: "example.io", Version: "v2", Kind: "Widget", Name: "widgets", IsCRD: true},
		{Group: "example.io", Version: "v2", Kind: "widgets", Name: "others", Verbs: []string{"list", "watch"}},
		{Group: "example.io", Version: "v2", Kind: "Alternate", Name: "widget", Verbs: []string{"get"}},
		{Group: "example.io", Version: "v1", Kind: "Unique", Name: "uniques", Namespaced: true},
		{Group: "other.io", Version: "v3", Kind: "Widget", Name: "widgets"},
		{Version: "v1", Kind: "Service", Name: "services", Namespaced: true},
		{Group: "example.io", Version: "v1", Kind: "Service", Name: "services"},
	}
	for _, reverse := range []bool{false, true} {
		t.Run(map[bool]string{false: "forward", true: "reverse"}[reverse], func(t *testing.T) {
			client := &scriptedDiscovery{}
			d, err := NewResourceDiscovery(client)
			if err != nil {
				t.Fatal(err)
			}
			queries := []string{"missing", "Renamed", "renamed", "UnIqUe"}
			for _, r := range resources {
				queries = append(queries, r.Kind, r.Name, strings.ToUpper(r.Kind), strings.ToUpper(r.Name))
			}
			check := func() {
				t.Helper()
				for _, group := range []string{"example.io", "other.io", "EXAMPLE.IO", "absent.io", ""} {
					for _, query := range queries {
						want, found := scanGroupResource(d.Catalog().Resources, query, group)
						if group == "" {
							want, found = d.GetResource(query)
						}
						got, ok := d.GetResourceWithGroup(query, group)
						if ok != found || !reflect.DeepEqual(got, want) {
							t.Fatalf("resource %q/%q = %+v, %v; scan = %+v, %v", group, query, got, ok, want, found)
						}
						wantGVR := schema.GroupVersionResource{}
						if found {
							wantGVR = schema.GroupVersionResource{Group: want.Group, Version: want.Version, Resource: want.Name}
						}
						if group == "" {
							wantGVR, found = d.GetGVR(query)
						}
						if gvr, ok := d.GetGVRWithGroup(query, group); ok != found || gvr != wantGVR {
							t.Fatalf("GVR %q/%q = %v, %v; scan = %v, %v", group, query, gvr, ok, wantGVR, found)
						}
					}
				}
			}
			for i := range resources {
				if reverse {
					i = len(resources) - i - 1
				}
				d.AddAPIResource(resources[i])
				check()
			}
			d.AddAPIResource(APIResource{Group: "example.io", Version: "v1", Kind: "Renamed", Name: "uniques", Verbs: []string{"list"}})
			check()
			d.AddAPIResource(APIResource{Group: "example.io", Version: "v2", Kind: "Widget", Name: "widgets", Namespaced: true, Verbs: []string{"get", "list", "watch"}})
			check()
			client.lists = []*metav1.APIResourceList{
				{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{{Kind: "Unique", Name: "uniques"}}},
				{GroupVersion: "example.io/v1", APIResources: []metav1.APIResource{{Kind: "Renamed", Name: "uniques", Namespaced: true}}},
			}
			if err := d.Refresh(); err != nil {
				t.Fatal(err)
			}
			check()
			client.lists = nil
			if err := d.Refresh(); err != nil {
				t.Fatal(err)
			}
			check()
		})
	}
}

func TestNilDiscoveryGroupQualifiedLookups(t *testing.T) {
	var d *ResourceDiscovery
	for _, group := range []string{"", "example.io"} {
		if _, ok := d.GetGVRWithGroup("Widget", group); ok {
			t.Fatal("nil discovery returned a GVR")
		}
		if _, ok := d.GetResourceWithGroup("Widget", group); ok {
			t.Fatal("nil discovery returned a resource")
		}
	}
}
