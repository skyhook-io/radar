package substrate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	"go.ate.dev/substrate/api/ateapi/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/httpstream"
	"k8s.io/apimachinery/pkg/util/httpstream/spdy"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	pfclient "k8s.io/client-go/tools/portforward"

	"github.com/skyhook-io/radar/internal/portforward"
)

// stubControl answers the snapshot's list RPCs. With block set, ListAtespaces
// waits for release. With scoped set, it is an identity with viewer on
// atespace "team" only: no atespace list, no list across atespaces.
type stubControl struct {
	ateapi.ControlClient
	entered chan struct{}
	release chan struct{}
	scoped  bool
	actor   string
	// during runs inside the read, as a context switch landing mid-read.
	during func()
	// unavailable fails ListAtespaces as an unreachable API.
	unavailable bool
	listError   error
}

func (c *stubControl) ListAtespaces(ctx context.Context, _ *ateapi.ListAtespacesRequest, _ ...grpc.CallOption) (*ateapi.ListAtespacesResponse, error) {
	if c.entered != nil {
		close(c.entered)
		select {
		case <-c.release:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	if c.unavailable {
		return nil, status.Error(codes.Unavailable, "connection refused")
	}
	if c.listError != nil {
		return nil, c.listError
	}
	if c.scoped {
		return nil, status.Error(codes.PermissionDenied, "global viewer required")
	}
	return &ateapi.ListAtespacesResponse{Atespaces: []*ateapi.Atespace{{Metadata: &ateapi.ResourceMetadata{Name: "team"}}}}, nil
}

func (c *stubControl) ListWorkers(context.Context, *ateapi.ListWorkersRequest, ...grpc.CallOption) (*ateapi.ListWorkersResponse, error) {
	if c.during != nil {
		c.during()
	}
	if c.listError != nil {
		return nil, c.listError
	}
	return &ateapi.ListWorkersResponse{}, nil
}

func (c *stubControl) allowed(atespace string) error {
	if c.listError != nil {
		return c.listError
	}
	if c.scoped && atespace != "team" {
		return status.Error(codes.PermissionDenied, "viewer on team only")
	}
	return nil
}

func (c *stubControl) ListActors(_ context.Context, r *ateapi.ListActorsRequest, _ ...grpc.CallOption) (*ateapi.ListActorsResponse, error) {
	if err := c.allowed(r.GetAtespace()); err != nil {
		return nil, err
	}
	name := c.actor
	if name == "" {
		name = "allowed"
	}
	return &ateapi.ListActorsResponse{Actors: []*ateapi.Actor{{Metadata: &ateapi.ResourceMetadata{Atespace: "team", Name: name}}}}, nil
}

func (c *stubControl) ListActorTemplates(_ context.Context, r *ateapi.ListActorTemplatesRequest, _ ...grpc.CallOption) (*ateapi.ListActorTemplatesResponse, error) {
	if err := c.allowed(r.GetAtespace()); err != nil {
		return nil, err
	}
	return &ateapi.ListActorTemplatesResponse{}, nil
}

func stubManager(t *testing.T, kube func() (Kube, error)) (*Manager, Kube, Settings) {
	t.Helper()
	k, _ := kube()
	s := Settings{Identity: IdentityShared}
	m := NewManager(&Store{Path: filepath.Join(t.TempDir(), "substrate.json")}, kube)
	_, _, revision, err := m.Store.GetRevision(k.Binding)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Store.Update(context.Background(), revision, k.Binding, &Saved{Target: k.Fingerprint, Settings: s}); err != nil {
		t.Fatal(err)
	}
	m.Select(k)
	return m, k, s
}

func fixedKube() (Kube, error) { return Kube{Binding: "ctx-a", Fingerprint: "cluster-a"}, nil }

func TestResetRetiresInFlightRead(t *testing.T) {
	m, k, s := stubManager(t, fixedKube)
	blocked := &stubControl{entered: make(chan struct{}), release: make(chan struct{})}
	m.conn = &conn{key: connKey(k, s), client: blocked}

	type answer struct {
		snap *Snapshot
		tier APITier
	}
	first := make(chan answer, 1)
	go func() {
		snap, tier := m.Snapshot(context.Background())
		first <- answer{snap, tier}
	}()
	<-blocked.entered
	m.Reset()

	// A caller after Reset does not join the retired flight: it reads under
	// the new generation, through the new connection.
	m.mu.Lock()
	m.conn = &conn{key: connKey(k, s), client: &stubControl{actor: "after-reset"}}
	m.mu.Unlock()
	snap, tier := m.Snapshot(context.Background())
	if tier.State != TierConnected || len(snap.Actors) != 1 || snap.Actors[0].Name != "after-reset" {
		t.Fatalf("post-Reset read = %+v %+v", tier, snap)
	}

	close(blocked.release)
	var old answer
	select {
	case old = <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("the retired read did not finish")
	}
	if old.snap != nil || old.tier.Code != CodeContextChanged {
		t.Errorf("the retired read must be discarded, got %+v", old.tier)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.good == nil || len(m.good.snap.Actors) != 1 || m.good.snap.Actors[0].Name != "after-reset" {
		t.Error("the retired read overwrote the current generation's cache")
	}
}

func TestReadDiscardedWhenTheClusterConnectionMoves(t *testing.T) {
	var moved atomic.Bool
	kube := func() (Kube, error) {
		k, _ := fixedKube()
		if moved.Load() {
			k.Generation = "after-switch"
		}
		return k, nil
	}
	m, k, s := stubManager(t, kube)
	m.conn = &conn{key: connKey(k, s), client: &stubControl{during: func() { moved.Store(true) }}}
	snap, tier := m.Snapshot(context.Background())
	if snap != nil || tier.Code != CodeContextChanged {
		t.Fatalf("a read whose capture moved = %+v", tier)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.good != nil && m.good.snap != nil {
		t.Error("a read under a moved capture was cached")
	}
}

func TestOperationCancelledDiscardsRead(t *testing.T) {
	op, cancel := context.WithCancel(context.Background())
	kube := func() (Kube, error) { return Kube{Binding: "ctx-a", Fingerprint: "cluster-a", Operation: op}, nil }
	m, k, s := stubManager(t, kube)
	blocked := &stubControl{entered: make(chan struct{}), release: make(chan struct{})}
	m.conn = &conn{key: connKey(k, s), client: blocked}
	done := make(chan APITier, 1)
	go func() {
		_, tier := m.Snapshot(context.Background())
		done <- tier
	}()
	<-blocked.entered
	cancel()
	select {
	case tier := <-done:
		if tier.State == TierConnected {
			t.Errorf("a read whose context operation ended was served: %+v", tier)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ending the operation did not cancel the read")
	}
}

// A probe shares Radar's one Substrate port-forward, so it runs only while no
// client dial does, and not at all for a context operation that ended.
func TestProbeSerializesWithDialsAndItsOperation(t *testing.T) {
	op, cancel := context.WithCancel(context.Background())
	kube := func() (Kube, error) { return Kube{Binding: "ctx-a", Fingerprint: "cluster-a", Operation: op}, nil }
	m, k, _ := stubManager(t, kube)
	// A closed port: the dial is lazy and the read fails at once.
	probeSettings := Settings{Identity: IdentityTokenFile, TokenFile: filepath.Join(t.TempDir(), "token"), Endpoint: "127.0.0.1:1"}

	m.dialMu.Lock()
	done := make(chan *TierError, 1)
	go func() { done <- m.Probe(context.Background(), k, probeSettings) }()
	select {
	case <-done:
		t.Fatal("the probe ran while a client dial held the forward")
	case <-time.After(100 * time.Millisecond):
	}
	m.dialMu.Unlock()
	select {
	case err := <-done:
		if err == nil || err.Code == CodeContextChanged {
			t.Errorf("an unreachable endpoint = %+v", err)
		}
	case <-time.After(ProbeTimeout + time.Second):
		t.Fatal("the probe did not run after the dial finished")
	}

	cancel()
	if err := m.Probe(context.Background(), k, probeSettings); err == nil || err.Code != CodeContextChanged {
		t.Errorf("a probe for an ended operation must not dial: %+v", err)
	}
}

func TestScopedIdentityReadsNamedAtespaces(t *testing.T) {
	c := &conn{client: &stubControl{scoped: true}}
	snap, tierErr := readSnapshot(context.Background(), c, time.Now(), nil)
	if tierErr != nil {
		t.Fatal(tierErr)
	}
	if snap.Coverage.Actors.State != CoverageDenied {
		t.Fatalf("without a scope, an identity that cannot list atespaces reads nothing: %+v", snap.Coverage.Actors)
	}

	snap, tierErr = readSnapshot(context.Background(), c, time.Now(), []string{"team", "other"})
	if tierErr != nil {
		t.Fatal(tierErr)
	}
	actors := snap.Coverage.Actors
	if actors.State != CoveragePartial || actors.Code != CodeDenied || !slices.Equal(actors.ReadAtespaces, []string{"team"}) ||
		!slices.Equal(actors.DeniedAtespaces, []string{"other"}) || len(snap.Actors) != 1 {
		t.Errorf("scoped actors = %+v (%d read)", actors, len(snap.Actors))
	}
	if !slices.Equal(snap.ScopedAtespaces, []string{"other", "team"}) || snap.Coverage.Atespaces.State != CoverageDenied {
		t.Errorf("scope %v, atespaces coverage %+v", snap.ScopedAtespaces, snap.Coverage.Atespaces)
	}

	snap, _ = readSnapshot(context.Background(), c, time.Now(), []string{"team"})
	if cov := snap.Coverage.Templates; cov.State != CoveragePartial || cov.Code != CodeScoped || !slices.Equal(cov.ReadAtespaces, []string{"team"}) {
		t.Errorf("every scoped atespace read is still partial: atespaces outside the scope are unknown: %+v", cov)
	}
}

func TestSettingsAtespacesValidated(t *testing.T) {
	for _, bad := range [][]string{{"Team"}, {"team", "team"}, {"a/b"}, {""}} {
		if err := (Settings{Identity: IdentityShared, Atespaces: bad}).Validate(); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
	if err := (Settings{Identity: IdentityShared, Atespaces: []string{"team", "research-2"}}).Validate(); err != nil {
		t.Error(err)
	}
}

func TestBusyClusterCapturePreservesSelection(t *testing.T) {
	m, k, s := stubManager(t, fixedKube)
	m.conn = &conn{key: connKey(k, s), client: &stubControl{actor: "warm"}}
	if _, tier := m.Snapshot(context.Background()); tier.State != TierConnected {
		t.Fatalf("seed: %+v", tier)
	}
	gen, ctx := m.generation()
	good := m.good
	m.Kube = func() (Kube, error) { return Kube{}, ErrClusterChanging }
	if snap, tier := m.Peek(context.Background()); snap != nil || tier.Code != CodeContextChanged {
		t.Fatalf("busy capture: %+v", tier)
	}
	next, _ := m.generation()
	if next != gen || ctx.Err() != nil || m.good != good {
		t.Fatal("transient configuration lock retired a healthy selection")
	}
	m.Kube = fixedKube
	if snap, tier := m.Snapshot(context.Background()); snap == nil || tier.State != TierConnected {
		t.Fatalf("recovered capture: %+v", tier)
	}
}

func TestPeekKeepsTheLastFailureWhileARetryRuns(t *testing.T) {
	m, k, s := stubManager(t, fixedKube)
	now := time.Now()
	m.Now = func() time.Time { return now }
	m.conn = &conn{key: connKey(k, s), client: &stubControl{unavailable: true}}
	if _, tier := m.Snapshot(context.Background()); tier.State != TierFailed || tier.Code != CodeUnreachable {
		t.Fatalf("first read = %+v", tier)
	}

	// Past the failure's freshness, a status peek starts a retry; it blocks here.
	blocked := &stubControl{entered: make(chan struct{}), release: make(chan struct{}), unavailable: true}
	m.mu.Lock()
	m.conn = &conn{key: connKey(k, s), client: blocked}
	m.mu.Unlock()
	now = now.Add(Freshness + time.Second)
	_, tier := m.Peek(context.Background())
	if tier.State != TierFailed || tier.Code != CodeUnreachable || !tier.Refreshing {
		t.Errorf("a retry after a failure must keep the failure, refreshing, not read as a first connect: %+v", tier)
	}
	<-blocked.entered
	close(blocked.release)
}

func TestFailedProbePreservesPublishedForward(t *testing.T) {
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/namespaces/ate-system/services/api":
			json.NewEncoder(w).Encode(&corev1.Service{
				TypeMeta:   metav1.TypeMeta{APIVersion: "v1", Kind: "Service"},
				ObjectMeta: metav1.ObjectMeta{Namespace: SystemNamespace, Name: APIService},
				Spec:       corev1.ServiceSpec{Selector: map[string]string{"app": "api"}, Ports: []corev1.ServicePort{{Port: APIPort}}},
			})
		case "/api/v1/namespaces/ate-system/pods":
			json.NewEncoder(w).Encode(&corev1.PodList{
				TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"},
				Items: []corev1.Pod{{
					ObjectMeta: metav1.ObjectMeta{Namespace: SystemNamespace, Name: "api-pod"},
					Status:     corev1.PodStatus{Phase: corev1.PodRunning},
				}},
			})
		case "/api/v1/namespaces/ate-system/pods/api-pod/portforward":
			if r.Method != http.MethodPost {
				http.Error(w, "SPDY upgrade only", http.StatusBadRequest)
				return
			}
			if _, err := httpstream.Handshake(r, w, []string{pfclient.PortForwardProtocolV1Name}); err != nil {
				t.Error(err)
				return
			}
			stream := spdy.NewResponseUpgrader().UpgradeResponse(w, r, func(httpstream.Stream, <-chan struct{}) error { return nil })
			if stream != nil {
				<-stream.CloseChan()
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	t.Cleanup(func() { portforward.Stop(portforward.OwnerSubstrate) })
	cfg := &rest.Config{Host: api.URL}
	client, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	kube := func() (Kube, error) {
		return Kube{Binding: "ctx-a", Context: "ctx-a", Fingerprint: "cluster-a", Client: client, Config: cfg}, nil
	}
	m, k, settings := stubManager(t, kube)

	// With no ClusterTrustBundle API, the probe fails after establishing a
	// tunnel. It must release this tunnel when nobody else uses it.
	if err := m.Probe(t.Context(), k, settings); err == nil || err.Code != CodeTLS {
		t.Fatalf("probe = %+v, want TLS failure", err)
	}
	if portforward.GetConnectionInfo(portforward.OwnerSubstrate).Connected {
		t.Fatal("failed probe left its own forward running")
	}

	port, err := startForward(t.Context(), k)
	if err != nil {
		t.Fatal(err)
	}
	m.conn = &conn{key: connKey(k, settings), forwardPort: port, client: &stubControl{}}
	if err := m.Probe(t.Context(), k, settings); err == nil || err.Code != CodeTLS {
		t.Fatalf("probe with live forward = %+v, want TLS failure", err)
	}
	if !m.conn.alive() {
		t.Fatal("failed probe stopped the published connection's forward")
	}
}

func TestSaveRevisionRetiresSelectedSettings(t *testing.T) {
	m, k, old := stubManager(t, fixedKube)
	_, selected, _, oldGen, oldCtx := m.settings(t.Context())
	if selected == nil || selected.Identity != old.Identity {
		t.Fatalf("selected before save = %+v", selected)
	}
	_, _, revision, err := m.Store.GetRevision(k.Binding)
	if err != nil {
		t.Fatal(err)
	}
	next := Settings{Identity: IdentityShared, Endpoint: "api.example:443"}
	target := Target{Binding: k.Binding, Fingerprint: k.Fingerprint}
	if err := m.SaveRevision(t.Context(), target, revision, &next); err != nil {
		t.Fatal(err)
	}
	savedGen, _ := m.generation()
	_, selected, tier, selectedGen, _ := m.settings(t.Context())
	if selected == nil || selected.Endpoint != next.Endpoint || oldCtx.Err() == nil || oldGen == selectedGen || savedGen != selectedGen {
		t.Fatalf("settings after save = %+v, tier %+v, generation %d (was %d, save set %d)", selected, tier, selectedGen, oldGen, savedGen)
	}
	if c, err := m.client(t.Context(), k, old, oldGen); c != nil || !errors.Is(err, errRetired) {
		t.Errorf("dial with retired settings = %v, %v", c, err)
	}
}
