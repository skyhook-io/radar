package opencost

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/opencost"
)

func TestCloseCancelsExportedKubecostClient(t *testing.T) {
	const allocation = `{"code":200,"data":[{"test":{"properties":{"cluster":"test"},"totalCost":1}}]}`
	entered := make(chan struct{})
	var block atomic.Bool
	oldBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if block.Load() {
			close(entered)
			<-r.Context().Done()
			return
		}
		fmt.Fprint(w, allocation)
	}))
	defer oldBackend.Close()
	var replacementCalls atomic.Int32
	newBackend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		replacementCalls.Add(1)
		fmt.Fprint(w, allocation)
	}))
	defer newBackend.Close()
	managerFor := func(url string) *Manager {
		t.Helper()
		m := NewManager(nil, false, nil)
		t.Cleanup(func() { _ = m.Close() })
		if err := m.Configure(ManagerConfig{Source: SourceKubecost, URL: url, ClusterID: "test"}); err != nil {
			t.Fatal(err)
		}
		return m
	}
	old, replacement := managerFor(oldBackend.URL), managerFor(newBackend.URL)
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	connection, err := old.Selected(ctx)
	if err != nil {
		t.Fatal(err)
	}
	block.Store(true)
	result := make(chan error, 1)
	go func() {
		_, err := connection.Client.GetAllocation(ctx, opencost.KubecostAllocationOptions{Window: "24h"})
		result <- err
	}()
	select {
	case <-entered:
	case <-ctx.Done():
		t.Fatal("query did not reach Kubecost")
	}
	if err := old.Close(); err != nil {
		t.Fatal(err)
	}
	if err := <-result; !errors.Is(err, context.Canceled) {
		t.Fatalf("retired Kubecost query returned %v", err)
	}
	if replacementCalls.Load() != 0 {
		t.Fatal("retired query reached the replacement")
	}
	if _, err := replacement.Selected(ctx); err != nil {
		t.Fatalf("replacement probe: %v", err)
	}
	if replacementCalls.Load() != 1 {
		t.Fatalf("replacement requests = %d, want 1", replacementCalls.Load())
	}
}
