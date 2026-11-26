package k8s

import (
	"context"
	"io"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/portforward"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"

	"k8s.io/client-go/kubernetes/fake"
)

type admissionTestSet chan struct{}

func (set admissionTestSet) Close() error {
	close(set)
	return nil
}

func TestAdmissionPublishesSetBeforeListenersAndClosesItOnAuthDemotion(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	clientMu.Lock()
	build := Current().fresh()
	installedConn.Store(build)
	clientMu.Unlock()
	set := make(admissionTestSet)
	var listened bool
	connectionCallbacksMu.Lock()
	saved := connectionCallbacks
	connectionCallbacks = []ConnectionChangeCallback{func(status ConnectionStatus) {
		if status.State == StateConnected {
			listened = true
			if build.Integrations() != set || status.ConnectionID != build.ID() {
				t.Fatal("listener saw an admission without its integration set")
			}
		}
	}}
	connectionCallbacksMu.Unlock()
	t.Cleanup(func() {
		connectionCallbacksMu.Lock()
		connectionCallbacks = saved
		connectionCallbacksMu.Unlock()
	})
	if !admit(t.Context(), build, ConnectionStatus{State: StateConnected}, func(context.Context, *Connection) (io.Closer, func()) {
		return set, nil
	}) || !listened {
		t.Fatal("build was not published to its listeners")
	}
	admission, err := CaptureServingConnection()
	if err != nil {
		t.Fatal(err)
	}
	if !transitionConnectedToRuntimeAuthFailure("auth-rejected") {
		t.Fatal("demotion failed")
	}
	select {
	case <-admission.Done():
	default:
		t.Fatal("demotion left the admission live")
	}
	select {
	case <-set:
	default:
		t.Fatal("demotion left the integration set live")
	}
	if build.Integrations() != nil || ServingConnectionID() != "" {
		t.Fatal("demotion left the build published")
	}
	runtimeAuthRecoveryOwed.Store(false)
}

func TestAdmitDiscardsSetPreparedForReplacedBuild(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	clientMu.Lock()
	a := Current().fresh()
	installedConn.Store(a)
	clientMu.Unlock()
	setA := make(admissionTestSet)
	started, release := make(chan struct{}), make(chan struct{})
	admittedA := make(chan bool, 1)
	go func() {
		admittedA <- admit(t.Context(), a, ConnectionStatus{State: StateConnected, Context: "a"}, func(context.Context, *Connection) (io.Closer, func()) {
			close(started)
			<-release
			return setA, nil
		})
	}()
	<-started
	clientMu.Lock()
	b := a.fresh()
	installedConn.Store(b)
	clientMu.Unlock()
	setB := make(admissionTestSet)
	ok := admit(t.Context(), b, ConnectionStatus{State: StateConnected, Context: "b"}, func(context.Context, *Connection) (io.Closer, func()) {
		return setB, nil
	})
	close(release)
	if <-admittedA || !ok || b.Integrations() != setB || a.Integrations() != nil {
		t.Fatal("the replaced build published its integration set")
	}
	select {
	case <-setA:
	default:
		t.Fatal("the unused candidate was not closed")
	}
	RetireServingConnection()
}

func publishedStatus() ConnectionStatus { return GetConnectionStatus() }

func TestConnectionIdentityAdmitAndRetire(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()

	PublishTestConnectionStatus(ConnectionStatus{State: StateConnecting, Context: "a"})
	if ServingConnectionID() != "" {
		t.Fatal("connecting must not admit a connection")
	}

	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	first := ServingConnectionID()
	if first == "" || !strings.HasPrefix(first, bootNonce+"-") {
		t.Fatalf("connected admitted %q, want an ID from this boot", first)
	}
	got := publishedStatus()
	if got.ConnectionID != first {
		t.Fatalf("published %+v, want the admitted ID", got)
	}

	// Republishing the same connection (a status refresh with no teardown) keeps it.
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	if ServingConnectionID() != first {
		t.Fatal("a repeated connected status must not admit a new connection")
	}

	// A disconnect without teardown (a liveness or Helm observation) keeps the
	// admitted connection: the ID means admitted, not healthy.
	PublishTestConnectionStatus(ConnectionStatus{State: StateDisconnected, Context: "a", Error: "cluster unreachable"})
	if ServingConnectionID() != first {
		t.Fatal("a disconnect without teardown must not retire the connection")
	}
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})

	// The point of no return clears the ID and stops claiming connected.
	RetireServingConnection()
	got = publishedStatus()
	if ServingConnectionID() != "" || got.ConnectionID != "" || got.State != StateConnecting {
		t.Fatalf("after retiring: id=%q published=%+v", ServingConnectionID(), got)
	}

	// A→B→A: coming back to the same context name is a new admission.
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "b"})
	second := ServingConnectionID()
	RetireServingConnection()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	third := ServingConnectionID()
	if second == first || third == first || third == second {
		t.Fatalf("IDs must never repeat: %q %q %q", first, second, third)
	}
}

func TestConnectionAdmissionsClosedAtShutdown(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	CloseConnectionAdmissions()
	if ServingConnectionID() != "" {
		t.Fatal("shutdown must retire the serving connection")
	}
	// A late startup publication can't admit a connection after shutdown began.
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
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
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
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
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	admitted, err := CaptureServingConnection()
	if err != nil {
		t.Fatal(err)
	}
	ctx := admitted.Bind(context.Background())
	if !StillBound(context.Background()) || !StillBound(ctx) {
		t.Fatal("an unbound request, or one bound to the serving connection, is still bound")
	}
	RetireServingConnection()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
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

	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	first := ServingConnectionID()
	RetireServingConnection()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})

	if second := ServingConnectionID(); second == "" || second == first {
		t.Fatalf("re-admission IDs %q then %q, want a new one", first, second)
	}
	if Current().CachedPermissionResult() == nil {
		t.Fatal("re-admission dropped the permission result its caches were built from")
	}
}

func TestNamespaceScopeStaysWithCapturedBuild(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "prod"})
	clientMu.RLock()
	previous := namespaceScopeOverride
	clientMu.RUnlock()
	t.Cleanup(func() { SetNamespaceScopeOverride(previous) })
	SetNamespaceScopeOverride("team-a")
	a := Current()
	ResetResourceDiscovery()
	SetNamespaceScopeOverride("team-b")
	if got := a.NamespaceScopeTarget(); got != "team-a" {
		t.Fatalf("previous build's namespace = %q", got)
	}
	if got := Current().NamespaceScopeTarget(); got != "team-b" {
		t.Fatalf("new build's namespace = %q", got)
	}
}

func TestReadmissionKeepsHelmReadPermissions(t *testing.T) {
	t.Cleanup(SnapshotTestConnection())
	clearTestConnectionIdentity()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "prod"})
	conn := Current()
	host := pkgauth.NewPermissionCache()
	perms := PermissionCacheFor(WithConnection(t.Context(), conn), host)
	perms.Set("alice", nil, &pkgauth.UserPermissions{AllowedNamespaces: []string{"team-a"}})
	RetireServingConnection()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "prod"})
	if got := PermissionCacheFor(CaptureConnection(t.Context()), host).Get("alice", nil); got == nil || !slices.Equal(got.AllowedNamespaces, []string{"team-a"}) {
		t.Fatalf("lost permissions on readmission: %v", got)
	}
	if got := PermissionCacheFor(WithConnection(t.Context(), conn.fresh()), host).Get("alice", nil); got != nil {
		t.Fatalf("fresh build inherited permissions: %v", got)
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

	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	RetireServingConnection()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})

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
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	admitted, err := CaptureServingConnection()
	if err != nil {
		t.Fatal(err)
	}
	first := admitted.ID()
	RetireServingConnection()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
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
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	first := ServingConnectionID()
	restore := SnapshotTestConnection()
	RetireServingConnection()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
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
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	built, first := Current(), ServingConnectionID()
	RetireServingConnection()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})

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
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
	manager := Current().PortForwards()
	RetireServingConnection()
	PublishTestConnectionStatus(ConnectionStatus{State: StateConnected, Context: "a"})
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
