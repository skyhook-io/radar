package openobserve

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// Metrics identified by the collector's cluster name alone are read only over
// the 7 days discovery checked that name for.
func TestGatewayMetricsStayInsideTheVerifiedWindow(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lookupNow = func() time.Time { return now }
	t.Cleanup(func() { lookupNow = time.Now })
	unix := func(d time.Duration) string { return strconv.FormatInt(now.Add(-d).Unix(), 10) }
	const day = 24 * time.Hour

	f := &fakeOO{body: `{"status":"success","data":{"resultType":"matrix","result":[]}}`}
	rt, _ := newRewriting(t, f)
	rt.Options = RewriteOptions{Matchers: []Matcher{{Name: "k8s_cluster", Value: "prod-a"}}, Dialect: DialectCollector, CollectorUID: uidA, GatewayMetricsAllowed: true}
	do := func(path string, params url.Values) error {
		_, err := rt.Do(context.Background(), http.MethodGet, path, params)
		return err
	}

	// A 14-day chart on a name-only metric shows the verified 7 days, on its step grid.
	if err := do("/api/v1/query_range", url.Values{"query": {`sum(rate(kube_pod_container_status_restarts_total[5m]))`}, "start": {unix(14 * day)}, "end": {unix(0)}, "step": {"7200"}}); err != nil {
		t.Fatal(err)
	}
	start, _ := strconv.ParseFloat(f.last(t).params.Get("start"), 64)
	earliest := now.Add(-nameHistoryDuration).Add(5 * time.Minute)
	if got := time.Unix(int64(start), 0); got.Before(earliest) || got.After(earliest.Add(2*time.Hour)) || got.Sub(now.Add(-14*day))%(2*time.Hour) != 0 {
		t.Fatalf("start = %s, want the first step at or after %s", got, earliest)
	}

	// The collector agent's own metrics carry the cluster UID: unaffected.
	if err := do("/api/v1/query_range", url.Values{"query": {`sum(k8s_pod_cpu_usage)`}, "start": {unix(14 * day)}, "end": {unix(0)}, "step": {"7200"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).params.Get("start"); got != unix(14*day) {
		t.Fatalf("UID-scoped query start = %s, want unchanged %s", got, unix(14*day))
	}

	before := f.count()
	for name, tc := range map[string]struct {
		path   string
		params url.Values
	}{
		"offset":         {"/api/v1/query", url.Values{"query": {`kube_pod_info offset 8d`}}},
		"range in range": {"/api/v1/query", url.Values{"query": {`max_over_time(kube_pod_info[8d])`}}},
		"subquery":       {"/api/v1/query", url.Values{"query": {`max_over_time(sum(kube_pod_info)[6d:1h] offset 2d)`}}},
		"fixed @":        {"/api/v1/query", url.Values{"query": {`kube_pod_info @ ` + unix(8*day)}}},
		"@ end()":        {"/api/v1/query_range", url.Values{"query": {`kube_pod_info @ end() offset 8d`}, "start": {unix(time.Hour)}, "end": {unix(0)}, "step": {"60"}}},
		"ends before":    {"/api/v1/query_range", url.Values{"query": {`kube_pod_info`}, "start": {unix(10 * day)}, "end": {unix(8 * day)}, "step": {"60"}}},
		"old instant":    {"/api/v1/query", url.Values{"query": {`kube_pod_info`}, "time": {unix(8 * day)}}},
		"old series":     {"/api/v1/series", url.Values{"match[]": {`kube_pod_info`}, "start": {unix(9 * day)}, "end": {unix(8 * day)}}},
	} {
		err := do(tc.path, tc.params)
		if !errors.Is(err, prom.ErrQueryRefused) || !errors.Is(err, errGatewayWindow) {
			t.Errorf("%s: err = %v, want the window refusal", name, err)
		}
	}
	if f.count() != before {
		t.Fatal("a refused query reached OpenObserve")
	}

	// Inside the window, offsets and lookups pass; a series lookup's start is clamped.
	if err := do("/api/v1/query", url.Values{"query": {`kube_pod_info offset 6d`}}); err != nil {
		t.Fatal(err)
	}
	// Rightsizing's 7-day subqueries join name-only metrics and reach a few minutes past 7 days.
	if err := do("/api/v1/query", url.Values{"query": {`quantile_over_time(0.95, (max by (namespace) (rate(container_cpu_usage_seconds_total[5m]) * on (namespace,pod) group_left() kube_pod_info))[7d:5m])`}}); err != nil {
		t.Fatalf("rightsizing query: %v", err)
	}
	if err := do("/api/v1/series", url.Values{"match[]": {`kube_pod_info`}, "start": {unix(14 * day)}, "end": {unix(0)}}); err != nil {
		t.Fatal(err)
	}
	if start, _ := strconv.ParseFloat(f.last(t).params.Get("start"), 64); time.Unix(int64(start), 0).Before(now.Add(-nameHistoryDuration)) {
		t.Fatalf("series start %v reaches before the window", start)
	}

	// Standard data and collector scopes without gateway metrics are unaffected.
	rt.Options = RewriteOptions{Matchers: []Matcher{{Name: "radar_cluster_uid", Value: uidA}}}
	if err := do("/api/v1/query", url.Values{"query": {`kube_pod_info offset 30d`}}); err != nil {
		t.Fatalf("standard dialect: %v", err)
	}
	if err := do("/api/v1/query_range", url.Values{"query": {`kube_pod_info`}, "start": {unix(14 * day)}, "end": {unix(0)}, "step": {"7200"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).params.Get("start"); got != unix(14*day) {
		t.Fatalf("standard dialect start = %s, want unchanged", got)
	}
}
