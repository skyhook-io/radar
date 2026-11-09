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

// Losing credentials while connected is the most common long outage, and it
// publishes through its own path; it must start the outage clock too, and the
// recovery loop's later reports must keep that start.
func TestRuntimeAuthLossStartsTheOutage(t *testing.T) {
	ResetTestState()
	t.Cleanup(ResetTestState)
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "prod"})

	before := time.Now().UTC().Add(-time.Second)
	if !transitionConnectedToRuntimeAuthFailure("auth-rejected") {
		t.Fatal("expected the connected session to be demoted")
	}
	status := GetConnectionStatus()
	started, err := time.Parse(time.RFC3339, status.DisconnectedAt)
	if err != nil {
		t.Fatalf("disconnectedAt = %q, want an RFC 3339 time: %v", status.DisconnectedAt, err)
	}
	if started.Before(before.Truncate(time.Second)) || started.After(time.Now().UTC()) {
		t.Fatalf("disconnectedAt = %s, want about now", started)
	}

	SetConnectionStatus(ConnectionStatus{State: StateDisconnected, Context: "prod", ErrorType: "auth-rejected", Error: "still rejected"})
	if got := GetConnectionStatus().DisconnectedAt; got != status.DisconnectedAt {
		t.Fatalf("recovery report moved the outage start from %q to %q", status.DisconnectedAt, got)
	}
}
