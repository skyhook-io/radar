package k8s

import (
	"context"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/portforward"
	"k8s.io/client-go/kubernetes/fake"
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
	ctx := admitted.Bind(context.Background())
	if !StillBound(context.Background()) || !StillBound(ctx) {
		t.Fatal("an unbound request, or one bound to the serving connection, is still bound")
	}
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	if StillBound(ctx) {
		t.Fatal("a request bound to a retired connection reported still bound")
	}
}

// A connection admitted again without a rebuild gets a new ID but keeps what
// was built for it: the caches it serves were probed under that result.
func TestReadmissionKeepsTheConnectionsPermissionResult(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	result := &PermissionCheckResult{Perms: &ResourcePermissions{}}
	t.Cleanup(SetTestPermissionResult(result))

	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	first := ServingConnectionID()
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})

	if second := ServingConnectionID(); second == "" || second == first {
		t.Fatalf("re-admission IDs %q then %q, want a new one", first, second)
	}
	if Current().CachedPermissionResult() == nil {
		t.Fatal("re-admission dropped the permission result its caches were built from")
	}
}

// The typed cache is part of what a re-admitted connection keeps: it is still
// running, and a copy without it would serve nothing and leak its informers.
func TestReadmissionKeepsTheConnectionsTypedCache(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	if err := InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetResourceCache)
	built := GetResourceCache()

	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})

	if got := GetResourceCache(); got == nil || got != built {
		t.Fatalf("re-admission served cache %p, want the one built for the connection (%p)", got, built)
	}
}

// A write admitted under one ID stays bound to it: if the same build is
// retired and admitted again before the request binds, binding must not pick up
// the new ID.
func TestAdmissionKeepsTheIDItWasCapturedUnder(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	admitted, err := CaptureServingConnection()
	if err != nil {
		t.Fatal(err)
	}
	first := admitted.ID()
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	if admitted.Connection() != Current() || ServingConnectionID() == first {
		t.Fatalf("setup: want the same build under a new ID, got conn %p/%p id %q", admitted.Connection(), Current(), ServingConnectionID())
	}
	ctx := admitted.Bind(context.Background())
	if admitted.ID() != first || StillBound(ctx) {
		t.Fatalf("admission id %q (want %q), still bound %v: a request admitted before the re-admission must not be still bound", admitted.ID(), first, StillBound(ctx))
	}
}

// Restoring a snapshot taken before the same connection was admitted again
// restores the ID it was serving under, so status and serving agree.
func TestSnapshotRestoresTheAdmissionID(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	first := ServingConnectionID()
	restore := SnapshotTestConnection()
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	restore()
	if got, status := ServingConnectionID(), GetConnectionStatus().ConnectionID; got != first || status != first {
		t.Fatalf("after restore: serving %q, status %q; want both %q", got, status, first)
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

func TestReadmissionKeepsPortForwardManager(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	installedConn.Store(&Connection{})
	clearTestConnectionIdentity()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	manager := Current().PortForwards()
	RetireServingConnection()
	SetConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	if manager == nil || Current().PortForwards() != manager {
		t.Fatal("readmission replaced the forward manager")
	}
	contextSwitchMu.Lock()
	previousReset := trafficResetFunc
	trafficResetFunc = func() {
		if !manager.Closed() {
			t.Error("traffic reset ran before pending forwards were stopped")
		}
	}
	contextSwitchMu.Unlock()
	t.Cleanup(func() { contextSwitchMu.Lock(); trafficResetFunc = previousReset; contextSwitchMu.Unlock() })
	ResetAllSubsystems()
	if Current().PortForwards() == manager {
		t.Fatal("rebuild reused the closed forward manager")
	}
	if _, err := manager.Start(context.Background(), "cost", "ns", "svc", 80); err != portforward.ErrClosed {
		t.Fatalf("start after teardown: %v", err)
	}
}

func TestDisplacingUnadmittedConnectionClosesItsForwardManager(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	installedConn.Store(&Connection{})
	ResetResourceDiscovery()
	inBetween := Current().PortForwards()
	dropClusterClients()
	if _, err := inBetween.Start(t.Context(), "cost", "ns", "svc", 80); err != portforward.ErrClosed {
		t.Fatalf("displaced manager can still start: %v", err)
	}
}
