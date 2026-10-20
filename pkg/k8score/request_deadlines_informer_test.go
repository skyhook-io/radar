package k8score

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientfeatures "k8s.io/client-go/features"
	clientfeaturestesting "k8s.io/client-go/features/testing"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// stallingSecretsAPI is a minimal apiserver for Secrets that refuses watches
// with 403, the way list-but-not-watch RBAC or a filtering proxy does, so the
// informer re-lists on a backoff loop. One LIST can be made to stall without
// sending a byte, like a proxy whose upstream connection dropped silently.
type stallingSecretsAPI struct {
	mu        sync.Mutex
	secrets   map[string]corev1.Secret
	rv        int
	stallNext bool
	stalled   atomic.Int64
}

func (f *stallingSecretsAPI) put(ns, name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rv++
	f.secrets[ns+"/"+name] = corev1.Secret{
		TypeMeta:   metav1.TypeMeta{Kind: "Secret", APIVersion: "v1"},
		ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name, ResourceVersion: strconv.Itoa(f.rv)},
	}
}

func (f *stallingSecretsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/api/v1/secrets" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if q := r.URL.Query().Get("watch"); q == "true" || q == "1" {
		w.WriteHeader(http.StatusForbidden)
		_ = json.NewEncoder(w).Encode(metav1.Status{
			TypeMeta: metav1.TypeMeta{Kind: "Status", APIVersion: "v1"},
			Status:   metav1.StatusFailure, Reason: metav1.StatusReasonForbidden, Code: http.StatusForbidden,
			Message: "secrets is forbidden: watch is not allowed",
		})
		return
	}
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
	for k := range f.secrets {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		list.Items = append(list.Items, f.secrets[k])
	}
	f.mu.Unlock()
	_ = json.NewEncoder(w).Encode(list)
}

func waitUntil(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %s waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// A real reflector whose LIST stalls recovers once the stall timeout ends
// that LIST: the reflector backs off, lists again, and the store catches up.
// Without a deadline the same stall would block the reflector for good.
func TestRequestDeadlines_StalledInformerListRecovers(t *testing.T) {
	for _, watchList := range []bool{true, false} {
		t.Run(fmt.Sprintf("watchlist=%v", watchList), func(t *testing.T) {
			clientfeaturestesting.SetFeatureDuringTest(t, clientfeatures.WatchListClient, watchList)

			api := &stallingSecretsAPI{secrets: map[string]corev1.Secret{}}
			api.put("shop", "db-conn")
			srv := httptest.NewServer(api)
			cfg := &rest.Config{Host: srv.URL, QPS: 100, Burst: 200}
			cfg.Wrap(func(rt http.RoundTripper) http.RoundTripper {
				return &requestDeadlineRoundTripper{next: rt, idle: time.Second, headerTimeout: time.Second, ceiling: time.Minute, watchGrace: time.Second}
			})
			rc, err := NewResourceCache(CacheConfig{
				Client:         kubernetes.NewForConfigOrDie(cfg),
				ResourceScopes: map[string]ResourceScope{"secrets": {Enabled: true}},
				Logger:         log.New(io.Discard, "", 0),
			})
			if err != nil {
				t.Fatalf("NewResourceCache: %v", err)
			}
			t.Cleanup(func() {
				rc.Stop()
				srv.CloseClientConnections()
				srv.Close()
			})
			waitUntil(t, 10*time.Second, "sync", func() bool { return rc.IsKindReady("secrets") })

			api.mu.Lock()
			api.stallNext = true
			api.mu.Unlock()
			waitUntil(t, 30*time.Second, "a stalled LIST", func() bool { return api.stalled.Load() > 0 })
			api.put("shop", "created-during-stall")
			waitUntil(t, 30*time.Second, "the new Secret in the cache", func() bool {
				_, err := rc.Secrets().Secrets("shop").Get("created-during-stall")
				return err == nil
			})
		})
	}
}
