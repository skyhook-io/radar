package mcp

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
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

type servingTransportCase struct {
	fixture         string
	gvr             schema.GroupVersionResource
	kind, listKind  string
	name            string
	generation      int64
	expectedServing string
	// prepare adjusts the fixture before it is cached.
	prepare func(t *testing.T, obj *unstructured.Unstructured)
	// checkContext asserts transport-specific context beyond the serving block.
	checkContext func(t *testing.T, transport string, rc map[string]any)
}

// runServingTransportCase serves one fixture from the dynamic cache and checks
// that REST AI detail and MCP get_resource emit the same serving block,
// that context=none omits the whole context, and that nothing mutates the cache.
func runServingTransportCase(t *testing.T, tc servingTransportCase) {
	setupFakeCacheForDiagnoseTests(t)
	data, err := os.ReadFile(tc.fixture)
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
	obj.SetName(tc.name)
	obj.SetGeneration(tc.generation)
	obj.SetUID("serving-context-test")
	if tc.prepare != nil {
		tc.prepare(t, obj)
	}
	before := obj.DeepCopy()
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{tc.gvr: tc.listKind}, obj)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: tc.gvr.Group, Version: tc.gvr.Version, Name: tc.gvr.Resource, Kind: tc.kind, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(tc.gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(tc.gvr, 5*time.Second) {
		t.Fatalf("%s cache did not sync", tc.kind)
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

	var wantServing map[string]any
	if err := json.Unmarshal([]byte(tc.expectedServing), &wantServing); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"basic", "none"} {
		t.Run(mode, func(t *testing.T) {
			response, err := http.Get(httpServer.URL + "/api/ai/resources/" + tc.gvr.Resource + "/alpha/" + tc.name + "?group=" + tc.gvr.Group + "&context=" + mode)
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
			result, err := session.CallTool(ctx, &mcpsdk.CallToolParams{Name: "get_resource", Arguments: map[string]any{"kind": tc.gvr.Resource, "group": tc.gvr.Group, "namespace": "alpha", "name": tc.name, "context": mode}})
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
					if serving, ok := rc["serving"].(map[string]any); ok {
						if kserve, ok := serving["kserveInferenceService"].(map[string]any); ok {
							for _, entry := range kserve["fieldNotes"].([]any) {
								field := entry.(map[string]any)["field"].(string)
								if _, found, err := unstructured.NestedFieldNoCopy(resource, strings.Split(field, ".")...); err != nil || !found {
									t.Fatalf("%s note path %s absent from detail resource", transport, field)
								}
							}
						}
					}
					if tc.checkContext != nil {
						tc.checkContext(t, transport, rc)
					}
				}
				meta := resource["metadata"].(map[string]any)
				if resource["apiVersion"] != tc.gvr.Group+"/"+tc.gvr.Version || resource["kind"] != tc.kind || meta["name"] != tc.name || meta["namespace"] != "alpha" || meta["generation"] != float64(tc.generation) {
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
	cached, err := k8s.GetResourceCache().GetDynamicWithGroup(ctx, tc.gvr.Resource, "alpha", tc.name, tc.gvr.Group)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Object, cached.Object) {
		t.Fatal("producer mutated cached resource")
	}
}

func TestServingContextRESTAndMCP(t *testing.T) {
	runServingTransportCase(t, servingTransportCase{
		fixture:    "../../pkg/servinginsight/testdata/rayservice-rollback.yaml",
		gvr:        schema.GroupVersionResource{Group: "ray.io", Version: "v1", Resource: "rayservices"},
		kind:       "RayService",
		listKind:   "RayServiceList",
		name:       "image-service",
		generation: 4,
		prepare: func(t *testing.T, obj *unstructured.Unstructured) {
			if err := unstructured.SetNestedField(obj.Object, int64(3), "status", "observedGeneration"); err != nil {
				t.Fatal(err)
			}
		},
		expectedServing: `{"rayService":{"observedGeneration":3,"suspendRequested":false,"active":{"clusterName":"image-service-raycluster-old","targetCapacityPercent":100,"trafficRoutedPercent":65,"applications":[{"name":"image","status":"RUNNING"}]},"pending":{"clusterName":"image-service-raycluster-new","targetCapacityPercent":0,"trafficRoutedPercent":0,"applications":[{"name":"image","status":"DEPLOY_FAILED"}]}}}`,
		checkContext: func(t *testing.T, transport string, rc map[string]any) {
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
		},
	})
}

func TestKServeServingContextRESTAndMCP(t *testing.T) {
	runServingTransportCase(t, servingTransportCase{
		fixture:         "../../pkg/servinginsight/testdata/kserve/recovered-stale-failure.json",
		gvr:             schema.GroupVersionResource{Group: "serving.kserve.io", Version: "v1beta1", Resource: "inferenceservices"},
		kind:            "InferenceService",
		listKind:        "InferenceServiceList",
		name:            "iris-cycle",
		generation:      7,
		expectedServing: `{"kserveInferenceService":{"deploymentMode":"Standard","clusterServingRuntimeName":"kserve-sklearnserver","model":{"transitionStatus":"UpToDate","targetModelState":"Loaded","lastFailure":{"reason":"ModelLoadFailed","exitCode":1}},"referenceWriterRelease":"KServe v0.20.0","fieldNotes":[{"field":"status.observedGeneration","note":"Copied from a child Deployment or Knative Service (whichever component was reconciled last), not this InferenceService's own; do not compare it with metadata.generation."},{"field":"status.modelStatus.transitionStatus","note":"Derived from the newest predictor Pod, falling back to the predictor Deployment's availability; UpToDate and Loaded can be reported while a new rollout's Pod has not started."},{"field":"status.modelStatus.states.activeModelState","note":"Set to Loaded when the controller marks a revision loaded and kept through later transitions (only a stop clears status); it does not show that any model is serving now."},{"field":"status.modelStatus.lastFailureInfo","note":"Kept after recovery and through later transitions (only a stop clears status); it may describe an earlier attempt rather than the current one."},{"field":"status.modelStatus.copies","note":"failedCopies is never written (always 0); totalCopies counts ready predictor Pods, not model copies."}]}}`,
		checkContext: func(t *testing.T, transport string, rc map[string]any) {
			ready := false
			for _, c := range rc["statusSummary"].(map[string]any)["conditions"].([]any) {
				condition := c.(map[string]any)
				if condition["type"] == "Ready" && condition["status"] == "True" {
					ready = true
				}
			}
			if !ready {
				t.Fatalf("%s lost root Ready condition: %+v", transport, rc["statusSummary"])
			}
		},
	})
}
