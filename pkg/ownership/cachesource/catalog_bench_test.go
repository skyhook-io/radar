package cachesource

import (
	"fmt"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	fakediscovery "k8s.io/client-go/discovery/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/pkg/k8score"
)

func BenchmarkBeginCatalog(b *testing.B) {
	for _, size := range []int{100, 1000} {
		b.Run(fmt.Sprint(size), func(b *testing.B) {
			resources := make([]metav1.APIResource, size)
			for i := range resources {
				resources[i] = metav1.APIResource{Kind: fmt.Sprint("Thing", i), Name: fmt.Sprint("things", i), Namespaced: true, Verbs: []string{"get", "list", "watch", "delete"}}
			}
			client := &fakediscovery.FakeDiscovery{Fake: &k8stesting.Fake{Resources: []*metav1.APIResourceList{{GroupVersion: "example.io/v1", APIResources: resources}}}}
			d, err := k8score.NewResourceDiscovery(client)
			if err != nil {
				b.Fatal(err)
			}
			caches := Caches{Typed: &k8score.ResourceCache{}, Discovery: d}
			source := New(func() Caches { return caches })
			if _, err := source.Begin(); err != nil {
				b.Fatal(err)
			}
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				if _, err := source.Begin(); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
