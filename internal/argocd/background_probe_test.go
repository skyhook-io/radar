package argocd

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/connections"
	"github.com/skyhook-io/radar/pkg/argoapi"
)

// mustConnect runs, synchronously, the probe a read would start in the
// background.
func mustConnect(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Probe(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
}

// waitForBackgroundProbe waits until no background probe is running.
func waitForBackgroundProbe(t *testing.T, m *Manager) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for {
		m.mu.Lock()
		running := m.background != nil
		m.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("background probe still running after 30s")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// switchableArgo is an argocd-server whose behavior a test changes while it
// runs. In slow mode every request waits until the test ends or the client
// gives up.
type switchableArgo struct {
	token    string
	slow     atomic.Bool
	reject   atomic.Bool // token no longer accepted
	failApps atomic.Bool
	hangUp   atomic.Bool // application reads lose their connection
	// slowUserInfo: only the token check is slow.
	slowUserInfo atomic.Bool
	// down: every connection is closed before an answer, as when nothing serves.
	down atomic.Bool
	// gateway: an ingress in front of argocd-server answers 502 for it, as
	// while argocd-server restarts.
	gateway  atomic.Bool
	requests atomic.Int64
	release  chan struct{}
}

func newSwitchableArgo(t *testing.T, token string) (*switchableArgo, *httptest.Server) {
	a := &switchableArgo{token: token, release: make(chan struct{})}
	mux := http.NewServeMux()
	// wait holds a request while the server is slow. It answers once the
	// test makes the server fast again, and drops it when the test ends or
	// the client gives up.
	wait := func(r *http.Request) bool {
		a.requests.Add(1)
		for a.slow.Load() {
			select {
			case <-a.release:
				return false
			case <-r.Context().Done():
				return false
			case <-time.After(10 * time.Millisecond):
			}
		}
		return true
	}
	authorized := func(r *http.Request) bool {
		return !a.reject.Load() && r.Header.Get("Authorization") == "Bearer "+a.token
	}
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		if a.down.Load() {
			wait(r) // counts the request, and holds it while the server is slow
			if conn, _, err := w.(http.Hijacker).Hijack(); err == nil {
				_ = conn.Close()
			}
			return
		}
		if wait(r) {
			_, _ = w.Write([]byte(`{"Version":"v3.5.2"}`))
		}
	})
	mux.HandleFunc("/api/v1/session/userinfo", func(w http.ResponseWriter, r *http.Request) {
		if a.slowUserInfo.Load() {
			select {
			case <-a.release:
			case <-r.Context().Done():
			}
			return
		}
		if !wait(r) {
			return
		}
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte(`{"loggedIn":true}`))
	})
	mux.HandleFunc("/api/v1/applications/", func(w http.ResponseWriter, r *http.Request) {
		if !wait(r) {
			return
		}
		if !authorized(r) {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if a.failApps.Load() {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		if a.hangUp.Load() {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = w.Write([]byte(`{"metadata":{"uid":"u1"},"status":{"resources":[]},"items":[]}`))
	})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a.gateway.Load() {
			a.requests.Add(1)
			w.Header().Set("Content-Type", "text/html")
			w.WriteHeader(http.StatusBadGateway)
			_, _ = w.Write([]byte("<html><body>502 Bad Gateway</body></html>"))
			return
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(func() {
		close(a.release)
		srv.Close()
	})
	return a, srv
}

var billing = argoapi.ApplicationQuery{AppName: "billing", AppNamespace: "argocd"}

// A page read with no connection yet must not wait for the connect: it
// starts it in the background and says so at once.
func TestRead_AnswersAtOnceWhileConnecting(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	a.slow.Store(true)
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	start := time.Now()
	_, err := m.ApplicationHealthCached(ctx, billing)
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("read waited %s for the connect", elapsed)
	}
	if !errors.Is(err, ErrConnecting) {
		t.Fatalf("read = %v, want ErrConnecting", err)
	}
	if !m.Connecting() {
		t.Fatal("a connect must be running in the background")
	}

	a.slow.Store(false)
	m.SetConfig(srv.URL, "argo-secret", false, true) // stops the hung probe
	m.Get()                                          // connects in the background
	waitForBackgroundProbe(t, m)
	if _, err := m.ApplicationHealthCached(ctx, billing); err != nil {
		t.Fatalf("read once connected = %v", err)
	}
}

// Settings in place at start (or after a context switch or a save) are
// connected right away, so the first page read on a healthy server gets
// Argo's answer instead of "connecting". Nothing configured: nothing runs.
func TestPrewarm_FirstReadGetsArgosAnswer(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{ArgoCDURL: srv.URL, ArgoCDToken: "argo-secret"})
	m.Prewarm()
	waitForBackgroundProbe(t, m)
	if _, err := m.ApplicationHealthCached(context.Background(), billing); err != nil {
		t.Fatalf("first read = %v, want Argo's answer", err)
	}

	// Already connected: no second probe.
	before := a.requests.Load()
	m.Prewarm()
	waitForBackgroundProbe(t, m)
	if got := a.requests.Load() - before; got != 0 {
		t.Fatalf("Prewarm on a live connection sent %d requests, want 0", got)
	}

	idle := newTestManager(config.Config{})
	idle.Prewarm()
	idle.mu.Lock()
	running := idle.background != nil
	idle.mu.Unlock()
	if running {
		t.Fatal("Prewarm must not run discovery when nothing is configured")
	}
}

// A prewarm that failed before any page asked doesn't hold the first page
// back for the retry interval: that read connects again at once, so a server
// that came back meanwhile is used straight away.
func TestPrewarm_FailureDoesNotThrottleTheFirstRead(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{ArgoCDURL: srv.URL, ArgoCDToken: "argo-secret"})
	a.down.Store(true)
	m.Prewarm()
	waitForBackgroundProbe(t, m)
	if m.LastProbeError() == nil {
		t.Fatal("the prewarm against a down server must fail")
	}

	a.down.Store(false)
	if _, err := m.ApplicationHealthCached(context.Background(), billing); err == nil {
		t.Fatal("the first read can't have Argo's answer yet")
	}
	waitForBackgroundProbe(t, m)
	if _, err := m.ApplicationHealthCached(context.Background(), billing); err != nil {
		t.Fatalf("read after the immediate reconnect = %v, want Argo's answer", err)
	}

	// Reads that already saw a failure stay throttled.
	a.down.Store(true)
	m.mu.Lock()
	fwd := m.dropConnectionLocked()
	m.mu.Unlock()
	if fwd != nil {
		fwd.stop()
	}
	_, _ = m.ApplicationHealthCached(context.Background(), billing) // starts a connect that fails
	waitForBackgroundProbe(t, m)
	before := a.requests.Load()
	_, _ = m.ApplicationHealthCached(context.Background(), billing)
	waitForBackgroundProbe(t, m)
	if got := a.requests.Load() - before; got != 0 {
		t.Fatalf("a read after a failure it saw sent %d requests, want 0 (throttled)", got)
	}
}

// A prewarm whose token was rejected, fixed before any page asks: the first
// read connects again, and while that runs the page is told to ask again,
// so it picks up Argo's answer.
func TestPrewarm_RejectedThenFixedTokenTellsThePageToAskAgain(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{ArgoCDURL: srv.URL, ArgoCDToken: "argo-secret"})
	a.reject.Store(true)
	m.Prewarm()
	waitForBackgroundProbe(t, m)
	if !errors.Is(m.LastProbeError(), ErrTokenInvalid) {
		t.Fatalf("prewarm = %v, want a rejected token", m.LastProbeError())
	}

	a.reject.Store(false)
	a.slow.Store(true) // hold the reconnect so the page's flags can be read
	if _, err := m.ApplicationHealthCached(context.Background(), billing); err == nil {
		t.Fatal("the first read can't have Argo's answer yet")
	}
	if !m.Retrying() {
		t.Fatal("while the reconnect runs the page must be told to ask again")
	}
	a.slow.Store(false)
	waitForBackgroundProbe(t, m)
	if _, err := m.ApplicationHealthCached(context.Background(), billing); err != nil {
		t.Fatalf("read after the reconnect = %v, want Argo's answer", err)
	}
}

// Settings (or the insights check) asking while a startup connect runs sees
// its failure: the page read after it stays throttled like any other.
func TestPrewarm_FailureSeenBySettingsKeepsReadsThrottled(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{ArgoCDURL: srv.URL, ArgoCDToken: "argo-secret"})
	a.down.Store(true)
	a.slow.Store(true) // hold the startup connect
	m.Prewarm()
	for a.requests.Load() == 0 {
		time.Sleep(time.Millisecond)
	}
	if _, ok := m.Get(); ok {
		t.Fatal("not connected yet")
	}
	a.slow.Store(false)
	waitForBackgroundProbe(t, m)
	if m.LastProbeError() == nil {
		t.Fatal("the startup connect must fail")
	}
	before := a.requests.Load()
	_, _ = m.ApplicationHealthCached(context.Background(), billing)
	waitForBackgroundProbe(t, m)
	if got := a.requests.Load() - before; got != 0 {
		t.Fatalf("the read sent %d requests, want 0 (throttled)", got)
	}
}

// A connected server that goes away: the read that failed saw it, so its
// re-check failing doesn't let the next read connect again before the
// retry interval ends.
func TestFailedRecheck_KeepsTheNextReadThrottled(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{ArgoCDURL: srv.URL, ArgoCDToken: "argo-secret"})
	mustConnect(t, m)

	a.hangUp.Store(true)
	a.down.Store(true)
	q := argoapi.ManagedResourcesQuery{AppName: "billing", AppNamespace: "argocd"}
	if _, err := m.ManagedResourcesCached(context.Background(), q); err == nil {
		t.Fatal("read must fail")
	}
	waitForBackgroundProbe(t, m)
	if _, ok := m.Get(); ok {
		t.Fatal("the re-check of a gone server must drop the connection")
	}
	waitForBackgroundProbe(t, m)
	before := a.requests.Load()
	_, _ = m.ApplicationHealthCached(context.Background(), billing)
	waitForBackgroundProbe(t, m)
	if got := a.requests.Load() - before; got != 0 {
		t.Fatalf("the next read sent %d requests, want 0 (throttled)", got)
	}
}

// A re-check that finds a working server slow keeps the connection; reads
// go on using it.
func TestSlowRecheckKeepsTheWorkingClient(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	a.slow.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := m.probeHoldingSlot(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("probe = %v, want a timeout (Settings says \"the request timed out\", not \"couldn't be reached\")", err)
	}
	if _, ok := m.Get(); !ok {
		t.Fatal("a slow answer dropped the working connection")
	}
}

// A background re-check that finds the server slow keeps the connection
// and records no connect failure: if the connection is later lost for
// another reason (a port-forward exiting), reads say "connecting" again
// rather than repeat an old timeout. Takes probeEndpointTimeout (8s).
func TestSlowRecheck_LeavesNoFailureBehind(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the reachability probe's own timeout")
	}
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	a.slow.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	q := argoapi.ManagedResourcesQuery{AppName: "billing", AppNamespace: "argocd"}
	if _, err := m.ManagedResourcesCached(ctx, q); err == nil {
		t.Fatal("read must time out")
	}
	waitForBackgroundProbe(t, m) // the re-check times out on the slow server
	if _, ok := m.Get(); !ok {
		t.Fatal("the slow re-check dropped the connection")
	}
	if err := m.LastProbeError(); err != nil {
		t.Fatalf("LastProbeError = %v, want nil: the connection was kept", err)
	}

	m.mu.Lock()
	fwd := m.dropConnectionLocked() // what a port-forward exit does
	m.mu.Unlock()
	if fwd != nil {
		fwd.stop()
	}
	a.slow.Store(false)
	if _, err := m.ManagedResourcesCached(context.Background(), q); !errors.Is(err, ErrConnecting) {
		t.Fatalf("read after the drop = %v, want ErrConnecting", err)
	}
	waitForBackgroundProbe(t, m)
}

// Through a port-forward a timeout can't tell a slow server from a dead
// tunnel, so the re-check looks for the server again. When nothing new
// answers either (here discovery finds no Service), the server is slow:
// the connection and its forward stay.
func TestSlowRecheckThroughAPortForwardKeepsTheConnection(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)
	stopped := make(chan struct{})
	fwd := &activeForward{localPort: 1, stopCh: make(chan struct{}), cancel: func() { close(stopped) }}
	m.mu.Lock()
	m.forward = fwd
	// The forward belongs to a discovered connection: no URL configured,
	// and the token bound to the current context.
	m.manualURL = ""
	m.contextName = func() string { return "kind-argo" }
	m.tokenBinding = "kind-argo"
	m.mu.Unlock()

	a.slow.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if err := m.probeHoldingSlot(ctx); !errors.Is(err, errNoAnswerInTime) {
		t.Fatalf("probe = %v, want a slow answer", err)
	}
	if _, ok := m.Get(); !ok {
		t.Fatal("a slow answer through a port-forward dropped the working connection")
	}
	m.mu.Lock()
	kept := m.forward == fwd
	m.mu.Unlock()
	if !kept {
		t.Fatal("the forward must stay installed")
	}
	select {
	case <-stopped:
		t.Fatal("the working forward must not be stopped")
	default:
	}
}

// The re-check through a port-forward that timed out looks for the server
// through a new forward. The connection in place and its forward stay
// until the replacement has answered and accepted the token: a dead tunnel
// is replaced, a slow server is not. Takes probeEndpointTimeout (8s).
func TestSlowRecheckThroughAPortForward_ReplacesOnlyAVerifiedForward(t *testing.T) {
	if testing.Short() {
		t.Skip("waits for the reachability probe's own timeout")
	}
	for _, tc := range []struct {
		name         string
		newTokenSlow bool
		newExits     bool // the new tunnel dies after the server answered through it
		wantReplaced bool
	}{
		{"the tunnel was dead: the new forward answers", false, false, true},
		{"the server is slow: the new forward's token check times out", true, false, false},
		{"the new forward exits before it is installed", false, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			old, oldSrv := newSwitchableArgo(t, "argo-secret")
			fresh, freshSrv := newSwitchableArgo(t, "argo-secret")
			port := func(srv *httptest.Server) int {
				u, _ := url.Parse(srv.URL)
				var p int
				_, _ = fmt.Sscanf(u.Port(), "%d", &p)
				return p
			}

			m := newTestManager(config.Config{})
			m.contextName = func() string { return "kind-argo" }
			svc := &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{Namespace: "argocd", Name: "argocd-server", Labels: map[string]string{"app.kubernetes.io/name": "argocd-server"}},
				Spec:       corev1.ServiceSpec{Ports: []corev1.ServicePort{{Name: "http", Port: 80}}},
			}
			k8sc := fake.NewSimpleClientset(svc)
			m.k8sClient = func() kubernetes.Interface { return k8sc }
			var stoppedMu sync.Mutex
			stopped := map[int]bool{}
			targets := []int{port(oldSrv), port(freshSrv)}
			m.portForward = func(context.Context, probeSnapshot, string, string, int) (*activeForward, error) {
				if len(targets) == 0 {
					return nil, errors.New("no more forwards")
				}
				p := targets[0]
				targets = targets[1:]
				fwd := &activeForward{localPort: p, stopCh: make(chan struct{}), cancel: func() {
					stoppedMu.Lock()
					stopped[p] = true
					stoppedMu.Unlock()
				}}
				fwd.exited.Store(tc.newExits && p == port(freshSrv))
				return fwd, nil
			}
			m.SetConfig("", "argo-secret", false, true)
			mustConnect(t, m)
			m.mu.Lock()
			oldClient, oldForward := m.client, m.forward
			m.mu.Unlock()
			if oldForward == nil || oldForward.localPort != port(oldSrv) {
				t.Fatalf("connected through %+v, want a forward to the first server", oldForward)
			}

			old.slow.Store(true)
			if tc.newTokenSlow {
				fresh.slowUserInfo.Store(true)
			}
			ctx, cancel := context.WithTimeout(context.Background(), probeEndpointTimeout+time.Second)
			defer cancel()
			err := m.probeHoldingSlot(ctx)
			m.mu.Lock()
			client, forward := m.client, m.forward
			m.mu.Unlock()
			stoppedMu.Lock()
			defer stoppedMu.Unlock()
			if tc.wantReplaced {
				if err != nil {
					t.Fatalf("probe = %v, want the new forward to connect", err)
				}
				if forward == nil || forward.localPort != port(freshSrv) || client == oldClient {
					t.Fatal("the verified new forward must replace the dead one")
				}
				if !stopped[port(oldSrv)] {
					t.Fatal("the replaced forward must be stopped")
				}
				return
			}
			if !errors.Is(err, ErrUnreachable) {
				t.Fatalf("probe = %v, want ErrUnreachable", err)
			}
			if client != oldClient || forward != oldForward {
				t.Fatal("the connection in place must stay until a replacement is verified")
			}
			if stopped[port(oldSrv)] {
				t.Fatal("the forward in use was stopped")
			}
			if !stopped[port(freshSrv)] {
				t.Fatal("the unverified new forward must be stopped")
			}
		})
	}
}

// A server that is gone (connection refused) is a definite failure: the
// re-check drops the connection.
func TestRecheckOfAGoneServerDropsTheClient(t *testing.T) {
	_, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	srv.Close()
	if err := m.probeHoldingSlot(context.Background()); !errors.Is(err, ErrUnreachable) {
		t.Fatalf("probe of a closed server = %v, want ErrUnreachable", err)
	}
	if _, ok := m.Get(); ok {
		t.Fatal("a refused connection must drop the client")
	}
}

// A failed read is kept for Settings and starts a re-check of the server.
// When the re-check finds the token rejected, the connection is dropped and
// Settings has the reason.
func TestFailedRead_RechecksAndDropsARejectedToken(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	m.Get() // connects in the background, as after a restart
	waitForBackgroundProbe(t, m)
	if _, ok := m.Get(); !ok {
		t.Fatal("background connect failed")
	}

	a.reject.Store(true)
	q := argoapi.ManagedResourcesQuery{AppName: "billing", AppNamespace: "argocd"}
	if _, err := m.ManagedResourcesCached(context.Background(), q); !errors.Is(err, argoapi.ErrUnauthorized) {
		t.Fatalf("read = %v, want ErrUnauthorized", err)
	}
	waitForBackgroundProbe(t, m)
	if _, ok := m.Get(); ok {
		t.Fatal("a rejected token must drop the connection")
	}
	if !errors.Is(m.LastProbeError(), ErrTokenInvalid) {
		t.Fatalf("LastProbeError = %v, want ErrTokenInvalid", m.LastProbeError())
	}
}

// A read that gets no answer keeps the connection, is reported to Settings,
// and is forgotten once a read succeeds.
func TestFailedRead_IsReportedUntilAReadSucceeds(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	a.slow.Store(true)
	q := argoapi.ManagedResourcesQuery{AppName: "billing", AppNamespace: "argocd"}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	if _, err := m.ManagedResourcesCached(ctx, q); err == nil {
		t.Fatal("read must fail")
	}
	if m.LastReadError() == nil {
		t.Fatal("the failed read must be recorded for Settings")
	}
	if !m.Retrying() {
		t.Fatal("while the server is re-checked, pages must be told to ask again")
	}
	a.slow.Store(false)
	waitForBackgroundProbe(t, m)
	if _, ok := m.Get(); !ok {
		t.Fatal("the re-check found the server fine; the connection must stay")
	}
	// The re-check only reaches /api/version and userinfo, not the
	// endpoints reads use: the failed read stays reported, and pages keep
	// asking so they recover without a reload.
	if m.LastReadError() == nil {
		t.Fatal("a re-check that kept the connection must not clear the failed read")
	}
	if !m.Retrying() {
		t.Fatal("pages must keep asking until a read succeeds")
	}

	// Nobody has read for a while (the page was closed): the failure no
	// longer shows in Settings, and nothing is polling.
	m.mu.Lock()
	m.lastReadAt = time.Now().Add(-readFailureShownFor - time.Second)
	m.mu.Unlock()
	if err := m.LastReadError(); err != nil || m.Retrying() {
		t.Fatalf("LastReadError = %v, Retrying = %v long after the read; want neither", err, m.Retrying())
	}
	m.mu.Lock()
	m.lastReadAt = time.Now()
	m.mu.Unlock()

	if _, err := m.ManagedResourcesCached(context.Background(), q); err != nil {
		t.Fatalf("read = %v", err)
	}
	if err := m.LastReadError(); err != nil {
		t.Fatalf("LastReadError = %v after a successful read, want nil", err)
	}
	if m.Retrying() {
		t.Fatal("pages must stop asking once a read succeeds")
	}
}

// A read that lost its connection (as during an argocd-server restart) is
// reported until the re-check it starts finds the server answering again;
// it doesn't linger in Settings after that.
func TestFailedRead_LostConnectionClearsWhenTheRecheckPasses(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	a.hangUp.Store(true)
	q := argoapi.ManagedResourcesQuery{AppName: "billing", AppNamespace: "argocd"}
	if _, err := m.ManagedResourcesCached(context.Background(), q); err == nil {
		t.Fatal("read must fail")
	}
	waitForBackgroundProbe(t, m)
	if _, ok := m.Get(); !ok {
		t.Fatal("the re-check found the server answering; the connection must stay")
	}
	if err := m.LastReadError(); err != nil {
		t.Fatalf("LastReadError = %v, want nil once the re-check passed", err)
	}
}

// An error the server answered with (here a 500, as when Argo CD fails to
// render from Git) is about that request: no re-check starts, and it clears
// an earlier "no answer" from Settings, since the server is answering.
func TestFailedRead_ServerAnswerIsNotAConnectionProblem(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	// An earlier read got no answer.
	a.slow.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	q := argoapi.ManagedResourcesQuery{AppName: "billing", AppNamespace: "argocd"}
	if _, err := m.ManagedResourcesCached(ctx, q); err == nil || m.LastReadError() == nil {
		t.Fatalf("timed-out read = %v, LastReadError = %v; want both set", err, m.LastReadError())
	}
	a.slow.Store(false)
	waitForBackgroundProbe(t, m)

	a.failApps.Store(true)
	m.mu.Lock()
	m.nextProbe = time.Time{} // a re-check would be allowed now
	m.mu.Unlock()
	before := a.requests.Load()
	if _, err := m.ManagedResourcesCached(context.Background(), q); err == nil {
		t.Fatal("read must fail")
	}
	if err := m.LastReadError(); err != nil {
		t.Fatalf("LastReadError = %v, want it cleared: the server answered", err)
	}
	waitForBackgroundProbe(t, m)
	if got := a.requests.Load() - before; got != 1 {
		t.Fatalf("server saw %d requests, want only the read (no re-check)", got)
	}
}

// A connect or DNS lookup that times out found no server: that drops the
// connection like a refused one. Only a server that accepted the
// connection and then answered slowly counts as slow.
func TestNoAnswerInTime_DialAndLookupTimeoutsAreGone(t *testing.T) {
	dial := &net.OpError{Op: "dial", Err: &timeoutErr{}}
	if isNoAnswerInTime(dial) {
		t.Error("a dial timeout must count as gone")
	}
	dns := &net.DNSError{Err: "timeout", IsTimeout: true}
	if isNoAnswerInTime(dns) {
		t.Error("a DNS timeout must count as gone")
	}
	read := &net.OpError{Op: "read", Err: &timeoutErr{}}
	if !isNoAnswerInTime(read) {
		t.Error("a read timeout on an open connection must count as slow")
	}
	if !isNoAnswerInTime(context.DeadlineExceeded) {
		t.Error("a deadline must count as slow")
	}
}

type timeoutErr struct{}

func (*timeoutErr) Error() string   { return "i/o timeout" }
func (*timeoutErr) Timeout() bool   { return true }
func (*timeoutErr) Temporary() bool { return true }

// After an HTTP timeout, a TCP connect tells a slow server (still accepts
// connections, kept) from an address nothing listens on (dropped).
func TestAcceptsConnections(t *testing.T) {
	_, srv := newSwitchableArgo(t, "argo-secret")
	if !acceptsConnections(context.Background(), srv.URL) {
		t.Fatal("a listening server must accept connections")
	}
	gone := httptest.NewServer(http.NotFoundHandler())
	addr := gone.URL
	gone.Close()
	if acceptsConnections(context.Background(), addr) {
		t.Fatal("a closed address must not accept connections")
	}
	expired, cancel := context.WithTimeout(context.Background(), 0)
	defer cancel()
	<-expired.Done()
	if !acceptsConnections(expired, srv.URL) {
		t.Fatal("the caller's own expired deadline must not make a live server look gone")
	}
}

// A background connect queued behind another probe (the Settings save) that
// already connected the same settings doesn't probe them again.
func TestBackgroundProbe_SkipsSettingsAlreadyConnected(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)

	release, err := m.acquireProbeSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	m.Get() // starts a background connect, which waits for the slot
	if err := m.probeHoldingSlot(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	after := a.requests.Load()
	release()
	waitForBackgroundProbe(t, m)
	if got := a.requests.Load() - after; got != 0 {
		t.Fatalf("the queued background connect sent %d requests, want 0", got)
	}
	if _, ok := m.Get(); !ok {
		t.Fatal("the connection must stay")
	}
}

// Retrying a connect that failed keeps reporting the failure, not
// "connecting": the retry usually fails the same way.
func TestReconnectAfterAFailureKeepsSayingWhy(t *testing.T) {
	m := newTestManager(config.Config{})
	m.SetConfig("http://127.0.0.1:1", "argo-secret", false, true)
	if _, err := m.ApplicationHealthCached(context.Background(), billing); !errors.Is(err, ErrConnecting) {
		t.Fatalf("first read = %v, want ErrConnecting", err)
	}
	waitForBackgroundProbe(t, m)
	m.mu.Lock()
	m.nextProbe = time.Time{} // the retry interval has passed
	m.mu.Unlock()
	_, err := m.ApplicationHealthCached(context.Background(), billing)
	if errors.Is(err, ErrConnecting) || !errors.Is(err, ErrUnreachable) {
		t.Fatalf("read during the retry = %v, want the last failure", err)
	}
	if m.Connecting() {
		t.Fatal("a retry after a failure must not report Connecting")
	}
	waitForBackgroundProbe(t, m)
}

// A Settings save that waited while another probe connected the same
// settings takes that as its answer instead of probing again.
func TestProbe_TakesAConnectThatLandedWhileItWaited(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)

	release, err := m.acquireProbeSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.Probe(context.Background()) }()
	if err := m.probeHoldingSlot(context.Background()); err != nil {
		t.Fatalf("connect: %v", err)
	}
	after := a.requests.Load()
	release()
	if err := <-done; err != nil {
		t.Fatalf("Probe = %v", err)
	}
	if got := a.requests.Load() - after; got != 0 {
		t.Fatalf("Probe sent %d requests, want 0", got)
	}
}

// A connect that failed while someone was reading is retried on its own,
// so once the server is back the next page load finds it connected.
func TestFailedConnect_RetriesOnItsOwnWhileReadsAreRecent(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	_ = l.Close() // nothing listens yet: connects are refused

	m := newTestManager(config.Config{})
	m.retryInterval = 200 * time.Millisecond
	m.SetConfig("http://"+addr, "argo-secret", false, true)
	if _, err := m.ApplicationHealthCached(context.Background(), billing); !errors.Is(err, ErrConnecting) {
		t.Fatalf("first read = %v, want ErrConnecting", err)
	}
	waitForBackgroundProbe(t, m)
	if m.LastProbeError() == nil {
		t.Fatal("the connect must have failed")
	}
	if !m.Retrying() {
		t.Fatal("a refused connect must be retried, and say so")
	}

	// The server comes back; nothing reads from here on (not even Get,
	// which would start a connect of its own).
	_, other := newSwitchableArgo(t, "argo-secret")
	back, err := net.Listen("tcp", addr)
	if err != nil {
		t.Skipf("port %s was taken meanwhile: %v", addr, err)
	}
	srv := &http.Server{Handler: other.Config.Handler}
	go func() { _ = srv.Serve(back) }()
	t.Cleanup(func() { _ = srv.Close() })

	deadline := time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		connected := m.client != nil
		m.lastDemand = time.Now() // a page is still open and asking
		m.mu.Unlock()
		if connected {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("not reconnected on its own; last error %v", m.LastProbeError())
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A rejected token won't fix itself: no retry is scheduled and the page is
// not told to keep asking.
func TestFailedConnect_RejectedTokenIsNotRetried(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	a.reject.Store(true)
	m := newTestManager(config.Config{})
	m.retryInterval = 200 * time.Millisecond
	m.SetConfig(srv.URL, "argo-secret", false, true)
	if _, err := m.ApplicationHealthCached(context.Background(), billing); !errors.Is(err, ErrConnecting) {
		t.Fatalf("first read = %v, want ErrConnecting", err)
	}
	waitForBackgroundProbe(t, m)
	if !errors.Is(m.LastProbeError(), ErrTokenInvalid) {
		t.Fatalf("LastProbeError = %v, want ErrTokenInvalid", m.LastProbeError())
	}
	if m.Retrying() {
		t.Fatal("a rejected token must not report Retrying")
	}
	m.mu.Lock()
	scheduled := !m.retryAt.IsZero()
	m.mu.Unlock()
	if scheduled {
		t.Fatal("a rejected token must not schedule a retry")
	}
}

// A probe that reaches the server drops cached "no answer" health results,
// so the next page poll reads again instead of repeating the failure.
func TestProbe_DropsCachedNoAnswerHealth(t *testing.T) {
	_, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	noAnswer := &url.Error{Op: "Get", URL: srv.URL, Err: io.EOF}
	answered := errors.New("argoapi: /x returned 500")
	m.mu.Lock()
	m.appHealthCache = map[string]appHealthEntry{
		"no-answer": {err: noAnswer, expires: time.Now().Add(time.Minute)},
		"answered":  {err: answered, expires: time.Now().Add(time.Minute)},
	}
	m.mu.Unlock()
	if err := m.probeHoldingSlot(context.Background()); err != nil { // the re-check
		t.Fatalf("re-check: %v", err)
	}
	m.mu.Lock()
	_, stale := m.appHealthCache["no-answer"]
	_, kept := m.appHealthCache["answered"]
	m.mu.Unlock()
	if stale || !kept {
		t.Fatalf("after a probe reached the server: no-answer cached %v (want false), answered cached %v (want true)", stale, kept)
	}
}

// Only a read that got no answer counts as a transport failure; a redirect
// Radar refused came from a server that answered.
func TestIsTransportFailure(t *testing.T) {
	wrap := func(err error) error {
		return fmt.Errorf("argoapi: GET /x: %w", &url.Error{Op: "Get", URL: "http://a", Err: err})
	}
	for _, tc := range []struct {
		err  error
		want bool
	}{
		{wrap(&net.OpError{Op: "dial", Err: errors.New("connection refused")}), true},
		{wrap(&net.DNSError{Err: "no such host"}), true},
		{wrap(io.EOF), true},
		{wrap(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}), true},
		{wrap(context.DeadlineExceeded), true},
		{wrap(errors.New("argoapi: refusing cross-origin redirect to evil (token would be exposed)")), false},
		{errors.New("argoapi: /x returned 500"), false},
		{fmt.Errorf("%w: /x returned 502: (unstructured error body omitted)", argoapi.ErrGatewayUnavailable), true},
	} {
		if got := isTransportFailure(tc.err); got != tc.want {
			t.Errorf("isTransportFailure(%v) = %v, want %v", tc.err, got, tc.want)
		}
	}
}

// A certificate Radar can't verify is a failed read, but asking again won't
// fix it, so pages don't keep asking.
func TestReadMayRecover_CertificateFailureNeedsTheUser(t *testing.T) {
	wrap := func(err error) error {
		return fmt.Errorf("argoapi: GET /x: %w", &url.Error{Op: "Get", URL: "http://a", Err: err})
	}
	if ReadMayRecover(wrap(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}})) {
		t.Error("certificate failure: want no retry")
	}
	if !ReadMayRecover(wrap(context.DeadlineExceeded)) {
		t.Error("timeout: want retry")
	}
	if !ReadMayRecover(fmt.Errorf("%w: /x returned 503", argoapi.ErrGatewayUnavailable)) {
		t.Error("gateway 503: want retry")
	}
	// While the shared connection is being retried, a page asks again even
	// for an answer that isn't a transport failure, but never for a
	// certificate failure.
	previous := defaultManager
	defaultManager = newTestManager(config.Config{})
	t.Cleanup(func() { defaultManager.Reset(); defaultManager = previous })
	m := defaultManager
	m.SetConfig("http://127.0.0.1:1", "argo-secret", false, true)
	m.mu.Lock()
	m.startBackgroundProbeLocked(true)
	m.mu.Unlock()
	waitForBackgroundProbe(t, m)
	_, _ = m.ApplicationHealthCached(context.Background(), billing)
	if !m.Retrying() {
		t.Fatal("setup: want the shared connection retrying")
	}
	if !ReadMayRecover(errors.New("argoapi: /x returned 500")) {
		t.Error("retrying: want the page to ask again")
	}
	if ReadMayRecover(wrap(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}})) {
		t.Error("certificate failure while retrying: want no retry")
	}
}

// A connect that failed while nobody was asking is still retried once a
// page does ask, so the page can recover.
func TestFailedConnect_RetryScheduledWhenAPageAsksLater(t *testing.T) {
	m := newTestManager(config.Config{})
	m.SetConfig("http://127.0.0.1:1", "argo-secret", false, true)
	m.mu.Lock()
	m.startBackgroundProbeLocked(false) // nobody asking
	m.mu.Unlock()
	waitForBackgroundProbe(t, m)
	if m.Retrying() {
		t.Fatal("nobody was reading: no retry should be scheduled yet")
	}
	// The first page to ask connects again at once; until that lands the
	// failure is still the answer.
	if _, err := m.ApplicationHealthCached(context.Background(), billing); err == nil || errors.Is(err, ErrConnecting) {
		t.Fatalf("read = %v, want the earlier failure", err)
	}
	if !m.Retrying() {
		t.Fatal("a page asking after the failure must have it retried")
	}
	waitForBackgroundProbe(t, m)
	// That retry failed with a page asking: the next read is throttled and
	// a retry is scheduled.
	if _, err := m.ApplicationHealthCached(context.Background(), billing); err == nil || !strings.Contains(err.Error(), "throttled") {
		t.Fatalf("read = %v, want the throttled failure", err)
	}
	if !m.Retrying() {
		t.Fatal("a page asking during the throttle must get a retry scheduled")
	}
}

// Failures that need the user (a certificate Radar can't verify, a URL that
// isn't Argo CD, a rejected token) are not retried as if they might pass.
func TestTransientConnectFailure(t *testing.T) {
	tls := fmt.Errorf("%w: %w (cert)", ErrUnreachable, x509.UnknownAuthorityError{})
	notArgo := fmt.Errorf("%w: Argo CD at u: %w", ErrUnreachable, argoapi.ErrNotFound)
	refused := fmt.Errorf("%w: Argo CD at u: %w", ErrUnreachable, &net.OpError{Op: "dial", Err: errors.New("refused")})
	for _, tc := range []struct {
		name string
		err  error
		want bool
	}{
		{"refused", refused, true},
		{"certificate", tls, false},
		{"not Argo CD", notArgo, false},
		{"token", fmt.Errorf("%w: x", ErrTokenInvalid), false},
		{"no argocd-server Service", fmt.Errorf("%w: %w: none labeled", ErrUnreachable, errNoArgoServer), false},
	} {
		if got := transientConnectFailure(tc.err); got != tc.want {
			t.Errorf("%s: transientConnectFailure = %v, want %v", tc.name, got, tc.want)
		}
	}
}

// Discovery that finds nothing to connect to (no argocd-server Service, or
// Radar may not list Services) is not retried on its own, and pages aren't
// told to keep asking: waiting won't change it.
func TestDiscover_NothingToConnectToIsNotRetried(t *testing.T) {
	forbidden := fake.NewSimpleClientset()
	forbidden.PrependReactor("list", "services", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "services"}, "", errors.New("RBAC"))
	})
	for name, client := range map[string]*fake.Clientset{"no Service": fake.NewSimpleClientset(), "list forbidden": forbidden} {
		m := newTestManager(config.Config{})
		_, _, err := m.discover(context.Background(), probeSnapshot{k8sClient: client})
		if !errors.Is(err, ErrUnreachable) {
			t.Errorf("%s: discover = %v, want ErrUnreachable", name, err)
		}
		if transientConnectFailure(err) {
			t.Errorf("%s: %v counted as a failure that may pass on its own", name, err)
		}
	}
}

// A read that finishes on a connection that has since been replaced doesn't
// report its failure against the new one.
func TestNoteRead_IgnoresAReplacedConnection(t *testing.T) {
	_, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)
	old := newClient(srv.URL, "argo-secret", false)
	m.mu.Lock()
	m.noteReadLocked(m.generation, old, &url.Error{Op: "Get", URL: srv.URL, Err: io.EOF})
	got := m.lastReadErr
	m.mu.Unlock()
	if got != nil {
		t.Fatalf("lastReadErr = %v from a replaced connection, want nil", got)
	}
}

// A read that finishes after its connection was dropped (a re-check found
// the token rejected or the server gone) answers its caller but is not
// cached for the next one.
func TestRead_NotCachedAfterItsConnectionWasDropped(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	a.slow.Store(true)
	before := a.requests.Load()
	done := make(chan error, 1)
	q := argoapi.ManagedResourcesQuery{AppName: "billing", AppNamespace: "argocd"}
	go func() {
		_, err := m.ManagedResourcesCached(context.Background(), q)
		done <- err
	}()
	deadline := time.Now().Add(5 * time.Second)
	for a.requests.Load() == before {
		if time.Now().After(deadline) {
			t.Fatal("the read never reached the server")
		}
		time.Sleep(5 * time.Millisecond)
	}
	m.mu.Lock()
	fwd := m.dropConnectionLocked() // what a re-check that found it gone does
	m.mu.Unlock()
	if fwd != nil {
		fwd.stop()
	}
	a.slow.Store(false)
	if err := <-done; err != nil {
		t.Fatalf("read = %v", err)
	}
	m.mu.Lock()
	cached := len(m.cache)
	m.mu.Unlock()
	if cached != 0 {
		t.Fatalf("%d entries cached from a dropped connection, want 0", cached)
	}
}

// A settings reload that fails (Kubernetes disconnected, a context switch
// under way) must not hold back the next connect for the retry interval.
func TestBackgroundProbe_FailedReloadDoesNotThrottle(t *testing.T) {
	previous := defaultManager
	defaultManager = newTestManager(config.Config{})
	t.Cleanup(func() { defaultManager.Reset(); defaultManager = previous })
	m := defaultManager
	m.SetConfig("http://127.0.0.1:1", "argo-secret", false, true)
	connections.RegisterRefresh(func(config.Integration) error { return errors.New("context switch in progress") })
	t.Cleanup(func() { connections.RegisterRefresh(nil) })

	m.Get()
	waitForBackgroundProbe(t, m)
	m.mu.Lock()
	next := m.nextProbe
	m.mu.Unlock()
	if !next.IsZero() {
		t.Fatalf("a failed reload armed the retry throttle until %s", next)
	}
}

// Saving new settings must not wait behind a background probe of the old
// ones that is stuck on a server that never answers.
func TestSettingsSave_IsNotHeldBehindAProbeOfOldSettings(t *testing.T) {
	hung, hungSrv := newSwitchableArgo(t, "argo-secret")
	hung.slow.Store(true)
	_, goodSrv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(hungSrv.URL, "argo-secret", false, true)
	m.Get() // starts the background probe, which hangs
	deadline := time.Now().Add(2 * time.Second)
	for hung.requests.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("background probe did not reach the server")
		}
		time.Sleep(5 * time.Millisecond)
	}

	m.SetConfig(goodSrv.URL, "argo-secret", false, true)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	start := time.Now()
	if err := m.Probe(ctx); err != nil {
		t.Fatalf("Probe of the new settings = %v", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Probe of the new settings waited %s behind the old probe", elapsed)
	}
}

// A caller waiting for another probe to finish gives up when its own
// context ends.
func TestProbe_StopsWaitingWhenItsContextEnds(t *testing.T) {
	m := newTestManager(config.Config{})
	m.SetConfig("http://127.0.0.1:1", "argo-secret", false, true)
	release, err := m.acquireProbeSlot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if err := m.Probe(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Probe = %v, want its own deadline", err)
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("Probe waited %s past its deadline", elapsed)
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// If argocd-server echoes the token in its error, the log must redact the
// token the probe sent, which the settings reload may have just changed.
func TestBackgroundProbe_RedactsTheTokenItProbedWith(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Version": "v3.0.0"}`))
	})
	mux.HandleFunc("/api/v1/session/userinfo", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message": "rejected ` + strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ") + `"}`))
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	var mu sync.Mutex
	var logs bytes.Buffer
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return logs.Write(p)
	}))
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	previous := defaultManager
	defaultManager = newTestManager(config.Config{})
	t.Cleanup(func() { defaultManager.Reset(); defaultManager = previous })
	m := defaultManager
	m.SetConfig(srv.URL, "token-before-reload", false, true)
	connections.RegisterRefresh(func(config.Integration) error {
		m.SetConfig(srv.URL, "token-after-reload", false, true)
		return nil
	})
	t.Cleanup(func() { connections.RegisterRefresh(nil) })

	m.Get() // starts the background probe
	deadline := time.Now().Add(2 * time.Second)
	for {
		mu.Lock()
		logged := logs.String()
		mu.Unlock()
		if strings.Contains(logged, "background probe") {
			if strings.Contains(logged, "token-after-reload") {
				t.Errorf("the token sent to Argo CD reached the log: %s", logged)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("the background probe logged nothing; log: %q", logged)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// An ingress answering 502/503/504 while argocd-server restarts is no answer
// from Argo CD: the page keeps asking, and the connection comes back on its
// own once argocd-server does, without a reload.
func TestFailedRead_GatewayErrorKeepsPagesAskingUntilArgoIsBack(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.retryInterval = 200 * time.Millisecond
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	a.gateway.Store(true)
	if _, err := m.ApplicationHealthCached(context.Background(), billing); !errors.Is(err, argoapi.ErrGatewayUnavailable) {
		t.Fatalf("read = %v, want a gateway error", err)
	}
	if m.LastReadError() == nil || !m.Retrying() {
		t.Fatalf("LastReadError = %v, Retrying = %v; want the failure shown and pages asking again", m.LastReadError(), m.Retrying())
	}

	// The re-check meets the gateway too; pages that keep asking keep being
	// told to.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		m.mu.Lock()
		m.lastDemand = time.Now()
		m.mu.Unlock()
		if !m.Retrying() {
			t.Fatal("while the gateway answers for argocd-server, pages must keep asking")
		}
		m.mu.Lock()
		dropped := m.client == nil && m.lastProbeErr != nil
		m.mu.Unlock()
		if dropped {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	a.gateway.Store(false)
	deadline = time.Now().Add(5 * time.Second)
	for {
		m.mu.Lock()
		connected := m.client != nil
		m.lastDemand = time.Now()
		m.mu.Unlock()
		if connected {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("not reconnected once argocd-server was back; last error %v", m.LastProbeError())
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := m.ApplicationHealthCached(context.Background(), billing); err != nil {
		t.Fatalf("read after recovery = %v", err)
	}
	if m.Retrying() {
		t.Fatal("pages must stop asking once a read succeeds")
	}
}

// A health read that ran out of time is shared like any other failure: the
// next viewer, or the next poll, gets it from the cache instead of waiting
// out the same budget against a server already too slow to answer.
func TestApplicationHealthCached_TimeoutIsSharedNotRepeated(t *testing.T) {
	a, srv := newSwitchableArgo(t, "argo-secret")
	m := newTestManager(config.Config{})
	m.SetConfig(srv.URL, "argo-secret", false, true)
	mustConnect(t, m)

	a.slow.Store(true)
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := m.ApplicationHealthCached(ctx, billing); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("first read = %v, want a timeout", err)
	}
	before := a.requests.Load()
	ctx2, cancel2 := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel2()
	if _, err := m.ApplicationHealthCached(ctx2, billing); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("second read = %v, want the cached timeout", err)
	}
	if got := a.requests.Load() - before; got != 0 {
		t.Fatalf("argocd-server saw %d more application requests, want 0", got)
	}

	// A caller that navigated away is not the server's answer.
	m.mu.Lock()
	m.appHealthCache = nil
	m.mu.Unlock()
	cctx, ccancel := context.WithCancel(context.Background())
	go func() { time.Sleep(50 * time.Millisecond); ccancel() }()
	if _, err := m.ApplicationHealthCached(cctx, billing); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled read = %v, want context.Canceled", err)
	}
	m.mu.Lock()
	_, cached := m.appHealthCache[billing.AppNamespace+"\x00"+billing.AppName]
	m.mu.Unlock()
	if cached {
		t.Fatal("a canceled read must not be cached")
	}
}
