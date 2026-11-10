package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
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

func TestListSizeFailureKeepsLoadingNamespaceCoverage(t *testing.T) {
	useLoadingConfigMaps(t)
	cache := k8s.GetResourceCache()
	if _, err := cache.Client().CoreV1().ConfigMaps("alpha").Create(context.Background(), &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "second"}}, metav1.CreateOptions{}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		objs, ok := cache.ReadableObjects("configmaps", []string{"alpha"})
		if ok && len(objs) == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("second ConfigMap did not reach cache")
		}
		time.Sleep(time.Millisecond)
	}
	var list testResourceListEnvelope
	assertOK(t, get(t, "/api/resources/configmaps?namespaces=alpha,gamma&maxItems=2"), &list)
	if len(list.Items) != 2 || list.Coverage.Status != resourceapi.CoveragePartial {
		t.Fatalf("under-budget list: %+v", list)
	}
	resp := get(t, "/api/resources/configmaps?namespaces=alpha,gamma&maxItems=1")
	defer resp.Body.Close()
	var body struct {
		resourceapi.ReadError
		Count, Limit int
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusBadRequest || body.ErrorCode != readreason.ListTooLarge || body.Count != 2 || body.Limit != 1 || body.Coverage == nil || body.Coverage.Read != nil {
		t.Fatalf("size failure: %+v", body)
	}
	if err := body.Coverage.Validate(); err != nil {
		t.Fatal(err)
	}
	for _, want := range []struct {
		code  readreason.Code
		names []string
	}{{readreason.KindSyncPending, []string{"gamma"}}, {readreason.ListTooLarge, []string{"alpha"}}} {
		i := slices.IndexFunc(body.Coverage.Gaps, func(g resourceapi.Gap) bool { return g.Code == want.code })
		if i < 0 || !slices.Equal(body.Coverage.Gaps[i].Namespaces.Names, want.names) {
			t.Fatalf("missing %s coverage: %+v", want.code, body.Coverage)
		}
	}
}

func TestListSizeFailurePlacesGapsWithinTarget(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target listTarget
		gaps   []resourceapi.Gap
	}{
		{name: "all namespaces"},
		{name: "cluster", target: listTarget{clusterScoped: true}},
		{name: "explicit", target: listTarget{explicit: true, named: []string{"alpha", "gamma"}}, gaps: []resourceapi.Gap{gapOf(readreason.KindSyncPending, []string{"gamma"})}},
		{name: "unknown remainder", target: listTarget{namespaces: []string{"alpha"}, narrowedBy: []resourceapi.Narrowing{resourceapi.NarrowedByNamespaceCollection}}, gaps: []resourceapi.Gap{{Code: readreason.ReadError, Namespaces: resourceapi.UnknownNames()}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rr := httptest.NewRecorder()
			if (&Server{}).checkResourceListSize(rr, 2, 1, tc.target, tc.gaps) {
				t.Fatal("oversized list accepted")
			}
			var body resourceapi.ReadError
			if err := json.Unmarshal(rr.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Coverage == nil || body.Coverage.Read != nil || len(body.Coverage.Gaps) != len(tc.gaps)+1 {
				t.Fatalf("coverage lost: %s", rr.Body.String())
			}
			if err := body.Coverage.Validate(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestDynamicSizeFailureKeepsOtherNamespaceGaps(t *testing.T) {
	for _, tc := range []struct {
		name, path   string
		limit, items int
	}{
		{"REST bounded", "/api/resources/endpointslices?namespaces=alpha,delta,gamma&maxItems=1", 1, 2},
		{"REST whole scope", "/api/resources/endpointslices?maxItems=1", 1, 2},
		{"AI whole scope", "/api/ai/resources/endpointslices?context=none", resourceListHardLimit, resourceListHardLimit + 1},
		{"REST aggregate", "/api/resources/endpointslices?namespaces=alpha,delta,gamma&maxItems=1", 1, 1},
		{"AI bounded", "/api/ai/resources/endpointslices?namespaces=alpha,delta,gamma&context=none", resourceListHardLimit, resourceListHardLimit + 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			useTestResourceCache(t, fake.NewClientset(testNamespaces("alpha", "delta", "gamma")...))
			gvr := schema.GroupVersionResource{Group: "discovery.k8s.io", Version: "v1", Resource: "endpointslices"}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "EndpointSliceList"})
			dyn.PrependReactor("list", "endpointslices", func(a ktesting.Action) (bool, runtime.Object, error) {
				if a.GetNamespace() == "gamma" {
					return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("denied"))
				}
				list := &unstructured.UnstructuredList{}
				for i := 0; i < tc.items; i++ {
					list.Items = append(list.Items, unstructured.Unstructured{Object: map[string]any{"apiVersion": "discovery.k8s.io/v1", "kind": "EndpointSlice", "metadata": map[string]any{"name": fmt.Sprintf("present-%d", i), "namespace": a.GetNamespace()}}})
				}
				return true, list, nil
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "EndpointSlice", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list"}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			resp := get(t, tc.path)
			defer resp.Body.Close()
			var body struct {
				resourceapi.ReadError
				Count, Limit int
			}
			if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
				t.Fatal(err)
			}
			if resp.StatusCode != http.StatusBadRequest || body.ErrorCode != readreason.ListTooLarge || body.Count <= tc.limit || body.Limit != tc.limit || body.Coverage == nil || body.Coverage.Read != nil {
				t.Fatalf("size failure: %+v", body)
			}
			if err := body.Coverage.Validate(); err != nil {
				t.Fatal(err)
			}
			if body.Coverage.Scope.Type == resourceapi.ScopeAllNamespaces {
				if len(body.Coverage.Gaps) != 1 || body.Coverage.Gaps[0].Code != readreason.ListTooLarge || body.Coverage.Gaps[0].Namespaces.Extent != resourceapi.ExtentAll {
					t.Fatalf("whole scope size gap: %+v", body.Coverage)
				}
				return
			}
			if len(body.Coverage.Gaps) != 2 {
				t.Fatalf("lost gap: %+v", body.Coverage)
			}
			for _, want := range []struct {
				code  readreason.Code
				names []string
			}{{readreason.RadarForbidden, []string{"gamma"}}, {readreason.ListTooLarge, []string{"alpha", "delta"}}} {
				i := slices.IndexFunc(body.Coverage.Gaps, func(g resourceapi.Gap) bool { return g.Code == want.code })
				if i < 0 || !slices.Equal(body.Coverage.Gaps[i].Namespaces.Names, want.names) {
					t.Fatalf("missing %s gap: %+v", want.code, body.Coverage)
				}
			}
		})
	}
}
