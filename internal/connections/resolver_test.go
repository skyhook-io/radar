package connections

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/pkg/prom"
)

func TestInlineSettingsRestartAndRedactedCatalog(t *testing.T) {
	r, a, b := setupResolver(t)
	for _, kind := range config.IntegrationKinds {
		req := Update{Kind: kind, Action: "save", URL: stringPtr("https://" + string(kind) + ".example")}
		if kind == config.IntegrationMetrics {
			req.Headers = []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "private-metrics-value"}}
		} else {
			req.Secret = &SecretEdit{Action: "set", Value: "private-" + string(kind) + "-value"}
		}
		apply(t, r, a, req)
	}
	apply(t, r, b, Update{Kind: config.IntegrationArgoCD, Action: "save", Secret: &SecretEdit{Action: "set", Value: "private-discovery-value"}})
	apply(t, r, b, Update{Kind: config.IntegrationCost, Action: "auto", Mode: stringPtr("prometheus")})

	restarted := NewResolver(r.Store, a, nil)
	for _, kind := range config.IntegrationKinds {
		selected := restarted.Resolve(a, kind, true)
		if selected.Err != nil || selected.View.URL != "https://"+string(kind)+".example" || !selected.View.SecretSet {
			t.Fatalf("restart lost %s configuration: %+v", kind, selected.View)
		}
		if got := restarted.Resolve(b, kind, true); got.View.URL != "" {
			t.Fatalf("cluster B inherited %s endpoint", kind)
		}
	}
	entries, err := restarted.Catalog()
	if err != nil || len(entries) != 5 {
		t.Fatalf("catalog: count=%d error=%v", len(entries), err)
	}
	for _, entry := range entries {
		if entry.Error != "" || entry.Revision == "" || entry.Binding == "" {
			t.Fatalf("invalid catalog entry: %+v", entry)
		}
		if entry.Binding == b.Binding && entry.Integration == config.IntegrationArgoCD && !entry.SecretSet {
			t.Fatal("discovery credential absent from cleanup catalog")
		}
	}
	data, _ := json.Marshal(entries)
	if strings.Contains(string(data), "private-") || strings.Contains(string(data), "connectionId") || strings.Contains(string(data), "\"uses\"") {
		t.Fatal("catalog exposed secrets or shared-record fields")
	}
}

func TestResolverLaunchIsolationAndTrustChange(t *testing.T) {
	r, a, b := setupResolver(t)
	apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://saved-a"), Headers: []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "saved-secret"}}})
	apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://saved-b")})
	r = NewResolver(r.Store, a, map[config.Integration]Bundle{config.IntegrationMetrics: {Settings: config.IntegrationSettings{Prometheus: &prom.Connection{URL: "https://launch"}}}})
	for i := 0; i < 2; i++ {
		got := r.Resolve(a, config.IntegrationMetrics, false)
		if got.View.State != "launch" || got.Settings.Prometheus.URL != "https://launch" || len(got.Settings.Prometheus.Headers) != 0 {
			t.Fatal("launch inherited saved credentials")
		}
		if got := r.Resolve(b, config.IntegrationMetrics, false); got.Settings.Prometheus.URL != "https://saved-b" {
			t.Fatal("launch escaped its target")
		}
	}
	a.Fingerprint = "changed"
	if got := r.Resolve(a, config.IntegrationMetrics, false); got.View.State != "target_changed" || got.Err == nil {
		t.Fatal("launch survived trust change")
	}
}

func TestResolverEnvironmentAndPrivateRevisions(t *testing.T) {
	r, a, _ := setupResolver(t)
	first := r.Resolve(a, config.IntegrationMetrics, false)
	other := NewResolver(r.Store, a, nil).Resolve(a, config.IntegrationMetrics, false)
	if first.View.Revision == first.fileRevision || first.View.Revision == other.View.Revision {
		t.Fatal("public revision is not process-private")
	}
	_, err := r.Store.Update(context.Background(), first.fileRevision, func(file *config.ClusterProfiles) error {
		file.Profiles[a.Binding] = config.ClusterProfile{Context: a.Context, Integrations: map[config.Integration]config.IntegrationSettings{config.IntegrationMetrics: {Prometheus: &prom.Connection{URL: "https://metrics", HeadersFromEnv: map[string]string{"Authorization": "RADAR_TEST_UNSET_PROFILE_SECRET"}}, Target: a.Fingerprint}}}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got := r.Resolve(a, config.IntegrationMetrics, false)
	if got.Err == nil || !got.View.HeadersManaged {
		t.Fatal("missing environment activated credentials")
	}
	t.Setenv("RADAR_TEST_UNSET_PROFILE_SECRET", "environment-secret")
	got = r.Resolve(a, config.IntegrationMetrics, false)
	got.Settings.Prometheus.Headers["Authorization"] = "changed"
	if got := r.Resolve(a, config.IntegrationMetrics, false); got.Err != nil || got.Settings.Prometheus.Headers["Authorization"] != "environment-secret" {
		t.Fatal("selection snapshot mutated store")
	}
}
