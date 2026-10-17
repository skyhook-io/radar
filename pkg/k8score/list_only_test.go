package k8score

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// fakeSecretsAPI is a minimal apiserver for Secrets. Watches are refused with
// 403 in the namespaces listed in denyWatch ("" = cluster-wide requests), and
// held open (no events) elsewhere. One LIST can be made to stall without
// sending a byte, the way a proxy behaves when its upstream silently dropped.
type fakeSecretsAPI struct {
	mu        sync.Mutex
	secrets   map[string]corev1.Secret
	rv        int
	denyWatch map[string]bool
	stallNext bool
	stalled   atomic.Int64
	lists     atomic.Int64
}

func newFakeSecretsAPI(denyWatch ...string) *fakeSecretsAPI {
	f := &fakeSecretsAPI{secrets: map[string]corev1.Secret{}, denyWatch: map[string]bool{}}
	for _, ns := range denyWatch {
		f.denyWatch[ns] = true
	}
	return f
}

func (f *fakeSecretsAPI) put(ns, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rv++
	f.secrets[ns+"/"+name] = corev1.Secret{
		TypeMeta:   metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, ResourceVersion: strconv.Itoa(f.rv)},
	}
}

func (f *fakeSecretsAPI) stallNextList() {
	f.mu.Lock()
	f.stallNext = true
	f.mu.Unlock()
}

func (f *fakeSecretsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	ns := ""
	switch {
	case r.URL.Path == "/api/v1/secrets":
	case strings.HasPrefix(r.URL.Path, "/api/v1/namespaces/") && strings.HasSuffix(r.URL.Path, "/secrets"):
		ns = strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/v1/namespaces/"), "/secrets")
	default:
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if q := r.URL.Query().Get("watch"); q == "true" || q == "1" {
		if f.denyWatch[ns] {
			w.WriteHeader(http.StatusForbidden)
			_ = json.NewEncoder(w).Encode(metav1.Status{
				TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
				Status:   metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden,
				Message: "secrets is forbidden: watch is not allowed",
			})
			return
		}
		if r.URL.Query().Get("sendInitialEvents") == "true" {
			// Like an apiserver without WatchList: the client falls back
			// to LIST + watch.
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(metav1.Status{
				TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
				Status:   metav1.StatusFailure, Reason: metav1.StatusReasonBadRequest, Code: http.StatusBadRequest,
			})
			return
		}
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		return
	}
	f.lists.Add(1)
	f.mu.Lock()
	stall := f.stallNext
	f.stallNext = false
	f.mu.Unlock()
	if stall {
		f.stalled.Add(1)
		<-r.Context().Done()
		return
	}
	f.mu.Lock()
	list := corev1.SecretList{TypeMeta: metav1.TypeMeta{Kind: "SecretList", APIVersion: "v1"}}
	list.ResourceVersion = strconv.Itoa(f.rv)
	keys := make([]string, 0, len(f.secrets))
	for k, s := range f.secrets {
		if ns == "" || s.Namespace == ns {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		list.Items = append(list.Items, f.secrets[k])
	}
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(list)
}

type listOnlyTestCache struct {
	rc  *ResourceCache
	api *fakeSecretsAPI
}

func startListOnlyTestCache(t *testing.T, api *fakeSecretsAPI, cfg CacheConfig, deadline time.Duration) listOnlyTestCache {
	t.Helper()
	srv := httptest.NewServer(api)
	restCfg := &rest.Config{Host: srv.URL, QPS: 100, Burst: 200}
	if deadline > 0 {
		restCfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
			return &requestDeadlineRoundTripper{next: rt, deadline: deadline, watchGrace: time.Second}
		})
	}
	cfg.Client = kubernetes.NewForConfigOrDie(restCfg)
	cfg.Logger = log.New(io.Discard, "", 0)
	rc, err := NewResourceCache(cfg)
	if err != nil {
		t.Fatalf("NewResourceCache: %v", err)
	}
	t.Cleanup(func() {
		rc.Stop()
		// A stalled handler only returns once the client gives up; don't
		// make Close wait on it.
		srv.CloseClientConnections()
		srv.Close()
	})
	return listOnlyTestCache{rc: rc, api: api}
}

func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func secretsClusterWide() map[string]ResourceScope {
	return map[string]ResourceScope{"secrets": {Enabled: true}}
}

func forEachWatchListMode(t *testing.T, fn func(t *testing.T)) {
	for _, watchList := range []bool{true, false} {
		t.Run(fmt.Sprintf("watchlist=%v", watchList), func(t *testing.T) {
			clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, watchList)
			fn(t)
		})
	}
}

// A list-only Secret informer whose LIST stalls recovers once the request
// deadline ends that LIST, and its absence answers are never authoritative.
func TestListOnlyInformer_StalledListRecoversWithDeadline(t *testing.T) {
	forEachWatchListMode(t, func(t *testing.T) {
		api := newFakeSecretsAPI("")
		api.put("shop", "db-conn")
		c := startListOnlyTestCache(t, api, CacheConfig{ResourceScopes: secretsClusterWide()}, time.Second)
		rc := c.rc

		waitFor(t, 10*time.Second, "list-only detection", func() bool { return rc.KindListOnly("secrets", "shop") })
		if rc.KindAbsenceVerifiable("secrets", "shop") {
			t.Fatal("absence on a list-only kind must not be verifiable")
		}
		if !rc.KindCoversNamespace("secrets", "shop") {
			t.Fatal("KindCoversNamespace is about scope and must stay true")
		}

		api.stallNextList()
		waitFor(t, 30*time.Second, "a stalled LIST", func() bool { return api.stalled.Load() > 0 })
		api.put("shop", "recreated")
		waitFor(t, 30*time.Second, "the recreated Secret in the cache", func() bool {
			_, err := rc.Secrets().Secrets("shop").Get("recreated")
			return err == nil
		})
	})
}

// Without a request deadline the stalled LIST never returns; the kind is then
// reported stale instead of silently serving a frozen snapshot.
func TestListOnlyInformer_StallWithoutDeadlineIsStale(t *testing.T) {
	forEachWatchListMode(t, func(t *testing.T) {
		api := newFakeSecretsAPI("")
		api.put("shop", "db-conn")
		c := startListOnlyTestCache(t, api, CacheConfig{
			ResourceScopes:     secretsClusterWide(),
			ListOnlyStaleAfter: 1500 * time.Millisecond,
		}, 0)
		rc := c.rc

		waitFor(t, 10*time.Second, "list-only detection", func() bool { return rc.KindListOnly("secrets", "") })
		if rc.KindStale("secrets", "") {
			t.Fatal("a kind that just completed a list must not be stale")
		}
		api.stallNextList()
		waitFor(t, 30*time.Second, "a stalled LIST", func() bool { return api.stalled.Load() > 0 })
		api.put("shop", "recreated")
		waitFor(t, 10*time.Second, "stale", func() bool { return rc.KindStale("secrets", "shop") })

		if _, err := rc.Secrets().Secrets("shop").Get("recreated"); err == nil {
			t.Fatal("expected the frozen store to miss the new Secret (repro precondition)")
		}
		if rc.KindAbsenceVerifiable("secrets", "shop") {
			t.Fatal("a stale kind's miss must not be verifiable")
		}
		status := statusFor(rc, "secrets")
		if !status.ListOnly || !status.Stale || status.LastListAt == "" {
			t.Fatalf("diagnostics should report list-only + stale, got %+v", status)
		}
	})
}

// A kind whose watch works is never list-only, so its misses stay authoritative.
func TestListOnlyInformer_WatchedKindIsNotListOnly(t *testing.T) {
	forEachWatchListMode(t, func(t *testing.T) {
		api := newFakeSecretsAPI()
		api.put("shop", "db-conn")
		c := startListOnlyTestCache(t, api, CacheConfig{
			ResourceScopes:     secretsClusterWide(),
			ListOnlyStaleAfter: 200 * time.Millisecond,
		}, time.Second)
		rc := c.rc
		waitFor(t, 10*time.Second, "sync", func() bool { return rc.IsKindReady("secrets") })
		time.Sleep(500 * time.Millisecond)
		if rc.KindListOnly("secrets", "") || rc.KindStale("secrets", "") {
			t.Fatal("a watched kind must not be list-only or stale")
		}
		if !rc.KindAbsenceVerifiable("secrets", "shop") || !rc.KindAbsenceVerifiable("secrets", "") {
			t.Fatal("a watched cluster-wide kind's misses are authoritative")
		}
	})
}

// With one informer per namespace, list-only state is tracked per namespace.
func TestListOnlyInformer_PerNamespace(t *testing.T) {
	api := newFakeSecretsAPI("a")
	c := startListOnlyTestCache(t, api, CacheConfig{
		ResourceScopes:          map[string]ResourceScope{"secrets": {Enabled: true, Namespace: "a"}},
		ResourceScopeNamespaces: map[string][]string{"secrets": {"a", "b"}},
	}, time.Second)
	rc := c.rc
	waitFor(t, 10*time.Second, "list-only detection in a", func() bool { return rc.KindListOnly("secrets", "a") })
	if rc.KindListOnly("secrets", "b") {
		t.Fatal("namespace b watches fine and must not be list-only")
	}
	if rc.KindAbsenceVerifiable("secrets", "a") || !rc.KindAbsenceVerifiable("secrets", "b") {
		t.Fatal("absence is verifiable in b only")
	}
	if !rc.KindListOnly("secrets", "") || rc.KindAbsenceVerifiable("secrets", "") {
		t.Fatal("cluster-wide questions see the list-only namespace and the namespaced scope")
	}
	if rc.KindAbsenceVerifiable("secrets", "c") {
		t.Fatal("an unwatched namespace is never verifiable")
	}
}

func statusFor(rc *ResourceCache, key string) InformerSyncStatus {
	for _, s := range rc.GetSyncStatus().Informers {
		if s.Key == key {
			return s
		}
	}
	return InformerSyncStatus{}
}

func TestObserveListOnlyCycle_Classification(t *testing.T) {
	rc := &ResourceCache{stdlog: log.New(io.Discard, "", 0)}
	gr := schema.GroupResource{Resource: "secrets"}
	now := time.Now()
	cases := []struct {
		name       string
		err        error
		wantDenied bool
	}{
		{"watch forbidden", apierrors.NewForbidden(gr, "", errors.New("no watch")), true},
		{"list forbidden", fmt.Errorf("failed to list *v1.Secret: %w", apierrors.NewForbidden(gr, "", errors.New("no list"))), false},
		{"list timeout", fmt.Errorf("failed to list *v1.Secret: %w", errors.New("context deadline exceeded")), false},
		{"watch unauthorized", apierrors.NewUnauthorized("expired"), false},
		{"watch ended", errors.New("very short watch"), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tr := &listOnlyTracker{kind: "Secret"}
			rc.observeListOnlyCycle(tr, tc.err, now)
			if tr.watchDenied != tc.wantDenied {
				t.Fatalf("watchDenied=%v, want %v", tr.watchDenied, tc.wantDenied)
			}
			if tc.wantDenied != !tr.lastListOK.IsZero() {
				t.Fatalf("lastListOK advanced=%v, want %v", !tr.lastListOK.IsZero(), tc.wantDenied)
			}
		})
	}
}
