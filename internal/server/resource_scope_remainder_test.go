package server

import (
	"encoding/json"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestDynamicReadsKeepReadableNamespacesAndUnknownProbeRemainder(t *testing.T) {
	for _, backoff := range []time.Duration{time.Hour, time.Millisecond} {
		t.Run(backoff.String(), func(t *testing.T) {
			useTestResourceCache(t, fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "read-a"}}, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "read-b"}}))

			gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList", {Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}: "EndpointSliceList"},
				&unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "present", "namespace": "read-a"}}},
				&unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "second", "namespace": "read-a"}}})
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
			for _, path := range []string{"/api/resource-counts", "/api/resources/widgets?group=example.test"} {
				if path == "/api/resource-counts" {
					var counts ResourceCountsResponse
					assertOK(t, get(t, path), &counts)
					coverage := counts.Coverage["example.test/Widget"]
					if coverage.Status != resourceapi.CoveragePartial || len(coverage.Gaps) != 1 || coverage.Gaps[0].Code != want || coverage.Gaps[0].Namespaces == nil || coverage.Gaps[0].Namespaces.Extent != resourceapi.ExtentUnknown {
						t.Fatalf("count: %+v", coverage)
					}
				} else {
					var list testResourceListEnvelope
					assertOK(t, get(t, path), &list)
					if list.Coverage.Status != resourceapi.CoveragePartial || len(list.Coverage.Gaps) != 1 || list.Coverage.Gaps[0].Code != want || list.Coverage.Gaps[0].Namespaces.Extent != resourceapi.ExtentUnknown {
						t.Fatalf("list: %+v", list.Coverage)
					}
					if len(list.Items) != 2 {
						t.Fatalf("readable namespace rows were lost: %+v", list.Items)
					}
					sizeResponse := get(t, path+"&maxItems=1")
					var sizeFailure struct {
						resourceapi.ReadError
						Count, Limit int
					}
					if err := json.NewDecoder(sizeResponse.Body).Decode(&sizeFailure); err != nil {
						t.Fatal(err)
					}
					sizeResponse.Body.Close()
					if sizeResponse.StatusCode != 400 || sizeFailure.ErrorCode != readreason.ListTooLarge || sizeFailure.Count != 2 || sizeFailure.Limit != 1 || sizeFailure.Coverage == nil || sizeFailure.Coverage.Read != nil {
						t.Fatalf("size failure: %+v", sizeFailure)
					}
					if err := sizeFailure.Coverage.Validate(); err != nil {
						t.Fatal(err)
					}
					if len(sizeFailure.Coverage.Gaps) != 2 || sizeFailure.Coverage.Gaps[0].Code != want || sizeFailure.Coverage.Gaps[0].Namespaces.Extent != resourceapi.ExtentUnknown {
						t.Fatalf("lost probe remainder: %+v", sizeFailure.Coverage)
					}
					assertOK(t, get(t, path+"&namespace=read-a"), &list)
					if list.Coverage.Status != resourceapi.CoverageComplete {
						t.Fatalf("explicit: %+v", list.Coverage)
					}
				}
			}
		})
	}
}
