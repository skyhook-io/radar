package server

import (
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

type fakeUpgradeResourceLister struct {
	synced            bool
	clusterWideSynced bool
	lists             int
	resources         []*unstructured.Unstructured
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
`)
	requests, err := parseDeprecatedAPIRequests(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 2 {
		t.Fatalf("requests = %+v", requests)
	}
	got := requests[0]
	if got.Group != "flowcontrol.apiserver.k8s.io" || got.Version != "v1beta3" || got.Resource != "flowschemas" || got.Subresource != "status" || got.RemovedRelease != "1.32" || got.Requests != 1 {
		t.Fatalf("request = %+v", got)
	}
}

func TestParseDeprecatedAPIRequestsMissingMetricIsObservedEmpty(t *testing.T) {
	requests, err := parseDeprecatedAPIRequests([]byte("# EOF\n"))
	if err != nil {
		t.Fatal(err)
	}
	if requests == nil || len(requests) != 0 {
		t.Fatalf("requests = %#v, want observed empty slice", requests)
	}
}
