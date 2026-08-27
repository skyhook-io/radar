package upgrade

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/kubernetes"
	fakeclientset "k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/upgradereadiness"
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

func TestParseUpgradeNodeConfigPreservesExplicitZero(t *testing.T) {
	raw := []byte(`{"kubeletconfig":{"eventRecordQPS":0,"featureGates":{"PreventStaticPodAPIReferences":false,"SidecarContainers":true}}}`)
	got, err := parseUpgradeNodeConfig(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ConfigAvailable || !got.EventRecordQPSAvailable || got.EventRecordQPS != 0 {
		t.Fatalf("config evidence = %+v, want observed explicit zero", got)
	}
	if got.FeatureGates["PreventStaticPodAPIReferences"] || !got.FeatureGates["SidecarContainers"] {
		t.Fatalf("feature gates = %+v", got.FeatureGates)
	}

	got, err = parseUpgradeNodeConfig([]byte(`{"kubeletconfig":{"featureGates":{}}}`))
	if err != nil || !got.ConfigAvailable || got.EventRecordQPSAvailable {
		t.Fatalf("missing eventRecordQPS = %+v err=%v, want available config with absent field", got, err)
	}

	if _, err = parseUpgradeNodeConfig([]byte(`{"other":{}}`)); err == nil {
		t.Fatal("configz payload without kubeletconfig must fail")
	}
}

func TestParseUpgradeNodeMetricsIncludesSELinuxEvidence(t *testing.T) {
	raw := []byte(`# TYPE kubelet_cgroup_version gauge
kubelet_cgroup_version 2
# TYPE kubelet_cri_losing_support gauge
kubelet_cri_losing_support{version="1.37"} 1
# TYPE volume_manager_selinux_volume_context_mismatch_warnings_total counter
volume_manager_selinux_volume_context_mismatch_warnings_total{access_mode="ReadWriteMany"} 2
volume_manager_selinux_volume_context_mismatch_warnings_total{access_mode="ReadWriteOnce"} 3
# TYPE volume_manager_selinux_volume_context_mismatch_errors_total counter
volume_manager_selinux_volume_context_mismatch_errors_total{access_mode="ReadWriteMany"} 1
`)
	got, err := parseUpgradeNodeMetrics(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !got.MetricsAvailable || !got.CgroupVersionAvailable || got.CgroupVersion != 2 || !got.CRILosingSupportAvailable || got.CRILosingSupportVersion != "1.37" || got.SELinuxMismatchWarnings != 5 || got.SELinuxMismatchErrors != 1 {
		t.Fatalf("metrics evidence = %+v", got)
	}
}

func TestMergeNodeRuntimeEvidenceKeepsIndependentSources(t *testing.T) {
	target := upgradereadiness.NodeRuntimeEvidence{NodeName: "node-a"}
	mergeNodeRuntimeEvidence(&target, upgradereadiness.NodeRuntimeEvidence{MetricsAvailable: true, CgroupVersionAvailable: true, CgroupVersion: 2, SELinuxMismatchWarnings: 4})
	mergeNodeRuntimeEvidence(&target, upgradereadiness.NodeRuntimeEvidence{ConfigAvailable: true, EventRecordQPSAvailable: true, EventRecordQPS: 50, FeatureGates: map[string]bool{"AnyVolumeDataSource": true}})
	if !target.MetricsAvailable || !target.ConfigAvailable || target.CgroupVersion != 2 || target.EventRecordQPS != 50 || target.SELinuxMismatchWarnings != 4 || !target.FeatureGates["AnyVolumeDataSource"] {
		t.Fatalf("merged evidence = %+v", target)
	}
}

func TestCollectUpgradeNodeRuntimeEvidenceClassifiesObservedForbidden(t *testing.T) {
	requestedPath := ""
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.Path
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","message":"forbidden","reason":"Forbidden","code":403}`))
	}))
	t.Cleanup(server.Close)

	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	nodes := []*corev1.Node{{ObjectMeta: metav1.ObjectMeta{Name: "node-a"}, Status: corev1.NodeStatus{NodeInfo: corev1.NodeSystemInfo{OperatingSystem: "linux"}}}}
	evidence, forbidden := collectUpgradeNodeRuntimeEvidenceWithClient(t.Context(), client.CoreV1().RESTClient(), nodes, false)
	if !forbidden || len(evidence) != 1 || evidence[0].NodeName != "node-a" || evidence[0].MetricsAvailable {
		t.Fatalf("node runtime result = evidence=%+v forbidden=%v, want observed forbidden with unavailable metrics", evidence, forbidden)
	}
	if requestedPath != "/api/v1/nodes/node-a/proxy/metrics" {
		t.Fatalf("request path = %q, want node metrics proxy path", requestedPath)
	}
}
