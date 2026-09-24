package connections

import (
	"context"
	"encoding/json"
	"errors"
	"os"
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
	if req.Action == "copy" || req.Action == "forget" {
		file, _, _ := r.Store.Read()
		req.SourceRevision = r.integrationRevision(file, req.Kind, req.Binding)
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

func TestDiscoveryStoresNoEmptyConnectionOrTarget(t *testing.T) {
	r, target, _ := setupResolver(t)
	for _, kind := range config.IntegrationKinds {
		apply(t, r, target, Update{Kind: kind, Action: "save", URL: stringPtr("")})
	}
	file, _, err := r.Store.Read()
	if err != nil {
		t.Fatal(err)
	}
	for kind, settings := range file.Profiles[target.Binding].Integrations {
		if settings.Prometheus != nil || settings.ArgoCD != nil || settings.Kubecost != nil || settings.Target != "" || settings.Identity != nil || settings.Mode != "" {
			t.Fatalf("discovery retained empty configuration for %s: %+v", kind, settings)
		}
		if selected := r.Resolve(target, kind, false); selected.Err != nil || selected.View.Mode != "auto" {
			t.Fatalf("discovery not restored for %s: %+v", kind, selected.View)
		}
	}
}

func TestCopyEditAndCredentialLifecycle(t *testing.T) {
	r, a, b := setupResolver(t)
	apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://metrics.example"), Headers: []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "opaque-test-credential"}, {Key: "X-Scope-OrgID", Action: "set", Value: "tenant-a"}}})
	second := apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "copy", Binding: a.Binding})
	if second.Settings.Prometheus.Headers["Authorization"] != "opaque-test-credential" {
		t.Fatal("reuse lost credential or assignment")
	}
	data, _ := json.Marshal(second.View)
	if strings.Contains(string(data), "opaque-test-credential") || strings.Contains(string(data), "tenant-a") {
		t.Fatal("view exposed credentials")
	}
	_, err := r.Prepare(b, Update{Target: b, Kind: config.IntegrationMetrics, Action: "save", Revision: second.View.Revision, URL: stringPtr("https://metrics.example/changed")})
	if err != nil {
		t.Fatal("independent edit rejected")
	}
	customized := apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "save", Headers: []prom.HeaderOperation{{Key: "X-Scope-OrgID", Action: "set", Value: "tenant-b"}}})
	if customized.Settings.Prometheus.Headers["Authorization"] != "opaque-test-credential" {
		t.Fatal("copy did not preserve auth independently")
	}
	if got := r.Resolve(a, config.IntegrationMetrics, false).Settings.Prometheus.Headers["X-Scope-Orgid"]; got != "tenant-a" {
		t.Fatalf("copy changed original tenant: %q", got)
	}
	apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "forget", Binding: b.Binding, ConfirmRemoval: true})
	file, _, _ := r.Store.Read()
	if len(file.Profiles) != 1 {
		t.Fatal("removal retained the destination's settings")
	}
	if !file.Dismissed[b.Binding][config.IntegrationMetrics] {
		t.Fatal("removal resurrects legacy settings")
	}
}

func TestCopyIsolationAndNextOperationRefresh(t *testing.T) {
	r, a, b := setupResolver(t)
	first := apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://metrics.example")})
	pending, err := r.Prepare(a, Update{Target: a, Kind: config.IntegrationMetrics, Action: "save", Revision: first.View.Revision, Headers: []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "rotated"}}})
	if err != nil {
		t.Fatal(err)
	}
	other := NewResolver(r.Store, b, nil)
	apply(t, other, b, Update{Kind: config.IntegrationMetrics, Action: "copy", Binding: a.Binding})
	if err := r.Commit(context.Background(), pending); !errors.Is(err, config.ErrProfileConflict) {
		t.Fatalf("stale impact accepted: %v", err)
	}
	updated := apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", Headers: []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "rotated"}}})
	if got := other.Resolve(b, config.IntegrationMetrics, false); got.Settings.Prometheus.Headers["Authorization"] != "" || updated.Settings.Prometheus.Headers["Authorization"] != "rotated" {
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
	apply(t, r, a, Update{Kind: config.IntegrationCost, Action: "save", URL: stringPtr("https://cost.example"), Secret: &SecretEdit{Action: "set", Value: "key"}, ClusterID: stringPtr("cluster-a")})
	second := apply(t, r, b, Update{Kind: config.IntegrationCost, Action: "copy", Binding: a.Binding})
	if second.Settings.ClusterID != "" {
		t.Fatal("reuse copied source cluster mapping")
	}
	_, err := r.Prepare(b, Update{Target: b, Kind: config.IntegrationCost, Action: "save", Revision: second.View.Revision, URL: stringPtr("https://other.example")})
	if err == nil {
		t.Fatal("fork leaked retained secret to another origin")
	}
	a.Fingerprint = "new-target"
	changed := apply(t, r, a, Update{Kind: config.IntegrationCost, Action: "copy", Binding: b.Binding, ConfirmRemoval: true})
	if changed.Settings.ClusterID != "" {
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
			if got.Settings.Mode != mode || got.Settings.Kubecost.APIKey != "" || got.Settings.URL() != "" || got.Settings.ClusterID != "" {
				t.Fatalf("source import changed semantics or retained inactive credentials: %+v", got.Settings)
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
	if got := r.Resolve(target, config.IntegrationArgoCD, false); got.View.Revision != argo.View.Revision || got.Settings.ArgoCD.Token != "token" {
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

func TestIndependentContextDraftSurvivesSameIntegrationSave(t *testing.T) {
	r, a, b := setupResolver(t)
	draft := r.Resolve(a, config.IntegrationMetrics, false)
	apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://b.example")})
	pending, err := r.Prepare(a, Update{Target: a, Kind: config.IntegrationMetrics, Action: "save", Revision: draft.View.Revision, URL: stringPtr("https://a.example")})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if r.Resolve(b, config.IntegrationMetrics, false).View.URL != "https://b.example" {
		t.Fatal("other cluster overwritten")
	}
}

func TestCopyRejectsChangedSourceAndDoesNotExposeSecrets(t *testing.T) {
	for _, kind := range config.IntegrationKinds {
		t.Run(string(kind), func(t *testing.T) {
			r, a, b := setupResolver(t)
			first := apply(t, r, a, Update{Kind: kind, Action: "save", URL: stringPtr("https://backend.example")})
			request := Update{Target: b, Kind: kind, Action: "copy", Binding: a.Binding, SourceRevision: first.View.Revision, Revision: r.Resolve(b, kind, false).View.Revision}
			edit := Update{Kind: kind, Action: "save", Secret: &SecretEdit{Action: "set", Value: "synthetic-rotated"}}
			if kind == config.IntegrationMetrics {
				edit.Secret = nil
				edit.Headers = []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "synthetic-rotated"}}
			}
			apply(t, r, a, edit)
			if _, err := r.Prepare(b, request); !errors.Is(err, config.ErrProfileConflict) {
				t.Fatalf("stale source accepted: %v", err)
			}
			copy := apply(t, r, b, Update{Kind: kind, Action: "copy", Binding: a.Binding})

			data, _ := json.Marshal(copy.View)
			if strings.Contains(string(data), "synthetic-rotated") {
				t.Fatal("secret exposed")
			}
		})
	}
}

func TestIndependentFileEntriesStayIsolated(t *testing.T) {
	r, a, b := setupResolver(t)
	apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://backend.example")})
	_, revision, _ := r.Store.Read()
	_, err := r.Store.Update(context.Background(), revision, func(file *config.ClusterProfiles) error {
		assignment := file.Settings(a.Binding, config.IntegrationMetrics)
		assignment.Target = b.Fingerprint
		putSettings(file, b, config.IntegrationMetrics, config.ClusterProfile{}, assignment)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	updated := apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://independent.example")})
	if r.Resolve(a, config.IntegrationMetrics, false).View.URL != "https://backend.example" {
		t.Fatal("edit changed another context")
	}
	for _, action := range []string{"use", "fork", "update_shared", "rename", "delete"} {
		_, err := r.Prepare(b, Update{Target: b, Kind: config.IntegrationMetrics, Action: action, Revision: updated.View.Revision})
		if err == nil {
			t.Fatalf("removed action %s accepted", action)
		}
	}
}

func TestCopyEnvironmentReferencesRemainReferences(t *testing.T) {
	r, a, b := setupResolver(t)
	apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://backend.example")})
	_, revision, _ := r.Store.Read()
	_, err := r.Store.Update(context.Background(), revision, func(file *config.ClusterProfiles) error {
		file.Profiles[a.Binding].Integrations[config.IntegrationMetrics].Prometheus.HeadersFromEnv = map[string]string{"Authorization": "RADAR_TEST_COPY_TOKEN"}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("RADAR_TEST_COPY_TOKEN", "")
	if err := os.Unsetenv("RADAR_TEST_COPY_TOKEN"); err != nil {
		t.Fatal(err)
	}
	file, _, _ := r.Store.Read()
	request := Update{Target: b, Kind: config.IntegrationMetrics, Action: "copy", Binding: a.Binding, Revision: r.Resolve(b, config.IntegrationMetrics, false).View.Revision, SourceRevision: r.integrationRevision(file, config.IntegrationMetrics, a.Binding)}
	if _, err := r.Prepare(b, request); err == nil {
		t.Fatal("unresolved environment reference copied")
	}
	t.Setenv("RADAR_TEST_COPY_TOKEN", "synthetic-env-secret")
	apply(t, r, b, request)
	file, _, _ = r.Store.Read()
	stored := file.Profiles[b.Binding].Integrations[config.IntegrationMetrics].Prometheus
	if stored.HeadersFromEnv["Authorization"] != "RADAR_TEST_COPY_TOKEN" || len(stored.Headers) != 0 {
		t.Fatal("resolved secret persisted instead of reference")
	}
}

func TestCopyRequiresConfirmationForDiscoveryCredentials(t *testing.T) {
	r, a, b := setupResolver(t)
	source := apply(t, r, a, Update{Kind: config.IntegrationArgoCD, Action: "save", URL: stringPtr("https://argo.example")})
	destination := apply(t, r, b, Update{Kind: config.IntegrationArgoCD, Action: "save", Secret: &SecretEdit{Action: "set", Value: "discovery-secret"}})
	req := Update{Target: b, Kind: config.IntegrationArgoCD, Action: "copy", Binding: a.Binding, SourceRevision: source.View.Revision, Revision: destination.View.Revision}
	if _, err := r.Prepare(b, req); err == nil {
		t.Fatal("discovery credentials replaced without confirmation")
	}
	req.ConfirmRemoval = true
	pending, err := r.Prepare(b, req)
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Commit(context.Background(), pending); err != nil {
		t.Fatal(err)
	}
	if got := r.Resolve(b, config.IntegrationArgoCD, false); got.Settings.ArgoCD.Token != "" {
		t.Fatal("old discovery credential survived explicit replacement")
	}
}

func TestCopyDraftEditsAreAtomicAndKeepSourceIndependent(t *testing.T) {
	for _, kind := range config.IntegrationKinds {
		t.Run(string(kind), func(t *testing.T) {
			r, sourceTarget, destinationTarget := setupResolver(t)
			sourceRequest := Update{Kind: kind, Action: "save", URL: stringPtr("https://source.example")}
			if kind == config.IntegrationMetrics {
				sourceRequest.Headers = []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "source-secret"}, {Key: "X-Scope-OrgID", Action: "set", Value: "source-tenant"}}
			} else {
				sourceRequest.Secret = &SecretEdit{Action: "set", Value: "source-secret"}
			}
			if kind == config.IntegrationCost {
				sourceRequest.ClusterID = stringPtr("source-cluster")
			}
			source := apply(t, r, sourceTarget, sourceRequest)
			destinationRequest := Update{Kind: kind, Action: "save", URL: stringPtr("https://destination.example")}
			if kind == config.IntegrationCost {
				destinationRequest.ClusterID = stringPtr("destination-cluster")
			}
			destination := apply(t, r, destinationTarget, destinationRequest)
			req := Update{Target: destinationTarget, Kind: kind, Action: "copy", Binding: sourceTarget.Binding, SourceRevision: source.View.Revision, Revision: destination.View.Revision, ConfirmRemoval: true, URL: stringPtr("https://source.example/custom")}
			if kind == config.IntegrationMetrics {
				req.Headers = []prom.HeaderOperation{{Key: "X-Scope-OrgID", Action: "set", Value: "destination-tenant"}}
			} else {
				req.Secret = &SecretEdit{Action: "set", Value: "destination-secret"}
			}
			pending, err := r.Prepare(destinationTarget, req)
			if err != nil {
				t.Fatal(err)
			}
			if got := r.Resolve(destinationTarget, kind, false); got.Settings.URL() != "https://destination.example" {
				t.Fatal("preparing a copy persisted it before commit")
			}
			if err := r.Commit(context.Background(), pending); err != nil {
				t.Fatal(err)
			}
			copied := r.Resolve(destinationTarget, kind, false)
			if copied.Settings.URL() != "https://source.example/custom" {
				t.Fatal("copy did not apply draft URL independently")
			}
			original := r.Resolve(sourceTarget, kind, false)
			if original.View.Revision != source.View.Revision || original.Settings.URL() != "https://source.example" {
				t.Fatal("copy edited the source")
			}
			switch kind {
			case config.IntegrationMetrics:
				if copied.Settings.Prometheus.Headers["Authorization"] != "source-secret" || copied.Settings.Prometheus.Headers["X-Scope-Orgid"] != "destination-tenant" || original.Settings.Prometheus.Headers["X-Scope-Orgid"] != "source-tenant" {
					t.Fatal("copied header operations were not isolated")
				}
			case config.IntegrationArgoCD:
				if copied.Settings.ArgoCD.Token != "destination-secret" || original.Settings.ArgoCD.Token != "source-secret" {
					t.Fatal("copied token edit was not isolated")
				}
			case config.IntegrationCost:
				if copied.Settings.Kubecost.APIKey != "destination-secret" || original.Settings.Kubecost.APIKey != "source-secret" || copied.Settings.ClusterID != "destination-cluster" {
					t.Fatal("copied API key or destination cluster mapping is incorrect")
				}
			}
			data, _ := json.Marshal(copied.View)
			if strings.Contains(string(data), "source-secret") || strings.Contains(string(data), "destination-secret") || strings.Contains(string(data), "destination-tenant") {
				t.Fatal("copy exposed credentials")
			}
		})
	}
}

func TestCopyDraftRejectsCredentialForwardingAndRaces(t *testing.T) {
	for _, kind := range config.IntegrationKinds {
		t.Run(string(kind), func(t *testing.T) {
			r, a, b := setupResolver(t)
			create := Update{Kind: kind, Action: "save", URL: stringPtr("https://source.example")}
			if kind == config.IntegrationMetrics {
				create.Headers = []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "source-secret"}}
			} else {
				create.Secret = &SecretEdit{Action: "set", Value: "source-secret"}
			}
			source := apply(t, r, a, create)
			req := Update{Target: b, Kind: kind, Action: "copy", Binding: a.Binding, SourceRevision: source.View.Revision, Revision: r.Resolve(b, kind, false).View.Revision, URL: stringPtr("https://other.example")}
			if _, err := r.Prepare(b, req); err == nil {
				t.Fatal("forwarded a copied credential to a different origin")
			}
			if r.Resolve(b, kind, false).View.State != "auto" {
				t.Fatal("failed copy wrote destination")
			}
			if kind == config.IntegrationMetrics {
				req.Headers = []prom.HeaderOperation{{Key: "Authorization", Action: "clear"}}
			} else {
				req.Secret = &SecretEdit{Action: "clear"}
			}
			pending, err := r.Prepare(b, req)
			if err != nil {
				t.Fatal(err)
			}
			apply(t, r, a, Update{Kind: kind, Action: "save", URL: stringPtr("https://source.example/changed")})
			if _, err := r.Prepare(b, req); !errors.Is(err, config.ErrProfileConflict) {
				t.Fatalf("source edit did not invalidate draft: %v", err)
			}
			if err := r.Commit(context.Background(), pending); !errors.Is(err, config.ErrProfileConflict) {
				t.Fatalf("source edit during probe did not prevent commit: %v", err)
			}
		})
	}
}
