package topology

import (
	"testing"
)

// TestGetRelationships_IncomingEdgeProtects_DispatchesByKind verifies that
// incoming "protects" edges split into rel.PDBs vs rel.NetworkPolicies based
// on the source kind.
//
// (Cluster-scoped NetworkPolicy variants like ClusterNetworkPolicy and
// CiliumClusterwideNetworkPolicy use a 2-segment node ID — parseNodeID
// rejects those today, so they never reach this dispatch. Pre-existing
// behavior, out of scope here.)
func TestGetRelationships_IncomingEdgeProtects_DispatchesByKind(t *testing.T) {
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
		name       string
		queryKind  string
		queryName  string // must match the name component of sourceID below
		sourceID   string
		sourceKind NodeKind
	}{
		{"PDB outgoing", "PodDisruptionBudget", "web-pdb", "poddisruptionbudget/demo/web-pdb", KindPDB},
		{"NetworkPolicy outgoing", "NetworkPolicy", "deny-egress", "networkpolicy/demo/deny-egress", KindNetworkPolicy},
		{"CiliumNetworkPolicy outgoing", "CiliumNetworkPolicy", "cnp-1", "ciliumnetworkpolicy/demo/cnp-1", KindCiliumNetworkPolicy},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			topo := &Topology{
				Nodes: []Node{
					{ID: c.sourceID, Kind: c.sourceKind, Name: c.queryName},
					{ID: "deployment/demo/web", Kind: KindDeployment, Name: "web"},
					{ID: "deployment/demo/api", Kind: KindDeployment, Name: "api"},
				},
				Edges: []Edge{
					{ID: "src-to-web", Source: c.sourceID, Target: "deployment/demo/web", Type: EdgeProtects},
					{ID: "src-to-api", Source: c.sourceID, Target: "deployment/demo/api", Type: EdgeProtects},
				},
			}

			// Control: the SAME topology, queried from the workload side, MUST
			// surface the policy via incoming-EdgeProtects dispatch. If this
			// fails, the test below would pass for the wrong reason — the
			// edges or node IDs aren't matching at all. Catches the
			// vacuous-pass class of mistakes.
			incoming := GetRelationships("Deployment", "demo", "web", topo, nil, nil)
			if incoming == nil {
				t.Fatalf("control assertion failed: querying the target Deployment should surface the policy via incoming EdgeProtects, got nil relationships")
			}
			switch c.sourceKind {
			case KindPDB:
				if len(incoming.PDBs) == 0 {
					t.Fatalf("control: expected workload to see incoming PDB, got %+v", incoming)
				}
			case KindNetworkPolicy, KindCiliumNetworkPolicy:
				if len(incoming.NetworkPolicies) == 0 {
					t.Fatalf("control: expected workload to see incoming NetworkPolicy, got %+v", incoming)
				}
			}

			// Actual assertion: querying from the source policy side should
			// NOT surface its targets (outgoing direction intentionally
			// unsurfaced until a Protects[] field exists).
			rel := GetRelationships(c.queryKind, "demo", c.queryName, topo, nil, nil)
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
}
