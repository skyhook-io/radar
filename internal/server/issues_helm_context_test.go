package server

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/helm"
	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

// stalledHelmStorage is an API server whose Helm release Secret list never
// answers on its own. It records whether the list request was abandoned by
// its client, which is the only way to tell "Radar stopped waiting" from
// "Radar stopped waiting but left the read running".
type stalledHelmStorage struct {
	server       *httptest.Server
	listStarted  chan struct{}
	listCanceled chan struct{}
	release      chan struct{}
	started      atomic.Bool
}

func newStalledHelmStorage(t *testing.T) *stalledHelmStorage {
	t.Helper()
	s := &stalledHelmStorage{
		listStarted:  make(chan struct{}),
		listCanceled: make(chan struct{}),
		release:      make(chan struct{}),
	}
	s.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/version":
			_, _ = io.WriteString(w, `{"major":"1","minor":"30","gitVersion":"v1.30.0"}`)
		case r.URL.Path == "/api":
			_, _ = io.WriteString(w, `{"kind":"APIVersions","versions":["v1"]}`)
		case r.URL.Path == "/apis":
			_, _ = io.WriteString(w, `{"kind":"APIGroupList","apiVersion":"v1","groups":[]}`)
		case strings.HasSuffix(r.URL.Path, "/secrets") && strings.Contains(r.URL.RawQuery, "owner"):
			if s.started.CompareAndSwap(false, true) {
				close(s.listStarted)
			}
			select {
			case <-r.Context().Done():
				close(s.listCanceled)
			case <-s.release:
				_, _ = io.WriteString(w, `{"apiVersion":"v1","kind":"SecretList","items":[]}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(func() {
		select {
		case <-s.release:
		default:
			close(s.release)
		}
		s.server.Close()
	})
	return s
}

func useStalledHelmStorage(t *testing.T) *stalledHelmStorage {
	t.Helper()
	storage := newStalledHelmStorage(t)
	config := &rest.Config{Host: storage.server.URL}
	client, err := kubernetes.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	previousClient := k8s.SetTestClient(client)
	helm.ResetClient()
	if err := helm.InitializeWithRESTConfig(config); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		helm.ResetClient()
		k8s.SetTestClient(previousClient)
	})
	return storage
}

// A caller that stops waiting (a closed browser tab, or a proxy whose own
// timeout fired) must get its issues request back promptly, and the Helm
// release Secret read behind it must stop with it. Before Helm reads were
// tied to the request, the handler held the response until the API server
// answered and the read kept running after the caller had gone.
func TestIssuesStopReadingHelmStorageWhenCallerGivesUp(t *testing.T) {
	storage := useStalledHelmStorage(t)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	request := httptest.NewRequest(http.MethodGet, "/api/issues?namespace=shop", nil).WithContext(ctx)
	recorder := httptest.NewRecorder()

	done := make(chan struct{})
	started := time.Now()
	go func() {
		testServerSrv.handleIssues(recorder, request)
		close(done)
	}()

	select {
	case <-storage.listStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("issues request never read Helm release storage")
	}

	// Unblock a handler that ignores its caller, so a failing run reports
	// instead of hanging the package.
	safety := time.AfterFunc(3*time.Second, func() { close(storage.release) })
	defer safety.Stop()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("issues handler did not return")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Errorf("issues answered %s after the request started; the caller stopped waiting at 200ms", elapsed.Round(time.Millisecond))
	}
	// The API server notices the dropped connection a moment after the
	// handler returns, so wait for it rather than sampling once.
	select {
	case <-storage.listCanceled:
	case <-time.After(2 * time.Second):
		t.Error("Helm release Secret list kept running after the caller stopped waiting")
	}
}

// The packages inventory reads Helm releases on the request's context too.
func TestPackagesHelmReadStopsWithItsRequest(t *testing.T) {
	storage := useStalledHelmStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan []SourceError, 1)
	go func() {
		_, errs := collectHelmReleases(ctx, []string{"shop"}, "", nil)
		done <- errs
	}()
	select {
	case <-storage.listStarted:
	case <-time.After(5 * time.Second):
		t.Fatal("packages never read Helm storage")
	}
	safety := time.AfterFunc(3*time.Second, func() { close(storage.release) })
	defer safety.Stop()
	select {
	case errs := <-done:
		if len(errs) == 0 {
			t.Error("a canceled Helm read reported no source error")
		}
	case <-time.After(2 * time.Second):
		t.Fatal("packages Helm read did not return after its request ended")
	}
	select {
	case <-storage.listCanceled:
	case <-time.After(2 * time.Second):
		t.Error("Helm release Secret list kept running after the request ended")
	}
}
