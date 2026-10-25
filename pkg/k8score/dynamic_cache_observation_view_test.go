package k8score

import (
	"reflect"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestObservationViewLiveReadinessAndRetries(t *testing.T) {
	d, gvrs, namespaces := observationScaleFixture(1, 20, 20)
	gvr := gvrs[0]
	d.config.NamespaceFallbacks = namespaces
	view := d.NewObservationView(gvrs)
	d.probeRetries = map[schema.GroupVersionResource]*probeRetry{}
	compare := func() {
		t.Helper()
		for _, ns := range append([]string{"", "outside"}, namespaces...) {
			want := d.NamespaceObservation(gvr, ns)
			if got := view.NamespaceObservation(gvr, ns); !reflect.DeepEqual(got, want) {
				t.Fatalf("%s: view=%+v cache=%+v", ns, got, want)
			}
		}
	}
	compare()
	for _, whole := range []bool{false, true} {
		for _, fullWalk := range []bool{false, true} {
			for _, running := range []bool{false, true} {
				for _, unanswered := range [][]string{nil, {namespaces[0], "outside"}} {
					d.probeRetries[gvr] = &probeRetry{whole: whole, fullWalk: fullWalk, running: running, unanswered: unanswered}
					compare()
				}
			}
		}
	}
	delete(d.probeRetries, gvr)
	key := informerKey{gvr: gvr, ns: namespaces[0]}
	d.informers[key].informer = scaleUnsyncedInformer{}
	d.informers[key].startedAt = time.Now().Add(-time.Minute)
	compare()
	if got := view.NamespaceObservation(gvr, namespaces[0]); got.ReasonCode != "sync_stalled" {
		t.Fatal(got)
	}
	d.fanoutIncomplete = map[schema.GroupVersionResource]bool{gvr: true}
	compare()
	for key := range d.informers {
		delete(d.informers, key)
	}
	d.informers[informerKey{gvr: gvr}] = &informerEntry{informer: scaleSyncedInformer{}}
	d.informerGeneration++
	compare()
	if got := view.NamespaceObservation(gvr, "outside"); got.Scope != DynamicObservationScopeCluster || got.State != DynamicObservationSynced {
		t.Fatal(got)
	}
	delete(d.informers, informerKey{gvr: gvr})
	d.informerGeneration++
	d.observations = map[schema.GroupVersionResource]retainedObservation{gvr: {state: DynamicObservationDeferred, reasonCode: "scope_probe_incomplete"}}
	d.probeRetries[gvr] = &probeRetry{unanswered: []string{"outside"}, running: true}
	compare()
	d.stopped = true
	compare()
}

type scaleUnsyncedInformer struct{ scaleSyncedInformer }

func (scaleUnsyncedInformer) HasSynced() bool { return false }
