package server

import (
	"encoding/json"
	"slices"
	"testing"
	"time"

	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	dynamicfake "k8s.io/client-go/dynamic/fake"

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
	for _, omitted := range []string{"origin", "watchStartedAt", "namespacePartial", "viewerRestricted"} {
		if _, exists := observation[omitted]; exists {
			t.Fatalf("observation serialized omitted field %s: %s", omitted, encoded)
		}
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
