package traffic

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCloseCancelsCarettaConnectWithoutWaitingForSourceLock(t *testing.T) {
	started := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-r.Context().Done()
	}))
	defer server.Close()

	manager := NewManager(nil, nil, "a", server.URL, nil)
	source := manager.sources["caretta"].(*CarettaSource)
	manager.activeSource = source
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	answered := make(chan bool, 1)
	go func() {
		info, err := manager.Connect(ctx)
		answered <- err == nil && info.Connected
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("Caretta Connect never reached its backend")
	}
	closed := make(chan struct{})
	go func() {
		_ = manager.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Close waited for Caretta's source lock")
	}
	select {
	case connected := <-answered:
		if connected {
			t.Fatal("retired Caretta source connected")
		}
	case <-ctx.Done():
		t.Fatal("Close did not cancel the backend request")
	}
}

func TestCloseEndsAnIdleFlowStream(t *testing.T) {
	manager := NewManager(nil, nil, "a", "", nil)
	manager.activeSource = manager.sources["caretta"]
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	flows, err := manager.StreamFlows(ctx, FlowOptions{})
	if err != nil {
		t.Fatal(err)
	}
	_ = manager.Close()
	select {
	case _, ok := <-flows:
		if ok {
			t.Fatal("received a flow after retirement")
		}
	case <-ctx.Done():
		t.Fatal("retirement left a flow stream open")
	}
}
