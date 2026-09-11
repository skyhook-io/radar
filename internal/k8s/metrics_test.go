package k8s

import (
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestResolveMetricsGVRPrefersV1AndSupportsV1Beta1(t *testing.T) {
	tests := []struct {
		name      string
		resources []APIResource
		want      string
	}{
		{
			name: "v1 preferred when both are served",
			resources: []APIResource{
				{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "PodMetrics", Name: "pods", Namespaced: true, Verbs: []string{"get", "list"}},
				{Group: "metrics.k8s.io", Version: "v1", Kind: "PodMetrics", Name: "pods", Namespaced: true, Verbs: []string{"get", "list"}},
			},
			want: "v1",
		},
		{
			name: "v1beta1 remains supported",
			resources: []APIResource{
				{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "PodMetrics", Name: "pods", Namespaced: true, Verbs: []string{"get", "list"}},
			},
			want: "v1beta1",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{})
			if err := InitTestDynamicResourceCache(dyn, tt.resources); err != nil {
				t.Fatalf("InitTestDynamicResourceCache: %v", err)
			}
			t.Cleanup(ResetTestDynamicState)

			gvr, ok := ResolveMetricsGVR("pods")
			if !ok || gvr.Version != tt.want {
				t.Fatalf("ResolveMetricsGVR = %v, ok=%v, want version %s", gvr, ok, tt.want)
			}
			path, ok := MetricsAPIPath("pods")
			if !ok || path != "/apis/metrics.k8s.io/"+tt.want+"/pods" {
				t.Fatalf("MetricsAPIPath = %q, ok=%v", path, ok)
			}
		})
	}
}

func TestResolveMetricsGVRIgnoresFailureInAnotherServedVersion(t *testing.T) {
	tests := []struct {
		name           string
		healthyVersion string
		failedVersion  string
	}{
		{name: "v1 healthy", healthyVersion: "v1", failedVersion: "v1beta1"},
		{name: "v1beta1 healthy", healthyVersion: "v1beta1", failedVersion: "v1"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeDiscovery := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
			fakeDiscovery.Resources = []*metav1.APIResourceList{{
				GroupVersion: "metrics.k8s.io/" + tt.healthyVersion,
				APIResources: []metav1.APIResource{{Name: "pods", Kind: "PodMetrics", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}}},
			}}
			fakeDiscovery.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
				return true, nil, &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
					{Group: "metrics.k8s.io", Version: tt.failedVersion}: apierrors.NewServiceUnavailable("metrics API unavailable"),
				}}
			})
			core, err := k8score.NewResourceDiscovery(fakeDiscovery, k8score.WithDiscoveryCacheTTL(0))
			if err != nil {
				t.Fatalf("NewResourceDiscovery: %v", err)
			}
			resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
			t.Cleanup(func() { resourceDiscovery = nil })

			gvr, ok := ResolveMetricsGVR("pods")
			if !ok || gvr.Version != tt.healthyVersion {
				t.Fatalf("ResolveMetricsGVR = %v, ok=%v, want version %s", gvr, ok, tt.healthyVersion)
			}
		})
	}
}

func TestResolveMetricsGVRRefreshesExpiredDiscovery(t *testing.T) {
	fakeDiscovery := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	core, err := k8score.NewResourceDiscovery(fakeDiscovery, k8score.WithDiscoveryCacheTTL(0))
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
	t.Cleanup(func() { resourceDiscovery = nil })

	fakeDiscovery.Resources = []*metav1.APIResourceList{{
		GroupVersion: "metrics.k8s.io/v1",
		APIResources: []metav1.APIResource{{Name: "pods", Kind: "PodMetrics", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}}},
	}}
	gvr, ok := ResolveMetricsGVR("pods")
	if !ok || gvr.Version != "v1" {
		t.Fatalf("ResolveMetricsGVR = %v, ok=%v, want metrics.k8s.io/v1", gvr, ok)
	}
}

func TestResolveMetricsGVRRefreshesImmediatelyAfterDiscoveryMiss(t *testing.T) {
	fakeDiscovery := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	core, err := k8score.NewResourceDiscovery(fakeDiscovery, k8score.WithDiscoveryCacheTTL(time.Hour))
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
	t.Cleanup(func() { resourceDiscovery = nil })

	fakeDiscovery.Resources = []*metav1.APIResourceList{{
		GroupVersion: "metrics.k8s.io/v1",
		APIResources: []metav1.APIResource{{Name: "pods", Kind: "PodMetrics", Namespaced: true, Verbs: metav1.Verbs{"get", "list"}}},
	}}
	gvr, ok := ResolveMetricsGVR("pods")
	if !ok || gvr.Version != "v1" {
		t.Fatalf("ResolveMetricsGVR = %v, ok=%v, want immediate metrics.k8s.io/v1 recovery", gvr, ok)
	}
}

func TestResolveMetricsGVRLimitsForcedRefreshesAfterDiscoveryMiss(t *testing.T) {
	fakeDiscovery := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
	refreshes := 0
	fakeDiscovery.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
		refreshes++
		return false, nil, nil
	})
	core, err := k8score.NewResourceDiscovery(fakeDiscovery, k8score.WithDiscoveryCacheTTL(time.Hour))
	if err != nil {
		t.Fatalf("NewResourceDiscovery: %v", err)
	}
	resourceDiscovery = &ResourceDiscovery{ResourceDiscovery: core}
	t.Cleanup(func() { resourceDiscovery = nil })
	refreshes = 0

	for range 2 {
		if _, ok := ResolveMetricsGVR("pods"); ok {
			t.Fatal("ResolveMetricsGVR unexpectedly found pods metrics")
		}
	}
	if refreshes != 1 {
		t.Fatalf("forced discovery refreshes = %d, want 1 within cooldown", refreshes)
	}
}
