package server

import (
	"context"
	"testing"
)

func TestStopAllPortForwardsCancelsSessionAndClosesForwarder(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	stopCh := make(chan struct{})

	pfManager.mu.Lock()
	previousSessions := pfManager.sessions
	previousNextID := pfManager.nextID
	pfManager.sessions = map[string]*PortForwardSession{
		"pf-test": {
			ID:     "pf-test",
			cancel: cancel,
			stopCh: stopCh,
		},
	}
	pfManager.mu.Unlock()
	t.Cleanup(func() {
		cancel()
		pfManager.mu.Lock()
		pfManager.sessions = previousSessions
		pfManager.nextID = previousNextID
		pfManager.mu.Unlock()
	})

	StopAllPortForwards()

	select {
	case <-ctx.Done():
	default:
		t.Error("session context was not canceled")
	}
	select {
	case <-stopCh:
	default:
		t.Error("port-forward stop channel was not closed")
	}
	if got := GetPortForwardCount(); got != 0 {
		t.Errorf("active sessions = %d, want 0", got)
	}
}
