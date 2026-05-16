package topology

import (
	"testing"
)

// TestGetRelationships_IncomingEdgeProtects_DispatchesByKind verifies that
// incoming "protects" edges split into rel.PDBs vs rel.NetworkPolicies based
// on the source kind, and that rel.Policies stays populated as the union for
// the deprecation period (so old clients/hub-fed responses keep working).
func TestGetRelationships_IncomingEdgeProtects_DispatchesByKind(t *testing.T) {
	// A Deployment receives incoming EdgeProtects from a PDB, a NetworkPolicy,
	// and a CiliumNetworkPolicy. Each should land in its appropriate field;
	// all three should appear in rel.Policies (deprecated union).
	// (Cluster-scoped NetworkPolicy variants like ClusterNetworkPolicy and
	// CiliumClusterwideNetworkPolicy use a 2-segment node ID — parseNodeID
	// rejects those today, so they never reach this dispatch. That's
	// pre-existing behavior and out of scope for this change.)
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
			{ID: "poddisruptionbudget/demo/web-pdb", Kind: KindPDB, Name: "web-pdb"},
			{ID: "networkpolicy/demo/web-np", Kind: KindNetworkPolicy, Name: "web-np"},
			{ID: "ciliumnetworkpolicy/demo/web-cnp", Kind: KindCiliumNetworkPolicy, Name: "web-cnp"},
		},
		Edges: []Edge{
			{ID: "pdb-to-web", Source: "poddisruptionbudget/demo/web-pdb", Target: "deployment/demo/web", Type: EdgeProtects},
			{ID: "np-to-web", Source: "networkpolicy/demo/web-np", Target: "deployment/demo/web", Type: EdgeProtects},
			{ID: "cnp-to-web", Source: "ciliumnetworkpolicy/demo/web-cnp", Target: "deployment/demo/web", Type: EdgeProtects},
		},
	}

	rel := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
	if rel == nil {
		t.Fatal("GetRelationships returned nil for deployment with 3 incoming protects edges")
	}

	if len(rel.PDBs) != 1 || rel.PDBs[0].Kind != "PodDisruptionBudget" || rel.PDBs[0].Name != "web-pdb" {
		t.Errorf("rel.PDBs: want [PodDisruptionBudget/web-pdb], got %+v", rel.PDBs)
	}

	if len(rel.NetworkPolicies) != 2 {
		t.Fatalf("rel.NetworkPolicies: want 2 entries (NetworkPolicy + CiliumNetworkPolicy), got %d (%+v)", len(rel.NetworkPolicies), rel.NetworkPolicies)
	}
	gotKinds := make(map[string]bool, 2)
	for _, ref := range rel.NetworkPolicies {
		gotKinds[ref.Kind] = true
	}
	for _, expected := range []string{"NetworkPolicy", "CiliumNetworkPolicy"} {
		if !gotKinds[expected] {
			t.Errorf("rel.NetworkPolicies missing %s; got kinds=%v", expected, gotKinds)
		}
	}

	// rel.Policies must contain the union of all three, since we keep it
	// populated for one release as a deprecated alias.
	if len(rel.Policies) != 3 {
		t.Errorf("rel.Policies (deprecated union): want 3 entries, got %d (%+v)", len(rel.Policies), rel.Policies)
	}
}

// TestGetRelationships_OutgoingEdgeProtects_NotSurfaced verifies that outgoing
// EdgeProtects edges (a PDB / NetworkPolicy / CiliumNetworkPolicy / etc. pointing
// at the workloads it protects) are intentionally NOT projected into the
// Relationships of the source resource. The PDBs / NetworkPolicies fields are
// reserved for the INCOMING-direction semantic ("things that act on me").
//
// Surfacing the outgoing direction requires a new Protects/SelectedWorkloads
// field, which is out of scope here. Until that field lands, querying a PDB
// or NetworkPolicy that has only outgoing protects edges returns nil.
//
// This also guards B1 (the old bug that wrote outgoing protects into
// rel.ScaleTarget) and the post-B1 over-fix (writing them into rel.PDBs,
// which conflated PDB-side and NP-side outgoing edges).
func TestGetRelationships_OutgoingEdgeProtects_NotSurfaced(t *testing.T) {
	cases := []struct {
		name     string
		queryKnd string
		sourceID string
		sourceKd NodeKind
	}{
		{"PDB outgoing", "PodDisruptionBudget", "poddisruptionbudget/demo/web-pdb", KindPDB},
		{"NetworkPolicy outgoing", "NetworkPolicy", "networkpolicy/demo/deny-egress", KindNetworkPolicy},
		{"CiliumNetworkPolicy outgoing", "CiliumNetworkPolicy", "ciliumnetworkpolicy/demo/cnp-1", KindCiliumNetworkPolicy},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			topo := &Topology{
				Nodes: []Node{
					{ID: c.sourceID, Kind: c.sourceKd, Name: "src"},
					{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
					{ID: "deployment/demo/api", Kind: KindDeployment, Name: "api"},
				},
				Edges: []Edge{
					{ID: "src-to-web", Source: c.sourceID, Target: "deployment/demo/web", Type: EdgeProtects},
					{ID: "src-to-api", Source: c.sourceID, Target: "deployment/demo/api", Type: EdgeProtects},
				},
			}

			rel := GetRelationships(c.queryKnd, "demo", "src", topo, nil, nil)
			if rel != nil {
				t.Errorf("want nil (outgoing protects intentionally not surfaced), got %+v", rel)
			}
		})
	}
}

// TestGetRelationships_NoProtects_FieldsOmitted ensures the new split fields
// stay nil when no protects edges exist, so JSON omitempty keeps the wire
// format identical for unrelated resources.
func TestGetRelationships_NoProtects_FieldsOmitted(t *testing.T) {
	topo := &Topology{
		Nodes: []Node{
			{ID: "deployment/demo/lone", Kind: KindDeployment, Name: "lone"},
			{ID: "replicaset/demo/lone-abc", Kind: KindReplicaSet, Name: "lone-abc"},
		},
		Edges: []Edge{
			{ID: "lone-rs", Source: "deployment/demo/lone", Target: "replicaset/demo/lone-abc", Type: EdgeManages},
		},
	}

	rel := GetRelationships("Deployment", "demo", "lone", topo, nil, nil)
	if rel == nil {
		t.Fatal("GetRelationships returned nil for deployment with a child")
	}
	if len(rel.PDBs) != 0 {
		t.Errorf("rel.PDBs: want empty, got %+v", rel.PDBs)
	}
	if len(rel.NetworkPolicies) != 0 {
		t.Errorf("rel.NetworkPolicies: want empty, got %+v", rel.NetworkPolicies)
	}
	if len(rel.Policies) != 0 {
		t.Errorf("rel.Policies: want empty, got %+v", rel.Policies)
	}
}
