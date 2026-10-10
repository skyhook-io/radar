package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/wait"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/kubernetes/scheme"
	ktesting "k8s.io/client-go/testing"

	"github.com/skyhook-io/radar/internal/k8s"
	pkgauth "github.com/skyhook-io/radar/pkg/auth"
	"github.com/skyhook-io/radar/pkg/k8score"
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
				for _, want := range []string{"forbidden", "your role", verb, resource, `group "` + group + `"`} {
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
				if _, _, cluster := k8s.ClusterOnlyKindGVR(b.Resource); cluster || b.Kind == "Secret" {
					kinds = append(kinds, b)
				}
			}
			kinds = append(kinds, resourceid.Builtin{Kind: "CustomResourceDefinition", Resource: "customresourcedefinitions", Group: "apiextensions.k8s.io", Version: "v1"}, resourceid.Builtin{Kind: "Widget", Resource: "widgets", Group: "example.test", Version: "v1", Namespaced: true})
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
			if err := k8s.InitTestResourceCache(fake.NewClientset(typedObjects...)); err != nil {
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
					if !populated && body != "[]" {
						t.Fatalf("genuine empty list must stay [], got %s", body)
					}
					result, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: b.Kind, Group: b.Group, Namespace: ns, Name: "present", Context: "none"})
					if populated {
						if err != nil {
							t.Fatal(err)
						}
						if !containsName(extractText(t, result), "present") {
							t.Fatal("missing get object")
						}
					} else if err == nil || !strings.Contains(err.Error(), "not found") {
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
			if err == nil || !strings.Contains(err.Error(), "forbidden") || !strings.Contains(err.Error(), ns) || !strings.Contains(err.Error(), verb) {
				t.Fatalf("%s/%s: expected namespace caller denial, got %v", kind, verb, err)
			}
		}
	}
	noAccess := withRestrictedUser(t, "no-access", []string{})
	_, _, err := handleListResources(noAccess, nil, listResourcesInput{Kind: "pods"})
	if err == nil || !strings.Contains(err.Error(), "no namespace access") {
		t.Fatalf("expected no namespace access, got %v", err)
	}
	unchecked := withClusterAdmin(t, "unchecked")
	for _, kind := range []string{"nodes", "secrets"} {
		for _, verb := range []string{"list", "get"} {
			if verb == "list" {
				_, _, err = handleListResources(unchecked, nil, listResourcesInput{Kind: kind})
			} else {
				_, _, err = handleGetResource(unchecked, nil, getResourceInput{Kind: kind, Namespace: "alpha", Name: "hidden"})
			}
			if err == nil || !strings.Contains(err.Error(), "permission_check_failed") {
				t.Fatalf("%s/%s: expected failed SAR, got %v", kind, verb, err)
			}
		}
	}

	discoveryFailed := pkgauth.ContextWithUser(context.Background(), &pkgauth.User{Username: "uncached"})
	_, _, err = handleListResources(discoveryFailed, nil, listResourcesInput{Kind: "pods"})
	if err == nil || !strings.Contains(err.Error(), "permission_check_failed") {
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
		if err == nil || !strings.Contains(err.Error(), ReasonOutsideNamespaceScope) || strings.Contains(err.Error(), "forbidden") {
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
		if err == nil || !strings.Contains(err.Error(), "kind_sync_failed") {
			t.Fatalf("expected failed sync, got %v", err)
		}
	}
	k8s.ResetTestState()
	if err := k8s.InitScopedTestResourceCache(fake.NewClientset(), map[string]k8score.ResourceScope{"services": {Enabled: true, Namespace: "alpha"}}); err != nil {
		t.Fatal(err)
	}
	for _, input := range []listResourcesInput{{Kind: "nodes"}, {Kind: "services", Namespace: "beta"}, {Kind: "services"}} {
		_, _, err := handleListResources(context.Background(), nil, input)
		if err == nil || !strings.Contains(err.Error(), "kind_not_watched") {
			t.Fatalf("expected unwatched kind/scope, got %v", err)
		}
	}
}

func TestResourceReadsDynamicPendingAndCollectorDenial(t *testing.T) {
	for _, denied := range []bool{false, true} {
		t.Run(fmt.Sprintf("collectorDenied=%v", denied), func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(k8s.ResetTestState)
			kinds := []resourceid.Builtin{{Kind: "Widget", Resource: "widgets", Group: "example.test", Version: "v1", Namespaced: true}, {Kind: "CustomResourceDefinition", Resource: "customresourcedefinitions", Group: "apiextensions.k8s.io", Version: "v1"}}
			for _, b := range resourceid.Builtins {
				if _, _, cluster := k8s.ClusterOnlyKindGVR(b.Resource); cluster && !k8s.TypedKindOwnsGroup(b.Kind, b.Group) {
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
					return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "", fmt.Errorf("collector denied"))
				}
				if action.(interface{ GetListOptions() metav1.ListOptions }).GetListOptions().Limit == 1 {
					return false, nil, nil
				}
				return true, nil, fmt.Errorf("initial list unavailable")
			})
			dyn.PrependReactor("get", "*", func(action ktesting.Action) (bool, runtime.Object, error) {
				if denied {
					return true, nil, apierrors.NewForbidden(action.GetResource().GroupResource(), "", fmt.Errorf("collector denied"))
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
							want = "collector_forbidden"
						} else if b.Kind == "CustomResourceDefinition" && verb == "get" {
							want = "resource not found"
						}
						if err == nil || !strings.Contains(err.Error(), want) || (want != "resource not found" && strings.Contains(err.Error(), "resource not found")) {
							t.Fatalf("%s: expected %s, got %v", verb, want, err)
						}
						if denied && (!strings.Contains(err.Error(), "Radar's") || !strings.Contains(err.Error(), "can't ")) {
							t.Fatalf("denial must identify collector, got %v", err)
						}
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
				if err == nil || !strings.Contains(err.Error(), "unknown_kind") || strings.Contains(err.Error(), "resource not found") {
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
	server := newServer(false)
	server.AddReceivingMiddleware(func(next mcpsdk.MethodHandler) mcpsdk.MethodHandler {
		return func(ctx context.Context, method string, req mcpsdk.Request) (mcpsdk.Result, error) {
			return next(pkgauth.ContextWithUser(ctx, &pkgauth.User{Username: "transport-reader"}), method, req)
		}
	})
	session := connectTo(t, server)
	for _, name := range []string{"list_resources", "get_resource"} {
		arguments := map[string]any{"kind": "nodes"}
		if name == "get_resource" {
			arguments["name"] = "hidden"
		}
		result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: name, Arguments: arguments})
		if err != nil {
			t.Fatal(err)
		}
		if !result.IsError || !strings.Contains(extractText(t, result), "forbidden: your role") {
			t.Fatalf("caller denial must be an MCP tool error, got %+v", result)
		}
	}
	result, err := session.CallTool(context.Background(), &mcpsdk.CallToolParams{Name: "list_resources", Arguments: map[string]any{"kind": "services", "context": "none"}})
	if err != nil {
		t.Fatal(err)
	}
	if result.IsError || extractText(t, result) != "[]" {
		t.Fatalf("genuine empty success must stay [], got %+v", result)
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
			if err := call(); err == nil || !strings.Contains(err.Error(), "permission_check_failed") {
				t.Fatalf("%s/%s first check: %v", kind, verb, err)
			}
			if err := call(); err == nil || !strings.Contains(err.Error(), "forbidden: your role") {
				t.Fatalf("%s/%s retry must establish denial: %v", kind, verb, err)
			}
			if calls != 2 {
				t.Fatalf("failed SAR must not be cached: got %d checks", calls)
			}
		}
	}
}
