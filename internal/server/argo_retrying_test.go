package server

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/argocd"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
)

// Retrying is process-wide: one app whose read gets no answer sets it for
// every app. A page whose own diff is ready must not be told to poll for it,
// or every Argo page of every user polls while one large app is slow.
func TestSetArgoCapabilities_AnotherAppsFailedReadDoesNotMakeThisPagePoll(t *testing.T) {
	setupArgoCDTest(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Version":"v3.5.2"}`))
	})
	mux.HandleFunc("/api/v1/session/userinfo", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"loggedIn":true}`))
	})
	mux.HandleFunc("/api/v1/applications/slow/managed-resources", func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	})
	mux.HandleFunc("/api/v1/applications/fast/managed-resources", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[]}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	argocd.SetConfig(srv.URL, "good-token", false, true)
	if err := argocd.Probe(context.Background()); err != nil {
		t.Fatalf("probe: %v", err)
	}
	root := &unstructured.Unstructured{Object: map[string]any{
		"spec": map[string]any{"destination": map[string]any{"server": "https://kubernetes.default.svc"}},
	}}

	// The fast app's page loads first; its diff is cached from here on.
	var first gitopsinsights.Capabilities
	setArgoCapabilities(context.Background(), "argocd", "fast", root, &first)
	if !first.ArgoDiffAvailable || first.ArgoRetrying {
		t.Fatalf("fast app first load: diff %v, retrying %v; want the diff and no polling", first.ArgoDiffAvailable, first.ArgoRetrying)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	var slow gitopsinsights.Capabilities
	setArgoCapabilities(ctx, "argocd", "slow", root, &slow)
	if !slow.ArgoRetrying || slow.ArgoDiffAvailable {
		t.Fatalf("slow app: retrying %v, diff %v; want retrying without a diff", slow.ArgoRetrying, slow.ArgoDiffAvailable)
	}
	if !argocd.Retrying() {
		t.Fatal("the slow app's read must leave the connection retrying")
	}

	// The fast app's next poll is served from its cache, so nothing it does
	// clears the slow app's failure.
	var fast gitopsinsights.Capabilities
	setArgoCapabilities(context.Background(), "argocd", "fast", root, &fast)
	if !fast.ArgoDiffAvailable {
		t.Fatal("fast app: the diff must be available")
	}
	if fast.ArgoRetrying {
		t.Fatal("fast app: its diff is ready, so its page must not poll for it")
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// A failed save rolls back to the previous settings before it logs why. The
// token the probe sent, and that a server or proxy may echo back, is the new
// one, so the log must not carry it.
func TestApplyArgoCDConfig_FailedSaveDoesNotLogTheNewToken(t *testing.T) {
	s := setupArgoCDTest(t)
	mux := http.NewServeMux()
	mux.HandleFunc("/api/version", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"Version":"v3.5.2"}`))
	})
	mux.HandleFunc("/api/v1/session/userinfo", func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer good-token" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":"invalid session","message":"rejected ` + auth + `"}`))
			return
		}
		_, _ = w.Write([]byte(`{"loggedIn":true}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	if w := putArgoCD(t, s, `{"argoCdUrl": "`+srv.URL+`", "argoCdToken": "good-token"}`); w.Code != http.StatusOK {
		t.Fatalf("first save: status %d, body %s", w.Code, w.Body.String())
	}

	var logs syncBuffer
	prev := log.Writer()
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(prev) })

	const candidate = "new-token-7f3a9c"
	if w := putArgoCD(t, s, `{"argoCdUrl": "`+srv.URL+`", "argoCdToken": "`+candidate+`"}`); w.Code != http.StatusBadRequest {
		t.Fatalf("second save: status %d, want 400; body %s", w.Code, w.Body.String())
	}
	got := logs.String()
	if !strings.Contains(got, "Probe of candidate settings failed") {
		t.Fatalf("expected the failed probe to be logged; logs:\n%s", got)
	}
	if strings.Contains(got, candidate) {
		t.Fatalf("the new token reached the log:\n%s", got)
	}
}
