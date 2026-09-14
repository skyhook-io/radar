package server

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/discovery"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/capacityapi"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type mutableResourceDiscovery struct {
	discovery.DiscoveryInterface
	resources []*metav1.APIResourceList
	err       error
}

func (d *mutableResourceDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, d.resources, d.err
}

func TestResourceDiscoveryCoverageStates(t *testing.T) {
	availableResources := []*metav1.APIResourceList{{
		GroupVersion: "example.io/v1",
		APIResources: []metav1.APIResource{{Name: "widgets", Kind: "Widget", Verbs: []string{"list", "watch"}}},
	}}

	t.Run("not initialized", func(t *testing.T) {
		got := resourceDiscoveryCoverage(nil)
		if got.Status != capacityapi.CoverageUnavailable || got.ReasonCode != "not_initialized" || got.ObservedAt != nil {
			t.Fatalf("coverage = %+v", got)
		}
	})

	t.Run("available", func(t *testing.T) {
		client := &mutableResourceDiscovery{resources: availableResources}
		core, err := k8score.NewResourceDiscovery(client)
		if err != nil {
			t.Fatalf("NewResourceDiscovery: %v", err)
		}
		got := resourceDiscoveryCoverage(&k8s.ResourceDiscovery{ResourceDiscovery: core})
		if got.Status != capacityapi.CoverageAvailable || got.ReasonCode != "" || got.ObservedAt == nil || len(got.ImpactFields) != 0 {
			t.Fatalf("coverage = %+v", got)
		}
	})

	t.Run("fresh partial", func(t *testing.T) {
		client := &mutableResourceDiscovery{resources: availableResources, err: errors.New("one group failed")}
		core, err := k8score.NewResourceDiscovery(client)
		if err != nil {
			t.Fatalf("NewResourceDiscovery: %v", err)
		}
		got := resourceDiscoveryCoverage(&k8s.ResourceDiscovery{ResourceDiscovery: core})
		if got.Status != capacityapi.CoveragePartial || got.ReasonCode != "partial_discovery" || got.ObservedAt == nil {
			t.Fatalf("coverage = %+v", got)
		}
	})

	t.Run("failed initial refresh", func(t *testing.T) {
		client := &mutableResourceDiscovery{err: errors.New("discovery unavailable")}
		core, err := k8score.NewResourceDiscovery(client)
		if err != nil {
			t.Fatalf("NewResourceDiscovery: %v", err)
		}
		got := resourceDiscoveryCoverage(&k8s.ResourceDiscovery{ResourceDiscovery: core})
		if got.Status != capacityapi.CoverageError || got.ReasonCode != "discovery_refresh_failed" || got.ObservedAt != nil {
			t.Fatalf("coverage = %+v", got)
		}
	})

	t.Run("stale preserved snapshot", func(t *testing.T) {
		client := &mutableResourceDiscovery{resources: availableResources}
		core, err := k8score.NewResourceDiscovery(client)
		if err != nil {
			t.Fatalf("NewResourceDiscovery: %v", err)
		}
		client.resources = nil
		client.err = errors.New("refresh failed")
		if err := core.Refresh(); err == nil {
			t.Fatal("Refresh unexpectedly succeeded")
		}
		got := resourceDiscoveryCoverage(&k8s.ResourceDiscovery{ResourceDiscovery: core})
		if got.Status != capacityapi.CoverageError || got.ReasonCode != "discovery_refresh_failed" || got.ObservedAt == nil {
			t.Fatalf("coverage = %+v", got)
		}
	})
}

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
	if _, exists := observation["origin"]; exists {
		t.Fatalf("unstarted observation serialized an origin: %s", encoded)
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
		State:            k8score.DynamicObservationWatched,
		Scope:            k8score.DynamicObservationScopeExplicitNamespaces,
		Namespaces:       []string{"team-a", "team-b"},
		NamespacePartial: true,
		Truncated:        true,
	}

	filtered := filterDynamicObservationNamespaces(observation, []string{"team-b"})
	if !slices.Equal(filtered.Namespaces, []string{"team-b"}) {
		t.Fatalf("filtered namespaces = %v", filtered.Namespaces)
	}
	if !filtered.NamespacePartial || !filtered.Truncated {
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
		State:      k8score.DynamicObservationWatched,
		Scope:      k8score.DynamicObservationScopeCluster,
		Namespaces: []string{"sentinel"},
	}
	cluster = filterDynamicObservationNamespaces(cluster, []string{"team-b"})
	if !slices.Equal(cluster.Namespaces, []string{"sentinel"}) {
		t.Fatalf("cluster observation was namespace-filtered: %+v", cluster)
	}
}
