package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func jsonDocument(t *testing.T, data []byte) any {
	t.Helper()
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestClusterFileRoundTrip(t *testing.T) {
	data, err := os.ReadFile("testdata/clusters-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	want := jsonDocument(t, data)
	s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
	if err := os.WriteFile(s.Path, data, 0600); err != nil {
		t.Fatal(err)
	}
	p, revision, err := s.Read()
	if err != nil {
		t.Fatal(err)
	}
	if err := p.ValidateChanges(ClusterProfiles{}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Update(context.Background(), revision, func(*ClusterProfiles) error { return nil }); err != nil {
		t.Fatal(err)
	}
	data, err = os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if got := jsonDocument(t, data); !reflect.DeepEqual(got, want) {
		t.Fatal("v1 fields changed or disappeared during round-trip")
	}
}

func TestClusterFileRejectedStructure(t *testing.T) {
	for name, raw := range map[string]string{
		"missing version":          `{"profiles":{}}`,
		"newer version":            `{"version":2,"profiles":{}}`,
		"missing profiles":         `{"version":1}`,
		"null profiles":            `{"version":1,"profiles":null}`,
		"unknown root field":       `{"version":1,"profiles":{},"connections":{}}`,
		"empty binding":            `{"version":1,"profiles":{"":{"context":"dev","integrations":{}}}}`,
		"missing context":          `{"version":1,"profiles":{"a":{"integrations":{}}}}`,
		"missing integrations":     `{"version":1,"profiles":{"a":{"context":"dev"}}}`,
		"null integrations":        `{"version":1,"profiles":{"a":{"context":"dev","integrations":null}}}`,
		"unknown profile field":    `{"version":1,"profiles":{"a":{"context":"dev","integrations":{},"assignments":{}}}}`,
		"unknown integration":      `{"version":1,"profiles":{"a":{"context":"dev","integrations":{"typo":{}}}}}`,
		"unknown setting":          `{"version":1,"profiles":{"a":{"context":"dev","integrations":{"metrics":{"connectionId":"old"}}}}}`,
		"unknown connection field": `{"version":1,"profiles":{"a":{"context":"dev","integrations":{"metrics":{"prometheus":{"url":"","typo":true}}}}}}`,
		"unknown imported kind":    `{"version":1,"profiles":{},"imported":{"typo":true}}`,
		"unknown dismissed kind":   `{"version":1,"profiles":{},"dismissed":{"a":{"typo":true}}}`,
		"empty dismissed binding":  `{"version":1,"profiles":{},"dismissed":{"":{"metrics":true}}}`,
	} {
		t.Run(name, func(t *testing.T) {
			s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
			if err := os.WriteFile(s.Path, []byte(raw), 0600); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Read(); err == nil {
				t.Fatal("reader accepted invalid structure")
			}
			if _, err := s.Update(context.Background(), "", func(*ClusterProfiles) error {
				t.Fatal("invalid file reached mutation")
				return nil
			}); err == nil {
				t.Fatal("writer accepted invalid structure")
			}
			data, err := os.ReadFile(s.Path)
			if err != nil || string(data) != raw {
				t.Fatal("invalid file was modified", err)
			}
		})
	}
}

func TestClusterFileFirstWrite(t *testing.T) {
	s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
	p, revision, err := s.Read()
	if err != nil || p.Version != 1 || len(p.Profiles) != 0 {
		t.Fatal("missing file did not start empty", err)
	}
	if _, err := s.Update(context.Background(), revision, func(*ClusterProfiles) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if p, _, err := s.Read(); err != nil || p.Version != 1 {
		t.Fatal("first write is not a readable v1 file", err)
	}
}

func TestClusterFileReaderNormalizesEmptyValues(t *testing.T) {
	for name, settings := range map[string]string{
		"null integration":        `null`,
		"null connection":         `{"prometheus":null}`,
		"null headers":            `{"prometheus":{"url":"","headers":null}}`,
		"omitted URL":             `{"prometheus":{}}`,
		"empty optional target":   `{"target":""}`,
		"omitted identity server": `{"identity":{}}`,
	} {
		t.Run(name, func(t *testing.T) {
			raw := []byte(`{"version":1,"profiles":{"a":{"context":"dev","integrations":{"metrics":` + settings + `}}}}`)
			s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
			if err := os.WriteFile(s.Path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			p, revision, err := s.Read()
			if err != nil {
				t.Fatal(err)
			}
			if err := p.Settings("a", IntegrationMetrics).Validate(IntegrationMetrics); err != nil {
				t.Fatal(err)
			}
			if _, err := s.Update(context.Background(), revision, func(*ClusterProfiles) error { return nil }); err != nil {
				t.Fatal(err)
			}
			if _, _, err := s.Read(); err != nil {
				t.Fatal("normalized output is not readable", err)
			}
		})
	}
}

func TestClusterFileSemanticBoundary(t *testing.T) {
	for _, tc := range []struct {
		name, settings string
		kind           Integration
	}{
		{"target required", `{"prometheus":{"url":"https://metrics.example"}}`, IntegrationMetrics},
		{"wrong integration fields", `{"argocd":{"url":"https://argo.example"},"target":"a"}`, IntegrationMetrics},
		{"redundant metrics mode", `{"mode":"auto"}`, IntegrationMetrics},
		{"invalid env reference", `{"target":"a","prometheus":{"url":"https://metrics.example","headersFromEnv":{"Authorization":"9TOKEN"}}}`, IntegrationMetrics},
		{"cost source contradiction", `{"mode":"prometheus","clusterId":"dev","target":"a"}`, IntegrationCost},
		{"unsupported cost mode", `{"mode":"other"}`, IntegrationCost},
		{"invalid URL", `{"target":"a","prometheus":{"url":"not-a-url"}}`, IntegrationMetrics},
		{"credentials in URL", `{"target":"a","argocd":{"url":"https://user:secret@argo.example"}}`, IntegrationArgoCD},
		{"headers without URL", `{"prometheus":{"url":"","headers":{"Authorization":"synthetic"}}}`, IntegrationMetrics},
		{"duplicate header sources", `{"target":"a","prometheus":{"url":"https://metrics.example","headers":{"Authorization":"synthetic"},"headersFromEnv":{"authorization":"TEST_TOKEN"}}}`, IntegrationMetrics},
		{"token line break", `{"target":"a","argocd":{"url":"","token":"a\nb"}}`, IntegrationArgoCD},
		{"key line break", `{"target":"a","kubecost":{"url":"","apiKey":"a\nb"}}`, IntegrationCost},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"version":1,"profiles":{"a":{"context":"dev","integrations":{"` + string(tc.kind) + `":` + tc.settings + `}}}}`)
			s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
			if err := os.WriteFile(s.Path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			p, _, err := s.Read()
			if err != nil {
				t.Fatalf("per-connection semantic error blocked the entire file: %v", err)
			}
			if err := p.Settings("a", tc.kind).Validate(tc.kind); err == nil {
				t.Fatal("runtime accepted invalid connection")
			}
		})
	}
}
