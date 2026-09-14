package prometheus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestHistoricalOwnershipStrategy(t *testing.T) {
	for _, tc := range []struct {
		name                      string
		raw, recorded             []int64
		wantRecorded, wantMissing bool
	}{
		{"equivalent prefers raw", []int64{100, 160, 220}, []int64{100, 160, 220}, false, false},
		{"rule starts halfway", []int64{100, 160, 220}, []int64{220}, false, false},
		{"raw retained less than rule", []int64{220}, []int64{100, 160, 220}, true, false},
		{"rule only", nil, []int64{100}, true, false},
		{"raw gaps rule complete", []int64{100, 220}, []int64{100, 160, 220}, true, false},
		{"no ownership", nil, nil, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := workloadQueryFunc(func(_ context.Context, query string, _, _ time.Time, _ time.Duration) (*prom.QueryResult, error) {
				if !strings.Contains(query, `namespace="shop"`) || !strings.Contains(query, `cluster="west"`) {
					t.Fatalf("unscoped ownership: %s", query)
				}
				timestamps := tc.raw
				if strings.Contains(query, "namespace_workload_pod:") {
					timestamps = tc.recorded
				}
				series := prom.Series{}
				for _, at := range timestamps {
					series.DataPoints = append(series.DataPoints, prom.DataPoint{Timestamp: at, Value: 2})
				}
				return &prom.QueryResult{Series: []prom.Series{series}}, nil
			})
			plan := chooseWorkloadHistory(context.Background(), q, PodScope{Kind: "Deployment", Namespace: "shop", Name: "api"}, prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "west"}}, time.Unix(100, 0), time.Unix(220, 0), time.Minute)
			if plan.err != nil || (plan.history == nil) != tc.wantMissing || plan.history != nil && plan.history.Recorded != tc.wantRecorded {
				t.Fatalf("wrong selection: %+v", plan)
			}
			if tc.wantMissing && !strings.Contains(plan.reason, "ownership") {
				t.Fatal("missing prerequisite explanation")
			}
		})
	}
}

func TestHistoricalPartitionRequiresExternalAnchor(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	for _, labeled := range []bool{false, true} {
		row := prom.Series{Labels: map[string]string{"namespace": "shop", "pod": pod.Name, "uid": pod.UID}}
		if labeled {
			row.Labels["cluster"] = "west"
		}
		client := prom.NewClient(attributionTransport(func(query string) ([]byte, error) {
			if !strings.Contains(query, `namespace="shop"`) {
				t.Fatal("namespace omitted from anchor probe")
			}
			return attributionEvidence([]prom.Series{row}, ""), nil
		}))
		config, err := probeHistoricalPartition(context.Background(), client, "shop", []prom.WorkloadPodIdentity{pod})
		if err != nil || (len(config.ClusterLabels) > 0) != labeled {
			t.Fatalf("invalid partition proof: %+v %v", config, err)
		}
		if !labeled {
			q := workloadQueryFunc(func(context.Context, string, time.Time, time.Time, time.Duration) (*prom.QueryResult, error) {
				t.Fatal("unverified historical query sent")
				return nil, nil
			})
			plan := chooseWorkloadHistory(context.Background(), q, PodScope{Kind: "Deployment", Namespace: "shop", Name: "api"}, config, time.Now(), time.Now(), time.Minute)
			if plan.history != nil || !strings.Contains(plan.reason, "--prometheus-single-cluster") {
				t.Fatal("missing explicit current-only remedy")
			}
		}
	}
}

func TestHistoricalPartitionMemoScopeAndExpiry(t *testing.T) {
	config := prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "private-cluster-value"}}
	c := &Client{discoveryGen: 3, workloadPartition: &workloadPartitionMemo{generation: 3, expires: time.Now().Add(time.Minute), scope: config}}
	for _, namespace := range []string{"original", "other-authorized"} {
		got, err := c.historicalClusterScope(context.Background(), PodScope{Namespace: namespace}, nil)
		if err != nil || got.ClusterLabels["cluster"] != config.ClusterLabels["cluster"] {
			t.Fatal("valid proof not reused")
		}
		h := prom.WorkloadHistory{Kind: "Deployment", Namespace: namespace, Name: "api", Scope: got}
		query, _ := prom.BuildHistoryResourceQuery(time.Minute, h, prom.CategoryCPU)
		if !strings.Contains(query, `namespace="`+namespace+`"`) {
			t.Fatalf("request namespace lost: %s", query)
		}
	}
	c.workloadPartition.expires = time.Now().Add(-time.Second)
	got, err := c.historicalClusterScope(context.Background(), PodScope{Namespace: "empty"}, nil)
	if err != nil || len(got.ClusterLabels) > 0 {
		t.Fatal("expired proof reused at cold zero")
	}
	c.workloadPartition.expires = time.Now().Add(time.Minute)
	c.discoveryGen++
	got, _ = c.historicalClusterScope(context.Background(), PodScope{Namespace: "empty"}, nil)
	if len(got.ClusterLabels) > 0 {
		t.Fatal("proof crossed connection generation")
	}
	c.cancelWorkloadAttributionsLocked()
	if c.workloadPartition != nil {
		t.Fatal("connection reset retained proof")
	}
}

func TestHistoryProbesPartitionDespiteUIDAttribution(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	var probed atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("query") != "up" && !strings.Contains(r.Form.Get("query"), "kube_pod_info") {
			t.Errorf("unexpected query: %s", r.Form.Get("query"))
		}
		if strings.Contains(r.Form.Get("query"), "kube_pod_info") {
			probed.Store(true)
		}
		_, _ = w.Write(attributionEvidence([]prom.Series{{Labels: map[string]string{"namespace": "shop", "pod": pod.Name, "uid": pod.UID, "cluster": "west"}}}, ""))
	}))
	defer server.Close()
	c := &Client{baseURL: server.URL, httpClient: server.Client(), workloadAttributionEntries: map[string]*workloadAttributionEntry{"shop/Deployment/api": {result: workloadAttributions{"cpu": {UID: true, Pods: []prom.WorkloadPodIdentity{pod}}}}}}
	got, err := c.historicalClusterScope(context.Background(), PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{pod}}, nil)
	if err != nil || !probed.Load() || got.ClusterLabels["cluster"] != "west" {
		t.Fatalf("history skipped partition after UID attribution: %+v %v", got, err)
	}
}

func TestHistoricalCollectionAtZeroAndBeyondCurrentCap(t *testing.T) {
	for _, total := range []int{0, 128} {
		history := prom.WorkloadHistory{Kind: "StatefulSet", Namespace: "shop", Name: "api", Scope: prom.WorkloadMetricsScope{ClusterLabels: map[string]string{"cluster": "west"}}}
		scope := PodScope{Kind: history.Kind, Namespace: history.Namespace, Name: history.Name, CurrentTotal: total}
		if total > 0 {
			scope.CurrentPods = []string{"api-0"}
			scope.Selection = prom.SelectPods("shop", scope.CurrentPods)
		}
		q := workloadQueryFunc(func(_ context.Context, query string, _, _ time.Time, _ time.Duration) (*prom.QueryResult, error) {
			result := &prom.QueryResult{Series: []prom.Series{}}
			if strings.Contains(query, "container_memory_working_set_bytes") && strings.Contains(query, "kube_pod_owner") {
				if strings.HasPrefix(query, "count(count") {
					result.Series = []prom.Series{{DataPoints: []prom.DataPoint{{Timestamp: 100, Value: 1}}}}
				} else {
					result.Series = []prom.Series{{Labels: map[string]string{"aggregation": "Workload"}, DataPoints: []prom.DataPoint{{Timestamp: 100, Value: 128}}}}
				}
			}
			return result, nil
		})
		resp := collectWorkloadMetricsWithHistory(context.Background(), q, scope, prom.WorkloadMetricsScope{}, "", "", time.Unix(100, 0), time.Unix(160, 0), time.Minute, nil, workloadHistoryPlan{history: &history})
		if resp.History["memory"].Mode != "workload-history" || len(resp.Panels["memory"].Series) != 1 || resp.Panels["memory"].Series[0].DataPoints[0].Value != 128 || resp.State != "available" {
			t.Fatalf("lost history with %d current Pods: %+v", total, resp)
		}
		for _, description := range resp.Attribution {
			if strings.Contains(description, "Operator-asserted") {
				t.Fatal("automatic history mislabeled as operator assertion")
			}
			if strings.Contains(description, "west") {
				t.Fatal("partition value leaked")
			}
		}
	}
}

func TestHistoryLookupFailureDoesNotChangeScope(t *testing.T) {
	q := workloadQueryFunc(func(context.Context, string, time.Time, time.Time, time.Duration) (*prom.QueryResult, error) {
		return nil, errors.New("private upstream message")
	})
	scope := PodScope{Kind: "Deployment", Namespace: "shop", Name: "api"}
	plan := chooseWorkloadHistory(context.Background(), q, scope, prom.WorkloadMetricsScope{SingleCluster: true}, time.Now(), time.Now(), time.Minute)
	scope.CurrentPods, scope.CurrentTotal = []string{"api-0"}, 1
	scope.Selection = prom.SelectPods("shop", scope.CurrentPods)
	current := workloadQueryFunc(func(context.Context, string, time.Time, time.Time, time.Duration) (*prom.QueryResult, error) {
		return &prom.QueryResult{Series: []prom.Series{{Labels: map[string]string{"pod": "api-0"}, DataPoints: []prom.DataPoint{{Timestamp: 100, Value: 1}}}}}, nil
	})
	resp := collectWorkloadMetricsWithHistory(context.Background(), current, scope, prom.WorkloadMetricsScope{SingleCluster: true}, "", "", time.Unix(40, 0), time.Unix(100, 0), time.Minute, nil, plan)
	if resp.History["cpu"].Mode != "unavailable" || resp.Panels["cpu"].State != "error" || len(resp.Panels["cpu"].Series) != 0 || strings.Contains(resp.History["cpu"].Reason, "private") {
		t.Fatalf("failure became fallback: %+v", resp)
	}
	if resp.Comparison["cpu"].State != "available" || resp.History["requests"].Mode != "current-pods" || resp.Panels["requests"].State != "available" {
		t.Fatalf("independent current observations lost: %+v", resp)
	}
}

func TestHistoricalPartitionSurvivesLeaderCancellation(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "anchor", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	started, release := make(chan struct{}), make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if strings.Contains(r.Form.Get("query"), "kube_pod_info") {
			close(started)
			select {
			case <-release:
			case <-r.Context().Done():
				return
			}
		}
		_, _ = w.Write(attributionEvidence([]prom.Series{{Labels: map[string]string{"namespace": "shop", "pod": pod.Name, "uid": pod.UID, "cluster": "west"}}}, ""))
	}))
	defer server.Close()
	c := &Client{baseURL: server.URL, httpClient: server.Client()}
	cache := scopeTestCache(t, map[string]bool{"pods": true}, &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: pod.Name, Namespace: "shop", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.historicalClusterScope(ctx, PodScope{Namespace: "shop"}, cache)
		done <- err
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		cancel()
		close(release)
		t.Fatal("cold zero did not use authorized namespace anchor")
	}
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader cancellation not returned: %v", err)
	}
	close(release)
	got, err := c.historicalClusterScope(context.Background(), PodScope{Namespace: "shop"}, cache)
	if err != nil || got.ClusterLabels["cluster"] != "west" {
		t.Fatalf("leader cancellation poisoned shared proof: %+v %v", got, err)
	}
}
