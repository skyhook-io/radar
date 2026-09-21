package cloud

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestCloudHandshakeHeadersAlwaysAdvertiseSelfUpgradeCapability(t *testing.T) {
	// False must be sent explicitly — it is meaningful for GitOps and
	// --no-self-upgrade installations and must not be confused with an older
	// agent that predates the capability contract.
	for _, tt := range []struct {
		name        string
		selfUpgrade bool
		want        string
	}{
		{name: "available", selfUpgrade: true, want: "true"},
		{name: "unavailable", selfUpgrade: false, want: "false"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			headers := cloudHandshakeHeaders(Config{Token: "rhc_test"}, tt.selfUpgrade)
			values := headers.Values(selfUpgradeAvailableHeader)
			if len(values) != 1 || values[0] != tt.want {
				t.Fatalf("%s = %q, want exactly [%q]", selfUpgradeAvailableHeader, values, tt.want)
			}
		})
	}
}

type fakeAdvertisementSession struct {
	closed chan struct{}
}

func newFakeAdvertisementSession() *fakeAdvertisementSession {
	return &fakeAdvertisementSession{closed: make(chan struct{})}
}

func (s *fakeAdvertisementSession) CloseChan() <-chan struct{} { return s.closed }

func (s *fakeAdvertisementSession) Close() error {
	select {
	case <-s.closed:
	default:
		close(s.closed)
	}
	return nil
}

func TestWatchSelfUpgradeAdvertisementReconnectsOnStableChange(t *testing.T) {
	sess := newFakeAdvertisementSession()
	go watchSelfUpgradeAdvertisement(context.Background(), func() bool { return true }, false, sess, time.Millisecond)

	select {
	case <-sess.CloseChan():
	case <-time.After(2 * time.Second):
		t.Fatal("session was not closed after the capability stably changed")
	}
}

func TestWatchSelfUpgradeAdvertisementIgnoresTransientMismatch(t *testing.T) {
	// One failed probe (a transient apiserver error reads as false) must not
	// cycle a healthy tunnel.
	var calls atomic.Int64
	current := func() bool { return calls.Add(1) != 1 }

	sess := newFakeAdvertisementSession()
	done := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		watchSelfUpgradeAdvertisement(ctx, current, true, sess, time.Millisecond)
		close(done)
	}()

	time.Sleep(50 * time.Millisecond)
	select {
	case <-sess.CloseChan():
		t.Fatal("a single transient mismatch closed the session")
	default:
	}
	if calls.Load() < 3 {
		t.Fatalf("watcher observed only %d probes; the transient mismatch was never followed up", calls.Load())
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not exit on context cancellation")
	}
}

func TestCloudHandshakeHeaders_Release(t *testing.T) {
	with := cloudHandshakeHeaders(Config{Token: "rhc_test", Release: "radar-prod"}, false)
	if got := with.Get("X-Radar-Release"); got != "radar-prod" {
		t.Fatalf("X-Radar-Release = %q, want radar-prod", got)
	}
	// Non-Helm installs have no release; the header must be absent, not
	// empty, so the hub keeps whatever it last stored.
	without := cloudHandshakeHeaders(Config{Token: "rhc_test"}, false)
	if _, ok := without["X-Radar-Release"]; ok {
		t.Fatal("X-Radar-Release sent with no release configured")
	}
}

func TestHandshakeRejectionErrorOnlyBlamesTheTokenOn401(t *testing.T) {
	// Radar Cloud answers the agent handshake with 101, 401 or 500. Any other
	// status came from the path in between, which never saw the token, and a
	// message that blames the credential sends the operator to rotate a token
	// that was fine.
	dialErr := errors.New("websocket: bad handshake")
	for _, tt := range []struct {
		name         string
		status       int
		wantContains []string
		forbidsToken bool
	}{
		{
			name:         "401 is the token verdict",
			status:       http.StatusUnauthorized,
			wantContains: []string{"401", "--cloud-token"},
		},
		{
			name:   "403 corrects the reader and points at the path",
			status: http.StatusForbidden,
			// The correction is load-bearing: naming 401 as how a bad token
			// actually comes back is what stops a token rotation here.
			wantContains: []string{"403", "401", "proxy or gateway in front"},
			forbidsToken: true,
		},
		{
			name:         "404 points at the URL",
			status:       http.StatusNotFound,
			wantContains: []string{"404", "--cloud-url"},
			forbidsToken: true,
		},
		{
			name:         "503 is a service failure",
			status:       http.StatusServiceUnavailable,
			wantContains: []string{"503", "was not rejected"},
			forbidsToken: true,
		},
		{
			name:         "unexpected status stays neutral",
			status:       http.StatusTeapot,
			wantContains: []string{"418"},
			forbidsToken: true,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			got := handshakeRejectionError(tt.status, dialErr).Error()
			for _, want := range tt.wantContains {
				if !strings.Contains(got, want) {
					t.Fatalf("status %d message %q does not mention %q", tt.status, got, want)
				}
			}
			if tt.forbidsToken {
				for _, banned := range []string{"revoked", "rejected the cluster token", "cluster disabled"} {
					if strings.Contains(got, banned) {
						t.Fatalf("status %d message %q claims %q, but only 401 is a verdict on the token",
							tt.status, got, banned)
					}
				}
			}
		})
	}
}

func TestHandshakeRejectionErrorKeepsTheDialErrorWhenItAddsSomething(t *testing.T) {
	// 401/403/404 are fully explained by the status, so the generic
	// "bad handshake" adds nothing. Everything else keeps it: it is the only
	// detail distinguishing one unexpected answer from another.
	dialErr := errors.New("websocket: bad handshake")
	if !errors.Is(handshakeRejectionError(http.StatusBadGateway, dialErr), dialErr) {
		t.Fatal("502 dropped the underlying dial error")
	}
	if !errors.Is(handshakeRejectionError(http.StatusTeapot, dialErr), dialErr) {
		t.Fatal("unexpected status dropped the underlying dial error")
	}
}

// TestDialAttributesTheAnswerItGot drives the real dial path. The table test
// above only exercises the helper, so without this the whole mapping can be
// reverted in dial() and the package still passes.
func TestDialAttributesTheAnswerItGot(t *testing.T) {
	for _, tt := range []struct {
		name    string
		status  int
		want    string
		notWant []string
	}{
		{
			name:   "401 names the token",
			status: http.StatusUnauthorized,
			want:   "--cloud-token",
		},
		{
			name:    "403 sends them to the path in front",
			status:  http.StatusForbidden,
			want:    "proxy or gateway in front",
			notWant: []string{"revoked", "rejected the cluster token"},
		},
		{
			name:    "503 is not a rejection",
			status:  http.StatusServiceUnavailable,
			want:    "was not rejected",
			notWant: []string{"revoked", "rejected the cluster token"},
		},
		{
			// A server may answer any three-digit status; net/http accepts it.
			// The 5xx branch's upper bound is what keeps this out of "service
			// or network failure", so it has to be exercised.
			name:    "a status above 5xx stays unattributed",
			status:  699,
			want:    "unexpected answer",
			notWant: []string{"was not rejected", "revoked"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()

			_, err := dial(context.Background(), Config{
				URL:       "ws://" + srv.Listener.Addr().String() + "/agent",
				Token:     "rhc_test",
				ClusterID: "c1",
				Handler:   http.NewServeMux(),
			}, false)
			if err == nil {
				t.Fatalf("status %d: dial succeeded", tt.status)
			}

			var answered *handshakeStatusError
			if !errors.As(err, &answered) {
				t.Fatalf("status %d: error is not marked as an answered handshake: %v", tt.status, err)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("status %d: %q does not mention %q", tt.status, err, tt.want)
			}
			for _, banned := range tt.notWant {
				if strings.Contains(err.Error(), banned) {
					t.Fatalf("status %d: %q claims %q", tt.status, err, banned)
				}
			}
		})
	}
}

// TestDialLeavesAnUnansweredHandshakeUnmarked pins the other half: a dial that
// never reached a server must NOT look like an answered handshake, or the
// escalation drops the flag guidance in the one case that needs it.
func TestDialLeavesAnUnansweredHandshakeUnmarked(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	addr := srv.Listener.Addr().String()
	srv.Close() // nothing is listening now

	_, err := dial(context.Background(), Config{
		URL:       "ws://" + addr + "/agent",
		Token:     "rhc_test",
		ClusterID: "c1",
		Handler:   http.NewServeMux(),
	}, false)
	if err == nil {
		t.Fatal("dial to a closed port succeeded")
	}
	var answered *handshakeStatusError
	if errors.As(err, &answered) {
		t.Fatalf("a refused connection was marked as an answered handshake: %v", err)
	}
}
