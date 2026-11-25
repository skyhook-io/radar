package openobserve

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

type recorded struct {
	method string
	path   string
	params url.Values
	auth   string
}

type fakeOO struct {
	mu       sync.Mutex
	requests []recorded
	status   int
	headers  map[string]string
	body     string
}

func (f *fakeOO) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	_ = r.ParseForm()
	f.mu.Lock()
	f.requests = append(f.requests, recorded{method: r.Method, path: r.URL.EscapedPath(), params: r.Form, auth: r.Header.Get("Authorization")})
	status, headers, body := f.status, f.headers, f.body
	f.mu.Unlock()
	for k, v := range headers {
		w.Header().Set(k, v)
	}
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	_, _ = io.WriteString(w, body)
}

func (f *fakeOO) last(t *testing.T) recorded {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("no request reached the server")
	}
	return f.requests[len(f.requests)-1]
}

func (f *fakeOO) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func newRewriting(t *testing.T, f *fakeOO) (*RewritingTransport, *int) {
	t.Helper()
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	conn := Connection{BaseURL: srv.URL + "/", Org: "default", User: "u@example.com", Token: "secret"}
	inner, err := conn.NewHTTPTransport(5 * time.Second)
	if err != nil {
		t.Fatal(err)
	}
	dropped := 0
	return &RewritingTransport{
		Inner:     inner,
		Options:   RewriteOptions{Matchers: []Matcher{{Name: "cluster", Value: "oo-b"}}},
		OnDropped: func(n int) { dropped += n },
	}, &dropped
}

const mixedVector = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"cluster":"oo-b"},"value":[1,"1"]},{"metric":{"cluster":"oo-a"},"value":[1,"1"]}]}}`

func TestRewritingTransportQuery(t *testing.T) {
	for _, method := range []string{http.MethodGet, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			f := &fakeOO{body: mixedVector}
			rt, dropped := newRewriting(t, f)
			params := url.Values{"query": {`sum by (Pod) (rate(X{cluster=~".*"}[5m]))`}, "time": {"1700000000"}}
			body, err := rt.Do(context.Background(), method, "/api/v1/query", params)
			if err != nil {
				t.Fatal(err)
			}
			req := f.last(t)
			if req.method != method || req.path != "/api/default/prometheus/api/v1/query" {
				t.Fatalf("request = %s %s", req.method, req.path)
			}
			if got := req.params.Get("query"); got != `sum by (pod, cluster) (rate(x{cluster="oo-b",cluster=~".*"}[5m]))` {
				t.Fatalf("query sent = %s", got)
			}
			if req.params.Get("time") != "1700000000" {
				t.Fatalf("time param lost: %v", req.params)
			}
			if req.auth != "Basic dUBleGFtcGxlLmNvbTpzZWNyZXQ=" {
				t.Fatalf("auth = %q", req.auth)
			}
			if strings.Contains(string(body), "oo-a") || *dropped != 1 {
				t.Fatalf("body=%s dropped=%d", body, *dropped)
			}
			if params.Get("query") != `sum by (Pod) (rate(X{cluster=~".*"}[5m]))` {
				t.Fatal("caller's params were mutated")
			}
		})
	}
}

func TestRewritingTransportQueryRangeThroughClient(t *testing.T) {
	f := &fakeOO{body: `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"cluster":"oo-b"},"values":[[1,"1"]]},{"metric":{},"values":[[1,"1"]]}]}}`}
	rt, dropped := newRewriting(t, f)
	res, err := prom.NewClient(rt).QueryRange(context.Background(), "up", time.Unix(0, 0), time.Unix(600, 0), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Series) != 1 || *dropped != 1 {
		t.Fatalf("series=%d dropped=%d", len(res.Series), *dropped)
	}
	if got := f.last(t).params.Get("query"); got != `up{cluster="oo-b"}` {
		t.Fatalf("query sent = %s", got)
	}
}

func TestRewritingTransportKeepsScalarRangeResults(t *testing.T) {
	for q, want := range map[string]string{
		`time()`:          `label_replace(vector(time()), "cluster", "oo-b", "", "")`,
		`1 + 2`:           `label_replace(vector(1 + 2), "cluster", "oo-b", "", "")`,
		`sum(up)`:         `sum by (cluster) (up{cluster="oo-b"})`,
	} {
		f := &fakeOO{body: `{"status":"success","data":{"resultType":"matrix","result":[{"metric":{"cluster":"oo-b"},"values":[[1,"1"]]}]}}`}
		rt, dropped := newRewriting(t, f)
		res, err := prom.NewClient(rt).QueryRange(context.Background(), q, time.Unix(0, 0), time.Unix(600, 0), time.Minute)
		if err != nil {
			t.Fatalf("%s: %v", q, err)
		}
		if got := f.last(t).params.Get("query"); got != want {
			t.Fatalf("%s: query sent = %s, want %s", q, got, want)
		}
		if len(res.Series) != 1 || *dropped != 0 {
			t.Fatalf("%s: series=%d dropped=%d", q, len(res.Series), *dropped)
		}
	}
	// An instant query keeps its scalar result type.
	f := &fakeOO{body: `{"status":"success","data":{"resultType":"scalar","result":[1,"1"]}}`}
	rt, _ := newRewriting(t, f)
	if _, err := rt.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"time()"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).params.Get("query"); got != `time()` {
		t.Fatalf("instant query sent = %s", got)
	}
}

func TestRewritingTransportMatch(t *testing.T) {
	tests := []struct {
		name      string
		path      string
		sentPath  string
		matches   []string
		wantMatch []string
		body      string
		dropped   int
	}{
		{
			name: "series rewrites each match", path: "/api/v1/series", sentPath: "/api/default/prometheus/api/v1/series",
			matches:   []string{`Up`, `{__name__="X",cluster="oo-a"}`},
			wantMatch: []string{`up{cluster="oo-b"}`, `{__name__="x",cluster="oo-a",cluster="oo-b"}`},
			body:      `{"status":"success","data":[{"__name__":"up","cluster":"oo-b"},{"__name__":"up","cluster":"oo-a"}]}`,
			dropped:   1,
		},
		{
			name: "series without match gets the cluster selector", path: "/api/v1/series", sentPath: "/api/default/prometheus/api/v1/series",
			wantMatch: []string{`{cluster="oo-b"}`},
			body:      `{"status":"success","data":[]}`,
		},
		{
			name: "labels without match", path: "/api/v1/labels", sentPath: "/api/default/prometheus/api/v1/labels",
			wantMatch: []string{`{cluster="oo-b"}`},
			body:      `{"status":"success","data":["cluster","namespace"]}`,
		},
		{
			name: "label values lowercases the label", path: "/api/v1/label/" + url.PathEscape("Namespace") + "/values", sentPath: "/api/default/prometheus/api/v1/label/namespace/values",
			matches:   []string{`kube_pod_info{cluster!="oo-b"}`},
			wantMatch: []string{`kube_pod_info{cluster!="oo-b",cluster="oo-b"}`},
			body:      `{"status":"success","data":["a"]}`,
		},
		{
			name: "label values without match", path: "/api/v1/label/pod/values", sentPath: "/api/default/prometheus/api/v1/label/pod/values",
			wantMatch: []string{`{cluster="oo-b"}`},
			body:      `{"status":"success","data":["a"]}`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeOO{body: tc.body}
			rt, dropped := newRewriting(t, f)
			params := url.Values{"start": {"1"}}
			for _, m := range tc.matches {
				params.Add("match[]", m)
			}
			if _, err := rt.Do(context.Background(), http.MethodGet, tc.path, params); err != nil {
				t.Fatal(err)
			}
			req := f.last(t)
			if req.path != tc.sentPath {
				t.Fatalf("path = %s, want %s", req.path, tc.sentPath)
			}
			got := req.params["match[]"]
			if strings.Join(got, "|") != strings.Join(tc.wantMatch, "|") {
				t.Fatalf("match[] = %v, want %v", got, tc.wantMatch)
			}
			if req.params.Get("start") != "1" {
				t.Fatal("start param lost")
			}
			if *dropped != tc.dropped {
				t.Fatalf("dropped = %d, want %d", *dropped, tc.dropped)
			}
		})
	}
}

func TestRewritingTransportRefusesRules(t *testing.T) {
	f := &fakeOO{body: `{"status":"success","data":{"groups":[]}}`}
	rt, _ := newRewriting(t, f)
	_, err := rt.Do(context.Background(), http.MethodGet, "/api/v1/rules", url.Values{"type": {"alert"}})
	if !errors.Is(err, prom.ErrQueryRefused) || !errors.Is(err, ErrRulesUnscoped) {
		t.Fatalf("err = %v, want a refused rules request", err)
	}
	if f.count() != 0 {
		t.Fatal("rules request reached the server")
	}
}

// Metadata and metric-name listings come from the whole organization, with
// nothing to narrow them to one cluster.
func TestRewritingTransportRefusesOrgWideListings(t *testing.T) {
	for path, want := range map[string]error{
		"/api/v1/metadata":              ErrMetadataUnscoped,
		"/api/v1/label/__name__/values": ErrMetricNamesUnscoped,
		"/api/v1/label/__NAME__/values": ErrMetricNamesUnscoped,
	} {
		f := &fakeOO{body: `{"status":"success","data":{}}`}
		rt, _ := newRewriting(t, f)
		_, err := rt.Do(context.Background(), http.MethodGet, path, url.Values{"match[]": {`{__name__=~"up.*"}`}})
		if !errors.Is(err, want) || !errors.Is(err, prom.ErrQueryRefused) {
			t.Errorf("%s: err = %v, want %v", path, err, want)
		}
		if len(f.requests) != 0 {
			t.Errorf("%s reached OpenObserve", path)
		}
	}
}

func TestRewritingTransportRefuses(t *testing.T) {
	tests := []struct {
		name   string
		method string
		path   string
		params url.Values
	}{
		{"unknown path", http.MethodGet, "/api/v1/status/config", nil},
		{"admin path", http.MethodPost, "/api/v1/admin/tsdb/delete_series", url.Values{"match[]": {"up"}}},
		{"label traversal", http.MethodGet, "/api/v1/label/" + url.PathEscape("../../x") + "/values", nil},
		{"query string in path", http.MethodGet, "/api/v1/query?query=up", nil},
		{"method", http.MethodDelete, "/api/v1/query", url.Values{"query": {"up"}}},
		{"no query", http.MethodGet, "/api/v1/query", nil},
		{"two queries", http.MethodGet, "/api/v1/query", url.Values{"query": {"up", "x"}}},
		{"unparseable query", http.MethodGet, "/api/v1/query", url.Values{"query": {"sum("}}},
		{"range match", http.MethodGet, "/api/v1/series", url.Values{"match[]": {"rate(x[5m])"}}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeOO{body: `{"status":"success","data":[]}`}
			rt, _ := newRewriting(t, f)
			if _, err := rt.Do(context.Background(), tc.method, tc.path, tc.params); err == nil {
				t.Fatal("expected an error")
			}
			if f.count() != 0 {
				t.Fatal("refused request reached the server")
			}
		})
	}

	f := &fakeOO{}
	rt, _ := newRewriting(t, f)
	rt.Options.Matchers = nil
	if _, err := rt.Do(context.Background(), http.MethodGet, "/api/v1/metadata", nil); err == nil || f.count() != 0 {
		t.Fatal("transport without matchers must refuse every request")
	}
}

func TestRewritingTransportRateLimited(t *testing.T) {
	f := &fakeOO{status: http.StatusTooManyRequests, headers: map[string]string{"Retry-After": "7"}, body: "slow down"}
	rt, _ := newRewriting(t, f)
	_, err := rt.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}})
	var limited ErrRateLimited
	if !errors.As(err, &limited) {
		t.Fatalf("err = %v, want ErrRateLimited", err)
	}
	if limited.RetryAfter != 7*time.Second {
		t.Fatalf("RetryAfter = %s", limited.RetryAfter)
	}
}

type staticTransport struct{ err error }

func (s staticTransport) Do(context.Context, string, string, url.Values) ([]byte, error) {
	return nil, s.err
}
func (s staticTransport) Address() string { return "static" }

func TestRewritingTransportRateLimitedFromHTTPError(t *testing.T) {
	rt := &RewritingTransport{
		Inner:   staticTransport{err: &prom.HTTPError{StatusCode: http.StatusTooManyRequests, URL: "x"}},
		Options: RewriteOptions{Matchers: clusterB},
	}
	_, err := rt.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}})
	var limited ErrRateLimited
	if !errors.As(err, &limited) || limited.RetryAfter != 0 {
		t.Fatalf("err = %v", err)
	}
	var httpErr *prom.HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatal("underlying HTTPError not reachable")
	}

	rt.Inner = staticTransport{err: &prom.HTTPError{StatusCode: http.StatusBadGateway}}
	_, err = rt.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}})
	if errors.As(err, &limited) {
		t.Fatal("502 reported as rate limited")
	}
	if rt.Address() != "static" {
		t.Fatal("Address not delegated")
	}
}

func TestParseRetryAfter(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	cases := map[string]time.Duration{
		"":                              0,
		"30":                            30 * time.Second,
		"0":                             0,
		"-5":                            0,
		"soon":                          0,
		"Thu, 01 Jan 2026 00:01:00 GMT": time.Minute,
		"Wed, 31 Dec 2025 00:00:00 GMT": 0,
	}
	for in, want := range cases {
		if got := ParseRetryAfter(in, now); got != want {
			t.Errorf("ParseRetryAfter(%q) = %s, want %s", in, got, want)
		}
	}
}

// TestLookupWithoutRangeGetsOne: OpenObserve refuses a series or label lookup
// without start and end ("invalid time range"), so a missing bound is filled
// with the last hour; a query keeps its own parameters.
func TestLookupWithoutRangeGetsOne(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	lookupNow = func() time.Time { return now }
	t.Cleanup(func() { lookupNow = time.Now })
	for _, tc := range []struct {
		path               string
		params             url.Values
		wantStart, wantEnd string
	}{
		{"/api/v1/labels", url.Values{}, "1699996400", "1700000000"},
		{"/api/v1/label/pod/values", url.Values{}, "1699996400", "1700000000"},
		{"/api/v1/series", url.Values{"match[]": {"up"}}, "1699996400", "1700000000"},
		{"/api/v1/labels", url.Values{"end": {"1600003600"}}, "1600000000", "1600003600"},
		{"/api/v1/labels", url.Values{"end": {"2020-09-13T13:26:40Z"}}, "1600000000", "2020-09-13T13:26:40Z"},
		{"/api/v1/labels", url.Values{"start": {"5"}}, "5", "1700000000"},
		{"/api/v1/labels", url.Values{"start": {"5"}, "end": {"6"}}, "5", "6"},
	} {
		f := &fakeOO{body: `{"status":"success","data":[]}`}
		rt, _ := newRewriting(t, f)
		if _, err := rt.Do(context.Background(), http.MethodGet, tc.path, tc.params); err != nil {
			t.Fatal(err)
		}
		got := f.last(t).params
		if got.Get("start") != tc.wantStart || got.Get("end") != tc.wantEnd {
			t.Errorf("%s %v: start=%s end=%s, want %s %s", tc.path, tc.params, got.Get("start"), got.Get("end"), tc.wantStart, tc.wantEnd)
		}
	}
	f := &fakeOO{body: `{"status":"success","data":{"resultType":"vector","result":[]}}`}
	rt, _ := newRewriting(t, f)
	if _, err := rt.Do(context.Background(), http.MethodGet, "/api/v1/query", url.Values{"query": {"up"}}); err != nil {
		t.Fatal(err)
	}
	if got := f.last(t).params; got.Has("start") || got.Has("end") {
		t.Fatalf("instant query params = %v", got)
	}
}
