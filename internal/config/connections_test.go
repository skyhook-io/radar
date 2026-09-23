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
	p := ClusterProfiles{Version: 1, Profiles: map[string]ClusterProfile{}, Connections: map[string]SavedConnection{
		"shared": {Type: IntegrationMetrics, Prometheus: &prom.Connection{URL: "https://metrics", Headers: map[string]string{"Authorization": "saved-secret"}}},
	}}
	for _, binding := range []string{"a", "b"} {
		p.Profiles[binding] = ClusterProfile{Context: binding, Source: "/kubeconfig", InFileName: binding, Integrations: map[Integration]IntegrationAssignment{
			IntegrationMetrics: {Mode: "connection", ConnectionID: "shared", Target: binding},
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

func TestConnectionAssignmentsRejectContradictions(t *testing.T) {
	p := connectionFixture()
	cases := []struct {
		kind Integration
		a    IntegrationAssignment
	}{
		{IntegrationMetrics, IntegrationAssignment{Mode: "auto", ConnectionID: "shared", Target: "a"}},
		{IntegrationMetrics, IntegrationAssignment{Mode: "connection", ConnectionID: "missing", Target: "a"}},
		{IntegrationMetrics, IntegrationAssignment{Mode: "connection", ConnectionID: "shared"}},
		{IntegrationArgoCD, IntegrationAssignment{Mode: "connection", ConnectionID: "shared", Target: "a"}},
		{IntegrationArgoCD, IntegrationAssignment{Mode: "auto", ArgoCD: &argoapi.Connection{URL: "https://argo"}}},
		{IntegrationCost, IntegrationAssignment{Mode: "prometheus", ClusterID: "other", Target: "a"}},
	}
	for _, tc := range cases {
		if tc.a.Validate(tc.kind, p.Connections) == nil {
			t.Errorf("accepted invalid %s assignment: %+v", tc.kind, tc.a)
		}
	}
}

func TestConnectionStorePreservesUnrelatedInvalidRecords(t *testing.T) {
	p := connectionFixture()
	p.Connections["broken"] = SavedConnection{Type: IntegrationArgoCD, ArgoCD: &argoapi.Connection{URL: "not-a-url"}}
	p.Profiles["a"].Integrations[IntegrationArgoCD] = IntegrationAssignment{Mode: "connection", Target: "a", ConnectionID: "broken"}
	s, rev := writeConnectionFixture(t, p)
	_, err := s.Update(context.Background(), rev, func(file *ClusterProfiles) error {
		c := file.Connections["shared"]
		c.Prometheus.Headers["Authorization"] = "rotated"
		file.Connections["shared"] = c
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	got, rev, err := s.Read()
	if err != nil || got.Connections["broken"].ArgoCD.URL != "not-a-url" {
		t.Fatal("invalid unrelated record was lost", err)
	}
	_, err = s.Update(context.Background(), rev, func(file *ClusterProfiles) error {
		c := file.Connections["shared"]
		c.Prometheus.URL = "https://user:password@metrics"
		file.Connections["shared"] = c
		return nil
	})
	if !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("invalid touched record: %v", err)
	}
}

func TestLastAssignmentDeletionIsExplicitAndAtomic(t *testing.T) {
	{
		p := connectionFixture()
		if err := p.RemoveAssignment("a", IntegrationMetrics); err != nil {
			t.Fatal(err)
		}
		if _, ok := p.Connections["shared"]; !ok {
			t.Fatal("removed credentials still used by another context")
		}
		if err := p.RemoveAssignment("b", IntegrationMetrics); err != nil {
			t.Fatal(err)
		}
		if _, ok := p.Connections["shared"]; ok {
			t.Fatal("unused credentials retained")
		}
	}
	s, rev := writeConnectionFixture(t, connectionFixture())
	_, err := s.Update(context.Background(), rev, func(file *ClusterProfiles) error { delete(file.Connections, "shared"); return nil })
	if !errors.Is(err, ErrProfileInvalid) {
		t.Fatalf("deleted assigned record: %v", err)
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
	c := p.Connections["shared"]
	c.Prometheus.Headers["Authorization"] = "other-secret"
	p.Connections["shared"] = c
	data, _ := json.Marshal(p)
	if err := os.WriteFile(s.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(s.Path, info.ModTime(), info.ModTime()); err != nil {
		t.Fatal(err)
	}
	got, next, changed, err := s.ReadSince(rev)
	if err != nil || !changed || next == rev || got.Connections["shared"].Prometheus.Headers["Authorization"] != "other-secret" {
		t.Fatal("missed content change", err)
	}
	_, _, changed, err = s.ReadSince(next)
	if err != nil || changed {
		t.Fatal("unchanged file reparsed", err)
	}
}
