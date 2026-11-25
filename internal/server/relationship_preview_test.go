package server_test

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/mcp"
	"github.com/skyhook-io/radar/internal/server"
	"github.com/skyhook-io/radar/pkg/resourcecontext"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
)

func TestResourceRelationshipPreviewAcrossRESTAndMCP(t *testing.T) {
	repos := schema.GroupVersionResource{Group: "source.toolkit.fluxcd.io", Version: "v1", Resource: "gitrepositories"}
	ks := schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}
	object := func(gvr schema.GroupVersionResource, kind, ns, name string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": gvr.GroupVersion().String(), "kind": kind,
			"metadata": map[string]any{"name": name, "namespace": ns, "uid": ns + "-" + name},
		}}
	}
	const repo = "platform"
	objects := []runtime.Object{object(repos, "GitRepository", "broken", repo)}
	for i := range 22 {
		ns := "broken"
		ref := map[string]any{"kind": "GitRepository", "name": repo}
		if i >= 20 {
			ns = "default"
			if i == 20 {
				ref["namespace"] = "broken"
			}
		}
		u := object(ks, "Kustomization", ns, fmt.Sprintf("app-%02d", i))
		u.Object["spec"] = map[string]any{"sourceRef": ref}
		objects = append(objects, u)
	}
	listKinds := map[schema.GroupVersionResource]string{repos: "GitRepositoryList", ks: "KustomizationList"}
	var resources []k8s.APIResource
	for gvr, list := range listKinds {
		resources = append(resources, k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: strings.TrimSuffix(list, "List"), Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch", "delete"}})
	}
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, objects...)
	if err := k8s.InitTestDynamicResourceCache(client, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	for gvr := range listKinds {
		if err := cache.EnsureWatching(gvr); err != nil || !cache.WaitForSync(gvr, 5*time.Second) {
			t.Fatalf("sync %s: %v", gvr, err)
		}
	}
	srv := server.New(server.Config{DevMode: true, MCPHandler: mcp.NewHandler()})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(func() { ts.Close(); srv.Stop() })
	session, err := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "preview-test", Version: "test"}, nil).Connect(t.Context(), &mcpsdk.StreamableClientTransport{Endpoint: ts.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = session.Close() })
	call := func(name string, args map[string]any, out any) {
		t.Helper()
		result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError || len(result.Content) != 1 {
			t.Fatalf("%s: %v %+v", name, err, result)
		}
		raw := result.Content[0].(*mcpsdk.TextContent).Text
		if err := json.Unmarshal([]byte(raw), out); err != nil {
			t.Fatalf("%s: %v", raw, err)
		}
	}
	type envelope struct {
		Context  resourcecontext.ResourceContext `json:"resourceContext"`
		Warnings []string                        `json:"warnings"`
	}
	var rest, viaMCP envelope
	resp, err := http.Get(ts.URL + "/api/ai/resources/gitrepositories/broken/platform?group=" + repos.Group)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if err := json.NewDecoder(resp.Body).Decode(&rest); err != nil || resp.StatusCode != 200 {
		t.Fatalf("REST %d: %v", resp.StatusCode, err)
	}
	args := map[string]any{"kind": "GitRepository", "group": repos.Group, "namespace": "broken", "name": repo}
	call("get_resource", args, &viaMCP)
	gap := resourcecontext.OmittedField{Field: "dependents", Reason: resourcecontext.OmittedBudgetExceeded}
	omittedConsumer := resourcecontext.ContextRef{Group: ks.Group, Kind: "Kustomization", Namespace: "default", Name: "app-20"}
	if len(rest.Context.Dependents) != 20 || !slices.Equal(rest.Context.Dependents, viaMCP.Context.Dependents) || slices.Contains(rest.Context.Dependents, omittedConsumer) ||
		!slices.Contains(rest.Context.Omitted, gap) || !slices.Contains(viaMCP.Context.Omitted, gap) {
		t.Fatalf("REST %+v MCP %+v", rest, viaMCP)
	}
	if !slices.Contains(viaMCP.Warnings, "Partial preview of dependents. Radar reached its display or access-check limit. The total is unknown. To list up to 200 of one, call get_resource for this resource with relationship=\"dependents\".") {
		t.Fatalf("warnings %v", viaMCP.Warnings)
	}
	args["context"] = "none"
	var bare map[string]json.RawMessage
	call("get_resource", args, &bare)
	if bare["resourceContext"] != nil || bare["warnings"] != nil || bare["kind"] == nil {
		t.Fatalf("bare read %s", mustJSON(t, bare))
	}
	delete(args, "context")
	args["relationship"] = "dependents"
	var expanded envelope
	call("get_resource", args, &expanded)
	if len(expanded.Context.Dependents) != 21 || !slices.Contains(expanded.Context.Dependents, omittedConsumer) ||
		slices.Contains(expanded.Context.Omitted, gap) || len(expanded.Warnings) != 0 {
		t.Fatalf("expanded read %+v", expanded)
	}
	for _, bad := range []map[string]any{
		{"relationship": "referencedBy"},
		{"relationship": "dependents", "context": "none"},
		{"relationship": "exposes"},
	} {
		badArgs := maps.Clone(args)
		maps.Copy(badArgs, bad)
		result, err := session.CallTool(t.Context(), &mcpsdk.CallToolParams{Name: "get_resource", Arguments: badArgs})
		if err != nil || !result.IsError {
			t.Fatalf("%v accepted: %v %+v", bad, err, result)
		}
	}
}
