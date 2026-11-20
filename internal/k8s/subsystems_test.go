package k8s

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"time"

	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/rest"
	"testing"
)

func TestInitAllSubsystemsPropagatesTimelineInitFailure(t *testing.T) {
	timelineErr := errors.New("timeline init failed")
	RegisterTimelineFuncs(nil, func() error {
		return timelineErr
	})
	t.Cleanup(func() {
		RegisterTimelineFuncs(nil, nil)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := InitAllSubsystems(ctx, func(string) {})
	if !errors.Is(err, timelineErr) {
		t.Fatalf("InitAllSubsystems error = %v, want timeline error %v", err, timelineErr)
	}
}

func TestInitAllSubsystemsCancellationDuringDiscovery(t *testing.T) {
	withCleanDiscoveryGlobals(t)
	if err := InitTestResourceCache(fake.NewSimpleClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(ResetResourceCache)

	release := make(chan struct{})
	started := make(chan struct{}, 1)
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case started <- struct{}{}:
		default:
		}
		<-release
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api" {
			_, _ = w.Write([]byte(`{"kind":"APIVersions","apiVersion":"v1","versions":[]}`))
			return
		}
		_, _ = w.Write([]byte(`{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`))
	}))
	t.Cleanup(api.Close)
	t.Cleanup(func() {
		close(release)
		// Let the discovery worker publish before restoring the test globals.
		for deadline := time.Now().Add(3 * time.Second); GetResourceDiscovery() == nil; {
			if time.Now().After(deadline) {
				t.Error("discovery did not finish after the API was released")
				return
			}
			time.Sleep(time.Millisecond)
		}
	})
	clientMu.Lock()
	discoveryClient = discovery.NewDiscoveryClientForConfigOrDie(&rest.Config{Host: api.URL})
	clientMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	waiting := make(chan struct{})
	go func() {
		done <- InitAllSubsystems(ctx, func(stage string) {
			if stage == "Waiting for Kubernetes API discovery..." {
				close(waiting)
			}
		})
	}()
	for _, observed := range []<-chan struct{}{started, waiting} {
		select {
		case <-observed:
		case <-time.After(3 * time.Second):
			t.Fatal("did not reach discovery wait")
		}
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("init returned %v, want cancellation", err)
		}
	case <-time.After(time.Second):
		t.Fatal("init is still waiting for discovery after cancellation")
	}
}
