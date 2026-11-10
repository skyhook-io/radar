package k8score

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// read marks a request bounded by the idle, header and ceiling timeouts.
const read = time.Duration(-1)

func TestRequestDeadlineClassify(t *testing.T) {
	cases := []struct {
		name   string
		method string
		url    string
		header http.Header
		want   time.Duration
		server string // path prefix of the configured server URL
	}{
		{"cluster list", "GET", "/api/v1/secrets?resourceVersion=5", nil, read, ""},
		{"namespaced list page", "GET", "/apis/apps/v1/namespaces/a/deployments?limit=500&continue=x", nil, read, ""},
		{"single get", "GET", "/api/v1/namespaces/a/secrets/db", nil, read, ""},
		{"namespace object", "GET", "/api/v1/namespaces/log", nil, read, ""},
		{"namespace status", "GET", "/api/v1/namespaces/a/status", nil, read, ""},
		{"object named log", "GET", "/api/v1/namespaces/a/configmaps/log", nil, read, ""},
		{"crd resource named proxy", "GET", "/apis/example.com/v1/namespaces/a/proxy", nil, read, ""},
		{"proxied server prefix", "GET", "/k8s/clusters/c-1/api/v1/namespaces/a/pods/p/log", nil, 0, "/k8s/clusters/c-1"},
		{"discovery", "GET", "/version", nil, read, ""},
		{"API-looking server prefix, log", "GET", "/api/v1/clusters/c-1/api/v1/namespaces/a/pods/p/log", nil, 0, "/api/v1/clusters/c-1"},
		{"API-looking server prefix, proxy tail", "GET", "/api/v1/clusters/c-1/api/v1/namespaces/a/services/prom:9090/proxy/api/v1/query", nil, 0, "/api/v1/clusters/c-1"},
		{"server prefix ending in proxy, list", "GET", "/api/v1/clusters/c-1/proxy/api/v1/secrets", nil, read, "/api/v1/clusters/c-1/proxy/"},
		{"prefix-less list under apis", "GET", "/apis/apps/v1beta2/deployments", nil, read, ""},
		{"api path inside a proxied tail", "GET", "/api/v1/namespaces/m/services/prom:9090/proxy/api/v1/query", nil, 0, ""},
		{"watch with timeout", "GET", "/api/v1/secrets?watch=true&timeoutSeconds=300", nil, 6 * time.Minute, ""},
		{"watch=1", "GET", "/api/v1/secrets?watch=1&timeoutSeconds=10", nil, 70 * time.Second, ""},
		{"watchlist", "GET", "/api/v1/secrets?watch=true&sendInitialEvents=true&timeoutSeconds=540", nil, 10 * time.Minute, ""},
		{"legacy watch path", "GET", "/api/v1/watch/namespaces/a/pods?timeoutSeconds=60", nil, 2 * time.Minute, ""},
		{"watch without timeout", "GET", "/api/v1/secrets?watch=true", nil, 0, ""},
		{"pod log", "GET", "/api/v1/namespaces/a/pods/p/log?container=c", nil, 0, ""},
		{"follow", "GET", "/api/v1/namespaces/a/pods/p/log?follow=true", nil, 0, ""},
		{"service proxy", "GET", "/api/v1/namespaces/m/services/prom:9090/proxy/api/v1/query", nil, 0, ""},
		{"node proxy", "GET", "/api/v1/nodes/n1/proxy/metrics", nil, 0, ""},
		{"exec upgrade", "GET", "/api/v1/namespaces/a/pods/p/exec?command=sh", http.Header{"Upgrade": {"websocket"}}, 0, ""},
		{"portforward", "GET", "/api/v1/namespaces/a/pods/p/portforward", nil, 0, ""},
		{"post", "POST", "/api/v1/namespaces/a/secrets", nil, 0, ""},
		{"patch", "PATCH", "/api/v1/namespaces/a/secrets/db", nil, 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, "https://apiserver"+tc.url, nil)
			for k, v := range tc.header {
				req.Header[k] = v
			}
			rt := WrapRequestDeadlines(tc.server)(http.DefaultTransport).(*requestDeadlineRoundTripper)
			bounded, watch := rt.classify(req)
			got := watch
			if bounded && watch == 0 {
				got = read
			}
			if got != tc.want {
				t.Fatalf("classify(%s %s) = %v, want %v", tc.method, tc.url, got, tc.want)
			}
		})
	}
}

// A client built from a Host with an API-looking path prefix classifies its
// requests by what follows the prefix: the pod log stream is not cut, and a
// stalled LIST still is.
func TestRequestDeadlines_PrefixedServerURL(t *testing.T) {
	const prefix = "/api/v1/clusters/c-1/proxy"
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch strings.TrimPrefix(r.URL.Path, prefix) {
		case "/api/v1/namespaces/a/pods/p/log":
			w.WriteHeader(http.StatusOK)
			w.(http.Flusher).Flush()
			time.Sleep(300 * time.Millisecond)
			_, _ = io.WriteString(w, "done")
		case "/api/v1/namespaces/a/secrets":
			select {
			case <-r.Context().Done():
			case <-release:
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	defer close(release)

	cfg := &rest.Config{Host: srv.URL + prefix}
	serverURL, _, err := rest.DefaultServerUrlFor(cfg)
	if err != nil {
		t.Fatalf("DefaultServerUrlFor: %v", err)
	}
	rt := WrapRequestDeadlines(serverURL.Path)(http.DefaultTransport).(*requestDeadlineRoundTripper)
	rt.headerTimeout = 100 * time.Millisecond
	cfg.Transport = rt
	client := kubernetes.NewForConfigOrDie(cfg)

	ctx := context.Background()
	body, err := client.CoreV1().Pods("a").GetLogs("p", &corev1.PodLogOptions{}).DoRaw(ctx)
	if err != nil || string(body) != "done" {
		t.Fatalf("log stream behind a prefixed server was cut: body=%q err=%v", body, err)
	}
	start := time.Now()
	if _, err := client.CoreV1().Secrets("a").List(ctx, metav1.ListOptions{}); err == nil {
		t.Fatal("stalled LIST behind a prefixed server should have hit the deadline")
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("stalled LIST behind a prefixed server was not ended by the deadline")
	}
}

// testRoundTripper is the production wrapper with short bounds.
func testRoundTripper(idle, header, ceiling time.Duration) *requestDeadlineRoundTripper {
	rt := WrapRequestDeadlines("")(http.DefaultTransport).(*requestDeadlineRoundTripper)
	rt.idle = idle
	rt.headerTimeout = header
	rt.ceiling = ceiling
	return rt
}

func TestRequestDeadlines_StallsEndSlowTransfersFinish(t *testing.T) {
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hold := func() {
			select {
			case <-r.Context().Done():
			case <-release:
			}
		}
		switch r.URL.Path {
		case "/api/v1/secrets": // never sends headers
			hold()
		case "/api/v1/configmaps": // headers and a first chunk, then nothing
			w.WriteHeader(http.StatusOK)
			_, _ = io.WriteString(w, `{"kind":`)
			w.(http.Flusher).Flush()
			hold()
		case "/api/v1/pods": // a slow transfer that keeps delivering bytes
			w.WriteHeader(http.StatusOK)
			for i := 0; i < 12; i++ {
				_, _ = io.WriteString(w, "x")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(50 * time.Millisecond):
				}
			}
		case "/api/v1/events": // trickles bytes forever
			w.WriteHeader(http.StatusOK)
			for {
				_, _ = io.WriteString(w, "x")
				w.(http.Flusher).Flush()
				select {
				case <-r.Context().Done():
					return
				case <-time.After(20 * time.Millisecond):
				}
			}
		}
	}))
	defer srv.Close()
	defer close(release)
	client := &http.Client{Transport: testRoundTripper(200*time.Millisecond, 300*time.Millisecond, 2*time.Second)}

	t.Run("no headers ends at the header timeout", func(t *testing.T) {
		start := time.Now()
		_, err := client.Get(srv.URL + "/api/v1/secrets")
		if !errors.Is(err, errRequestEnded) {
			t.Fatalf("err = %v, want a stall", err)
		}
		if d := time.Since(start); d < 250*time.Millisecond || d > 2*time.Second {
			t.Fatalf("ended after %v, want about the 300ms header timeout", d)
		}
	})

	t.Run("a body that stops ends at the idle timeout", func(t *testing.T) {
		resp, err := client.Get(srv.URL + "/api/v1/configmaps")
		if err != nil {
			t.Fatalf("headers should arrive: %v", err)
		}
		start := time.Now()
		_, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !errors.Is(err, errRequestEnded) {
			t.Fatalf("err = %v, want a stall", err)
		}
		if n := strings.Count(err.Error(), errRequestEnded.Error()); n != 1 {
			t.Fatalf("the cause should appear once, got %q", err)
		}
		if d := time.Since(start); d < 150*time.Millisecond || d > 2*time.Second {
			t.Fatalf("ended after %v, want about the 200ms idle timeout", d)
		}
	})

	t.Run("a slow transfer longer than the idle timeout is not cut", func(t *testing.T) {
		start := time.Now()
		resp, err := client.Get(srv.URL + "/api/v1/pods")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		body, err := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if err != nil || len(body) != 12 {
			t.Fatalf("slow transfer was cut: %d bytes, err=%v", len(body), err)
		}
		if d := time.Since(start); d < 400*time.Millisecond {
			t.Fatalf("transfer took %v; the test needs it to outlast the idle and header timeouts", d)
		}
	})

	t.Run("a transfer that never ends stops at the ceiling", func(t *testing.T) {
		start := time.Now()
		resp, err := client.Get(srv.URL + "/api/v1/events")
		if err != nil {
			t.Fatalf("get: %v", err)
		}
		_, err = io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		if !errors.Is(err, errRequestEnded) || !strings.Contains(err.Error(), "still running") {
			t.Fatalf("err = %v, want the ceiling", err)
		}
		if d := time.Since(start); d < 1500*time.Millisecond || d > 4*time.Second {
			t.Fatalf("ended after %v, want about the 2s ceiling", d)
		}
	})

	t.Run("a caller's shorter deadline still wins", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
		defer cancel()
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, srv.URL+"/api/v1/secrets", nil)
		start := time.Now()
		if _, err := client.Do(req); !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v", err)
		}
		if time.Since(start) > 200*time.Millisecond {
			t.Fatal("caller's shorter deadline was not honored")
		}
	})
}

// A quiet watch is healthy: it is bounded only by its timeoutSeconds plus
// grace, never by the idle timeout.
func TestRequestDeadlines_QuietWatchIsNotIdleTimed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(500 * time.Millisecond):
			_, _ = io.WriteString(w, "event")
		}
	}))
	defer srv.Close()
	client := &http.Client{Transport: testRoundTripper(100*time.Millisecond, 100*time.Millisecond, time.Second)}
	resp, err := client.Get(srv.URL + "/api/v1/secrets?watch=true&timeoutSeconds=60")
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	body, err := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if err != nil || string(body) != "event" {
		t.Fatalf("quiet watch was cut: body=%q err=%v", body, err)
	}
}

// A watch that outlives its timeoutSeconds plus grace is ended with an error
// client-go passes on instead of retrying, so the reflector lists again rather
// than silently re-watching a dead path.
func TestRequestDeadlines_DeadWatchSurfacesAsError(t *testing.T) {
	var attempts atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("watch") != "true" {
			http.NotFound(w, r)
			return
		}
		if attempts.Add(1) == 1 || r.URL.Query().Get("resourceVersion") == "hang-headers" {
			<-r.Context().Done() // no headers
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done() // headers, then nothing
	}))
	defer srv.Close()
	rt := WrapRequestDeadlines("")(http.DefaultTransport).(*requestDeadlineRoundTripper)
	rt.watchGrace = 200 * time.Millisecond
	cfg := &rest.Config{Host: srv.URL, Transport: rt}
	client := kubernetes.NewForConfigOrDie(cfg)
	timeout := int64(1) // the deadline is timeoutSeconds plus grace: 1.2s
	ctx := context.Background()

	_, err := client.CoreV1().Secrets("").Watch(ctx, metav1.ListOptions{TimeoutSeconds: &timeout, ResourceVersion: "hang-headers"})
	if !errors.Is(err, errRequestEnded) {
		t.Fatalf("a watch with no headers: err = %v, want it ended by Radar", err)
	}
	if n := attempts.Load(); n != 1 {
		t.Fatalf("client-go retried the ended watch %d times; it must surface the error", n-1)
	}

	w, err := client.CoreV1().Secrets("").Watch(ctx, metav1.ListOptions{TimeoutSeconds: &timeout})
	if err != nil {
		t.Fatalf("watch: %v", err)
	}
	defer w.Stop()
	select {
	case ev, ok := <-w.ResultChan():
		if !ok || ev.Type != watch.Error {
			t.Fatalf("a dead established watch must end with an error event, got ok=%v %v", ok, ev.Type)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("a dead established watch was not ended")
	}
}
