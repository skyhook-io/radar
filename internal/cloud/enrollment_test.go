package cloud

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
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
	clienttesting "k8s.io/client-go/testing"
)

const (
	testNamespace  = "radar"
	testSecretName = "radar-cloud-token"
	testClusterUID = "0b6c7c43-6f0e-4a8e-9d6c-5d2f4f6b7a10"
	testKey        = "rhe_AbCdEfGhIjKlMnOpQrStUvWxYz0123456789_-abcd"
)

var secretsGVR = schema.GroupVersionResource{Version: "v1", Resource: "secrets"}

// newTokenSecretClient returns a fake clientset holding the token Secret and
// kube-system, whose Secret updates are refused unless they carry the stored
// resourceVersion, as the real apiserver does.
func newTokenSecretClient(t *testing.T, data map[string][]byte) *fake.Clientset {
	t.Helper()
	cs := fake.NewClientset(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: testSecretName, Namespace: testNamespace, ResourceVersion: "1"}, Data: data},
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(testClusterUID)}},
	)
	var mu sync.Mutex
	next := 1
	cs.PrependReactor("update", "secrets", func(a clienttesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		obj := a.(clienttesting.UpdateAction).GetObject().(*corev1.Secret)
		cur, err := cs.Tracker().Get(secretsGVR, obj.Namespace, obj.Name)
		if err != nil {
			return true, nil, err
		}
		if obj.ResourceVersion != cur.(*corev1.Secret).ResourceVersion {
			return true, nil, apierrors.NewConflict(secretsGVR.GroupResource(), obj.Name, errors.New("resourceVersion mismatch"))
		}
		next++
		obj.ResourceVersion = strconv.Itoa(next)
		return false, nil, nil
	})
	return cs
}

func secretData(t *testing.T, cs *fake.Clientset) map[string][]byte {
	t.Helper()
	s, err := cs.CoreV1().Secrets(testNamespace).Get(context.Background(), testSecretName, metav1.GetOptions{})
	if err != nil {
		t.Fatalf("get Secret: %v", err)
	}
	return s.Data
}

// clearSecret does what `radar cloud reset-enrollment` does.
func clearSecret(t *testing.T, cs *fake.Clientset) {
	t.Helper()
	for {
		s, err := cs.CoreV1().Secrets(testNamespace).Get(context.Background(), testSecretName, metav1.GetOptions{})
		if err != nil {
			t.Fatalf("get Secret: %v", err)
		}
		s.Data = nil
		if _, err := cs.CoreV1().Secrets(testNamespace).Update(context.Background(), s, metav1.UpdateOptions{}); err == nil {
			return
		} else if !apierrors.IsConflict(err) {
			t.Fatalf("clear Secret: %v", err)
		}
	}
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
	return hubReply{status: 200, body: `{"status":"joined","cluster_id":"c-` + token + `","token":"` + token + `","wss_url":"ws://127.0.0.1:1/agent"}`}
}

// fakeTunnel records each tunnel the enroller starts. A tunnel runs until its
// context ends, or returns what the test pushes into exit.
type fakeTunnel struct {
	mu      sync.Mutex
	configs []Config
	running int
	exit    chan error
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
	prev := []time.Duration{enrollMinBackoff, enrollMaxBackoff, enrollLongDelay, enrollNotReadyDelay, enrollDefaultRateDelay, secretRetryDelay}
	enrollMinBackoff, enrollMaxBackoff, enrollLongDelay, enrollNotReadyDelay, enrollDefaultRateDelay, secretRetryDelay = time.Millisecond, 5*time.Millisecond, 5*time.Millisecond, time.Millisecond, time.Millisecond, 5*time.Millisecond
	t.Cleanup(func() {
		enrollMinBackoff, enrollMaxBackoff, enrollLongDelay, enrollNotReadyDelay, enrollDefaultRateDelay, secretRetryDelay = prev[0], prev[1], prev[2], prev[3], prev[4], prev[5]
	})
}

// startEnroller runs the enroller until the test ends.
func startEnroller(t *testing.T, cs *fake.Clientset, hub *fakeHub, keyFile string, tunnel *fakeTunnel, names NameOptions) {
	t.Helper()
	fastEnrollDelays(t)
	e, err := newEnroller(EnrollmentConfig{
		HubOrigin:  hub.srv.URL,
		KeyFile:    keyFile,
		Client:     cs,
		Namespace:  testNamespace,
		Release:    "radar",
		SecretName: testSecretName,
		Names:      names,
		Tunnel:     Config{Handler: http.NotFoundHandler(), APIServerURL: "https://api.prod-eu.example.com:6443"},
	})
	if err != nil {
		t.Fatalf("newEnroller: %v", err)
	}
	e.runTunnel = tunnel.run
	ctx, cancel := context.WithCancel(context.Background())
	watchTokenSecret(ctx, cs, testNamespace, testSecretName, e.changes)
	done := make(chan struct{})
	go func() {
		_ = e.run(ctx)
		close(done)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
}

func TestEnrollmentPersistsNonceBeforeFirstCallThenJoins(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	hub := newFakeHub(t, joined("rhc_first"))
	hub.onCall = func(req EnrollRequest) {
		stored := secretData(t, cs)
		if string(stored[SecretKeyNonce]) != req.Nonce || string(stored[SecretKeyNonceOrigin]) != hub.srv.URL {
			t.Errorf("enroll sent nonce %q before persisting it; Secret holds nonce %q for %q", req.Nonce, stored[SecretKeyNonce], stored[SecretKeyNonceOrigin])
		}
	}
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the tunnel to start", func() bool { return len(tunnel.started()) == 1 })
	data := secretData(t, cs)
	if got := string(data[SecretKeyToken]); got != "rhc_first" {
		t.Fatalf("token = %q", got)
	}
	if string(data[SecretKeyHubOrigin]) != hub.srv.URL || string(data[SecretKeyStatus]) != EnrollStatusJoined || string(data[SecretKeyClusterID]) != "c-rhc_first" {
		t.Fatalf("joined Secret = %v", stringMap(data))
	}
	if _, ok := data[SecretKeyNonce]; ok {
		t.Fatal("nonce kept after joining")
	}
	req := hub.requests()[0]
	if req.AgentUID != testClusterUID || req.Namespace != testNamespace || req.Release != "radar" || len(req.Nonce) != 43 {
		t.Fatalf("enroll request identity = %+v", req)
	}
	if req.Name != "api.prod-eu.example.com" || req.NameSource != NameSourceAPIServer {
		t.Fatalf("name = %q (%s)", req.Name, req.NameSource)
	}
	cfg := tunnel.started()[0]
	if cfg.Token != "rhc_first" || cfg.ClusterID != "c-rhc_first" || cfg.URL != "ws://127.0.0.1:1/agent" || cfg.ClusterUID != testClusterUID || !cfg.Enrolled {
		t.Fatalf("tunnel config = %+v", cfg)
	}
	if hub.keys[0] != testKey {
		t.Fatalf("key sent = %q", hub.keys[0])
	}
}

func TestEnrollmentRetriesTheStoredNonce(t *testing.T) {
	// A restart mid-join: the Secret already holds this attempt.
	cs := newTokenSecretClient(t, map[string][]byte{
		SecretKeyNonce:  []byte("stored-nonce-stored-nonce-stored-nonce-abcde"),
		SecretKeyStatus: []byte(EnrollStatusJoining),
	})
	hub := newFakeHub(t, hubReply{status: 500, body: `{}`}, joined("rhc_same"))
	// NonceOrigin must match the hub; set it now that the URL is known.
	s, _ := cs.CoreV1().Secrets(testNamespace).Get(context.Background(), testSecretName, metav1.GetOptions{})
	s.Data[SecretKeyNonceOrigin] = []byte(hub.srv.URL)
	if _, err := cs.CoreV1().Secrets(testNamespace).Update(context.Background(), s, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{Exact: "prod"})

	eventually(t, "the tunnel to start", func() bool { return len(tunnel.started()) == 1 })
	reqs := hub.requests()
	if len(reqs) != 2 {
		t.Fatalf("enroll calls = %d, want 2", len(reqs))
	}
	for _, r := range reqs {
		if r.Nonce != "stored-nonce-stored-nonce-stored-nonce-abcde" {
			t.Fatalf("retry used nonce %q, not the stored one", r.Nonce)
		}
		if r.Name != "prod" || r.NameSource != NameSourceValues {
			t.Fatalf("name = %q (%s)", r.Name, r.NameSource)
		}
	}
}

func TestEnrollmentClosedStopsUntilResetThenJoinsAgain(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	hub := newFakeHub(t, hubReply{status: 409, body: `{"error":"enrollment_closed"}`})
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the refusal to be recorded", func() bool {
		return string(secretData(t, cs)[SecretKeyStatus]) == EnrollStatusRefused
	})
	data := secretData(t, cs)
	if !strings.Contains(string(data[SecretKeyError]), "radar cloud reset-enrollment") {
		t.Fatalf("enroll_error = %q, want the reset instruction", data[SecretKeyError])
	}
	if data[SecretKeyAt] == nil {
		t.Fatal("enroll_at not set")
	}
	firstNonce := hub.requests()[0].Nonce
	time.Sleep(50 * time.Millisecond)
	if n := len(hub.requests()); n != 1 {
		t.Fatalf("enroll calls after enrollment_closed = %d, want 1", n)
	}

	hub.setReplies(joined("rhc_after_reset"))
	clearSecret(t, cs)
	eventually(t, "a join after reset", func() bool { return len(tunnel.started()) == 1 })
	reqs := hub.requests()
	if reqs[len(reqs)-1].Nonce == firstNonce {
		t.Fatal("re-join after reset reused the closed nonce")
	}
}

func TestEnrollmentReenrollsWhenDataClearedWhileConnected(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	hub := newFakeHub(t, joined("rhc_one"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the first tunnel", func() bool { return tunnel.isRunning() })
	hub.setReplies(joined("rhc_two"))
	clearSecret(t, cs)

	eventually(t, "a second tunnel with the new token", func() bool {
		started := tunnel.started()
		return len(started) == 2 && started[1].Token == "rhc_two"
	})
	if string(secretData(t, cs)[SecretKeyToken]) != "rhc_two" {
		t.Fatal("new token not stored")
	}
	reqs := hub.requests()
	if len(reqs) != 2 || reqs[0].Nonce == reqs[1].Nonce {
		t.Fatalf("expected two enroll calls with different nonces, got %+v", reqs)
	}
}

func TestEnrollmentDoesNotReenrollOnTunnelRejection(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	hub := newFakeHub(t, joined("rhc_one"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the tunnel", func() bool { return tunnel.isRunning() })
	tunnel.exit <- ErrTokenRejected

	eventually(t, "the refusal", func() bool {
		return string(secretData(t, cs)[SecretKeyStatus]) == EnrollStatusRefused
	})
	data := secretData(t, cs)
	if got := string(data[SecretKeyError]); got != "token rejected; run radar cloud reset-enrollment to join again" {
		t.Fatalf("enroll_error = %q", got)
	}
	if string(data[SecretKeyToken]) != "rhc_one" {
		t.Fatal("token was dropped on rejection")
	}
	time.Sleep(50 * time.Millisecond)
	if n := len(hub.requests()); n != 1 {
		t.Fatalf("enroll calls after a tunnel 401 = %d, want 1", n)
	}
	if n := len(tunnel.started()); n != 1 {
		t.Fatalf("tunnel restarted %d times after the token was rejected", n-1)
	}
}

func TestEnrollmentReplacesAnotherHubsCredentials(t *testing.T) {
	// The fleet's values now point at a new Hub. The old Hub's token and join
	// attempt must never be sent; Radar drops them and joins the new Hub.
	cs := newTokenSecretClient(t, map[string][]byte{
		SecretKeyToken:       []byte("rhc_other"),
		SecretKeyHubOrigin:   []byte("https://other-hub.example.com"),
		SecretKeyWSSURL:      []byte("wss://other-hub.example.com/agent"),
		SecretKeyClusterID:   []byte("c-other"),
		SecretKeyNonce:       []byte("other-hub-nonce-other-hub-nonce-other-hub-n"),
		SecretKeyNonceOrigin: []byte("https://other-hub.example.com"),
		SecretKeyStatus:      []byte(EnrollStatusRefused),
		SecretKeyError:       []byte("old hub refused"),
	})
	hub := newFakeHub(t, joined("rhc_new"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "a tunnel to the new Hub", func() bool { return len(tunnel.started()) == 1 })
	for _, cfg := range tunnel.started() {
		if cfg.Token == "rhc_other" || strings.Contains(cfg.URL, "other-hub") {
			t.Fatalf("the old Hub's credentials were used: %+v", cfg)
		}
	}
	reqs := hub.requests()
	if len(reqs) != 1 || reqs[0].Nonce == "other-hub-nonce-other-hub-nonce-other-hub-n" {
		t.Fatalf("enroll requests = %+v, want one with a fresh nonce", reqs)
	}
	data := secretData(t, cs)
	if string(data[SecretKeyToken]) != "rhc_new" || string(data[SecretKeyHubOrigin]) != hub.srv.URL || string(data[SecretKeyStatus]) != EnrollStatusJoined {
		t.Fatalf("Secret = %v", stringMap(data))
	}
	for _, a := range cs.Actions() {
		if a.GetResource().Resource == "secrets" && (a.GetVerb() == "create" || a.GetVerb() == "delete") {
			t.Fatalf("Radar sent %s on the token Secret", a.GetVerb())
		}
	}
}

func TestTokenSecretStoreClearForeignKeepsOwnFields(t *testing.T) {
	cs := newTokenSecretClient(t, map[string][]byte{
		SecretKeyToken:       []byte("rhc_mine"),
		SecretKeyHubOrigin:   []byte("https://b.example.com"),
		SecretKeyNonce:       []byte("n-a"),
		SecretKeyNonceOrigin: []byte("https://a.example.com"),
	})
	store := &TokenSecretStore{Client: cs, Namespace: testNamespace, Name: testSecretName, HubOrigin: "https://b.example.com"}
	st, _ := store.Load(context.Background())
	stale := st
	got, err := store.ClearForeign(context.Background(), st)
	if err != nil {
		t.Fatal(err)
	}
	if got.Token != "rhc_mine" || got.Nonce != "" {
		t.Fatalf("after clear: %+v", got)
	}
	if _, err := store.ClearForeign(context.Background(), stale); !errors.Is(err, ErrTokenSecretConflict) {
		t.Fatalf("stale clear err = %v, want conflict", err)
	}
}

func TestEnrollmentNeverCreatesOrDeletesTheSecret(t *testing.T) {
	fastEnrollDelays(t)
	cs := fake.NewClientset(&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "kube-system", UID: types.UID(testClusterUID)}})
	hub := newFakeHub(t, joined("rhc_x"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})
	time.Sleep(50 * time.Millisecond)
	for _, a := range cs.Actions() {
		if a.GetResource().Resource == "secrets" && (a.GetVerb() == "create" || a.GetVerb() == "delete") {
			t.Fatalf("Radar sent %s on the token Secret", a.GetVerb())
		}
	}
	if len(hub.requests()) != 0 {
		t.Fatal("enrolled without a place to keep the nonce")
	}
}

func TestEnrollmentRereadsTheKeyFile(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	hub := newFakeHub(t, hubReply{status: 401, body: `{"error":"invalid_key"}`})
	keyFile := writeKeyFile(t, testKey)
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, keyFile, tunnel, NameOptions{})

	eventually(t, "a rejected key", func() bool { return len(hub.requests()) >= 1 })
	if !strings.Contains(string(secretData(t, cs)[SecretKeyError]), "enrollment key") {
		t.Fatalf("enroll_error = %q", secretData(t, cs)[SecretKeyError])
	}
	rotated := "rhe_rotatedrotatedrotatedrotatedrotated12345"
	hub.setReplies(joined("rhc_k"))
	if err := os.WriteFile(keyFile, []byte(rotated), 0o600); err != nil {
		t.Fatal(err)
	}
	eventually(t, "a join with the new key", func() bool { return len(tunnel.started()) == 1 })
	hub.mu.Lock()
	last := hub.keys[len(hub.keys)-1]
	hub.mu.Unlock()
	if last != rotated {
		t.Fatalf("last key sent = %q, want the rotated key", last)
	}
}

func TestTokenSecretStoreConditionalWrites(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	store := &TokenSecretStore{Client: cs, Namespace: testNamespace, Name: testSecretName, HubOrigin: "https://hub.example.com"}
	ctx := context.Background()
	st, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.SaveNonce(ctx, st, "n1"); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// st is now stale: a write based on it must not land.
	if _, err := store.SaveStatus(ctx, st, EnrollStatusWaiting, "x"); !errors.Is(err, ErrTokenSecretConflict) {
		t.Fatalf("stale write err = %v, want conflict", err)
	}
	if _, err := store.SaveNonce(ctx, st, "n2"); !errors.Is(err, ErrTokenSecretConflict) {
		t.Fatalf("stale nonce write err = %v, want conflict", err)
	}
	fresh, err := store.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if fresh.Nonce != "n1" || fresh.NonceOrigin != "https://hub.example.com" || fresh.Status != EnrollStatusJoining {
		t.Fatalf("stored state = %+v", fresh)
	}
	joinedState, err := store.SaveJoined(ctx, fresh, &EnrollResult{ClusterID: "c1", Token: "rhc_1", WSSURL: "wss://hub.example.com/agent"})
	if err != nil {
		t.Fatal(err)
	}
	if joinedState.HubOrigin != "https://hub.example.com" || joinedState.Nonce != "" {
		t.Fatalf("joined state = %+v", joinedState)
	}
	// An unchanged status is not rewritten.
	again, err := store.SaveStatus(ctx, joinedState, EnrollStatusJoined, "")
	if err != nil || again.secret.ResourceVersion != joinedState.secret.ResourceVersion {
		t.Fatalf("no-op status write changed the Secret (err=%v)", err)
	}
}

func TestTokenSecretStoreBindsWritesToItsOrigin(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	ctx := context.Background()
	other := &TokenSecretStore{Client: cs, Namespace: testNamespace, Name: testSecretName, HubOrigin: "https://a.example.com"}
	st, _ := other.Load(ctx)
	st, err := other.SaveNonce(ctx, st, "nonce-for-a")
	if err != nil {
		t.Fatal(err)
	}
	mine := &TokenSecretStore{Client: cs, Namespace: testNamespace, Name: testSecretName, HubOrigin: "https://b.example.com"}
	if _, err := mine.SaveJoined(ctx, st, &EnrollResult{ClusterID: "c", Token: "rhc_b", WSSURL: "wss://b.example.com/agent"}); err == nil {
		t.Fatal("stored a token for b against a join attempt made with a")
	}
	noOrigin := &TokenSecretStore{Client: cs, Namespace: testNamespace, Name: testSecretName}
	if _, err := noOrigin.SaveStatus(ctx, st, EnrollStatusWaiting, "x"); err == nil {
		t.Fatal("a store with no Hub origin wrote the Secret")
	}
	if err := checkOrigins(map[string][]byte{SecretKeyToken: []byte("rhc")}); err == nil {
		t.Fatal("accepted a token with no origin")
	}
	if err := checkOrigins(map[string][]byte{SecretKeyNonce: []byte("n")}); err == nil {
		t.Fatal("accepted a nonce with no origin")
	}
}

func TestEnrollClientStatusHandling(t *testing.T) {
	tests := []struct {
		name       string
		reply      hubReply
		wantStatus int
		wantCode   string
		wantRetry  time.Duration
	}{
		{name: "closed", reply: hubReply{status: 409, body: `{"error":"enrollment_closed"}`}, wantStatus: 409, wantCode: "enrollment_closed"},
		{name: "already connected", reply: hubReply{status: 409, body: `{"error":"already_connected","cluster_id":"c1"}`}, wantStatus: 409, wantCode: "already_connected"},
		{name: "rate limited", reply: hubReply{status: 429, body: `{}`, retryAfter: "17"}, wantStatus: 429, wantRetry: 17 * time.Second},
		{name: "licence limit", reply: hubReply{status: 402, body: `{"error":"license_limit","limit":3}`}, wantStatus: 402, wantCode: "license_limit"},
		{name: "invalid key", reply: hubReply{status: 401, body: `{"error":"invalid_key"}`}, wantStatus: 401, wantCode: "invalid_key"},
		{name: "not ready", reply: hubReply{status: 503, body: `{"error":"hub_not_ready"}`}, wantStatus: 503, wantCode: "hub_not_ready"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hub := newFakeHub(t, tt.reply)
			c, err := NewEnrollClient(hub.srv.URL, writeKeyFile(t, testKey), nil)
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.Enroll(context.Background(), EnrollRequest{Nonce: "n"})
			var he *EnrollError
			if !errors.As(err, &he) {
				t.Fatalf("err = %v, want *EnrollError", err)
			}
			if he.StatusCode != tt.wantStatus || he.Code != tt.wantCode || he.RetryAfter != tt.wantRetry {
				t.Fatalf("got %+v", he)
			}
		})
	}

	t.Run("joined, and the same answer for a retried nonce", func(t *testing.T) {
		hub := newFakeHub(t, joined("rhc_same"))
		c, _ := NewEnrollClient(hub.srv.URL, writeKeyFile(t, testKey), nil)
		for i := 0; i < 2; i++ {
			res, err := c.Enroll(context.Background(), EnrollRequest{Nonce: "same"})
			if err != nil || res.Token != "rhc_same" || res.ClusterID != "c-rhc_same" {
				t.Fatalf("attempt %d: %+v %v", i, res, err)
			}
		}
	})

	t.Run("plaintext wss_url to a remote host is refused", func(t *testing.T) {
		hub := newFakeHub(t, hubReply{status: 200, body: `{"status":"joined","cluster_id":"c","token":"rhc","wss_url":"ws://hub.example.com/agent"}`})
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

func TestClassifyEnrollError(t *testing.T) {
	fastEnrollDelays(t)
	tests := []struct {
		err        error
		wantStatus string
		wantDelay  func(time.Duration) bool
	}{
		{&EnrollError{StatusCode: 409, Code: "enrollment_closed"}, EnrollStatusRefused, func(d time.Duration) bool { return d < 0 }},
		{&EnrollError{StatusCode: 409, Code: "already_connected"}, EnrollStatusWaiting, func(d time.Duration) bool { return d == 0 }},
		{&EnrollError{StatusCode: 401, Code: "invalid_key"}, EnrollStatusWaiting, func(d time.Duration) bool { return d == 0 }},
		{&EnrollError{StatusCode: 402}, EnrollStatusWaiting, func(d time.Duration) bool { return d >= enrollLongDelay }},
		{&EnrollError{StatusCode: 503}, EnrollStatusWaiting, func(d time.Duration) bool { return d >= enrollNotReadyDelay && d < enrollLongDelay }},
		{&EnrollError{StatusCode: 429, RetryAfter: 42 * time.Second, HasRetryAfter: true}, EnrollStatusWaiting, func(d time.Duration) bool { return d == 42*time.Second }},
		{errors.New("dial tcp: refused"), EnrollStatusWaiting, func(d time.Duration) bool { return d == 0 }},
	}
	for _, tt := range tests {
		status, msg, delay := classifyEnrollError(tt.err)
		if status != tt.wantStatus || !tt.wantDelay(delay) || msg == "" {
			t.Errorf("%v: status=%s delay=%s msg=%q", tt.err, status, delay, msg)
		}
	}
	if _, msg, _ := classifyEnrollError(&EnrollError{StatusCode: 409, Code: "enrollment_closed"}); !strings.Contains(msg, "radar cloud reset-enrollment") {
		t.Errorf("closed message %q lacks the reset instruction", msg)
	}
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

func TestRunStopsOnRepeatedRejectionOnlyWhenEnrolled(t *testing.T) {
	var gotUID string
	var mu sync.Mutex
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotUID = r.Header.Get(clusterUIDHeader)
		mu.Unlock()
		http.Error(w, "unauthorized", http.StatusUnauthorized)
	}))
	defer srv.Close()
	prev := tokenRejectedThreshold
	tokenRejectedThreshold = 1
	defer func() { tokenRejectedThreshold = prev }()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/agent"
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := Run(ctx, Config{URL: wsURL, Token: "rhc", ClusterID: "c", Handler: http.NotFoundHandler(), Enrolled: true, ClusterUID: testClusterUID})
	if !errors.Is(err, ErrTokenRejected) {
		t.Fatalf("Run = %v, want ErrTokenRejected", err)
	}
	mu.Lock()
	if gotUID != testClusterUID {
		t.Fatalf("%s = %q", clusterUIDHeader, gotUID)
	}
	mu.Unlock()

	// A token configured by hand keeps retrying, as before.
	short, cancelShort := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelShort()
	if err := Run(short, Config{URL: wsURL, Token: "rhc", ClusterID: "c", Handler: http.NotFoundHandler()}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("token-mode Run = %v, want it to keep retrying", err)
	}
}

func TestClusterUIDHeaderOnlyForUUIDs(t *testing.T) {
	if got := cloudHandshakeHeaders(Config{Token: "rhc", ClusterUID: testClusterUID}, false).Get(clusterUIDHeader); got != testClusterUID {
		t.Fatalf("header = %q", got)
	}
	if _, ok := cloudHandshakeHeaders(Config{Token: "rhc", ClusterUID: "x\r\ninjected"}, false)[clusterUIDHeader]; ok {
		t.Fatal("sent a non-UUID cluster UID")
	}
}

func TestCAFileTrustsAPrivateHub(t *testing.T) {
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"joined","cluster_id":"c","token":"rhc_tls","wss_url":"wss://hub.example.com/agent"}`))
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
	ca, err := NewCAFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	with, _ := NewEnrollClient(hub.URL, keyFile, ca)
	if res, err := with.Enroll(context.Background(), EnrollRequest{}); err != nil || res.Token != "rhc_tls" {
		t.Fatalf("with CA file: %+v %v", res, err)
	}
	if tlsCfg := hubTLSConfig(ca.Pool(), false); tlsCfg == nil || tlsCfg.RootCAs != ca.Pool() {
		t.Fatal("the dialer's TLS config does not carry the CA pool")
	}

	empty := filepath.Join(t.TempDir(), "empty.crt")
	_ = os.WriteFile(empty, []byte("no certs here"), 0o600)
	if _, err := LoadCAFile(empty); err == nil {
		t.Fatal("accepted a CA file with no certificates")
	}
}

func stringMap(data map[string][]byte) map[string]string {
	out := make(map[string]string, len(data))
	for k, v := range data {
		out[k] = string(v)
	}
	return out
}

// failNextGets makes the next n Secret reads fail with a server error.
func failNextGets(cs *fake.Clientset) func(n int) {
	var mu sync.Mutex
	remaining := 0
	cs.PrependReactor("get", "secrets", func(clienttesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		if remaining > 0 {
			remaining--
			return true, nil, apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
		}
		return false, nil, nil
	})
	return func(n int) {
		mu.Lock()
		remaining = n
		mu.Unlock()
	}
}

// clearSecretBehindReactors clears the Secret straight in the object tracker,
// so the only reads that follow are Radar's own.
func clearSecretBehindReactors(t *testing.T, cs *fake.Clientset) {
	t.Helper()
	obj, err := cs.Tracker().Get(secretsGVR, testNamespace, testSecretName)
	if err != nil {
		t.Fatal(err)
	}
	s := obj.(*corev1.Secret).DeepCopy()
	s.Data = nil
	if err := cs.Tracker().Update(secretsGVR, s, testNamespace); err != nil {
		t.Fatal(err)
	}
}

func TestEnrollmentSeesAResetDespiteAFailedRead(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	failGets := failNextGets(cs)
	hub := newFakeHub(t, hubReply{status: 409, body: `{"error":"enrollment_closed"}`})
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the refusal", func() bool {
		return string(secretData(t, cs)[SecretKeyStatus]) == EnrollStatusRefused
	})
	time.Sleep(50 * time.Millisecond) // let Radar settle into its open-ended wait
	hub.setReplies(joined("rhc_after_reset"))
	failGets(1)
	clearSecretBehindReactors(t, cs)
	eventually(t, "a join after the reset", func() bool { return len(tunnel.started()) == 1 })
}

func TestTunnelStopsOnAResetDespiteAFailedRead(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	failGets := failNextGets(cs)
	hub := newFakeHub(t, joined("rhc_one"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the first tunnel", func() bool { return tunnel.isRunning() })
	time.Sleep(50 * time.Millisecond)
	hub.setReplies(joined("rhc_two"))
	failGets(1)
	clearSecretBehindReactors(t, cs)
	eventually(t, "a second tunnel with the new token", func() bool {
		started := tunnel.started()
		return len(started) == 2 && started[1].Token == "rhc_two"
	})
}

// writeFileNewer writes data and moves its modification time forward, so a
// rewrite within the same clock tick still reads as a change.
func writeFileNewer(t *testing.T, path string, data []byte, step int) {
	t.Helper()
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	when := time.Now().Add(time.Duration(step) * time.Minute)
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestCAFileRotationIsPickedUpWithoutARestart(t *testing.T) {
	hub := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"status":"joined","cluster_id":"c","token":"rhc_tls","wss_url":"wss://hub.example.com/agent"}`))
	}))
	defer hub.Close()
	hubPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: hub.Certificate().Raw})

	caPath := filepath.Join(t.TempDir(), "ca.crt")
	writeFileNewer(t, caPath, unrelatedCAPEM(t), 0)
	ca, err := NewCAFile(caPath)
	if err != nil {
		t.Fatal(err)
	}
	client, _ := NewEnrollClient(hub.URL, writeKeyFile(t, testKey), ca)
	if _, err := client.Enroll(context.Background(), EnrollRequest{}); err == nil {
		t.Fatal("trusted the Hub with the wrong CA")
	}

	writeFileNewer(t, caPath, hubPEM, 1)
	if res, err := client.Enroll(context.Background(), EnrollRequest{}); err != nil || res.Token != "rhc_tls" {
		t.Fatalf("rotated CA not picked up: %+v %v", res, err)
	}
	rotated := ca.Pool()
	if tlsCfg := hubTLSConfig(ca.Pool(), false); tlsCfg.RootCAs != rotated {
		t.Fatal("the dialer would not use the rotated pool")
	}

	// A broken rewrite keeps the last good pool.
	writeFileNewer(t, caPath, []byte("half-written"), 2)
	if ca.Pool() != rotated {
		t.Fatal("a broken CA file replaced the last good pool")
	}
	if _, err := client.Enroll(context.Background(), EnrollRequest{}); err != nil {
		t.Fatalf("enroll after a broken rewrite: %v", err)
	}
	if _, err := NewCAFile(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("NewCAFile accepted a missing file")
	}
}

// failRefusedWrites makes the next n writes that store enroll_status=refused
// fail with a server error.
func failRefusedWrites(cs *fake.Clientset) func(n int) {
	var mu sync.Mutex
	remaining := 0
	cs.PrependReactor("update", "secrets", func(a clienttesting.Action) (bool, runtime.Object, error) {
		mu.Lock()
		defer mu.Unlock()
		obj := a.(clienttesting.UpdateAction).GetObject().(*corev1.Secret)
		if remaining > 0 && string(obj.Data[SecretKeyStatus]) == EnrollStatusRefused {
			remaining--
			return true, nil, apierrors.NewInternalError(errors.New("etcdserver: request timed out"))
		}
		return false, nil, nil
	})
	return func(n int) {
		mu.Lock()
		remaining = n
		mu.Unlock()
	}
}

func TestEnrollmentClosedStopsOnlyOnceRefusalIsStored(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	failWrites := failRefusedWrites(cs)
	failWrites(1)
	hub := newFakeHub(t, hubReply{status: 409, body: `{"error":"enrollment_closed"}`})
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the refusal to be stored", func() bool {
		return string(secretData(t, cs)[SecretKeyStatus]) == EnrollStatusRefused
	})
	if !strings.Contains(string(secretData(t, cs)[SecretKeyError]), "reset-enrollment") {
		t.Fatalf("enroll_error = %q", secretData(t, cs)[SecretKeyError])
	}
}

func TestTunnelRejectionIsRecordedDespiteAFailedRead(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	failGets := failNextGets(cs)
	failWrites := failRefusedWrites(cs)
	hub := newFakeHub(t, joined("rhc_one"))
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "the tunnel", func() bool { return tunnel.isRunning() })
	time.Sleep(50 * time.Millisecond)
	failGets(1)
	failWrites(1)
	tunnel.exit <- ErrTokenRejected

	// Read through the tracker: a test read must not use up the injected
	// failure meant for Radar.
	eventually(t, "the refusal", func() bool {
		obj, err := cs.Tracker().Get(secretsGVR, testNamespace, testSecretName)
		return err == nil && string(obj.(*corev1.Secret).Data[SecretKeyStatus]) == EnrollStatusRefused
	})
	time.Sleep(50 * time.Millisecond)
	if n := len(tunnel.started()); n != 1 {
		t.Fatalf("the rejected token was used for %d more tunnel(s)", n-1)
	}
}

func TestRestartFromStoredTokenSendsTheDerivedName(t *testing.T) {
	hub := newFakeHub(t, joined("unused"))
	cs := newTokenSecretClient(t, map[string][]byte{
		SecretKeyToken:     []byte("rhc_stored"),
		SecretKeyClusterID: []byte("c-stored"),
		SecretKeyWSSURL:    []byte("ws://127.0.0.1:1/agent"),
		SecretKeyStatus:    []byte(EnrollStatusJoined),
	})
	s, _ := cs.CoreV1().Secrets(testNamespace).Get(context.Background(), testSecretName, metav1.GetOptions{})
	s.Data[SecretKeyHubOrigin] = []byte(hub.srv.URL)
	if _, err := cs.CoreV1().Secrets(testNamespace).Update(context.Background(), s, metav1.UpdateOptions{}); err != nil {
		t.Fatal(err)
	}
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{Prefix: "nkp-"})

	eventually(t, "the tunnel", func() bool { return len(tunnel.started()) == 1 })
	cfg := tunnel.started()[0]
	if cfg.ClusterName != "nkp-api.prod-eu.example.com" || cfg.ClusterUID != testClusterUID {
		t.Fatalf("first handshake after restart: name %q, uid %q", cfg.ClusterName, cfg.ClusterUID)
	}
	if len(hub.requests()) != 0 {
		t.Fatal("restart with a stored token called enroll")
	}
}

// unrelatedCAPEM is a self-signed certificate no test server uses.
func unrelatedCAPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "unrelated test CA"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestEnrollDelaysForHubSideRefusals(t *testing.T) {
	// Production values. A Hub with no org yet becomes ready the moment its
	// admin signs in, so clusters installed first must show up within about
	// half a minute, not ten.
	for i := 0; i < 50; i++ {
		_, _, notReady := classifyEnrollError(&EnrollError{StatusCode: 503, Code: "hub_not_ready"})
		if notReady < 30*time.Second || notReady >= 36*time.Second {
			t.Fatalf("503 attempt %d waits %s, want 30s plus at most 20%% jitter", i, notReady)
		}
		_, _, limit := classifyEnrollError(&EnrollError{StatusCode: 402})
		if limit < 10*time.Minute || limit >= 12*time.Minute {
			t.Fatalf("402 waits %s, want 10m plus at most 20%% jitter", limit)
		}
	}
}

func TestHubNotReadyKeepsRetryingWithoutBackingOff(t *testing.T) {
	cs := newTokenSecretClient(t, nil)
	hub := newFakeHub(t, hubReply{status: 503, body: `{"error":"hub_not_ready"}`})
	tunnel := newFakeTunnel()
	startEnroller(t, cs, hub, writeKeyFile(t, testKey), tunnel, NameOptions{})

	eventually(t, "several 503 retries", func() bool { return len(hub.requests()) >= 5 })
	if got := string(secretData(t, cs)[SecretKeyError]); got != "the Hub is not ready to add clusters yet" {
		t.Fatalf("enroll_error = %q", got)
	}
	hub.setReplies(joined("rhc_ready"))
	eventually(t, "a join once the Hub is ready", func() bool { return len(tunnel.started()) == 1 })
	reqs := hub.requests()
	for _, r := range reqs[1:] {
		if r.Nonce != reqs[0].Nonce {
			t.Fatal("a 503 retry started a new join attempt")
		}
	}
}
