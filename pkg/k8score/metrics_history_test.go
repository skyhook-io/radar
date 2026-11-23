package k8score

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"testing/synctest"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/rest"
	restfake "k8s.io/client-go/rest/fake"
	k8stesting "k8s.io/client-go/testing"
)

func TestMetricsHistoryStopCancelsPodList(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		podListStarted := make(chan struct{})
		httpClient := restfake.CreateHTTPClient(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != "/apis/metrics.k8s.io/v1beta1/pods" {
				t.Errorf("unexpected request: %s %s", req.Method, req.URL.Path)
				return nil, errors.New("unexpected request")
			}
			close(podListStarted)
			<-req.Context().Done()
			return nil, req.Context().Err()
		})
		client, err := dynamic.NewForConfigAndClient(&rest.Config{Host: "https://metrics.test"}, httpClient)
		if err != nil {
			t.Fatal(err)
		}
		store := NewMetricsHistoryStore(client)
		store.Start()
		<-podListStarted

		start := time.Now()
		store.Stop()
		store.wg.Wait()
		if elapsed := time.Since(start); elapsed != 0 {
			t.Errorf("Stop advanced the test clock by %s while the pod LIST was blocked", elapsed)
		}
		health := store.CollectionHealth()
		if health.PodMetrics != (MetricsSourceHealth{}) || health.NodeMetrics != (MetricsSourceHealth{}) {
			t.Errorf("cancelled poll updated source health: %+v", health)
		}
	})
}

func TestMetricsHistoryStopWhileResolving(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		resolving := make(chan struct{})
		release := make(chan struct{})
		client := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
		store := NewMetricsHistoryStoreWithResolver(client, func(string) (schema.GroupVersionResource, bool) {
			close(resolving)
			<-release
			return schema.GroupVersionResource{}, false
		})
		store.OnError = func(_, _, format string, args ...any) { t.Errorf(format, args...) }
		store.Start()
		<-resolving
		before := store.CollectionHealth()
		stopped := make(chan struct{})
		go func() {
			store.Stop()
			store.Stop()
			close(stopped)
		}()
		synctest.Wait()
		select {
		case <-stopped:
		default:
			t.Error("Stop waited for the blocked metrics resolver")
		}
		close(release)
		store.wg.Wait()
		if after := store.CollectionHealth(); after != before {
			t.Errorf("released resolver changed health from %+v to %+v", before, after)
		}
	})
}

func TestMetricsHistoryResolvesServedVersionLazily(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: MetricsAPIGroup, Version: "v1", Resource: "pods"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "PodMetricsList"},
	)
	dyn.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetResource() != gvr {
			t.Fatalf("metrics collection used %v, want %v", action.GetResource(), gvr)
		}
		return true, &unstructured.UnstructuredList{}, nil
	})
	ready := false
	store := NewMetricsHistoryStoreWithResolver(dyn, func(resource string) (schema.GroupVersionResource, bool) {
		if !ready || resource != "pods" {
			return schema.GroupVersionResource{}, false
		}
		return gvr, true
	})

	store.collectPodMetrics(context.Background(), time.Now())
	if store.lastPodError != ErrMetricsAPINotDiscovered.Error() {
		t.Fatalf("initial error = %q", store.lastPodError)
	}

	ready = true
	store.collectPodMetrics(context.Background(), time.Now())
	if store.consecutivePodErrors != 0 || store.lastPodError != "" {
		t.Fatalf("collection did not recover after discovery: errors=%d last=%q", store.consecutivePodErrors, store.lastPodError)
	}
}

func TestMetricsHistoryWarnsOnConfirmedMiss(t *testing.T) {
	pending := true
	store := NewMetricsHistoryStoreWithPollResolver(dynamicfake.NewSimpleDynamicClient(runtime.NewScheme()), func(string) MetricsPollDiscovery {
		return MetricsPollDiscovery{RefreshPending: pending}
	})
	defer store.Stop()
	var warnings []string
	store.OnError = func(source, level, format string, args ...any) {
		if source != "metrics" || level != "warning" {
			t.Errorf("diagnostic source %q, level %q", source, level)
		}
		warnings = append(warnings, fmt.Sprintf(format, args...))
	}
	store.collectMetrics()
	if len(warnings) != 0 {
		t.Fatalf("pending refresh emitted warnings: %v", warnings)
	}
	pending = false
	store.collectMetrics()
	store.collectMetrics()
	want := []string{
		"pod metrics collection failed (count=1): metrics API is not discovered",
		"node metrics collection failed (count=1): metrics API is not discovered",
	}
	if !slices.Equal(warnings, want) {
		t.Errorf("warnings = %v, want %v", warnings, want)
	}
	health := store.CollectionHealth()
	for _, source := range []MetricsSourceHealth{health.PodMetrics, health.NodeMetrics} {
		if source.LastPollDeferred || source.ConsecutiveErrors != 2 || !MetricsAPIUnavailable(fmt.Errorf("failed to get metrics: %s", source.LastError)) {
			t.Errorf("history should report the metrics API as unavailable: %+v", source)
		}
	}
}

func TestMetricsCollectionErrorLevel(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want string
	}{
		{
			name: "api not found",
			err:  apierrors.NewNotFound(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "api"),
			want: "warning",
		},
		{
			name: "metrics api resource absent",
			err:  errors.New("the server could not find the requested resource (get pods.metrics.k8s.io)"),
			want: "warning",
		},
		{
			name: "metrics kind not matched",
			err:  errors.New("no matches for kind PodMetrics in version metrics.k8s.io/v1beta1"),
			want: "warning",
		},
		{
			name: "metrics not available",
			err:  errors.New("pods.metrics.k8s.io not available"),
			want: "warning",
		},
		{
			name: "no metrics known",
			err:  errors.New("no metrics known for pod api in pods.metrics.k8s.io"),
			want: "warning",
		},
		{
			name: "unable to fetch metrics",
			err:  errors.New("unable to fetch metrics from pods.metrics.k8s.io"),
			want: "warning",
		},
		{
			name: "metrics APIService unavailable",
			err:  errors.New("the server is currently unable to handle the request (get pods.metrics.k8s.io)"),
			want: "warning",
		},
		{
			name: "non metrics missing resource",
			err:  errors.New("the server could not find the requested resource"),
			want: "error",
		},
		{
			name: "forbidden metrics",
			err:  errors.New("pods.metrics.k8s.io is forbidden"),
			want: "error",
		},
		{
			name: "nil",
			err:  nil,
			want: "error",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := metricsCollectionErrorLevel(tt.err); got != tt.want {
				t.Fatalf("metricsCollectionErrorLevel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestMetricsPollDefersOnlyTheAffectedSource(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
			PodMetricsGVR: "PodMetricsList", NodeMetricsGVR: "NodeMetricsList",
		})
		known := map[string]schema.GroupVersionResource{"pods": PodMetricsGVR, "nodes": NodeMetricsGVR}
		pending := false
		store := NewMetricsHistoryStoreWithPollResolver(dyn, func(resource string) MetricsPollDiscovery {
			return MetricsPollDiscovery{GVR: known[resource], RefreshPending: pending}
		})
		defer store.Stop()
		store.collectMetrics()
		before := store.CollectionHealth()

		delete(known, "pods")
		pending = true
		time.Sleep(MetricsPollInterval)
		store.collectMetrics()
		health := store.CollectionHealth()
		if !health.PodMetrics.LastPollDeferred || health.PodMetrics.Collecting || health.PodMetrics.ConsecutiveErrors != 0 ||
			health.PodMetrics.SuccessfulCollections != 1 || health.PodMetrics.LastSuccess != before.PodMetrics.LastSuccess {
			t.Fatalf("pod GVR missing after success: %+v", health.PodMetrics)
		}
		if !health.NodeMetrics.Collecting || health.NodeMetrics.SuccessfulCollections != 2 || health.NodeMetrics.LastSuccess == before.NodeMetrics.LastSuccess {
			t.Fatalf("known node GVR: %+v", health.NodeMetrics)
		}

		known["pods"] = PodMetricsGVR
		dyn.PrependReactor("list", "nodes", func(k8stesting.Action) (bool, runtime.Object, error) {
			return true, nil, apierrors.NewNotFound(NodeMetricsGVR.GroupResource(), "")
		})
		time.Sleep(MetricsPollInterval)
		store.collectMetrics()
		after := store.CollectionHealth()
		if after.PodMetrics.LastPollDeferred || !after.PodMetrics.Collecting || after.PodMetrics.SuccessfulCollections != 2 {
			t.Fatalf("resumed pod collection: %+v", after.PodMetrics)
		}
		if !after.NodeMetrics.LastPollDeferred || after.NodeMetrics.Collecting || after.NodeMetrics.ConsecutiveErrors != 0 ||
			after.NodeMetrics.SuccessfulCollections != 2 || after.NodeMetrics.LastSuccess != health.NodeMetrics.LastSuccess {
			t.Fatalf("node LIST returned 404 during refresh: %+v", after.NodeMetrics)
		}

		pending = false
		store.collectMetrics()
		if node := store.CollectionHealth().NodeMetrics; node.LastPollDeferred || node.ConsecutiveErrors != 1 || node.LastError == "" {
			t.Fatalf("node LIST returned 404 with fresh discovery: %+v", node)
		}
	})
}

func TestParseCPU(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"137492n", 137492},
		{"188u", 188000},
		{"250m", 250000000},
		{"1", 1000000000},
		{"1.5", 1500000000},
		{"1k", 1000000000000},
		{"garbage", 0},
	}

	for _, tt := range tests {
		if got := parseCPU(tt.input); got != tt.want {
			t.Errorf("parseCPU(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestParseMemory(t *testing.T) {
	tests := []struct {
		input string
		want  int64
	}{
		{"", 0},
		{"1024", 1024},
		{"966240Ki", 966240 * 1024},
		{"1500k", 1500 * 1000},
		{"1.5Gi", 3 * 512 * 1024 * 1024},
		{"400m", 1},
		{"garbage", 0},
	}

	for _, tt := range tests {
		if got := parseMemory(tt.input); got != tt.want {
			t.Errorf("parseMemory(%q) = %d, want %d", tt.input, got, tt.want)
		}
	}
}

func TestGetAllNodeMetricsLatestPreservesSampleTimestamp(t *testing.T) {
	first := time.Date(2026, time.July, 13, 8, 0, 0, 0, time.UTC)
	latest := first.Add(30 * time.Second)
	buffer := newRingBuffer(MetricsHistorySize)
	buffer.Add(MetricsDataPoint{Timestamp: first, CPU: 100, Memory: 200})
	buffer.Add(MetricsDataPoint{Timestamp: latest, CPU: 300, Memory: 400})
	store := &MetricsHistoryStore{
		nodeMetrics: map[string]*nodeMetricsBuffer{
			"node-a": {name: "node-a", buffer: buffer},
		},
	}

	got := store.GetAllNodeMetricsLatest()
	if len(got) != 1 {
		t.Fatalf("latest node metrics count = %d, want 1", len(got))
	}
	if !got[0].ObservedAt.Equal(latest) {
		t.Fatalf("observedAt = %s, want sample timestamp %s", got[0].ObservedAt, latest)
	}
	if got[0].CPU != 300 || got[0].Memory != 400 {
		t.Fatalf("latest node metrics = %+v, want CPU=300 Memory=400", got[0])
	}
}

func TestMetricsHistoryListsPodMetricsPerNamespace(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: MetricsAPIGroup, Version: "v1beta1", Resource: "pods"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "PodMetricsList"},
	)
	dyn.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		namespace := action.GetNamespace()
		if namespace == "" || namespace == "denied" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", errors.New("list denied"))
		}
		item := unstructured.Unstructured{Object: map[string]any{
			"containers": []any{map[string]any{"name": "app", "usage": map[string]any{"cpu": "5m", "memory": "1Mi"}}},
		}}
		item.SetNamespace(namespace)
		item.SetName("web")
		return true, &unstructured.UnstructuredList{Items: []unstructured.Unstructured{item}}, nil
	})
	store := NewMetricsHistoryStoreWithResolver(dyn, func(string) (schema.GroupVersionResource, bool) { return gvr, true })

	store.collectPodMetrics(context.Background(), time.Now())
	if store.consecutivePodErrors != 1 {
		t.Fatalf("cluster-wide collection: errors=%d, want the forbidden list to fail", store.consecutivePodErrors)
	}

	store.Namespaces = func() []string { return []string{"app", "billing"} }
	store.collectPodMetrics(context.Background(), time.Now())
	if store.consecutivePodErrors != 0 || store.lastPodError != "" {
		t.Fatalf("per-namespace collection failed: errors=%d last=%q", store.consecutivePodErrors, store.lastPodError)
	}
	for _, key := range []string{"app/web", "billing/web"} {
		if store.podMetrics[key] == nil {
			t.Errorf("no metrics collected for %s", key)
		}
	}
	if len(store.podMetrics) != 2 {
		t.Errorf("collected %d pods, want 2", len(store.podMetrics))
	}

	store.Namespaces = func() []string { return []string{"app", "denied"} }
	later := time.Now().Add(time.Minute)
	store.collectPodMetrics(context.Background(), later)
	if store.consecutivePodErrors != 0 || store.lastPodError != "" {
		t.Fatalf("one failed namespace: errors=%d last=%q, want it kept out of the shared collection error", store.consecutivePodErrors, store.lastPodError)
	}
	if store.partialPodErrors != 1 {
		t.Errorf("partial failures = %d, want 1", store.partialPodErrors)
	}
	if !store.podMetrics["app/web"].lastSeen.Equal(later) {
		t.Errorf("app/web was not recorded when another namespace failed")
	}

	// Every namespace failing: the error shown with any pod's history and
	// the diagnostic must not name a namespace.
	var reported string
	store.OnError = func(_, _ string, format string, args ...any) {
		reported = fmt.Sprintf(format, args...)
	}
	store.Namespaces = func() []string { return []string{"denied"} }
	store.collectPodMetrics(context.Background(), later.Add(time.Minute))
	if store.consecutivePodErrors != 1 {
		t.Fatalf("errors = %d, want the failed collection counted", store.consecutivePodErrors)
	}
	for _, shown := range []string{store.lastPodError, reported} {
		if strings.Contains(shown, "denied") {
			t.Errorf("collection error names a namespace: %q", shown)
		}
	}
}

// A namespace-limited install whose metrics API is down fails in every
// namespace. The stored error must still read as "metrics unavailable" (it is
// classified by its text) and must not name a namespace.
func TestMetricsHistoryAllNamespacesUnavailableStaysClassified(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: MetricsAPIGroup, Version: "v1beta1", Resource: "pods"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(
		runtime.NewScheme(),
		map[schema.GroupVersionResource]string{gvr: "PodMetricsList"},
	)
	dyn.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewServiceUnavailable("the server is currently unable to handle the request")
	})
	store := NewMetricsHistoryStoreWithResolver(dyn, func(string) (schema.GroupVersionResource, bool) { return gvr, true })
	store.Namespaces = func() []string { return []string{"app", "billing"} }
	store.collectPodMetrics(context.Background(), time.Now())

	if store.lastPodError == "" {
		t.Fatal("no collection error recorded")
	}
	for _, ns := range []string{"app", "billing"} {
		if strings.Contains(store.lastPodError, ns) {
			t.Errorf("collection error names namespace %q: %q", ns, store.lastPodError)
		}
	}
	if !MetricsAPIUnavailable(fmt.Errorf("failed to get pod metrics: %s", store.lastPodError)) {
		t.Errorf("stored error %q is not recognised as metrics unavailable", store.lastPodError)
	}
}

func TestMetricsPollDuringRefreshWithNamespaced404s(t *testing.T) {
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{
		PodMetricsGVR: "PodMetricsList",
	})
	dyn.PrependReactor("list", "pods", func(action k8stesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "denied" {
			return true, nil, apierrors.NewForbidden(PodMetricsGVR.GroupResource(), "", errors.New("list denied"))
		}
		return true, nil, apierrors.NewNotFound(PodMetricsGVR.GroupResource(), "")
	})
	store := NewMetricsHistoryStoreWithPollResolver(dyn, func(string) MetricsPollDiscovery {
		return MetricsPollDiscovery{GVR: PodMetricsGVR, RefreshPending: true}
	})
	defer store.Stop()
	namespaces := []string{"a", "b"}
	store.Namespaces = func() []string { return namespaces }
	store.collectPodMetrics(context.Background(), time.Now())
	if got := store.CollectionHealth().PodMetrics; got != (MetricsSourceHealth{LastPollDeferred: true}) {
		t.Fatalf("all LISTs returned 404: %+v", got)
	}
	namespaces = append(namespaces, "denied")
	store.collectPodMetrics(context.Background(), time.Now())
	if got := store.CollectionHealth().PodMetrics; got.LastPollDeferred || got.ConsecutiveErrors != 1 || strings.Contains(got.LastError, "denied") {
		t.Fatalf("a 404 and a forbidden LIST: %+v", got)
	}
}
