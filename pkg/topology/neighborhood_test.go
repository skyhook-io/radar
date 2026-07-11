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

func TestEdgeTypesForAutoIncludesWorkflowDefinitionEdges(t *testing.T) {
	for _, kind := range []NodeKind{KindWorkflow, KindCronWorkflow, KindWorkflowTemplate, KindClusterWorkflowTemplate} {
		if !edgeTypesForAuto(kind)[EdgeConfigures] {
			t.Fatalf("auto profile for %s must include definition edges", kind)
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

	// Hops=99 should be clamped to neighborhoodMaxHops=2. Under auto for
	// a Pod we reach the management chain plus adjacent networking/policy
	// nodes, but still exclude config/configure edges.
	sub := BuildNeighborhood(topo, root, NeighborhoodOptions{
		Profile: ProfileAuto,
		Hops:    99,
	})
	got := nodeIDs(sub)
	want := []string{
		"deployment/prod/cart",
		"ingress/prod/cart",
		"networkpolicy/prod/cart-allow",
		"pod/prod/cart-xyz",
		"poddisruptionbudget/prod/cart-pdb",
		"replicaset/prod/cart-rs",
		"service/prod/cart",
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
		Profile: ProfileAuto,
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
		Profile: ProfileAuto,
		Hops:    1,
	})
	gotEdges := edgeIDs(sub)
	wantEdges := []string{
		"networkpolicy/prod/cart-allow-protects-pod/prod/cart-xyz",
		"poddisruptionbudget/prod/cart-pdb-protects-pod/prod/cart-xyz",
		"replicaset/prod/cart-rs-manages-pod/prod/cart-xyz",
		"service/prod/cart-exposes-pod/prod/cart-xyz",
	}
	if !equalStrings(gotEdges, wantEdges) {
		t.Errorf("auto 1-hop edges = %v, want %v", gotEdges, wantEdges)
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
// caller must be able to disambiguate by passing ResourceRef.Group.
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
		NeighborhoodOptions{Profile: ProfileAll, Hops: 1},
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

// TestBuildNeighborhood_GroupEmptyRootAmbiguous verifies that omitted group
// does not silently pick an arbitrary node when multiple API groups share the
// same kind+namespace+name. Callers must pass ResourceRef.Group to resolve the
// collision.
func TestBuildNeighborhood_GroupEmptyRootAmbiguous(t *testing.T) {
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
	if !sub.AmbiguousRoot {
		t.Fatal("expected ambiguous root when group is omitted for colliding resources")
	}
	if len(sub.Nodes) != 0 || len(sub.Edges) != 0 {
		t.Errorf("expected empty subgraph for ambiguous root, got nodes=%v edges=%v", nodeIDs(sub), edgeIDs(sub))
	}
}

// TestBuildNeighborhood_AllowDeniesRoot verifies the root is gated by Allow,
// not just the BFS frontier. A Secret root with namespace access but no
// per-namespace `get secrets` SAR is the load-bearing case: callers' upfront
// RBAC check (kind+namespace) doesn't catch per-kind tightening inside a
// namespace, so the root Allow gate is the only place that denial surfaces.
//
// Empty subgraph + RBACDenied=1 lets handlers translate to 404 (matching the
// "root not in topology" path), preserving existence-hiding — a user without
// `get secrets` can't distinguish "Secret doesn't exist" from "Secret exists,
// you can't read it."
func TestBuildNeighborhood_AllowDeniesRoot(t *testing.T) {
	pod := makeNode(KindPod, "prod", "cart-xyz")
	secret := makeNode(KindSecret, "prod", "api-tls")
	topo := &Topology{
		Nodes: []Node{pod, secret},
		Edges: []Edge{
			makeEdge(EdgeConfigures, secret.ID, pod.ID),
		},
	}

	sub := BuildNeighborhoodWithIndex(topo,
		ResourceRef{Kind: "Secret", Namespace: "prod", Name: "api-tls"},
		NeighborhoodOptions{
			Profile: ProfileAll,
			Hops:    1,
			// Simulate "user has namespace access but no get-secrets" — the
			// root Secret is rejected before BFS starts.
			Allow: func(n *Node) bool { return n.Kind != KindSecret },
		},
		nil, nil,
	)

	if len(sub.Nodes) != 0 {
		t.Errorf("expected empty subgraph when root Allow denies; got %v", nodeIDs(sub))
	}
	if len(sub.Edges) != 0 {
		t.Errorf("expected no edges when root denied; got %d", len(sub.Edges))
	}
	if sub.RBACDenied != 1 {
		t.Errorf("expected RBACDenied=1 for denied root, got %d", sub.RBACDenied)
	}
}

// TestBuildNeighborhood_AllowAllowsRoot is the positive counterpart: a Secret
// root passes the Allow gate when the caller's predicate accepts it, and the
// expansion proceeds normally.
func TestBuildNeighborhood_AllowAllowsRoot(t *testing.T) {
	pod := makeNode(KindPod, "prod", "cart-xyz")
	secret := makeNode(KindSecret, "prod", "api-tls")
	topo := &Topology{
		Nodes: []Node{pod, secret},
		Edges: []Edge{
			makeEdge(EdgeConfigures, secret.ID, pod.ID),
		},
	}

	sub := BuildNeighborhoodWithIndex(topo,
		ResourceRef{Kind: "Secret", Namespace: "prod", Name: "api-tls"},
		NeighborhoodOptions{
			Profile: ProfileAll,
			Hops:    1,
			Allow:   func(n *Node) bool { return true },
		},
		nil, nil,
	)
	if len(sub.Nodes) == 0 {
		t.Fatalf("expected non-empty subgraph when Allow accepts root")
	}
	if sub.Nodes[0].ID != secret.ID {
		t.Errorf("expected Secret root first, got %s", sub.Nodes[0].ID)
	}
	if sub.RBACDenied != 0 {
		t.Errorf("expected RBACDenied=0 when nothing was denied, got %d", sub.RBACDenied)
	}
}

// TestBuildNeighborhood_GroupAwareRootIDMatch pins the group-aware check
// applied even when buildNodeID's direct ID match hits. The default
// buildNodeID heuristic (lowercase kind / namespace / name) collides when
// two CRDs share a plural — without the apiVersion-group validation, the
// caller-supplied group is silently ignored on the direct-match path.
//
// Setup: a CNPG Cluster occupies the lowercase-kind ID "cluster/fleet/prod"
// (what buildNodeID produces for kind=Cluster). The CAPI Cluster lives under
// a distinct ID to avoid the collision. A caller asking for
// group=cluster.x-k8s.io must NOT silently get the CNPG node back just
// because the direct ID lookup hit.
func TestBuildNeighborhood_GroupAwareRootIDMatch(t *testing.T) {
	// CNPG Cluster: occupies the lowercase-kind ID buildNodeID produces.
	cnpg := Node{
		ID:     "cluster/fleet/prod",
		Kind:   "Cluster",
		Name:   "prod",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "fleet",
			"apiVersion": "postgresql.cnpg.io/v1",
		},
	}
	// CAPI Cluster: same kind+namespace+name but distinct ID (group-prefixed
	// to mirror real-world collision-avoidance). The caller asks for it by
	// group; the direct-ID path must reject CNPG, then findNodeByRef must
	// surface the CAPI variant.
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
	topo := &Topology{Nodes: []Node{cnpg, capi}}

	sub := BuildNeighborhoodWithIndex(topo,
		ResourceRef{Kind: "Cluster", Namespace: "fleet", Name: "prod", Group: "cluster.x-k8s.io"},
		NeighborhoodOptions{Profile: ProfileAll, Hops: 1},
		nil, nil,
	)
	if len(sub.Nodes) == 0 {
		t.Fatal("expected non-empty neighborhood for group-disambiguated root")
	}
	got := nodeAPIGroupFromData(&sub.Nodes[0])
	if got != "cluster.x-k8s.io" {
		t.Errorf("group-aware lookup picked apiGroup=%q (id=%s), expected cluster.x-k8s.io (CAPI)", got, sub.Nodes[0].ID)
	}

	// Sanity counterpart: caller asking for the CNPG group must get CNPG via
	// the direct-ID path with the apiGroup matching.
	sub2 := BuildNeighborhoodWithIndex(topo,
		ResourceRef{Kind: "Cluster", Namespace: "fleet", Name: "prod", Group: "postgresql.cnpg.io"},
		NeighborhoodOptions{Profile: ProfileAll, Hops: 1},
		nil, nil,
	)
	if len(sub2.Nodes) == 0 {
		t.Fatal("expected non-empty neighborhood for CNPG-disambiguated root")
	}
	got2 := nodeAPIGroupFromData(&sub2.Nodes[0])
	if got2 != "postgresql.cnpg.io" {
		t.Errorf("group=postgresql.cnpg.io picked apiGroup=%q, expected postgresql.cnpg.io", got2)
	}
}

// TestBuildNeighborhood_PseudoKindRootLookup pins the pseudo-kind mapping in
// findNodeByRef. KNative serving.knative.dev/Service is stored in the topology
// under NodeKind="KnativeService" (a synthesized label, not a real K8s kind).
// A caller asking for kind=Service&group=serving.knative.dev must find the
// KnativeService node — without the (kind, group) → pseudo-kind translation,
// the direct kind comparison ("Service" vs "KnativeService") never matches.
//
// Also pins the disambiguation: a core Service of the same name in the same
// namespace must NOT be returned when group=serving.knative.dev is supplied.
func TestBuildNeighborhood_PseudoKindRootLookup(t *testing.T) {
	knsvc := Node{
		ID:     "knativeservice/prod/api",
		Kind:   KindKnativeService,
		Name:   "api",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "prod",
			"apiVersion": "serving.knative.dev/v1",
		},
	}
	coreSvc := Node{
		ID:     "service/prod/api",
		Kind:   KindService,
		Name:   "api",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "prod",
			"apiVersion": "v1",
		},
	}
	pod := makeNode(KindPod, "prod", "api-xyz")
	topo := &Topology{
		Nodes: []Node{knsvc, coreSvc, pod},
		Edges: []Edge{
			makeEdge(EdgeExposes, knsvc.ID, pod.ID),
		},
	}

	// Both Pascal-case ("Service" — MCP path) and lowercase ("service" — REST
	// path after normalizeKind) must resolve to the KnativeService pseudo-kind.
	// REST's lowercasing previously slipped past pseudoKindFor's Pascal-only
	// switch and returned 404 for legitimate cross-group root lookups.
	// REST normalizes URL paths to lowercase plural ("services"); MCP arrives
	// in Pascal singular ("Service"). Both must resolve.
	for _, kind := range []string{"Service", "service", "services"} {
		t.Run(kind, func(t *testing.T) {
			sub := BuildNeighborhoodWithIndex(topo,
				ResourceRef{Kind: kind, Namespace: "prod", Name: "api", Group: "serving.knative.dev"},
				NeighborhoodOptions{Profile: ProfileAuto, Hops: 1},
				nil, nil,
			)
			if len(sub.Nodes) == 0 {
				t.Fatalf("expected non-empty neighborhood for KnativeService pseudo-kind root with kind=%q", kind)
			}
			if sub.Nodes[0].ID != knsvc.ID {
				t.Errorf("pseudo-kind root lookup with kind=%q returned %s, expected KnativeService %s", kind, sub.Nodes[0].ID, knsvc.ID)
			}
			if sub.Nodes[0].Kind != KindKnativeService {
				t.Errorf("expected NodeKind=KnativeService for kind=%q, got %s", kind, sub.Nodes[0].Kind)
			}
		})
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

// TestBuildNeighborhood_NodeClassPerVariantSAR pins the per-variant
// authorization at the topology layer: a single NodeKind (NodeClass) has
// three discriminated variants (EC2NodeClass / AKSNodeClass / GCENodeClass)
// keyed by apiVersion-group. The caller's Allow predicate must be able to
// pass one variant while denying another so a user with EC2-only RBAC
// doesn't see AKS or GCP NodeClass nodes on a multi-provider cluster.
//
// Setup: a NodePool root with two child NodeClass nodes — one EC2
// (karpenter.k8s.aws) and one AKS (karpenter.azure.com). Allow returns
// true for EC2 group, false for AKS group. The BFS expansion must surface
// EC2, drop AKS, and bump RBACDenied for the dropped AKS node.
func TestBuildNeighborhood_NodeClassPerVariantSAR(t *testing.T) {
	np := Node{
		ID:     "nodepool/_/karpenter-default",
		Kind:   KindNodePool,
		Name:   "karpenter-default",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "",
			"apiVersion": "karpenter.sh/v1",
		},
	}
	ec2 := Node{
		ID:     "nodeclass/_/ec2-default",
		Kind:   KindNodeClass,
		Name:   "ec2-default",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "",
			"apiVersion": "karpenter.k8s.aws/v1",
		},
	}
	aks := Node{
		ID:     "nodeclass/_/aks-default",
		Kind:   KindNodeClass,
		Name:   "aks-default",
		Status: StatusHealthy,
		Data: map[string]any{
			"namespace":  "",
			"apiVersion": "karpenter.azure.com/v1beta1",
		},
	}
	topo := &Topology{
		Nodes: []Node{np, ec2, aks},
		Edges: []Edge{
			makeEdge(EdgeConfigures, np.ID, ec2.ID),
			makeEdge(EdgeConfigures, np.ID, aks.ID),
		},
	}

	// Allow returns true for NodePool + EC2 NodeClass, false for AKS
	// NodeClass — keyed on the node's apiVersion group, which is exactly
	// the discriminator the handler-side fix relies on.
	sub := BuildNeighborhoodWithIndex(topo,
		ResourceRef{Kind: "NodePool", Namespace: "", Name: "karpenter-default", Group: "karpenter.sh"},
		NeighborhoodOptions{
			Profile: ProfileAll,
			Hops:    1,
			Allow: func(n *Node) bool {
				if n.Kind != KindNodeClass {
					return true
				}
				return nodeAPIGroupFromData(n) == "karpenter.k8s.aws"
			},
		},
		nil, nil,
	)

	sawEC2 := false
	sawAKS := false
	for _, n := range sub.Nodes {
		switch n.ID {
		case ec2.ID:
			sawEC2 = true
		case aks.ID:
			sawAKS = true
		}
	}
	if !sawEC2 {
		t.Errorf("EC2 NodeClass denied — per-variant Allow with karpenter.k8s.aws=true must surface it (got nodes: %v)", nodeIDs(sub))
	}
	if sawAKS {
		t.Errorf("AKS NodeClass leaked despite Allow=false for karpenter.azure.com — per-variant gate must drop it")
	}
	if sub.RBACDenied != 1 {
		t.Errorf("expected RBACDenied=1 for the dropped AKS NodeClass, got %d", sub.RBACDenied)
	}
}

// TestAPIVersionGroup pins the canonical split-on-first-slash extraction.
// Previously this function existed three times (REST, MCP, and inside
// topology); the test now lives next to the single shared implementation.
func TestAPIVersionGroup(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"v1", ""},
		{"apps/v1", "apps"},
		{"argoproj.io/v1alpha1", "argoproj.io"},
		{"networking.k8s.io/v1", "networking.k8s.io"},
		{"serving.knative.dev/v1", "serving.knative.dev"},
		{"", ""},
		{"/v1", ""},               // leading slash → empty group
		{"apps/v1/extra", "apps"}, // multi-slash → split on FIRST
	}
	for _, tc := range cases {
		if got := APIVersionGroup(tc.in); got != tc.want {
			t.Errorf("APIVersionGroup(%q) = %q, want %q", tc.in, got, tc.want)
		}
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
