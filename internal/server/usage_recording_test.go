package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/usagedata"
)

// captureUsage turns the middleware on and collects what it would record.
func captureUsage(t *testing.T) *[]usagedata.APIRequest {
	t.Helper()
	var got []usagedata.APIRequest
	prevRecording, prevRecord := usageRecording, recordUsageRequest
	usageRecording = func() bool { return true }
	recordUsageRequest = func(r usagedata.APIRequest) { got = append(got, r) }
	t.Cleanup(func() { usageRecording, recordUsageRequest = prevRecording, prevRecord })
	return &got
}

func usageTestRouter(s *Server) http.Handler {
	r := chi.NewRouter()
	r.Use(s.usageMiddleware)
	// A wrapper between the middleware and the handler, as in production.
	r.Use(middleware.Compress(5))
	r.Route("/api", func(r chi.Router) {
		r.Get("/dashboard", func(w http.ResponseWriter, r *http.Request) {
			if !s.requireConnected(w) {
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		r.Post("/workloads/{kind}/{namespace}/{name}/restart", func(w http.ResponseWriter, r *http.Request) {
			if !s.requireConnectedForAction(w) {
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		r.Delete("/resources/{kind}/{namespace}/{name}", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		})
		r.Post("/prometheus/connect", func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Query().Get("fail") != "" {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
		// A route that answers 503 on a missing client without marking it.
		r.Get("/pods/{namespace}/{name}/logs", func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, "cluster client not available", http.StatusServiceUnavailable)
		})
		r.Get("/events/stream", func(w http.ResponseWriter, r *http.Request) {
			if _, ok := w.(http.Flusher); !ok {
				http.Error(w, "no flusher", http.StatusInternalServerError)
				return
			}
			w.WriteHeader(http.StatusOK)
		})
	})
	return r
}

func TestUsageMiddlewareMarksNotConnectedAnswers(t *testing.T) {
	if k8s.IsConnected() {
		t.Skip("a cluster connection is published in this process")
	}
	got := captureUsage(t)
	h := usageTestRouter(&Server{})
	for _, req := range []*http.Request{
		httptest.NewRequest(http.MethodGet, "/api/dashboard", nil),
		httptest.NewRequest(http.MethodPost, "/api/workloads/deployments/prod/api/restart", nil),
	} {
		req.Header.Set("Accept-Encoding", "gzip")
		w := httptest.NewRecorder()
		h.ServeHTTP(w, req)
		if w.Code != http.StatusServiceUnavailable {
			t.Fatalf("%s: status = %d, want 503 unchanged", req.URL.Path, w.Code)
		}
		for name := range w.Header() {
			if strings.Contains(strings.ToLower(name), "usage") || strings.Contains(strings.ToLower(name), "radar") {
				t.Errorf("%s: internal mark leaked as header %q", req.URL.Path, name)
			}
		}
	}
	if len(*got) != 2 {
		t.Fatalf("recorded %d requests, want 2", len(*got))
	}
	for _, r := range *got {
		if !r.NotConnected || r.Status != http.StatusServiceUnavailable {
			t.Errorf("%s %s: NotConnected=%v status=%d", r.Method, r.Pattern, r.NotConnected, r.Status)
		}
	}
}

func TestUsageMiddlewareReadsOriginKindAndGroup(t *testing.T) {
	got := captureUsage(t)
	h := usageTestRouter(&Server{})

	auto := httptest.NewRequest(http.MethodPost, "/api/prometheus/connect?optional=true", nil)
	auto.Header.Set(requestOriginHeader, "auto")
	h.ServeHTTP(httptest.NewRecorder(), auto)
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/prometheus/connect", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/resources/Pods/prod/payments-db?group=", nil))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodDelete, "/api/resources/services/prod/web?group=serving.knative.dev", nil))
	stream := httptest.NewRecorder()
	h.ServeHTTP(stream, httptest.NewRequest(http.MethodGet, "/api/events/stream", nil))
	if stream.Code != http.StatusOK {
		t.Fatalf("streaming handler lost http.Flusher under the usage writer: %d", stream.Code)
	}

	if len(*got) != 5 {
		t.Fatalf("recorded %d requests, want 5: %+v", len(*got), *got)
	}
	r := *got
	if !r[0].Automatic || r[1].Automatic {
		t.Errorf("automatic flags = %v, %v; want true, false", r[0].Automatic, r[1].Automatic)
	}
	if r[2].Pattern != "/api/resources/{kind}/{namespace}/{name}" || r[2].Kind != "Pods" || r[2].Group != "" {
		t.Errorf("delete request = %+v", r[2])
	}
	if r[3].Kind != "services" || r[3].Group != "serving.knative.dev" {
		t.Errorf("custom-group delete request = %+v", r[3])
	}
}

func TestUsageResponseWriterMarkThroughWrappers(t *testing.T) {
	uw := &usageResponseWriter{ResponseWriter: httptest.NewRecorder()}
	ww := middleware.NewWrapResponseWriter(uw, 1)
	markClusterNotConnected(ww)
	if !uw.notConnected {
		t.Fatal("mark did not reach the usage writer through chi's wrapper")
	}
	// Usage data off: no usage writer in the chain, nothing to mark.
	markClusterNotConnected(httptest.NewRecorder())
	markClusterNotConnected(middleware.NewWrapResponseWriter(httptest.NewRecorder(), 1))
}

func TestConnectionFailureTrackerCountsEpisodes(t *testing.T) {
	var got []string
	tr := &connectionFailureTracker{record: func(errorType string) { got = append(got, errorType) }}
	for _, st := range []k8s.ConnectionStatus{
		{State: k8s.StateConnecting, Context: "a"},
		{State: k8s.StateDisconnected, Context: "a", ErrorType: "auth"}, // startup failure: counted
		{State: k8s.StateConnecting, Context: "a"},                      // retry
		{State: k8s.StateDisconnected, Context: "a", ErrorType: "auth"}, // same episode
		{State: k8s.StateDisconnected, Context: "a", ErrorType: "timeout"},
		{State: k8s.StateConnecting, Context: "b"},
		{State: k8s.StateDisconnected, Context: "b", ErrorType: "tls"}, // failed switch: counted
		{State: k8s.StateConnecting, Context: "a"},
		{State: k8s.StateConnected, Context: "a"},
		{State: k8s.StateDisconnected, Context: "a", ErrorType: "network"}, // dropped: counted
	} {
		tr.observe(st)
	}
	want := []string{"auth", "tls", "network"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("recorded %v, want %v", got, want)
	}
}

func TestUsageDataActiveEventChecksView(t *testing.T) {
	for body, want := range map[string]int{
		`{"type":"active","minutes":5,"view":"topology"}`:                http.StatusNoContent,
		`{"type":"active","minutes":5,"view":"panel:logs"}`:              http.StatusNoContent,
		`{"type":"active","minutes":5,"view":"cnpg"}`:                    http.StatusNoContent,
		`{"type":"active","minutes":5,"view":"resources:my-crd"}`:        http.StatusBadRequest,
		`{"type":"active","minutes":5,"view":"/resources/pods/prod/db"}`: http.StatusBadRequest,
		`{"type":"ui","name":"issue_action:investigate"}`:                http.StatusNoContent,
		`{"type":"ui","name":"ui_error:CloudNativePG"}`:                  http.StatusNoContent,
		`{"type":"view","name":"panel:permissions"}`:                     http.StatusBadRequest,
	} {
		r := httptest.NewRequest(http.MethodPost, "http://localhost:9280/api/usage-data/event", strings.NewReader(body))
		r.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		(&Server{}).handleUsageEvent(w, r)
		if w.Code != want {
			t.Fatalf("body %s: status = %d, want %d", body, w.Code, want)
		}
	}
}

func TestUsageSetupVocabulary(t *testing.T) {
	setup := (&Server{}).usageSetup()
	switch setup.Prometheus {
	case "none", "discovered", "manual":
	default:
		t.Errorf("prometheus = %q", setup.Prometheus)
	}
	switch setup.TrafficSource {
	case "none", "hubble", "caretta", "istio", "beyla":
	default:
		t.Errorf("trafficSource = %q", setup.TrafficSource)
	}
	if setup.AIConfigured {
		t.Errorf("aiConfigured = true with no AI engine")
	}
}

// An unmarked 503 is "not connected" only while the cluster isn't connected;
// once connected, a 503 is a real failure and still counts.
func TestUsageMiddlewareTreatsDisconnected503AsNotConnected(t *testing.T) {
	for _, tc := range []struct {
		name      string
		connected bool
		want      bool
	}{
		{"disconnected", false, true},
		{"connected", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := captureUsage(t)
			prev := clusterConnected
			clusterConnected = func() bool { return tc.connected }
			t.Cleanup(func() { clusterConnected = prev })

			w := httptest.NewRecorder()
			usageTestRouter(&Server{}).ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/api/pods/shop/web-0/logs", nil))
			if w.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want 503", w.Code)
			}
			if len(*got) != 1 || (*got)[0].NotConnected != tc.want {
				t.Fatalf("recorded %+v, want NotConnected=%v", *got, tc.want)
			}
		})
	}
}
