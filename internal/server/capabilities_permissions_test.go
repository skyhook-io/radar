package server

import (
	"encoding/json"
	"net/http"
	"sync"
	"testing"

	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// TestCapabilitiesRendersPublishedPermissions pins that /api/capabilities
// reports what the permission cache holds.
func TestCapabilitiesRendersPublishedPermissions(t *testing.T) {
	restore := k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{
		Perms:           &k8s.ResourcePermissions{Pods: true, Deployments: false},
		NamespaceScoped: true,
		Namespace:       "team-a",
		Scopes:          map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true, Namespace: "team-a"}},
		ScopeCandidates: []string{"team-a"},
	})
	defer restore()

	resp := get(t, "/api/capabilities")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Resources map[string]bool `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Resources == nil {
		t.Fatal("capabilities response omitted resources while the cache held a current result")
	}
	if !body.Resources["pods"] {
		t.Error("pods reported false, but the published result granted it")
	}
	if body.Resources["deployments"] {
		t.Error("deployments reported true, but the published result denied it")
	}
}

// TestCapabilitiesOmitsSupersededProbeResult covers the branch taken when the
// permission cache is empty: the handler probes, and the cache is invalidated
// while that probe is in flight, as a context switch or rescope would. The
// probe's result describes the cluster or scope it was started against, so the
// handler must leave resources unset rather than render it.
func TestCapabilitiesOmitsSupersededProbeResult(t *testing.T) {
	if k8s.GetResourceCache() == nil {
		t.Fatal("the shared fixture has no resource cache; the handler would skip the probe")
	}
	k8s.InvalidateResourcePermissionsCache()
	t.Cleanup(k8s.InvalidateResourcePermissionsCache)
	t.Cleanup(k8s.InvalidateCapabilitiesCache)

	// Points at a dead port: the probe only needs a non-nil client, and the
	// namespace discovery and capability checks that use it fail fast.
	prevClient := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: "http://127.0.0.1:1"}))
	t.Cleanup(func() { k8s.SetTestClient(prevClient) })

	// Every list succeeds, so an unguarded handler would report full access.
	// The first one invalidates the cache, retiring the probe mid-flight.
	var invalidateOnce sync.Once
	t.Cleanup(k8s.SetTestPermissionProbeClient(func() {
		invalidateOnce.Do(k8s.InvalidateResourcePermissionsCache)
	}))

	resp := get(t, "/api/capabilities")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	var body struct {
		Resources map[string]bool `json:"resources"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Resources != nil {
		t.Fatalf("capabilities rendered a superseded probe result: %v", body.Resources)
	}
	if cached := k8s.GetCachedPermissionResult(); cached != nil {
		t.Fatalf("the superseded probe was published to the cache: %+v", cached.Perms)
	}
}
