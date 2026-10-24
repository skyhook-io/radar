package upgrade

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestReadAPIMetricsEvidenceKeepsOnlyDeprecatedAPISeries(t *testing.T) {
	body := `# HELP apiserver_request_duration_seconds Response latency distribution.
# TYPE apiserver_request_duration_seconds histogram
apiserver_request_duration_seconds_bucket{verb="GET",le="0.05"} 12
# HELP apiserver_requested_deprecated_apis_total Not the series we read.
apiserver_requested_deprecated_apis_total{removed_release="1.25"} 9
# HELP apiserver_requested_deprecated_apis Gauge of deprecated APIs that have been requested.
# TYPE apiserver_requested_deprecated_apis gauge
apiserver_requested_deprecated_apis{group="",removed_release="1.25",resource="podsecuritypolicies",subresource="",version="v1beta1"} 3
# TYPE process_start_time_seconds gauge
process_start_time_seconds 1.721234567e+09
process_start_time_seconds_extra 7
`
	raw, err := readAPIMetricsEvidence(io.NopCloser(bytes.NewBufferString(body)), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte("request_duration")) || bytes.Contains(raw, []byte("_total")) || bytes.Contains(raw, []byte("_extra")) {
		t.Fatalf("unrelated series kept:\n%s", raw)
	}
	requests, startedAt, err := parseDeprecatedAPIRequests(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Resource != "podsecuritypolicies" || requests[0].Requests != 3 {
		t.Fatalf("requests = %+v", requests)
	}
	if got := startedAt.Unix(); got != 1721234567 {
		t.Fatalf("process start = %d, want 1721234567", got)
	}
}

func TestReadAPIMetricsEvidenceHandlesBodiesOverSixteenMiB(t *testing.T) {
	var body bytes.Buffer
	filler := "apiserver_request_duration_seconds_bucket{verb=\"GET\",resource=\"pods\",le=\"0.05\"} 12\n"
	for body.Len() < 17<<20 {
		body.WriteString(filler)
	}
	body.WriteString("apiserver_requested_deprecated_apis{group=\"\",removed_release=\"1.25\",resource=\"podsecuritypolicies\",subresource=\"\",version=\"v1beta1\"} 1\n")
	raw, err := readAPIMetricsEvidence(io.NopCloser(&body), upgradeAPIMetricsResponseLimit)
	if err != nil {
		t.Fatal(err)
	}
	requests, _, err := parseDeprecatedAPIRequests(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 {
		t.Fatalf("requests = %+v, want the one deprecated series", requests)
	}
}

func TestReadAPIMetricsEvidenceSkipsLongUnrelatedLines(t *testing.T) {
	const deprecated = "apiserver_requested_deprecated_apis{group=\"\",removed_release=\"1.25\",resource=\"podsecuritypolicies\",subresource=\"\",version=\"v1beta1\"} 2"
	body := "unrelated_metric{label=\"" + strings.Repeat("x", 2<<20) + "\"} 1\n" +
		"# TYPE process_start_time_seconds gauge\nprocess_start_time_seconds 1.721234567e+09\n" +
		deprecated
	raw, err := readAPIMetricsEvidence(io.NopCloser(bytes.NewBufferString(body)), 1<<30)
	if err != nil {
		t.Fatal(err)
	}
	requests, startedAt, err := parseDeprecatedAPIRequests(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(requests) != 1 || requests[0].Requests != 2 {
		t.Fatalf("requests = %+v, want the deprecated series after the long line", requests)
	}
	if startedAt.Unix() != 1721234567 {
		t.Fatalf("process start = %d, want 1721234567", startedAt.Unix())
	}
}

func TestReadAPIMetricsEvidenceRejectsLongKeptLine(t *testing.T) {
	body := "apiserver_requested_deprecated_apis{group=\"" + strings.Repeat("x", 2*upgradeAPIMetricsLineBuffer) + "\"} 1\n"
	if _, err := readAPIMetricsEvidence(io.NopCloser(bytes.NewBufferString(body)), 1<<30); err == nil {
		t.Fatal("expected an error for an oversized line the check needs")
	}
}

func TestReadAPIMetricsEvidenceBoundsBytesScanned(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		wantErr bool
	}{
		{name: "at limit", body: "up 1\n"},
		{name: "over limit", body: "up 1\nx", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := readAPIMetricsEvidence(io.NopCloser(bytes.NewBufferString(tc.body)), 5)
			if (err != nil) != tc.wantErr {
				t.Fatalf("error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestReadAPIMetricsEvidenceWithoutDeprecatedSeriesIsObservedEmpty(t *testing.T) {
	raw, err := readAPIMetricsEvidence(io.NopCloser(bytes.NewBufferString("up 1\n")), 1<<20)
	if err != nil {
		t.Fatal(err)
	}
	requests, _, err := parseDeprecatedAPIRequests(raw)
	if err != nil {
		t.Fatal(err)
	}
	if requests == nil || len(requests) != 0 {
		t.Fatalf("requests = %#v, want observed empty slice", requests)
	}
}

func TestCollectDeprecatedAPIRequestsReadsLargeMetricsBody(t *testing.T) {
	const deprecated = "apiserver_requested_deprecated_apis{group=\"\",removed_release=\"1.25\",resource=\"podsecuritypolicies\",subresource=\"\",version=\"v1beta1\"} 4\n"
	filler := "apiserver_request_duration_seconds_bucket{verb=\"GET\",resource=\"pods\",le=\"0.05\"} 12\n"
	for _, tc := range []struct {
		name         string
		status       int
		bodyBytes    int
		wantRequests int
		wantWindow   bool
	}{
		{name: "body over 16 MiB", status: http.StatusOK, bodyBytes: 17 << 20, wantRequests: 1, wantWindow: true},
		{name: "body under 16 MiB", status: http.StatusOK, bodyBytes: 1 << 20, wantRequests: 1, wantWindow: true},
		{name: "forbidden", status: http.StatusForbidden},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var requestedPath string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requestedPath = r.URL.Path
				if tc.status != http.StatusOK {
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(tc.status)
					_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
					return
				}
				w.Header().Set("Content-Type", "text/plain; version=0.0.4")
				_, _ = io.WriteString(w, "# TYPE process_start_time_seconds gauge\nprocess_start_time_seconds 1.721234567e+09\n")
				for written := 0; written < tc.bodyBytes; written += len(filler) {
					_, _ = io.WriteString(w, filler)
				}
				_, _ = io.WriteString(w, deprecated)
			}))
			t.Cleanup(server.Close)

			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			requests, window, failure := collectDeprecatedAPIRequestsWithClient(t.Context(), client.Discovery().RESTClient())
			if requestedPath != "/metrics" {
				t.Fatalf("request path = %q, want /metrics", requestedPath)
			}
			if len(requests) != tc.wantRequests || (window != "") != tc.wantWindow {
				t.Fatalf("result = requests=%+v window=%q, want %d requests, window=%v", requests, window, tc.wantRequests, tc.wantWindow)
			}
			if tc.status == http.StatusForbidden && failure != upgradereadiness.APIMetricsForbidden {
				t.Fatalf("failure = %q, want forbidden", failure)
			}
			if tc.status != http.StatusOK && requests != nil {
				t.Fatalf("requests = %#v, want nil so the scan reports the metrics as unavailable", requests)
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

func TestAPIMetricsFailuresReachUpgradeCheck(t *testing.T) {
	for _, scenario := range []struct {
		name        string
		wantFailure upgradereadiness.APIMetricsFailure
		wantText    string
	}{
		{"response too large", upgradereadiness.APIMetricsTooLarge, "exceeded Radar's size limit"},
		{"required line too large", upgradereadiness.APIMetricsTooLarge, "exceeded Radar's size limit"},
		{"forbidden", upgradereadiness.APIMetricsForbidden, "Grant get"},
		{"unauthorized", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"truncated", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"malformed", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"HTML", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"metric then HTML", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"process metric then HTML", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"metric then malformed sample", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"malformed after validation batches", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"malformed metadata after sample", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"empty", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"canceled", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
		{"canceled during read", upgradereadiness.APIMetricsUnreadable, "could not read or parse"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if scenario.name == "canceled during read" {
					w.Header().Set("Content-Type", "text/plain; version=0.0.4")
					_, _ = io.WriteString(w, "process_start_time_seconds 1721234567\n")
					if err := http.NewResponseController(w).Flush(); err != nil {
						t.Error(err)
					}
					cancel()
					<-r.Context().Done()
					return
				}
				if scenario.name == "truncated" {
					hijacker, ok := w.(http.Hijacker)
					if !ok {
						t.Error("server cannot hijack connection")
						return
					}
					conn, buf, err := hijacker.Hijack()
					if err != nil {
						t.Error(err)
						return
					}
					defer conn.Close()
					_, _ = buf.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 9999\r\nContent-Type: text/plain\r\n\r\nprocess_start_time_seconds 1721234567\n")
					_ = buf.Flush()
					return
				}
				if scenario.name == "forbidden" || scenario.name == "unauthorized" {
					w.Header().Set("Content-Type", "application/json")
					if scenario.name == "unauthorized" {
						w.WriteHeader(http.StatusUnauthorized)
						_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Unauthorized","code":401}`)
					} else {
						w.WriteHeader(http.StatusForbidden)
						_, _ = io.WriteString(w, `{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`)
					}
					return
				}
				w.Header().Set("Content-Type", "text/plain; version=0.0.4")
				switch scenario.name {
				case "response too large":
					// Isolate the byte cap from parsing work and the collection deadline.
					chunk := strings.Repeat("# unrelated metrics metadata\n", 65536)
					for written := 0; written <= upgradeAPIMetricsResponseLimit; written += len(chunk) {
						if _, err := io.WriteString(w, chunk); err != nil {
							return
						}
					}
				case "required line too large":
					_, _ = io.WriteString(w, "apiserver_requested_deprecated_apis{group=\""+strings.Repeat("x", 2*upgradeAPIMetricsLineBuffer)+"\"} 1\n")
				case "malformed":
					_, _ = io.WriteString(w, "process_start_time_seconds not-a-number\n")
				case "HTML":
					_, _ = io.WriteString(w, "<html><body>Sign in</body></html>\n")
				case "metric then HTML":
					_, _ = io.WriteString(w, "up 1\n<html><body>Sign in</body></html>\n")
				case "process metric then HTML":
					_, _ = io.WriteString(w, "process_start_time_seconds 1721234567\n<html><body>Sign in</body></html>\n")
				case "metric then malformed sample":
					_, _ = io.WriteString(w, "up 1\nunrelated_metric not-a-number\n")
				case "malformed after validation batches":
					_, _ = io.WriteString(w, strings.Repeat("unrelated_metric 1\n", 10000)+"<html>Sign in</html>\n")
				case "malformed metadata after sample":
					_, _ = io.WriteString(w, "up 1\n# TYPE unrelated_metric invalid_type\n")
				}
			}))
			defer server.Close()
			client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
			if err != nil {
				t.Fatal(err)
			}
			if scenario.name == "canceled" {
				cancel()
			}
			requests, window, failure := collectDeprecatedAPIRequestsWithClient(ctx, client.Discovery().RESTClient())
			if requests != nil || window != "" || failure != scenario.wantFailure {
				t.Fatalf("requests=%v window=%q failure=%q, want unavailable with %q", requests, window, failure, scenario.wantFailure)
			}
			result, err := RunFromCache(nil, nil, Options{CurrentVersion: "1.35", TargetVersion: "1.36", DeprecatedAPIRequests: requests, DeprecatedAPIMetricsWindow: window, DeprecatedAPIMetricsFailure: failure})
			if err != nil {
				t.Fatal(err)
			}
			for _, check := range result.Checks {
				if check.ID != "deprecated-api-requests" {
					continue
				}
				if check.Status != upgradereadiness.CheckUnknown || !strings.Contains(check.Caveat, scenario.wantText) {
					t.Fatalf("unexpected check: %+v", check)
				}
				if failure != upgradereadiness.APIMetricsForbidden && strings.Contains(check.Caveat, "Grant") {
					t.Fatalf("non-permission failure suggests RBAC: %+v", check)
				}
				t.Logf("status=%s summary=%s caveat=%s", check.Status, check.Summary, check.Caveat)
				return
			}
			t.Fatal("deprecated API check missing")
		})
	}
}
