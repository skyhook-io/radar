package config

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/skyhook-io/radar/pkg/argoapi"
	"github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

func clusterFileSchema(t *testing.T) *jsonschema.Schema {
	t.Helper()
	schema, err := jsonschema.NewCompiler().Compile("../../docs/schemas/clusters.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	return schema
}

func schemaDocument(t *testing.T, data []byte) any {
	t.Helper()
	var doc any
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

func TestClusterFileSchemaRoundTrip(t *testing.T) {
	schema := clusterFileSchema(t)
	data, err := os.ReadFile("testdata/clusters-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	want := schemaDocument(t, data)
	if err := schema.Validate(want); err != nil {
		t.Fatal(err)
	}
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
	got := schemaDocument(t, data)
	if err := schema.Validate(got); err != nil {
		t.Fatalf("writer produced a schema-invalid file: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatal("v1 fields changed or disappeared during round-trip")
	}
}

func TestClusterFileSchemaFieldCoverage(t *testing.T) {
	data, err := os.ReadFile("../../docs/schemas/clusters.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	root := schemaDocument(t, data).(map[string]any)
	defs := root["$defs"].(map[string]any)
	for _, tc := range []struct {
		name    string
		value   any
		schemas []map[string]any
	}{
		{"root", ClusterProfiles{}, []map[string]any{root}},
		{"profile", ClusterProfile{}, []map[string]any{defs["profile"].(map[string]any)}},
		{"capi", CAPIProfileReference{}, []map[string]any{defs["profile"].(map[string]any)["properties"].(map[string]any)["capi"].(map[string]any)}},
		{"identity", TargetIdentity{}, []map[string]any{defs["identity"].(map[string]any)}},
		{"prometheus", prom.Connection{}, []map[string]any{defs["prometheus"].(map[string]any)}},
		{"argocd", argoapi.Connection{}, []map[string]any{defs["argocd"].(map[string]any)}},
		{"kubecost", opencost.Connection{}, []map[string]any{defs["kubecost"].(map[string]any)}},
		{"integration", IntegrationSettings{}, []map[string]any{defs["metricsSettings"].(map[string]any), defs["argocdSettings"].(map[string]any), defs["costSettings"].(map[string]any)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			goFields, schemaFields := map[string]bool{}, map[string]bool{}
			typ := reflect.TypeOf(tc.value)
			for i := range typ.NumField() {
				goFields[strings.Split(typ.Field(i).Tag.Get("json"), ",")[0]] = true
			}
			for _, schema := range tc.schemas {
				for name := range schema["properties"].(map[string]any) {
					schemaFields[name] = true
				}
			}
			if !reflect.DeepEqual(goFields, schemaFields) {
				t.Fatalf("persisted fields drifted: Go %v, schema %v", goFields, schemaFields)
			}
		})
	}
}

func TestClusterFileSchemaRejectedStructure(t *testing.T) {
	schema := clusterFileSchema(t)
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
			if err := schema.Validate(schemaDocument(t, []byte(raw))); err == nil {
				t.Fatal("schema accepted invalid structure")
			}
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

func TestClusterFileSchemaFirstWrite(t *testing.T) {
	s := &ProfileStore{Path: filepath.Join(t.TempDir(), "clusters.json")}
	p, revision, err := s.Read()
	if err != nil || p.Version != 1 || len(p.Profiles) != 0 {
		t.Fatal("missing file did not start empty", err)
	}
	if _, err := s.Update(context.Background(), revision, func(*ClusterProfiles) error { return nil }); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(s.Path)
	if err != nil {
		t.Fatal(err)
	}
	if err := clusterFileSchema(t).Validate(schemaDocument(t, data)); err != nil {
		t.Fatal("first write does not conform to v1", err)
	}
}

func TestClusterFileSchemaReaderNormalizesEmptyValues(t *testing.T) {
	schema := clusterFileSchema(t)
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
			if err := schema.Validate(schemaDocument(t, raw)); err == nil {
				t.Fatal("non-canonical input unexpectedly passes schema")
			}
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
			data, err := os.ReadFile(s.Path)
			if err != nil {
				t.Fatal(err)
			}
			if err := schema.Validate(schemaDocument(t, data)); err != nil {
				t.Fatal("normalized output fails schema", err)
			}
		})
	}
}

func TestClusterFileSchemaSemanticBoundary(t *testing.T) {
	schema := clusterFileSchema(t)
	for _, tc := range []struct {
		name, settings string
		kind           Integration
		schemaValid    bool
	}{
		{"target required", `{"prometheus":{"url":"https://metrics.example"}}`, IntegrationMetrics, false},
		{"wrong integration fields", `{"argocd":{"url":"https://argo.example"},"target":"a"}`, IntegrationMetrics, false},
		{"redundant metrics mode", `{"mode":"auto"}`, IntegrationMetrics, false},
		{"invalid env reference", `{"target":"a","prometheus":{"url":"https://metrics.example","headersFromEnv":{"Authorization":"9TOKEN"}}}`, IntegrationMetrics, false},
		{"cost source contradiction", `{"mode":"prometheus","clusterId":"dev","target":"a"}`, IntegrationCost, false},
		{"unsupported cost mode", `{"mode":"other"}`, IntegrationCost, false},
		{"invalid URL", `{"target":"a","prometheus":{"url":"not-a-url"}}`, IntegrationMetrics, true},
		{"credentials in URL", `{"target":"a","argocd":{"url":"https://user:secret@argo.example"}}`, IntegrationArgoCD, true},
		{"headers without URL", `{"prometheus":{"url":"","headers":{"Authorization":"synthetic"}}}`, IntegrationMetrics, true},
		{"duplicate header sources", `{"target":"a","prometheus":{"url":"https://metrics.example","headers":{"Authorization":"synthetic"},"headersFromEnv":{"authorization":"TEST_TOKEN"}}}`, IntegrationMetrics, true},
		{"token line break", `{"target":"a","argocd":{"url":"","token":"a\nb"}}`, IntegrationArgoCD, true},
		{"key line break", `{"target":"a","kubecost":{"url":"","apiKey":"a\nb"}}`, IntegrationCost, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			raw := []byte(`{"version":1,"profiles":{"a":{"context":"dev","integrations":{"` + string(tc.kind) + `":` + tc.settings + `}}}}`)
			if got := schema.Validate(schemaDocument(t, raw)) == nil; got != tc.schemaValid {
				t.Fatalf("schema valid = %v, want %v", got, tc.schemaValid)
			}
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
