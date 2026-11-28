package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/prometheus"
	"github.com/skyhook-io/radar/internal/traffic"
	"github.com/skyhook-io/radar/pkg/prom"
)

func TestTrafficRoutesReturnUnavailableWhenMemberCloses(t *testing.T) {
	s := &Server{}
	match := url.QueryEscape(`{"endpoints":[{"namespace":"default","name":"example","kind":"Pod"}]}`)
	for _, tc := range []struct {
		name, method, path string
		handle             http.HandlerFunc
	}{
		{"connect", http.MethodPost, "/api/traffic/connect", s.handleTrafficConnect},
		{"flows", http.MethodGet, "/api/traffic/flows", s.handleGetTrafficFlows},
		{"records", http.MethodGet, "/api/traffic/flows/records?match=" + match, s.handleGetTrafficRecords},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Cleanup(k8s.SetTestConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected}))
			started := make(chan struct{}, 1)
			backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.name != "connect" && r.FormValue("query") == "up" {
					_, _ = w.Write([]byte(`{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"prometheus"},"value":[1700000000,"1"]}]}}`))
					return
				}
				select {
				case started <- struct{}{}:
				default:
				}
				<-r.Context().Done()
			}))
			defer backend.Close()

			client := prometheus.NewClient(nil, nil, "test", nil, false, nil)
			defer client.Close()
			client.Configure(backend.URL, nil)
			if err := client.SetWorkloadMetricsScope(prom.WorkloadMetricsScope{SingleCluster: true}, ""); err != nil {
				t.Fatal(err)
			}
			manager := traffic.NewConnectionManager(k8s.Current(), client, nil, "")
			defer manager.Close()
			t.Cleanup(k8s.SetTestIntegrations(&connectionruntime.Set{Traffic: manager}))
			if err := manager.SetActiveSource("beyla"); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()

			done := make(chan *httptest.ResponseRecorder, 1)
			go func() {
				response := httptest.NewRecorder()
				tc.handle(response, httptest.NewRequest(tc.method, tc.path, nil).WithContext(ctx))
				done <- response
			}()
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("request did not reach the backend")
			}
			_ = manager.Close()
			check := func(w *httptest.ResponseRecorder) {
				t.Helper()
				if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "Traffic connection closed") {
					t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
				}
			}
			select {
			case response := <-done:
				check(response)
			case <-time.After(3 * time.Second):
				t.Fatal("request remained blocked after close")
			}
			if tc.name == "connect" {
				for _, query := range []string{"", "?recent=1"} {
					response := httptest.NewRecorder()
					s.handleGetTrafficSources(response, httptest.NewRequest(http.MethodGet, "/api/traffic/sources"+query, nil))
					check(response)
				}
			}
		})
	}
}
