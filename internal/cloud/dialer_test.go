package cloud

import (
	"context"
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
