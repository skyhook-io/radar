package mcp

import (
	"context"
	"fmt"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
	"slices"
	"sync/atomic"
	"testing"
	"time"
)

func TestDynamicListKeepsUnknownWholeProbeExtentBesideReadyNamespace(t *testing.T) {
	for _, backoff := range []time.Duration{time.Hour, time.Millisecond} {
		t.Run(backoff.String(), func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "read-a"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "read-b"}})); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)

			gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList", {Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList"})
			var unresolved atomic.Bool
			dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetNamespace() == "" {
					if unresolved.Load() {
						return true, nil, fmt.Errorf("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
					}
					return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("denied"))
				}
				return false, nil, nil
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			d := k8s.GetDynamicResourceCache()
			d.Stop()
			core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: k8s.GetResourceDiscovery().ResourceDiscovery, NamespaceFallbacks: []string{"read-a", "read-b"}, ProbeRetryBackoff: []time.Duration{backoff}, ProbeRecoveryIntervals: []time.Duration{time.Hour}})
			if err != nil {
				t.Fatal(err)
			}
			d.DynamicResourceCache = core
			if _, err := d.List(gvr, "read-a"); err != nil {
				t.Fatal(err)
			}
			if !d.WaitForSync(gvr, 3*time.Second) {
				t.Fatal("read-a did not sync")
			}
			unresolved.Store(true)
			_ = d.EnsureWatching(gvr)
			deadline := time.Now().Add(3 * time.Second)
			for backoff != time.Hour && d.Observation(gvr).ProbeRetrying {
				if time.Now().After(deadline) {
					t.Fatal("retry did not exhaust")
				}
				time.Sleep(time.Millisecond)
			}
			observation := d.Observation(gvr)
			if len(observation.Namespaces) != 1 || len(observation.UnansweredNamespaces) != 0 || observation.ProbeRetrying != (backoff == time.Hour) {
				t.Fatalf("fixture: %+v", observation)
			}

			want := readreason.KindSyncPending
			if backoff != time.Hour {
				want = readreason.ReadError
			}
			result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "widgets", Group: gvr.Group, Context: "none"})
			if err != nil {
				t.Fatal(err)
			}
			_, coverage := listBody(t, result)
			if coverage.Status != "partial" || !slices.Equal(coverage.Codes, []string{string(want)}) {
				t.Fatalf("coverage: %+v", coverage)
			}
			result, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: "widgets", Group: gvr.Group, Namespace: "read-a", Context: "none"})
			if err != nil {
				t.Fatal(err)
			}
			_, coverage = listBody(t, result)
			if coverage.Status != "complete" {
				t.Fatalf("explicit: %+v", coverage)
			}
		})
	}
}
