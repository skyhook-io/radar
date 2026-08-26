package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	internalopencost "github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
)

func TestOpenCostDetailResponsesIncludeCurrencyWhenUnavailable(t *testing.T) {
	s := &Server{openCostCurrency: internalopencost.NewCurrencyResolver("GBP")}
	tests := []struct {
		name   string
		method string
		target string
		body   string
		serve  func(http.ResponseWriter, *http.Request)
		params map[string]string
	}{
		{
			name: "workload current", method: http.MethodGet,
			target: "/api/opencost/workload/Deployment/default/nginx",
			serve:  s.handleOpenCostWorkload,
			params: map[string]string{"kind": "Deployment", "namespace": "default", "name": "nginx"},
		},
		{
			name: "workload trend", method: http.MethodGet,
			target: "/api/opencost/workload/Deployment/default/nginx/trend?range=24h",
			serve:  s.handleOpenCostWorkloadTrend,
			params: map[string]string{"kind": "Deployment", "namespace": "default", "name": "nginx"},
		},
		{
			name: "application current", method: http.MethodPost,
			target: "/api/opencost/application",
			body:   `{"workloads":[{"kind":"Deployment","namespace":"default","name":"nginx"}]}`,
			serve:  s.handleOpenCostApplication,
		},
		{
			name: "application trend", method: http.MethodPost,
			target: "/api/opencost/application/trend",
			body:   `{"range":"24h","workloads":[{"kind":"Deployment","namespace":"default","name":"nginx"}]}`,
			serve:  s.handleOpenCostApplicationTrend,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(tt.method, tt.target, strings.NewReader(tt.body))
			if len(tt.params) > 0 {
				rctx := chi.NewRouteContext()
				for key, value := range tt.params {
					rctx.URLParams.Add(key, value)
				}
				req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, rctx))
			}
			w := httptest.NewRecorder()
			tt.serve(w, req)
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

func TestOpenCostConnectionFailureReason(t *testing.T) {
	if got := internalopencost.ConnectionFailureReason(fmt.Errorf("wrapped: %w", prometheuspkg.ErrPrometheusNotFound)); got != pkgopencost.ReasonNoPrometheus {
		t.Fatalf("not-found reason = %q, want %q", got, pkgopencost.ReasonNoPrometheus)
	}
	if got := internalopencost.ConnectionFailureReason(errors.New("manual URL unreachable")); got != pkgopencost.ReasonQueryError {
		t.Fatalf("connection-error reason = %q, want %q", got, pkgopencost.ReasonQueryError)
	}
}

func TestFocusOpenCostWorkloadScaledToZeroReturnsCurrentZero(t *testing.T) {
	resp := focusOpenCostWorkload(&pkgopencost.WorkloadCostResponse{
		Available: false,
		Reason:    pkgopencost.ReasonNoMetrics,
		Namespace: "default",
	}, "Deployment", "default", "checkout", 0)

	if !resp.Available {
		t.Fatalf("expected scaled-to-zero workload to be available, got %+v", resp)
	}
	if resp.Current == nil {
		t.Fatalf("expected zero current row, got nil")
	}
	if resp.Current.Name != "checkout" || resp.Current.Kind != "Deployment" {
		t.Fatalf("current row = %s/%s, want checkout/Deployment", resp.Current.Name, resp.Current.Kind)
	}
	if resp.Current.HourlyCost != 0 || resp.Current.Replicas != 0 {
		t.Fatalf("current row should be zero cost and zero replicas, got %+v", resp.Current)
	}
}

func TestFocusOpenCostWorkloadMissingTargetWithReplicasReturnsNoMetrics(t *testing.T) {
	resp := focusOpenCostWorkload(&pkgopencost.WorkloadCostResponse{
		Available: true,
		Namespace: "default",
		Workloads: []pkgopencost.WorkloadCost{
			{Name: "other", Kind: "Deployment", HourlyCost: 1, Replicas: 1},
		},
	}, "Deployment", "default", "checkout", 2)

	if resp.Available {
		t.Fatalf("expected missing workload with desired replicas to be unavailable, got %+v", resp)
	}
	if resp.Reason != pkgopencost.ReasonNoMetrics {
		t.Fatalf("Reason = %q, want %q", resp.Reason, pkgopencost.ReasonNoMetrics)
	}
}

func TestFocusOpenCostWorkloadHandlesNilResponse(t *testing.T) {
	resp := focusOpenCostWorkload(nil, "Deployment", "default", "web", 1)
	if resp.Available || resp.Reason != pkgopencost.ReasonQueryError {
		t.Fatalf("unexpected response: %#v", resp)
	}
}
