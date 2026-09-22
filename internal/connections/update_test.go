package connections

import (
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

func setupResolver(t *testing.T) (*Resolver, k8s.ProfileTarget, k8s.ProfileTarget) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	t.Setenv("USERPROFILE", t.TempDir())
	a := k8s.ProfileTarget{Binding: "a", Context: "development", Source: "/kube/a", InFileName: "dev", Fingerprint: "target-a", ClientGeneration: 1}
	b := k8s.ProfileTarget{Binding: "b", Context: "staging", Source: "/kube/b", InFileName: "staging", Fingerprint: "target-b", ClientGeneration: 2}
	return NewResolver(&config.ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}, a, nil), a, b
}

func apply(t *testing.T, r *Resolver, target k8s.ProfileTarget, req Update) Selection {
	t.Helper()
	req.Target = target
	req.Revision = r.Resolve(target, req.Kind, false).View.Revision
	if req.Action == "reconfirm" {
		req.Revisions = map[config.Integration]string{}
		for _, kind := range req.Kinds {
			req.Revisions[kind] = r.Resolve(target, kind, false).View.Revision
		}
	}
	pending, err := r.Prepare(target, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	return r.Resolve(target, req.Kind, true)
}

func stringPtr(s string) *string { return &s }

func TestShareCustomizeAndCredentialLifecycle(t *testing.T) {
	r, a, b := setupResolver(t)
	first := apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://metrics.example"), Headers: []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "opaque-test-credential"}, {Key: "X-Scope-OrgID", Action: "set", Value: "tenant-a"}}})
	id := first.View.Connection.ID
	second := apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "use", ConnectionID: id})
	if len(second.View.Connection.Uses) != 2 || second.Connection.Prometheus.Headers["Authorization"] != "opaque-test-credential" {
		t.Fatal("reuse lost credential or assignment")
	}
	data, _ := json.Marshal(second.View)
	if strings.Contains(string(data), "opaque-test-credential") || strings.Contains(string(data), "tenant-a") {
		t.Fatal("view exposed credentials")
	}
	_, err := r.Prepare(b, Update{Target: b, Kind: config.IntegrationMetrics, Action: "save", Revision: second.View.Revision, URL: stringPtr("https://metrics.example/changed")})
	if err == nil {
		t.Fatal("implicit shared edit accepted")
	}
	customized := apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "fork", Headers: []prom.HeaderOperation{{Key: "X-Scope-OrgID", Action: "set", Value: "tenant-b"}}})
	if customized.View.Connection.ID == id || customized.Connection.Prometheus.Headers["Authorization"] != "opaque-test-credential" {
		t.Fatal("fork did not preserve auth independently")
	}
	if got := r.Resolve(a, config.IntegrationMetrics, false).Connection.Prometheus.Headers["X-Scope-Orgid"]; got != "tenant-a" {
		t.Fatalf("fork changed original tenant: %q", got)
	}
	apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "forget", Binding: b.Binding, ConfirmRemoval: true})
	file, _, _ := r.Store.Read()
	if len(file.Connections) != 1 || len(file.Profiles) != 1 {
		t.Fatal("explicit last-use removal retained credentials")
	}
	if !file.Dismissed[b.Binding][config.IntegrationMetrics] {
		t.Fatal("removal resurrects legacy settings")
	}
}

func TestSharedImpactConflictAndNextOperationRefresh(t *testing.T) {
	r, a, b := setupResolver(t)
	first := apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://metrics.example")})
	pending, err := r.Prepare(a, Update{Target: a, Kind: config.IntegrationMetrics, Action: "update_shared", Revision: first.View.Revision, Headers: []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "rotated"}}})
	if err != nil {
		t.Fatal(err)
	}
	other := NewResolver(r.Store, b, nil)
	apply(t, other, b, Update{Kind: config.IntegrationMetrics, Action: "use", ConnectionID: first.View.Connection.ID})
	if err := r.Commit(context.Background(), pending); !errors.Is(err, config.ErrProfileConflict) {
		t.Fatalf("stale impact accepted: %v", err)
	}
	updated := apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "update_shared", Headers: []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "rotated"}}})
	if got := other.Resolve(b, config.IntegrationMetrics, false); got.Connection.Prometheus.Headers["Authorization"] != "rotated" || len(updated.View.Connection.Uses) != 2 {
		t.Fatal("second process did not refresh at resolve")
	}
}

func TestTargetAcceptanceIsPerIntegrationAndDiscoveryNeedsNone(t *testing.T) {
	r, a, _ := setupResolver(t)
	apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://metrics.example")})
	apply(t, r, a, Update{Kind: config.IntegrationArgoCD, Action: "save", URL: stringPtr("https://argo.example"), Secret: &SecretEdit{Action: "set", Value: "token"}})
	a.Fingerprint = "changed"
	if r.Resolve(a, config.IntegrationMetrics, false).Err == nil || r.Resolve(a, config.IntegrationArgoCD, false).Err == nil {
		t.Fatal("changed target activated saved credential")
	}
	if r.Resolve(a, config.IntegrationCost, false).Err != nil {
		t.Fatal("anonymous discovery required target acceptance")
	}
	apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "reconfirm", Kinds: []config.Integration{config.IntegrationMetrics}})
	if r.Resolve(a, config.IntegrationMetrics, false).Err != nil || r.Resolve(a, config.IntegrationArgoCD, false).Err == nil {
		t.Fatal("confirmation affected another integration")
	}
}

func TestCostReuseKeepsDestinationMappingAndOriginFence(t *testing.T) {
	r, a, b := setupResolver(t)
	first := apply(t, r, a, Update{Kind: config.IntegrationCost, Action: "save", URL: stringPtr("https://cost.example"), Secret: &SecretEdit{Action: "set", Value: "key"}, ClusterID: stringPtr("cluster-a")})
	second := apply(t, r, b, Update{Kind: config.IntegrationCost, Action: "use", ConnectionID: first.View.Connection.ID})
	if second.Assignment.ClusterID != "" {
		t.Fatal("reuse copied source cluster mapping")
	}
	_, err := r.Prepare(b, Update{Target: b, Kind: config.IntegrationCost, Action: "fork", Revision: second.View.Revision, URL: stringPtr("https://other.example")})
	if err == nil {
		t.Fatal("fork leaked retained secret to another origin")
	}
	a.Fingerprint = "new-target"
	changed := apply(t, r, a, Update{Kind: config.IntegrationCost, Action: "use", ConnectionID: first.View.Connection.ID})
	if changed.Assignment.ClusterID != "" {
		t.Fatal("reuse silently accepted old target's cost mapping")
	}
}

func TestLegacyCostAdoptionPreservesSource(t *testing.T) {
	for _, mode := range []string{"prometheus", "kubecost"} {
		t.Run(mode, func(t *testing.T) {
			r, target, _ := setupResolver(t)
			_, err := config.Update(func(c *config.Config) {
				c.CostSource = mode
				if mode == "prometheus" {
					c.KubecostURL = "https://inactive-cost.example"
					c.KubecostAPIKey = "inactive-credential"
					c.KubecostClusterID = "inactive-cluster"
				}
			})
			if err != nil {
				t.Fatal(err)
			}
			offer := r.Resolve(target, config.IntegrationCost, true).View.Legacy
			if offer == nil {
				t.Fatal("missing legacy source offer")
			}
			got := apply(t, r, target, Update{Kind: config.IntegrationCost, Action: "adopt", LegacyRevision: offer.Revision})
			if got.Assignment.Mode != mode || got.Assignment.Kubecost != nil || got.Assignment.ConnectionID != "" || got.Assignment.ClusterID != "" {
				t.Fatalf("source import changed semantics or retained inactive credentials: %+v", got.Assignment)
			}
		})
	}
}

func TestIntegrationDraftSurvivesSiblingSave(t *testing.T) {
	r, target, _ := setupResolver(t)
	draft := r.Resolve(target, config.IntegrationMetrics, true)
	argo := apply(t, r, target, Update{Kind: config.IntegrationArgoCD, Action: "save", URL: stringPtr("https://argo.example"), Secret: &SecretEdit{Action: "set", Value: "token"}})
	pending, err := r.Prepare(target, Update{Target: target, Kind: config.IntegrationMetrics, Action: "save", Revision: draft.View.Revision, URL: stringPtr("https://metrics.example")})
	if err != nil {
		t.Fatalf("sibling save invalidated metrics draft: %v", err)
	}
	if err := r.Commit(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve(target, config.IntegrationArgoCD, false); got.View.Revision != argo.View.Revision || got.Connection.ArgoCD.Token != "token" {
		t.Fatal("metrics save changed Argo settings")
	}
}

func TestIntegrationRevisionRejectsInvisibleCredentialRotation(t *testing.T) {
	for _, kind := range config.IntegrationKinds {
		t.Run(string(kind), func(t *testing.T) {
			r, target, _ := setupResolver(t)
			req := Update{Kind: kind, Action: "save", URL: stringPtr("https://backend.example"), Secret: &SecretEdit{Action: "set", Value: "before"}}
			if kind == config.IntegrationMetrics {
				req.Secret = nil
				req.Headers = []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "before"}}
			}
			before := apply(t, r, target, req)
			originalRevision := before.View.Revision
			external := NewResolver(r.Store, target, nil)
			if kind == config.IntegrationMetrics {
				req.Headers[0].Value = "rotated"
			} else {
				req.Secret.Value = "rotated"
			}
			apply(t, external, target, req)
			after := r.Resolve(target, kind, true)
			if before.View.Revision == after.View.Revision {
				t.Fatal("credential rotation did not change the revision")
			}
			before.View.Revision = after.View.Revision
			oldJSON, _ := json.Marshal(before.View)
			newJSON, _ := json.Marshal(after.View)
			if string(oldJSON) != string(newJSON) {
				t.Fatal("test rotation changed visible fields")
			}
			req.Target, req.Revision = target, originalRevision
			_, err := r.Prepare(target, req)
			if !errors.Is(err, config.ErrProfileConflict) {
				t.Fatalf("stale secret overwrite accepted: %v", err)
			}
		})
	}
}

func TestSiblingWriteDuringProbeRemainsAtomic(t *testing.T) {
	r, target, _ := setupResolver(t)
	before := r.Resolve(target, config.IntegrationMetrics, false)
	pending, err := r.Prepare(target, Update{Target: target, Kind: config.IntegrationMetrics, Action: "save", Revision: before.View.Revision, URL: stringPtr("https://metrics.example")})
	if err != nil {
		t.Fatal(err)
	}
	apply(t, r, target, Update{Kind: config.IntegrationArgoCD, Action: "save", URL: stringPtr("https://argo.example")})
	if err := r.Commit(context.Background(), pending); !errors.Is(err, config.ErrProfileConflict) {
		t.Fatalf("whole-file candidate overwrote sibling save: %v", err)
	}
	if got := r.Resolve(target, config.IntegrationArgoCD, false); got.View.URL != "https://argo.example" {
		t.Fatal("sibling settings were lost")
	}
}

func TestReconfirmChecksEverySelectedIntegrationRevision(t *testing.T) {
	r, target, _ := setupResolver(t)
	apply(t, r, target, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://metrics.example")})
	apply(t, r, target, Update{Kind: config.IntegrationArgoCD, Action: "save", URL: stringPtr("https://argo.example"), Secret: &SecretEdit{Action: "set", Value: "before"}})
	original := target
	target.Fingerprint = "new-target"
	views := r.ResolveAll(target, true)
	req := Update{Target: target, Kind: config.IntegrationMetrics, Action: "reconfirm", Revision: views[config.IntegrationMetrics].View.Revision, Kinds: []config.Integration{config.IntegrationMetrics, config.IntegrationArgoCD}, Revisions: map[config.Integration]string{config.IntegrationMetrics: views[config.IntegrationMetrics].View.Revision, config.IntegrationArgoCD: views[config.IntegrationArgoCD].View.Revision}}
	apply(t, r, original, Update{Kind: config.IntegrationArgoCD, Action: "save", URL: stringPtr("https://argo.example"), Secret: &SecretEdit{Action: "set", Value: "rotated"}})
	if _, err := r.Prepare(target, req); !errors.Is(err, config.ErrProfileConflict) {
		t.Fatalf("accepted an unseen credential for a new target: %v", err)
	}
	req.Revisions[config.IntegrationArgoCD] = r.Resolve(target, config.IntegrationArgoCD, true).View.Revision
	pending, err := r.Prepare(target, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	for _, kind := range req.Kinds {
		if r.Resolve(target, kind, false).Err != nil {
			t.Fatalf("confirmed %s still blocked", kind)
		}
	}
}
