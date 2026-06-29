package k8s

import (
	"context"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestFallbackListProbeUsesNamespaceFallback(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "VirtualServiceList"},
	)
	client.PrependReactor("list", "virtualservices", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listAction := action.(k8stesting.ListAction)
		if listAction.GetNamespace() == "team-a" {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, "", nil)
	})

	namespace, ok := fallbackListProbe(client, gvr, true, "team-a")
	if !ok {
		t.Fatal("expected namespace fallback probe to succeed")
	}
	if namespace != "team-a" {
		t.Fatalf("namespace = %q, want team-a", namespace)
	}
}

func TestFallbackListProbeDoesNotNamespaceFallbackClusterScoped(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "gateway.networking.k8s.io", Version: "v1", Resource: "gatewayclasses"}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "GatewayClassList"},
	)
	client.PrependReactor("list", "gatewayclasses", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listAction := action.(k8stesting.ListAction)
		if listAction.GetNamespace() != "" {
			t.Fatalf("cluster-scoped fallback probe unexpectedly used namespace %q", listAction.GetNamespace())
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, "", nil)
	})

	if _, ok := fallbackListProbe(client, gvr, false, "team-a"); ok {
		t.Fatal("expected cluster-scoped fallback probe to fail after cluster-wide denial")
	}
}

func TestRegisterSupportedCRDFallbacks_RegistersSupportedCRDFromPartialDiscovery(t *testing.T) {
	cases := []struct {
		name       string
		group      string
		version    string
		resource   string
		kind       string
		objectName string
	}{
		{
			name:       "istio virtualservice",
			group:      "networking.istio.io",
			version:    "v1",
			resource:   "virtualservices",
			kind:       "VirtualService",
			objectName: "reviews",
		},
		{
			name:       "keda scaledobject",
			group:      "keda.sh",
			version:    "v1alpha1",
			resource:   "scaledobjects",
			kind:       "ScaledObject",
			objectName: "worker-scale",
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ResetDynamicResourceCache()
			ResetResourceDiscovery()
			t.Cleanup(func() {
				ResetDynamicResourceCache()
				ResetResourceDiscovery()
				clientMu.Lock()
				dynamicClient = nil
				clientMu.Unlock()
			})

			coreGVR := schema.GroupVersionResource{Group: "", Version: "v1", Resource: "pods"}
			targetGVR := schema.GroupVersionResource{Group: tt.group, Version: tt.version, Resource: tt.resource}
			listKinds := supportedFallbackListKinds(coreGVR, tt.group)
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
				runtime.NewScheme(),
				listKinds,
				&unstructured.Unstructured{
					Object: map[string]any{
						"apiVersion": tt.group + "/" + tt.version,
						"kind":       tt.kind,
						"metadata": map[string]any{
							"name":      tt.objectName,
							"namespace": "default",
						},
					},
				},
			)
			dyn.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
				listAction := action.(k8stesting.ListAction)
				gvr := listAction.GetResource()
				if gvr == targetGVR {
					return false, nil, nil
				}
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, "", nil)
			})
			dyn.PrependWatchReactor("*", func(action k8stesting.Action) (bool, watch.Interface, error) {
				if action.GetResource() == targetGVR {
					return true, watch.NewFake(), nil
				}
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource}, "", nil)
			})
			clientMu.Lock()
			dynamicClient = dyn
			clientMu.Unlock()

			fakeDisc := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
			fakeDisc.Resources = []*metav1.APIResourceList{
				{
					GroupVersion: "v1",
					APIResources: []metav1.APIResource{
						{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}},
					},
				},
			}
			fakeDisc.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
				err := &discovery.ErrGroupDiscoveryFailed{
					Groups: map[schema.GroupVersion]error{
						{Group: tt.group, Version: tt.version}: apierrors.NewForbidden(
							schema.GroupResource{Group: tt.group, Resource: tt.resource},
							"",
							nil,
						),
					},
				}
				return true, nil, err
			})
			coreDiscovery, err := k8score.NewResourceDiscovery(fakeDisc)
			if err != nil {
				t.Fatalf("NewResourceDiscovery failed: %v", err)
			}
			resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: coreDiscovery}
			if !resourceDiscovery.HasPartialDiscovery() || !resourceDiscovery.GroupHadPartialDiscovery(tt.group) {
				t.Fatalf("expected fake discovery to record partial %s discovery", tt.group)
			}
			if _, ok := resourceDiscovery.GetGVRWithGroup(tt.kind, tt.group); ok {
				t.Fatalf("%s should be absent before fallback probing", tt.kind)
			}

			RegisterSupportedCRDFallbacks()

			gvr, ok := resourceDiscovery.GetGVRWithGroup(tt.kind, tt.group)
			if !ok {
				t.Fatalf("expected fallback probing to register %s/%s", tt.group, tt.kind)
			}
			if gvr != targetGVR {
				t.Fatalf("GVR = %v, want %v", gvr, targetGVR)
			}
			if !resourceDiscovery.SupportsWatchGVR(targetGVR) {
				t.Fatal("registered fallback GVR should support watch")
			}

			if err := InitDynamicResourceCache(nil); err != nil {
				t.Fatalf("InitDynamicResourceCache failed: %v", err)
			}
			dynamicCache := GetDynamicResourceCache()
			if dynamicCache == nil {
				t.Fatal("dynamic cache was not initialized")
			}
			if err := dynamicCache.EnsureWatching(targetGVR); err != nil {
				t.Fatalf("EnsureWatching failed: %v", err)
			}
			if !dynamicCache.WaitForSync(targetGVR, 5*time.Second) {
				t.Fatalf("timed out waiting for %s informer sync", tt.kind)
			}
			cache := &ResourceCache{}
			items, err := cache.ListDynamicWithGroup(context.Background(), tt.kind, "default", tt.group)
			if err != nil {
				t.Fatalf("ListDynamicWithGroup failed after fallback registration: %v", err)
			}
			if len(items) != 1 || items[0].GetName() != tt.objectName {
				t.Fatalf("items = %#v, want one %s named %s", items, tt.kind, tt.objectName)
			}
		})
	}
}

func TestRegisterSupportedCRDFallbacks_DoesNotProbeOnCleanDiscovery(t *testing.T) {
	ResetResourceDiscovery()
	t.Cleanup(func() {
		ResetResourceDiscovery()
		clientMu.Lock()
		dynamicClient = nil
		clientMu.Unlock()
	})

	istioGVR := schema.GroupVersionResource{Group: "networking.istio.io", Version: "v1", Resource: "virtualservices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{istioGVR: "VirtualServiceList"},
	)
	listCalls := 0
	dyn.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		listCalls++
		return false, nil, nil
	})
	clientMu.Lock()
	dynamicClient = dyn
	clientMu.Unlock()

	fakeDisc := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}},
			},
		},
	}
	coreDiscovery, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("NewResourceDiscovery failed: %v", err)
	}
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: coreDiscovery}
	if resourceDiscovery.HasPartialDiscovery() {
		t.Fatal("clean fake discovery should not be partial")
	}

	RegisterSupportedCRDFallbacks()

	if listCalls != 0 {
		t.Fatalf("fallback probing issued %d list calls on clean discovery, want 0", listCalls)
	}
	if _, ok := resourceDiscovery.GetGVRWithGroup("VirtualService", "networking.istio.io"); ok {
		t.Fatal("VirtualService should not be registered without partial discovery")
	}
}

func TestRegisterSupportedCRDFallbacks_DoesNotRegisterWhenWatchDenied(t *testing.T) {
	ResetResourceDiscovery()
	t.Cleanup(func() {
		ResetResourceDiscovery()
		clientMu.Lock()
		dynamicClient = nil
		clientMu.Unlock()
	})

	targetGVR := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		supportedFallbackListKinds(schema.GroupVersionResource{}, "keda.sh"),
	)
	dyn.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.ListAction).GetResource() == targetGVR {
			return false, nil, nil
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource}, "", nil)
	})
	dyn.PrependWatchReactor("*", func(action k8stesting.Action) (bool, watch.Interface, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource}, "", nil)
	})
	clientMu.Lock()
	dynamicClient = dyn
	clientMu.Unlock()

	fakeDisc := fakeDiscoveryWithPartialGroup(t, "keda.sh", "v1alpha1")
	coreDiscovery, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("NewResourceDiscovery failed: %v", err)
	}
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: coreDiscovery}

	RegisterSupportedCRDFallbacks()

	if _, ok := resourceDiscovery.GetGVRWithGroup("ScaledObject", "keda.sh"); ok {
		t.Fatal("ScaledObject should not be registered when watch is denied")
	}
}

func TestRegisterSupportedCRDFallbacks_MultiVersionProbeOrder(t *testing.T) {
	cases := []struct {
		name          string
		allowedGVR    schema.GroupVersionResource
		wantGVR       schema.GroupVersionResource
		wantProbeGVRs []schema.GroupVersionResource
	}{
		{
			name:       "falls back to beta when v1 is not listable",
			allowedGVR: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: "gitrepositories"},
			wantGVR:    schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: "gitrepositories"},
			wantProbeGVRs: []schema.GroupVersionResource{
				{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"},
				{Group: "source.toolkit.fluxcd.io", Version: "v1beta2", Resource: "gitrepositories"},
			},
		},
		{
			name:       "stops after stable version succeeds",
			allowedGVR: schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"},
			wantGVR:    schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"},
			wantProbeGVRs: []schema.GroupVersionResource{
				{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"},
			},
		},
	}

	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			ResetDynamicResourceCache()
			ResetResourceDiscovery()
			t.Cleanup(func() {
				ResetDynamicResourceCache()
				ResetResourceDiscovery()
				clientMu.Lock()
				dynamicClient = nil
				clientMu.Unlock()
			})

			listKinds := supportedFallbackListKinds(schema.GroupVersionResource{}, "source.toolkit.fluxcd.io")
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
			var probed []schema.GroupVersionResource
			dyn.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
				gvr := action.(k8stesting.ListAction).GetResource()
				if gvr.Resource == "gitrepositories" {
					probed = append(probed, gvr)
				}
				if gvr == tt.allowedGVR {
					return false, nil, nil
				}
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: gvr.Group, Resource: gvr.Resource}, "", nil)
			})
			dyn.PrependWatchReactor("*", func(action k8stesting.Action) (bool, watch.Interface, error) {
				if action.GetResource() == tt.allowedGVR {
					return true, watch.NewFake(), nil
				}
				return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource}, "", nil)
			})
			clientMu.Lock()
			dynamicClient = dyn
			clientMu.Unlock()

			fakeDisc := fakeDiscoveryWithPartialGroup(t, "source.toolkit.fluxcd.io", "v1")
			coreDiscovery, err := k8score.NewResourceDiscovery(fakeDisc)
			if err != nil {
				t.Fatalf("NewResourceDiscovery failed: %v", err)
			}
			resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: coreDiscovery}

			RegisterSupportedCRDFallbacks()

			gotGVR, ok := resourceDiscovery.GetGVRWithGroup("GitRepository", "source.toolkit.fluxcd.io")
			if !ok {
				t.Fatal("expected GitRepository to be registered")
			}
			if gotGVR != tt.wantGVR {
				t.Fatalf("GVR = %v, want %v", gotGVR, tt.wantGVR)
			}
			if len(probed) != len(tt.wantProbeGVRs) {
				t.Fatalf("probed %v, want %v", probed, tt.wantProbeGVRs)
			}
			for i := range probed {
				if probed[i] != tt.wantProbeGVRs[i] {
					t.Fatalf("probed %v, want %v", probed, tt.wantProbeGVRs)
				}
			}
		})
	}
}

func TestRegisterSupportedCRDFallbacks_SkipsGroupsWithoutPartialDiscovery(t *testing.T) {
	ResetResourceDiscovery()
	t.Cleanup(func() {
		ResetResourceDiscovery()
		clientMu.Lock()
		dynamicClient = nil
		clientMu.Unlock()
	})

	kedaGVR := schema.GroupVersionResource{Group: "keda.sh", Version: "v1alpha1", Resource: "scaledobjects"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		supportedFallbackListKinds(schema.GroupVersionResource{}, "networking.istio.io", "keda.sh"),
	)
	probedKeda := false
	dyn.PrependReactor("list", "*", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.(k8stesting.ListAction).GetResource() == kedaGVR {
			probedKeda = true
		}
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Group: action.GetResource().Group, Resource: action.GetResource().Resource}, "", nil)
	})
	clientMu.Lock()
	dynamicClient = dyn
	clientMu.Unlock()

	fakeDisc := fakeDiscoveryWithPartialGroup(t, "networking.istio.io", "v1")
	coreDiscovery, err := k8score.NewResourceDiscovery(fakeDisc)
	if err != nil {
		t.Fatalf("NewResourceDiscovery failed: %v", err)
	}
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: coreDiscovery}

	RegisterSupportedCRDFallbacks()

	if probedKeda {
		t.Fatal("fallback probed keda.sh even though only networking.istio.io had partial discovery")
	}
}

func supportedFallbackListKinds(coreGVR schema.GroupVersionResource, groups ...string) map[schema.GroupVersionResource]string {
	listKinds := map[schema.GroupVersionResource]string{}
	if coreGVR.Resource != "" {
		listKinds[coreGVR] = "PodList"
	}
	groupSet := make(map[string]bool, len(groups))
	for _, group := range groups {
		groupSet[group] = true
	}
	for _, candidate := range supportedCRDFallbacks {
		if !groupSet[candidate.Group] {
			continue
		}
		for _, version := range candidate.Versions {
			gvr := schema.GroupVersionResource{Group: candidate.Group, Version: version, Resource: candidate.Resource}
			listKinds[gvr] = candidate.Kind + "List"
		}
	}
	return listKinds
}

func fakeDiscoveryWithPartialGroup(t *testing.T, group, version string) *fakediscovery.FakeDiscovery {
	t.Helper()
	fakeDisc := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	fakeDisc.Resources = []*metav1.APIResourceList{
		{
			GroupVersion: "v1",
			APIResources: []metav1.APIResource{
				{Name: "pods", Kind: "Pod", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}},
			},
		},
	}
	fakeDisc.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, &discovery.ErrGroupDiscoveryFailed{
			Groups: map[schema.GroupVersion]error{
				{Group: group, Version: version}: apierrors.NewForbidden(
					schema.GroupResource{Group: group, Resource: "resources"},
					"",
					nil,
				),
			},
		}
	})
	return fakeDisc
}
