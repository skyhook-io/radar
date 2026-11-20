package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func TestCapabilitiesSkipRBACChecksUntilConnected(t *testing.T) {
	var reviews atomic.Int32
	cluster := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reviews.Add(1)
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["status"] = map[string]any{"allowed": true}
		_ = json.NewEncoder(w).Encode(body)
	}))
	t.Cleanup(cluster.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: cluster.URL})
	if err != nil {
		t.Fatal(err)
	}
	previousClient := k8s.SetTestClient(client)
	previousConnection := k8s.GetConnectionStatus()
	t.Cleanup(func() {
		k8s.SetTestClient(previousClient)
		k8s.SetConnectionStatus(previousConnection)
		k8s.InvalidateCapabilitiesCache()
	})
	k8s.InvalidateCapabilitiesCache()
	s := &Server{}
	get := func() {
		s.handleCapabilities(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/capabilities?namespace=default", nil))
	}

	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnecting, Context: "ctx"})
	get()
	if n := reviews.Load(); n != 0 {
		t.Fatalf("permission checks while connecting = %d, want 0", n)
	}

	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected, Context: "ctx"})
	get()
	if reviews.Load() == 0 {
		t.Fatal("no permission checks once connected")
	}
}
