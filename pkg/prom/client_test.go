package prom

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// fakeProm returns an HTTPTransport pointed at a test server with a scripted
// response for /api/v1/query and /api/v1/query_range.
func fakeProm(t *testing.T, handler http.HandlerFunc) *HTTPTransport {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return NewHTTPTransport(srv.URL, "", nil)
}

func TestClient_Query_ParsesVector(t *testing.T) {
	body := `{
	  "status":"success",
	  "data":{
	    "resultType":"vector",
	    "result":[
	      {"metric":{"namespace":"checkout"},"value":[1700000000, "42.5"]}
	    ]
	  }
	}`
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/query") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if got := r.URL.Query().Get("query"); got != "up" {
			t.Errorf("query param = %q, want up", got)
		}
		_, _ = w.Write([]byte(body))
	})

	c := NewClient(tr)
	res, err := c.Query(context.Background(), "up")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.ResultType != "vector" || len(res.Series) != 1 {
		t.Fatalf("bad result: %+v", res)
	}
	s := res.Series[0]
	if s.Labels["namespace"] != "checkout" {
		t.Errorf("label: %v", s.Labels)
	}
	if len(s.DataPoints) != 1 || s.DataPoints[0].Timestamp != 1700000000 || s.DataPoints[0].Value != 42.5 {
		t.Errorf("datapoint: %+v", s.DataPoints)
	}
}

func TestClient_QueryRange_ParsesMatrix(t *testing.T) {
	body := `{
	  "status":"success",
	  "data":{
	    "resultType":"matrix",
	    "result":[
	      {"metric":{"pod":"p1"},"values":[[1700000000,"1"],[1700000060,"2"]]}
	    ]
	  }
	}`
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/query_range") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		if r.URL.Query().Get("step") == "" {
			t.Error("step missing")
		}
		_, _ = w.Write([]byte(body))
	})

	c := NewClient(tr)
	res, err := c.QueryRange(context.Background(), `rate(x[1m])`,
		time.Unix(1700000000, 0), time.Unix(1700000060, 0), 30*time.Second)
	if err != nil {
		t.Fatalf("QueryRange: %v", err)
	}
	if res.ResultType != "matrix" || len(res.Series[0].DataPoints) != 2 {
		t.Fatalf("bad result: %+v", res)
	}
}

func TestClient_Query_PropagatesPromError(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"parse error"}`))
	})
	c := NewClient(tr)
	_, err := c.Query(context.Background(), "up")
	if err == nil || !strings.Contains(err.Error(), "parse error") {
		t.Errorf("expected prom error, got %v", err)
	}
}

func TestClient_Query_HTTPErrorIsTyped(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("upstream busy"))
	})
	c := NewClient(tr)
	_, err := c.Query(context.Background(), "up")
	if err == nil {
		t.Fatal("expected error")
	}
	var httpErr *HTTPError
	if !errors.As(err, &httpErr) {
		t.Fatalf("want *HTTPError, got %T: %v", err, err)
	}
	if httpErr.StatusCode != http.StatusBadGateway {
		t.Errorf("status: %d", httpErr.StatusCode)
	}
}

func TestClient_Probe_RejectsEmptyInstance(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	})
	c := NewClient(tr)
	ok, reason := c.Probe(context.Background())
	if ok {
		t.Error("probe should reject instance with empty up result")
	}
	if reason != ProbeReasonEmptyInstance {
		t.Errorf("reason = %q, want empty_instance", reason)
	}
}

func TestClient_Probe_AcceptsActiveInstance(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{},"value":[1,"1"]}]}}`))
	})
	c := NewClient(tr)
	ok, reason := c.Probe(context.Background())
	if !ok {
		t.Error("probe should accept active instance")
	}
	if reason != "" {
		t.Errorf("reason should be empty on success, got %q", reason)
	}
}

func TestClient_Probe_RejectsNonPromBody(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>captive portal</html>`))
	})
	c := NewClient(tr)
	ok, reason := c.Probe(context.Background())
	if ok {
		t.Error("probe should reject non-JSON body")
	}
	if reason != ProbeReasonNotPrometheus {
		t.Errorf("reason = %q, want not_prometheus", reason)
	}
}

func TestHTTPTransport_BasePathIncluded(t *testing.T) {
	var capturedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedPath = r.URL.Path
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[]}}`))
	}))
	defer srv.Close()

	tr := NewHTTPTransport(srv.URL, "/select/0/prometheus", nil)
	c := NewClient(tr)
	_, _ = c.Query(context.Background(), "up")
	if capturedPath != "/select/0/prometheus/api/v1/query" {
		t.Errorf("base path not applied: got %q", capturedPath)
	}
}

func TestClient_LabelValues_SendsParamsAndParses(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/label/__name__/values") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		q := r.URL.Query()
		if got := q["match[]"]; len(got) != 2 || got[0] != `{job="node"}` || got[1] != `{job="kubelet"}` {
			t.Errorf("match[] = %v", got)
		}
		if q.Get("start") == "" || q.Get("end") == "" {
			t.Error("start/end missing")
		}
		if got := q.Get("limit"); got != "100" {
			t.Errorf("limit = %q, want 100", got)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":["node_cpu_seconds_total","node_memory_Active_bytes"]}`))
	})

	c := NewClient(tr)
	end := time.Unix(1700003600, 0)
	start := end.Add(-time.Hour)
	vals, err := c.LabelValues(context.Background(), "__name__",
		[]string{`{job="node"}`, `{job="kubelet"}`}, start, end, 100)
	if err != nil {
		t.Fatalf("LabelValues: %v", err)
	}
	if len(vals) != 2 || vals[0] != "node_cpu_seconds_total" {
		t.Errorf("values: %v", vals)
	}
}

func TestClient_LabelValues_OmitsOptionalParams(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		for _, p := range []string{"start", "end", "limit"} {
			if q.Has(p) {
				t.Errorf("param %q should be omitted, got %q", p, q.Get(p))
			}
		}
		_, _ = w.Write([]byte(`{"status":"success","data":[]}`))
	})

	c := NewClient(tr)
	if _, err := c.LabelValues(context.Background(), "namespace", nil, time.Time{}, time.Time{}, 0); err != nil {
		t.Fatalf("LabelValues: %v", err)
	}
}

func TestClient_LabelValues_PropagatesPromError(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"error","errorType":"bad_data","error":"invalid match[]"}`))
	})
	c := NewClient(tr)
	_, err := c.LabelValues(context.Background(), "pod", []string{"{"}, time.Time{}, time.Time{}, 0)
	if err == nil || !strings.Contains(err.Error(), "invalid match[]") {
		t.Errorf("error should carry prom body, got %v", err)
	}
}

func TestClient_Metadata_ParsesFirstEntryPerName(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/metadata") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{
			"http_requests_total":[{"type":"counter","help":"Total HTTP requests"},{"type":"counter","help":"dup"}],
			"node_memory_Active_bytes":[{"type":"gauge","help":"Memory active"}]
		}}`))
	})

	c := NewClient(tr)
	md, err := c.Metadata(context.Background(), 0)
	if err != nil {
		t.Fatalf("Metadata: %v", err)
	}
	// The raw per-name list is returned as-is; collapsing to one entry is the
	// caller's job.
	if got := md["http_requests_total"]; len(got) != 2 || got[0].Type != "counter" || got[0].Help != "Total HTTP requests" {
		t.Errorf("http_requests_total = %+v", got)
	}
	if got := md["node_memory_Active_bytes"]; len(got) != 1 || got[0].Type != "gauge" {
		t.Errorf("node_memory_Active_bytes = %+v", got)
	}
}

func TestClient_Query_ParsesScalar(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"scalar","result":[1700000000,"42"]}}`))
	})
	c := NewClient(tr)
	res, err := c.Query(context.Background(), "scalar(sum(up))")
	if err != nil {
		t.Fatalf("Query: %v", err)
	}
	if res.ResultType != "scalar" || len(res.Series) != 1 {
		t.Fatalf("bad result: %+v", res)
	}
	s := res.Series[0]
	if len(s.Labels) != 0 {
		t.Errorf("scalar series should be labelless, got %v", s.Labels)
	}
	if len(s.DataPoints) != 1 || s.DataPoints[0].Value != 42 || s.DataPoints[0].Timestamp != 1700000000 {
		t.Errorf("datapoint: %+v", s.DataPoints)
	}
}

func TestClient_Rules_ParsesGroups(t *testing.T) {
	body := `{"status":"success","data":{"groups":[
	  {"name":"kubernetes-apps","file":"/etc/rules.yaml","rules":[
	    {"type":"alerting","name":"KubePodCrashLooping","query":"rate(kube_pod_container_status_restarts_total[5m]) > 0","duration":900,"state":"firing","health":"ok",
	     "labels":{"severity":"warning"},"annotations":{"summary":"Pod is crash looping"},
	     "alerts":[{"state":"firing","activeAt":"2026-06-12T10:00:00Z","value":"0.2","labels":{"pod":"web-1"}}]},
	    {"type":"recording","name":"namespace:container_cpu:sum","query":"sum(rate(container_cpu_usage_seconds_total[5m])) by (namespace)","health":"ok"}
	  ]}
	]}}`
	var gotType string
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/rules") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		gotType = r.URL.Query().Get("type")
		_, _ = w.Write([]byte(body))
	})

	c := NewClient(tr)
	groups, err := c.Rules(context.Background(), "alert")
	if err != nil {
		t.Fatalf("Rules: %v", err)
	}
	if gotType != "alert" {
		t.Errorf("type param = %q, want alert", gotType)
	}
	if len(groups) != 1 || groups[0].Name != "kubernetes-apps" {
		t.Fatalf("groups = %+v, want one kubernetes-apps group", groups)
	}
	rules := groups[0].Rules
	if len(rules) != 2 {
		t.Fatalf("rules = %d, want 2", len(rules))
	}
	r0 := rules[0]
	if r0.Type != "alerting" || r0.Name != "KubePodCrashLooping" {
		t.Errorf("rule[0] = %+v", r0)
	}
	if r0.State != "firing" || r0.Duration != 900 || r0.Labels["severity"] != "warning" {
		t.Errorf("rule[0] details = %+v", r0)
	}
	if len(r0.Alerts) != 1 || r0.Alerts[0].Labels["pod"] != "web-1" {
		t.Errorf("rule[0] alerts = %+v", r0.Alerts)
	}
	if rules[1].Type != "recording" || rules[1].State != "" {
		t.Errorf("rule[1] = %+v", rules[1])
	}
}

func TestClient_Query_MissingOrNullResultIsEmptyNotError(t *testing.T) {
	cases := map[string]string{
		"result key absent": `{"status":"success","data":{"resultType":"vector"}}`,
		"result is null":    `{"status":"success","data":{"resultType":"vector","result":null}}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(body))
			})
			res, err := NewClient(tr).Query(context.Background(), "up")
			if err != nil {
				t.Fatalf("a success response without data must not be a parse error: %v", err)
			}
			if res.ResultType != "vector" {
				t.Errorf("resultType = %q, want vector", res.ResultType)
			}
			if len(res.Series) != 0 {
				t.Errorf("series = %d, want 0", len(res.Series))
			}
		})
	}
}


func TestClient_Query_RejectsStringResult(t *testing.T) {
	tr := fakeProm(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"string","result":[1700000000,"hello world"]}}`))
	})
	c := NewClient(tr)
	_, err := c.Query(context.Background(), `"hello world"`)
	if err == nil {
		t.Fatal("string result should be an explicit error, not a value-less series")
	}
	if !strings.Contains(err.Error(), "string result type is not supported") {
		t.Errorf("error = %v, want the string-unsupported message", err)
	}
}
