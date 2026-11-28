package prometheus

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
)

const memberQueryResult = `{"status":"success","data":{"resultType":"vector","result":[{"metric":{"job":"test"},"value":[1700000000,"1"]}]}}`

func TestClosingMetricsMemberCancelsExportedClient(t *testing.T) {
	started := make(chan struct{})
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.FormValue("query") == "blocked" {
			close(started)
			<-r.Context().Done()
			return
		}
		_, _ = w.Write([]byte(memberQueryResult))
	}))
	defer backend.Close()
	old := NewClient(nil, nil, "a", nil, false, nil)
	defer old.Close()
	old.baseURL = backend.URL
	newMember := NewClient(nil, nil, "a", nil, false, nil)
	defer newMember.Close()
	newMember.baseURL = backend.URL
	exported := old.Prom()

	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	query := make(chan error, 1)
	go func() {
		_, err := exported.Query(ctx, "blocked")
		query <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("old query never reached the backend")
	}
	_ = old.Close()
	if err := <-query; !errors.Is(err, context.Canceled) {
		t.Fatalf("old query returned %v after retirement", err)
	}
	if _, err := newMember.Prom().Query(ctx, "up"); err != nil {
		t.Fatalf("replacement query failed: %v", err)
	}
}

func TestRetiredDiscoveryCannotPublishAfterReplacementConnects(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	replied := make(chan struct{})
	unblock := sync.OnceFunc(func() { close(release) })
	stalled := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(memberQueryResult))
		close(replied)
	}))
	defer stalled.Close()
	defer unblock()
	healthy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(memberQueryResult))
	}))
	defer healthy.Close()

	a := NewClient(nil, nil, "a", nil, false, nil)
	defer a.Close()
	a.manualURL = stalled.URL
	b := NewClient(nil, nil, "b", nil, false, nil)
	defer b.Close()
	b.manualURL = healthy.URL
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, _, err := a.EnsureConnected(ctx)
		done <- err
	}()
	select {
	case <-started:
	case <-ctx.Done():
		t.Fatal("discovery never reached A")
	}
	_ = a.Close()
	if addr, _, err := b.EnsureConnected(ctx); err != nil || addr != healthy.URL {
		t.Fatalf("replacement discovery = %q, %v", addr, err)
	}
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("retired discovery returned %v", err)
	}
	unblock()
	select {
	case <-replied:
	case <-ctx.Done():
		t.Fatal("A did not finish its late response")
	}
	if status := b.GetStatus(); status.Address != healthy.URL || status.ContextName != "b" {
		t.Fatalf("replacement status = %+v", status)
	}
	if status := a.GetStatus(); status.Connected {
		t.Fatalf("late discovery reconnected the retired member: %+v", status)
	}
}
