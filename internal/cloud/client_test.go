package cloud

import (
	"bytes"
	"context"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEscalationWarningKeepsFlagGuidanceOffAnsweredHandshakes(t *testing.T) {
	dialErr := errors.New("websocket: bad handshake")

	// A handshake that was answered already named what to check. Appending the
	// flag list there is what turns somebody else's outage into a token
	// rotation. It must not appear for ANY answered status, 401 included:
	// the 401 message points at --cloud-token on its own terms.
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusServiceUnavailable} {
		got := escalationWarning(5, handshakeRejectionError(status, dialErr))
		if strings.Contains(got, "Verify --cloud-url") {
			t.Fatalf("status %d escalation appended the flag list: %q", status, got)
		}
		if !strings.Contains(got, "5 consecutive failures") {
			t.Fatalf("status %d escalation dropped the failure count: %q", status, got)
		}
	}

	// No answer at all is what the flag list is for: from the agent's side a
	// wrong Cloud URL and an unreachable one are the same observation.
	unanswered := escalationWarning(5, errors.New("ws dial: dial tcp 10.0.0.1:443: connect: connection refused"))
	for _, want := range []string{"5 consecutive failures", "--cloud-url", "--cloud-token", "--cluster-name", "connection refused"} {
		if !strings.Contains(unanswered, want) {
			t.Fatalf("an unanswered-dial escalation is missing %q: %q", want, unanswered)
		}
	}
}

// TestRunEscalationUsesTheAnsweredHandshakeWording covers the wiring, not the
// helper: without it the whole attribution fix can be unwired from Run and the
// package still passes.
func TestRunEscalationUsesTheAnsweredHandshakeWording(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "forbidden", http.StatusForbidden)
	}))
	defer srv.Close()

	var buf syncBuffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(&buf)
	log.SetFlags(0)
	defer func() { log.SetOutput(prevOut); log.SetFlags(prevFlags) }()

	// Escalate on the first failure so the test doesn't sit through the
	// backoff between five real attempts.
	prevWarnAfter := warnAfterFailures
	warnAfterFailures = 1
	defer func() { warnAfterFailures = prevWarnAfter }()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = Run(ctx, Config{
			URL:       "ws://" + srv.Listener.Addr().String() + "/agent",
			Token:     "rhc_test",
			ClusterID: "c1",
			Handler:   http.NewServeMux(),
		})
	}()

	deadline := time.Now().Add(15 * time.Second)
	for !strings.Contains(buf.String(), "consecutive failures") {
		if time.Now().After(deadline) {
			cancel()
			<-done
			t.Fatalf("Run never escalated; log was:\n%s", buf.String())
		}
		time.Sleep(20 * time.Millisecond)
	}
	cancel()
	<-done

	got := buf.String()
	if strings.Contains(got, "Verify --cloud-url") {
		t.Fatalf("Run's escalation appended the flag list after a 403:\n%s", got)
	}
	if !strings.Contains(got, "403") {
		t.Fatalf("Run's escalation dropped the status the handshake returned:\n%s", got)
	}
}

// syncBuffer is a log sink the test goroutine can read while Run writes to it
// from its own. bytes.Buffer alone races here, and the race detector is not in
// CI to catch it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
