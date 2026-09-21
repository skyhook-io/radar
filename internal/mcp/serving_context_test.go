package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/server"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"sigs.k8s.io/yaml"
)

func TestServingContextRESTAndMCP(t *testing.T) {
	setupFakeCacheForDiagnoseTests(t)
	data, err := os.ReadFile("../../pkg/servinginsight/testdata/rayservice-rollback.yaml")
	if err != nil {
		t.Fatal(err)
	}
	data, err = yaml.YAMLToJSON(data)
	if err != nil {
		t.Fatal(err)
	}
	obj := &unstructured.Unstructured{}
	if err := obj.UnmarshalJSON(data); err != nil {
		t.Fatal(err)
	}
	obj.SetNamespace("alpha")
	obj.SetGeneration(4)
	obj.SetUID("serving-context-test")
	if err := unstructured.SetNestedField(obj.Object, int64(3), "status", "observedGeneration"); err != nil {
		t.Fatal(err)
	}
	before := obj.DeepCopy()
	gvr := schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayservices"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "RayServiceList"}, obj)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "RayService", Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 5*time.Second) {
		t.Fatal("RayService cache did not sync")
	}

	srv := server.New(server.Config{DevMode: true, MCPHandler: NewHandler()})
	httpServer := httptest.NewServer(srv.Handler())
	defer httpServer.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := mcpsdk.NewClient(&mcpsdk.Implementation{Name: "serving-context-test", Version: "test"}, nil)
	session, err := client.Connect(ctx, &mcpsdk.StreamableClientTransport{Endpoint: httpServer.URL + "/mcp"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer session.Close()

	const expectedServing = `{"rayService":{"observedGeneration":3,"suspendRequested":false,"active":{"clusterName":"image-service-raycluster-old","targetCapacityPercent":100,"trafficRoutedPercent":65,"applications":[{"name":"image","status":"RUNNING"}]},"pending":{"clusterName":"image-service-raycluster-new","targetCapacityPercent":0,"trafficRoutedPercent":0,"applications":[{"name":"image","status":"DEPLOY_FAILED"}]}}}`
	var wantServing map[string]any
	if err := json.Unmarshal([]byte(expectedServing), &wantServing); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"basic", "none"} {
		t.Run(mode, func(t *testing.T) {
			response, err := http.Get(httpServer.URL + "/api/ai/resources/rayservices/alpha/image-service?group=ray.io&context=" + mode)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			if response.StatusCode != http.StatusOK {
				t.Fatalf("REST status: %d", response.StatusCode)
			}
			var rest map[string]any
			if err := json.NewDecoder(response.Body).Decode(&rest); err != nil {
				t.Fatal(err)
			}
			result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_resource", Arguments: map[string]any{"kind": "rayservices", "group": "ray.io", "namespace": "alpha", "name": "image-service", "context": mode}})
			if err != nil {
				t.Fatal(err)
			}
			if result.IsError {
				t.Fatalf("MCP error: %s", extractText(t, result))
			}
			var mcp map[string]any
			if err := json.Unmarshal([]byte(extractText(t, result)), &mcp); err != nil {
				t.Fatal(err)
			}
			for transport, body := range map[string]map[string]any{"REST": rest, "MCP": mcp} {
				resource := body
				if mode == "none" {
					if _, present := body["resourceContext"]; present {
						t.Fatalf("%s context=none emitted context", transport)
					}
				} else {
					resource = body["resource"].(map[string]any)
					rc := body["resourceContext"].(map[string]any)
					if rc["tier"] != "basic" || !reflect.DeepEqual(rc["serving"], wantServing) {
						t.Fatalf("%s serving contract: %+v", transport, rc)
					}
					for _, excluded := range []string{"execution", "rayServiceSummary"} {
						if _, present := rc[excluded]; present {
							t.Fatalf("%s emitted %s", transport, excluded)
						}
					}
					status := rc["statusSummary"].(map[string]any)
					conditions := status["conditions"].([]any)
					if len(conditions) != 3 || conditions[0].(map[string]any)["type"] != "Ready" || conditions[0].(map[string]any)["status"] != "True" || conditions[2].(map[string]any)["type"] != "RollbackInProgress" || conditions[2].(map[string]any)["status"] != "True" {
						t.Fatalf("%s lost independent root conditions: %+v", transport, status)
					}
					if _, present := status["conditionsTruncated"]; present {
						t.Fatalf("%s uncapped conditions marked truncated", transport)
					}
				}
				meta := resource["metadata"].(map[string]any)
				if resource["apiVersion"] != "ray.io/v1" || resource["kind"] != "RayService" || meta["name"] != "image-service" || meta["namespace"] != "alpha" || meta["generation"] != float64(4) {
					t.Fatalf("%s resource identity/generation: %+v", transport, resource)
				}
				if _, exists := meta["uid"]; exists {
					t.Fatalf("%s minification bypassed", transport)
				}
			}
			if mode == "none" && !reflect.DeepEqual(rest, mcp) {
				t.Fatal("bare resource differs across transports")
			}
		})
	}
	cached, err := k8s.GetResourceCache().GetDynamicWithGroup(ctx, "rayservices", "alpha", "image-service", "ray.io")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Object, cached.Object) {
		t.Fatal("producer mutated cached resource")
	}
}
