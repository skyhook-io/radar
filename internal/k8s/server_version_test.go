package k8s

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	"k8s.io/apimachinery/pkg/version"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func setServerVersionClient(t *testing.T, handler http.HandlerFunc) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL})
	if err != nil {
		t.Fatal(err)
	}
	InvalidateServerVersionCache()
	previous := SetTestClient(client)
	t.Cleanup(func() {
		SetTestClient(previous)
		InvalidateServerVersionCache()
	})
}

func writeVersion(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(version.Info{GitVersion: "v1.31.2"})
}

// A failed version read (a timeout behind the informer start) must not stick
// until the next context switch; a successful one is read once.
func TestGetServerVersionRetriesAfterFailureAndKeepsSuccess(t *testing.T) {
	var failing atomic.Bool
	var reads atomic.Int32
	failing.Store(true)
	setServerVersionClient(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		if failing.Load() {
			http.Error(w, "timeout", http.StatusGatewayTimeout)
			return
		}
		writeVersion(w)
	})

	if got := GetServerVersion(); got != "" {
		t.Fatalf("failed read = %q, want empty", got)
	}
	failing.Store(false)
	if got := GetServerVersion(); got != "v1.31.2" {
		t.Fatalf("after the apiserver answers = %q, want v1.31.2", got)
	}
	before := reads.Load()
	if got := GetServerVersion(); got != "v1.31.2" || reads.Load() != before {
		t.Errorf("a successful version must be kept: got %q, reads %d -> %d", got, before, reads.Load())
	}
}

// Concurrent callers share one in-flight read rather than each asking.
func TestGetServerVersionSharesOneInFlightRead(t *testing.T) {
	var reads atomic.Int32
	entered := make(chan struct{})
	release := make(chan struct{})
	var once sync.Once
	setServerVersionClient(t, func(w http.ResponseWriter, r *http.Request) {
		reads.Add(1)
		once.Do(func() { close(entered) })
		<-release
		writeVersion(w)
	})

	var wg sync.WaitGroup
	wg.Add(1)
	go func() { defer wg.Done(); GetServerVersion() }()
	<-entered
	for range 5 {
		wg.Add(1)
		go func() { defer wg.Done(); GetServerVersion() }()
	}
	close(release)
	wg.Wait()
	if n := reads.Load(); n != 1 {
		t.Errorf("concurrent callers made %d reads, want 1", n)
	}
}
