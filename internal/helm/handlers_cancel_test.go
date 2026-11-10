package helm

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// A caller that leaves while Helm storage is still being read is not a Helm
// error. Recording it as one fills the diagnostics error log with failures
// that never happened, and a 5xx Helm error is also what can mark the
// cluster disconnected. A read that outlasts the server's own request
// deadline is a real failure and is still recorded.
func TestHelmListAndUpgradeCheckRecordOnlyRealFailures(t *testing.T) {
	release := make(chan struct{})
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if !strings.HasSuffix(r.URL.Path, "/secrets") {
			http.NotFound(w, r)
			return
		}
		select {
		case <-r.Context().Done():
		case <-release:
			_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","items":[]}`)
		}
	}))
	t.Cleanup(storage.Close)
	t.Cleanup(func() { close(release) })
	config := &rest.Config{Host: storage.URL}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := k8s.SetTestClient(client)
	// A 5xx Helm error that reads as a connectivity failure can mark the
	// cluster disconnected; keep that out of later tests.
	previousStatus := k8s.GetConnectionStatus()
	t.Cleanup(func() { k8s.SetConnectionStatus(previousStatus) })
	ResetClient()
	if err := InitializeWithRESTConfig(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ResetClient()
		k8s.SetTestClient(previousClient)
	})

	h := NewHandlers(func(*http.Request) ([]string, bool) { return []string{"shop"}, true })
	for name, handle := range map[string]http.HandlerFunc{
		"list":          h.handleListReleases,
		"upgrade check": h.handleBatchUpgradeCheck,
	} {
		t.Run(name+", caller leaves", func(t *testing.T) {
			errorlog.Reset()
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			time.AfterFunc(200*time.Millisecond, cancel)
			request := httptest.NewRequest(http.MethodGet, "/api/helm/releases?namespace=shop", nil).WithContext(ctx)
			handle(httptest.NewRecorder(), request)
			if entries := errorlog.GetEntries(); len(entries) > 0 {
				t.Errorf("a caller leaving was recorded as a Helm error: %+v", entries)
			}
		})
		t.Run(name+", server deadline", func(t *testing.T) {
			errorlog.Reset()
			ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
			defer cancel()
			request := httptest.NewRequest(http.MethodGet, "/api/helm/releases?namespace=shop", nil).WithContext(ctx)
			recorder := httptest.NewRecorder()
			handle(recorder, request)
			if recorder.Code < 500 || len(errorlog.GetEntries()) == 0 {
				t.Errorf("a read past the deadline answered %d and recorded %d errors; want a 5xx and a record", recorder.Code, len(errorlog.GetEntries()))
			}
		})
	}
}
