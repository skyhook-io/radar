package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
)

// TestPodLogsStream_FlushesConnectedBeforeFollow asserts the pod-log SSE
// contract: the handler must flush its 200 + SSE headers + `connected` frame
// to the client before it opens the blocking follow log stream against the
// apiserver. Behind a buffering OIDC/ALB proxy a follow stream that stalls
// (accepts the connection but never sends headers) otherwise leaves the
// browser EventSource pending with 0 bytes, because nothing was flushed first.
//
// The fake apiserver emulates that stall: it accepts the GetLogs
// (`.../log?follow=true`) request and blocks without ever writing response
// headers. The client must still receive the 200 status line and the
// `connected` SSE frame promptly, even though the follow body never flows.
func TestPodLogsStream_FlushesConnectedBeforeFollow(t *testing.T) {
	// Fake apiserver: block the follow-log request without sending any bytes,
	// mimicking a stalled/proxy-buffered upstream stream.
	apiserver := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/log") {
			// Accept the connection but never respond: hang until the client
			// disconnects (request context cancelled), so the goroutine exits.
			<-r.Context().Done()
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer apiserver.Close()

	client, err := kubernetes.NewForConfig(&rest.Config{Host: apiserver.URL})
	if err != nil {
		t.Fatalf("build clientset: %v", err)
	}
	prev := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prev) })

	s := &Server{}
	router := chi.NewRouter()
	router.Get("/api/pods/{namespace}/{name}/logs/stream", s.handlePodLogsStream)
	srv := httptest.NewServer(router)
	defer srv.Close()

	type result struct {
		status int
		body   string
		err    error
	}
	// Cancel any in-flight request on teardown so the blocked follow stream and
	// its connections unwind instead of leaking past the test.
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)

	resCh := make(chan result, 1)
	go func() {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/pods/prod/web/logs/stream?container=app", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			resCh <- result{err: err}
			return
		}
		defer resp.Body.Close()
		// Read the first flushed chunk (headers arrive with it). The follow
		// body never flows, so read just enough to see the connected frame.
		buf := make([]byte, 512)
		n, _ := resp.Body.Read(buf)
		resCh <- result{status: resp.StatusCode, body: string(buf[:n])}
	}()

	select {
	case res := <-resCh:
		if res.err != nil {
			t.Fatalf("request failed: %v", res.err)
		}
		if res.status != http.StatusOK {
			t.Fatalf("status = %d, want 200 before the follow stream flows", res.status)
		}
		if !strings.Contains(res.body, "event: connected") {
			t.Fatalf("expected `connected` SSE frame before the blocking follow stream, got: %q", res.body)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out: no 200 / connected frame arrived before deadline — pod-log EventSource pends indefinitely")
	}
}
