package cloud

import (
	"bytes"
	"context"
	"encoding/json"
	"encoding/pem"
	"errors"
	"log"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
)

const (
	testNamespace  = "radar"
	testClusterUID = "0b6c7c43-6f0e-4a8e-9d6c-5d2f4f6b7a10"
	testKey        = "rhe_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcd"
)

// newEnrollClientset holds the kube-system namespace Radar reads its
// cluster identity from.
func newEnrollClientset() *fake.Clientset {
	return fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(testClusterUID)}})
}

// newEnrollClientsetWithAPIServer also publishes kube-public/cluster-info
// naming https://api.prod-eu.example.com:6443.
func newEnrollClientsetWithAPIServer() *fake.Clientset {
	kubeconfig := "apiVersion: v1\nkind: Config\nclusters:\n- name: \"\"\n  cluster:\n    server: https://api.prod-eu.example.com:6443\n"
	return fake.NewClientset(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(testClusterUID)}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "kube-public", Name: "cluster-info"}, Data: map[string]string{"kubeconfig": kubeconfig}},
	)
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func writeKeyFile(t *testing.T, key string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "key")
	if err := os.WriteFile(path, []byte(key+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

type hubReply struct {
	status     int
	body       string
	retryAfter string
}

// fakeHub answers enroll calls from a script; the last reply repeats.
type fakeHub struct {
	t       *testing.T
	mu      sync.Mutex
	replies []hubReply
	reqs    []EnrollRequest
	keys    []string
	onCall  func(EnrollRequest)
	srv     *httptest.Server
}

func newFakeHub(t *testing.T, replies ...hubReply) *fakeHub {
	h := &fakeHub{t: t, replies: replies}
	h.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != enrollPath || r.Method != http.MethodPost {
			http.NotFound(w, r)
			return
		}
		var req EnrollRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("decode enroll body: %v", err)
		}
		h.mu.Lock()
		h.reqs = append(h.reqs, req)
		h.keys = append(h.keys, strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "))
		reply := h.replies[0]
		if len(h.replies) > 1 {
			h.replies = h.replies[1:]
		}
		onCall := h.onCall
		h.mu.Unlock()
		if onCall != nil {
			onCall(req)
		}
		if reply.retryAfter != "" {
			w.Header().Set("Retry-After", reply.retryAfter)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(reply.status)
		_, _ = w.Write([]byte(reply.body))
	}))
	t.Cleanup(h.srv.Close)
	return h
}

func (h *fakeHub) setReplies(replies ...hubReply) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.replies = replies
}

func (h *fakeHub) requests() []EnrollRequest {
	h.mu.Lock()
	defer h.mu.Unlock()
	return append([]EnrollRequest(nil), h.reqs...)
}

func joined(token string) hubReply {
	return hubReply{status: 200, body: `{"cluster_id":"c-` + token + `","token":"` + token + `","agent_url":"ws://127.0.0.1:1/agent","name":"hub-name-` + token + `"}`}
}

func refusal(status int, code, message string) hubReply {
	body, _ := json.Marshal(map[string]string{"error": code, "message": message})
	return hubReply{status: status, body: string(body)}
}

// fakeTunnel records each tunnel the enroller starts. A tunnel reports an
// open session, then runs until its context ends or returns what the test
// pushes into exit.
type fakeTunnel struct {
	mu      sync.Mutex
	configs []Config
	running int
	exit    chan error
	// neverConnects: the tunnel ends before it ever comes up.
	neverConnects bool
}

func newFakeTunnel() *fakeTunnel { return &fakeTunnel{exit: make(chan error, 1)} }

func (f *fakeTunnel) run(ctx context.Context, cfg Config) error {
	f.mu.Lock()
	f.configs = append(f.configs, cfg)
	f.running++
	f.mu.Unlock()
	defer func() {
		f.mu.Lock()
		f.running--
		f.mu.Unlock()
	}()
	if cfg.OnConnected != nil && !f.neverConnects {
		cfg.OnConnected()
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-f.exit:
		return err
	}
}

func (f *fakeTunnel) started() []Config {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]Config(nil), f.configs...)
}

func (f *fakeTunnel) isRunning() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.running > 0
}

func fastEnrollDelays(t *testing.T) {
	t.Helper()
	prev := []time.Duration{enrollMinBackoff, enrollMaxBackoff, enrollLongDelay, enrollNotReadyDelay, enrollConnectedDelay, enrollDefaultRateDelay}
	enrollMinBackoff, enrollMaxBackoff, enrollLongDelay, enrollNotReadyDelay, enrollConnectedDelay, enrollDefaultRateDelay = time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, time.Millisecond, time.Millisecond, time.Millisecond
	t.Cleanup(func() {
		enrollMinBackoff, enrollMaxBackoff, enrollLongDelay, enrollNotReadyDelay, enrollConnectedDelay, enrollDefaultRateDelay = prev[0], prev[1], prev[2], prev[3], prev[4], prev[5]
	})
}

// startEnroller runs the enroller until the test ends.
func startEnroller(t *testing.T, cs *fake.Clientset, hub *fakeHub, keyFile string, tunnel *fakeTunnel, names NameOptions) {
	t.Helper()
	e, err := newEnroller(EnrollmentConfig{
		HubOrigin: hub.srv.URL,
		KeyFile:   keyFile,
		Client:    cs,
		Namespace: testNamespace,
		Release:   "radar",
		Names:     names,
		Tunnel:    Config{Handler: http.NotFoundHandler()},
	})
	if err != nil {
		t.Fatalf("newEnroller: %v", err)
	}
	e.runTunnel = tunnel.run
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = e.run(ctx); close(done) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

// captureLog collects what the package logs until the test ends.
func captureLog(t *testing.T) func() string {
	t.Helper()
	var mu sync.Mutex
	var buf bytes.Buffer
	prevOut, prevFlags := log.Writer(), log.Flags()
	log.SetOutput(writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return buf.Write(p)
	}))
	log.SetFlags(0)
	t.Cleanup(func() {
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return func() string {
		mu.Lock()
		defer mu.Unlock()
		return buf.String()
	}
}

type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

func TestEnrollmentJoinsAndServesTheTunnelWithTheTokenInMemory(t *testing.T) {
	fastEnrollDelays(t)
	cs := newEnrollClientsetWithAPIServer()
	hub := newFakeHub(t, joined("rhc_one"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{Prefix: "nkp-"})

	eventually(t, "the tunnel", func() bool { return len(tunnel.started()) == 1 })
	cfg := tunnel.started()[0]
	if cfg.Token != "rhc_one" || cfg.ClusterID != "c-rhc_one" || cfg.URL != "ws://127.0.0.1:1/agent" || !cfg.Enrolled {
		t.Fatalf("tunnel config = %+v", cfg)
	}
	if cfg.ClusterName != "nkp-api.prod-eu.example.com" {
		t.Fatalf("tunnel name %q, want the name Radar picked", cfg.ClusterName)
	}
	req := hub.requests()[0]
	if req.AgentUID != testClusterUID || req.Namespace != testNamespace || req.Release != "radar" || req.Name != "nkp-api.prod-eu.example.com" || req.NameSource != NameSourceAPIServer {
		t.Fatalf("enroll request = %+v", req)
	}
}

func TestEnrollmentNeverWritesToKubernetes(t *testing.T) {
	fastEnrollDelays(t)
	cs := newEnrollClientset()
	hub := newFakeHub(t, joined("rhc_one"), refusal(409, "already_connected", ""), joined("rhc_two"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the first tunnel", func() bool { return tunnel.isRunning() })
	tunnel.exit <- ErrTokenRejected
	eventually(t, "a second tunnel", func() bool { return len(tunnel.started()) == 2 && tunnel.isRunning() })
	for _, a := range cs.Actions() {
		if a.GetResource().Resource == "secrets" || (a.GetVerb() != "get" && a.GetVerb() != "list" && a.GetVerb() != "watch") {
			t.Fatalf("enrollment made a %s call on %s", a.GetVerb(), a.GetResource())
		}
	}
}

// A cluster-info read that fails while the API server is briefly unreachable
// is retried: joining under the cluster-<uid> fallback would rename the
// cluster in the Hub.
func TestEnrollmentRetriesAnUnreadableAPIServerAddress(t *testing.T) {
	fastEnrollDelays(t)
	cs := newEnrollClientsetWithAPIServer()
	failed := false
	cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !failed {
			failed = true
			return true, nil, errors.New("connection refused")
		}
		return false, nil, nil
	})
	hub := newFakeHub(t, joined("rhc_one"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the tunnel", func() bool { return len(tunnel.started()) == 1 })
	if reqs := hub.requests(); len(reqs) != 1 || reqs[0].Name != "api.prod-eu.example.com" {
		t.Fatalf("enroll requests = %+v, want one, named after the API server", reqs)
	}
}

// A cluster that forbids reading cluster-info has no address to give, so
// the cluster-<uid> name is its real default and Radar joins with it.
func TestEnrollmentJoinsWhenTheAPIServerAddressIsForbidden(t *testing.T) {
	fastEnrollDelays(t)
	cs := newEnrollClientsetWithAPIServer()
	cs.PrependReactor("get", "configmaps", func(k8stesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewForbidden(schema.GroupResource{Resource: "configmaps"}, "cluster-info", errors.New("denied"))
	})
	hub := newFakeHub(t, joined("rhc_one"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the tunnel", func() bool { return len(tunnel.started()) == 1 })
	if name := hub.requests()[0].Name; name != "cluster-0b6c7c43" {
		t.Fatalf("name = %q, want the cluster-<uid> default", name)
	}
}

// Production delays: a token that worked and is then refused is replaced at
// once, not after the join backoff.
func TestTokenRejectedEnrollsAgain(t *testing.T) {
	cs := newEnrollClientset()
	hub := newFakeHub(t, joined("rhc_one"), joined("rhc_two"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the first tunnel", func() bool { return tunnel.isRunning() })
	tunnel.exit <- ErrTokenRejected
	eventually(t, "a tunnel with the new token", func() bool {
		started := tunnel.started()
		return len(started) == 2 && started[1].Token == "rhc_two"
	})
	if n := len(hub.requests()); n != 2 {
		t.Fatalf("enroll calls = %d, want 2", n)
	}
}

// A token refused before the tunnel ever came up waits the join backoff, so
// a Hub that refuses every new token isn't hammered.
func TestTokenRefusedBeforeConnectingWaits(t *testing.T) {
	cs := newEnrollClientset()
	hub := newFakeHub(t, joined("rhc_one"), joined("rhc_two"))
	tunnel := newFakeTunnel()
	tunnel.neverConnects = true
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the first tunnel", func() bool { return tunnel.isRunning() })
	tunnel.exit <- ErrTokenRejected
	time.Sleep(time.Second)
	if n := len(hub.requests()); n != 1 {
		t.Fatalf("enroll calls within a second = %d, want 1", n)
	}
}

func TestEnrollRefusalsWaitAndExplain(t *testing.T) {
	// Production delays: these are the retry promises the log lines make.
	within := func(d, base time.Duration) bool { return d >= base && d < base+base/5+time.Millisecond }
	tests := []struct {
		name   string
		err    *EnrollError
		reason string
		delay  func(time.Duration) bool
		long   bool
	}{
		{"bad request", &EnrollError{StatusCode: 400, Code: "bad_request"}, "could not read the join request", func(d time.Duration) bool { return d == 0 }, false},
		{"key unknown", &EnrollError{StatusCode: 401, Code: "key_unknown"}, "The Hub rejected the enrollment key.", func(d time.Duration) bool { return d == 0 }, false},
		{"key deleted", &EnrollError{StatusCode: 401, Code: "key_deleted"}, "rejected the enrollment key", func(d time.Duration) bool { return d == 0 }, false},
		{"key replaced", &EnrollError{StatusCode: 401, Code: "key_replaced"}, "rejected the enrollment key", func(d time.Duration) bool { return d == 0 }, false},
		{"plan limit", &EnrollError{StatusCode: 402}, "license or plan limit", func(d time.Duration) bool { return within(d, 10*time.Minute) }, true},
		{"removed", &EnrollError{StatusCode: 403, Code: "cluster_removed"}, "This cluster was removed in Radar Hub. An owner can let it back in under Settings > Enrollment keys.", func(d time.Duration) bool { return within(d, 10*time.Minute) }, true},
		{"pending approval", &EnrollError{StatusCode: 403, Code: "pending_approval"}, "This cluster is waiting for an owner to approve it in Radar Hub.", func(d time.Duration) bool { return within(d, 30*time.Second) }, false},
		{"key full", &EnrollError{StatusCode: 409, Code: "key_full"}, "An owner can raise it on the Enrollment keys page in Radar Hub.", func(d time.Duration) bool { return within(d, 10*time.Minute) }, true},
		{"already connected", &EnrollError{StatusCode: 409, Code: "already_connected"}, "Another Radar with this cluster's identity is connected", func(d time.Duration) bool { return within(d, 15*time.Second) }, false},
		{"no agent address", &EnrollError{StatusCode: 409, Code: "no_agent_address"}, "no agent address", func(d time.Duration) bool { return d == 0 }, false},
		{"rate limited", &EnrollError{StatusCode: 429, RetryAfter: 42 * time.Second, HasRetryAfter: true}, "rate limiting", func(d time.Duration) bool { return d == 42*time.Second }, false},
		{"rate limited, Retry-After capped", &EnrollError{StatusCode: 429, RetryAfter: 24 * time.Hour, HasRetryAfter: true}, "rate limiting", func(d time.Duration) bool { return d == 10*time.Minute }, false},
		{"rate limited, no Retry-After", &EnrollError{StatusCode: 429}, "rate limiting", func(d time.Duration) bool { return d == time.Minute }, false},
		{"not ready", &EnrollError{StatusCode: 503, Code: "hub_not_ready"}, "not ready", func(d time.Duration) bool { return within(d, 30*time.Second) }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for i := 0; i < 20; i++ {
				out := classifyEnrollError(tt.err)
				if !strings.Contains(out.reason, tt.reason) || !tt.delay(out.delay) || out.long != tt.long {
					t.Fatalf("got %+v", out)
				}
			}
			withMsg := *tt.err
			withMsg.Message = "The Hub's own sentence."
			if got := classifyEnrollError(&withMsg).reason; got != "The Hub's own sentence." {
				t.Fatalf("reason with a Hub message = %q", got)
			}
		})
	}
	if out := classifyEnrollError(errors.New("dial tcp: connection refused")); out.delay != 0 || !strings.Contains(out.reason, "connection refused") {
		t.Fatalf("network error: %+v", out)
	}
}

func TestRemovedClusterLogsAndChecksAgainWithoutARestart(t *testing.T) {
	fastEnrollDelays(t)
	logs := captureLog(t)
	cs := newEnrollClientset()
	hub := newFakeHub(t, refusal(403, "cluster_removed", ""))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	const want = "This cluster was removed in Radar Hub. An owner can let it back in under Settings > Enrollment keys. Checking again every 10 minutes."
	eventually(t, "the removed message in the log", func() bool { return strings.Contains(logs(), want) })
	eventually(t, "several retries", func() bool { return len(hub.requests()) >= 3 })
	if n := strings.Count(logs(), want); n != 1 {
		t.Fatalf("the unchanged refusal was logged %d times, want once", n)
	}
	// Let back in: the next retry joins.
	hub.setReplies(joined("rhc_back"))
	eventually(t, "a join after the owner let it back in", func() bool { return len(tunnel.started()) == 1 })
}

func TestHubMessageIsLoggedForARefusal(t *testing.T) {
	fastEnrollDelays(t)
	logs := captureLog(t)
	cs := newEnrollClientset()
	hub := newFakeHub(t, refusal(409, "key_full", "This key has reached its limit of 50 clusters. An owner can raise it on the Enrollment keys page in Radar Hub."))
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), newFakeTunnel(), NameOptions{})

	eventually(t, "the Hub's message in the log", func() bool {
		return strings.Contains(logs(), "This key has reached its limit of 50 clusters. An owner can raise it on the Enrollment keys page in Radar Hub. Checking again every 10 minutes.")
	})
}

func TestEnrollClientStatusHandling(t *testing.T) {
	tests := []struct {
		name        string
		reply       hubReply
		wantStatus  int
		wantCode    string
		wantMessage string
		wantRetry   time.Duration
	}{
		{name: "removed", reply: refusal(403, "cluster_removed", "This cluster was removed in Radar Hub."), wantStatus: 403, wantCode: "cluster_removed", wantMessage: "This cluster was removed in Radar Hub."},
		{name: "already connected", reply: hubReply{status: 409, body: `{"error":"already_connected"}`}, wantStatus: 409, wantCode: "already_connected"},
		{name: "rate limited", reply: hubReply{status: 429, body: `{}`, retryAfter: "17"}, wantStatus: 429, wantRetry: 17 * time.Second},
		{name: "license limit", reply: hubReply{status: 402, body: `{"error":"license_limit","limit":3}`}, wantStatus: 402, wantCode: "license_limit"},
		{name: "message with control characters", reply: refusal(401, "key_unknown", "bad\r\nkey"), wantStatus: 401, wantCode: "key_unknown", wantMessage: "bad  key"},
		{name: "not ready", reply: hubReply{status: 503, body: `{"error":"hub_not_ready"}`}, wantStatus: 503, wantCode: "hub_not_ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t, tt.reply)
			c, err := NewEnrollClient(hub.srv.URL, writeKeyFile(t, testKey), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Enroll(context.Background(), EnrollRequest{})
			var he *EnrollError
			if !errors.As(err, &he) {
				t.Fatalf("err = %v, want *EnrollError", err)
			}
			if he.StatusCode != tt.wantStatus || he.Code != tt.wantCode || he.Message != tt.wantMessage || he.RetryAfter != tt.wantRetry {
				t.Fatalf("got %+v", he)
			}
		})
	}

	t.Run("joined", func(t *testing.T) {
		hub := newFakeHub(t, joined("rhc_same"))
		c, _ := NewEnrollClient(hub.srv.URL, writeKeyFile(t, testKey), nil)
		res, err := c.Enroll(context.Background(), EnrollRequest{})
		if err != nil || res.Token != "rhc_same" || res.ClusterID != "c-rhc_same" || res.AgentURL != "ws://127.0.0.1:1/agent" {
			t.Fatalf("%+v %v", res, err)
		}
	})

	t.Run("a response without agent_url is refused", func(t *testing.T) {
		hub := newFakeHub(t, hubReply{status: 200, body: `{"cluster_id":"c","token":"rhc"}`})
		c, _ := NewEnrollClient(hub.srv.URL, writeKeyFile(t, testKey), nil)
		if _, err := c.Enroll(context.Background(), EnrollRequest{}); err == nil {
			t.Fatal("accepted a join with no agent address")
		}
	})

	t.Run("plaintext agent_url to a remote host is refused", func(t *testing.T) {
		hub := newFakeHub(t, hubReply{status: 200, body: `{"cluster_id":"c","token":"rhc","agent_url":"ws://hub.example.com/agent"}`})
		c, _ := NewEnrollClient(hub.srv.URL, writeKeyFile(t, testKey), nil)
		if _, err := c.Enroll(context.Background(), EnrollRequest{}); err == nil {
			t.Fatal("accepted ws:// to a non-loopback host")
		}
	})

	t.Run("redirects are not followed", func(t *testing.T) {
		var followed bool
		target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { followed = true }))
		defer target.Close()
		redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, target.URL+enrollPath, http.StatusTemporaryRedirect)
		}))
		defer redirect.Close()
		c, _ := NewEnrollClient(redirect.URL, writeKeyFile(t, testKey), nil)
		if _, err := c.Enroll(context.Background(), EnrollRequest{}); err == nil || followed {
			t.Fatalf("redirect followed=%v err=%v", followed, err)
		}
	})

	t.Run("a malformed key file never reaches the Hub", func(t *testing.T) {
		hub := newFakeHub(t, joined("rhc"))
		c, _ := NewEnrollClient(hub.srv.URL, writeKeyFile(t, "not-a-key"), nil)
		if _, err := c.Enroll(context.Background(), EnrollRequest{}); err == nil || len(hub.requests()) != 0 {
			t.Fatalf("err=%v calls=%d", err, len(hub.requests()))
		}
	})
}

func TestResolveClusterName(t *testing.T) {
	long := strings.Repeat("h", 100)
	tests := []struct {
		name       string
		opts       NameOptions
		apiServer  string
		wantName   string
		wantSource string
	}{
		{"exact wins over prefix and API server", NameOptions{Exact: "prod-eu", Prefix: "p-"}, "https://api.x:6443", "prod-eu", NameSourceValues},
		{"API server host with prefix and suffix", NameOptions{Prefix: "nkp-", Suffix: "-eu"}, "https://10.0.0.5:6443", "nkp-10.0.0.5-eu", NameSourceAPIServer},
		{"UID fallback", NameOptions{Prefix: "nkp-"}, "", "nkp-cluster-0b6c7c43", NameSourceUID},
		{"unparseable API server falls back to UID", NameOptions{}, "://bad", "cluster-0b6c7c43", NameSourceUID},
		{"long host is cut, prefix and suffix kept", NameOptions{Prefix: "a-", Suffix: "-z"}, "https://" + long, "a-" + strings.Repeat("h", 76) + "-z", NameSourceAPIServer},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			name, source := ResolveClusterName(tt.opts, tt.apiServer, testClusterUID)
			if name != tt.wantName || source != tt.wantSource {
				t.Fatalf("got %q (%s), want %q (%s)", name, source, tt.wantName, tt.wantSource)
			}
		})
	}
	if err := (NameOptions{Prefix: strings.Repeat("p", 70)}).Validate(); err == nil {
		t.Fatal("accepted a prefix that leaves no room for a name")
	}
	if err := (NameOptions{Exact: strings.Repeat("n", 81)}).Validate(); err == nil {
		t.Fatal("accepted an 81-character name")
	}
}

func TestRunStopsOnRejectionOnlyWhenEnrolled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Run(ctx, Config{URL: wsURL, Token: "rhc", ClusterID: "c", Handler: http.NotFoundHandler(), Enrolled: true})
	if !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("Run = %v, want ErrTokenRejected", err)
	}

	// A token configured by hand keeps retrying.
	short, cancelShort := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelShort()
	if err := Run(short, Config{URL: wsURL, Token: "rhc", ClusterID: "c", Handler: http.NotFoundHandler()}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("token-mode Run = %v, want it to keep retrying", err)
	}
}

func TestCAFileTrustsAPrivateHub(t *testing.T) {
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"cluster_id":"c","token":"rhc_tls","agent_url":"wss://hub.example.com/agent"}`))
	}))
	defer hub.Close()
	keyFile := writeKeyFile(t, testKey)

	without, _ := NewEnrollClient(hub.URL, keyFile, nil)
	if _, err := without.Enroll(context.Background(), EnrollRequest{}); err == nil {
		t.Fatal("trusted a private CA without the CA file")
	}

	caPath := filepath.Join(t.TempDir(), "ca.crt")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hub.Certificate().Raw})
	if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	ca, err := LoadCAFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	with, _ := NewEnrollClient(hub.URL, keyFile, ca)
	if res, err := with.Enroll(context.Background(), EnrollRequest{}); err != nil || res.Token != "rhc_tls" {
		t.Fatalf("with CA file: %+v %v", res, err)
	}
	if tlsCfg := hubTLSConfig(ca, false); tlsCfg == nil || tlsCfg.RootCAs != ca {
		t.Fatal("the dialer's TLS config does not carry the CA pool")
	}

	empty := filepath.Join(t.TempDir(), "empty.crt")
	_ = os.WriteFile(empty, []byte("no certs here"), 0o600)
	if _, err := LoadCAFile(empty); err == nil {
		t.Fatal("accepted a CA file with no certificates")
	}
}

func TestHubNotReadyKeepsRetryingWithoutBackingOff(t *testing.T) {
	fastEnrollDelays(t)
	logs := captureLog(t)
	cs := newEnrollClientset()
	hub := newFakeHub(t, hubReply{status: 503, body: `{"error":"hub_not_ready"}`})
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "several 503 retries", func() bool { return len(hub.requests()) >= 5 })
	if !strings.Contains(logs(), "The Hub is not ready to add clusters yet. Trying again in ") {
		t.Fatalf("log:\n%s", logs())
	}
	hub.setReplies(joined("rhc_ready"))
	eventually(t, "a join once the Hub is ready", func() bool { return len(tunnel.started()) == 1 })
}

func TestUntrustedHubCertificateNamesTheCABundle(t *testing.T) {
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(joined("rhc").body))
	}))
	defer hub.Close()
	c, _ := NewEnrollClient(hub.URL, writeKeyFile(t, testKey), nil)
	_, err := c.Enroll(context.Background(), EnrollRequest{})
	if err == nil {
		t.Fatal("trusted a self-signed Hub without its CA")
	}
	out := classifyEnrollError(err)
	if !strings.HasPrefix(out.reason, "Radar does not trust the Hub's certificate. "+caBundleFix+" Details: ") {
		t.Fatalf("reason = %q", out.reason)
	}
	if untrustedCert(errors.New("dial tcp: connection refused")) {
		t.Fatal("CA hint for an error that is not about the certificate")
	}
}
