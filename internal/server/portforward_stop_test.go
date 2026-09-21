package server

import (
	"context"
	"errors"
	"testing"
)

func TestPortForwardManagerStopAllClosesSessions(t *testing.T) {
	firstCtx, firstCancel := context.WithCancel(context.Background())
	secondCtx, secondCancel := context.WithCancel(context.Background())
	firstStop := make(chan struct{})
	secondStop := make(chan struct{})
	first := &PortForwardSession{ID: "pf-1", Status: "running", cancel: firstCancel, stopCh: firstStop}
	second := &PortForwardSession{ID: "pf-2", Status: "starting", cancel: secondCancel, stopCh: secondStop}
	manager := &PortForwardManager{
		sessions: map[string]*PortForwardSession{
			first.ID:  first,
			second.ID: second,
		},
	}

	manager.stopAll()

	if len(manager.sessions) != 0 {
		t.Fatalf("expected all sessions removed, got %d", len(manager.sessions))
	}
	for _, session := range []*PortForwardSession{first, second} {
		if session.Status != "stopped" {
			t.Errorf("session %s status = %q, want stopped", session.ID, session.Status)
		}
	}
	for name, ctx := range map[string]context.Context{"first": firstCtx, "second": secondCtx} {
		select {
		case <-ctx.Done():
		default:
			t.Errorf("%s session context was not canceled", name)
		}
	}
	for name, stopCh := range map[string]chan struct{}{"first": firstStop, "second": secondStop} {
		select {
		case <-stopCh:
		default:
			t.Errorf("%s session stop channel was not closed", name)
		}
	}
}

func TestPortForwardManagerFinishStart(t *testing.T) {
	t.Run("missing session", func(t *testing.T) {
		manager := &PortForwardManager{sessions: make(map[string]*PortForwardSession)}

		response, err := manager.finishStart("pf-missing")

		if response != nil {
			t.Fatalf("response = %#v, want nil", response)
		}
		if !errors.Is(err, errPortForwardStoppedWhileStarting) {
			t.Fatalf("error = %v, want %v", err, errPortForwardStoppedWhileStarting)
		}
	})

	t.Run("stopped session", func(t *testing.T) {
		session := &PortForwardSession{ID: "pf-1", Status: "stopped"}
		manager := &PortForwardManager{sessions: map[string]*PortForwardSession{session.ID: session}}

		response, err := manager.finishStart(session.ID)

		if response != nil {
			t.Fatalf("response = %#v, want nil", response)
		}
		if !errors.Is(err, errPortForwardStoppedWhileStarting) {
			t.Fatalf("error = %v, want %v", err, errPortForwardStoppedWhileStarting)
		}
	})

	t.Run("startup error", func(t *testing.T) {
		session := &PortForwardSession{ID: "pf-1", Status: "error", Error: "dial failed"}
		manager := &PortForwardManager{sessions: map[string]*PortForwardSession{session.ID: session}}

		response, err := manager.finishStart(session.ID)

		if response != nil {
			t.Fatalf("response = %#v, want nil", response)
		}
		if err == nil || err.Error() != session.Error {
			t.Fatalf("error = %v, want %q", err, session.Error)
		}
	})

	t.Run("successful startup", func(t *testing.T) {
		session := &PortForwardSession{ID: "pf-1", Status: "starting", LocalPort: 12345}
		manager := &PortForwardManager{sessions: map[string]*PortForwardSession{session.ID: session}}

		response, err := manager.finishStart(session.ID)

		if err != nil {
			t.Fatalf("finishStart() error = %v", err)
		}
		if session.Status != "running" || response.Status != "running" {
			t.Fatalf("statuses = session %q, response %q; want running", session.Status, response.Status)
		}
		if response == session {
			t.Fatal("finishStart returned the live session instead of a response snapshot")
		}
	})
}
