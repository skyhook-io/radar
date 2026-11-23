package k8s

import (
	"context"
	"strings"
	"testing"
)

func publishedStatus() ConnectionStatus { return GetConnectionStatus() }

func TestConnectionIdentityAdmitAndRetire(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()

	SetConnectionStatus(ConnectionStatus{State: StateConnecting, Context: "a"})
	if ServingConnectionID() != "" {
		t.Fatal("connecting must not admit a connection")
	}

	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	first := ServingConnectionID()
	if first == "" || !strings.HasPrefix(first, bootNonce+"-") {
		t.Fatalf("connected admitted %q, want an ID from this boot", first)
	}
	got := publishedStatus()
	if got.ConnectionID != first {
		t.Fatalf("published %+v, want the admitted ID", got)
	}

	// Republishing the same connection (a status refresh with no teardown) keeps it.
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	if ServingConnectionID() != first {
		t.Fatal("a repeated connected status must not admit a new connection")
	}

	// A disconnect without teardown (a liveness or Helm observation) keeps the
	// admitted connection: the ID means admitted, not healthy.
	SetConnectionStatus(ConnectionStatus{State: StateDisconnected, Context: "a", Error: "cluster unreachable"})
	if ServingConnectionID() != first {
		t.Fatal("a disconnect without teardown must not retire the connection")
	}
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})

	// The point of no return clears the ID and stops claiming connected.
	RetireServingConnection()
	got = publishedStatus()
	if ServingConnectionID() != "" || got.ConnectionID != "" || got.State != StateConnecting {
		t.Fatalf("after retiring: id=%q published=%+v", ServingConnectionID(), got)
	}

	// A→B→A: coming back to the same context name is a new admission.
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "b"})
	second := ServingConnectionID()
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	third := ServingConnectionID()
	if second == first || third == first || third == second {
		t.Fatalf("IDs must never repeat: %q %q %q", first, second, third)
	}
}

func TestConnectionAdmissionsClosedAtShutdown(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	CloseConnectionAdmissions()
	if ServingConnectionID() != "" {
		t.Fatal("shutdown must retire the serving connection")
	}
	// A late startup publication can't admit a connection after shutdown began.
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	if ServingConnectionID() != "" {
		t.Fatal("a connection was admitted after shutdown")
	}
}

func TestCaptureServingConnectionRefusesWithoutServingConnection(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	if _, err := CaptureServingConnection(); err != ErrNoServingConnection {
		t.Fatalf("no serving connection: err=%v", err)
	}
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	// Admitted with no clients installed: the capture names the connection and
	// leaves the missing clients for the handler to report.
	b, err := CaptureServingConnection()
	if err != nil || b.ID() != ServingConnectionID() {
		t.Fatalf("admitted connection: id=%q err=%v", b.ID(), err)
	}
}

func clearTestConnectionIdentity() {
	connectionStatusMu.Lock()
	serving = nil
	admissionsClosed = false
	connectionStatus = ConnectionStatus{}
	connectionStatusMu.Unlock()
}

func TestStillBoundFollowsTheServingConnection(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	admitted, err := CaptureServingConnection()
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithConnection(context.Background(), admitted)
	if !StillBound(context.Background()) || !StillBound(ctx) {
		t.Fatal("an unbound request, or one bound to the serving connection, is still bound")
	}
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	if StillBound(ctx) {
		t.Fatal("a request bound to a retired connection reported still bound")
	}
}

// Admitted again after a retirement with no rebuild, a connection is the same
// build: discovery that lands on it later is served. Only the ID changes.
func TestReadmissionKeepsTheSameConnection(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	built, first := Current(), ServingConnectionID()
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})

	if Current() != built || ServingConnectionID() == first {
		t.Fatalf("re-admission: connection replaced=%v, ID %q then %q", Current() != built, first, ServingConnectionID())
	}
	built.publishDiscovery(buildFakeDiscoveryCore(t))
	if GetResourceDiscovery() == nil {
		t.Fatal("discovery that finished after the re-admission isn't served")
	}
}
