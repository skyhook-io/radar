package opencost

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

func TestUnavailableResponsesIncludeCurrency(t *testing.T) {
	tests := []struct {
		name    string
		target  string
		handler func(http.ResponseWriter, *http.Request, func() string, ScopeResolver)
	}{
		{name: "summary", target: "/opencost/summary", handler: handleSummary},
		{name: "workloads", target: "/opencost/workloads?namespace=default", handler: handleWorkloads},
		{name: "trend", target: "/opencost/trend", handler: handleTrend},
		{name: "nodes", target: "/opencost/nodes", handler: handleNodes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			w := httptest.NewRecorder()
			tt.handler(w, req, func() string { return "GBP" }, fixedScope(unrestricted))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var body struct {
				Currency string `json:"currency"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Currency != "GBP" {
				t.Errorf("currency = %q, want GBP", body.Currency)
			}
		})
	}
}

func TestConnectedResponsesIncludeCurrency(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Query().Get("query") == "up" {
			_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
			return
		}
		resultType := "vector"
		if r.URL.Path == "/api/v1/query_range" {
			resultType = "matrix"
		}
		_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"` + resultType + `","result":[]}}`))
	}))
	prometheuspkg.Initialize(nil, nil, "test")
	prometheuspkg.SetManualURL(srv.URL)
	t.Cleanup(func() {
		srv.Close()
		prometheuspkg.Reset()
		prometheuspkg.Initialize(nil, nil, "")
	})

	tests := []struct {
		name    string
		target  string
		handler func(http.ResponseWriter, *http.Request, func() string, ScopeResolver)
	}{
		{name: "summary", target: "/opencost/summary", handler: handleSummary},
		{name: "workloads", target: "/opencost/workloads?namespace=default", handler: handleWorkloads},
		{name: "trend", target: "/opencost/trend", handler: handleTrend},
		{name: "nodes", target: "/opencost/nodes", handler: handleNodes},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			tt.handler(w, httptest.NewRequest(http.MethodGet, tt.target, nil), func() string { return "GBP" }, fixedScope(unrestricted))

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, body = %s", w.Code, w.Body.String())
			}
			var body struct {
				Currency string `json:"currency"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if body.Currency != "GBP" {
				t.Errorf("currency = %q, want GBP", body.Currency)
			}
		})
	}
}
