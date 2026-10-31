package k8s

import (
	"testing"
	"time"
)

func TestOutageStartHoldsThroughRetriesAndClearsOnConnect(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 14, 2, 0, 0, time.UTC)
	t1 := t0.Add(10 * time.Minute)
	connected := ConnectionStatus{State: StateConnected, Context: "prod"}
	down := ConnectionStatus{State: StateDisconnected, Context: "prod", ErrorType: "network"}

	started := outageStart(connected, down, t0)
	if started != "2026-10-30T14:02:00Z" {
		t.Fatalf("outage start = %q", started)
	}
	down.DisconnectedAt = started

	retrying := ConnectionStatus{State: StateConnecting, Context: "prod"}
	if got := outageStart(down, retrying, t1); got != started {
		t.Fatalf("retry reset the outage start: %q", got)
	}
	retrying.DisconnectedAt = started
	stillDown := ConnectionStatus{State: StateDisconnected, Context: "prod", ErrorType: "timeout"}
	if got := outageStart(retrying, stillDown, t1); got != started {
		t.Fatalf("a different error reset the outage start: %q", got)
	}
	if got := outageStart(down, connected, t1); got != "" {
		t.Fatalf("connected kept an outage start: %q", got)
	}
}

func TestOutageStartRestartsForAnotherContext(t *testing.T) {
	t0 := time.Date(2026, 10, 30, 14, 2, 0, 0, time.UTC)
	t1 := t0.Add(time.Hour)
	downA := ConnectionStatus{State: StateDisconnected, Context: "a", DisconnectedAt: t0.Format(time.RFC3339)}
	downB := ConnectionStatus{State: StateDisconnected, Context: "b"}
	if got := outageStart(downA, downB, t1); got != t1.Format(time.RFC3339) {
		t.Fatalf("outage start for a new context = %q, want %q", got, t1.Format(time.RFC3339))
	}
}

func TestOutageStartNotSetWhileFirstConnecting(t *testing.T) {
	if got := outageStart(ConnectionStatus{}, ConnectionStatus{State: StateConnecting, Context: "prod"}, time.Now()); got != "" {
		t.Fatalf("startup connecting set an outage start: %q", got)
	}
}
