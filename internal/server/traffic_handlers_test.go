package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/traffic"
)

func TestTrafficRoutesReturnUnavailableWhenMemberCloses(t *testing.T) {
	started := make(chan struct{}, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-r.Context().Done()
	}))
	defer backend.Close()
	manager := traffic.NewManager(nil, nil, "test", backend.URL, nil)
	defer manager.Close()
	t.Cleanup(k8s.SetTestIntegrations(&connectionruntime.Set{Traffic: manager}))
	if err := manager.SetActiveSource("caretta"); err != nil {
		t.Fatal(err)
	}
	s := &Server{}
	connect := httptest.NewRequest(http.MethodPost, "/api/traffic/connect", nil)
	done := make(chan *httptest.ResponseRecorder, 1)
	go func() {
		response := httptest.NewRecorder()
		s.handleTrafficConnect(response, connect)
		done <- response
	}()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("traffic connect did not reach the backend")
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
		t.Fatal("traffic connect remained blocked after close")
	}
	for _, query := range []string{"", "?recent=1"} {
		response := httptest.NewRecorder()
		s.handleGetTrafficSources(response, httptest.NewRequest(http.MethodGet, "/api/traffic/sources"+query, nil))
		check(response)
	}
}
