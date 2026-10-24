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
	"github.com/skyhook-io/radar/pkg/packages"
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
// release Secret read behind it must stop with it.
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

func TestPackagesHelmReadStopsWithItsRequest(t *testing.T) {
	storage := useStalledHelmStorage(t)
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	done := make(chan []SourceError, 1)
	go func() {
		_, errs := collectHelmReleases(ctx, []string{"shop"}, nil)
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

// The packages inventory is cached per namespace scope for every caller, so
// a read cut short by one caller leaving must not be stored as the scope's
// inventory. Otherwise everyone asking about that scope gets the inventory
// without Helm releases, plus a "context canceled" Helm error, until the
// entry expires.
func TestPackagesAbandonedRequestIsNotCachedForOthers(t *testing.T) {
	storage := useStalledHelmStorage(t)
	withCleanCache(t, func() {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() {
			defer close(done)
			_, _ = ListPackages(ctx, ListPackagesParams{Namespaces: []string{"shop"}})
		}()
		select {
		case <-storage.listStarted:
		case <-time.After(5 * time.Second):
			t.Fatal("packages never read Helm storage")
		}
		cancel()
		safety := time.AfterFunc(3*time.Second, func() { close(storage.release) })
		defer safety.Stop()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Fatal("abandoned packages request did not return")
		}

		// Helm storage answers promptly from here on.
		if safety.Stop() {
			close(storage.release)
		}
		resp, err := ListPackages(context.Background(), ListPackagesParams{Namespaces: []string{"shop"}})
		if err != nil {
			t.Fatalf("ListPackages: %v", err)
		}
		for _, se := range resp.SourcesErrored {
			if se.Source == packages.SourceHelm {
				t.Fatalf("a later caller got the abandoned request's Helm error: %+v", se)
			}
		}
	})
}

// A Helm read that outlasts the server's own request deadline still answers
// with the other sources and a Helm source error, instead of nothing.
func TestPackagesPastTheDeadlineAnswerWithoutHelm(t *testing.T) {
	storage := useStalledHelmStorage(t)
	safety := time.AfterFunc(3*time.Second, func() { close(storage.release) })
	defer safety.Stop()
	withCleanCache(t, func() {
		ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
		defer cancel()
		resp, err := ListPackages(ctx, ListPackagesParams{Namespaces: []string{"shop"}})
		if err != nil {
			t.Fatalf("ListPackages: %v", err)
		}
		for _, se := range resp.SourcesErrored {
			if se.Source == packages.SourceHelm {
				return
			}
		}
		t.Fatalf("no Helm source error after the deadline: %+v", resp.SourcesErrored)
	})
}
