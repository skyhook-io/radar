package topology

import (
	"encoding/json"
	"reflect"
	"strings"
	"sync"
	"testing"
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

func TestAuthorizedNeighborhoodDoesNotExposeInternalCoverageIDs(t *testing.T) {
	topo := &Topology{Nodes: []Node{{ID: "globalnetworkpolicy//all", Kind: KindCalicoGlobalNetworkPolicy, Name: "all", Data: map[string]any{"apiVersion": "projectcalico.org/v3", "policyCoverageWorkloads": []string{"deployment/private/secret-workload"}}}}}
	sub := BuildNeighborhood(topo, ResourceRef{Kind: "GlobalNetworkPolicy", Name: "all", Group: "projectcalico.org"}, NeighborhoodOptions{Allow: func(*Node) bool { return true }})
	if len(sub.Nodes) != 1 || sub.Nodes[0].Data["policyCoverageWorkloads"] != nil {
		t.Fatalf("internal IDs escaped: %+v", sub)
	}
	if topo.Nodes[0].Data["policyCoverageWorkloads"] == nil {
		t.Fatal("mutated cached metadata")
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

func TestAuthorizedNeighborhoodWithholdsUnqualifiedCoverageBadge(t *testing.T) {
	original := &Topology{Nodes: []Node{
		{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app", "group": "apps", "policyStatus": "protected"}},
		{ID: "ciliumclusterwidenetworkpolicy//hidden", Kind: KindCiliumClusterwideNetworkPolicy, Name: "hidden", Data: map[string]any{"group": "cilium.io", "matchesAllPods": true}},
	}}
	ref := ResourceRef{Kind: "Deployment", Group: "apps", Namespace: "app", Name: "web"}
	sub := BuildNeighborhood(original, ref, NeighborhoodOptions{Hops: 0, Allow: func(n *Node) bool { return n.Kind != KindCiliumClusterwideNetworkPolicy }})
	if len(sub.Nodes) != 1 {
		t.Fatalf("nodes=%+v", sub.Nodes)
	}
	if _, ok := sub.Nodes[0].Data["policyStatus"]; ok {
		t.Fatal("badge computed with unverified policy evidence escaped")
	}
	if original.Nodes[0].Data["policyStatus"] != "protected" {
		t.Fatal("mutated shared annotation")
	}
	unfiltered := BuildNeighborhood(original, ref, NeighborhoodOptions{Hops: 0})
	if len(unfiltered.Nodes) != 1 || unfiltered.Nodes[0].Data["policyStatus"] != "protected" {
		t.Fatal("changed unfiltered neighborhood")
	}
}
