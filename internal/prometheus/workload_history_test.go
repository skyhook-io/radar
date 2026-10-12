package prometheus

import (
	"context"
	"errors"
	"fmt"
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

func TestHistoryFailureDoesNotTurnPendingAttributionIntoMissingMetrics(t *testing.T) {
	for _, state := range []string{"detecting", "error", "unavailable", "available"} {
		for _, historyErr := range []error{nil, context.DeadlineExceeded} {
			plan := workloadHistoryPlan{err: historyErr}
			if got := plan.awaitingAttribution(state); got != (state != "available") {
				t.Fatalf("state=%s historyErr=%v: waiting=%v", state, historyErr, got)
			}
			plan.history = &prom.WorkloadHistory{}
			if plan.awaitingAttribution(state) {
				t.Fatalf("usable history suppressed while attribution=%s", state)
			}
		}
	}
}

func TestHistoricalOwnershipStrategiesFailIndependently(t *testing.T) {
	rawErr, recordedErr := errors.New("raw lookup failed"), errors.New("recorded lookup failed")
	for _, tc := range []struct {
		name                      string
		rawErr, recordedErr       error
		empty                     bool
		wantRecorded, wantFailure bool
	}{
		{"raw failure uses rule", rawErr, nil, false, true, false},
		{"rule failure preserves raw", nil, recordedErr, false, false, false},
		{"raw timeout uses rule", context.DeadlineExceeded, nil, false, true, false},
		{"both fail", rawErr, recordedErr, false, false, true},
		{"raw fails rule empty", rawErr, nil, true, false, true},
		{"raw empty rule fails", nil, recordedErr, true, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			q := workloadQueryFunc(func(_ context.Context, query string, _, _ time.Time, _ time.Duration) (*prom.QueryResult, error) {
				calls++
				err := tc.rawErr
				if strings.Contains(query, "namespace_workload_pod:") {
					err = tc.recordedErr
				}
				if err != nil {
					return nil, err
				}
				result := &prom.QueryResult{}
				if !tc.empty {
					result.Series = []prom.Series{{DataPoints: []prom.DataPoint{{Timestamp: 100, Value: 1}}}}
				}
				return result, nil
			})
			plan := chooseWorkloadHistory(context.Background(), q, PodScope{Kind: "Deployment", Namespace: "shop", Name: "api"}, prom.WorkloadMetricsScope{SingleCluster: true}, time.Unix(100, 0), time.Unix(160, 0), time.Minute)
			if calls != 2 || (plan.err != nil) != tc.wantFailure || (plan.history == nil) != tc.wantFailure {
				t.Fatalf("calls=%d plan=%+v", calls, plan)
			}
			if plan.history != nil && plan.history.Recorded != tc.wantRecorded {
				t.Fatalf("wrong strategy: %+v", plan.history)
			}
			if tc.wantFailure {
				for _, err := range []error{tc.rawErr, tc.recordedErr} {
					if err != nil && !errors.Is(plan.err, err) {
						t.Fatalf("lost lookup error: %v", plan.err)
					}
				}
			}
		})
	}
}

func TestHistoricalOwnershipStopsOnCallerCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	calls := 0
	q := workloadQueryFunc(func(context.Context, string, time.Time, time.Time, time.Duration) (*prom.QueryResult, error) {
		calls++
		cancel()
		return nil, ctx.Err()
	})
	plan := chooseWorkloadHistory(ctx, q, PodScope{Kind: "Deployment", Namespace: "shop", Name: "api"}, prom.WorkloadMetricsScope{SingleCluster: true}, time.Now(), time.Now(), time.Minute)
	if calls != 1 || !errors.Is(plan.err, context.Canceled) || plan.history != nil {
		t.Fatalf("canceled lookup: calls=%d plan=%+v", calls, plan)
	}
}

func TestHistoricalPartitionDiscoveryHasSeparateBudget(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "030a7597-c1fc-48b0-9bb4-683489285358"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(attributionEvidence([]prom.Series{{Labels: map[string]string{"namespace": "shop", "pod": pod.Name, "uid": pod.UID, "cluster": "west"}}}, ""))
	}))
	defer server.Close()
	c := &Client{manualURL: server.URL, httpClient: server.Client()}
	select {
	case discoveryGate <- struct{}{}:
	default:
		t.Fatal("discovery gate busy")
	}
	release := time.AfterFunc(9*time.Second, func() { <-discoveryGate })
	defer func() {
		if release.Stop() {
			<-discoveryGate
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	got, err := c.historicalClusterScope(ctx, PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{pod}}, nil)
	if err != nil || got.ClusterLabels["cluster"] != "west" {
		t.Fatalf("discovery consumed proof budget: %+v %v", got, err)
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
		proof, err := probeHistoricalPartition(context.Background(), client, "shop", []prom.WorkloadPodIdentity{pod})
		config := proof.scope
		if err != nil || (len(config.ClusterLabels) > 0) != labeled {
			t.Fatalf("invalid partition proof: %+v %v", config, err)
		}
		if !labeled {
			q := workloadQueryFunc(func(context.Context, string, time.Time, time.Time, time.Duration) (*prom.QueryResult, error) {
				t.Fatal("unverified historical query sent")
				return nil, nil
			})
			plan := chooseWorkloadHistory(context.Background(), q, PodScope{Kind: "Deployment", Namespace: "shop", Name: "api"}, config, time.Now(), time.Now(), time.Minute)
			if plan.history != nil || !strings.Contains(plan.reason, "About these metrics") || strings.Contains(plan.reason, "--prometheus-") {
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

func TestHistoricalPartitionDoesNotCacheEmptyProof(t *testing.T) {
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("query") == "up" {
			_, _ = w.Write([]byte(authProbeBody))
			return
		}
		var rows []prom.Series
		if probes.Add(1) > 1 {
			rows = []prom.Series{{Labels: map[string]string{"namespace": "shop", "pod": "api-0", "uid": "uid", "cluster": "east"}}}
		}
		_, _ = w.Write(attributionEvidence(rows, ""))
	}))
	defer upstream.Close()
	c := &Client{baseURL: upstream.URL, httpClient: upstream.Client()}
	scope := PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "uid"}}}
	config, err := c.historicalClusterScope(context.Background(), scope, nil)
	if err != nil || len(config.ClusterLabels) != 0 || c.workloadPartition != nil {
		t.Fatalf("empty proof cached: config=%+v memo=%+v err=%v", config, c.workloadPartition, err)
	}
	config, err = c.historicalClusterScope(context.Background(), scope, nil)
	if err != nil || config.ClusterLabels["cluster"] != "east" || probes.Load() != 2 {
		t.Fatalf("new evidence not checked: config=%+v probes=%d err=%v", config, probes.Load(), err)
	}
	config, err = c.historicalClusterScope(context.Background(), PodScope{Namespace: "other"}, nil)
	if err != nil || config.ClusterLabels["cluster"] != "east" || probes.Load() != 2 {
		t.Fatalf("positive proof not reused: config=%+v probes=%d err=%v", config, probes.Load(), err)
	}
}

func TestHistoricalPartitionEmptyProbeDoesNotSuppressOtherNamespaces(t *testing.T) {
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		query := r.Form.Get("query")
		if query == "up" {
			_, _ = w.Write([]byte(authProbeBody))
			return
		}
		probes.Add(1)
		var rows []prom.Series
		if strings.Contains(query, `namespace="other"`) {
			rows = []prom.Series{{Labels: map[string]string{"namespace": "other", "pod": "api-0", "uid": "uid", "cluster": "west"}}}
		}
		_, _ = w.Write(attributionEvidence(rows, ""))
	}))
	defer upstream.Close()
	c := &Client{baseURL: upstream.URL, httpClient: upstream.Client()}
	for _, namespace := range []string{"empty", "other", "empty"} {
		got, err := c.historicalClusterScope(context.Background(), PodScope{Namespace: namespace, Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "uid"}}}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if probes.Load() == 1 && len(got.ClusterLabels) != 0 {
			t.Fatalf("empty namespace gained a proof: %+v", got)
		}
		if namespace == "other" && got.ClusterLabels["cluster"] != "west" {
			t.Fatalf("empty probe suppressed another namespace's proof: %+v", got)
		}
		if namespace == "empty" && probes.Load() == 2 && got.ClusterLabels["cluster"] != "west" {
			t.Fatalf("positive proof not reused across namespaces: %+v", got)
		}
	}
	if probes.Load() != 2 {
		t.Fatalf("want two namespace probes with connection-wide positive reuse, got %d", probes.Load())
	}
}

func TestHistoricalPartitionDistinguishesUnlabeledEvidence(t *testing.T) {
	pod := prom.WorkloadPodIdentity{Name: "api-0", UID: "local-uid"}
	local := prom.Series{Labels: map[string]string{"namespace": "shop", "pod": pod.Name, "uid": pod.UID}}
	foreign := prom.Series{Labels: map[string]string{"namespace": "shop", "pod": pod.Name, "uid": "foreign-uid"}}
	labeledLocal := prom.Series{Labels: map[string]string{"namespace": "shop", "pod": pod.Name, "uid": pod.UID, "cluster": "east"}}
	labeledForeign := prom.Series{Labels: map[string]string{"namespace": "shop", "pod": pod.Name, "uid": "foreign-uid", "cluster": "west"}}
	for _, tc := range []struct {
		name      string
		rows      []prom.Series
		unlabeled bool
		cluster   string
	}{
		{name: "no evidence"},
		{name: "foreign UID", rows: []prom.Series{foreign}},
		{name: "unlabeled UID match", rows: []prom.Series{local}, unlabeled: true},
		{name: "foreign labeled population", rows: []prom.Series{local, labeledForeign}, unlabeled: true},
		{name: "unlabeled UID collision", rows: []prom.Series{local, foreign}},
		{name: "labeled proof takes precedence", rows: []prom.Series{local, labeledLocal}, cluster: "east"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := prom.NewClient(attributionTransport(func(string) ([]byte, error) {
				return attributionEvidence(tc.rows, ""), nil
			}))
			proof, err := probeHistoricalPartition(context.Background(), client, "shop", []prom.WorkloadPodIdentity{pod})
			if err != nil || proof.unlabeled != tc.unlabeled || proof.scope.ClusterLabels["cluster"] != tc.cluster {
				t.Fatalf("proof=%+v err=%v", proof, err)
			}
			if !tc.unlabeled && tc.cluster == "" && len(proof.scope.ClusterLabels) != 0 {
				t.Fatalf("unknown identity became a verified partition: %+v", proof)
			}
		})
	}
}

func TestUnlabeledPartitionFlightStaysInNamespace(t *testing.T) {
	for _, labeledOther := range []bool{false, true} {
		t.Run(fmt.Sprintf("labeled other=%t", labeledOther), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				query := r.Form.Get("query")
				if query == "up" {
					_, _ = w.Write([]byte(authProbeBody))
					return
				}
				var rows []prom.Series
				if strings.Contains(query, `namespace="original"`) {
					close(started)
					<-release
					rows = []prom.Series{{Labels: map[string]string{"namespace": "original", "pod": "api-0", "uid": "uid"}}}
				} else if labeledOther {
					rows = []prom.Series{{Labels: map[string]string{"namespace": "other", "pod": "api-0", "uid": "other-uid", "cluster": "east"}}}
				}
				_, _ = w.Write(attributionEvidence(rows, ""))
			}))
			defer upstream.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			c := &Client{baseURL: upstream.URL, httpClient: upstream.Client()}
			anchors := []prom.WorkloadPodIdentity{{Name: "api-0", UID: "uid"}}
			result := make(chan workloadPartitionProof, 1)
			go func() {
				proof, _ := c.historicalClusterProof(context.Background(), PodScope{Namespace: "original", Identities: anchors}, nil)
				result <- proof
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("original namespace probe did not start")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			proof, err := c.historicalClusterProof(ctx, PodScope{Namespace: "other", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "other-uid"}}}, nil)
			if err != nil || proof.unlabeled || (len(proof.scope.ClusterLabels) > 0) != labeledOther {
				t.Fatalf("other namespace inherited or waited for the original proof: %+v err=%v", proof, err)
			}
			close(release)
			if original := <-result; !original.unlabeled {
				t.Fatalf("original namespace did not prove its unlabeled partition: %+v", original)
			}
			if labeledOther {
				config, err := c.historicalClusterScope(context.Background(), PodScope{Namespace: "no-ksm"}, nil)
				if err != nil || config.ClusterLabels["cluster"] != "east" {
					t.Fatalf("late unlabeled proof evicted connection-wide labeled evidence: %+v err=%v", config, err)
				}
			}
		})
	}
}

func TestUnlabeledPartitionRequiresPodUID(t *testing.T) {
	rows := []prom.Series{{Labels: map[string]string{"pod": "api-0"}}}
	partition, _ := identifyPartitionWithUnlabeled([]prom.WorkloadPodIdentity{{Name: "api-0"}}, rows, true)
	if len(partition) != 0 {
		t.Fatalf("missing Pod UID became an unlabeled proof: %v", partition)
	}
}

func TestHistoricalPartitionFlightsUsePodUIDs(t *testing.T) {
	for _, labeled := range []bool{false, true} {
		t.Run(fmt.Sprintf("labeled=%t", labeled), func(t *testing.T) {
			started, release := make(chan struct{}), make(chan struct{})
			var probes atomic.Int32
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_ = r.ParseForm()
				if r.Form.Get("query") == "up" {
					_, _ = w.Write([]byte(authProbeBody))
					return
				}
				if probes.Add(1) == 1 {
					close(started)
					<-release
				}
				labels := map[string]string{"namespace": "shop", "pod": "api-0", "uid": "current"}
				if labeled {
					labels["cluster"] = "east"
				}
				_, _ = w.Write(attributionEvidence([]prom.Series{{Labels: labels}}, ""))
			}))
			defer upstream.Close()
			defer func() {
				select {
				case <-release:
				default:
					close(release)
				}
			}()
			c := &Client{baseURL: upstream.URL, httpClient: upstream.Client()}
			done := make(chan error, 1)
			go func() {
				proof, err := c.historicalClusterProof(context.Background(), PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "previous"}}}, nil)
				if err == nil && (proof.unlabeled || len(proof.scope.ClusterLabels) != 0) {
					err = fmt.Errorf("previous Pod gained current Pod's proof: %+v", proof)
				}
				done <- err
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("previous Pod's probe did not start")
			}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			proof, err := c.historicalClusterProof(ctx, PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "current"}}}, nil)
			if err != nil || proof.unlabeled == labeled || (proof.scope.ClusterLabels["cluster"] == "east") != labeled || probes.Load() != 2 {
				t.Fatalf("current Pod inherited or waited for another UID's probe: %+v probes=%d err=%v", proof, probes.Load(), err)
			}
			close(release)
			if err := <-done; err != nil {
				t.Fatal(err)
			}
			if memo := c.workloadPartition; memo == nil || memo.unlabeled == labeled || (memo.scope.ClusterLabels["cluster"] == "east") != labeled {
				t.Fatalf("late empty probe evicted valid evidence: %+v", memo)
			}
		})
	}
}

func TestHistoricalPartitionUnlabeledMemoChecksPodUIDs(t *testing.T) {
	var probes atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		if r.Form.Get("query") == "up" {
			_, _ = w.Write([]byte(authProbeBody))
			return
		}
		labels := map[string]string{"namespace": "shop", "pod": "api-0", "uid": "previous"}
		if probes.Add(1) > 1 {
			labels["uid"], labels["cluster"] = "current", "east"
		}
		_, _ = w.Write(attributionEvidence([]prom.Series{{Labels: labels}}, ""))
	}))
	defer upstream.Close()
	c := &Client{baseURL: upstream.URL, httpClient: upstream.Client()}
	scope := PodScope{Namespace: "shop", Identities: []prom.WorkloadPodIdentity{{Name: "api-0", UID: "previous"}}}
	for range 2 {
		proof, err := c.historicalClusterProof(context.Background(), scope, nil)
		if err != nil || !proof.unlabeled || probes.Load() != 1 {
			t.Fatalf("same Pod did not reuse unlabeled evidence: %+v probes=%d err=%v", proof, probes.Load(), err)
		}
	}
	scope.Identities[0].UID = "current"
	proof, err := c.historicalClusterProof(context.Background(), scope, nil)
	if err != nil || proof.unlabeled || proof.scope.ClusterLabels["cluster"] != "east" || probes.Load() != 2 {
		t.Fatalf("new Pod inherited stale unlabeled proof: %+v probes=%d err=%v", proof, probes.Load(), err)
	}
}
