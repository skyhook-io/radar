package config

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/skyhook-io/radar/pkg/argoapi"
	"github.com/skyhook-io/radar/pkg/prom"
)

func connectionFixture() ClusterProfiles {
	p := ClusterProfiles{Version: 1, Profiles: map[string]ClusterProfile{}}
	for _, binding := range []string{"a", "b"} {
		p.Profiles[binding] = ClusterProfile{Context: binding, Source: "/kubeconfig", InFileName: binding, Integrations: map[Integration]IntegrationSettings{
			IntegrationMetrics: {Target: binding, Prometheus: &prom.Connection{URL: "https://metrics", Headers: map[string]string{"Authorization": "saved-secret"}}},
		}}
	}
	return p
}

func writeConnectionFixture(t *testing.T, p ClusterProfiles) (*ProfileStore, string) {
	t.Helper()
	s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(s.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	_, rev, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	return s, rev
}

func TestIntegrationSettingsRejectContradictions(t *testing.T) {
	cases := []struct {
		kind Integration
		a    IntegrationSettings
	}{
		{IntegrationMetrics, IntegrationSettings{Mode: "auto", Prometheus: &prom.Connection{URL: "https://metrics"}, Target: "a"}},
		{IntegrationMetrics, IntegrationSettings{Mode: "connection", Target: "a"}},
		{IntegrationMetrics, IntegrationSettings{Prometheus: &prom.Connection{URL: "https://metrics"}}},
		{IntegrationArgoCD, IntegrationSettings{Prometheus: &prom.Connection{URL: "https://metrics"}, Target: "a"}},
		{IntegrationArgoCD, IntegrationSettings{Mode: "auto", ArgoCD: &argoapi.Connection{URL: "https://argo"}}},
		{IntegrationCost, IntegrationSettings{Mode: "prometheus", ClusterID: "other", Target: "a"}},
	}
	for _, tc := range cases {
		if tc.a.Validate(tc.kind) == nil {
			t.Errorf("accepted invalid %s assignment: %+v", tc.kind, tc.a)
		}
	}
}

func TestConnectionStorePreservesUnrelatedInvalidRecords(t *testing.T) {
	p := connectionFixture()
	p.Profiles["a"].Integrations[IntegrationArgoCD] = IntegrationSettings{Target: "a", ArgoCD: &argoapi.Connection{URL: "not-a-url"}}
	s, rev := writeConnectionFixture(t, p)
	_, err := s.Update(context.Background(), rev, func(file *ClusterProfiles) error {
		c := file.Profiles["a"].Integrations[IntegrationMetrics]
		c.Prometheus.Headers["Authorization"] = "rotated"
		file.Profiles["a"].Integrations[IntegrationMetrics] = c
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, rev, err := s.Read()
	if err != nil || got.Profiles["a"].Integrations[IntegrationArgoCD].ArgoCD.URL != "not-a-url" {
		t.Fatal("invalid unrelated record was lost", err)
	}
	_, err = s.Update(context.Background(), rev, func(file *ClusterProfiles) error {
		c := file.Profiles["a"].Integrations[IntegrationMetrics]
		c.Prometheus.URL = "https://user:password@metrics"
		file.Profiles["a"].Integrations[IntegrationMetrics] = c
		return nil
	})
	if !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("invalid touched record: %v", err)
	}
}

func TestRemovalOnlyDeletesSelectedSettings(t *testing.T) {
	s, rev := writeConnectionFixture(t, connectionFixture())
	_, err := s.Update(context.Background(), rev, func(file *ClusterProfiles) error {
		return file.RemoveSettings("a", IntegrationMetrics)
	})
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := s.Read()
	if err != nil || len(got.Profiles) != 1 || got.Profiles["b"].Integrations[IntegrationMetrics].Prometheus.Headers["Authorization"] != "saved-secret" {
		t.Fatal("removal affected another cluster", err)
	}
}

func TestStoreWritesInlineSettings(t *testing.T) {
	s, _ := writeConnectionFixture(t, connectionFixture())
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	var stored map[string]any
	if err := json.Unmarshal(data, &stored); err != nil {
		t.Fatal(err)
	}
	if _, exists := stored["connections"]; exists {
		t.Fatal("retained shared records")
	}
	profiles := stored["profiles"].(map[string]any)
	for _, binding := range []string{"a", "b"} {
		metrics := profiles[binding].(map[string]any)["integrations"].(map[string]any)["metrics"].(map[string]any)
		if _, exists := metrics["mode"]; exists {
			t.Fatal("retained redundant metrics mode")
		}
		if _, exists := metrics["connectionId"]; exists {
			t.Fatal("retained connection reference")
		}
		if metrics["prometheus"].(map[string]any)["url"] != "https://metrics" {
			t.Fatal("missing inline connection")
		}
	}
}

func TestProfileReadSinceDetectsSameSizeSameTimestamp(t *testing.T) {
	s, rev := writeConnectionFixture(t, connectionFixture())
	info, err := os.Stat(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	p, _, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	c := p.Profiles["a"].Integrations[IntegrationMetrics]
	c.Prometheus.Headers["Authorization"] = "other-secret"
	p.Profiles["a"].Integrations[IntegrationMetrics] = c
	data, _ := json.Marshal(p)
	if err := os.WriteFile(s.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(s.Path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	got, next, changed, err := s.ReadSince(rev)
	if err != nil || !changed || next == rev || got.Profiles["a"].Integrations[IntegrationMetrics].Prometheus.Headers["Authorization"] != "other-secret" {
		t.Fatal("missed content change", err)
	}
	_, _, changed, err = s.ReadSince(next)
	if err != nil || changed {
		t.Fatal("unchanged file reparsed", err)
	}
}
