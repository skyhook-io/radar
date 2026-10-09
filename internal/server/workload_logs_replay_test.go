package server

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

// A finished container's follow ends at once, and the next discovery pass
// reopens it. The reopen must resume after the lines already sent, not repeat
// the tail.
func TestWorkloadLogsStream_ReopenDoesNotRepeatLines(t *testing.T) {
	lines := []string{
		"2026-09-28T14:00:00.100000000Z first",
		"2026-09-28T14:00:05.200000000Z second",
		"2026-09-28T14:00:05.700000000Z third",
	}
	var mu sync.Mutex
	var sinceTimes []string
	// Like the kubelet, sinceTime is applied at second granularity, so a
	// resumed follow repeats the boundary second.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		since := r.URL.Query().Get("sinceTime")
		mu.Lock()
		sinceTimes = append(sinceTimes, since)
		mu.Unlock()
		for _, l := range lines {
			if since != "" && l[:len("2026-09-28T14:00:05")] < strings.TrimSuffix(since, "Z") {
				continue
			}
			_, _ = w.Write([]byte(l + "\n"))
		}
	}))
	t.Cleanup(srv.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })

	stream := readSSEFor(t, "/api/workloads/deployments/default/nginx/logs/stream", 7*time.Second)

	mu.Lock()
	defer mu.Unlock()
	if len(sinceTimes) < 2 || sinceTimes[0] != "" || sinceTimes[1] != "2026-09-28T14:00:05Z" {
		t.Fatalf("follow requests sinceTime = %q, want a first open without it and a reopen from the last sent second", sinceTimes)
	}
	for _, content := range []string{"first", "second", "third"} {
		if n := strings.Count(stream, `"content":"`+content+`"`); n != 1 {
			t.Errorf("%q sent %d times, want once:\n%s", content, n, stream)
		}
	}
}
