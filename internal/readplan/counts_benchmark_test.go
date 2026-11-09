package readplan

import (
	"fmt"
	"github.com/skyhook-io/radar/pkg/k8score"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"testing"
)

type countsObservation struct{ namespaces []string }

func (o countsObservation) NamespaceObservation(_ schema.GroupVersionResource, _ string) k8score.DynamicResourceObservation {
	return k8score.DynamicResourceObservation{State: k8score.DynamicObservationSynced, Scope: k8score.DynamicObservationScopeExplicitNamespaces, Namespaces: o.namespaces}
}

func BenchmarkCountsPlanning1000Namespaces100Kinds(b *testing.B) {
	namespaces := make([]string, 1000)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("ns-%04d", i)
	}
	observer := countsObservation{namespaces: namespaces}
	gvrs := make([]schema.GroupVersionResource, 100)
	for i := range gvrs {
		gvrs[i] = schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: fmt.Sprintf("widgets-%03d", i)}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		for _, gvr := range gvrs {
			inventory := ForInventory(Dynamic(observer, gvr, namespaces, false), namespaces, true, false)
			if !inventory.CanReadTarget {
				b.Fatal("ready scope must be readable")
			}
		}
	}
}
