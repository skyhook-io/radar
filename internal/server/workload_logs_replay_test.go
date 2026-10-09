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

func cursorLine(follow int, ts, content string) workloadLogEntry {
	return workloadLogEntry{Timestamp: ts, Content: content, follow: follow}
}

func TestLogStreamCursorResumesWithoutReplay(t *testing.T) {
	cursors := map[string]*logStreamCursor{}
	c := cursorFor(cursors, "pg-1/postgres", "uid-1", 1)
	first := c.restartOptions("postgres", 200, nil)
	if first.TailLines == nil || *first.TailLines != 200 || first.SinceTime != nil || !first.Follow || !first.Timestamps || first.Container != "postgres" {
		t.Fatalf("first start = %+v", first)
	}
	for _, e := range []workloadLogEntry{cursorLine(1, "2026-09-28T14:00:00.1Z", "a"), cursorLine(1, "2026-09-28T14:00:05.7Z", "b"), cursorLine(1, "2026-09-28T14:00:05.7Z", "c")} {
		if !c.admit(e) {
			t.Fatalf("fresh line %+v rejected", e)
		}
	}

	if cursorFor(cursors, "pg-1/postgres", "uid-1", 2) != c {
		t.Fatal("same Pod got a new cursor")
	}
	restart := c.restartOptions("postgres", 200, nil)
	if restart.TailLines != nil || restart.SinceSeconds != nil || restart.SinceTime == nil ||
		!restart.SinceTime.Time.Equal(time.Date(2026, 9, 28, 14, 0, 5, 0, time.UTC)) {
		t.Fatalf("restart = %+v, want sinceTime at the last delivered second and no tail", restart)
	}

	// The resumed follow replays the boundary second.
	for _, e := range []workloadLogEntry{cursorLine(2, "2026-09-28T14:00:05.2Z", "earlier in the second"), cursorLine(2, "2026-09-28T14:00:05.7Z", "b"), cursorLine(2, "2026-09-28T14:00:05.7Z", "c")} {
		if c.admit(e) {
			t.Errorf("replayed line %+v admitted", e)
		}
	}
	for _, e := range []workloadLogEntry{cursorLine(2, "2026-09-28T14:00:05.7Z", "d"), cursorLine(2, "2026-09-28T14:00:06Z", "e")} {
		if !c.admit(e) {
			t.Errorf("new line %+v rejected", e)
		}
	}
	if c.admit(cursorLine(2, "2026-09-28T14:00:05.7Z", "d")) {
		t.Error("line before the new last timestamp admitted")
	}
}

// Some runtimes stamp lines written together with one timestamp, so the same
// (timestamp, content) can legitimately arrive more than once.
func TestLogStreamCursorKeepsIdenticalLines(t *testing.T) {
	c := cursorFor(map[string]*logStreamCursor{}, "web-0/app", "uid-1", 1)
	ts := "2026-09-28T14:00:05.7Z"
	for i := range 3 {
		if !c.admit(cursorLine(1, ts, "")) {
			t.Fatalf("identical line %d in one follow rejected", i+1)
		}
	}
	// A resumed follow reads the three again, then a fourth that is new.
	for i := range 3 {
		if c.admit(cursorLine(2, ts, "")) {
			t.Fatalf("replayed line %d admitted", i+1)
		}
	}
	if !c.admit(cursorLine(2, ts, "")) {
		t.Fatal("fourth identical line rejected")
	}
}

// A replacement Pod can take the old one's name between discovery passes. It
// must start from the caller's window, and the old Pod's lines still queued
// must neither be dropped nor move the new cursor.
func TestLogStreamCursorReplacementPod(t *testing.T) {
	cursors := map[string]*logStreamCursor{}
	old := cursorFor(cursors, "web-0/app", "uid-old", 1)
	old.admit(cursorLine(1, "2026-09-28T14:00:09Z", "old pod line"))

	c := cursorFor(cursors, "web-0/app", "uid-new", 2)
	if c == old {
		t.Fatal("replacement Pod reused the old cursor")
	}
	if opts := c.restartOptions("app", 50, nil); opts.SinceTime != nil || opts.TailLines == nil {
		t.Fatalf("replacement start = %+v, want the caller's tail", opts)
	}
	if !c.admit(cursorLine(1, "2026-09-28T14:00:10Z", "queued old pod line")) {
		t.Fatal("old Pod's queued line dropped")
	}
	if !c.admit(cursorLine(2, "2026-09-28T14:00:08Z", "new pod line, node clock behind")) {
		t.Fatal("replacement Pod's line dropped because of the old Pod's timestamps")
	}
}
