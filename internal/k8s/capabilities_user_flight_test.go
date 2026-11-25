package k8s

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// sarServer answers every SubjectAccessReview as allowed, or with a 504 while
// failing is set, after waiting on gate when it is non-nil.
type sarServer struct {
	entered chan struct{}
	reviews atomic.Int32
	failing atomic.Bool
	gate    atomic.Pointer[chan struct{}]
}

// hold makes reviews wait until the returned release runs (also at cleanup).
func (s *sarServer) hold(t *testing.T) (release func()) {
	gate := make(chan struct{})
	s.gate.Store(&gate)
	var once sync.Once
	release = func() { once.Do(func() { s.gate.Store(nil); close(gate) }) }
	t.Cleanup(release)
	return release
}

func useSARServer(t *testing.T) *sarServer {
	t.Helper()
	s := &sarServer{entered: make(chan struct{})}
	signalReview := sync.OnceFunc(func() { close(s.entered) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.reviews.Add(1)
		signalReview()
		if g := s.gate.Load(); g != nil {
			<-*g
		}
		if s.failing.Load() {
			http.Error(w, "timeout", http.StatusGatewayTimeout)
			return
		}
		var review authv1.SubjectAccessReview
		if err := json.NewDecoder(r.Body).Decode(&review); err != nil {
			http.Error(w, "bad review", http.StatusBadRequest)
			return
		}
		review.Status.Allowed = true
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(review)
	}))
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, ContentConfig: rest.ContentConfig{ContentType: "application/json"}})
	if err != nil {
		t.Fatal(err)
	}
	clientMu.Lock()
	previous, oldNs := installedConn.Load(), contextNamespace
	contextNamespace = ""
	installedConn.Store(installedConnectionLocked(nil, &sharedKubernetesClients{clientset: client}))
	clientMu.Unlock()

	t.Cleanup(func() {
		clientMu.Lock()
		installedConn.Store(previous)
		contextNamespace = oldNs
		clientMu.Unlock()
	})
	return s
}

func expireUserCapabilities() {
	Current().permissions().userCapabilitiesCache.Range(func(_, value any) bool {
		value.(*userCapEntry).expiresAt = time.Now().Add(-time.Second)
		return true
	})
}

// Concurrent requests from one identity share one round of reviews.
func TestCheckCapabilitiesForUserCoalescesConcurrentChecks(t *testing.T) {
	s := useSARServer(t)
	if _, err := CheckCapabilitiesForUser(t.Context(), "alice", nil); err != nil {
		t.Fatal(err)
	}
	perRound := s.reviews.Swap(0)
	Current().ResetPermissions()

	release := s.hold(t)
	var wg sync.WaitGroup
	results := make([]*Capabilities, 8)
	for i := range results {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i], _ = CheckCapabilitiesForUser(t.Context(), "alice", nil)
		}()
	}
	time.Sleep(200 * time.Millisecond)
	release()
	wg.Wait()
	if got := s.reviews.Load(); got != perRound {
		t.Errorf("8 concurrent checks sent %d reviews, want one round of %d", got, perRound)
	}
	for i, caps := range results {
		if caps == nil || !caps.Exec {
			t.Fatalf("caller %d got %+v", i, caps)
		}
		if i > 0 && caps == results[0] {
			t.Fatal("callers share one *Capabilities")
		}
	}
}

// After a failed check, the re-check doesn't hold callers or hand them the
// fail-closed answer while it runs: they get the last complete result.
func TestCheckCapabilitiesForUserServesLastGoodDuringRecheck(t *testing.T) {
	s := useSARServer(t)
	if caps, _ := CheckCapabilitiesForUser(t.Context(), "alice", nil); !caps.Exec || caps.ChecksIncomplete {
		t.Fatalf("first check = %+v", caps)
	}
	s.failing.Store(true)
	expireUserCapabilities()
	if caps, _ := CheckCapabilitiesForUser(t.Context(), "alice", nil); caps.Exec || !caps.ChecksIncomplete {
		t.Fatalf("failed check = %+v, want fail-closed and incomplete", caps)
	}

	s.failing.Store(false)
	release := s.hold(t)
	expireUserCapabilities()
	done := make(chan *Capabilities, 1)
	go func() {
		caps, _ := CheckCapabilitiesForUser(t.Context(), "alice", nil)
		done <- caps
	}()
	select {
	case caps := <-done:
		if !caps.Exec || caps.ChecksIncomplete {
			t.Errorf("during the re-check got %+v, want the last complete result", caps)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("caller waited on the re-check")
	}
	release()
	deadline := time.Now().Add(5 * time.Second)
	for {
		if caps, _ := CheckCapabilitiesForUser(t.Context(), "alice", nil); !caps.ChecksIncomplete {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the re-check never replaced the failed answer")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestUserCapabilityFlightsStayOnTheirConnection(t *testing.T) {
	a := useSARServer(t)
	connA := Current()
	releaseA := a.hold(t)
	check := func(ctx context.Context) <-chan *Capabilities {
		done := make(chan *Capabilities, 1)
		go func() {
			caps, _ := CheckCapabilitiesForUser(ctx, "alice", nil)
			done <- caps
		}()
		return done
	}
	resultA := check(t.Context())
	select {
	case <-a.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("A never started a review")
	}

	b := useSARServer(t)
	b.failing.Store(true)
	releaseB := b.hold(t)
	resultB := check(t.Context())
	select {
	case <-b.entered:
	case <-time.After(5 * time.Second):
		releaseA()
		t.Fatal("B did not start its own review while A was blocked")
	}

	releaseA()
	if caps := <-resultA; !caps.Exec || caps.ChecksIncomplete {
		t.Fatalf("A's result = %+v", caps)
	}
	Current().permissions().userCapabilitiesCache.Range(func(key, _ any) bool {
		t.Errorf("B cached %v before its reviews completed", key)
		return true
	})
	if caps, _ := CheckCapabilitiesForUser(WithConnection(t.Context(), connA), "alice", nil); !caps.Exec {
		t.Fatal("a request bound to A lost its cached grant")
	}
	releaseB()
	if caps := <-resultB; caps.Exec || !caps.ChecksIncomplete {
		t.Fatalf("B's result = %+v", caps)
	}
	reviews := b.reviews.Load()
	if caps, _ := CheckCapabilitiesForUser(t.Context(), "alice", nil); caps.Exec || !caps.ChecksIncomplete || b.reviews.Load() != reviews {
		t.Fatalf("B did not cache its own result: %+v", caps)
	}
}
