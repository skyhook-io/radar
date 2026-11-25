package openobserve

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/openobserve/oofake"
	"github.com/skyhook-io/radar/pkg/prom"
	oo "github.com/skyhook-io/radar/pkg/prom/openobserve"
)

func testConn(t *testing.T, base string) oo.Connection {
	t.Helper()
	conn, err := Connection(*entryFor(base))
	if err != nil {
		t.Fatal(err)
	}
	return conn
}

func TestNotCollectedQueryAnswersEmptyInsteadOfError(t *testing.T) {
	f := &oofake.Server{}
	srv := oofake.Start(t, f)
	opts := oo.RewriteOptions{
		Matchers:     []oo.Matcher{{Name: "k8s_cluster", Value: "cluster1"}},
		Dialect:      oo.DialectCollector,
		CollectorUID: uidB,
	}
	tr := transportFactory(testConn(t, srv.URL), opts, false, &health{})(5*time.Second, 0)
	for path, want := range map[string]string{
		"/api/v1/query":       `"resultType":"vector","result":[]`,
		"/api/v1/query_range": `"resultType":"matrix","result":[]`,
	} {
		body, err := tr.Do(context.Background(), http.MethodGet, path, url.Values{"query": {"kube_pod_container_status_restarts_total"}})
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		if !strings.Contains(string(body), want) {
			t.Fatalf("%s: body = %s", path, body)
		}
	}
	if n := len(f.Recorded()); n != 0 {
		t.Fatalf("a refused query reached OpenObserve %d times", n)
	}
}

func TestOverloadIsRetriedOnceThenReportedUnavailable(t *testing.T) {
	var calls atomic.Int32
	var failures atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if failures.Load() > 0 {
			failures.Add(-1)
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"transport error"}`))
			return
		}
		oofake.WriteVector(w, []map[string]string{{"radar_cluster_uid": uidB}})
	}))
	t.Cleanup(srv.Close)
	opts := oo.RewriteOptions{Matchers: []oo.Matcher{{Name: "radar_cluster_uid", Value: uidB}}}
	h := &health{}
	tr := transportFactory(testConn(t, srv.URL), opts, false, h)(5*time.Second, 0)

	failures.Store(1)
	if _, err := tr.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}}); err != nil {
		t.Fatalf("one overload answer must be retried: %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want 2", calls.Load())
	}

	calls.Store(0)
	failures.Store(5)
	_, err := tr.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}})
	if err == nil || !strings.HasPrefix(err.Error(), UnavailablePrefix) {
		t.Fatalf("err = %v", err)
	}
	if calls.Load() != 2 {
		t.Fatalf("calls = %d, want exactly one retry", calls.Load())
	}
	if down, msg := h.get(); !down || !strings.HasPrefix(msg, UnavailablePrefix) {
		t.Fatalf("health = %v %q", down, msg)
	}
	failures.Store(0)
	if _, err := tr.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}}); err != nil {
		t.Fatal(err)
	}
	if down, _ := h.get(); down {
		t.Fatal("a success must clear the failure")
	}
}

func TestRateLimitIsReportedAsUnavailable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	t.Cleanup(srv.Close)
	h := &health{}
	tr := transportFactory(testConn(t, srv.URL), oo.RewriteOptions{Matchers: []oo.Matcher{{Name: "radar_cluster_uid", Value: uidB}}}, false, h)(5*time.Second, 0)
	_, err := tr.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}})
	var limited oo.ErrRateLimited
	if err == nil || !asRateLimited(err, &limited) || limited.RetryAfter != 7*time.Second {
		t.Fatalf("err = %v", err)
	}
	if down, msg := h.get(); !down || !strings.Contains(msg, "rate limited") {
		t.Fatalf("health = %v %q", down, msg)
	}
}

func TestRateLimitHoldsRequestsForRetryAfter(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			w.Header().Set("Retry-After", "7")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		oofake.WriteVector(w, []map[string]string{{"radar_cluster_uid": uidB}})
	}))
	t.Cleanup(srv.Close)
	now := time.Unix(1_700_000_000, 0)
	h := &health{now: func() time.Time { return now }}
	opts := oo.RewriteOptions{Matchers: []oo.Matcher{{Name: "radar_cluster_uid", Value: uidB}}}
	// Two transports of one relay (a chart query and a health probe) share h.
	chart := transportFactory(testConn(t, srv.URL), opts, false, h)(5*time.Second, 0)
	probe := transportFactory(testConn(t, srv.URL), opts, false, h)(5*time.Second, 0)
	query := func(tr prom.Transport) error {
		_, err := tr.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}})
		return err
	}

	if err := query(chart); err == nil {
		t.Fatal("429 answered without an error")
	}
	now = now.Add(3 * time.Second)
	var limited oo.ErrRateLimited
	if err := query(probe); !asRateLimited(err, &limited) || limited.RetryAfter != 4*time.Second {
		t.Fatalf("during the wait: %v", err)
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("OpenObserve called %d times during its requested wait", n)
	}
	now = now.Add(4 * time.Second)
	if err := query(chart); err != nil {
		t.Fatalf("after the wait: %v", err)
	}
	if down, _ := h.get(); down {
		t.Fatal("a success after the wait must clear the failure")
	}
}

func TestForbiddenQuerySaysTheAccountCantReadMetrics(t *testing.T) {
	for code, want := range map[int]string{
		http.StatusUnauthorized: "it rejected Radar's user or token",
		http.StatusForbidden:    "it doesn't let this account read metrics",
	} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(code) }))
		tr := transportFactory(testConn(t, srv.URL), oo.RewriteOptions{Matchers: []oo.Matcher{{Name: "radar_cluster_uid", Value: uidB}}}, false, &health{})(5*time.Second, 0)
		_, err := tr.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}})
		srv.Close()
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("%d: err = %v, want %q", code, err, want)
		}
	}
}

func asRateLimited(err error, target *oo.ErrRateLimited) bool {
	l, ok := err.(oo.ErrRateLimited)
	if ok {
		*target = l
	}
	return ok
}

// TestLegacySchemaFailureMarksSourceUnavailable: a stream schema lookup the
// store rejects is an OpenObserve failure like a rejected query: the source
// is marked unavailable and the error names why, in Radar Hub's words.
func TestLegacySchemaFailureMarksSourceUnavailable(t *testing.T) {
	var queries atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Path, "/streams/") {
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"code":403,"message":"Unauthorized Access"}`))
			return
		}
		queries.Add(1)
		oofake.WriteVector(w, []map[string]string{{"radar_cluster_uid": uidB}})
	}))
	t.Cleanup(srv.Close)
	h := &health{}
	opts := oo.RewriteOptions{Matchers: []oo.Matcher{{Name: "radar_cluster_uid", Value: uidB}}}
	tr := transportFactory(testConn(t, srv.URL), opts, true, h)(5*time.Second, 0)
	_, err := tr.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"kube_pod_info"}})
	want := UnavailablePrefix + ": it doesn't let this account read metrics; give it a role that can read the metrics streams"
	if err == nil || err.Error() != want {
		t.Fatalf("err = %v, want %q", err, want)
	}
	if down, msg := h.get(); !down || msg != want {
		t.Fatalf("health = %v %q", down, msg)
	}
	if n := queries.Load(); n != 0 {
		t.Fatalf("query forwarded %d times after a failed schema check", n)
	}
}

// TestErrorsShowNoURL: the user-facing error says why OpenObserve failed but
// never carries the request's address or query.
func TestErrorsShowNoURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.RawQuery, "refused") || strings.Contains(r.FormValue("query"), "refused") {
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error at char 3"}`))
			return
		}
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"status":"error","error":"boom"}`))
	}))
	t.Cleanup(srv.Close)
	h := &health{}
	opts := oo.RewriteOptions{Matchers: []oo.Matcher{{Name: "radar_cluster_uid", Value: uidB}}}
	tr := transportFactory(testConn(t, srv.URL), opts, false, h)(5*time.Second, 0)
	host := strings.TrimPrefix(srv.URL, "http://")
	for q, want := range map[string]string{
		"up":      UnavailablePrefix + ": it answered HTTP 500",
		"refused": "OpenObserve refused the query: parse error at char 3",
	} {
		_, err := tr.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {q}})
		if err == nil || err.Error() != want {
			t.Fatalf("%s: err = %v, want %q", q, err, want)
		}
		if strings.Contains(err.Error(), host) || strings.Contains(err.Error(), uidB) {
			t.Fatalf("%s: error shows the request: %v", q, err)
		}
	}
}
