package argocd

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/argoapi"
)

func TestCloseCancelsExportedArgoClient(t *testing.T) {
	entered := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/version":
			fmt.Fprint(w, `{"Version":"v3.3.0"}`)
		case "/api/v1/session/userinfo":
			fmt.Fprint(w, `{"loggedIn":true,"username":"example"}`)
		case "/api/v1/applications/demo":
			close(entered)
			<-r.Context().Done()
		default:
			http.NotFound(w, r)
		}
	}))
	defer backend.Close()
	m := NewConnectionManager(nil, false, "")
	defer m.Close()
	m.SetConfig(backend.URL, "token", false, true)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := m.Probe(ctx); err != nil {
		t.Fatal(err)
	}
	client, ok := m.Get()
	if !ok {
		t.Fatal("probe did not publish an Argo CD client")
	}
	result := make(chan error, 1)
	go func() {
		_, _, err := client.ApplicationGeneration(ctx, argoapi.ApplicationQuery{AppName: "demo"})
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("query did not reach Argo CD")
	}
	if err := m.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("retired Argo CD query returned %v", err)
	}
	if _, ok := m.Get(); ok {
		t.Fatal("retired Argo CD manager still serves")
	}
	if err := m.Probe(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("retired Argo CD probe returned %v", err)
	}
}
