package server

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s.io/client-go/openapi"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
)

func TestWriteJSONResourceSchemasMatchesStock(t *testing.T) {
	const raw = `{"components":{"schemas":{
		"Widget": {
			"x-kubernetes-group-version-kind": [{"group":"example.io","version":"v1","kind":"Widget"}],
			"description":"<Widget>",
			"properties":{"spec":{"$ref":"#/components/schemas/WidgetSpec"}}
		},
		"WidgetSpec":{"type":"object"}
	}}}`
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/openapi/v3":
			io.WriteString(w, `{"paths":{"apis/example.io/v1":{"serverRelativeURL":"/openapi/v3/apis/example.io/v1"}}}`)
		case "/openapi/v3/apis/example.io/v1":
			io.WriteString(w, raw)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(api.Close)
	oldStatus := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnected})
	t.Cleanup(func() { k8s.SetConnectionStatus(oldStatus) })
	oldConfig := k8s.SetTestConfig(&rest.Config{Host: api.URL})
	t.Cleanup(func() { k8s.SetTestConfig(oldConfig) })

	req := httptest.NewRequest(http.MethodPost, "/api/resources/schemas",
		strings.NewReader(`{"documents":[{"index":0,"apiVersion":"example.io/v1","kind":"Widget"}]}`))
	rec := httptest.NewRecorder()
	testServerSrv.Handler().ServeHTTP(rec, req)

	want := stockJSONResponse(t, yamlSchemaResponse{
		Documents: []yamlSchemaDocument{{Index: 0, Status: "available", BundleKey: "apis/example.io/v1", SchemaRef: "#/definitions/Widget"}},
		Bundles: map[string]yamlSchemaBundle{"apis/example.io/v1": {Definitions: map[string]any{
			"Widget": map[string]any{
				"x-kubernetes-group-version-kind": []any{map[string]any{"group": "example.io", "version": "v1", "kind": "Widget"}},
				"description":                     "<Widget>",
				"properties":                      map[string]any{"spec": map[string]any{"$ref": "#/definitions/WidgetSpec"}},
			},
			"WidgetSpec": map[string]any{"type": "object"},
		}}},
	})
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "application/json" || !bytes.Equal(rec.Body.Bytes(), want) {
		t.Fatalf("schema response = %d %v %q, want 200 application/json %q", rec.Code, rec.Header(), rec.Body, want)
	}
}

func TestYAMLSchemaCachePrincipalSeparatesUsersAndGroups(t *testing.T) {
	request := httptest.NewRequest("POST", "/", nil)
	if got := requestCachePrincipal(request); got != "local" {
		t.Fatalf("local principal = %q", got)
	}
	alice := &auth.User{Username: "alice", Groups: []string{"viewers", "devs"}}
	request = request.WithContext(auth.ContextWithUser(context.Background(), alice))
	got := requestCachePrincipal(request)
	if got != "alice\x00devs\x00viewers" {
		t.Fatalf("principal = %q", got)
	}
}

func TestCacheYAMLSchemaPathsSweepsAndCapsPrincipals(t *testing.T) {
	now := time.Now()
	server := &Server{yamlSchemaPathCache: map[string]yamlSchemaPathCacheEntry{
		"expired": {paths: map[string]openapi.GroupVersion{}, expiresAt: now.Add(-time.Second)},
		"active":  {paths: map[string]openapi.GroupVersion{}, expiresAt: now.Add(time.Second)},
	}}
	server.cacheYAMLSchemaPaths("new", map[string]openapi.GroupVersion{}, now)
	if _, exists := server.yamlSchemaPathCache["expired"]; exists {
		t.Fatal("expired path cache entry was retained")
	}
	if len(server.yamlSchemaPathCache) != 2 {
		t.Fatalf("path cache entries = %d, want active and new", len(server.yamlSchemaPathCache))
	}

	server.yamlSchemaPathCache = make(map[string]yamlSchemaPathCacheEntry)
	for i := 0; i < maxYAMLSchemaCacheEntries; i++ {
		server.yamlSchemaPathCache[string(rune(i))] = yamlSchemaPathCacheEntry{expiresAt: now.Add(time.Minute)}
	}
	server.cacheYAMLSchemaPaths("bounded", map[string]openapi.GroupVersion{}, now)
	if len(server.yamlSchemaPathCache) != 1 || server.yamlSchemaPathCache["bounded"].paths == nil {
		t.Fatalf("full path cache was not bounded: %#v", server.yamlSchemaPathCache)
	}
}

func TestBuildYAMLSchemaBundle_SelectsGVKAndReferencedDefinitions(t *testing.T) {
	raw := []byte(`{
  "components": {"schemas": {
    "io.example.Widget": {
      "type": "object",
      "x-kubernetes-group-version-kind": [{"group":"example.io","version":"v1","kind":"Widget"}],
      "properties": {"spec": {"$ref":"#/components/schemas/io.example.WidgetSpec"}}
    },
    "io.example.WidgetSpec": {
      "type": "object",
      "properties": {
        "port": {"type":"string","format":"int-or-string"},
        "note": {"type":"string","nullable":true},
        "oldField": {"type":"string","deprecated":true},
        "template": {
          "type":"object",
          "default":{"format":"int-or-string","deprecated":true,"nullable":true}
        },
        "extension": {"type":"object","x-kubernetes-preserve-unknown-fields":true}
      }
    },
    "io.example.Unused": {"type":"object"}
  }}
}`)

	bundle, roots, err := buildYAMLSchemaBundle(raw, []yamlSchemaIdentity{{
		Index: 3, APIVersion: "example.io/v1", Kind: "Widget",
	}})
	if err != nil {
		t.Fatalf("buildYAMLSchemaBundle failed: %v", err)
	}
	if roots[3] != "io.example.Widget" {
		t.Fatalf("root = %q, want io.example.Widget", roots[3])
	}
	if len(bundle.Definitions) != 2 || bundle.Definitions["io.example.Unused"] != nil {
		t.Fatalf("definitions = %#v, want only root closure", bundle.Definitions)
	}
	encoded, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	text := string(encoded)
	if strings.Contains(text, "#/components/schemas/") || !strings.Contains(text, "#/definitions/io.example.WidgetSpec") {
		t.Fatalf("refs were not rewritten: %s", text)
	}
	if !strings.Contains(text, `"oneOf":[{"type":"integer"},{"type":"string"}]`) {
		t.Fatalf("int-or-string was not normalized: %s", text)
	}
	if !strings.Contains(text, `"type":["string","null"]`) {
		t.Fatalf("nullable type was not normalized: %s", text)
	}
	if !strings.Contains(text, `"deprecationMessage":"[radar-advisory:deprecated] This field is deprecated."`) {
		t.Fatalf("deprecated field was not normalized: %s", text)
	}
	if !strings.Contains(text, `"default":{"deprecated":true,"format":"int-or-string","nullable":true}`) {
		t.Fatalf("schema default value was mutated as though it were a schema: %s", text)
	}
	if !strings.Contains(text, `"extension":{"additionalProperties":true`) {
		t.Fatalf("preserve-unknown-fields was not normalized: %s", text)
	}
}

func TestBuildYAMLSchemaBundle_MissingGVKIsPerDocument(t *testing.T) {
	raw := []byte(`{"components":{"schemas":{"io.example.Widget":{"x-kubernetes-group-version-kind":{"group":"example.io","version":"v1","kind":"Widget"}}}}}`)
	bundle, roots, err := buildYAMLSchemaBundle(raw, []yamlSchemaIdentity{
		{Index: 0, APIVersion: "example.io/v1", Kind: "Widget"},
		{Index: 1, APIVersion: "example.io/v1", Kind: "Missing"},
	})
	if err != nil {
		t.Fatalf("buildYAMLSchemaBundle failed: %v", err)
	}
	if roots[0] == "" || roots[1] != "" || len(bundle.Definitions) != 1 {
		t.Fatalf("roots=%v definitions=%v", roots, bundle.Definitions)
	}
}

func TestOpenAPIPathForVersion(t *testing.T) {
	tests := map[string]string{
		"v1":      "api/v1",
		"apps/v1": "apis/apps/v1",
	}
	for input, want := range tests {
		got, err := openAPIPathForVersion(input)
		if err != nil || got != want {
			t.Fatalf("openAPIPathForVersion(%q) = %q, %v; want %q", input, got, err, want)
		}
	}
	if _, err := openAPIPathForVersion("apps/v1/extra"); err == nil {
		t.Fatal("invalid apiVersion was accepted")
	}
}

func TestYAMLSchemaDocumentSignatureIgnoresOrderIndicesAndDuplicates(t *testing.T) {
	first := yamlSchemaDocumentSignature([]yamlSchemaIdentity{
		{Index: 9, APIVersion: "v1", Kind: "ConfigMap"},
		{Index: 1, APIVersion: "apps/v1", Kind: "Deployment"},
		{Index: 2, APIVersion: "v1", Kind: "ConfigMap"},
	})
	second := yamlSchemaDocumentSignature([]yamlSchemaIdentity{
		{Index: 0, APIVersion: "apps/v1", Kind: "Deployment"},
		{Index: 8, APIVersion: "v1", Kind: "ConfigMap"},
	})
	if first != second {
		t.Fatalf("equivalent schema requests produced different signatures: %q != %q", first, second)
	}
}
