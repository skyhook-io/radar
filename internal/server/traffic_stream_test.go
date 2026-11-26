package server

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/connectionruntime"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestTrafficStreamBoundsWritesAndReleasesOnRetirement(t *testing.T) {
	s := serverWithTestIntegrations(t, connectionruntime.Settings{})
	if err := currentTestIntegrations().Traffic.SetActiveSource("caretta"); err != nil {
		t.Fatal(err)
	}
	release := make(chan struct{})
	defer close(release)
	w := &blockedTrafficStreamWriter{
		ResponseRecorder: httptest.NewRecorder(),
		writing:          make(chan time.Time, 1),
		release:          release,
		timeout:          make(chan struct{}),
	}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		req := httptest.NewRequest(http.MethodGet, "/api/traffic/flows/stream", nil)
		s.handleTrafficFlowsStream(w, req)
	}()
	select {
	case deadline := <-w.writing:
		if remaining := time.Until(deadline); remaining <= 0 || remaining > 5*time.Second {
			t.Fatalf("first stream write had deadline %v", deadline)
		}
	case <-finished:
		t.Fatalf("stream returned before writing: %s", w.Body)
	case <-time.After(3 * time.Second):
		t.Fatal("stream never began writing")
	}

	retired := make(chan struct{})
	go func() {
		k8s.RetireServingConnection()
		close(retired)
	}()
	select {
	case <-retired:
	case <-time.After(3 * time.Second):
		t.Fatal("a blocked SSE writer held up retirement")
	}
	close(w.timeout)
	select {
	case <-finished:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not return after the write deadline")
	}
}

type blockedTrafficStreamWriter struct {
	*httptest.ResponseRecorder
	deadline time.Time
	writing  chan time.Time
	release  <-chan struct{}
	timeout  chan struct{}
}

func (w *blockedTrafficStreamWriter) SetWriteDeadline(deadline time.Time) error {
	w.deadline = deadline
	return nil
}

func (w *blockedTrafficStreamWriter) Write([]byte) (int, error) {
	w.writing <- w.deadline
	select {
	case <-w.timeout:
	case <-w.release:
	}
	return 0, os.ErrDeadlineExceeded
}
