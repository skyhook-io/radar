package loki

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

// fakeLoki answers /loki/api/v1/labels; status other than 200 answers body.
func fakeLoki(t *testing.T, status int, body string, seen *atomic.Value) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if seen != nil {
			seen.Store(r.Header.Clone())
		}
		if r.URL.Path != "/loki/api/v1/labels" {
			http.NotFound(w, r)
			return
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

const labelsOK = `{"status":"success","data":["namespace","pod","container"]}`

func reset(t *testing.T) {
	t.Helper()
	savedList, savedForward, savedContext := listServices, startForward, currentContext
	t.Cleanup(func() {
		listServices, startForward, currentContext = savedList, savedForward, savedContext
		Retire(nil)
	})
	currentContext = func() string { return "ctx" }
	listServices = func() []corev1.Service { return nil }
	startForward = func(context.Context, string, string, int, string) (string, error) {
		return "", errors.New("no forward in tests")
	}
}

func TestManualURLConnectsAndDetects(t *testing.T) {
	reset(t)
	var seen atomic.Value
	srv := fakeLoki(t, 200, labelsOK, &seen)
	Configure(srv.URL, map[string]string{"X-Scope-OrgID": "t"})
	if err := EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := seen.Load().(http.Header).Get("X-Scope-OrgID"); got != "t" {
		t.Errorf("header = %q", got)
	}
	st := CurrentStatus()
	if !st.Configured || !st.Connected || st.Source != "manual" || st.Labels == nil || st.Labels.Pod != "pod" || st.HistoryReason != "" {
		t.Fatalf("status = %+v", st)
	}
}

func TestStatusNeverCarriesSecrets(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	host := strings.TrimPrefix(srv.URL, "http://")
	Configure("http://user:pw@"+host+"/?token=x", map[string]string{"Authorization": "Bearer zzz"})
	_ = EnsureConnected(context.Background())
	data, _ := json.Marshal(CurrentStatus())
	for _, leak := range []string{"pw", "token=x", "zzz"} {
		if strings.Contains(string(data), leak) {
			t.Errorf("status leaks %q: %s", leak, data)
		}
	}
}

func TestLokiErrorIsRedacted(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 401, "bad Bearer zzz", nil)
	Configure(srv.URL, map[string]string{"Authorization": "Bearer zzz"})
	err := EnsureConnected(context.Background())
	if err == nil || strings.Contains(err.Error(), "zzz") {
		t.Fatalf("error = %v", err)
	}
	if strings.Contains(CurrentStatus().Error, "zzz") || CurrentStatus().Error == "" {
		t.Fatalf("status error = %q", CurrentStatus().Error)
	}
}

func TestRetireDisables(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	Configure(srv.URL, nil)
	_ = EnsureConnected(context.Background())
	Retire(errors.New("x"))
	st := CurrentStatus()
	if st.Connected || st.Configured || st.Error != "x" {
		t.Fatalf("status = %+v", st)
	}
	if _, err := Get(context.Background(), "/loki/api/v1/labels", nil); !errors.Is(err, ErrNotConfigured) {
		t.Fatalf("Get after Retire = %v", err)
	}
}

func TestForwardLostDropsAddress(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	listServices = func() []corev1.Service {
		return []corev1.Service{svc("logging", "loki", nil, port("http-metrics", 3100))}
	}
	startForward = func(context.Context, string, string, int, string) (string, error) { return srv.URL, nil }
	Configure("", nil)
	if err := EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	forwardLost("loki", srv.URL)
	if CurrentStatus().Connected {
		t.Fatal("a lost forward still reads as connected")
	}
	forwardLost("prometheus", "http://localhost:1")
	if !CurrentStatus().Configured {
		t.Fatal("an unrelated loss retired Loki")
	}
}

func TestDiscoveryPrefersDirectThenForward(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	listServices = func() []corev1.Service {
		return []corev1.Service{svc("logging", "loki", nil, port("http-metrics", 3100))}
	}
	var forwarded atomic.Bool
	startForward = func(_ context.Context, ns, name string, p int, ctxName string) (string, error) {
		if ns != "logging" || name != "loki" || p != 3100 || ctxName != "ctx" {
			t.Errorf("forward to %s/%s:%d on %s", ns, name, p, ctxName)
		}
		forwarded.Store(true)
		return srv.URL, nil
	}
	Configure("", nil)
	if err := EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	st := CurrentStatus()
	if !forwarded.Load() || st.Source != "discovered" || st.Address != srv.URL || !st.Connected {
		t.Fatalf("status = %+v (the direct Service DNS name can't resolve in tests, so the forward must answer)", st)
	}
}

func TestContextSwitchRediscovers(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	Configure(srv.URL, nil)
	_ = EnsureConnected(context.Background())
	currentContext = func() string { return "other" }
	if CurrentStatus().Connected {
		t.Fatal("a connection found on another context reads as connected")
	}
}

func TestNoLokiFound(t *testing.T) {
	reset(t)
	Configure("", nil)
	if err := EnsureConnected(context.Background()); err == nil || !strings.Contains(err.Error(), "no Loki Service") {
		t.Fatalf("error = %v", err)
	}
}

// Credentials never go to an endpoint nobody named: with headers and no URL,
// discovery doesn't run.
func TestDiscoveryNeverSendsHeaders(t *testing.T) {
	reset(t)
	listServices = func() []corev1.Service {
		t.Error("discovery ran with credentials and no URL")
		return nil
	}
	Configure("", map[string]string{"Authorization": "Bearer zzz"})
	if err := EnsureConnected(context.Background()); err == nil || !strings.Contains(err.Error(), "URL") {
		t.Fatalf("error = %v", err)
	}
}

// A slow attempt doesn't hold its caller past the caller's own deadline.
func TestEnsureConnectedHonoursCallerDeadline(t *testing.T) {
	reset(t)
	release := make(chan struct{})
	// Let the background attempt finish before the seams are restored.
	t.Cleanup(func() {
		close(release)
		for CurrentStatus().Discovering {
			time.Sleep(time.Millisecond)
		}
	})
	listServices = func() []corev1.Service {
		<-release
		return nil
	}
	Configure("", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if err := EnsureConnected(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("error = %v", err)
	}
	if !CurrentStatus().Discovering {
		t.Fatal("the attempt should still be running")
	}
}

// Turning Loki off during discovery stops the forward that attempt opened.
func TestRetireDuringDiscoveryStopsItsForward(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	listServices = func() []corev1.Service {
		return []corev1.Service{svc("logging", "loki", nil, port("http-metrics", 3100))}
	}
	var stops atomic.Int32
	savedStop := stopForward
	t.Cleanup(func() { stopForward = savedStop })
	stopForward = func() { stops.Add(1) }
	startForward = func(context.Context, string, string, int, string) (string, error) {
		Retire(nil) // turned off while this attempt runs
		before := stops.Load()
		t.Cleanup(func() {
			if stops.Load() <= before {
				t.Error("the stale attempt's forward was left running")
			}
		})
		return srv.URL, nil
	}
	Configure("", nil)
	_ = EnsureConnected(context.Background())
}

// A connection found while the context switched belongs to the old cluster:
// it is dropped, and the caller gets a connection for the current one.
func TestContextSwitchDuringAttemptIsNotKept(t *testing.T) {
	reset(t)
	var ctxName atomic.Value
	ctxName.Store("old")
	currentContext = func() string { return ctxName.Load().(string) }
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			ctxName.Store("new") // the switch lands while the first probe is answered
		}
		_, _ = w.Write([]byte(labelsOK))
	}))
	t.Cleanup(srv.Close)
	Configure(srv.URL, nil)
	if err := EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	if requests.Load() < 2 {
		t.Fatal("the attempt from the old context was kept instead of probing again")
	}
	client.mu.Lock()
	got := client.contextName
	client.mu.Unlock()
	if got != "new" {
		t.Fatalf("connection context = %q", got)
	}
}

// Get never reads through a connection made for another context.
func TestGetRechecksTheContext(t *testing.T) {
	reset(t)
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(labelsOK))
	}))
	t.Cleanup(srv.Close)
	Configure(srv.URL, nil)
	if err := EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
	currentContext = func() string { return "other" }
	if _, err := Get(context.Background(), "/loki/api/v1/labels", nil); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	got := client.contextName
	client.mu.Unlock()
	if got != "other" || hits.Load() < 3 {
		t.Fatalf("Get used the old context's connection (context %q, %d requests)", got, hits.Load())
	}
}

// A short header value (a numeric tenant) is not a secret worth scrubbing,
// and replacing it would garble every number in an error.
func TestShortHeaderValuesKeepErrorsReadable(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 401, "no org id 1 for tenant", nil)
	Configure(srv.URL, map[string]string{"X-Scope-OrgID": "1", "Authorization": "Bearer zzzz"})
	err := EnsureConnected(context.Background())
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("error = %v", err)
	}
}

// A gateway Service maps port 80 to its pod's own port; a port-forward goes
// to the pod, so it must use that port, numeric or named.
func TestForwardUsesThePodPort(t *testing.T) {
	for _, tc := range []struct {
		name   string
		target intstr.IntOrString
	}{{"numeric", intstr.FromInt32(8080)}, {"named", intstr.FromString("http")}} {
		t.Run(tc.name, func(t *testing.T) {
			reset(t)
			srv := fakeLoki(t, 200, labelsOK, nil)
			gw := svc("loki", "loki-gateway", nil, corev1.ServicePort{Name: "http", Port: 80, TargetPort: tc.target})
			gw.Spec.Selector = map[string]string{"app": "gw"}
			listServices = func() []corev1.Service { return []corev1.Service{gw} }
			savedNamed := namedPodPort
			t.Cleanup(func() { namedPodPort = savedNamed })
			namedPodPort = func(ns string, sel map[string]string, name string) int {
				if ns == "loki" && sel["app"] == "gw" && name == "http" {
					return 8080
				}
				return 0
			}
			var got atomic.Int32
			startForward = func(_ context.Context, _, _ string, p int, _ string) (string, error) {
				got.Store(int32(p))
				return srv.URL, nil
			}
			Configure("", nil)
			if err := EnsureConnected(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got.Load() != 8080 {
				t.Fatalf("forwarded to pod port %d, want 8080", got.Load())
			}
		})
	}
}

// discoveredAt connects through a "forward" to srv, as discovery does.
func discoveredAt(t *testing.T, srv *httptest.Server) {
	t.Helper()
	listServices = func() []corev1.Service {
		return []corev1.Service{svc("logging", "loki", nil, port("http-metrics", 3100))}
	}
	startForward = func(context.Context, string, string, int, string) (string, error) { return srv.URL, nil }
	Configure("", nil)
	if err := EnsureConnected(context.Background()); err != nil {
		t.Fatal(err)
	}
}

// A read answered after a context switch carries the old cluster's data.
func TestGetDropsAnAnswerFromAnOldContext(t *testing.T) {
	reset(t)
	var ctxName atomic.Value
	ctxName.Store("old")
	currentContext = func() string { return ctxName.Load().(string) }
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/read" {
			ctxName.Store("new")
		}
		_, _ = w.Write([]byte(labelsOK))
	}))
	t.Cleanup(srv.Close)
	Configure(srv.URL, nil)
	if _, err := Get(context.Background(), "/read", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("error = %v, want the answer dropped", err)
	}
}

// A read cancelled by its caller, or timed out, says nothing about Loki.
func TestCancelledReadKeepsTheConnection(t *testing.T) {
	reset(t)
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/slow" {
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(labelsOK))
	}))
	t.Cleanup(slow.Close)
	discoveredAt(t, slow)
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	if _, err := Get(ctx, "/slow", nil); err == nil {
		t.Fatal("want an error")
	}
	if _, ok := Labels(); !ok {
		t.Fatal("a cancelled read dropped the connection")
	}
}

// A read that fails after Loki was found again must not drop the new
// connection, even when it reuses the same local address.
func TestOldFailedReadKeepsANewConnection(t *testing.T) {
	reset(t)
	var srv *httptest.Server
	srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/read" {
			client.mu.Lock()
			client.baseURL = "" // the forward was lost ...
			client.mu.Unlock()
			if err := EnsureConnected(context.Background()); err != nil { // ... and found again
				t.Error(err)
			}
			conn, _, _ := w.(http.Hijacker).Hijack()
			_, _ = conn.Write([]byte("not http\r\n\r\n")) // then the old read fails
			_ = conn.Close()
			return
		}
		_, _ = w.Write([]byte(labelsOK))
	}))
	t.Cleanup(srv.Close)
	discoveredAt(t, srv)
	if _, err := Get(context.Background(), "/read", nil); err == nil {
		t.Fatal("want an error")
	}
	if _, ok := Labels(); !ok {
		t.Fatal("the old read dropped the new connection")
	}
}

// New settings don't wait for a slow attempt made with the old ones.
func TestConfigureCancelsARunningAttempt(t *testing.T) {
	reset(t)
	srv := fakeLoki(t, 200, labelsOK, nil)
	listServices = func() []corev1.Service {
		return []corev1.Service{svc("logging", "loki", nil, port("http-metrics", 3100))}
	}
	started := make(chan struct{})
	startForward = func(ctx context.Context, _, _ string, _ int, _ string) (string, error) {
		close(started)
		<-ctx.Done()
		return "", ctx.Err()
	}
	Configure("", nil)
	first := make(chan struct{})
	go func() { _ = EnsureConnected(context.Background()); close(first) }()
	<-started
	Configure(srv.URL, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := EnsureConnected(ctx); err != nil {
		t.Fatalf("the new URL waited for the old attempt: %v", err)
	}
	<-first
}

// After a failure, status polls don't start discovery again until the
// cooldown has passed.
func TestKickBacksOffAfterAFailure(t *testing.T) {
	reset(t)
	var lists atomic.Int32
	listServices = func() []corev1.Service { lists.Add(1); return nil }
	Configure("", nil)
	_ = EnsureConnected(context.Background())
	for range 5 {
		Kick()
		time.Sleep(5 * time.Millisecond)
	}
	for CurrentStatus().Discovering {
		time.Sleep(time.Millisecond)
	}
	if n := lists.Load(); n != 1 {
		t.Fatalf("discovery ran %d times, want 1", n)
	}
}

// Retiring forgets the old settings, so status shows no stale address.
func TestRetireForgetsTheAddress(t *testing.T) {
	reset(t)
	Configure("http://loki.example.com:3100", map[string]string{"Authorization": "Bearer secret-token"})
	Retire(errors.New("bad header Bearer secret-token"))
	st := CurrentStatus()
	if st.Address != "" || strings.Contains(st.Error, "secret-token") {
		t.Fatalf("status = %+v", st)
	}
}
