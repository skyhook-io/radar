package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync/atomic"
	"testing"
	"time"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"github.com/skyhook-io/radar/internal/auth"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
)

func TestAPIResourceResponseObservationWireShape(t *testing.T) {
	response := apiResourceResponse{
		APIResource: k8score.APIResource{
			Group: "kueue.x-k8s.io", Version: "v1beta1", Kind: "Workload", Name: "workloads", IsCRD: true,
		},
		Observation: &k8score.DynamicResourceObservation{
			State:      k8score.DynamicObservationDeferred,
			ReasonCode: "resource_count_exceeds_eager_limit",
		},
	}
	encoded, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var got map[string]any
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	observation, ok := got["observation"].(map[string]any)
	if !ok {
		t.Fatalf("observation missing from %s", encoded)
	}
	if observation["state"] != "deferred" || observation["reasonCode"] != "resource_count_exceeds_eager_limit" {
		t.Fatalf("observation = %#v", observation)
	}

	builtIn, err := json.Marshal(apiResourceResponse{APIResource: k8score.APIResource{Version: "v1", Kind: "Pod", Name: "pods"}})
	if err != nil {
		t.Fatalf("Marshal built-in: %v", err)
	}
	var builtInWire map[string]any
	if err := json.Unmarshal(builtIn, &builtInWire); err != nil {
		t.Fatalf("Unmarshal built-in: %v", err)
	}
	if _, exists := builtInWire["observation"]; exists {
		t.Fatalf("built-in resource serialized dynamic observation: %s", builtIn)
	}
}

func TestFilterDynamicObservationNamespaces(t *testing.T) {
	observation := k8score.DynamicResourceObservation{
		State:      k8score.DynamicObservationSynced,
		Scope:      k8score.DynamicObservationScopeExplicitNamespaces,
		Namespaces: []string{"team-a", "team-b"},
		Truncated:  true,
	}

	filtered := filterDynamicObservationNamespaces(observation, []string{"team-b"})
	if !slices.Equal(filtered.Namespaces, []string{"team-b"}) {
		t.Fatalf("filtered namespaces = %v", filtered.Namespaces)
	}
	if !filtered.Truncated {
		t.Fatalf("filter weakened coverage bounds: %+v", filtered)
	}

	unrestricted := filterDynamicObservationNamespaces(observation, nil)
	if !slices.Equal(unrestricted.Namespaces, observation.Namespaces) {
		t.Fatalf("unrestricted namespaces = %v", unrestricted.Namespaces)
	}
	empty := observation
	empty.Namespaces = nil
	if got := filterDynamicObservationNamespaces(empty, []string{"team-b"}); len(got.Namespaces) != 0 {
		t.Fatalf("empty observation expanded to caller namespaces: %+v", got)
	}

	cluster := k8score.DynamicResourceObservation{
		State:      k8score.DynamicObservationSynced,
		Scope:      k8score.DynamicObservationScopeCluster,
		Namespaces: []string{"sentinel"},
	}
	cluster = filterDynamicObservationNamespaces(cluster, []string{"team-b"})
	if !slices.Equal(cluster.Namespaces, []string{"team-b"}) || cluster.Scope != k8score.DynamicObservationScopeExplicitNamespaces {
		t.Fatalf("cluster observation escaped viewer scope: %+v", cluster)
	}
	if got := filterDynamicObservationNamespaces(observation, []string{}); got.State != k8score.DynamicObservationUnwatched || got.ReasonCode != "no_visible_observation" || len(got.Namespaces) != 0 {
		t.Fatalf("empty viewer scope retained an observation: %+v", got)
	}
}

func TestAPIResourcesObservationUsesAuthenticatedViewerScope(t *testing.T) {
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	gvr := schema.GroupVersionResource{Group: "example.io", Version: "v1", Resource: "widgets"}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WidgetList"})
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: "Widget", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}}); err != nil {
		t.Fatal(err)
	}
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(gvr); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(gvr, 3*time.Second) {
		t.Fatal("cache did not sync")
	}
	env := newAuthTestServer(t)
	for _, namespaces := range [][]string{{"team-a"}, {}} {
		env.srv.permCache.Set("viewer", nil, &auth.UserPermissions{AllowedNamespaces: namespaces})
		var resources []apiResourceResponse
		assertOK(t, env.authGet(t, "/api/api-resources", "viewer", ""), &resources)
		var observation *k8score.DynamicResourceObservation
		for _, resource := range resources {
			if resource.Group == gvr.Group && resource.Name == gvr.Resource {
				observation = resource.Observation
			}
		}
		if observation == nil || observation.Scope != k8score.DynamicObservationScopeExplicitNamespaces || !slices.Equal(observation.Namespaces, namespaces) {
			t.Fatalf("viewer observation = %+v, namespaces = %v", observation, namespaces)
		}
		if len(namespaces) == 0 && (observation.State != k8score.DynamicObservationUnwatched || observation.Truncated) {
			t.Fatalf("invisible cache reported observed: %+v", observation)
		}
	}
	if got := cache.Observation(gvr); got.Scope != k8score.DynamicObservationScopeCluster {
		t.Fatalf("viewer projection mutated global state: %+v", got)
	}
}

func TestEmptyViewerScopeClearsRetainedObservationEvidence(t *testing.T) {
	now := time.Now()
	for _, state := range []k8score.DynamicObservationState{k8score.DynamicObservationDenied, k8score.DynamicObservationDeferred} {
		observation := k8score.DynamicResourceObservation{State: state, ReasonCode: "scope_probe_incomplete", ObservedAt: &now, Truncated: true}
		got := filterDynamicObservationNamespaces(observation, []string{})
		if got.State != k8score.DynamicObservationUnwatched || got.ReasonCode != "no_visible_observation" || got.ObservedAt != nil || got.Truncated {
			t.Fatalf("empty viewer retained probe evidence: %+v", got)
		}
		if got := filterDynamicObservationNamespaces(observation, nil); got.State != state || got.ObservedAt == nil {
			t.Fatalf("unrestricted viewer lost evidence: %+v", got)
		}
	}
}

func TestAPIResourcesDefinitionNamesRequireObservedReadableCRDs(t *testing.T) {
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	widget := k8s.APIResource{Group: "plants.example.io", Version: "v1", Kind: "Cactus", Name: "cacti", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list", "watch"}}
	definitions := k8s.APIResource{Group: crdGVR.Group, Version: crdGVR.Version, Kind: "CustomResourceDefinition", Name: crdGVR.Resource, Verbs: []string{"get", "list", "watch"}}
	metrics := k8s.APIResource{Group: "metrics.k8s.io", Version: "v1beta1", Kind: "PodMetrics", Name: "pods", Namespaced: true, IsCRD: true, Verbs: []string{"get", "list"}}
	native := k8s.APIResource{Group: "resource.k8s.io", Version: "v1", Kind: "DeviceClass", Name: "deviceclasses", IsCRD: true, Verbs: []string{"get", "list", "watch"}}
	definition := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "apiextensions.k8s.io/v1", "kind": "CustomResourceDefinition", "metadata": map[string]any{"name": "cacti.plants.example.io"}, "spec": map[string]any{"group": "plants.example.io", "names": map[string]any{"kind": "Cactus", "plural": "cacti"}}}}
	dyn := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{crdGVR: "CustomResourceDefinitionList"}, definition)
	if err := k8s.InitTestDynamicResourceCache(dyn, []k8s.APIResource{widget, definitions, metrics, native}); err != nil {
		t.Fatal(err)
	}
	env := newAuthTestServer(t)
	permissions := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
	permissions.SetCanI("list", crdGVR.Group, crdGVR.Resource, "", true)
	permissions.SetCanI("get", crdGVR.Group, crdGVR.Resource, "", true)
	env.srv.permCache.Set("viewer", nil, permissions)
	before := len(dyn.Actions())
	var coldResources []apiResourceResponse
	assertOK(t, env.authGet(t, "/api/api-resources", "viewer", ""), &coldResources)
	for _, resource := range coldResources {
		if resource.DefinitionName != "" {
			t.Fatalf("unwatched inventory confirmed a definition: %+v", resource)
		}
		if resource.Group == crdGVR.Group && resource.Name == crdGVR.Resource && (resource.Observation == nil || resource.Observation.State != k8score.DynamicObservationUnwatched) {
			t.Fatalf("cold definition inventory omitted its state: %+v", resource)
		}
	}
	if len(dyn.Actions()) != before {
		t.Fatal("cold discovery performed object reads")
	}
	cache := k8s.GetDynamicResourceCache()
	if err := cache.EnsureWatching(crdGVR); err != nil {
		t.Fatal(err)
	}
	if !cache.WaitForSync(crdGVR, 3*time.Second) {
		t.Fatal("CRD cache did not sync")
	}
	for _, access := range []struct{ list, get bool }{{true, true}, {true, false}, {false, true}, {false, false}} {
		permissions := &auth.UserPermissions{AllowedNamespaces: []string{"team"}}
		permissions.SetCanI("list", crdGVR.Group, crdGVR.Resource, "", access.list)
		permissions.SetCanI("get", crdGVR.Group, crdGVR.Resource, "", access.get)
		env.srv.permCache.Set("viewer", nil, permissions)
		before := len(dyn.Actions())
		var resources []apiResourceResponse
		assertOK(t, env.authGet(t, "/api/api-resources", "viewer", ""), &resources)
		found := false
		for _, resource := range resources {
			if resource.Group == widget.Group && resource.Name == widget.Name {
				found = true
				want := ""
				if access.list && access.get {
					want = definition.GetName()
				}
				if resource.DefinitionName != want {
					t.Fatalf("access=%+v, definition=%q want=%q", access, resource.DefinitionName, want)
				}
			} else if resource.DefinitionName != "" {
				t.Fatalf("non-CRD inferred definition: %+v", resource)
			}
			if resource.Group == crdGVR.Group && resource.Name == crdGVR.Resource {
				if access.list && access.get {
					if resource.Observation == nil || resource.Observation.State != k8score.DynamicObservationSynced {
						t.Fatalf("readable inventory omitted its sync state: %+v", resource)
					}
				} else if resource.Observation != nil {
					t.Fatalf("unreadable definition inventory exposed state: %+v", resource)
				}
			}
		}
		if !found {
			t.Fatal("custom API missing")
		}
		if len(dyn.Actions()) != before {
			t.Fatal("discovery performed live object reads")
		}
	}
}

func TestAPIResourcesRejectsDiscoveryReplacedDuringAssembly(t *testing.T) {
	k8s.ResetTestDynamicState()
	t.Cleanup(k8s.ResetTestDynamicState)
	var replaced atomic.Bool
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		k8s.ResetResourceDiscovery()
		replaced.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&authorizationv1.SubjectAccessReview{Status: authorizationv1.SubjectAccessReviewStatus{Allowed: true}})
	}))
	t.Cleanup(api.Close)
	client, err := kubernetes.NewForConfig(&rest.Config{Host: api.URL})
	if err != nil {
		t.Fatal(err)
	}
	previous := k8s.SetTestClient(client)
	t.Cleanup(func() { k8s.SetTestClient(previous) })
	dyn := dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())
	if err := k8s.InitTestDynamicResourceCache(dyn, nil); err != nil {
		t.Fatal(err)
	}
	env := newAuthTestServer(t)
	env.srv.permCache.Set("viewer", nil, &auth.UserPermissions{AllowedNamespaces: []string{"team"}})
	response := env.authGet(t, "/api/api-resources", "viewer", "")
	defer response.Body.Close()
	if !replaced.Load() {
		t.Fatal("discovery was not replaced during authorization")
	}
	if response.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("discovery replacement returned %d, want 503", response.StatusCode)
	}
}
