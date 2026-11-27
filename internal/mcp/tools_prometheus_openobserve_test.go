package mcp

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

// setupOpenObserveRelay serves metrics the way an OpenObserve connection
// does: through a relay whose transport scopes every request to one cluster.
func setupOpenObserveRelay(t *testing.T) *fakeProm {
	t.Helper()
	f := &fakeProm{}
	srv := httptest.NewServer(f)
	prometheus.Initialize(nil, nil, "test-ctx", nil)
	opts := oo.RewriteOptions{Matchers: []oo.Matcher{{Name: oo.IDLabel, Value: "uid-1"}}}
	if err := prometheus.EnableRelay(prometheus.RelayConfig{
		Key:      "test-openobserve",
		Endpoint: srv.URL,
		Address:  "OpenObserve " + srv.URL,
		Source:   prom.MetricsSource{Kind: "openobserve", Via: "local", MatchedBy: "id"},
		NewTransport: func(time.Duration, int64) prom.Transport {
			return &oo.RewritingTransport{Inner: prom.NewHTTPTransport(srv.URL, "", nil), Options: opts}
		},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		prometheus.DisableRelay()
		srv.Close()
		prometheus.Reset()
		prometheus.Initialize(nil, nil, "", nil)
	})
	return f
}

func TestHandleQueryPrometheus_OpenObserveRefusesScalar(t *testing.T) {
	setupOpenObserveRelay(t)
	_, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{Query: "scalar(sum(up))"})
	if err == nil {
		t.Fatal("scalar() over an OpenObserve relay answered")
	}
	msg := err.Error()
	if !strings.HasPrefix(msg, "OpenObserve-backed metrics don't support scalar()") || !strings.Contains(msg, "sum(...)") {
		t.Fatalf("error = %q, want the scalar refusal telling the model what to query instead", msg)
	}
}

// Metric names can't be listed for one cluster on OpenObserve, so
// discover_metrics says so instead of failing, and sends nothing.
func TestHandleDiscoverMetrics_OpenObserveMetricNames(t *testing.T) {
	f := setupOpenObserveRelay(t)
	for _, in := range []discoverMetricsInput{
		{Match: `{__name__=~"container_.*"}`},
		{Label: "__name__", Match: `{namespace="shop"}`},
	} {
		result, _, err := handleDiscoverMetrics(context.Background(), nil, in)
		if err != nil {
			t.Fatalf("%+v: %v", in, err)
		}
		resp := decodeDiscoverResponse(t, extractText(t, result))
		if resp.Count != 0 || len(resp.Metrics) != 0 || !strings.Contains(resp.Note, "isn't available for metrics served from OpenObserve") {
			t.Fatalf("%+v: response = %+v", in, resp)
		}
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelCalls) != 0 || f.metadataCalls != 0 {
		t.Fatalf("listing reached OpenObserve: labels=%v metadata=%d", f.labelCalls, f.metadataCalls)
	}
}

// Label values for a named metric still work through the scoped transport.
func TestHandleDiscoverMetrics_OpenObserveLabelValues(t *testing.T) {
	f := setupOpenObserveRelay(t)
	f.labelBody = `{"status":"success","data":["api-1"]}`
	result, _, err := handleDiscoverMetrics(context.Background(), nil, discoverMetricsInput{Label: "pod", Match: `kube_pod_info{namespace="shop"}`})
	if err != nil {
		t.Fatal(err)
	}
	if resp := decodeDiscoverResponse(t, extractText(t, result)); len(resp.Values) != 1 {
		t.Fatalf("values = %+v", resp)
	}
	if got := f.lastLabelCall(t).params["match[]"]; len(got) != 1 || !strings.Contains(got[0], `radar_cluster_uid="uid-1"`) {
		t.Fatalf("match[] sent = %v", got)
	}
}

// A query with no data gets no metric-name suggestions on OpenObserve, and
// no error from trying.
func TestHandleQueryPrometheus_OpenObserveNoSuggestions(t *testing.T) {
	f := setupOpenObserveRelay(t)
	result, _, err := handleQueryPrometheus(context.Background(), nil, queryPrometheusInput{Query: "container_cpu_usage_secnds_total"})
	if err != nil {
		t.Fatal(err)
	}
	resp := decodeQueryResponse(t, extractText(t, result))
	if len(resp.SuggestedMetrics) != 0 || !strings.Contains(resp.Note, "query returned no data") {
		t.Fatalf("response = %+v", resp)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.labelCalls) != 0 {
		t.Fatalf("suggestion lookup reached OpenObserve: %v", f.labelCalls)
	}
}
