package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"

	authv1 "k8s.io/api/authorization/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/karpenter"
)

func TestCapabilitiesNodePoolReviewStaysOnRequestConnection(t *testing.T) {
	initCapacityContractDynamicState(t, true, true)
	nodePools := authv1.ResourceAttributes{Verb: "list", Group: karpenter.Group, Resource: "nodepools"}
	var reviewsA, reviewsB atomic.Int32
	clientB, _ := fakeSARClient(t, func(attrs authv1.ResourceAttributes) bool {
		if attrs == nodePools {
			reviewsB.Add(1)
			return false
		}
		return true
	})
	switchToB := sync.OnceFunc(func() { t.Cleanup(k8s.ReconnectTestClient(clientB)) })
	fakeSARServer(t, func(attrs authv1.ResourceAttributes) bool {
		// Switch before replying to the cold permission lookup: namespace
		// discovery starts with list pods, a direct check with list nodepools.
		if attrs == nodePools {
			reviewsA.Add(1)
			switchToB()
		} else if attrs == (authv1.ResourceAttributes{Verb: "list", Resource: "pods"}) {
			switchToB()
		}
		return true
	})
	t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}}))
	connA := k8s.Current()
	s := newAuthServer(auth.Config{Mode: "proxy"})
	poll := func(conn *k8s.Connection) k8s.IntegrationCapability {
		t.Helper()
		r := requestWithUser(http.MethodGet, "/api/capabilities", &auth.User{Username: "alice", Groups: []string{"viewers"}})
		w := httptest.NewRecorder()
		s.handleCapabilities(w, r.WithContext(k8s.WithConnection(r.Context(), conn)))
		var caps k8s.Capabilities
		assertOK(t, w.Result(), &caps)
		return caps.Karpenter
	}

	first := poll(connA)
	if k8s.Current().Client() != clientB {
		t.Fatal("request did not switch to B during the permission lookup")
	}
	if first.State != capacityapi.IntegrationAvailable || reviewsA.Load() != 1 || reviewsB.Load() != 0 {
		t.Fatalf("first poll = %+v, NodePool reviews A=%d B=%d, want available with A=1 B=0", first, reviewsA.Load(), reviewsB.Load())
	}
	if next := poll(connA); next != first || reviewsA.Load() != 1 || reviewsB.Load() != 0 {
		t.Fatalf("repeat poll = %+v, NodePool reviews A=%d B=%d, want cached grant from A", next, reviewsA.Load(), reviewsB.Load())
	}
	for range 2 {
		if got := poll(k8s.Current()); got.State != capacityapi.IntegrationDenied || reviewsB.Load() != 1 {
			t.Fatalf("poll B = %+v, NodePool reviews B=%d, want denied with B=1", got, reviewsB.Load())
		}
	}
}

func TestCapabilitiesReportsIntegrationsFromBoundConnection(t *testing.T) {
	initCapacityContractDynamicState(t, true, true)
	fakeSARServer(t, func(authv1.ResourceAttributes) bool { return true })
	publishIndex(t)
	t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{Pods: true}}))

	s := newAuthServer(auth.Config{Mode: "proxy"})
	user := &auth.User{Username: "alice"}
	r := requestWithUser(http.MethodGet, "/api/capabilities", user)
	r = r.WithContext(k8s.WithConnection(r.Context(), k8s.Current()))

	t.Cleanup(k8s.ReconnectTestClient(nil))
	k8s.ResetTestDynamicState()
	perms := &auth.UserPermissions{}
	perms.SetCanI("list", karpenter.Group, "nodepools", "", false)
	s.permCache.Set(user.Username, user.Groups, perms)

	w := httptest.NewRecorder()
	s.handleCapabilities(w, r)
	var caps k8s.Capabilities
	assertOK(t, w.Result(), &caps)
	if caps.Resources == nil || !caps.Resources.Pods {
		t.Errorf("resources = %+v, want the bound connection's pod permission", caps.Resources)
	}
	if caps.Karpenter.State != capacityapi.IntegrationAvailable {
		t.Errorf("karpenter = %+v, want available", caps.Karpenter)
	}
	if caps.PolicyReports == nil || caps.PolicyReports.Status != k8s.KyvernoStatusReady {
		t.Errorf("policyReports = %+v, want ready", caps.PolicyReports)
	}
}

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

	// Points at a dead port: the probe only needs a non-nil client, and the
	// namespace discovery and capability checks that use it fail fast.
	prevClient := k8s.SetTestClient(kubernetes.NewForConfigOrDie(&rest.Config{Host: "http://127.0.0.1:1"}))
	t.Cleanup(func() { k8s.SetTestClient(prevClient) })

	// Every list succeeds, so an unguarded handler would report full access.
	// The first one invalidates the cache, retiring the probe mid-flight.
	var invalidateOnce sync.Once
	t.Cleanup(k8s.SetTestPermissionProbeClient(func() {
		invalidateOnce.Do(k8s.Current().ResetPermissions)
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

// A switch while the handler's probe is in flight: the probe lands on the
// connection it started for, and the response must not report it for the one
// now installed, which has no result yet.
func TestCapabilitiesOmitsProbeResultAfterConnectionReplaced(t *testing.T) {
	if k8s.GetResourceCache() == nil {
		t.Fatal("the shared fixture has no resource cache; the handler would skip the probe")
	}
	t.Cleanup(k8s.SnapshotTestConnection())

	client := kubernetes.NewForConfigOrDie(&rest.Config{Host: "http://127.0.0.1:1"})
	prevClient := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(prevClient) })

	var replaceOnce sync.Once
	t.Cleanup(k8s.SetTestPermissionProbeClient(func() {
		replaceOnce.Do(func() { k8s.SetTestClient(client) })
	}))
	started := k8s.Current()

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
	if k8s.Current() == started {
		t.Fatal("setup: the probe did not replace the connection")
	}
	if body.Resources != nil {
		t.Fatalf("capabilities reported the replaced connection's probe result: %v", body.Resources)
	}
	if cached := k8s.GetCachedPermissionResult(); cached != nil {
		t.Fatalf("the replacement connection holds a result it never probed: %+v", cached.Perms)
	}
}
