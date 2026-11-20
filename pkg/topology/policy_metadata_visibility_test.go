package topology

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

func TestStripPolicyEvidenceCopiesSharedData(t *testing.T) {
	original := &Topology{Nodes: []Node{
		{ID: "deployment/app/web", Kind: KindDeployment, Data: map[string]any{"namespace": "app", "policyStatus": "protected"}},
		{ID: "deployment/hidden/db", Kind: KindDeployment, Data: map[string]any{"namespace": "hidden"}},
		{ID: "calicoglobalnetworkpolicy/all", Kind: KindCalicoGlobalNetworkPolicy, Data: map[string]any{"policyCoverageWorkloads": []string{"deployment/app/web", "deployment/hidden/db"}}},
		{ID: "ciliumnetworkpolicy/app/hidden", Kind: KindCiliumNetworkPolicy, Data: map[string]any{"namespace": "app"}},
	}}
	filtered := &Topology{Nodes: append([]Node(nil), original.Nodes...)}
	filtered.StripNodeIDs(map[string]bool{"deployment/hidden/db": true, "ciliumnetworkpolicy/app/hidden": true})
	encoded, err := json.Marshal(filtered)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "hidden") || strings.Contains(string(encoded), "policyStatus") {
		t.Fatal(string(encoded))
	}
	if original.Nodes[0].Data["policyStatus"] != "protected" || len(original.Nodes[2].Data["policyCoverageWorkloads"].([]string)) != 2 {
		t.Fatal("modified shared cache")
	}
}

func TestPolicyFilteringDoesNotWriteUnqualifiedSharedData(t *testing.T) {
	for _, status := range []string{"absent", "nil", "protected"} {
		t.Run(status, func(t *testing.T) {
			data := map[string]any{"namespace": "app", "label": "public"}
			expected := map[string]any{"namespace": "app", "label": "public"}
			if status == "nil" {
				data["policyStatus"] = nil
				expected["policyStatus"] = nil
			}
			if status == "protected" {
				data["policyStatus"] = "protected"
				expected["policyStatus"] = "protected"
			}
			original := &Topology{Nodes: []Node{{ID: "service/app/web", Kind: KindService, Data: data}, {ID: "ciliumnetworkpolicy/app/private", Kind: KindCiliumNetworkPolicy, Data: map[string]any{"namespace": "app"}}}}
			var workers sync.WaitGroup
			workers.Add(2)
			go func() {
				defer workers.Done()
				for i := 0; i < 1000; i++ {
					for key, value := range original.Nodes[0].Data {
						_ = key
						_ = value
					}
				}
			}()
			go func() {
				defer workers.Done()
				for i := 0; i < 1000; i++ {
					filtered := &Topology{Nodes: append([]Node(nil), original.Nodes...)}
					filtered.StripNodeIDs(map[string]bool{"ciliumnetworkpolicy/app/private": true})
				}
			}()
			workers.Wait()
			if !reflect.DeepEqual(data, expected) {
				t.Fatalf("shared data mutated: %#v", data)
			}
		})
	}
}

// The walk's display data is built from the admitted objects alone, so the
// coverage a policy node would carry names only what this caller was admitted
// to: the walk returns neither it nor a workload's coverage badge.
func TestWalkNeighborhoodDoesNotExposeInternalCoverageIDs(t *testing.T) {
	policy := charUnstructured("projectcalico.org/v3", "GlobalNetworkPolicy", "", "all", nil, map[string]any{"selector": "all()"}, nil)
	dep := charDeployment("app", "web", 1, map[string]string{"app": "web"}, charPodSpec("", ""))
	d := newCharDynamic()
	d.add("projectcalico.org", "v3", "GlobalNetworkPolicy", "globalnetworkpolicies", false, policy)
	d.add("apps", "v1", "Deployment", "deployments", true)
	f := &charFixture{
		provider: &mockProvider{deployments: []*appsv1.Deployment{dep}},
		dynamic:  d,
		objects: []charObject{
			{group: "projectcalico.org", kind: "GlobalNetworkPolicy", resource: "globalnetworkpolicies", obj: policy},
			{group: "apps", kind: "Deployment", resource: "deployments", obj: dep},
		},
	}
	for _, root := range []ResourceRef{{Kind: "GlobalNetworkPolicy", Group: "projectcalico.org", Name: "all"}, {Kind: "Deployment", Group: "apps", Namespace: "app", Name: "web"}} {
		sub, err := WalkNeighborhood(context.Background(), neighborhoodFixtureSession(t, f), root, NeighborhoodOptions{Profile: ProfileAll, Allow: func(*Node) bool { return true }}, f.provider, f.dynamic)
		if err != nil || len(sub.Nodes) == 0 {
			t.Fatalf("%s: %v %+v", root.Kind, err, sub)
		}
		for _, n := range sub.Nodes {
			if _, ok := n.Data["policyCoverageWorkloads"]; ok {
				t.Errorf("%s: internal coverage IDs escaped on %s", root.Kind, n.ID)
			}
			if _, ok := n.Data["policyStatus"]; ok {
				t.Errorf("%s: coverage badge escaped on %s", root.Kind, n.ID)
			}
		}
	}
}

// A workload's coverage badge would be computed without a policy the caller
// may not read, so the walk withholds it.
func TestWalkNeighborhoodWithholdsUnqualifiedCoverageBadge(t *testing.T) {
	neighborhoodDisplayPolicyEffect = true
	t.Cleanup(func() { neighborhoodDisplayPolicyEffect = false })
	dep := projectionObject(&appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app", Labels: map[string]string{"app": "web"}}}, "apps", "Deployment")
	hidden := projectionCR("cilium.io", "CiliumClusterwideNetworkPolicy", "", "hidden", map[string]any{"spec": map[string]any{"endpointSelector": map[string]any{}}})
	displayed, _, err := neighborhoodDisplayNodes(map[resourceid.Ref]runtime.Object{projectionRef(dep): dep}, nil, nil)
	if _, ok := displayed[projectionRef(dep)].Data["policyStatus"]; err != nil || !ok {
		t.Fatalf("the fixture no longer computes a coverage badge: %+v %v", displayed, err)
	}
	sub := walkTestNeighborhood(t, newProjectionSession(t, dep, hidden), dep, NeighborhoodOptions{Allow: func(n *Node) bool { return n.Kind != KindCiliumClusterwideNetworkPolicy }})
	if len(sub.Nodes) != 1 || sub.Nodes[0].Kind != KindDeployment {
		t.Fatalf("nodes=%+v", sub.Nodes)
	}
	if _, ok := sub.Nodes[0].Data["policyStatus"]; ok {
		t.Fatal("badge computed with unverified policy evidence escaped")
	}
}
