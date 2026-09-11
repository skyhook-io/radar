package server

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

// TestCapabilitiesRendersPublishedPermissions pins that /api/capabilities
// reports what the permission cache holds, which nothing asserted before.
//
// It does NOT cover the branch taken when the cache is empty: reaching the
// superseded-probe gate there needs a probe that is retired mid-flight, which
// cannot be arranged from this package — there is no seam for installing a
// blocking dynamic client. That the probe reports itself superseded is asserted
// in internal/k8s; only the handler's use of that answer is unpinned.
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
