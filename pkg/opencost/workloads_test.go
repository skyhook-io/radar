package opencost

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
)

// podVectorBody builds a PromQL vector response where each result row has
// only a `pod` label — matching what `sum by (pod) (...)` queries return.
func podVectorBody(samples map[string]float64) string {
	type result struct {
		Metric map[string]string `json:"metric"`
		Value  []interface{}     `json:"value"`
	}
	body := struct {
		Status string `json:"status"`
		Data   struct {
			ResultType string   `json:"resultType"`
			Result     []result `json:"result"`
		} `json:"data"`
	}{Status: "success"}
	body.Data.ResultType = "vector"
	for pod, v := range samples {
		body.Data.Result = append(body.Data.Result, result{
			Metric: map[string]string{"pod": pod},
			Value:  []interface{}{1700000000.0, formatFloat(v)},
		})
	}
	b, _ := json.Marshal(body)
	return string(b)
}

// workloadsProm returns a prom.Client where every PromQL query returns the
// same canned pod-keyed body.
func workloadsProm(t *testing.T, body string) *prom.Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return prom.NewClient(prom.NewHTTPTransport(srv.URL, "", nil))
}

func TestComputeWorkloads_OwnerLookupResolves(t *testing.T) {
	// Three pods reported by PromQL; ownerLookup resolves all three to two
	// distinct workloads. Replicas should be 2 + 1, not 3 standalone rows.
	// worker pod cost (5.0) > sum of api pods (1.0 + 1.0 = 2.0) so sort
	// is deterministic. The same vector body is returned for all four
	// queries (CPU alloc, mem alloc, CPU usage, mem usage), so the
	// per-pod HourlyCost is 2× the input value (cpu + mem).
	client := workloadsProm(t, podVectorBody(map[string]float64{
		"api-7f8d9c-xyz12":  1.0,
		"api-7f8d9c-abc34":  1.0,
		"worker-deadbeef01": 5.0,
	}))
	lookup := func(pod string) (WorkloadOwner, bool) {
		switch pod {
		case "api-7f8d9c-xyz12", "api-7f8d9c-abc34":
			return WorkloadOwner{Name: "api", Kind: "Deployment"}, true
		case "worker-deadbeef01":
			return WorkloadOwner{Name: "worker", Kind: "Job"}, true
		}
		return WorkloadOwner{}, false
	}
	got := ComputeWorkloadsFromProm(context.Background(), client, "default", lookup)
	if !got.Available {
		t.Fatalf("expected Available=true, got %+v", got)
	}
	if len(got.Workloads) != 2 {
		t.Fatalf("expected 2 workloads, got %d: %+v", len(got.Workloads), got.Workloads)
	}
	// workloads are sorted descending by HourlyCost; worker (2.0 + 2.0 mem
	// from the same query body) comes first.
	if got.Workloads[0].Name != "worker" || got.Workloads[0].Kind != "Job" {
		t.Errorf("first workload: got %s/%s, want worker/Job", got.Workloads[0].Name, got.Workloads[0].Kind)
	}
	if got.Workloads[0].Replicas != 1 {
		t.Errorf("worker replicas: got %d, want 1", got.Workloads[0].Replicas)
	}
	if got.Workloads[1].Name != "api" || got.Workloads[1].Kind != "Deployment" {
		t.Errorf("second workload: got %s/%s, want api/Deployment", got.Workloads[1].Name, got.Workloads[1].Kind)
	}
	if got.Workloads[1].Replicas != 2 {
		t.Errorf("api replicas: got %d, want 2", got.Workloads[1].Replicas)
	}
}

func TestComputeWorkloads_OwnerLookupNilFallsBackToPodSuffixStrip(t *testing.T) {
	// nil lookup → every pod falls through to stripPodSuffix; kind="standalone".
	client := workloadsProm(t, podVectorBody(map[string]float64{
		"api-7f8d9c-xyz12": 1.0,
	}))
	got := ComputeWorkloadsFromProm(context.Background(), client, "default", nil)
	if !got.Available {
		t.Fatalf("expected Available=true, got %+v", got)
	}
	if len(got.Workloads) != 1 {
		t.Fatalf("expected 1 workload, got %d", len(got.Workloads))
	}
	if got.Workloads[0].Name != "api" || got.Workloads[0].Kind != "standalone" {
		t.Errorf("got %s/%s, want api/standalone", got.Workloads[0].Name, got.Workloads[0].Kind)
	}
}

func TestComputeWorkloads_OwnerLookupUnresolvedPodFallsBack(t *testing.T) {
	// Lookup resolves one pod, returns false for the other — false case must
	// still produce a row (with the stripPodSuffix-derived name) rather than
	// silently dropping the pod.
	client := workloadsProm(t, podVectorBody(map[string]float64{
		"api-7f8d9c-xyz12":   1.0,
		"orphan-pod-abc-123": 1.0,
	}))
	lookup := func(pod string) (WorkloadOwner, bool) {
		if pod == "api-7f8d9c-xyz12" {
			return WorkloadOwner{Name: "api", Kind: "Deployment"}, true
		}
		return WorkloadOwner{}, false
	}
	got := ComputeWorkloadsFromProm(context.Background(), client, "default", lookup)
	if !got.Available {
		t.Fatalf("expected Available=true, got %+v", got)
	}
	if len(got.Workloads) != 2 {
		t.Fatalf("expected 2 workloads, got %d: %+v", len(got.Workloads), got.Workloads)
	}
	// Find the orphan — should have kind="standalone" and stripped name.
	var orphan *WorkloadCost
	for i := range got.Workloads {
		if got.Workloads[i].Kind == "standalone" {
			orphan = &got.Workloads[i]
			break
		}
	}
	if orphan == nil {
		t.Fatalf("no standalone workload found in %+v", got.Workloads)
	}
	if orphan.Name != "orphan-pod" {
		// stripPodSuffix strips two trailing -suffixes: orphan-pod-abc-123 → orphan-pod
		t.Errorf("orphan name: got %q, want %q", orphan.Name, "orphan-pod")
	}
}

func TestComputeWorkloads_EmptyResultReturnsNoMetricsReason(t *testing.T) {
	// Queries succeed but return zero series — should surface ReasonNoMetrics
	// (not Available=true with empty workloads list).
	emptyBody := `{"status":"success","data":{"resultType":"vector","result":[]}}`
	client := workloadsProm(t, emptyBody)
	got := ComputeWorkloadsFromProm(context.Background(), client, "default", nil)
	if got.Available {
		t.Errorf("expected Available=false on empty results, got Available=true")
	}
	if got.Reason != ReasonNoMetrics {
		t.Errorf("Reason: got %q, want %q", got.Reason, ReasonNoMetrics)
	}
}

func TestComputeWorkloads_NilClient(t *testing.T) {
	got := ComputeWorkloadsFromProm(context.Background(), nil, "default", nil)
	if got.Available {
		t.Errorf("expected Available=false with nil client")
	}
	if got.Reason != ReasonNoPrometheus {
		t.Errorf("Reason: got %q, want %q", got.Reason, ReasonNoPrometheus)
	}
}

func TestStripPodSuffix(t *testing.T) {
	cases := []struct {
		in, want string
	}{
		{"myapp-7f8d9c-xyz12", "myapp"},        // deployment pod (rs-hash + pod-hash)
		{"myapp-xyz12", "myapp"},               // single suffix (e.g. CronJob)
		{"mywf-step-1-abc12-xyz", "mywf-step-1"}, // multi-segment workflow name
		{"plain", "plain"},                     // no dashes
		{"-leading", "-leading"},               // leading-dash edge case
	}
	for _, tc := range cases {
		got := stripPodSuffix(tc.in)
		if got != tc.want {
			t.Errorf("stripPodSuffix(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
