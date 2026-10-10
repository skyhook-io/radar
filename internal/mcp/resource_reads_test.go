package mcp

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	mcpsdk "github.com/modelcontextprotocol/go-sdk/mcp"
	corev1 "k8s.io/api/core/v1"
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
			want := "no_namespace_access:"
			if kind == "secrets" {
				want = "forbidden:"
			}
			if err == nil || !strings.HasPrefix(err.Error(), want) || !strings.Contains(err.Error(), ns) || (kind == "secrets" && !strings.Contains(err.Error(), verb)) {
				t.Fatalf("%s/%s: expected namespace caller denial, got %v", kind, verb, err)
			}
		}
	}
	noAccess := withRestrictedUser(t, "no-access", []string{})
	_, _, err := handleListResources(noAccess, nil, listResourcesInput{Kind: "pods"})
	if err == nil || !strings.Contains(err.Error(), "no Radar access") {
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
	for _, input := range []listResourcesInput{{Kind: "nodes"}, {Kind: "services", Namespace: "beta"}} {
		_, _, err := handleListResources(context.Background(), nil, input)
		if err == nil || !strings.Contains(err.Error(), "kind_not_watched") {
			t.Fatalf("expected unwatched kind/scope, got %v", err)
		}
	}
	result, _, err := handleListResources(context.Background(), nil, listResourcesInput{Kind: "services", Context: "none"})
	if err != nil || extractText(t, result) != "[]" {
		t.Fatalf("unscoped collector-covered list: %v, %v", result, err)
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
						if err == nil || !(strings.HasPrefix(err.Error(), want+":") || (want == "resource not found" && strings.Contains(err.Error(), want))) || (want != "resource not found" && strings.Contains(err.Error(), "resource not found")) {
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

// The collector covers alpha and gamma (gamma is empty); beta holds an object
// it cannot read. Typed and dynamic kinds must answer every scope alike.
func setupCoverageRuleFixture(t *testing.T, typed bool) (kind string, prewarm func()) {
	t.Helper()
	if typed {
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
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestState)
	t.Cleanup(k8s.SetTestPermissionResult(&k8s.PermissionCheckResult{Perms: &k8s.ResourcePermissions{}, NamespaceScoped: true, Namespace: "alpha", ScopeCandidates: []string{"alpha", "gamma"}}))
	gvr := schema.GroupVersionResource{Group: "example.test", Version: "v1", Resource: "widgets"}
	objs := []runtime.Object{
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "alpha-object", "namespace": "alpha"}}},
		&unstructured.Unstructured{Object: map[string]any{"apiVersion": "example.test/v1", "kind": "Widget", "metadata": map[string]any{"name": "beta-object", "namespace": "beta"}}},
	}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"}, objs...)
	dyn.PrependReactor("list", "widgets", func(action ktesting.Action) (bool, runtime.Object, error) {
		if ns := action.GetNamespace(); ns != "alpha" && ns != "gamma" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("collector denied"))
		}
		return false, nil, nil
	})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Kind: "Widget", Name: gvr.Resource, Namespaced: true, Verbs: []string{"list", "watch", "get"}}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(k8s.ResetTestDynamicState)
	return "widgets", func() {
		if err := k8s.GetDynamicResourceCache().EnsureWatching(gvr); err != nil {
			t.Fatal(err)
		}
	}
}

func TestResourceReadsCollectorCoverageRule(t *testing.T) {
	const anyHidden = "the collector's coverage does not include any namespace you can read"
	cases := []struct {
		name      string
		caller    string // "" = auth disabled, "admin" = cluster-wide access, "restricted" = allowed
		allowed   []string
		namespace string
		rows      []string
		wantErr   []string
		hidden    []string
	}{
		{name: "auth disabled reads the covered namespaces", rows: []string{"alpha-object"}},
		{name: "cluster-wide caller reads the covered namespaces", caller: "admin", rows: []string{"alpha-object"}},
		{name: "restricted caller fully covered", caller: "restricted", allowed: []string{"alpha", "gamma"}, rows: []string{"alpha-object"}},
		{name: "restricted caller covered and empty", caller: "restricted", allowed: []string{"gamma"}, rows: []string{}},
		{name: "explicit covered and empty", namespace: "gamma", rows: []string{}},
		{name: "restricted partial overlap", caller: "restricted", allowed: []string{"alpha", "beta"}, wantErr: []string{`namespace "beta"`, `covered namespaces you can read: "alpha"`, "retry with one of them as the namespace"}, hidden: []string{"gamma"}},
		{name: "restricted partial overlap with an empty covered namespace", caller: "restricted", allowed: []string{"gamma", "beta"}, wantErr: []string{`namespace "beta"`, `covered namespaces you can read: "gamma"`}, hidden: []string{"alpha"}},
		{name: "no overlap", caller: "restricted", allowed: []string{"beta"}, wantErr: []string{`namespace "beta"`, anyHidden}, hidden: []string{"alpha", "gamma"}},
		{name: "no overlap across namespaces", caller: "restricted", allowed: []string{"beta", "delta"}, wantErr: []string{`namespaces "beta,delta"`, anyHidden}, hidden: []string{"alpha", "gamma"}},
		{name: "explicit uncovered", namespace: "beta", wantErr: []string{`namespace "beta"`, `covered namespaces you can read: "alpha,gamma"`}},
		{name: "restricted explicit uncovered", caller: "restricted", allowed: []string{"alpha", "beta"}, namespace: "beta", wantErr: []string{`namespace "beta"`, `covered namespaces you can read: "alpha"`}, hidden: []string{"gamma"}},
	}
	for _, typed := range []bool{true, false} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("typed=%v/%s", typed, tc.name), func(t *testing.T) {
				kind, prewarm := setupCoverageRuleFixture(t, typed)
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
					if err == nil || !strings.HasPrefix(err.Error(), "kind_not_watched:") {
						t.Fatalf("uncovered scope must fail with kind_not_watched, got %v", err)
					}
					for _, text := range tc.wantErr {
						if !strings.Contains(err.Error(), text) {
							t.Fatalf("missing %s: %v", text, err)
						}
					}
					for _, ns := range tc.hidden {
						if strings.Contains(err.Error(), ns) {
							t.Fatalf("error names namespace %q the caller cannot read: %v", ns, err)
						}
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				body := extractText(t, result)
				if len(tc.rows) == 0 && body != "[]" {
					t.Fatalf("covered empty scope must be a plain empty list: %s", body)
				}
				for _, name := range tc.rows {
					if !containsName(body, name) {
						t.Fatalf("missing %s: %s", name, body)
					}
				}
				if containsName(body, "beta-object") {
					t.Fatalf("uncovered namespace leaked: %s", body)
				}
			})
		}
	}
	for _, typed := range []bool{true, false} {
		t.Run(fmt.Sprintf("typed=%v/get uncovered", typed), func(t *testing.T) {
			kind, prewarm := setupCoverageRuleFixture(t, typed)
			prewarm()
			ctx := withRestrictedUser(t, "coverage-getter", []string{"alpha", "beta"})
			_, _, err := handleGetResource(ctx, nil, getResourceInput{Kind: kind, Namespace: "beta", Name: "beta-object"})
			if err == nil || !strings.HasPrefix(err.Error(), "kind_not_watched:") || !strings.Contains(err.Error(), `covered namespaces you can read: "alpha"`) || strings.Contains(err.Error(), "gamma") {
				t.Fatalf("uncovered get: %v", err)
			}
		})
	}
	t.Run("typed=false/no overlap before any namespace is collected", func(t *testing.T) {
		kind, _ := setupCoverageRuleFixture(t, false)
		ctx := withRestrictedUser(t, "cold-coverage-reader", []string{"beta"})
		_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: kind, Context: "none"})
		if err == nil || !strings.HasPrefix(err.Error(), "collector_forbidden:") || !strings.Contains(err.Error(), `"beta"`) {
			t.Fatalf("uncovered cold scope must name the collector denial, got %v", err)
		}
	})
	t.Run("pinned scope is not named to a caller who cannot read it", func(t *testing.T) {
		setupFakeCacheForFilterTests(t)
		oldPin, oldTarget := k8s.ForceNamespaceScope, k8s.GetNamespaceScopeTarget()
		k8s.ForceNamespaceScope = true
		k8s.SetNamespaceScopeOverride("alpha")
		t.Cleanup(func() { k8s.ForceNamespaceScope = oldPin; k8s.SetNamespaceScopeOverride(oldTarget) })
		ctx := withRestrictedUser(t, "pin-outsider", []string{"beta"})
		_, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "pods"})
		if err == nil || !strings.HasPrefix(err.Error(), "no_namespace_access:") || strings.Contains(err.Error(), "alpha") {
			t.Fatalf("unscoped denial must not name the pinned namespace: %v", err)
		}
	})
}

func TestResourceReadsColdDynamicKinds(t *testing.T) {
	kinds := []resourceid.Builtin{{Kind: "Widget", Resource: "widgets", Group: "example.test", Version: "v1", Namespaced: true}}
	for _, b := range resourceid.Builtins {
		if _, _, cluster := k8s.ClusterOnlyKindGVR(b.Resource); cluster && !k8s.TypedKindOwnsGroup(b.Kind, b.Group) {
			kinds = append(kinds, b)
		}
	}
	for _, b := range kinds {
		for _, verb := range []string{"list", "get"} {
			t.Run(b.Kind+"/"+verb, func(t *testing.T) {
				if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
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
		want := "collector_forbidden:"
		if unauthorized {
			probeErrors["nodes"] = apierrors.NewUnauthorized("expired token")
			want = "collector_unauthorized:"
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
	if err == nil || !strings.HasPrefix(err.Error(), "collector_unauthorized:") || strings.Contains(err.Error(), "forbidden") {
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
		for _, text := range []string{"no_namespace_access:", "no Radar access", "beta", "list pods or deployments", "~2 minutes"} {
			if err == nil || !strings.Contains(err.Error(), text) {
				t.Fatalf("missing %s: %v", text, err)
			}
		}
		if strings.Contains(err.Error(), "your role cannot list") || strings.Contains(err.Error(), "API group") {
			t.Fatalf("namespace membership cannot claim exact SAR: %v", err)
		}
	}
	err := resourcePermissionError(true, "list", "", "secrets", namespaceForError([]string{"alpha", "beta"}))
	if strings.Contains(err.Error(), "at cluster scope") || !strings.Contains(err.Error(), "beta") {
		t.Fatal(err)
	}
}

func TestResourceReadsDynamicCollectorIntersection(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
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
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", fmt.Errorf("collector denied"))
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
	if err == nil || !strings.HasPrefix(err.Error(), "kind_not_watched:") {
		t.Fatalf("caller namespace denied by collector must not return empty success: %v", err)
	}
	for _, verb := range []string{"list", "get"} {
		var err error
		if verb == "list" {
			_, _, err = handleListResources(context.Background(), nil, listResourcesInput{Kind: "widgets", Namespace: "beta"})
		} else {
			_, _, err = handleGetResource(context.Background(), nil, getResourceInput{Kind: "widgets", Namespace: "beta", Name: "x"})
		}
		if err == nil || !strings.HasPrefix(err.Error(), "kind_not_watched:") || !strings.Contains(err.Error(), `covered namespaces you can read: "alpha"`) {
			t.Fatalf("%s explicit uncovered scope: %v", verb, err)
		}
	}
}

func TestResourceReadsDynamicUnauthorizedPrefix(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
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
		if err == nil || !strings.HasPrefix(err.Error(), "collector_unauthorized:") || !strings.Contains(err.Error(), "expired token") {
			t.Fatalf("%s credentials rejection: %v", verb, err)
		}
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
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
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
	if !containsName(extractText(t, result), "alpha-widget") {
		t.Fatalf("cold pinned list lost widget: %s", extractText(t, result))
	}
}

func TestResourceReadsRestrictedDirectLists(t *testing.T) {
	for _, b := range []resourceid.Builtin{
		{Kind: "Lease", Resource: "leases", Group: "coordination.k8s.io", Version: "v1"},
		{Kind: "EndpointSlice", Resource: "endpointslices", Group: "discovery.k8s.io", Version: "v1"},
		{Kind: "Endpoints", Resource: "endpoints", Version: "v1"},
	} {
		t.Run(b.Kind, func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
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

func TestResourceReadsDynamicCallerNamespaceOutsideCandidates(t *testing.T) {
	for _, prewarm := range []bool{false, true} {
		t.Run(fmt.Sprintf("prewarm=%v", prewarm), func(t *testing.T) {
			if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
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
		if err == nil || !strings.HasPrefix(err.Error(), "collector_unauthorized:") || !strings.Contains(err.Error(), "alpha rejected token") {
			t.Fatalf("%s: namespace credential rejection lost: %v", verb, err)
		}
	}
	result, _, err := handleListResources(ctx, nil, listResourcesInput{Kind: "pods", Namespace: "beta", Context: "none"})
	if err != nil || !containsName(extractText(t, result), "present") {
		t.Fatalf("healthy namespace incorrectly denied: %v", err)
	}
	_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "pods", Context: "none"})
	if err == nil || !strings.HasPrefix(err.Error(), "collector_unauthorized:") {
		t.Fatalf("unscoped list must not omit a namespace whose credentials were rejected: %v", err)
	}
}

func TestReadDynamicScopeRereadsOnlyBeforeSync(t *testing.T) {
	if err := k8s.InitTestResourceCache(fake.NewClientset()); err != nil {
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
		if err == nil || !strings.HasPrefix(err.Error(), verb+"_error:") || !strings.Contains(err.Error(), "widgets") {
			t.Fatalf("%s: %v", verb, err)
		}
	}
	err := resourceGetError(context.Background(), fmt.Errorf("store unavailable"), "pods", "alpha", "present")
	if !strings.HasPrefix(err.Error(), "get_error:") || !strings.Contains(err.Error(), "pods") {
		t.Fatal(err)
	}
	namespaces := make([]string, 15)
	for i := range namespaces {
		namespaces[i] = fmt.Sprintf("ns-%02d", i)
	}
	ctx := withRestrictedUser(t, "secret-denial-reader", namespaces)
	seedSecretListCanI(t, "secret-denial-reader", nil, namespaces)
	_, _, err = handleListResources(ctx, nil, listResourcesInput{Kind: "secrets"})
	if err == nil || !strings.Contains(err.Error(), "ns-09") || !strings.Contains(err.Error(), "(+5 more)") || strings.Contains(err.Error(), "ns-10") {
		t.Fatalf("Secret namespace denial must be bounded: %v", err)
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
		{"unknown", "", "unknown_kind:"},
		{"deployments", "wrong.group", "unknown_kind:"},
	} {
		_, _, err := handleGetResource(context.Background(), nil, getResourceInput{Kind: tc.kind, Group: tc.group, Name: "present"})
		if err == nil || !strings.HasPrefix(err.Error(), tc.want) {
			t.Fatalf("%s/%s: %v", tc.kind, tc.group, err)
		}
	}
	k8s.ResetTestDynamicState()
	_, _, err := handleGetResource(context.Background(), nil, getResourceInput{Kind: "widgets", Name: "present"})
	if err == nil || !strings.HasPrefix(err.Error(), "kind_sync_pending:") {
		t.Fatalf("uninitialized discovery: %v", err)
	}
}
