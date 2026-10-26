package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/discovery"
	fakediscovery "k8s.io/client-go/discovery/fake"
	"k8s.io/client-go/dynamic"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/investigationrefs"
	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/investigation"
	"github.com/skyhook-io/radar/pkg/k8score"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestResourceReadsReportCallerDenial(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withClusterAdmin(t, "denied-reader")
	for _, kind := range []string{"nodes", "persistentvolumes", "namespaces", "storageclasses", "volumeattachments", "ingressclasses", "clusterroles", "clusterrolebindings", "priorityclasses", "runtimeclasses", "mutatingwebhookconfigurations", "validatingwebhookconfigurations", "customresourcedefinitions"} {
		group, resource, ok := k8s.ClusterOnlyKindGVR(kind)
		if !ok {
			t.Fatalf("%s missing from scope catalog", kind)
		}
		denyClusterRead(t, "denied-reader", group+"/"+resource)
		for _, verb := range []string{"list", "get"} {
			t.Run(kind+"/"+verb, func(t *testing.T) {
				var err error
				if verb == "list" {
					_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: kind})
				} else {
					_, _, err = handleGetResource(ctx, nil, getResourceInput{Kind: kind, Name: "hidden"})
				}
				for _, want := range []string{"user_forbidden: Your Kubernetes access doesn't allow you to " + verb + " " + qualifiedResource(group, resource) + " across the cluster"} {
					if err == nil || !strings.Contains(err.Error(), want) {
						t.Errorf("want %q in caller error, got %v", want, err)
					}
				}
			})
		}
	}
}

func TestResourceReadsRejectUnsyncedTypedCache(t *testing.T) {
	delays := map[string]time.Duration{}
	for _, kind := range []string{"nodes", "persistentvolumes", "namespaces", "storageclasses", "ingressclasses", "clusterroles", "clusterrolebindings", "secrets"} {
		delays[kind] = time.Hour
	}
	if err := k8s.InitTestPromotedSyncingCache(fake.NewClientset(), 5*time.Second, time.Minute, delays); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	for kind := range delays {
		for _, verb := range []string{"list", "get"} {
			t.Run(kind+"/"+verb, func(t *testing.T) {
				var err error
				if verb == "list" {
					_, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: kind})
				} else {
					_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: kind, Namespace: "alpha", Name: "unseen"})
				}
				if err == nil || !strings.Contains(err.Error(), "kind_sync_pending") {
					t.Errorf("unsynced store must report pending, got %v", err)
				}
			})
		}
	}
}

func TestResourceReadsReadyContract(t *testing.T) {
	for _, populated := range []bool{false, true} {
		t.Run(fmt.Sprintf("populated=%v", populated), func(t *testing.T) {
			var typedObjects, dynamicObjects []runtime.Object
			var resources []k8s.APIResource
			listKinds := map[schema.GroupVersionResource]string{}
			var kinds []resourceid.Builtin
			for _, b := range resourceid.Builtins {
				if _, _, cluster := k8s.ClusterOnlyKindGVR(b.Resource); cluster || b.Kind == "Secret" || b.Kind == "APIService" {
					kinds = append(kinds, b)
				}
			}
			kinds = append(kinds, resourceid.Builtin{Kind: "Widget", Resource: "widgets", Group: "example.test", Version: "v1", Namespaced: true})
			for _, b := range kinds {
				version := b.Version
				if version == "" {
					version = "v1"
				}
				gvr := schema.GroupVersionResource{Group: b.Group, Version: version, Resource: b.Resource}
				if k8s.TypedKindOwnsGroup(b.Kind, b.Group) {
					if populated {
						obj, err := scheme.Scheme.New(gvr.GroupVersion().WithKind(b.Kind))
						if err != nil {
							t.Fatal(err)
						}
						metadata, err := meta.Accessor(obj)
						if err != nil {
							t.Fatal(err)
						}
						metadata.SetName("present")
						if b.Namespaced {
							metadata.SetNamespace("alpha")
						}
						typedObjects = append(typedObjects, obj)
					}
				} else {
					listKinds[gvr] = b.Kind + "List"
					resources = append(resources, k8s.APIResource{Group: b.Group, Version: version, Kind: b.Kind, Name: b.Resource, Namespaced: b.Namespaced, Verbs: []string{"get", "list", "watch"}})
					if populated {
						obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvr.GroupVersion().String(), "kind": b.Kind, "metadata": map[string]any{"name": "present"}}}
						if b.Namespaced {
							obj.SetNamespace("alpha")
						}
						dynamicObjects = append(dynamicObjects, obj)
					}
				}
			}
			if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces(typedObjects...)...)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds, dynamicObjects...), resources); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			for _, b := range kinds {
				t.Run(b.Kind, func(t *testing.T) {
					ns := ""
					if b.Namespaced {
						ns = "alpha"
					}
					if !k8s.TypedKindOwnsGroup(b.Kind, b.Group) {
						_, err := k8s.GetResourceCache().ListDynamicWithGroup(context.Background(), b.Kind, ns, b.Group)
						if err != nil {
							t.Fatal(err)
						}
						version := b.Version
						if version == "" {
							version = "v1"
						}
						if !k8s.GetDynamicResourceCache().WaitForSync(schema.GroupVersionResource{Group: b.Group, Version: version, Resource: b.Resource}, 5*time.Second) {
							t.Fatal("dynamic cache did not sync")
						}
					}
					result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: b.Kind, Group: b.Group, Namespace: ns, Context: "none"})
					if err != nil {
						t.Fatal(err)
					}
					body := extractText(t, result)
					if populated && !containsName(body, "present") {
						t.Fatalf("missing populated row: %s", body)
					}
					// The fixture's namespaces exist, so only the Namespace kind
					// has rows in an otherwise empty cluster.
					if rows, _ := completeListBody(t, result); !populated && b.Kind != "Namespace" && len(rows) != 0 {
						t.Fatalf("a genuine empty list is complete and empty, got %s", body)
					}
					result, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: b.Kind, Group: b.Group, Namespace: ns, Name: "present", Context: "none"})
					if populated {
						if err != nil {
							t.Fatal(err)
						}
						if !containsName(extractText(t, result), "present") {
							t.Fatal("missing get object")
						}
					} else if err == nil || !strings.HasPrefix(err.Error(), "object_not_found: No ") || !strings.Contains(err.Error(), `named "present" exist in `) {
						t.Fatalf("ready absent get must report not found, got %v", err)
					}
				})
			}
		})
	}
}

func TestResourceReadsPermissionAndScopeFailures(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "alice", []string{"alpha"})
	for _, kind := range []string{"secrets", "pods", "widgets"} {
		for _, verb := range []string{"list", "get"} {
			ns := "beta"
			if kind == "secrets" {
				ns = "alpha"
				getPermCache().Get("alice", nil).SetCanI(verb, "", "secrets", ns, false)
			}
			var err error
			if verb == "list" {
				_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: kind, Namespace: ns})
			} else {
				_, _, err = handleGetResource(ctx, nil, getResourceInput{Kind: kind, Namespace: ns, Name: "hidden"})
			}
			want := "user_namespace_not_visible:"
			if kind == "secrets" {
				want = "user_forbidden:"
			}
			if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), ns) || (kind == "secrets" && !strings.Contains(err.Error(), verb)) {
				t.Fatalf("%s/%s: expected namespace caller denial, got %v", kind, verb, err)
			}
		}
	}
	noAccess := withRestrictedUser(t, "no-access", []string{})
	_, _, err := handleListResources(noAccess, nil, listResourcesInput{Kind: "pods"})
	if err == nil || !strings.Contains(err.Error(), "You can't access any namespace") {
		t.Fatalf("expected Radar namespace access denial, got %v", err)
	}
	unchecked := withClusterAdmin(t, "unchecked")
	for _, kind := range []string{"nodes", "secrets"} {
		for _, verb := range []string{"list", "get"} {
			if verb == "list" {
				_, _, err = handleListResources(unchecked, nil, listResourcesInput{Kind: kind})
			} else {
				_, _, err = handleGetResource(unchecked, nil, getResourceInput{Kind: kind, Namespace: "alpha", Name: "hidden"})
			}
			if err == nil || !strings.Contains(err.Error(), "user_permission_check_failed") {
				t.Fatalf("%s/%s: expected failed SAR, got %v", kind, verb, err)
			}
		}
	}

	discoveryFailed := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "uncached"})
	_, _, err = handleListResources(discoveryFailed, nil, listResourcesInput{Kind: "pods"})
	if err == nil || !strings.Contains(err.Error(), "user_permission_check_failed") {
		t.Fatalf("expected failed namespace discovery, got %v", err)
	}
	oldPin, oldTarget := k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("alpha")
	t.Cleanup(func() { k8s.ForceNamespaceScope = oldPin; k8s.SetNamespaceScopeOverride(oldTarget) })
	for _, verb := range []string{"list", "get"} {
		if verb == "list" {
			_, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: "pods", Namespace: "beta"})
		} else {
			_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "pods", Namespace: "beta", Name: "hidden"})
		}
		if err == nil || !strings.HasPrefix(err.Error(), "outside_namespace_pin: ") || strings.Contains(err.Error(), "user_forbidden") {
			t.Fatalf("scope exclusion must not claim denial, got %v", err)
		}
	}
}

func TestResourceReadsReportFailedAndDisabledTypedKinds(t *testing.T) {
	if err := k8s.InitTestPromotedSyncingCache(fake.NewClientset(), 5*time.Second, 50*time.Millisecond, map[string]time.Duration{"nodes": time.Hour}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	if err := wait.PollUntilContextTimeout(context.Background(), 10*time.Millisecond, 5*time.Second, true, func(context.Context) (bool, error) {
		return k8s.GetResourceCache().KindReadinessFor("nodes") == k8s.KindFailed, nil
	}); err != nil {
		t.Fatal(err)
	}
	for _, verb := range []string{"list", "get"} {
		var err error
		if verb == "list" {
			_, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: "nodes"})
		} else {
			_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "nodes", Name: "unseen"})
		}
		if err == nil || !strings.Contains(err.Error(), "kind_sync_timed_out") {
			t.Fatalf("expected failed sync, got %v", err)
		}
	}
	k8s.ResetTestState()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"services": {Enabled: true, Namespace: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		input listResourcesInput
		want  string
	}{
		{listResourcesInput{Kind: "nodes"}, "radar_kind_not_collected: "},
		{listResourcesInput{Kind: "services", Namespace: "beta"}, "radar_namespace_not_collected: "},
	} {
		_, _, err := handleListResources(context.Background(), nil, tc.input)
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Fatalf("%+v: want %q, got %v", tc.input, tc.want, err)
		}
	}
	result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "services", Context: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if rows, coverage := narrowedListBody(t, result); len(rows) != 0 || !slices.Equal(coverage.Read, []string{"alpha"}) || !slices.Equal(coverage.Scope, []string{"alpha"}) {
		t.Fatalf("unscoped collector-covered list must be narrowed to what Radar collects: %s", extractText(t, result))
	}
}

func TestResourceReadsDynamicPendingAndCollectorDenial(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprintf("collectorDenied=%v", denied), func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			kinds := []resourceid.Builtin{{Kind: "Widget", Resource: "widgets", Group: "example.test", Version: "v1", Namespaced: true}}
			for _, b := range resourceid.Builtins {
				if _, _, cluster := k8s.ClusterOnlyKindGVR(b.Resource); (cluster || b.Kind == "APIService") && !k8s.TypedKindOwnsGroup(b.Kind, b.Group) {
					kinds = append(kinds, b)
				}
			}
			listKinds := map[schema.GroupVersionResource]string{}
			var resources []k8s.APIResource
			for _, b := range kinds {
				version := b.Version
				if version == "" {
					version = "v1"
				}
				gvr := schema.GroupVersionResource{Group: b.Group, Version: version, Resource: b.Resource}
				listKinds[gvr] = b.Kind + "List"
				resources = append(resources, k8s.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: b.Kind, Namespaced: b.Namespaced, Verbs: []string{"get", "list", "watch"}})
			}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
			dyn.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				if denied {
					return true, nil, collectorRefusal(action.GetResource().GroupResource(), action.GetVerb(), action.GetNamespace())
				}
				if action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions().Limit == 1 {
					return false, nil, nil
				}
				return true, nil, fmt.Errorf("initial list unavailable")
			})
			dyn.PrependReactor("get", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				if denied {
					return true, nil, collectorRefusal(action.GetResource().GroupResource(), action.GetVerb(), action.GetNamespace())
				}
				return false, nil, nil
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			for _, b := range kinds {
				t.Run(b.Kind, func(t *testing.T) {
					ns := ""
					if b.Namespaced {
						ns = "alpha"
					}
					for _, verb := range []string{"list", "get"} {
						var err error
						if verb == "list" {
							_, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: b.Kind, Group: b.Group, Namespace: ns})
						} else {
							_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: b.Kind, Group: b.Group, Namespace: ns, Name: "hidden"})
						}
						want := "kind_sync_pending"
						if denied {
							want = "radar_forbidden"
						} else if (b.Kind == "CustomResourceDefinition" || b.Kind == "APIService") && verb == "get" {
							want = "object_not_found"
						}
						if err == nil || !strings.HasPrefix(err.Error(), want+":") || (want != "object_not_found" && strings.Contains(err.Error(), "object_not_found")) {
							t.Fatalf("%s: expected %s, got %v", verb, want, err)
						}
						if denied && (!strings.Contains(err.Error(), "Radar's") || !strings.Contains(err.Error(), "can't ")) {
							t.Fatalf("denial must identify collector, got %v", err)
						}
						assertNoCollectorLeak(t, err)
					}
				})
			}
			for _, verb := range []string{"list", "get"} {
				var err error
				if verb == "list" {
					_, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: "unknown", Namespace: "alpha"})
				} else {
					_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "unknown", Namespace: "alpha", Name: "hidden"})
				}
				if err == nil || !strings.Contains(err.Error(), "kind_unknown") || strings.Contains(err.Error(), "object_not_found") {
					t.Fatalf("unknown kind must not claim absent resource, got %v", err)
				}
			}
		})
	}
}

func TestResourceReadToolErrorTransport(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	_ = withClusterAdmin(t, "transport-reader")
	denyClusterRead(t, "transport-reader", "/nodes")
	server := newServer(false, false, "")
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			return next(pkgauth.ContextWithUser(ctx, &pkgauth.User{Username: "transport-reader"}), method, req)
		}
	})
	session := connectTo(t, server)
	// wire decodes a result the way any MCP client receives it.
	wire := func(result *mcpsdk.CallToolResult) map[string]any {
		t.Helper()
		raw, err := json.Marshal(result)
		if err != nil {
			t.Fatal(err)
		}
		var out map[string]any
		if err := json.Unmarshal(raw, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, tc := range []struct {
		name      string
		arguments map[string]any
		reason    string
		detail    string
		next      string
	}{
		// Only a cluster admin can change a denial, so it carries no next.
		{"list_resources", map[string]any{"kind": "nodes"}, "user_forbidden", "Your Kubernetes access doesn't allow you to list nodes across the cluster", ""},
		{"get_resource", map[string]any{"kind": "nodes", "name": "hidden"}, "user_forbidden", "Your Kubernetes access doesn't allow you to get nodes across the cluster", ""},
		{"get_resource", map[string]any{"kind": "pods", "name": "hidden"}, "namespace_required", "Pods live in namespaces, so get_resource needs one.",
			`Call get_resource again with namespace set. If you don't know it, call search with query "kind:pods hidden" to find it.`},
	} {
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: tc.name, Arguments: tc.arguments})
		if err != nil {
			t.Fatal(err)
		}
		body := wire(result)
		assertReadErrorWire(t, tc.name, body, extractText(t, result), tc.reason, tc.detail)
		if next, _ := body["structuredContent"].(map[string]any)["next"].(string); next != tc.next {
			t.Fatalf("%s %s: next = %q, want %q", tc.name, tc.reason, next, tc.next)
		}
	}
	// An absent object in a namespace Radar read is an answer with a code too.
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "get_resource", Arguments: map[string]any{"kind": "pods", "namespace": "alpha", "name": "absent"}})
	if err != nil {
		t.Fatal(err)
	}
	body := wire(result)
	assertReadErrorWire(t, "get_resource absent", body, extractText(t, result), "object_not_found", `No pods named "absent" exist in namespace "alpha".`)
	if next, _ := body["structuredContent"].(map[string]any)["next"].(string); next != `Call search with query "kind:pods absent" to look for it in every namespace you can read.` {
		t.Fatalf("a miss points at search: next = %q", next)
	}
	result, err = session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "list_resources", Arguments: map[string]any{"kind": "services", "context": "none"}})
	if err != nil {
		t.Fatal(err)
	}
	if rows, _ := completeListBody(t, result); len(rows) != 0 {
		t.Fatalf("genuine empty success is complete and empty, got %s", extractText(t, result))
	}
}

// assertReadErrorWire checks a read error as any MCP client receives it: an
// error result whose structuredContent and only text block are the same
// {error, error_code} body, so a client reading either gets the code.
func assertReadErrorWire(t *testing.T, name string, body map[string]any, text, code, detail string) {
	t.Helper()
	structured, _ := body["structuredContent"].(map[string]any)
	explanation, _ := structured["error"].(string)
	if body["isError"] != true || structured["error_code"] != code || !strings.Contains(explanation, detail) || strings.HasPrefix(explanation, code) {
		t.Fatalf("%s: want isError with error_code %q, got %v", name, code, body)
	}
	var fromText map[string]any
	if err := json.Unmarshal([]byte(text), &fromText); err != nil || !reflect.DeepEqual(fromText, structured) {
		t.Fatalf("%s: the text block must be the structured body, got %q", name, text)
	}
}

func TestResourceReadsFailedSARCanRecover(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	previousClient := k8s.SetTestClient(&kubernetes.Clientset{})
	t.Cleanup(func() { k8s.SetTestClient(previousClient) })
	calls := 0
	stubSubjectCanI(t, func(context.Context, kubernetes.Interface, string, []string, string, string, string, string) (bool, error) {
		calls++
		if calls == 1 {
			return false, fmt.Errorf("permission service unavailable")
		}
		return false, nil
	})
	ctx := withRestrictedUser(t, "sar-reader", []string{"alpha"})
	for _, kind := range []string{"nodes", "secrets"} {
		for _, verb := range []string{"list", "get"} {
			calls = 0
			call := func() error {
				if verb == "list" {
					_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: kind, Namespace: "alpha"})
					return err
				}
				_, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: kind, Namespace: "alpha", Name: "hidden"})
				return err
			}
			if err := call(); err == nil || !strings.Contains(err.Error(), "user_permission_check_failed") {
				t.Fatalf("%s/%s first check: %v", kind, verb, err)
			}
			if err := call(); err == nil || !strings.Contains(err.Error(), "user_forbidden: Your Kubernetes access") {
				t.Fatalf("%s/%s retry must establish denial: %v", kind, verb, err)
			}
			if calls != 2 {
				t.Fatalf("failed SAR must not be cached: got %d checks", calls)
			}
		}
	}
}

func TestResourceReadsNamespacesIgnoreNamespacePin(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	oldPin, oldTarget := k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("alpha")
	t.Cleanup(func() { k8s.ForceNamespaceScope = oldPin; k8s.SetNamespaceScopeOverride(oldTarget) })
	for _, allowed := range [][]string{{"alpha"}, {}} {
		ctx := withRestrictedUser(t, "namespace-reader", allowed)
		getPermCache().Get("namespace-reader", nil).SetCanI("list", "", "namespaces", "", true)
		result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "namespaces", Namespace: "beta", Context: "none"})
		if err != nil {
			t.Fatal(err)
		}
		for _, ns := range []string{"alpha", "beta", "gamma"} {
			if !containsName(extractText(t, result), ns) {
				t.Fatalf("Namespace list must ignore pin/namespace membership: %s", extractText(t, result))
			}
		}
	}
}

// coverageClass is how Radar's collector reads a kind: a typed informer whose
// coverage the startup probe fixed, a custom-resource informer started per
// namespace on demand, or a direct API read with no informer.
type coverageClass string

const (
	coverageTyped  coverageClass = "typed"
	coverageCRD    coverageClass = "crd"
	coverageDirect coverageClass = "direct"
)

// collectorIdentity appears in every fake collector refusal; no caller-facing
// error may repeat it, or the namespace Radar runs in.
const (
	collectorInstallNamespace = "radar-install"
	collectorIdentity         = "system:serviceaccount:" + collectorInstallNamespace + ":radar-collector"
)

func collectorRefusal(gr schema.GroupResource, verb, namespace string) error {
	scope := fmt.Sprintf("in the namespace %q", namespace)
	if namespace == "" {
		scope = "at the cluster scope"
	}
	return apierrors.NewForbidden(gr, "", fmt.Errorf("User %q cannot %s resource %q in API group %q %s", collectorIdentity, verb, gr.Resource, gr.Group, scope))
}

// listWireCoverage is a list's coverage as a client decodes it.
type listWireCoverage struct {
	Status        string
	Codes         []string
	GapNamespaces map[string][]string
	ReadType      string
	Read          []string
	ScopeType     string
	Scope         []string
	NarrowedBy    []string
	Summary       string
	Next          string
}

// listBody decodes a list_resources result, failing unless it is the
// documented shape: one text block and nothing else, holding exactly
// {"summary", "next"?, "coverage", "items"} with the summary first, and
// coverage holding the shared single-kind coverage.
func listBody(t *testing.T, result *mcpsdk.CallToolResult) ([]map[string]any, listWireCoverage) {
	t.Helper()
	if result == nil || result.IsError || len(result.Content) != 1 || result.StructuredContent != nil {
		t.Fatalf("a list is one successful text block without structuredContent, got %+v", result)
	}
	text := extractText(t, result)
	// A reader that keeps only the head of a long result must keep the caveat.
	if !strings.HasPrefix(text, `{"summary":`) {
		t.Fatalf("a list must lead with its summary, got %.80s", text)
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal([]byte(text), &top); err != nil || len(top) < 3 || len(top) > 4 || top["items"] == nil || top["coverage"] == nil || top["summary"] == nil || (len(top) == 4 && top["next"] == nil) {
		t.Fatalf("a list must be {\"summary\", \"next\"?, \"coverage\", \"items\"}, got %s", text)
	}
	var next string
	if top["next"] != nil {
		if err := json.Unmarshal(top["next"], &next); err != nil || next == "" {
			t.Fatalf("next, when present, must be a sentence, got %s", top["next"])
		}
	}
	var summary string
	if err := json.Unmarshal(top["summary"], &summary); err != nil || summary == "" {
		t.Fatalf("a list must say what it covers in words, got %s", top["summary"])
	}
	var rows []map[string]any
	if err := json.Unmarshal(top["items"], &rows); err != nil || rows == nil {
		t.Fatalf("items must be an array, got %s", top["items"])
	}
	var coverage resourceapi.Coverage
	if err := json.Unmarshal(top["coverage"], &coverage); err != nil || coverage.Read == nil || coverage.Gaps == nil ||
		(coverage.Status != resourceapi.CoverageComplete && coverage.Status != resourceapi.CoveragePartial) ||
		(coverage.Status == resourceapi.CoveragePartial) != (len(coverage.Gaps) > 0) {
		t.Fatalf("invalid coverage: %s", top["coverage"])
	}
	if err := coverage.Validate(); err != nil {
		t.Fatalf("coverage breaks the contract: %v: %s", err, top["coverage"])
	}
	out := listWireCoverage{Status: string(coverage.Status), GapNamespaces: map[string][]string{}, ReadType: string(coverage.Read.Type), ScopeType: string(coverage.Scope.Type), Summary: summary, Next: next}
	for _, gap := range coverage.Gaps {
		out.Codes = append(out.Codes, string(gap.Code))
		if gap.Namespaces != nil {
			out.GapNamespaces[string(gap.Code)] = gap.Namespaces.Names
		}
	}
	if coverage.Read.Namespaces != nil {
		out.Read = coverage.Read.Namespaces.Names
		if !slices.IsSorted(out.Read) {
			t.Fatalf("namespace samples must be sorted: %v", out.Read)
		}
	}
	if coverage.Scope.Namespaces != nil {
		out.Scope = coverage.Scope.Namespaces.Names
	}
	for _, n := range coverage.NarrowedBy {
		out.NarrowedBy = append(out.NarrowedBy, string(n))
	}
	// The verdict leads, and every unread part is called unknown: readers
	// keep a caveat that comes first and drop one that trails.
	partial := coverage.Status == resourceapi.CoveragePartial
	if partial && (!strings.HasPrefix(summary, "Partial: ") || strings.Count(summary, "Unknown: ") != len(coverage.Gaps)) ||
		!partial && !strings.HasPrefix(summary, "Complete") ||
		len(coverage.NarrowedBy) > 0 && !strings.Contains(summary, "Other namespaces are unknown") {
		t.Fatalf("a list must lead with whether it is complete and call each part it didn't read unknown: %q", summary)
	}
	return rows, out
}

// partialListBody decodes a list Radar read only part of.
func partialListBody(t *testing.T, result *mcpsdk.CallToolResult) ([]map[string]any, listWireCoverage) {
	t.Helper()
	rows, coverage := listBody(t, result)
	if coverage.Status != "partial" || !strings.Contains(coverage.Summary, "Unknown: ") {
		t.Fatalf("want a partial list whose summary says it cannot establish absence, got %+v", coverage)
	}
	return rows, coverage
}

// narrowedListBody decodes a list the caller didn't scope that Radar's
// collection narrowed: complete within the namespaces Radar collects.
func narrowedListBody(t *testing.T, result *mcpsdk.CallToolResult) ([]map[string]any, listWireCoverage) {
	t.Helper()
	rows, coverage := completeListBody(t, result)
	if !slices.Contains(coverage.NarrowedBy, "namespace_collection") || coverage.ScopeType != "namespaces" || !strings.Contains(coverage.Summary, "Radar's collection") && !strings.Contains(coverage.Summary, "Radar collects ") {
		t.Fatalf("want a list narrowed by collection, got %+v", coverage)
	}
	return rows, coverage
}

// completeListBody decodes a list Radar read all of, within its scope.
func completeListBody(t *testing.T, result *mcpsdk.CallToolResult) ([]map[string]any, listWireCoverage) {
	t.Helper()
	rows, coverage := listBody(t, result)
	if coverage.Status != "complete" || len(coverage.Codes) != 0 {
		t.Fatalf("want a complete list, got %+v", coverage)
	}
	return rows, coverage
}

func rowNames(rows []map[string]any) []string {
	names := make([]string, 0, len(rows))
	for _, row := range rows {
		name, _ := row["name"].(string)
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

func assertNoCollectorLeak(t *testing.T, err error, hidden ...string) {
	t.Helper()
	if err == nil {
		return
	}
	for _, text := range append([]string{"system:serviceaccount:", collectorInstallNamespace, "is forbidden", "cannot list resource", "cannot get resource"}, hidden...) {
		if strings.Contains(err.Error(), text) {
			t.Fatalf("caller-facing error contains %q: %v", text, err)
		}
	}
}

// The collector covers alpha and gamma (gamma is empty); beta holds an object
// it cannot read. Every class must answer every scope by the same rule.
func setupCoverageRuleFixture(t *testing.T, class coverageClass) (kind string, prewarm func()) {
	t.Helper()
	if class == coverageTyped {
		client := fake.NewClientset(
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "alpha-object", Namespace: "alpha"}},
			&corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: "beta-object", Namespace: "beta"}},
		)
		if err := k8s.InitScopedTestResourceCacheNamespaces(client, map[string]k8score.ResourceScope{"services": {Enabled: true, Namespace: "alpha"}}, map[string][]string{"services": {"alpha", "gamma"}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetTestState)
		return "services", func() {}
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}, NamespaceScoped: true, Namespace: "alpha", ScopeCandidates: []string{"alpha", "gamma"}}))
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	listKind := "Widget"
	if class == coverageDirect {
		gvr = schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}
		listKind = "Lease"
	}
	objs := []runtime.Object{
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": gvr.GroupVersion().String(), "kind": listKind, "metadata": map[string]any{"name": "alpha-object", "namespace": "alpha"}}},
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": gvr.GroupVersion().String(), "kind": listKind, "metadata": map[string]any{"name": "beta-object", "namespace": "beta"}}},
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: listKind + "List"}, objs...)
	for _, verb := range []string{"list", "get"} {
		dyn.PrependReactor(verb, gvr.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
			if ns := action.GetNamespace(); ns != "alpha" && ns != "gamma" {
				return true, nil, collectorRefusal(gvr.GroupResource(), verb, ns)
			}
			return false, nil, nil
		})
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: listKind, Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	if class == coverageDirect {
		return "leases", func() {}
	}
	return "widgets", func() {
		if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
	}
}

// Typed kinds never read outside the coverage the startup probe fixed, so an
// uncovered namespace is radar_namespace_not_collected. Custom resources and direct
// kinds read every requested namespace, so the same namespace is
// radar_forbidden: Radar tried and was refused.
func coverageReason(class coverageClass) string {
	if class == coverageTyped {
		return "radar_namespace_not_collected"
	}
	return "radar_forbidden"
}

// partialCoverageReason is why an unrestricted read covered only some
// namespaces. Typed kinds and custom resources read the coverage their
// informers established; a direct kind was refused the cluster-wide list for
// this very request and fell back to the candidate namespaces.
func partialCoverageReason(class coverageClass) string {
	if class == coverageDirect {
		return "radar_forbidden"
	}
	return "radar_namespace_not_collected"
}

func TestResourceReadsCollectorCoverageRule(t *testing.T) {
	const anyHidden = "in any namespace you can access"
	cases := []struct {
		name      string
		caller    string // "" = auth disabled, "admin" = cluster-wide access, "restricted" = allowed
		allowed   []string
		namespace string
		rows      []string
		// unrestricted marks an all-namespaces read the collector serves only
		// in alpha and gamma: the read is narrowed to them.
		unrestricted bool
		// read and uncollected describe a caller's namespaces only some of
		// which Radar collects: the read is narrowed to read.
		read        []string
		uncollected bool
		narrowed    bool
		// wantErr is a read that served nothing.
		wantErr []string
		// coverage is the coverage text every class with known coverage
		// reports. A direct kind knows only the namespaces this request read,
		// so it reports coverage only when directCoverage is set.
		coverage       []string
		directCoverage bool
		// typedOnly limits coverage to typed kinds, whose coverage the
		// startup probe established. A refusal for this request says nothing
		// about where else Radar can read a custom resource or direct kind.
		typedOnly bool
		hidden    []string
	}{
		{name: "auth disabled reads the covered namespaces", rows: []string{"alpha-object"}, unrestricted: true},
		{name: "cluster-wide caller reads the covered namespaces", caller: "admin", rows: []string{"alpha-object"}, unrestricted: true},
		{name: "restricted caller fully covered", caller: "restricted", allowed: []string{"alpha", "gamma"}, rows: []string{"alpha-object"}, narrowed: true},
		{name: "restricted caller covered and empty", caller: "restricted", allowed: []string{"gamma"}, rows: []string{}, narrowed: true},
		{name: "explicit covered and empty", namespace: "gamma", rows: []string{}},
		{name: "restricted partial overlap", caller: "restricted", allowed: []string{"alpha", "beta"}, rows: []string{"alpha-object"}, narrowed: true, read: []string{"alpha"}, uncollected: true},
		{name: "restricted partial overlap with an empty covered namespace", caller: "restricted", allowed: []string{"gamma", "beta"}, rows: []string{}, narrowed: true, read: []string{"gamma"}, uncollected: true},
		{name: "no overlap", caller: "restricted", allowed: []string{"beta"}, wantErr: []string{`namespace "beta"`}, coverage: []string{anyHidden}, typedOnly: true, hidden: []string{"alpha", "gamma"}},
		{name: "no overlap across namespaces", caller: "restricted", allowed: []string{"beta", "delta"}, wantErr: []string{`namespaces "beta,delta"`}, coverage: []string{anyHidden}, typedOnly: true, hidden: []string{"alpha", "gamma"}},
		{name: "explicit uncovered", namespace: "beta", wantErr: []string{`namespace "beta"`}, coverage: []string{`in namespaces "alpha,gamma".`}},
		{name: "restricted explicit uncovered", caller: "restricted", allowed: []string{"alpha", "beta"}, namespace: "beta", wantErr: []string{`namespace "beta"`}, coverage: []string{`in namespace "alpha".`}, hidden: []string{"gamma"}},
	}
	for _, class := range []coverageClass{coverageTyped, coverageCRD, coverageDirect} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/%s", class, tc.name), func(t *testing.T) {
				kind, prewarm := setupCoverageRuleFixture(t, class)
				prewarm()
				ctx := context.Background()
				switch tc.caller {
				case "admin":
					ctx = withClusterAdmin(t, "coverage-admin")
				case "restricted":
					ctx = withRestrictedUser(t, "coverage-reader", tc.allowed)
				}
				result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: kind, Namespace: tc.namespace, Context: "none"})
				if tc.wantErr != nil {
					if err == nil || !strings.HasPrefix(err.Error(), coverageReason(class)+":") {
						t.Fatalf("a read that served nothing must fail with %s, got %v", coverageReason(class), err)
					}
					want := tc.wantErr
					if (class != coverageDirect || tc.directCoverage) && (class == coverageTyped || !tc.typedOnly) {
						want = append(slices.Clone(want), tc.coverage...)
					} else if strings.Contains(err.Error(), "It collects ") || strings.Contains(err.Error(), anyHidden) {
						t.Fatalf("%s cannot claim coverage it did not establish: %v", class, err)
					}
					for _, text := range want {
						if !strings.Contains(err.Error(), text) {
							t.Fatalf("missing %s: %v", text, err)
						}
					}
					assertNoCollectorLeak(t, err, tc.hidden...)
					rendered, ok := readErrorResult(err)
					body, _ := rendered.StructuredContent.(mcpReadError)
					if !ok || string(body.ErrorCode) != coverageReason(class) || body.Coverage != nil && (body.Coverage.Read != nil || body.Coverage.Validate() != nil) {
						t.Fatalf("an MCP read error says what wasn't read in words, and any coverage it sends is valid and has no read: %+v", body)
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				rows, coverage := listBody(t, result)
				switch {
				case tc.unrestricted:
					if coverage.Status != "complete" || len(coverage.Codes) != 0 || !slices.Equal(coverage.NarrowedBy, []string{"namespace_collection"}) || !slices.Equal(coverage.Read, []string{"alpha", "gamma"}) || !slices.Equal(coverage.Scope, []string{"alpha", "gamma"}) {
						t.Fatalf("coverage = %+v, want narrowed by collection to alpha and gamma", coverage)
					}
				case tc.uncollected:
					// The caller didn't name beta: Radar not collecting it narrows
					// the read rather than leaving a gap in it.
					if coverage.Status != "complete" || len(coverage.Codes) != 0 || !slices.Equal(coverage.NarrowedBy, []string{"user_access", "namespace_collection"}) || !slices.Equal(coverage.Read, tc.read) || !slices.Equal(coverage.Scope, tc.read) {
						t.Fatalf("coverage = %+v, want read and scope %v narrowed by user_access and collection", coverage, tc.read)
					}
					assertNoCollectorLeak(t, errors.New(coverage.Summary), tc.hidden...)
				default:
					if coverage.Status != "complete" || len(coverage.Codes) != 0 {
						t.Fatalf("coverage = %+v, want complete", coverage)
					}
				}
				if tc.narrowed != slices.Contains(coverage.NarrowedBy, "user_access") {
					t.Fatalf("narrowedBy = %v, want user_access: %v", coverage.NarrowedBy, tc.narrowed)
				}
				if got := rowNames(rows); !slices.Equal(got, tc.rows) {
					t.Fatalf("rows = %v, want %v", got, tc.rows)
				}
			})
		}
	}
	for _, class := range []coverageClass{coverageTyped, coverageCRD, coverageDirect} {
		t.Run(fmt.Sprintf("%s/get uncovered", class), func(t *testing.T) {
			kind, prewarm := setupCoverageRuleFixture(t, class)
			prewarm()
			ctx := withRestrictedUser(t, "coverage-getter", []string{"alpha", "beta"})
			_, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: kind, Namespace: "beta", Name: "beta-object"})
			if err == nil || !strings.HasPrefix(err.Error(), coverageReason(class)+":") || !strings.Contains(err.Error(), `namespace "beta"`) {
				t.Fatalf("uncovered get: %v", err)
			}
			if class != coverageDirect && !strings.Contains(err.Error(), `in namespace "alpha".`) {
				t.Fatalf("uncovered get must offer the caller's covered namespaces: %v", err)
			}
			assertNoCollectorLeak(t, err, "gamma")
		})
	}
	for _, class := range []coverageClass{coverageCRD, coverageDirect} {
		t.Run(fmt.Sprintf("%s/no overlap before any namespace is collected", class), func(t *testing.T) {
			kind, _ := setupCoverageRuleFixture(t, class)
			ctx := withRestrictedUser(t, "cold-coverage-reader", []string{"beta"})
			_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: kind, Context: "none"})
			if err == nil || !strings.HasPrefix(err.Error(), "radar_forbidden:") || !strings.Contains(err.Error(), `namespace "beta"`) {
				t.Fatalf("uncovered cold scope must name the collector denial, got %v", err)
			}
			assertNoCollectorLeak(t, err, "alpha", "gamma")
		})
	}
	t.Run("pinned scope is not named to a caller who cannot read it", func(t *testing.T) {
		setupFakeCacheForFilterTests(t)
		oldPin, oldTarget := k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
		k8s.ForceNamespaceScope = true
		k8s.SetNamespaceScopeOverride("alpha")
		t.Cleanup(func() { k8s.ForceNamespaceScope = oldPin; k8s.SetNamespaceScopeOverride(oldTarget) })
		ctx := withRestrictedUser(t, "pin-outsider", []string{"beta"})
		_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "pods"})
		if err == nil || !strings.HasPrefix(err.Error(), "user_no_namespace_access:") || strings.Contains(err.Error(), "alpha") {
			t.Fatalf("unscoped denial must not name the pinned namespace: %v", err)
		}
	})
}

// setupListCoverageFixture seeds one object, named "<namespace>-object", in
// each of objects for a kind of class, and a collector that reads the whole
// cluster or, when limited, only alpha and gamma.
func setupListCoverageFixture(t *testing.T, class coverageClass, limited bool, objects ...string) string {
	t.Helper()
	if class == coverageTyped {
		var objs []runtime.Object
		for _, ns := range objects {
			objs = append(objs, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: ns + "-object", Namespace: ns}})
		}
		client := fake.NewClientset(objs...)
		err := k8s.InitTestResourceCache(client)
		if limited {
			err = k8s.InitScopedTestResourceCacheNamespaces(client, map[string]k8score.ResourceScope{"services": {Enabled: true, Namespace: "alpha"}}, map[string][]string{"services": {"alpha", "gamma"}})
		}
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetTestState)
		return "services"
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}, ScopeCandidates: []string{"alpha", "gamma"}}))
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	listKind := "Widget"
	if class == coverageDirect {
		gvr = schema.GroupVersionResource{Group: "coordination.k8s.io", Version: "v1", Resource: "leases"}
		listKind = "Lease"
	}
	var objs []runtime.Object
	for _, ns := range objects {
		objs = append(objs, &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvr.GroupVersion().String(), "kind": listKind, "metadata": map[string]any{"name": ns + "-object", "namespace": ns}}})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: listKind + "List"}, objs...)
	if limited {
		dyn.PrependReactor("list", gvr.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
			if ns := action.GetNamespace(); ns != "alpha" && ns != "gamma" {
				return true, nil, collectorRefusal(gvr.GroupResource(), "list", ns)
			}
			return false, nil, nil
		})
	}
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: listKind, Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	return gvr.Resource
}

// setupLoadingServicesFixture watches Services in alpha and gamma, where alpha
// has synced and gamma never answers, with a Service in each of objects'
// namespaces: an unscoped read is narrowed to alpha and gamma and partial,
// with gamma still loading.
func setupLoadingServicesFixture(t *testing.T, objects ...string) {
	t.Helper()
	var objs []runtime.Object
	for _, ns := range objects {
		objs = append(objs, &corev1.Service{ObjectMeta: metav1.ObjectMeta{Name: ns + "-object", Namespace: ns}})
	}
	client := fake.NewClientset(objs...)
	client.PrependReactor("list", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "gamma" {
			return true, nil, errors.New("gamma does not answer")
		}
		return false, nil, nil
	})
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client:                  client,
		ResourceTypes:           map[string]bool{"services": true},
		ResourceScopes:          map[string]k8score.ResourceScope{"services": {Enabled: true, Namespace: "alpha"}},
		ResourceScopeNamespaces: map[string][]string{"services": {"alpha", "gamma"}},
		DeferredTypes:           map[string]bool{"services": true},
		DeferredSyncTimeout:     time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	deadline := time.Now().Add(5 * time.Second)
	for !k8s.GetResourceCache().AssessKindRead("services", []string{"alpha"}).Complete {
		if time.Now().After(deadline) {
			t.Fatal("alpha never synced")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestListSummaryBoundsNamespaceProse(t *testing.T) {
	for _, count := range []int{1, 3, resourceapi.MCPSetLimit, resourceapi.MCPSetLimit + 1, 40, 1000} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			read := make([]string, count)
			for i := range read {
				read[i] = fmt.Sprintf("team-%04d", count-i)
			}
			original := slices.Clone(read)
			gaps := []resourceapi.Gap{{Code: readreason.RadarNamespaceNotCollected, Namespaces: resourceapi.UnknownNames()}}
			coverage := listCoverageFor(false, readTarget{}, read, false, gaps)
			expected := slices.Clone(read)
			slices.Sort(expected)
			listed := count <= resourceapi.MCPSetLimit
			if !slices.Equal(read, original) || coverage.Read.Namespaces.Count != count || listed != (coverage.Read.Namespaces.Names != nil) ||
				(listed && !slices.Equal(coverage.Read.Namespaces.Names, expected)) {
				t.Fatalf("namespace data changed: input %v, coverage %+v", read, coverage.Read.Namespaces)
			}
			quoted := make([]string, len(expected))
			for i := range quoted {
				quoted[i] = fmt.Sprintf("%q", expected[i])
			}
			where := "namespace " + quoted[0]
			switch {
			case !listed:
				where = fmt.Sprintf("%d namespaces", count)
			case count > 1:
				where = "namespaces " + strings.Join(quoted, ", ")
			}
			want := fmt.Sprintf("Partial: 1 Service from %s. Unknown: some namespaces Radar can't name, because Radar doesn't collect services there (%s).", where, collectionRemedy("services"))
			if got := listSummary(context.Background(), "services", 1, "Service", coverage); got != want || coverage.Status != resourceapi.CoveragePartial {
				t.Fatalf("summary = %q, want %q", got, want)
			}
			// The agent can't tell whether a namespace outside the read was
			// read or skipped: the gap's extent is unknown, so naming one
			// settles it.
			wantNext := "To check one namespace, call list_resources again with namespace set to it."
			if got := listNext(coverage); got != wantNext {
				t.Fatalf("next = %q, want %q", got, wantNext)
			}
		})
	}
}

// An unrestricted, unscoped list asks for the whole cluster. When Radar's
// collector reads the kind only in some namespaces, the rows come with the
// namespaces actually read, including when they hold nothing; a complete read
// says it is complete.
func TestListResourcesReportsCollectorCoverage(t *testing.T) {
	for _, class := range []coverageClass{coverageTyped, coverageCRD, coverageDirect} {
		for _, tc := range []struct {
			name    string
			limited bool
			objects []string
			rows    []string
		}{
			{name: "complete", objects: []string{"alpha", "beta"}, rows: []string{"alpha-object", "beta-object"}},
			{name: "complete and empty", rows: []string{}},
			{name: "partial", limited: true, objects: []string{"alpha", "beta"}, rows: []string{"alpha-object"}},
			{name: "partial and empty", limited: true, objects: []string{"beta"}, rows: []string{}},
		} {
			for _, caller := range []string{"auth disabled", "cluster-wide"} {
				t.Run(fmt.Sprintf("%s/%s/%s", class, tc.name, caller), func(t *testing.T) {
					kind := setupListCoverageFixture(t, class, tc.limited, tc.objects...)
					ctx := context.Background()
					if caller == "cluster-wide" {
						ctx = withClusterAdmin(t, "coverage-lister")
					}
					result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: kind, Context: "none"})
					if err != nil {
						t.Fatal(err)
					}
					var rows []map[string]any
					if tc.limited {
						var coverage listWireCoverage
						rows, coverage = narrowedListBody(t, result)
						if !slices.Equal(coverage.NarrowedBy, []string{"namespace_collection"}) || !slices.Equal(coverage.Read, []string{"alpha", "gamma"}) || !slices.Equal(coverage.Scope, []string{"alpha", "gamma"}) {
							t.Fatalf("coverage = %+v, want narrowed by collection to alpha and gamma", coverage)
						}
						if want := fmt.Sprintf(`Complete within Radar's collection: %s in namespaces "alpha", "gamma". Other namespaces are unknown. Whoever runs Radar can widen its collection.`, countPhrase(len(rows), kind, onlyRowKindOf(rows))); coverage.Summary != want {
							t.Fatalf("summary must name the kind, the namespaces collected and who widens them, got %q", coverage.Summary)
						}
						assertNoCollectorLeak(t, errors.New(extractText(t, result)))
					} else {
						var coverage listWireCoverage
						rows, coverage = completeListBody(t, result)
						want := fmt.Sprintf("Complete: %s across every namespace.", countPhrase(len(rows), kind, onlyRowKindOf(rows)))
						if len(rows) == 0 {
							want = fmt.Sprintf("Complete: no %s in any namespace.", kind)
						}
						if coverage.ScopeType != "all_namespaces" || coverage.ReadType != "all_namespaces" || coverage.Summary != want {
							t.Fatalf("a complete unscoped list covers every namespace: %+v", coverage)
						}
					}
					if got := rowNames(rows); !slices.Equal(got, tc.rows) {
						t.Fatalf("rows = %v, want %v", got, tc.rows)
					}
				})
			}
		}
	}
}

// The partial-read result as a client receives it over MCP, pinned in the
// shared fixture that the agent-backend joins (internal/ai) and the Findings
// projection (web) are tested against.
const partialListFixturePath = "../../testdata/mcp/list-resources-partial-coverage.json"

type partialListFixture struct {
	Description string `json:"description"`
	Cases       []struct {
		Name      string          `json:"name"`
		Objects   []string        `json:"objects"`
		Arguments map[string]any  `json:"arguments"`
		Result    json.RawMessage `json:"result"`
	} `json:"cases"`
}

func TestListResourcesPartialCoverageTransport(t *testing.T) {
	raw, err := os.ReadFile(partialListFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	var fixture partialListFixture
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Cases) == 0 {
		t.Fatal("no cases in the partial-list fixture")
	}
	for _, tc := range fixture.Cases {
		t.Run(tc.Name, func(t *testing.T) {
			setupLoadingServicesFixture(t, tc.Objects...)
			session := connectTo(t, newServer(false, false, ""))
			result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "list_resources", Arguments: tc.Arguments})
			if err != nil {
				t.Fatal(err)
			}
			partialListBody(t, result)
			got, err := json.Marshal(result)
			if err != nil {
				t.Fatal(err)
			}
			var gotWire, wantWire any
			if err := json.Unmarshal(got, &gotWire); err != nil {
				t.Fatal(err)
			}
			// The fixture pins what Radar returns. Fields the SDK adds to every
			// result (_meta, resultType) change with protocol versions.
			if m, ok := gotWire.(map[string]any); ok {
				for k := range m {
					if k != "content" && k != "structuredContent" && k != "isError" {
						delete(m, k)
					}
				}
			}
			if err := json.Unmarshal(tc.Result, &wantWire); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(gotWire, wantWire) {
				t.Fatalf("the wire result no longer matches %s; update the fixture's result to:\n%s", partialListFixturePath, got)
			}

			// On the investigation mount the evidence middleware issues a ref
			// for exactly this text and prepends its marker as a block of its
			// own: the input internal/ai feeds every agent backend's parser.
			text := extractText(t, result)
			scope := strings.Repeat("a", 26)
			refs := investigationrefs.NewRegistry()
			lease, err := refs.Begin(scope)
			if err != nil {
				t.Fatal(err)
			}
			annotate := investigationEvidenceReferenceMiddleware(refs)(func(context.Context, string, mcpsdk.Request) (mcpsdk.Result, error) {
				return result, nil
			})
			annotated, err := annotate(context.WithValue(context.Background(), investigationEvidenceScopeKey{}, scope), "tools/call", nil)
			if err != nil {
				t.Fatal(err)
			}
			blocks := annotated.(*mcpsdk.CallToolResult).Content
			if len(blocks) != 2 {
				t.Fatalf("annotated result has %d blocks, want the marker and the producer text", len(blocks))
			}
			marker, _ := blocks[0].(*mcpsdk.TextContent)
			producer, _ := blocks[1].(*mcpsdk.TextContent)
			if marker == nil || producer == nil || producer.Text != text {
				t.Fatalf("annotated blocks = %#v, want a marker then the unchanged producer text", blocks)
			}
			_, ref := investigation.SplitRefMarker(marker.Text)
			if marker.Text != investigation.RefMarker(ref) || lease.Close()[ref] != text {
				t.Fatalf("marker %q must carry a ref issued for the producer text", marker.Text)
			}
		})
	}
}

// A caller who can read only beta gets the same refusal whether or not the
// collector already watches namespaces the caller cannot read.
func TestResourceReadsHiddenCoverageDoesNotChangeTheError(t *testing.T) {
	for _, verb := range []string{"list", "get"} {
		t.Run(verb, func(t *testing.T) {
			kind, prewarm := setupCoverageRuleFixture(t, coverageCRD)
			ctx := withRestrictedUser(t, "hidden-coverage-reader", []string{"beta"})
			read := func() string {
				var err error
				if verb == "list" {
					_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: kind, Context: "none"})
				} else {
					_, _, err = handleGetResource(ctx, nil, getResourceInput{Kind: kind, Namespace: "beta", Name: "beta-object"})
				}
				if err == nil || !strings.HasPrefix(err.Error(), "radar_forbidden:") {
					t.Fatalf("beta must be refused, got %v", err)
				}
				return err.Error()
			}
			cold := read()
			prewarm()
			if warm := read(); warm != cold {
				t.Fatalf("coverage the caller cannot read changed the error:\nbefore: %s\nafter:  %s", cold, warm)
			}
		})
	}
}

// Namespace informers sync independently, and in a shared Radar another user
// may have started a stalled one. A read of a namespace whose informer just
// started is still loading: its own informer decides, not the kind's oldest.
func TestResourceReadsDynamicStallIsPerNamespace(t *testing.T) {
	const stallAfter = 5 * time.Second
	previousWait := dynamicSyncWait
	dynamicSyncWait = 200 * time.Millisecond
	t.Cleanup(func() { dynamicSyncWait = previousWait })
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.URL.Path == "/apis/example.test/v1/widgets":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"kind":"Status","apiVersion":"v1","status":"Failure","reason":"Forbidden","code":403}`))
		case r.URL.Query().Get("limit") == "1":
			_, _ = w.Write([]byte(`{"kind":"WidgetList","apiVersion":"example.test/v1","metadata":{"resourceVersion":"1"},"items":[]}`))
		default:
			// Informer lists and watches never complete, so no informer syncs.
			<-r.Context().Done()
		}
	}))
	t.Cleanup(api.Close)
	client, err := dynamic.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(k8s.SetTestDynamicSyncStallAfter(stallAfter))
	t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}, ScopeCandidates: []string{"beta"}}))
	if err := k8s.InitTestDynamicResourceCache(client, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	// Beta is past stallAfter for both reads; alpha's informer, started by its
	// own read, is a fraction of stallAfter old when the read classifies it.
	time.Sleep(stallAfter)
	for _, tc := range []struct{ namespace, want string }{
		{"alpha", "kind_sync_pending:"},
		{"beta", "kind_sync_timed_out:"},
	} {
		_, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "Widget", Group: gvr.Group, Namespace: tc.namespace, Context: "none"})
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Fatalf("%s: want %s, got %v", tc.namespace, tc.want, err)
		}
	}
}

func TestResourceReadsColdDynamicKinds(t *testing.T) {
	kinds := []resourceid.Builtin{{Kind: "Widget", Resource: "widgets", Group: "example.test", Version: "v1", Namespaced: true}}
	for _, b := range resourceid.Builtins {
		if _, _, cluster := k8s.ClusterOnlyKindGVR(b.Resource); (cluster || b.Kind == "APIService") && !k8s.TypedKindOwnsGroup(b.Kind, b.Group) {
			kinds = append(kinds, b)
		}
	}
	for _, b := range kinds {
		for _, verb := range []string{"list", "get"} {
			t.Run(b.Kind+"/"+verb, func(t *testing.T) {
				if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(k8s.ResetTestState)
				version := b.Version
				if version == "" {
					version = "v1"
				}
				gvr := schema.GroupVersionResource{Group: b.Group, Version: version, Resource: b.Resource}
				obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvr.GroupVersion().String(), "kind": b.Kind, "metadata": map[string]any{"name": "present"}}}
				ns := ""
				if b.Namespaced {
					ns = "alpha"
					obj.SetNamespace(ns)
				}
				dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: b.Kind + "List"}, obj)
				if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: b.Group, Version: version, Kind: b.Kind, Name: b.Resource, Namespaced: b.Namespaced, Verbs: []string{"list", "watch", "get"}}}); err != nil {
					t.Fatal(err)
				}
				t.Cleanup(k8s.ResetTestDynamicState)
				if len(k8s.GetDynamicResourceCache().GetWatchedResources()) != 0 {
					t.Fatal("fixture must not prewarm informers")
				}
				var result *mcpsdk.CallToolResult
				var err error
				if verb == "list" {
					result, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: b.Kind, Group: b.Group, Namespace: ns, Context: "none"})
				} else {
					result, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: b.Kind, Group: b.Group, Namespace: ns, Name: "present", Context: "none"})
				}
				if err != nil {
					t.Fatalf("cold first %s: %v", verb, err)
				}
				if !containsName(extractText(t, result), "present") {
					t.Fatalf("cold read lost present object: %s", extractText(t, result))
				}
			})
		}
	}
}

func TestResourceReadsCollectorReasons(t *testing.T) {
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"nodes": {}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	for _, unauthorized := range []bool{false, true} {
		probeErrors := map[string]error{}
		want := "radar_forbidden:"
		if unauthorized {
			probeErrors["nodes"] = apierrors.NewUnauthorized("expired token")
			want = "radar_credentials_rejected:"
		}
		restore := k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}, Scopes: map[string]k8score.ResourceScope{"nodes": {}}, ProbeErrors: probeErrors})
		for _, verb := range []string{"list", "get"} {
			var err error
			if verb == "list" {
				_, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: "nodes"})
			} else {
				_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "nodes", Name: "x"})
			}
			if err == nil || !strings.HasPrefix(err.Error(), want) {
				t.Fatalf("%s: %v", verb, err)
			}
		}
		restore()
	}
	old := k8s.GetConnectionStatus()
	k8s.SetConnectionStatus(k8s.ConnectionStatus{State: k8s.StateConnecting})
	t.Cleanup(func() { k8s.SetConnectionStatus(old) })
	_, err := checkDynamicResourceRead(context.Background(), "widgets", "example.test", "alpha", "list", k8s.ErrDynamicNotReady)
	if err == nil || !strings.HasPrefix(err.Error(), "kind_sync_pending:") {
		t.Fatalf("connecting dynamic: %v", err)
	}
	_, err = checkDynamicResourceRead(context.Background(), "widgets", "example.test", "alpha", "list", apierrors.NewUnauthorized("expired token"))
	if err == nil || !strings.HasPrefix(err.Error(), "radar_credentials_rejected:") || strings.Contains(err.Error(), "forbidden") || strings.Contains(err.Error(), "access_denied") {
		t.Fatalf("collector credentials: %v", err)
	}
	for _, kind := range []string{"pods", "widgets"} {
		_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: kind, Name: "x"})
		want := "namespace_required:"
		if kind == "widgets" {
			want = "kind_sync_pending:"
		}
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("%s: %v", kind, err)
		}
	}
}

func TestResourceReadsNamespaceDenialAttribution(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	ctx := withRestrictedUser(t, "attribution-reader", []string{"alpha"})
	for _, kind := range []string{"pods", "widgets"} {
		_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: kind, Namespace: "beta"})
		for _, text := range []string{"user_namespace_not_visible:", "doesn't exist or you can't access it", "beta", "list pods or deployments", "about every 2 minutes"} {
			if err == nil || !strings.Contains(err.Error(), text) {
				t.Fatalf("missing %s: %v", text, err)
			}
		}
		if strings.Contains(err.Error(), "your role cannot list") || strings.Contains(err.Error(), "API group") {
			t.Fatalf("namespace membership cannot claim exact SAR: %v", err)
		}
		if next := readErrorNext(t, err); next != "Call list_namespaces to see the namespaces you can access." {
			t.Fatalf("a caller who named an unseen namespace is pointed at list_namespaces, got %q", next)
		}
	}
	// list_namespaces can't answer when Radar's identity can't list Namespaces.
	k8s.ResetResourceCache()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{k8score.Pods: {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "pods", Namespace: "beta"})
	if next := readErrorNext(t, err); next != "" {
		t.Fatalf("no dead-end hint without a namespace list, got %q", next)
	}
	err = resourcePermissionError(ctx, true, "list", "", "secrets", namespaceForError([]string{"alpha", "beta"}))
	if strings.Contains(err.Error(), "at cluster scope") || !strings.Contains(err.Error(), "beta") {
		t.Fatal(err)
	}
}

func TestResourceReadsDynamicCollectorIntersection(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	restore := k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}, NamespaceScoped: true, Namespace: "alpha", ScopeCandidates: []string{"alpha", "beta"}})
	t.Cleanup(restore)
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "alpha-widget", "namespace": "alpha"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, obj)
	dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() != "alpha" {
			return true, nil, collectorRefusal(gvr.GroupResource(), "list", action.GetNamespace())
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, allowed := range [][]string{nil, {"alpha"}} {
		ctx := withRestrictedUser(t, "dynamic-scoped-reader", allowed)
		result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "widgets", Context: "none"})
		if err != nil {
			t.Fatal(err)
		}
		body := extractText(t, result)
		if !containsName(body, "alpha-widget") {
			t.Fatalf("missing covered widget: %s", body)
		}
	}
	ctx := withRestrictedUser(t, "uncovered-reader", []string{"beta"})
	_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "widgets", Context: "none"})
	if err == nil || !strings.HasPrefix(err.Error(), "radar_forbidden:") {
		t.Fatalf("caller namespace denied by collector must not return empty success: %v", err)
	}
	assertNoCollectorLeak(t, err, "alpha")
	for _, verb := range []string{"list", "get"} {
		var err error
		if verb == "list" {
			_, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: "widgets", Namespace: "beta"})
		} else {
			_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "widgets", Namespace: "beta", Name: "x"})
		}
		if err == nil || !strings.HasPrefix(err.Error(), "radar_forbidden:") || !strings.Contains(err.Error(), `in namespace "alpha".`) {
			t.Fatalf("%s explicit uncovered scope: %v", verb, err)
		}
		assertNoCollectorLeak(t, err)
	}
}

func TestResourceReadsDynamicUnauthorizedPrefix(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, apierrors.NewUnauthorized("expired token")
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, verb := range []string{"list", "get"} {
		var err error
		if verb == "list" {
			_, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: "widgets", Namespace: "alpha"})
		} else {
			_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "widgets", Namespace: "alpha", Name: "x"})
		}
		if err == nil || !strings.HasPrefix(err.Error(), "radar_credentials_rejected:") || strings.Contains(err.Error(), "expired token") {
			t.Fatalf("%s credentials rejection: %v", verb, err)
		}
		if cause := collectorCause(err); cause == nil || !strings.Contains(cause.Error(), "expired token") {
			t.Fatalf("%s: API server response must stay available to the log: %v", verb, cause)
		}
	}
}

func TestResourceReadsDynamicUnauthorizedIsNotMissingCoverage(t *testing.T) {
	for _, tc := range []struct {
		name, rejectedNamespace, namespace string
	}{
		{"cluster rejection with readable fallback", "", ""},
		{"cluster rejection with readable requested namespace", "", "alpha"},
		{"namespace rejection with readable fallback", "beta", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}, ScopeCandidates: []string{"alpha", "beta"}}))
			gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
			dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
				switch action.GetNamespace() {
				case tc.rejectedNamespace:
					return true, nil, apierrors.NewUnauthorized(collectorIdentity + " token rejected")
				case "":
					return true, nil, collectorRefusal(gvr.GroupResource(), "list", "")
				default:
					return false, nil, nil
				}
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			_, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "widgets", Namespace: tc.namespace, Context: "none"})
			if err == nil || !strings.HasPrefix(err.Error(), "radar_credentials_rejected:") {
				t.Fatalf("credential rejection must fail the read even with readable namespaces: %v", err)
			}
			assertNoCollectorLeak(t, err)
		})
	}
}

func TestResourceReadsCollectorCoverageRespectsGroupCollision(t *testing.T) {
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"services": {Enabled: true, Namespace: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "serving.knative.dev", Version: "v1", Resource: "services"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "serving.knative.dev/v1", "kind": "Service", "metadata": map[string]any{"name": "beta-knative", "namespace": "beta"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ServiceList"}, obj)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Service", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "services", Group: gvr.Group, Context: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if !containsName(extractText(t, result), "beta-knative") {
		t.Fatalf("core Service coverage incorrectly narrowed Knative list: %s", extractText(t, result))
	}
}

func TestResourceReadsColdDynamicUnderNamespacePin(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	oldPin, oldTarget := k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("alpha")
	t.Cleanup(func() { k8s.ForceNamespaceScope = oldPin; k8s.SetNamespaceScopeOverride(oldTarget) })
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "alpha-widget", "namespace": "alpha"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, obj)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "widgets", Context: "none"})
	if err != nil {
		t.Fatal(err)
	}
	rows, coverage := completeListBody(t, result)
	if got := rowNames(rows); !slices.Equal(got, []string{"alpha-widget"}) || !slices.Equal(coverage.NarrowedBy, []string{"namespace_pin"}) {
		t.Fatalf("cold pinned list = %v (%+v), want alpha-widget narrowed by the pin", got, coverage)
	}
}

// A --namespace-scope pin narrows an unscoped read to the pinned namespace, so
// on a collector that covers only some namespaces it is still a complete read
// of its scope — but the list says the pin narrowed it, so nobody reads it as
// the cluster.
func TestListResourcesUnderNamespacePinIsNarrowed(t *testing.T) {
	for _, class := range []coverageClass{coverageTyped, coverageCRD, coverageDirect} {
		for _, caller := range []string{"auth disabled", "cluster-wide"} {
			t.Run(fmt.Sprintf("%s/%s", class, caller), func(t *testing.T) {
				kind := setupListCoverageFixture(t, class, true, "alpha", "beta")
				oldPin, oldTarget := k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
				k8s.ForceNamespaceScope = true
				k8s.SetNamespaceScopeOverride("alpha")
				t.Cleanup(func() { k8s.ForceNamespaceScope = oldPin; k8s.SetNamespaceScopeOverride(oldTarget) })
				ctx := context.Background()
				if caller == "cluster-wide" {
					ctx = withClusterAdmin(t, "pinned-lister")
				}
				result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: kind, Context: "none"})
				if err != nil {
					t.Fatal(err)
				}
				rows, coverage := completeListBody(t, result)
				if got := rowNames(rows); !slices.Equal(got, []string{"alpha-object"}) {
					t.Fatalf("pinned list = %v, want alpha-object", got)
				}
				if !slices.Equal(coverage.NarrowedBy, []string{"namespace_pin"}) || !slices.Equal(coverage.Scope, []string{"alpha"}) || !strings.HasPrefix(coverage.Summary, "Complete within Radar's --namespace-scope setting: ") {
					t.Fatalf("pinned list must say the pin narrowed it: %+v", coverage)
				}
			})
		}
	}
}

func TestResourceReadsRestrictedDirectLists(t *testing.T) {
	for _, b := range []resourceid.Builtin{
		{Kind: "Lease", Resource: "leases", Group: "coordination.k8s.io", Version: "v1"},
		{Kind: "EndpointSlice", Resource: "endpointslices", Group: "discovery.k8s.io", Version: "v1"},
		{Kind: "Endpoints", Resource: "endpoints", Version: "v1"},
	} {
		t.Run(b.Kind, func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			gvr := schema.GroupVersionResource{Group: b.Group, Version: b.Version, Resource: b.Resource}
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvr.GroupVersion().String(), "kind": b.Kind, "metadata": map[string]any{"name": "present", "namespace": "alpha"}}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: b.Kind + "List"}, obj)
			dyn.PrependReactor("list", b.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
				if action.GetNamespace() != "alpha" {
					return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("collector denied"))
				}
				return false, nil, nil
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: b.Group, Version: b.Version, Name: b.Resource, Kind: b.Kind, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			ctx := withRestrictedUser(t, "direct-reader", []string{"alpha"})
			result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: b.Resource, Context: "none"})
			if err != nil {
				t.Fatal(err)
			}
			if !containsName(extractText(t, result), "present") {
				t.Fatal("direct read lost object")
			}
			if len(k8s.GetDynamicResourceCache().GetWatchedResources()) != 0 {
				t.Fatal("direct reads must not start informers")
			}
			for _, action := range dyn.Actions() {
				if action.GetNamespace() != "alpha" {
					t.Fatalf("unexpected collector request: %+v", action)
				}
			}
		})
	}
}

// An unrestricted caller's unscoped read of a direct kind returns the
// namespaces the collector can read when it is refused cluster-wide, the same
// answer a custom resource gives from its fallback informers.
func TestResourceReadsUnrestrictedDirectListsFallBackToCandidates(t *testing.T) {
	for _, b := range []resourceid.Builtin{
		{Kind: "Lease", Resource: "leases", Group: "coordination.k8s.io", Version: "v1"},
		{Kind: "EndpointSlice", Resource: "endpointslices", Group: "discovery.k8s.io", Version: "v1"},
		{Kind: "Endpoints", Resource: "endpoints", Version: "v1"},
	} {
		for _, tc := range []struct {
			name       string
			candidates []string
			readable   []string // namespaces the collector may list; "" = cluster scope
			rows       []string
			wantErr    []string
		}{
			{name: "cluster scope readable", candidates: []string{"alpha"}, readable: []string{"", "alpha", "beta", "gamma"}, rows: []string{"alpha-object", "beta-object", "gamma-object"}},
			{name: "refused cluster-wide reads the candidates", candidates: []string{"alpha", "beta", "gamma"}, readable: []string{"alpha", "gamma"}, rows: []string{"alpha-object", "gamma-object"}},
			{name: "refused everywhere", candidates: []string{"alpha", "beta"}, wantErr: []string{`namespaces "alpha,beta"`}},
			{name: "refused cluster-wide with no candidates", wantErr: []string{"across the cluster"}},
		} {
			for _, caller := range []string{"auth disabled", "cluster-wide"} {
				t.Run(b.Kind+"/"+tc.name+"/"+caller, func(t *testing.T) {
					if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(k8s.ResetTestState)
					t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}, ScopeCandidates: tc.candidates}))
					gvr := schema.GroupVersionResource{Group: b.Group, Version: b.Version, Resource: b.Resource}
					var objs []runtime.Object
					for _, ns := range []string{"alpha", "beta", "gamma"} {
						objs = append(objs, &unstructured.Unstructured{Object: map[string]any{"apiVersion": gvr.GroupVersion().String(), "kind": b.Kind, "metadata": map[string]any{"name": ns + "-object", "namespace": ns}}})
					}
					dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: b.Kind + "List"}, objs...)
					dyn.PrependReactor("list", b.Resource, func(action ktesting.Action) (bool, runtime.Object, error) {
						if !slices.Contains(tc.readable, action.GetNamespace()) {
							return true, nil, collectorRefusal(gvr.GroupResource(), "list", action.GetNamespace())
						}
						return false, nil, nil
					})
					if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: b.Group, Version: b.Version, Name: b.Resource, Kind: b.Kind, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
						t.Fatal(err)
					}
					t.Cleanup(k8s.ResetTestDynamicState)
					ctx := context.Background()
					if caller == "cluster-wide" {
						ctx = withClusterAdmin(t, "direct-admin")
					}
					result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: b.Resource, Context: "none"})
					if tc.wantErr != nil {
						if err == nil || !strings.HasPrefix(err.Error(), "radar_forbidden:") {
							t.Fatalf("a read that covered nothing must fail, got %v", err)
						}
						for _, text := range tc.wantErr {
							if !strings.Contains(err.Error(), text) {
								t.Fatalf("missing %s: %v", text, err)
							}
						}
						assertNoCollectorLeak(t, err)
						return
					}
					if err != nil {
						t.Fatal(err)
					}
					body := extractText(t, result)
					for _, ns := range []string{"alpha", "beta", "gamma"} {
						if want := slices.Contains(tc.rows, ns+"-object"); containsName(body, ns+"-object") != want {
							t.Fatalf("%s-object present=%v, want %v: %s", ns, !want, want, body)
						}
					}
					if len(k8s.GetDynamicResourceCache().GetWatchedResources()) != 0 {
						t.Fatal("direct reads must not start informers")
					}
					if slices.Contains(tc.readable, "") && len(dyn.Actions()) != 1 {
						t.Fatalf("a readable cluster scope must be read once, got %d requests", len(dyn.Actions()))
					}
				})
			}
		}
	}
}

// Every collector refusal a caller can reach keeps the API server's response,
// which names Radar's identity, out of the tool error.
func TestCollectorRefusalsNeverEchoTheAPIServerResponse(t *testing.T) {
	t.Run("typed credential rejection", func(t *testing.T) {
		if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"nodes": {}, "pods": {Enabled: true, Namespace: "beta"}}); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetTestState)
		rejected := apierrors.NewUnauthorized(collectorIdentity + " token expired")
		t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{
			Perms:                &k8s.ResourcePermissions{Pods: true},
			Scopes:               map[string]k8score.ResourceScope{"nodes": {}, "pods": {Enabled: true, Namespace: "beta"}},
			ProbeErrors:          map[string]error{"nodes": rejected},
			NamespaceProbeErrors: map[string]map[string]error{"pods": {"alpha": rejected}},
		}))
		for _, input := range []listResourcesInput{{Kind: "nodes"}, {Kind: "pods", Namespace: "alpha"}} {
			_, _, err := handleListResources(context.Background(), nil, input)
			if err == nil || !strings.HasPrefix(err.Error(), "radar_credentials_rejected:") {
				t.Fatalf("%s: %v", input.Kind, err)
			}
			assertNoCollectorLeak(t, err)
		}
	})
	t.Run("diagnose", func(t *testing.T) {
		setupFakeCacheForFilterTests(t)
		var resources []k8s.APIResource
		listKinds := map[schema.GroupVersionResource]string{}
		for _, b := range []resourceid.Builtin{
			{Kind: "Rollout", Resource: "rollouts", Group: "argoproj.io", Version: "v1alpha1"},
			{Kind: "Application", Resource: "applications", Group: "argoproj.io", Version: "v1alpha1"},
		} {
			gvr := schema.GroupVersionResource{Group: b.Group, Version: b.Version, Resource: b.Resource}
			listKinds[gvr] = b.Kind + "List"
			resources = append(resources, k8s.APIResource{Group: b.Group, Version: b.Version, Name: b.Resource, Kind: b.Kind, Namespaced: true, Verbs: []string{"list", "watch", "get"}})
		}
		dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
		for _, verb := range []string{"list", "get"} {
			dyn.PrependReactor(verb, "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				return true, nil, collectorRefusal(action.GetResource().GroupResource(), verb, action.GetNamespace())
			})
		}
		if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(k8s.ResetTestDynamicState)
		for _, kind := range []string{"rollout", "application"} {
			_, _, err := handleDiagnose(context.Background(), nil, diagnoseInput{diagnoseCommonInput: diagnoseCommonInput{Kind: kind, Namespace: "alpha", Name: "web"}})
			if err == nil || !strings.HasPrefix(err.Error(), "radar_forbidden:") || strings.Contains(err.Error(), "object_not_found") {
				t.Fatalf("%s: a collector refusal is not an absent object: %v", kind, err)
			}
			assertNoCollectorLeak(t, err)
		}
	})
	t.Run("collector auth errors", func(t *testing.T) {
		setupFakeCacheForFilterTests(t)
		target := collectorTarget{resource: "pods", group: "metrics.k8s.io", action: "get"}
		for _, tc := range []struct {
			cause error
			want  string
		}{
			{fmt.Errorf("failed to get pod metrics: %w", collectorRefusal(schema.GroupResource{Group: "metrics.k8s.io", Resource: "pods"}, "get", "alpha")), "radar_forbidden: "},
			{apierrors.NewUnauthorized(collectorIdentity + " token expired"), "radar_credentials_rejected: "},
		} {
			err := collectorAuthError(context.Background(), tc.cause, target, "alpha")
			if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
				t.Fatalf("auth refusal not recognised as %q: %v", tc.want, err)
			}
			assertNoCollectorLeak(t, err)
		}
		if err := collectorAuthError(context.Background(), fmt.Errorf("metrics API unavailable"), target, "alpha"); err != nil {
			t.Fatalf("a non-auth failure is not a collector refusal: %v", err)
		}
	})
}

func TestResourceReadsDynamicCallerNamespaceOutsideCandidates(t *testing.T) {
	for _, prewarm := range []bool{false, true} {
		t.Run(fmt.Sprintf("prewarm=%v", prewarm), func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}, NamespaceScoped: true, Namespace: "alpha", ScopeCandidates: []string{"alpha"}}))
			gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "beta-widget", "namespace": "beta"}}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, obj)
			dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
				if action.GetNamespace() == "" || (!prewarm && action.GetNamespace() == "alpha") {
					return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("collector denied"))
				}
				return false, nil, nil
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			if prewarm {
				if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err != nil {
					t.Fatal(err)
				}
			}
			ctx := withRestrictedUser(t, "outside-reader", []string{"beta"})
			result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "widgets", Context: "none"})
			if err != nil {
				t.Fatal(err)
			}
			if !containsName(extractText(t, result), "beta-widget") || !k8s.GetDynamicResourceCache().IsNamespaceSynced(gvr, "beta") {
				t.Fatal("caller namespace was not read and synchronized")
			}
		})
	}
}

func TestResourceReadsNamespaceCollectorUnauthorized(t *testing.T) {
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "present", Namespace: "beta"}}), map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "beta"}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{Pods: true}, Scopes: map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "beta"}}, NamespaceProbeErrors: map[string]map[string]error{"pods": {"alpha": apierrors.NewUnauthorized("alpha rejected token")}}}))
	ctx := withRestrictedUser(t, "credential-reader", []string{"alpha", "beta"})
	for _, verb := range []string{"list", "get"} {
		var err error
		if verb == "list" {
			_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "pods", Namespace: "alpha"})
		} else {
			_, _, err = handleGetResource(ctx, nil, getResourceInput{Kind: "pods", Namespace: "alpha", Name: "present"})
		}
		if err == nil || !strings.HasPrefix(err.Error(), "radar_credentials_rejected:") || strings.Contains(err.Error(), "alpha rejected token") || !strings.Contains(err.Error(), "reconnect Radar") {
			t.Fatalf("%s: namespace credential rejection lost: %v", verb, err)
		}
		if cause := collectorCause(err); cause == nil || !strings.Contains(cause.Error(), "alpha rejected token") {
			t.Fatalf("%s: API server response must stay available to the log: %v", verb, cause)
		}
	}
	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "pods", Namespace: "beta", Context: "none"})
	if err != nil || !containsName(extractText(t, result), "present") {
		t.Fatalf("healthy namespace incorrectly denied: %v", err)
	}
	// An unscoped list still reads the namespaces Radar collects; the one
	// whose credentials were rejected is a gap, never silently omitted, and
	// its remedy is a reconnect, since a retry reads the same recorded result.
	for _, tc := range []struct {
		name       string
		ctx        context.Context
		narrowedBy []string
	}{
		{"restricted", ctx, []string{"user_access"}},
		{"auth disabled", context.Background(), []string{"namespace_collection"}},
		{"cluster-wide", withClusterAdmin(t, "credential-admin"), []string{"namespace_collection"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			result, _, err := handleListResources(tc.ctx, nil, listResourcesInput{Kind: "pods", Context: "none"})
			if err != nil {
				t.Fatalf("a rejected namespace failed the whole list: %v", err)
			}
			rows, coverage := listBody(t, result)
			if len(rows) != 1 || rows[0]["name"] != "present" {
				t.Fatalf("rows = %v, want beta's pod", rows)
			}
			if coverage.Status != "partial" || !slices.Equal(coverage.Codes, []string{"radar_credentials_rejected"}) ||
				!slices.Equal(coverage.GapNamespaces["radar_credentials_rejected"], []string{"alpha"}) ||
				!slices.Equal(coverage.Read, []string{"beta"}) || !slices.Equal(coverage.Scope, []string{"alpha", "beta"}) ||
				!slices.Equal(coverage.NarrowedBy, tc.narrowedBy) {
				t.Fatalf("coverage = %+v, want alpha a credentials gap beside beta's rows, narrowed by %v", coverage, tc.narrowedBy)
			}
			if !strings.Contains(coverage.Summary, "POST /api/connection/retry") || strings.Contains(coverage.Next, "Retry") || strings.Contains(coverage.Summary, "alpha rejected token") {
				t.Fatalf("summary %q / next %q: want the reconnect remedy, no retry, and no API server text", coverage.Summary, coverage.Next)
			}
			assertNoCollectorLeak(t, errors.New(coverage.Summary))
		})
	}
	// A user who can't see alpha gets a complete answer that never names it.
	betaOnly := withRestrictedUser(t, "beta-reader", []string{"beta"})
	result, _, err = handleListResources(betaOnly, nil, listResourcesInput{Kind: "pods", Context: "none"})
	if err != nil {
		t.Fatal(err)
	}
	if _, coverage := listBody(t, result); coverage.Status != "complete" || len(coverage.Codes) != 0 || strings.Contains(coverage.Summary, "alpha") {
		t.Fatalf("coverage = %+v, want complete and alpha never named", coverage)
	}
}

func TestDiagnoseRejectsUnobservedTypedTargets(t *testing.T) {
	for _, pending := range []bool{false, true} {
		t.Run(fmt.Sprintf("pending=%v", pending), func(t *testing.T) {
			client := fake.NewClientset(&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "present", Namespace: "alpha"}})
			var err error
			want := "radar_namespace_not_collected:"
			if pending {
				err = k8s.InitTestPromotedSyncingCache(client, 5*time.Second, time.Minute, map[string]time.Duration{"pods": time.Hour})
				want = "kind_sync_pending:"
			} else {
				err = k8s.InitScopedTestResourceCache(client, map[string]k8score.ResourceScope{"pods": {Enabled: true, Namespace: "beta"}})
			}
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			_, _, err = handleDiagnose(context.Background(), nil, testDiagnoseInput("pod", "alpha", "present"))
			if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "not found") {
				t.Fatalf("unobserved target must not be declared absent, want %s, got %v", want, err)
			}
		})
	}
}

func TestDiagnoseRejectsUnsyncedDynamicTargets(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	previousWait := dynamicSyncWait
	dynamicSyncWait = 20 * time.Millisecond
	t.Cleanup(func() { dynamicSyncWait = previousWait })
	listKinds := map[schema.GroupVersionResource]string{}
	var resources []k8s.APIResource
	for _, b := range []resourceid.Builtin{
		{Kind: "Rollout", Resource: "rollouts", Group: "argoproj.io", Version: "v1alpha1"},
		{Kind: "Application", Resource: "applications", Group: "argoproj.io", Version: "v1alpha1"},
	} {
		gvr := schema.GroupVersionResource{Group: b.Group, Version: b.Version, Resource: b.Resource}
		listKinds[gvr] = b.Kind + "List"
		resources = append(resources, k8s.APIResource{Group: b.Group, Version: b.Version, Name: b.Resource, Kind: b.Kind, Namespaced: true, Verbs: []string{"list", "watch", "get"}})
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), listKinds)
	dyn.PrependReactor("list", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions().Limit == 1 {
			return false, nil, nil
		}
		return true, nil, fmt.Errorf("initial list unavailable")
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, resources); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, kind := range []string{"rollout", "application"} {
		t.Run(kind, func(t *testing.T) {
			_, _, err := handleDiagnose(context.Background(), nil, testDiagnoseInput(kind, "alpha", "present"))
			if err == nil || !strings.HasPrefix(err.Error(), "kind_sync_pending:") || strings.Contains(err.Error(), "not found") {
				t.Fatalf("unsynced target must not be declared absent, got %v", err)
			}
		})
	}
}

func TestReadDynamicScopeRereadsOnlyBeforeSync(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "present", "namespace": "alpha"}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, obj)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	cache := k8s.GetResourceCache()
	for _, want := range []struct {
		state string
		reads int
	}{{"cold", 2}, {"synced", 1}} {
		reads := 0
		items, err := readDynamicScope(context.Background(), "widgets", gvr.Group, "alpha", "list", func() ([]*unstructured.Unstructured, error) {
			reads++
			return cache.ListDynamicWithGroup(context.Background(), "widgets", "alpha", gvr.Group)
		})
		if err != nil || len(items) != 1 {
			t.Fatalf("%s read: %v %v", want.state, items, err)
		}
		if reads != want.reads {
			t.Fatalf("%s scope read the store %d times, want %d", want.state, reads, want.reads)
		}
	}
}

func TestResourceReadErrorDetails(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	for _, local := range []bool{false, true} {
		restore := k8s.SetTestLocalMode()
		previousInCluster := k8s.ForceInCluster
		k8s.ForceInCluster = !local
		err := collectorUnauthorizedError("widgets", apierrors.NewUnauthorized("rejected"))
		want := "service-account credentials"
		if local {
			want = "kubeconfig credentials"
		}
		if !strings.Contains(err.Error(), want) || !strings.Contains(err.Error(), "widgets") {
			t.Fatal(err)
		}
		k8s.ForceInCluster = previousInCluster
		restore()
	}
	for _, verb := range []string{"list", "get"} {
		_, err := checkDynamicResourceRead(context.Background(), "widgets", "example.test", "alpha", verb, fmt.Errorf("transport unavailable"))
		if err == nil || !strings.HasPrefix(err.Error(), "read_error: Radar failed to "+verb+" widgets") {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	err := resourceGetError(context.Background(), fmt.Errorf("store unavailable"), "pods", "alpha", "present")
	if !strings.HasPrefix(err.Error(), "read_error: Radar failed to get pods alpha/present") {
		t.Fatal(err)
	}
	namespaces := make([]string, 15)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("ns-%02d", i)
	}
	ctx := withRestrictedUser(t, "secret-denial-reader", namespaces)
	seedSecretListCanI(t, "secret-denial-reader", nil, namespaces)
	_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "secrets"})
	if err == nil || !strings.Contains(err.Error(), "ns-00,ns-01,ns-02 and 12 more") || strings.Contains(err.Error(), "ns-03") {
		t.Fatalf("Secret namespace denial must be bounded: %v", err)
	}
	// Denied everywhere it could look, the list still says where that was.
	result, ok := readErrorResult(err)
	if !ok {
		t.Fatal(err)
	}
	body := result.StructuredContent.(mcpReadError)
	if body.ErrorCode != readreason.UserForbidden || body.Coverage == nil || body.Coverage.Read != nil || len(body.Coverage.Gaps) != 1 ||
		body.Coverage.Gaps[0].Code != readreason.UserForbidden || body.Coverage.Gaps[0].Namespaces.Count != 15 {
		t.Fatalf("an exact-access failure says where in words and as coverage: %+v", body)
	}
}

// A list that read none of the namespaces Radar collects reports that it was
// narrowed to them: the scope is those namespaces, and every one of them is a
// gap.
func TestFailedListKeepsCollectionScope(t *testing.T) {
	client := fake.NewClientset()
	client.PrependReactor("list", "services", func(action ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("does not answer")
	})
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client:                  client,
		ResourceTypes:           map[string]bool{"services": true},
		ResourceScopes:          map[string]k8score.ResourceScope{"services": {Enabled: true, Namespace: "alpha"}},
		ResourceScopeNamespaces: map[string][]string{"services": {"alpha", "gamma"}},
		DeferredTypes:           map[string]bool{"services": true},
		DeferredSyncTimeout:     time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	_, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "services", Context: "none"})
	result, ok := readErrorResult(err)
	if !ok {
		t.Fatalf("want a read failure, got %v", err)
	}
	body := result.StructuredContent.(mcpReadError)
	if body.ErrorCode != readreason.KindSyncPending || body.Coverage == nil || !slices.Equal(body.Coverage.NarrowedBy, []resourceapi.Narrowing{resourceapi.NarrowedByNamespaceCollection}) ||
		!strings.Contains(body.Error, `Other namespaces are unknown: Radar collects services only in namespaces "alpha", "gamma"`) {
		t.Fatalf("the error must say the list was narrowed to what Radar collects: %+v", body)
	}
	if body.Next != `Retry in a few seconds to read namespaces "alpha", "gamma".` {
		t.Fatalf("next = %q", body.Next)
	}
}

func TestResourceReadsNoNamespaceKindResolution(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	if err := k8s.InitTestDynamicResourceCache(dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}), []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, tc := range []struct{ kind, group, want string }{
		{"deployments", "apps", "namespace_required:"},
		{"widgets", "example.test", "namespace_required:"},
		{"widgets", "", "namespace_required:"},
		{"unknown", "", "kind_unknown:"},
		{"deployments", "wrong.group", "kind_unknown:"},
	} {
		_, _, err := handleGetResource(context.Background(), nil, getResourceInput{Kind: tc.kind, Group: tc.group, Name: "present"})
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Fatalf("%s/%s: %v", tc.kind, tc.group, err)
		}
	}
	k8s.ResetTestDynamicState()
	// Without discovery, a connecting Radar is still loading; a connected one
	// failed to start it, which says nothing about Radar's access.
	for state, want := range map[k8s.ConnectionState]string{k8s.StateConnecting: "kind_sync_pending:", k8s.StateConnected: "read_error:"} {
		k8s.SetConnectionStatus(k8s.ConnectionStatus{State: state})
		_, _, err := handleGetResource(context.Background(), nil, getResourceInput{Kind: "widgets", Name: "present"})
		if err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Fatalf("%s without discovery: %v", state, err)
		}
		_, err = checkDynamicResourceRead(context.Background(), "widgets", "example.test", "alpha", "list", k8s.ErrDynamicNotReady)
		if err == nil || !strings.HasPrefix(err.Error(), want) || strings.Contains(err.Error(), "list/watch") {
			t.Fatalf("%s dynamic read without discovery: %v", state, err)
		}
	}
}

// A caller who can see every namespace is told a named namespace doesn't
// exist, rather than that it's empty. A caller limited to some namespaces is
// not told which, and without a synced Namespace informer Radar can't tell.
func TestResourceReadsNamespaceNotFound(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	for name, ctx := range map[string]context.Context{"local": context.Background(), "cluster admin": withClusterAdmin(t, "admin")} {
		for _, verb := range []string{"list", "get"} {
			var err error
			if verb == "list" {
				_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "pods", Namespace: "typo"})
			} else {
				_, _, err = handleGetResource(ctx, nil, getResourceInput{Kind: "pods", Namespace: "typo", Name: "web"})
			}
			var re *readError
			if !errors.As(err, &re) || re.code != readreason.NamespaceNotFound || !strings.Contains(err.Error(), `Namespace "typo" doesn't exist`) || re.next == "" {
				t.Fatalf("%s %s: %v", name, verb, err)
			}
		}
	}
	if _, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "pods", Namespace: "beta"}); err != nil {
		t.Fatalf("existing namespace: %v", err)
	}
	_, _, err := handleListResources(withRestrictedUser(t, "dev", []string{"alpha"}), nil, listResourcesInput{Kind: "pods", Namespace: "typo"})
	if err == nil || !strings.HasPrefix(err.Error(), "user_namespace_not_visible:") {
		t.Fatalf("restricted caller: %v", err)
	}

	k8s.ResetTestState()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"pods": {Enabled: true}}); err != nil {
		t.Fatal(err)
	}
	if k8s.GetResourceCache().Namespaces() != nil {
		t.Fatal("fixture must not collect namespaces")
	}
	if _, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "pods", Namespace: "typo"}); err != nil {
		t.Fatalf("without a namespace informer: %v", err)
	}
}

func TestResourceReadsNonWatchableKindIsNotSupported(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "reports"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "ReportList"})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Report", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	for _, ns := range []string{"alpha", ""} {
		_, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "reports", Group: "example.test", Namespace: ns})
		if err == nil || !strings.HasPrefix(err.Error(), "kind_not_watchable: Radar can't collect reports: its API doesn't support both list and watch") {
			t.Fatalf("list ns=%q: %v", ns, err)
		}
	}
	_, _, err := handleGetResource(context.Background(), nil, getResourceInput{Kind: "reports", Group: "example.test", Namespace: "alpha", Name: "x"})
	if err == nil || !strings.HasPrefix(err.Error(), "kind_not_watchable: ") {
		t.Fatalf("get: %v", err)
	}
}

func TestReadDynamicScopeRejectsChangedHandlesBeforeAbsence(t *testing.T) {
	setupListCoverageFixture(t, coverageCRD, false)
	_, err := readDynamicScope(context.Background(), "widgets", "example.test", "alpha", "get", func() (*unstructured.Unstructured, error) {
		k8s.ResetTestDynamicState()
		return nil, k8score.ErrResourceNotFound
	})
	if err == nil || !strings.HasPrefix(err.Error(), "connection_changed:") || isObjectNotFound(err) {
		t.Fatalf("changed cluster produced authoritative absence: %v", err)
	}
}

func TestListDynamicResourcesRejectsRetiredCacheHandle(t *testing.T) {
	setupListCoverageFixture(t, coverageCRD, false)
	retired := k8s.GetResourceCache()
	k8s.ResetResourceCache()
	target := readTarget{namespaces: []string{"alpha", "beta"}}
	_, _, err := listDynamicResources(context.Background(), retired, "widgets", "example.test", target, target.namespaces, gapSet{}, false, "none")
	if err == nil || !strings.HasPrefix(err.Error(), "connection_changed:") {
		t.Fatalf("retired cache served a multi-namespace read: %v", err)
	}
}

func TestGetResourceRejectsExtrasFetchedAfterASwitch(t *testing.T) {
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	dep := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "web", UID: "web-uid"}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces(dep)...)); err != nil {
		t.Fatal(err)
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(),
		map[schema.GroupVersionResource]string{{Group: "apps", Version: "v1", Resource: "replicasets"}: "ReplicaSetList"},
		&unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "apps/v1", "kind": "Deployment",
			"metadata": map[string]any{"namespace": "alpha", "name": "web", "uid": "web-uid"},
		}})
	dyn.PrependReactor("list", "replicasets", func(ktesting.Action) (bool, runtime.Object, error) {
		k8s.ResetResourceCache()
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{
		{Group: "apps", Version: "v1", Kind: "Deployment", Name: "deployments", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
		{Group: "apps", Version: "v1", Kind: "ReplicaSet", Name: "replicasets", Namespaced: true, Verbs: []string{"get", "list", "watch"}},
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)

	_, _, err := handleGetResource(context.Background(), nil, getResourceInput{Kind: "deployments", Namespace: "alpha", Name: "web", Include: "revisions", Context: "none"})
	if err == nil || !strings.HasPrefix(err.Error(), "connection_changed:") {
		t.Fatalf("revisions read after a cluster switch were returned with the previous cluster's resource: %v", err)
	}
}

func TestTypedReadRejectsRetainedPreviousCache(t *testing.T) {
	k8s.ResetTestState()
	t.Cleanup(k8s.ResetTestState)
	oldPod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "old"}}
	if err := k8s.InitTestResourceCache(fake.NewClientset(oldPod)); err != nil {
		t.Fatal(err)
	}
	previous := k8s.GetResourceCache()
	if !resourceReadCacheIsCurrent(previous) {
		t.Fatal("current cache rejected")
	}
	k8s.ResetResourceCache()
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	if _, err := k8s.FetchResource(previous, "pods", "alpha", "old"); err != nil {
		t.Fatalf("retained old cache no longer serves its rows: %v", err)
	}
	if resourceReadCacheIsCurrent(previous) || !resourceReadCacheIsCurrent(k8s.GetResourceCache()) {
		t.Fatal("cache generation replacement was not detected")
	}
}

// A built-in kind Radar watches namespace by namespace is readable in a
// namespace whose own informer has synced while another namespace is still
// loading: the loading namespace is a gap, not a reason to refuse the read.
func TestResourceReadsTypedReadinessIsPerNamespace(t *testing.T) {
	client := fake.NewClientset(
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "alpha", Name: "alpha-cm"}},
		&corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "gamma", Name: "gamma-cm"}},
	)
	client.PrependReactor("list", "configmaps", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() == "gamma" {
			return true, nil, errors.New("gamma does not answer")
		}
		return false, nil, nil
	})
	if err := k8s.InitTestResourceCacheFromConfig(k8score.CacheConfig{
		Client:                  client,
		ResourceTypes:           map[string]bool{"configmaps": true},
		ResourceScopes:          map[string]k8score.ResourceScope{"configmaps": {Enabled: true, Namespace: "alpha"}},
		ResourceScopeNamespaces: map[string][]string{"configmaps": {"alpha", "gamma"}},
		DeferredTypes:           map[string]bool{"configmaps": true},
		DeferredSyncTimeout:     time.Hour,
	}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	deadline := time.Now().Add(5 * time.Second)
	for !k8s.GetResourceCache().AssessKindRead("configmaps", []string{"alpha"}).Complete {
		if time.Now().After(deadline) {
			t.Fatal("alpha never synced")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if k8s.GetResourceCache().ConfigMaps() != nil {
		t.Fatal("fixture must leave the kind as a whole loading")
	}
	ctx := context.Background()

	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "configmaps", Namespace: "alpha", Context: "none"})
	if err != nil {
		t.Fatalf("alpha has synced, so a read of it must succeed: %v", err)
	}
	if rows, _ := completeListBody(t, result); !slices.Equal(rowNames(rows), []string{"alpha-cm"}) {
		t.Fatalf("alpha rows = %v", rowNames(rows))
	}

	result, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "configmaps", Context: "none"})
	if err != nil {
		t.Fatal(err)
	}
	rows, coverage := partialListBody(t, result)
	if !slices.Equal(rowNames(rows), []string{"alpha-cm"}) || !slices.Equal(coverage.Read, []string{"alpha"}) ||
		!slices.Equal(coverage.GapNamespaces["kind_sync_pending"], []string{"gamma"}) || !slices.Equal(coverage.Codes, []string{"kind_sync_pending"}) ||
		!slices.Equal(coverage.NarrowedBy, []string{"namespace_collection"}) || !slices.Equal(coverage.Scope, []string{"alpha", "gamma"}) {
		t.Fatalf("unscoped read while gamma loads = %v %+v", rowNames(rows), coverage)
	}

	if _, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "configmaps", Namespace: "alpha", Name: "alpha-cm", Context: "none"}); err != nil {
		t.Fatalf("get in synced alpha: %v", err)
	}
	if _, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "configmaps", Namespace: "alpha", Name: "missing", Context: "none"}); err == nil || !strings.HasPrefix(err.Error(), "object_not_found:") {
		t.Fatalf("a miss in synced alpha is a confirmed absence: %v", err)
	}
	if _, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: "configmaps", Namespace: "gamma", Name: "gamma-cm", Context: "none"}); err == nil || !strings.HasPrefix(err.Error(), "kind_sync_pending:") {
		t.Fatalf("a get in loading gamma must say it is loading: %v", err)
	}
	if _, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "configmaps", Namespace: "gamma", Context: "none"}); err == nil || !strings.HasPrefix(err.Error(), "kind_sync_pending:") {
		t.Fatalf("a list of only loading gamma read nothing: %v", err)
	}
}

// Every way Radar can be without a cluster answers with a code that marks the
// whole cluster as failing; the sentence says whether to wait or to fix
// credentials. A confirmed credential rejection is the connection's, not one
// read's, so it is not radar_credentials_rejected.
func TestNotConnectedErrorsCarryCodes(t *testing.T) {
	for _, tc := range []struct {
		status k8s.ConnectionStatus
		want   readreason.Code
	}{
		{k8s.ConnectionStatus{State: k8s.StateConnecting}, readreason.ClusterConnecting},
		{k8s.ConnectionStatus{State: k8s.StateDisconnected, Error: "401", ErrorType: "auth-rejected"}, readreason.ClusterDisconnected},
		{k8s.ConnectionStatus{State: k8s.StateDisconnected, Error: "plugin hung", ErrorType: "auth-plugin-stuck"}, readreason.ClusterDisconnected},
		{k8s.ConnectionStatus{State: k8s.StateDisconnected, Error: "dial tcp: refused"}, readreason.ClusterDisconnected},
	} {
		restore := k8s.SetTestConnectionStatus(tc.status)
		err := errNotConnected()
		restore()
		var re *readError
		if !errors.As(err, &re) || re.code != tc.want || !readreason.AffectsWholeCluster(re.code) {
			t.Fatalf("%+v: got %v, want %s", tc.status, err, tc.want)
		}
	}
}

// readErrorNext is the next an MCP client gets with a read failure.
func readErrorNext(t *testing.T, err error) string {
	t.Helper()
	result, ok := readErrorResult(err)
	if !ok {
		t.Fatalf("not a read failure: %v", err)
	}
	return result.StructuredContent.(mcpReadError).Next
}

// A list that read nothing can fail for several reasons at once; the code
// names the most actionable, and next still offers the retry that can recover
// another part.
// A list whose namespaces failed for different reasons names every cause in
// its error as well as in its coverage; its code is the most
// actionable one and next still covers the cause that clears by itself.
func TestFailedListNamesEveryCause(t *testing.T) {
	var gaps gapSet
	gaps.add(readreason.UserForbidden, errors.New("denied"), "alpha")
	gaps.add(readreason.KindSyncTimedOut, errors.New("slow"), "beta")
	err := withListCoverage(context.Background(), gaps.failure(), "secrets", readTarget{namespaces: []string{"alpha", "beta"}, explicit: true}, false)
	result, ok := readErrorResult(err)
	if !ok {
		t.Fatal(err)
	}
	body := result.StructuredContent.(mcpReadError)
	for _, want := range []string{
		`Unknown: namespace "alpha", because your Kubernetes access doesn't allow listing secrets there`,
		`Unknown: namespace "beta", because Radar's load of secrets there didn't finish in time`,
	} {
		if !strings.Contains(body.Error, want) {
			t.Fatalf("missing %q in %q", want, body.Error)
		}
	}
	if body.ErrorCode != readreason.UserForbidden || body.Next != `Retry in a minute to read namespace "beta".` {
		t.Fatalf("body = %+v", body)
	}
	// error_code names only the most actionable cause; coverage keeps the
	// other one, and where each applies, for a program to act on.
	raw, _ := json.Marshal(body.Coverage)
	if want := `{"status":"partial","scope":{"type":"namespaces","namespaces":{"count":2,"names":["alpha","beta"]}},"gaps":[{"code":"user_forbidden","action":"user_access","namespaces":{"count":1,"names":["alpha"]}},{"code":"kind_sync_timed_out","action":"wait","namespaces":{"count":1,"names":["beta"]}}]}`; string(raw) != want {
		t.Fatalf("coverage = %s, want %s", raw, want)
	}
}

func TestFailedListNextComesFromEveryGap(t *testing.T) {
	cov := &resourceapi.Coverage{Status: resourceapi.CoveragePartial, Scope: resourceapi.Scope{Type: resourceapi.ScopeNamespaces, Namespaces: resourceapi.Names([]string{"alpha", "beta"}, resourceapi.MCPSetLimit)},
		Gaps: []resourceapi.Gap{
			{Code: readreason.UserForbidden, Namespaces: resourceapi.Names([]string{"alpha"}, resourceapi.MCPSetLimit)},
			{Code: readreason.KindSyncTimedOut, Namespaces: resourceapi.Names([]string{"beta"}, resourceapi.MCPSetLimit)},
		}}
	err := &readError{code: readreason.UserForbidden, err: errors.New("denied"), coverage: cov}
	if next := readErrorNext(t, err); next != `Retry in a minute to read namespace "beta".` {
		t.Fatalf("next = %q", next)
	}
}

func TestDenialNamesRadarCloudOwner(t *testing.T) {
	as := func(groups ...string) context.Context {
		return pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "someone", Groups: groups})
	}
	const owner = "an owner of your Radar Cloud organization can raise your"
	const may = " role, which may grant it"
	for _, tc := range []struct {
		name  string
		cloud string
		ctx   context.Context
		want  string
	}{
		{"local", "false", as("radar:viewer"), ""},
		{"viewer", "true", as("radar:viewer"), owner + " viewer" + may},
		{"member", "true", as("cloud:member"), owner + " member" + may},
		{"owner", "true", as("radar:owner"), ""},
		{"no role", "true", as("radar:ai:reader"), ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("RADAR_CLOUD_MODE", tc.cloud)
			msg := resourcePermissionError(tc.ctx, true, "list", "", "secrets", "beta").Error()
			_, who := gapReason(tc.ctx, "secrets", readreason.UserForbidden)
			for _, got := range []string{msg, who} {
				if tc.want == "" {
					if strings.Contains(got, "Radar Cloud") {
						t.Fatalf("no Radar Cloud remedy expected, got %q", got)
					}
					continue
				}
				if !strings.Contains(got, "a cluster admin can grant") && !strings.Contains(got, "A cluster admin can grant") {
					t.Fatalf("the cluster admin stays a remedy, got %q", got)
				}
				if !strings.Contains(got, tc.want) {
					t.Fatalf("want %q in %q", tc.want, got)
				}
			}
		})
	}
}

// Through list_resources: a Radar Cloud viewer reads the owner remedy where
// their own access denied Secrets, and nowhere a retry or Radar's own
// collection is the fix.
func TestListNamesRadarCloudOwnerOnlyForDenials(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	t.Setenv("RADAR_CLOUD_MODE", "true")
	const remedy = "an owner of your Radar Cloud organization can raise your viewer role, which may grant it"
	viewer := func(name string, canI map[string]bool) context.Context {
		groups := []string{"radar:viewer"}
		perms := &pkgauth.UserPermissions{AllowedNamespaces: []string{"alpha", "beta"}}
		for ns, ok := range canI {
			perms.SetCanI("list", "", "secrets", ns, ok)
		}
		getPermCache().Set(name, groups, perms)
		return pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: name, Groups: groups})
	}

	denied := viewer("cloud-viewer", map[string]bool{"alpha": true, "beta": false})
	result, _, err := handleListResources(denied, nil, listResourcesInput{Kind: "secrets"})
	if err != nil {
		t.Fatal(err)
	}
	if body := extractText(t, result); !strings.Contains(body, remedy) {
		t.Fatalf("a partial list denied by the viewer's access names the owner: %s", body)
	}
	_, _, err = handleListResources(denied, nil, listResourcesInput{Kind: "secrets", Namespace: "beta"})
	if err == nil || !strings.Contains(err.Error(), remedy) {
		t.Fatalf("a denied list names the owner: %v", err)
	}

	unchecked := viewer("cloud-viewer-unchecked", map[string]bool{"alpha": true})
	result, _, err = handleListResources(unchecked, nil, listResourcesInput{Kind: "secrets"})
	if err != nil {
		t.Fatal(err)
	}
	if body := extractText(t, result); strings.Contains(body, "Radar Cloud") || !strings.Contains(body, "user_permission_check_failed") {
		t.Fatalf("a failed permission check is retried, not escalated to an owner: %s", body)
	}
	for _, code := range []readreason.Code{readreason.RadarNamespaceNotCollected, readreason.RadarForbidden, readreason.KindSyncPending} {
		if why, who := gapReason(denied, "secrets", code); strings.Contains(why+who, "Radar Cloud") {
			t.Fatalf("%s isn't the caller's access to widen: %q / %q", code, why, who)
		}
	}
}

// fixtureNamespaces returns objects plus a Namespace for each namespace the
// resource-read fixtures place objects in, as a real cluster has one: Radar
// reads a namespace its synced Namespace informer lacks as nonexistent.
func fixtureNamespaces(objects ...runtime.Object) []runtime.Object {
	for _, name := range []string{"alpha", "beta", "gamma", "shop", "kube-system"} {
		objects = append(objects, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: name}})
	}
	return objects
}

// Discovery that failed for an API group can't rule a kind out: a get naming
// that group says the API isn't answering, and an unqualified kind says it may
// exist behind one, instead of suggesting a misspelling.
func TestKindUnknownWithPartialDiscovery(t *testing.T) {
	setupFakeCacheForFilterTests(t)
	fd := fake.NewClientset().Discovery().(*fakediscovery.FakeDiscovery)
	fd.Resources = []*metav1.APIResourceList{{GroupVersion: "example.test/v1", APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Namespaced: true, Verbs: metav1.Verbs{"get", "list", "watch"}}}}}
	fd.PrependReactor("get", "resource", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, &discovery.ErrGroupDiscoveryFailed{Groups: map[schema.GroupVersion]error{{Group: "broken.test", Version: "v1"}: apierrors.NewServiceUnavailable("aggregated API down")}}
	})
	restore, err := k8s.SetTestResourceDiscovery(fd)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore)
	for _, tc := range []struct{ kind, group, want, mention string }{
		{"gadgets", "broken.test", "read_error:", `API group "broken.test"`},
		{"gadgets", "", "kind_unknown:", "may exist behind an API that isn't answering"},
		{"gadgets", "fine.test", "kind_unknown:", ""},
	} {
		_, _, err := handleGetResource(context.Background(), nil, getResourceInput{Kind: tc.kind, Group: tc.group, Name: "x"})
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) || !strings.Contains(err.Error(), tc.mention) {
			t.Fatalf("%s/%s: %v", tc.kind, tc.group, err)
		}
		if tc.group == "fine.test" && strings.Contains(err.Error(), "isn't answering") {
			t.Fatalf("a group that discovered fine must not be hedged: %v", err)
		}
	}

	// A refresh that returns nothing leaves the kinds held out of date, so a
	// miss is a failed read to retry, until a good refresh clears it.
	healthy := fake.NewClientset().Discovery().(*fakediscovery.FakeDiscovery)
	healthy.Resources = fd.Resources
	failing := true
	healthy.PrependReactor("get", "group", func(ktesting.Action) (bool, runtime.Object, error) {
		if failing {
			return true, nil, errors.New("discovery unavailable")
		}
		return false, nil, nil
	})
	failing = false
	restore2, err := k8s.SetTestResourceDiscovery(healthy)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(restore2)
	failing = true
	if err := k8s.GetResourceDiscovery().Refresh(); err == nil {
		t.Fatal("refresh should fail")
	}
	_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "gadgets", Group: "fine.test", Name: "x"})
	if err == nil || !strings.HasPrefix(err.Error(), "read_error:") || !strings.Contains(err.Error(), "last discovery refresh failed") {
		t.Fatalf("after a failed refresh: %v", err)
	}
	failing = false
	if err := k8s.GetResourceDiscovery().Refresh(); err != nil {
		t.Fatal(err)
	}
	_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "gadgets", Group: "fine.test", Name: "x"})
	if err == nil || strings.Contains(err.Error(), "refresh failed") {
		t.Fatalf("a good refresh must clear the hedge: %v", err)
	}
}

func onlyRowKindOf(rows []map[string]any) string {
	if len(rows) != 1 {
		return ""
	}
	kind, _ := rows[0]["kind"].(string)
	return kind
}

// A single row is named by its Kind, so an irregular or CRD-chosen plural
// never produces a guessed singular.
func TestCountPhraseNamesASingleRowByKind(t *testing.T) {
	for _, tc := range []struct {
		n             int
		resource, row string
		want          string
	}{
		{0, "endpoints", "", "no endpoints"},
		{1, "endpoints", "Endpoints", "1 Endpoints"},
		{1, "caches", "Cache", "1 Cache"},
		{2, "services", "", "2 services"},
	} {
		if got := countPhrase(tc.n, tc.resource, tc.row); got != tc.want {
			t.Errorf("countPhrase(%d, %q, %q) = %q, want %q", tc.n, tc.resource, tc.row, got, tc.want)
		}
	}
	if got := onlyRowKind([]any{map[string]any{"kind": "Endpoints", "name": "api"}}); got != "Endpoints" {
		t.Errorf("onlyRowKind = %q", got)
	}
}

// A caller who can't see a namespace gets the same answer for it whether or
// not Radar is pinned there, so guessing names can't find the pin. Only a
// namespace the caller can see is reported as outside the pin.
func TestNamespacePinIsNotRevealedToGuesses(t *testing.T) {
	kind := setupListCoverageFixture(t, coverageTyped, true, "alpha", "payroll", "finance")
	oldPin, oldTarget := k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("payroll")
	t.Cleanup(func() { k8s.ForceNamespaceScope = oldPin; k8s.SetNamespaceScopeOverride(oldTarget) })
	ctx := withRestrictedUser(t, "pin-guesser", []string{"alpha"})
	codeFor := func(namespace string) readreason.Code {
		_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: kind, Namespace: namespace, Context: "none"})
		var re *readError
		if !errors.As(err, &re) {
			t.Fatalf("%s: want a read error, got %v", namespace, err)
		}
		return re.code
	}
	if got, other := codeFor("payroll"), codeFor("finance"); got != readreason.UserNamespaceNotVisible || other != got {
		t.Fatalf("hidden namespaces: payroll %q, finance %q; want both %q", got, other, readreason.UserNamespaceNotVisible)
	}
	if got := codeFor("alpha"); got != readreason.OutsideNamespacePin {
		t.Fatalf("a visible namespace outside the pin: %q", got)
	}
	// The tools that report a reason instead of a code follow the same rule.
	for _, guess := range []string{"payroll", "finance"} {
		if got := deniedScopeReason(ctx, []string{guess}); got != pkgopencost.ReasonAccessDenied {
			t.Fatalf("deniedScopeReason(%s) = %q, want access denied", guess, got)
		}
	}
	if got := deniedScopeReason(ctx, []string{"alpha"}); got != ReasonOutsideNamespacePin {
		t.Fatalf("deniedScopeReason(alpha) = %q", got)
	}
}

// A failure after some namespaces were already withheld covers only what the
// failed read was reading, so no namespace lands in two gaps; and a failure
// that wasn't assessed namespace by namespace still says where it applies.
func TestFailedListCoverageStaysDisjointAndPresent(t *testing.T) {
	target := readTarget{namespaces: []string{"alpha", "beta"}, explicit: true}
	var gaps gapSet
	gaps.add(readreason.UserForbidden, errors.New("denied"), "alpha")
	if !gaps.absorb(errKindNotCollected("secrets"), []string{"beta"}) {
		t.Fatal("a read failure is absorbed")
	}
	err := withListCoverage(context.Background(), gaps.failure(), "secrets", target, false)
	result, ok := readErrorResult(err)
	if !ok {
		t.Fatal(err)
	}
	body := result.StructuredContent.(mcpReadError)
	if body.Coverage == nil || body.Coverage.Validate() != nil || len(body.Coverage.Gaps) != 2 {
		t.Fatalf("coverage = %+v", body.Coverage)
	}
	for _, gap := range body.Coverage.Gaps {
		want := map[readreason.Code]string{readreason.UserForbidden: "alpha", readreason.RadarKindNotCollected: "beta"}[gap.Code]
		if gap.Namespaces == nil || !slices.Equal(gap.Namespaces.Names, []string{want}) {
			t.Fatalf("gap %s covers %+v, want %s", gap.Code, gap.Namespaces, want)
		}
	}

	unassessed := withListCoverage(context.Background(), errKindNotCollected("services"), "services", readTarget{}, false)
	result, ok = readErrorResult(unassessed)
	if !ok {
		t.Fatal(unassessed)
	}
	body = result.StructuredContent.(mcpReadError)
	if body.Coverage == nil || body.Coverage.Validate() != nil || len(body.Coverage.Gaps) != 1 || body.Coverage.Gaps[0].Namespaces.Extent != resourceapi.ExtentAll {
		t.Fatalf("an unassessed failure reaches the whole scope: %+v", body.Coverage)
	}
}

func TestDynamicBroadListKeepsUnansweredNamespaces(t *testing.T) {
	for _, tc := range []struct {
		name    string
		backoff time.Duration
		reject  bool
		want    readreason.Code
	}{
		{"active retry with existing informer", time.Hour, false, readreason.KindSyncPending},
		{"exhausted retry", time.Millisecond, false, readreason.ReadError},
		{"rejected credentials", 10 * time.Millisecond, true, readreason.RadarCredentialsRejected},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
			widget := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"namespace": "alpha", "name": "present"}}}
			dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, widget)
			var reject atomic.Bool
			dyn.PrependReactor("list", "widgets", func(a ktesting.Action) (bool, runtime.Object, error) {
				switch a.GetNamespace() {
				case "":
					return true, nil, collectorRefusal(gvr.GroupResource(), "list", "")
				case "beta":
					if reject.Load() {
						return true, nil, apierrors.NewUnauthorized("token rejected")
					}
					return true, nil, errors.New("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
				}
				return false, nil, nil
			})
			if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestDynamicState)
			d := k8s.GetDynamicResourceCache()
			d.Stop()
			core, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dyn, Discovery: k8s.GetResourceDiscovery().ResourceDiscovery, NamespaceFallbacks: []string{"alpha", "beta"}, ProbeRetryBackoff: []time.Duration{tc.backoff}, ProbeRecoveryIntervals: []time.Duration{time.Hour}})
			if err != nil {
				t.Fatal(err)
			}
			d.DynamicResourceCache = core
			if err := d.EnsureWatching(gvr); err != nil {
				t.Fatal(err)
			}
			reject.Store(tc.reject)
			deadline := time.Now().Add(3 * time.Second)
			for !d.IsNamespaceSynced(gvr, "alpha") || (tc.backoff != time.Hour && d.Observation(gvr).ProbeRetrying) {
				if time.Now().After(deadline) {
					t.Fatalf("scenario did not settle: %+v", d.Observation(gvr))
				}
				time.Sleep(time.Millisecond)
			}
			if err := d.EnsureWatching(gvr); tc.backoff == time.Hour && err != nil {
				t.Fatalf("existing informer must make EnsureWatching succeed during active retry: %v", err)
			}
			result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "widgets", Group: gvr.Group, Context: "none"})
			if err != nil {
				t.Fatal(err)
			}
			rows, cov := listBody(t, result)
			if len(rows) != 1 || cov.Status != "partial" || !slices.Equal(cov.Read, []string{"alpha"}) || !slices.Equal(cov.Scope, []string{"alpha", "beta"}) || !slices.Equal(cov.Codes, []string{string(tc.want)}) || !slices.Equal(cov.GapNamespaces[string(tc.want)], []string{"beta"}) {
				t.Fatalf("rows=%+v coverage=%+v", rows, cov)
			}
		})
	}
}

func TestDynamicBroadListKeepsUnknownProbeExtent(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces()...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	dyn.PrependReactor("list", "widgets", func(ktesting.Action) (bool, runtime.Object, error) {
		return true, nil, errors.New("client rate limiter Wait returned an error: rate: Wait(n=1) would exceed context deadline")
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	_, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "widgets", Group: gvr.Group, Context: "none"})
	result, ok := readErrorResult(err)
	if !ok {
		t.Fatalf("unclassified read: %v", err)
	}
	var body mcpReadError
	if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
		t.Fatal(err)
	}
	if body.ErrorCode != readreason.KindSyncPending || body.Coverage == nil || body.Coverage.Scope.Type != resourceapi.ScopeAllNamespaces || len(body.Coverage.Gaps) != 1 || body.Coverage.Gaps[0].Namespaces.Extent != resourceapi.ExtentUnknown {
		t.Fatalf("%+v", body)
	}
	if err := body.Coverage.Validate(); err != nil {
		t.Fatal(err)
	}
}

// A caller who can see every namespace and names one that doesn't exist is
// told so even when it is outside the pin: a mistyped name is the likelier
// cause, and "outside the pin" would send them to widen the pin to it. An
// existing namespace outside the pin is still outside_namespace_pin.
func TestMissingNamespaceOutsideThePinIsNotFound(t *testing.T) {
	// The Namespace informer holds alpha, beta, gamma, shop and kube-system.
	setupFakeCacheForFilterTests(t)
	oldPin, oldTarget := k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
	k8s.ForceNamespaceScope = true
	k8s.SetNamespaceScopeOverride("alpha")
	t.Cleanup(func() { k8s.ForceNamespaceScope = oldPin; k8s.SetNamespaceScopeOverride(oldTarget) })
	codeFor := func(namespace string) readreason.Code {
		_, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "pods", Namespace: namespace, Context: "none"})
		var re *readError
		if !errors.As(err, &re) {
			t.Fatalf("%s: want a read error, got %v", namespace, err)
		}
		return re.code
	}
	if got := codeFor("ghost"); got != readreason.NamespaceNotFound {
		t.Fatalf("missing namespace outside the pin: %q, want %q", got, readreason.NamespaceNotFound)
	}
	if got := codeFor("beta"); got != readreason.OutsideNamespacePin {
		t.Fatalf("existing namespace outside the pin: %q, want %q", got, readreason.OutsideNamespacePin)
	}
}

func TestRBACResourceReadsRequireExactNamespaceAccess(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset(fixtureNamespaces(
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "alpha"}},
		&rbacv1.Role{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "beta"}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "alpha"}},
		&rbacv1.RoleBinding{ObjectMeta: metav1.ObjectMeta{Name: "entry", Namespace: "beta"}},
	)...)); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	ctx := withRestrictedUser(t, "rbac-reader", []string{"alpha", "beta"})
	perms := getPermCache().Get("rbac-reader", nil)
	for _, resource := range []string{"roles", "rolebindings"} {
		for _, verb := range []string{"list", "get"} {
			perms.SetCanI(verb, "rbac.authorization.k8s.io", resource, "alpha", true)
			perms.SetCanI(verb, "rbac.authorization.k8s.io", resource, "beta", false)
		}
		result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: resource})
		if err != nil {
			t.Fatal(err)
		}
		var body struct {
			Items    []map[string]any     `json:"items"`
			Coverage resourceapi.Coverage `json:"coverage"`
		}
		if err := json.Unmarshal([]byte(extractText(t, result)), &body); err != nil {
			t.Fatal(err)
		}
		if len(body.Items) != 1 || body.Coverage.Status != resourceapi.CoveragePartial || len(body.Coverage.Gaps) != 1 || body.Coverage.Gaps[0].Code != readreason.UserForbidden || !slices.Equal(body.Coverage.Gaps[0].Namespaces.Names, []string{"beta"}) {
			t.Fatalf("exact list access: %+v", body)
		}
		for _, ns := range []string{"alpha", "beta"} {
			result, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: resource, Namespace: ns, Name: "entry"})
			if ns == "beta" {
				if err == nil || !strings.HasPrefix(err.Error(), "user_forbidden:") {
					t.Fatalf("denied get: %v", err)
				}
			} else if err != nil || !strings.Contains(extractText(t, result), "entry") {
				t.Fatalf("allowed get: %v", err)
			}
		}
	}
}
