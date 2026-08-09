package server

import (
	"bytes"
	"io"
	"net/http/httptest"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestReadBoundedUpgradeResponse(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		limit   int64
		wantErr bool
	}{
		{name: "within limit", body: "1234", limit: 4},
		{name: "over limit", body: "12345", limit: 4, wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := readBoundedUpgradeResponse(io.NopCloser(bytes.NewBufferString(tc.body)), tc.limit)
			if (err != nil) != tc.wantErr {
				t.Fatalf("read error = %v, wantErr %v", err, tc.wantErr)
			}
			if !tc.wantErr && string(got) != tc.body {
				t.Fatalf("body = %q, want %q", got, tc.body)
			}
		})
	}
}

func TestDiscoverUpgradePrometheusRuleDistinguishesPartialDiscoveryFromAbsentAPI(t *testing.T) {
	for _, tc := range []struct {
		name             string
		partial          bool
		wantDiscoverable bool
	}{
		{name: "clean discovery without API", wantDiscoverable: true},
		{name: "partial monitoring discovery", partial: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fakeDiscovery := fakeclientset.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
			fakeDiscovery.Resources = []*metav1.APIResourceList{{
				GroupVersion: "v1",
				APIResources: []metav1.APIResource{{Name: "pods", Kind: "Pod", Namespaced: true}},
			}}
			if tc.partial {
				fakeDiscovery.PrependReactor("get", "resource", func(k8stesting.Action) (bool, runtime.Object, error) {
					return true, nil, &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{
						{Group: "monitoring.coreos.com", Version: "v1"}: apierrors.NewForbidden(schema.GroupResource{Group: "monitoring.coreos.com", Resource: "prometheusrules"}, "", nil),
					}}
				})
			}
			coreDiscovery, err := k8score.NewResourceDiscovery(fakeDiscovery)
			if err != nil {
				t.Fatalf("NewResourceDiscovery: %v", err)
			}
			_, installed, discoverable := discoverUpgradePrometheusRule(&k8s.ResourceDiscovery{ResourceDiscovery: coreDiscovery})
			if installed || discoverable != tc.wantDiscoverable {
				t.Fatalf("discovery state = installed=%v discoverable=%v, want false/%v", installed, discoverable, tc.wantDiscoverable)
			}
		})
	}
}

type fakeUpgradeResourceLister struct {
	synced            bool
	clusterWideSynced bool
	lists             int
	resources         []*unstructured.Unstructured
}

func TestUpgradeReadinessNamespacesIgnoresBrowsingFilter(t *testing.T) {
	req := httptest.NewRequest("GET", "/api/upgrade-readiness?namespaces=payments", nil)
	if got := (&Server{}).upgradeReadinessNamespaces(req); got != nil {
		t.Fatalf("upgrade namespace scope = %v, want cluster-wide", got)
	}
}

func TestUpgradeReadinessNamespacesHonorsForcedScope(t *testing.T) {
	previousForce := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("tenant-a")
	t.Cleanup(func() {
		k8s.SetNamespaceScopeOverride("")
		k8s.ForceNamespaceScope = previousForce
	})

	req := httptest.NewRequest("GET", "/api/upgrade-readiness", nil)
	got := (&Server{}).upgradeReadinessNamespaces(req)
	if len(got) != 1 || got[0] != "tenant-a" {
		t.Fatalf("upgrade namespace scope = %v, want [tenant-a]", got)
	}
}

func TestUpgradeReadinessNamespacesIntersectsForcedScopeWithUserAccess(t *testing.T) {
	previousForce := k8s.ForceNamespaceScope
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("tenant-a")
	t.Cleanup(func() {
		k8s.SetNamespaceScopeOverride("")
		k8s.ForceNamespaceScope = previousForce
	})

	s := &Server{permCache: auth.NewPermissionCache()}
	s.permCache.Set("alice", &auth.UserPermissions{AllowedNamespaces: []string{"tenant-b"}})
	req := requestWithUser("GET", "/api/upgrade-readiness", &auth.User{Username: "alice"})
	if got := s.upgradeReadinessNamespaces(req); !noNamespaceAccess(got) {
		t.Fatalf("upgrade namespace scope = %v, want no access", got)
	}
}

func TestSameNamespaceScopeDistinguishesClusterWideFromEmpty(t *testing.T) {
	if sameNamespaceScope(nil, []string{}) {
		t.Fatal("cluster-wide and empty namespace scopes are not equivalent")
	}
	if !sameNamespaceScope([]string{"b", "a"}, []string{"a", "b"}) {
		t.Fatal("scope comparison should ignore ordering")
	}
}

func (f *fakeUpgradeResourceLister) ListNamespaces(schema.GroupVersionResource, []string) ([]*unstructured.Unstructured, error) {
	f.lists++
	if f.lists == 1 {
		return []*unstructured.Unstructured{}, nil
	}
	return f.resources, nil
}

func (f *fakeUpgradeResourceLister) WaitForSync(schema.GroupVersionResource, time.Duration) bool {
	return f.synced
}

func (f *fakeUpgradeResourceLister) IsClusterWideSynced(schema.GroupVersionResource) bool {
	return f.clusterWideSynced
}

func TestListSyncedUpgradeResourcesRejectsColdCache(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheusrules"}
	cache := &fakeUpgradeResourceLister{}
	resources, synced, err := listSyncedUpgradeResources(cache, gvr, []string{"default"})
	if err != nil || synced || resources != nil || cache.lists != 1 {
		t.Fatalf("cold cache result = resources=%v synced=%v err=%v lists=%d", resources, synced, err, cache.lists)
	}

	rule := &unstructured.Unstructured{Object: map[string]any{"kind": "PrometheusRule"}}
	cache = &fakeUpgradeResourceLister{synced: true, resources: []*unstructured.Unstructured{rule}}
	resources, synced, err = listSyncedUpgradeResources(cache, gvr, []string{"default"})
	if err != nil || !synced || len(resources) != 1 || cache.lists != 2 {
		t.Fatalf("synced cache result = resources=%v synced=%v err=%v lists=%d", resources, synced, err, cache.lists)
	}
}

func TestListSyncedUpgradeResourcesRequiresClusterWideSyncForAllNamespaces(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "monitoring.coreos.com", Version: "v1", Resource: "prometheusrules"}
	cache := &fakeUpgradeResourceLister{synced: true, clusterWideSynced: false}
	resources, synced, err := listSyncedUpgradeResources(cache, gvr, nil)
	if err != nil || synced || resources != nil || cache.lists != 1 {
		t.Fatalf("partial cluster cache result = resources=%v synced=%v err=%v lists=%d", resources, synced, err, cache.lists)
	}
}

func TestParseDeprecatedAPIRequests(t *testing.T) {
	raw := []byte(`# HELP apiserver_requested_deprecated_apis Gauge of deprecated APIs that have been requested.
# TYPE apiserver_requested_deprecated_apis gauge
apiserver_requested_deprecated_apis{group="flowcontrol.apiserver.k8s.io",removed_release="1.32",resource="flowschemas",subresource="status",version="v1beta3"} 1
apiserver_requested_deprecated_apis{group="",removed_release="1.25",resource="podsecuritypolicies",subresource="",version="v1beta1"} 0
# TYPE process_start_time_seconds gauge
process_start_time_seconds 1.721234567e+09
`)
	requests, startedAt, err := parseDeprecatedAPIRequests(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %+v", requests)
	}
	if got := startedAt.Unix(); got != 1721234567 {
		t.Fatalf("process start = %d, want 1721234567", got)
	}
	got := requests[0]
	if got.Group != "flowcontrol.apiserver.k8s.io" || got.Version != "v1beta3" || got.Resource != "flowschemas" || got.Subresource != "status" || got.RemovedRelease != "1.32" || got.Requests != 1 {
		t.Fatalf("request = %+v", got)
	}
}

func TestParseDeprecatedAPIRequestsMissingMetricIsObservedEmpty(t *testing.T) {
	requests, startedAt, err := parseDeprecatedAPIRequests([]byte("# EOF\n"))
	if err != nil {
		t.Fatal(err)
	}
	if requests == nil || len(requests) != 0 {
		t.Fatalf("requests = %#v, want observed empty slice", requests)
	}
	if !startedAt.IsZero() {
		t.Fatalf("process start = %v, want zero", startedAt)
	}
}
