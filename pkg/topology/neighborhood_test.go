package topology

import (
	"sort"
	"testing"
)

// makeNode is a tiny helper for the BFS tests. It assembles a Node with a
// matching ID and namespace data so BuildNeighborhood's lookups work.
func makeNode(kind NodeKind, ns, name string) Node {
	idKind := normalizeIDKind(kind)
	id := idKind + "/" + ns + "/" + name
	return Node{
		ID:     id,
		Kind:   kind,
		Name:   name,
		Status: StatusHealthy,
		Data:   map[string]any{"namespace": ns},
	}
}

func normalizeIDKind(kind NodeKind) string {
	// Mirror buildNodeID's lowercase-kind convention. For the kinds these
	// tests use, the lowercase form is the ID prefix.
	switch kind {
	case KindPod:
		return "pod"
	case KindService:
		return "service"
	case KindDeployment:
		return "deployment"
	case KindReplicaSet:
		return "replicaset"
	case KindIngress:
		return "ingress"
	case KindHTTPRoute:
		return "httproute"
	case KindPDB:
		return "poddisruptionbudget"
	case KindNetworkPolicy:
		return "networkpolicy"
	case KindHPA:
		return "horizontalpodautoscaler"
	case KindConfigMap:
		return "configmap"
	case KindSecret:
		return "secret"
	case KindApplication:
		return "application"
	case KindNode:
		return "node"
	}
	return string(kind)
}

func makeEdge(typ EdgeType, source, target string) Edge {
	return Edge{
		ID:     source + "-" + string(typ) + "-" + target,
		Source: source,
		Target: target,
		Type:   typ,
	}
}

// podNeighborhood builds a representative topology for a Pod with surrounding
// owner chain, exposing service + ingress, attached PDB and NetworkPolicy.
//
//	Ingress  → Service → Pod
//	Deployment → ReplicaSet → Pod
//	PDB → Pod, NetworkPolicy → Pod (EdgeProtects)
//	Pod uses ConfigMap (EdgeConfigures)
func podNeighborhood() *Topology {
	pod := makeNode(KindPod, "prod", "cart-xyz")
	rs := makeNode(KindReplicaSet, "prod", "cart-rs")
	dep := makeNode(KindDeployment, "prod", "cart")
	svc := makeNode(KindService, "prod", "cart")
	ing := makeNode(KindIngress, "prod", "cart")
	pdb := makeNode(KindPDB, "prod", "cart-pdb")
	np := makeNode(KindNetworkPolicy, "prod", "cart-allow")
	cm := makeNode(KindConfigMap, "prod", "cart-config")

	return &Topology{
		Nodes: []Node{pod, rs, dep, svc, ing, pdb, np, cm},
		Edges: []Edge{
			makeEdge(EdgeManages, dep.ID, rs.ID),
			makeEdge(EdgeManages, rs.ID, pod.ID),
			makeEdge(EdgeExposes, svc.ID, pod.ID),
			makeEdge(EdgeRoutesTo, ing.ID, svc.ID),
			makeEdge(EdgeProtects, pdb.ID, pod.ID),
			makeEdge(EdgeProtects, np.ID, pod.ID),
			makeEdge(EdgeConfigures, cm.ID, pod.ID),
		},
	}
}

func nodeIDs(s *Subgraph) []string {
	ids := make([]string, 0, len(s.Nodes))
	for _, n := range s.Nodes {
		ids = append(ids, n.ID)
	}
	sort.Strings(ids)
	return ids
}

func edgeIDs(s *Subgraph) []string {
	ids := make([]string, 0, len(s.Edges))
	for _, e := range s.Edges {
		ids = append(ids, e.ID)
	}
	sort.Strings(ids)
	return ids
}

func TestBuildNeighborhood_PodManagementProfile(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileManagement,
		Hops:    1,
	})
	got := nodeIDs(sub)
	want := []string{"pod/prod/cart-xyz", "replicaset/prod/cart-rs"}
	if !equalStrings(got, want) {
		t.Errorf("management 1-hop nodes = %v, want %v", got, want)
	}
	if sub.Truncated {
		t.Errorf("did not expect truncated")
	}
}

func TestBuildNeighborhood_PodManagementProfileTwoHops(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileManagement,
		Hops:    2,
	})
	got := nodeIDs(sub)
	want := []string{
		"deployment/prod/cart",
		"pod/prod/cart-xyz",
		"replicaset/prod/cart-rs",
	}
	if !equalStrings(got, want) {
		t.Errorf("management 2-hop nodes = %v, want %v", got, want)
	}
}

func TestBuildNeighborhood_PodNetworkingProfile(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileNetworking,
		Hops:    2,
	})
	got := nodeIDs(sub)
	want := []string{
		"ingress/prod/cart",
		"pod/prod/cart-xyz",
		"service/prod/cart",
	}
	if !equalStrings(got, want) {
		t.Errorf("networking 2-hop nodes = %v, want %v", got, want)
	}
}

func TestBuildNeighborhood_PolicyProfile(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfilePolicy,
		Hops:    1,
	})
	got := nodeIDs(sub)
	want := []string{
		"networkpolicy/prod/cart-allow",
		"pod/prod/cart-xyz",
		"poddisruptionbudget/prod/cart-pdb",
	}
	if !equalStrings(got, want) {
		t.Errorf("policy 1-hop nodes = %v, want %v", got, want)
	}
}

func TestBuildNeighborhood_SecurityProfileEmpty(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileSecurity,
		Hops:    1,
	})
	got := nodeIDs(sub)
	want := []string{"pod/prod/cart-xyz"}
	if !equalStrings(got, want) {
		t.Errorf("security profile should expand to root only, got %v", got)
	}
	if len(sub.Edges) != 0 {
		t.Errorf("security profile should have no edges, got %d", len(sub.Edges))
	}
}

func TestBuildNeighborhood_AutoForPod(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileAuto,
		Hops:    1,
	})
	// Auto for Pod = management + networking + policy. ReplicaSet (manages),
	// Service (exposes), PDB + NP (protects) all reachable in 1 hop. The
	// ConfigMap is via EdgeConfigures and should NOT appear under auto.
	got := nodeIDs(sub)
	want := []string{
		"networkpolicy/prod/cart-allow",
		"pod/prod/cart-xyz",
		"poddisruptionbudget/prod/cart-pdb",
		"replicaset/prod/cart-rs",
		"service/prod/cart",
	}
	if !equalStrings(got, want) {
		t.Errorf("auto pod 1-hop nodes = %v, want %v", got, want)
	}
	// EdgeConfigures should not be present even though ConfigMap is excluded.
	for _, e := range sub.Edges {
		if e.Type == EdgeConfigures {
			t.Errorf("auto profile for Pod must not include EdgeConfigures: %+v", e)
		}
	}
}

func TestBuildNeighborhood_AutoForApplication(t *testing.T) {
	// Application → Deployment via EdgeManages.
	app := makeNode(KindApplication, "argocd", "cart")
	dep := makeNode(KindDeployment, "prod", "cart")
	svc := makeNode(KindService, "prod", "cart")
	topo := &Topology{
		Nodes: []Node{app, dep, svc},
		Edges: []Edge{
			makeEdge(EdgeManages, app.ID, dep.ID),
			makeEdge(EdgeExposes, svc.ID, dep.ID),
		},
	}

	sub := BuildNeighborhood(topo, ResourceRef{Kind: "Application", Namespace: "argocd", Name: "cart"}, NeighborhoodOptions{
		Profile: ProfileAuto,
		Hops:    1,
	})
	got := nodeIDs(sub)
	want := []string{"application/argocd/cart", "deployment/prod/cart"}
	if !equalStrings(got, want) {
		t.Errorf("auto Application 1-hop nodes = %v, want %v", got, want)
	}
}

func TestBuildNeighborhood_AutoForService(t *testing.T) {
	topo := podNeighborhood()
	sub := BuildNeighborhood(topo, ResourceRef{Kind: "Service", Namespace: "prod", Name: "cart"}, NeighborhoodOptions{
		Profile: ProfileAuto,
		Hops:    1,
	})
	got := nodeIDs(sub)
	// Service auto profile = networking. Should walk to Pod (it exposes)
	// and Ingress (routes-to Service). Should NOT walk to ReplicaSet
	// (manages) even though it's adjacent.
	want := []string{
		"ingress/prod/cart",
		"pod/prod/cart-xyz",
		"service/prod/cart",
	}
	if !equalStrings(got, want) {
		t.Errorf("auto Service 1-hop nodes = %v, want %v", got, want)
	}
}

func TestBuildNeighborhood_AutoForPDB(t *testing.T) {
	topo := podNeighborhood()
	sub := BuildNeighborhood(topo, ResourceRef{Kind: "PodDisruptionBudget", Namespace: "prod", Name: "cart-pdb"}, NeighborhoodOptions{
		Profile: ProfileAuto,
		Hops:    1,
	})
	got := nodeIDs(sub)
	want := []string{
		"pod/prod/cart-xyz",
		"poddisruptionbudget/prod/cart-pdb",
	}
	if !equalStrings(got, want) {
		t.Errorf("auto PDB 1-hop nodes = %v, want %v", got, want)
	}
}

func TestBuildNeighborhood_MaxNodesTriggersTruncation(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	// MaxNodes=2 — only the root and one neighbor will fit, even though many
	// are reachable in 1 hop under ProfileAll.
	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile:  ProfileAll,
		Hops:     2,
		MaxNodes: 2,
	})
	if !sub.Truncated {
		t.Errorf("expected Truncated=true, got false")
	}
	if len(sub.Nodes) != 2 {
		t.Errorf("expected exactly 2 nodes under MaxNodes=2, got %d (%v)", len(sub.Nodes), nodeIDs(sub))
	}
	if sub.Nodes[0].ID != "pod/prod/cart-xyz" {
		t.Errorf("expected root first, got %s", sub.Nodes[0].ID)
	}
}

func TestBuildNeighborhood_DefaultsApplied(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{})
	if len(sub.Nodes) == 0 {
		t.Fatalf("expected non-empty neighborhood under default options")
	}
	// Default hops=1 — must include root and at least one neighbor; should
	// NOT include the Deployment (which is 2 hops away).
	if sub.Nodes[0].ID != "pod/prod/cart-xyz" {
		t.Errorf("expected root first, got %s", sub.Nodes[0].ID)
	}
	for _, n := range sub.Nodes {
		if n.Kind == KindDeployment {
			t.Errorf("default hops=1 should not include Deployment (2 hops away)")
		}
	}
}

func TestBuildNeighborhood_HopsClampedToMax(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	// Hops=99 should be clamped to neighborhoodMaxHops=2. Under
	// management with 2 hops we reach Deployment but no further.
	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileManagement,
		Hops:    99,
	})
	got := nodeIDs(sub)
	want := []string{
		"deployment/prod/cart",
		"pod/prod/cart-xyz",
		"replicaset/prod/cart-rs",
	}
	if !equalStrings(got, want) {
		t.Errorf("hops clamp: got %v, want %v", got, want)
	}
}

func TestBuildNeighborhood_RootMissingReturnsEmpty(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "does-not-exist"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{Profile: ProfileAuto})
	if sub == nil {
		t.Fatal("expected non-nil subgraph for missing root")
	}
	if len(sub.Nodes) != 0 {
		t.Errorf("expected empty nodes for missing root, got %v", nodeIDs(sub))
	}
	if sub.Root.Name != "does-not-exist" {
		t.Errorf("subgraph.Root should echo the requested root")
	}
}

// TestBuildNeighborhood_UnknownProfileDefaultsToAuto verifies that an
// unrecognized profile string (typo, future profile name from an older
// client) is normalized to ProfileAuto rather than silently expanding
// to ProfileAll. Empty profile gets the same treatment so direct lib
// callers that don't pre-default through a handler see the documented
// "Profile=auto" default.
func TestBuildNeighborhood_UnknownProfileDefaultsToAuto(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	// Reference: ProfileAuto for a Pod traverses management + networking + policy.
	auto := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileAuto,
		Hops:    2,
	})
	autoNodeIDs := nodeIDs(auto)

	cases := []struct {
		name    string
		profile Profile
	}{
		{"empty profile", ""},
		{"unknown profile string", "banana"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := BuildNeighborhood(topo, root, NeighborhoodOptions{
				Profile: c.profile,
				Hops:    2,
			})
			if !equalStrings(nodeIDs(got), autoNodeIDs) {
				t.Errorf("%q profile should match ProfileAuto:\n  got:  %v\n  want: %v", c.profile, nodeIDs(got), autoNodeIDs)
			}
		})
	}

	// Sanity: ProfileAll is broader than auto for a Pod (auto excludes
	// EdgeUses + EdgeConfigures). Confirms our "unknown→auto" guard
	// actually narrows the expansion vs. the previous "unknown→all"
	// behavior.
	all := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileAll,
		Hops:    2,
	})
	if len(nodeIDs(all)) <= len(autoNodeIDs) {
		t.Errorf("expected ProfileAll to cover at least as many nodes as ProfileAuto for a Pod root; got all=%d auto=%d", len(nodeIDs(all)), len(autoNodeIDs))
	}
}

// TestBuildNeighborhood_AllowSkipsForbidden verifies that nodes for which
// Allow returns false are skipped during BFS — they don't appear in the
// output AND don't consume the MaxNodes budget. This is the security
// boundary: forbidden nodes must not influence the visible graph.
func TestBuildNeighborhood_AllowSkipsForbidden(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	// Deny ReplicaSet — forbidden during BFS.
	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileAll,
		Hops:    2,
		Allow: func(n *Node) bool {
			return n.Kind != KindReplicaSet
		},
	})

	for _, n := range sub.Nodes {
		if n.Kind == KindReplicaSet {
			t.Errorf("BFS surfaced a forbidden ReplicaSet node: %s", n.ID)
		}
	}
	if sub.RBACDenied != 1 {
		t.Errorf("expected RBACDenied=1 (the single ReplicaSet), got %d", sub.RBACDenied)
	}
}

// TestBuildNeighborhood_AllowPreventsPathFragments verifies the path-fragment
// guarantee: a forbidden node cannot serve as a bridge between two allowed
// nodes the user reaches via BFS. Without pre-filtering, BFS would traverse
// through the forbidden node and surface its downstream allowed neighbors
// (leaking that the forbidden node connects them).
//
// Topology: Pod → ReplicaSet → Deployment (management chain). With
// ReplicaSet forbidden, BFS from Pod must NOT reach Deployment.
func TestBuildNeighborhood_AllowPreventsPathFragments(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileManagement,
		Hops:    2,
		Allow: func(n *Node) bool {
			return n.Kind != KindReplicaSet
		},
	})

	for _, n := range sub.Nodes {
		if n.Kind == KindDeployment {
			t.Errorf("BFS reached Deployment through a forbidden ReplicaSet — path-fragment leak: %s", n.ID)
		}
	}
}

// TestBuildNeighborhood_AllowProtectsBudget verifies that forbidden nodes
// don't consume the MaxNodes truncation budget. Without pre-filtering, a
// run of forbidden nodes near the root could exhaust the budget and cause
// allowed nodes further out to be truncated — a side-channel leak.
func TestBuildNeighborhood_AllowProtectsBudget(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	// With ReplicaSet denied AND MaxNodes=2, BFS should NOT count the
	// denied node toward the budget. We should still fit the root +
	// another allowed node, instead of root + denied node (which would
	// trip truncation with zero allowed neighbors).
	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile:  ProfileAll,
		Hops:     2,
		MaxNodes: 2,
		Allow: func(n *Node) bool {
			return n.Kind != KindReplicaSet
		},
	})

	// At least the root must be present.
	if len(sub.Nodes) == 0 {
		t.Fatal("expected non-empty subgraph; got zero nodes")
	}
	if sub.Nodes[0].Kind != "Pod" {
		t.Errorf("expected Pod root first, got %s", sub.Nodes[0].Kind)
	}
	// Denied node must not appear.
	for _, n := range sub.Nodes {
		if n.Kind == KindReplicaSet {
			t.Errorf("denied node consumed budget and appeared: %s", n.ID)
		}
	}
}

func TestBuildNeighborhood_EdgesOnlyBetweenIncludedNodes(t *testing.T) {
	topo := podNeighborhood()
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}

	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileManagement,
		Hops:    1,
	})
	// Only edge should be the one between ReplicaSet and Pod under management.
	gotEdges := edgeIDs(sub)
	wantEdges := []string{"replicaset/prod/cart-rs-manages-pod/prod/cart-xyz"}
	if !equalStrings(gotEdges, wantEdges) {
		t.Errorf("management 1-hop edges = %v, want %v", gotEdges, wantEdges)
	}
}

func TestBuildNeighborhood_NilTopologyReturnsEmpty(t *testing.T) {
	root := ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"}
	sub := BuildNeighborhood(nil, root, NeighborhoodOptions{})
	if sub == nil {
		t.Fatal("expected non-nil subgraph")
	}
	if len(sub.Nodes) != 0 || len(sub.Edges) != 0 {
		t.Errorf("expected empty subgraph for nil topology, got %+v", sub)
	}
}

// TestBuildNeighborhood_GroupCollisionRoot pins the group-aware root lookup.
// When two nodes share kind+namespace+name but come from different API
// groups (a real collision: CAPI cluster.x-k8s.io/Cluster vs the hypothetical
// KubeFleet cluster.fleet.io/Cluster — same kind, different groups), the
// caller must be able to disambiguate by passing ResourceRef.Group. Without
// the group filter, findNodeByRef returns the first match — a leak risk if
// the wrong-group node has different visibility than the requested one.
func TestBuildNeighborhood_GroupCollisionRoot(t *testing.T) {
	// Two "Cluster" nodes in the same namespace, same name, different groups.
	// Each has a distinct neighbor so we can verify which root was selected.
	capi := Node{
		ID:     "cluster.x-k8s.io/cluster/fleet/prod",
		Kind:   "Cluster",
		Name:   "prod",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "fleet",
			"apiVersion": "cluster.x-k8s.io/v1beta1",
		},
	}
	capiMachine := makeNode("Machine", "fleet", "prod-md-0")
	capiMachine.Data["apiVersion"] = "cluster.x-k8s.io/v1beta1"

	fleet := Node{
		ID:     "cluster.fleet.io/cluster/fleet/prod",
		Kind:   "Cluster",
		Name:   "prod",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "fleet",
			"apiVersion": "cluster.fleet.io/v1alpha1",
		},
	}
	fleetGroup := makeNode("ClusterGroup", "fleet", "prod-grp")
	fleetGroup.Data["apiVersion"] = "cluster.fleet.io/v1alpha1"

	topo := &Topology{
		Nodes: []Node{capi, capiMachine, fleet, fleetGroup},
		Edges: []Edge{
			makeEdge(EdgeManages, capi.ID, capiMachine.ID),
			makeEdge(EdgeManages, fleet.ID, fleetGroup.ID),
		},
	}

	// Explicit group=cluster.x-k8s.io must pick the CAPI Cluster and surface
	// its Machine neighbor, NOT the Fleet ClusterGroup.
	sub := BuildNeighborhoodWithIndex(topo,
		ResourceRef{Kind: "Cluster", Namespace: "fleet", Name: "prod", Group: "cluster.x-k8s.io"},
		NeighborhoodOptions{Profile: ProfileManagement, Hops: 1},
		nil, nil,
	)
	if sub.Nodes[0].ID != capi.ID {
		t.Fatalf("expected CAPI root (id=%s), got %s", capi.ID, sub.Nodes[0].ID)
	}
	got := nodeIDs(sub)
	want := []string{capiMachine.ID, capi.ID}
	sort.Strings(want)
	if !equalStrings(got, want) {
		t.Errorf("group=cluster.x-k8s.io root neighborhood = %v, want %v", got, want)
	}
}

// TestBuildNeighborhood_GroupEmptyRootBackCompat pins the back-compat
// behavior: when ResourceRef.Group is empty, group-blind matching wins
// and the first matching node (by topology iteration order) is selected.
// This preserves pre-fix behavior for callers that don't supply a group
// (REST clients on /api/ai/neighborhood/{kind}/{ns}/{name} without ?group=).
func TestBuildNeighborhood_GroupEmptyRootBackCompat(t *testing.T) {
	// Same collision setup as GroupCollisionRoot. With Group="" the lookup
	// should return one of the two "Cluster" nodes (current iteration order
	// returns the first one — pin that as the contract).
	capi := Node{
		ID:     "cluster.x-k8s.io/cluster/fleet/prod",
		Kind:   "Cluster",
		Name:   "prod",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "fleet",
			"apiVersion": "cluster.x-k8s.io/v1beta1",
		},
	}
	fleet := Node{
		ID:     "cluster.fleet.io/cluster/fleet/prod",
		Kind:   "Cluster",
		Name:   "prod",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "fleet",
			"apiVersion": "cluster.fleet.io/v1alpha1",
		},
	}
	topo := &Topology{
		Nodes: []Node{capi, fleet},
		Edges: []Edge{},
	}

	sub := BuildNeighborhoodWithIndex(topo,
		ResourceRef{Kind: "Cluster", Namespace: "fleet", Name: "prod", Group: ""},
		NeighborhoodOptions{Profile: ProfileAll, Hops: 1},
		nil, nil,
	)
	if len(sub.Nodes) == 0 {
		t.Fatal("expected non-empty neighborhood for empty-group lookup")
	}
	// Back-compat: first node in topo.Nodes wins when group is unset.
	if sub.Nodes[0].ID != capi.ID {
		t.Errorf("empty-group lookup returned %s, expected first-match %s", sub.Nodes[0].ID, capi.ID)
	}
}

// TestBuildNeighborhood_SecretFilteredByAllow verifies the secret-leak fix at
// the topology layer: a Secret node in the BFS frontier is dropped when the
// caller's Allow predicate rejects it, matching the per-kind RBAC gate the
// REST and MCP handlers apply for Secret reads. The Secret must not appear in
// the output and the RBACDenied counter must reflect the skip.
func TestBuildNeighborhood_SecretFilteredByAllow(t *testing.T) {
	pod := makeNode(KindPod, "prod", "cart-xyz")
	secret := makeNode(KindSecret, "prod", "db-creds")
	topo := &Topology{
		Nodes: []Node{pod, secret},
		Edges: []Edge{
			makeEdge(EdgeConfigures, secret.ID, pod.ID),
		},
	}

	// Caller drops the Secret — simulates the REST/MCP per-kind SAR refusing
	// the user even though they have namespace access.
	sub := BuildNeighborhoodWithIndex(topo,
		ResourceRef{Kind: "Pod", Namespace: "prod", Name: "cart-xyz"},
		NeighborhoodOptions{
			Profile: ProfileAll,
			Hops:    1,
			Allow:   func(n *Node) bool { return n.Kind != KindSecret },
		},
		nil, nil,
	)
	for _, n := range sub.Nodes {
		if n.Kind == KindSecret {
			t.Errorf("Secret leaked through BFS despite Allow=false: %s", n.ID)
		}
	}
	if sub.RBACDenied != 1 {
		t.Errorf("expected RBACDenied=1 for the denied Secret, got %d", sub.RBACDenied)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
