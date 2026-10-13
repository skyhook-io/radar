package topology

import (
	"encoding/json"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"strings"
	"testing"
)

func TestPolicyRelationshipsIncludeEdgeLessSelectionAndUnknown(t *testing.T) {
	topo := &Topology{Nodes: []Node{
		{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app", "networkPolicyMatchesIncomplete": true}},
		{ID: "networkpolicy/app/all", Kind: KindNetworkPolicy, Name: "all", Data: map[string]any{"namespace": "app", "matchesAllPods": true}},
		{ID: "networkpolicy/other/all", Kind: KindNetworkPolicy, Name: "all", Data: map[string]any{"namespace": "other", "matchesAllPods": true}},
		{ID: "ciliumclusterwidenetworkpolicy/all", Kind: KindCiliumClusterwideNetworkPolicy, Name: "all", Data: map[string]any{"apiVersion": "cilium.io/v2", "matchesAllPods": true}},
		{ID: "calicoglobalnetworkpolicy/scoped", Kind: KindCalicoGlobalNetworkPolicy, Name: "scoped", Data: map[string]any{"apiVersion": "projectcalico.org/v3", "policyCoverageWorkloads": []string{"deployment/other/db"}}},
	}}
	for _, indexed := range []bool{false, true} {
		var idx *RelationshipsIndex
		if indexed {
			idx = IndexByResource(topo)
		}
		rel := GetRelationshipsWithIndex("deployments", "app", "web", topo, nil, nil, idx)
		if !rel.NetworkPolicyMatchesIncomplete || len(rel.NetworkPolicies) != 2 {
			t.Fatalf("indexed=%v: %+v", indexed, rel)
		}
		for _, p := range rel.NetworkPolicies {
			if p.Namespace == "other" || p.Name == "scoped" {
				t.Fatal("joined unrelated policy", p)
			}
		}
	}
}

func TestUnknownPolicySelectionSurvivesWithoutAnyRelationships(t *testing.T) {
	topo := &Topology{Nodes: []Node{{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app", "networkPolicyMatchesIncomplete": true}}}}
	rel := GetRelationships("deployments", "app", "web", topo, nil, nil)
	if rel == nil || !rel.NetworkPolicyMatchesIncomplete {
		t.Fatal("empty edges erased uncertainty")
	}
}

func TestUnknownPolicyProvenanceHonorsFilteredViews(t *testing.T) {
	hiddenID := "ciliumclusterwidenetworkpolicy/private"
	original := &Topology{Nodes: []Node{
		{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", networkPolicyMatchesIncompleteSources: []string{hiddenID}, Data: map[string]any{"namespace": "app", "networkPolicyMatchesIncomplete": true, "policyStatus": "unknown"}},
		{ID: hiddenID, Kind: KindCiliumClusterwideNetworkPolicy, Name: "private", Data: map[string]any{"apiVersion": "cilium.io/v2"}},
	}}
	sub := BuildNeighborhood(original, ResourceRef{Kind: "Deployment", Namespace: "app", Name: "web"}, NeighborhoodOptions{Allow: func(n *Node) bool { return n.Kind != KindCiliumClusterwideNetworkPolicy }})
	encoded, err := json.Marshal(sub)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), "private") || strings.Contains(string(encoded), "networkPolicyMatchesIncomplete") || strings.Contains(string(encoded), "policyStatus") {
		t.Fatal(string(encoded))
	}
	filtered := &Topology{Nodes: append([]Node(nil), original.Nodes...)}
	filtered.StripNodeIDs(map[string]bool{hiddenID: true})
	if filtered.Nodes[0].Data["networkPolicyMatchesIncomplete"] != nil {
		t.Fatal("hidden provenance survived strip")
	}
	if original.Nodes[0].Data["networkPolicyMatchesIncomplete"] != true || len(original.Nodes[0].networkPolicyMatchesIncompleteSources) != 1 {
		t.Fatal("mutated shared provenance")
	}
}

func TestPDBDoesNotClaimNetworkPolicySelection(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		workload := Node{ID: "deployment/app/web", Kind: KindDeployment, Data: map[string]any{"namespace": "app", "networkPolicyMatchesIncomplete": unknown}}
		nodes := []Node{workload, {ID: "pdb/app/availability", Kind: KindPDB}}
		annotateNodePolicyCoverage(nodes, []Edge{{Source: "pdb/app/availability", Target: workload.ID, Type: EdgeProtects}}, nil, nil, nil, nil)
		want := "unprotected"
		if unknown {
			want = "unknown"
		}
		if nodes[0].Data["policyStatus"] != want {
			t.Fatalf("PDB claimed policy selection: %v", nodes[0].Data)
		}
	}
}

func TestCachedCalicoCoverageKeepsStagedPoliciesSeparate(t *testing.T) {
	for _, definition := range calicoPolicyDefinitions {
		for _, group := range calicoPolicyGroups {
			t.Run(group+"/"+definition.kind, func(t *testing.T) {
				workloadID := "deployment/app/web"
				policy := Node{ID: "policy/selected", Kind: definition.nodeKind, Name: "selected", Data: map[string]any{
					"apiVersion": group + "/v3", "policyCoverageWorkloads": []string{workloadID},
				}}
				if definition.namespaced {
					policy.Data["namespace"] = "app"
				}
				graph := &Topology{Nodes: []Node{{ID: workloadID, Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app"}}, policy}}
				for _, indexed := range []bool{false, true} {
					var idx *RelationshipsIndex
					if indexed {
						idx = IndexByResource(graph)
					}
					rel := GetRelationshipsWithIndex("deployments", "app", "web", graph, nil, nil, idx)
					if rel == nil {
						t.Fatal("lost cached selection")
					}
					selected, other := rel.NetworkPolicies, rel.StagedPolicies
					if definition.staged {
						selected, other = rel.StagedPolicies, rel.NetworkPolicies
					}
					if len(selected) != 1 || len(other) != 0 || selected[0].Kind != definition.kind || selected[0].Group != group {
						t.Fatalf("indexed=%v: wrong policy projection: %+v", indexed, rel)
					}
				}
			})
		}
	}
}

func TestAllPodPolicySelectionRejectsCustomWorkloadKinds(t *testing.T) {
	for _, kind := range []NodeKind{KindDeployment, KindStatefulSet, KindDaemonSet} {
		for _, group := range []string{"apps", "custom.example.io"} {
			t.Run(group+"/"+string(kind), func(t *testing.T) {
				id := strings.ToLower(string(kind)) + "/app/web"
				if group != "apps" {
					id += "/" + group
				}
				graph := &Topology{Nodes: []Node{
					{ID: id, Kind: kind, Name: "web", Data: map[string]any{"namespace": "app", "apiVersion": group + "/v1"}},
					{ID: "networkpolicy/app/all", Kind: KindNetworkPolicy, Name: "all", Data: map[string]any{"namespace": "app", "apiVersion": "networking.k8s.io/v1", "matchesAllPods": true}},
				}}
				obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": group + "/v1", "kind": string(kind), "metadata": map[string]any{"namespace": "app", "name": "web"}}}
				for _, indexed := range []bool{false, true} {
					var idx *RelationshipsIndex
					if indexed {
						idx = IndexByResource(graph)
					}
					rel := GetRelationshipsWithObject(string(kind), "app", "web", obj, graph, nil, nil, idx)
					if group == "apps" {
						if rel == nil || len(rel.NetworkPolicies) != 1 {
							t.Fatalf("lost real workload selection: %+v", rel)
						}
					} else if rel != nil && len(rel.NetworkPolicies) != 0 {
						t.Fatalf("custom kind inherited Pod policies: %+v", rel)
					}
				}
			})
		}
	}
}

func TestPolicyProtectsUsesVisibleCachedTargetsWithoutEdges(t *testing.T) {
	for _, tc := range []struct {
		kind               NodeKind
		namespace, version string
		coverage           bool
		want               int
	}{
		{KindNetworkPolicy, "app", "networking.k8s.io/v1", false, 1},
		{KindCiliumNetworkPolicy, "app", "cilium.io/v2", false, 1},
		{KindCiliumClusterwideNetworkPolicy, "", "cilium.io/v2", false, 2},
		{KindCalicoGlobalNetworkPolicy, "", "projectcalico.org/v3", true, 1},
		{KindCalicoStagedGlobalNetworkPolicy, "", "projectcalico.org/v3", true, 1},
	} {
		t.Run(string(tc.kind), func(t *testing.T) {
			policy := Node{ID: "policy/" + tc.namespace + "/all", Kind: tc.kind, Name: "all", Data: map[string]any{"namespace": tc.namespace, "apiVersion": tc.version, "matchesAllPods": true}}
			if tc.coverage {
				policy.Data["policyCoverageWorkloads"] = []string{"deployment/other/db", "deployment/hidden/private"}
			}
			graph := &Topology{Nodes: []Node{
				policy,
				{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app", "apiVersion": "apps/v1"}},
				{ID: "deployment/other/db", Kind: KindDeployment, Name: "db", Data: map[string]any{"namespace": "other", "apiVersion": "apps/v1"}},
				{ID: "deployment/app/shadow/custom.example.io", Kind: KindDeployment, Name: "shadow", Data: map[string]any{"namespace": "app", "apiVersion": "custom.example.io/v1"}},
			}}
			ref := resourceRefForNode(&policy, nil)
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": tc.version, "kind": ref.Kind, "metadata": map[string]any{"namespace": tc.namespace, "name": "all"}}}
			for _, indexed := range []bool{false, true} {
				var idx *RelationshipsIndex
				if indexed {
					idx = IndexByResource(graph)
				}
				rel := GetRelationshipsWithObject(ref.Kind, tc.namespace, "all", obj, graph, nil, nil, idx)
				if rel == nil || len(rel.Protects) != tc.want {
					t.Fatalf("indexed=%v: expected %d visible targets: %+v", indexed, tc.want, rel)
				}
				for _, target := range rel.Protects {
					if target.Group != "apps" || target.Name == "private" || target.Name == "shadow" {
						t.Fatalf("selected an unavailable or custom target: %+v", target)
					}
					if !tc.coverage && tc.namespace != "" && target.Namespace != tc.namespace {
						t.Fatalf("selected another namespace: %+v", target)
					}
				}
			}
			if len(graph.Edges) != 0 {
				t.Fatal("relationship recovery added dense graph edges")
			}
		})
	}
}

func TestManagementProjectionSkipsBroadPolicyTargets(t *testing.T) {
	graph := &Topology{Nodes: []Node{
		{ID: "networkpolicy/app/all", Kind: KindNetworkPolicy, Name: "all", Data: map[string]any{"namespace": "app", "apiVersion": "networking.k8s.io/v1", "matchesAllPods": true}},
		{ID: "deployment/app/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "app", "apiVersion": "apps/v1"}},
		{ID: "kustomization/app/config", Kind: KindKustomization, Name: "config", Data: map[string]any{"namespace": "app", "apiVersion": "kustomize.toolkit.fluxcd.io/v1"}},
	}, Edges: []Edge{{Source: "kustomization/app/config", Target: "networkpolicy/app/all", Type: EdgeManages}}}
	full := GetRelationships("NetworkPolicy", "app", "all", graph, nil, nil)
	summary := GetManagementRelationshipsWithObject("NetworkPolicy", "app", "all", nil, graph, nil, nil, IndexByResource(graph))
	if full == nil || summary == nil || len(full.Protects) != 1 || len(summary.Protects) != 0 || summary.Owner == nil || *summary.Owner != *full.Owner {
		t.Fatalf("management projection diverged: full=%+v summary=%+v", full, summary)
	}
}
