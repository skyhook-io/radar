package k8score

import (
	"fmt"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"
)

type scaleSyncedInformer struct{ cache.SharedIndexInformer }

func (scaleSyncedInformer) HasSynced() bool { return true }

func observationScaleFixture(kinds, visible, watched int) (*DynamicResourceCache, []schema.GroupVersionResource, []string) {
	d := &DynamicResourceCache{informers: map[informerKey]*informerEntry{}}
	gvrs := make([]schema.GroupVersionResource, kinds)
	namespaces := make([]string, visible)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("ns-%05d", i)
	}
	for i := range gvrs {
		gvrs[i] = schema.GroupVersionResource{Group: "scale.example.io", Version: "v1", Resource: fmt.Sprintf("widgets-%03d", i)}
		for j := range watched {
			ns := ""
			if watched > 1 {
				ns = namespaces[j]
			}
			d.informers[informerKey{gvr: gvrs[i], ns: ns}] = &informerEntry{informer: scaleSyncedInformer{}}
		}
	}
	return d, gvrs, namespaces
}

func TestObservationScale(t *testing.T) {
	for _, tc := range []struct {
		name             string
		visible, watched int
	}{{"cluster-wide", 5000, 1}, {"namespace-fallback", 20, 20}} {
		t.Run(tc.name, func(t *testing.T) {
			d, gvrs, namespaces := observationScaleFixture(500, tc.visible, tc.watched)
			start := time.Now()
			view := d.NewObservationView(gvrs)
			calls := 0
			for _, gvr := range gvrs {
				observation := view.NamespaceObservation(gvr, "")
				if observation.State != DynamicObservationSynced {
					t.Fatal(observation)
				}
				calls++
				for _, ns := range namespaces {
					got := observation
					if observation.Scope != DynamicObservationScopeCluster {
						got = view.NamespaceObservation(gvr, ns)
					}
					if got.State != DynamicObservationSynced {
						t.Fatal(got)
					}
					calls++
				}
			}
			t.Logf("%d readiness checks in %s", calls, time.Since(start))
		})
	}
}

func BenchmarkObservationScale(b *testing.B) {
	for _, tc := range []struct {
		name             string
		visible, watched int
	}{{"cluster-wide", 5000, 1}, {"namespace-fallback", 20, 20}} {
		b.Run(tc.name, func(b *testing.B) {
			d, gvrs, namespaces := observationScaleFixture(500, tc.visible, tc.watched)
			b.ReportAllocs()
			b.ResetTimer()
			for range b.N {
				view := d.NewObservationView(gvrs)
				for _, gvr := range gvrs {
					observation := view.NamespaceObservation(gvr, "")
					for _, ns := range namespaces {
						if observation.Scope != DynamicObservationScopeCluster {
							view.NamespaceObservation(gvr, ns)
						}
					}
				}
			}
		})
	}
}
