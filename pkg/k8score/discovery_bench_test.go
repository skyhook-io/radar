package k8score

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func BenchmarkGroupQualifiedDiscovery(b *testing.B) {
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			resources := make([]metav1.APIResource, size)
			for i := range resources {
				resources[i] = metav1.APIResource{Kind: fmt.Sprint("Thing", i), Name: fmt.Sprint("things", i), Namespaced: true}
			}
			d, err := NewResourceDiscovery(&scriptedDiscovery{lists: []*metav1.APIResourceList{{GroupVersion: "example.io/v1", APIResources: resources}}})
			if err != nil {
				b.Fatal(err)
			}
			for _, query := range []string{fmt.Sprint("Thing", size-1), fmt.Sprint("things", size-1), "Missing"} {
				b.Run("GVR/"+query, func(b *testing.B) {
					b.ReportAllocs()
					for range b.N {
						d.GetGVRWithGroup(query, "example.io")
					}
				})
				b.Run("Resource/"+query, func(b *testing.B) {
					b.ReportAllocs()
					for range b.N {
						d.GetResourceWithGroup(query, "example.io")
					}
				})
			}
		})
	}
}
