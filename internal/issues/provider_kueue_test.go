package issues

import (
	"errors"
	"testing"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	dynamicfake "k8s.io/client-go/dynamic/fake"
	"k8s.io/client-go/kubernetes/fake"
	ktesting "k8s.io/client-go/testing"
)

func TestKueueInventoryReadsAnExplicitNamespaceOutsideDeniedFallbacks(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: kueueGroup, Version: "v1beta2", Resource: "workloads"}
	w := actionableWorkload("allowed", "blocked", "q")
	client := dynamicfake.NewSimpleDynamicClientWithCustomListKinds(runtime.NewScheme(), map[schema.GroupVersionResource]string{gvr: "WorkloadList"}, w)
	client.PrependReactor("list", "workloads", func(action ktesting.Action) (bool, runtime.Object, error) {
		if action.GetNamespace() != "allowed" {
			return true, nil, apierrors.NewForbidden(gvr.GroupResource(), "", nil)
		}
		return false, nil, nil
	})
	discovery, err := k8score.NewResourceDiscovery(fake.NewClientset().Discovery())
	if err != nil {
		t.Fatal(err)
	}
	discovery.AddAPIResource(k8score.APIResource{Group: gvr.Group, Version: gvr.Version, Name: gvr.Resource, Kind: kueueWorkloadKind, Namespaced: true, Verbs: []string{"list", "watch"}})
	dynamic, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: client, Discovery: discovery, NamespaceFallbacks: []string{"denied"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dynamic.Stop)
	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dynamic}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery}}
	deadline := time.Now().Add(5 * time.Second)
	for {
		inventory := p.kueueInventory(kueueGroup, kueueWorkloadKind, []string{"allowed"})
		if inventory.OK && !inventory.Partial && len(inventory.Items) == 1 && inventory.Items[0].GetName() == "blocked" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("readable namespace blocked by global probe: %+v", inventory)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

type failedKueueDiscovery struct{ discovery.DiscoveryInterface }

func (failedKueueDiscovery) ServerGroupsAndResources() ([]*metav1.APIGroup, []*metav1.APIResourceList, error) {
	return nil, nil, errors.New("discovery unavailable")
}
func TestKueueFailedDiscoveryIsNotAbsence(t *testing.T) {
	discovery, err := k8score.NewResourceDiscovery(failedKueueDiscovery{})
	if err != nil {
		t.Fatal(err)
	}
	dynamic, err := k8score.NewDynamicResourceCache(k8score.DynamicCacheConfig{DynamicClient: dynamicfake.NewSimpleDynamicClient(runtime.NewScheme())})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(dynamic.Stop)
	p := &CacheProvider{dynamic: &k8s.DynamicResourceCache{DynamicResourceCache: dynamic}, discovery: &k8s.ResourceDiscovery{ResourceDiscovery: discovery}}
	got := p.kueueInventory(kueueGroup, kueueWorkloadKind, []string{"team"})
	if got.OK || got.Gap != "read_failed" {
		t.Fatalf("failed discovery read as absent Kueue: %+v", got)
	}
}
