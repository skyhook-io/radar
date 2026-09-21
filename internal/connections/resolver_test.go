package connections

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/internal/config"
	"github.com/skyhook-io/radar/pkg/prom"
)

func TestResolverLaunchIsolationAndTrustChange(t *testing.T) {
	r, a, b := setupResolver(t)
	apply(t, r, a, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://saved-a"), Headers: []prom.HeaderOperation{{Key: "Authorization", Action: "set", Value: "saved-secret"}}})
	apply(t, r, b, Update{Kind: config.IntegrationMetrics, Action: "save", URL: stringPtr("https://saved-b")})
	r = NewResolver(r.Store, a, map[config.Integration]Bundle{config.IntegrationMetrics: {Connection: config.SavedConnection{Type: config.IntegrationMetrics, Prometheus: &prom.Connection{URL: "https://launch"}}, Assignment: config.IntegrationAssignment{Mode: "connection"}}})
	for i := 0; i < 2; i++ {
		got := r.Resolve(a, config.IntegrationMetrics, false)
		if got.View.State != "launch" || got.Connection.Prometheus.URL != "https://launch" || len(got.Connection.Prometheus.Headers) != 0 {
			t.Fatal("launch inherited saved credentials")
		}
		if got := r.Resolve(b, config.IntegrationMetrics, false); got.Connection.Prometheus.URL != "https://saved-b" {
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
		file.Connections["env"] = config.SavedConnection{Type: config.IntegrationMetrics, Prometheus: &prom.Connection{URL: "https://metrics", HeadersFromEnv: map[string]string{"Authorization": "RADAR_TEST_UNSET_PROFILE_SECRET"}}}
		file.Profiles[a.Binding] = config.ClusterProfile{Context: a.Context, Integrations: map[config.Integration]config.IntegrationAssignment{config.IntegrationMetrics: {Mode: "connection", ConnectionID: "env", Target: a.Fingerprint}}}
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
	got.Connection.Prometheus.Headers["Authorization"] = "changed"
	if got := r.Resolve(a, config.IntegrationMetrics, false); got.Err != nil || got.Connection.Prometheus.Headers["Authorization"] != "environment-secret" {
		t.Fatal("selection snapshot mutated store")
	}
}
