package resourcecontext

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestPolicySelectionUncertaintyReachesAgentContext(t *testing.T) {
	rc := Build(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"}}, Options{Tier: TierBasic, Relationships: &topology.Relationships{NetworkPolicyMatchesIncomplete: true, NetworkPolicyMatchesIncompleteSources: []topology.ResourceRef{{Kind: "CiliumNetworkPolicy", Group: "cilium.io", Namespace: "app", Name: "unknown"}}, NetworkPolicies: []topology.ResourceRef{{Kind: "CiliumNetworkPolicy", Group: "cilium.io", Namespace: "app", Name: "known"}}}, AccessChecker: allowAllChecker{}})
	if !rc.NetworkPolicyMatchesIncomplete || len(rc.SelectedBy) != 1 {
		t.Fatalf("uncertainty or known selection lost: %+v", rc)
	}
	wire, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(wire), `"networkPolicyMatchesIncomplete":true`) || strings.Contains(string(wire), `"policySelectionUnknown"`) || strings.Contains(string(wire), `"name":"unknown"`) {
		t.Fatalf("ambiguous field or private provenance in agent context: %s", wire)
	}
}

func TestUnknownSelectionDoesNotRevealUnreadableClusterwidePolicy(t *testing.T) {
	rc := Build(context.Background(), &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "web", Namespace: "app"}}, Options{Tier: TierBasic, Relationships: &topology.Relationships{NetworkPolicyMatchesIncomplete: true, NetworkPolicyMatchesIncompleteSources: []topology.ResourceRef{{Kind: "CiliumClusterwideNetworkPolicy", Group: "cilium.io", Name: "hidden"}}}, AccessChecker: denyChecker{group: "cilium.io", kind: "CiliumClusterwideNetworkPolicy", namespace: ""}})
	if rc.NetworkPolicyMatchesIncomplete {
		t.Fatal("unreadable clusterwide policy leaked through uncertainty")
	}
	if len(rc.Omitted) == 0 {
		t.Fatal("RBAC omission should explain incomplete selection")
	}
	wire, err := json.Marshal(rc)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(wire), "networkPolicyMatchesIncomplete") || strings.Contains(string(wire), "hidden") || !hasOmitted(rc.Omitted, "selectedBy") {
		t.Fatalf("unreadable policy was exposed or omission was lost: %s", wire)
	}
}

func TestAllPodPolicyTargetsReachAgentContextWithPermissions(t *testing.T) {
	policy := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "all", "namespace": "app"}}}
	graph := &topology.Topology{Nodes: []topology.Node{
		{ID: "networkpolicy/app/all", Kind: topology.KindNetworkPolicy, Name: "all", Data: map[string]any{"namespace": "app", "apiVersion": "networking.k8s.io/v1", "matchesAllPods": true}},
		{ID: "deployment/app/web", Kind: topology.KindDeployment, Name: "web", Data: map[string]any{"namespace": "app", "apiVersion": "apps/v1"}},
	}}
	rel := topology.GetRelationshipsWithObject("NetworkPolicy", "app", "all", policy, graph, nil, nil, nil)
	full := Build(context.Background(), policy, Options{Relationships: rel, AccessChecker: allowAllChecker{}})
	if len(full.Protects) != 1 || full.Protects[0].Kind != "Deployment" {
		t.Fatalf("lost broad policy targets: %+v", full)
	}
	limited := Build(context.Background(), policy, Options{Relationships: rel, AccessChecker: denyChecker{group: "apps", kind: "Deployment", namespace: "app"}})
	if len(limited.Protects) != 0 || !hasOmitted(limited.Omitted, "protects") {
		t.Fatalf("policy targets ignored permissions: %+v", limited)
	}
}

type policyTargetCheckCounter struct {
	calls int
	allow bool
}

func (c *policyTargetCheckCounter) CanRead(ctx context.Context, group, kind, namespace string) bool {
	c.calls++
	return c.allow
}

func TestPolicyTargetContextBoundsDistinctScopesAndPreservesUncertainty(t *testing.T) {
	for _, allow := range []bool{false, true} {
		t.Run(fmt.Sprint(allow), func(t *testing.T) {
			refs := make([]topology.ResourceRef, 0, maxPolicyTargetScopes+10)
			for i := maxPolicyTargetScopes + 9; i >= 0; i-- {
				refs = append(refs, topology.ResourceRef{Kind: "Deployment", Group: "apps", Namespace: fmt.Sprintf("ns-%03d", i), Name: "web"})
			}
			counter := &policyTargetCheckCounter{allow: allow}
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "all", "namespace": "app"}}}
			rc := Build(context.Background(), obj, Options{Relationships: &topology.Relationships{Protects: refs}, AccessChecker: counter})
			if counter.calls != maxPolicyTargetScopes {
				t.Fatalf("permission checks=%d", counter.calls)
			}
			want := 0
			if allow {
				want = maxPolicyTargetRefs
			}
			if len(rc.Protects) != want {
				t.Fatalf("targets=%d want=%d", len(rc.Protects), want)
			}
			budget := false
			for _, omission := range rc.Omitted {
				if omission.Field == "protects" && omission.Reason == OmittedBudgetExceeded {
					budget = true
				}
			}
			if !budget {
				t.Fatalf("truncated targets looked complete: %+v", rc)
			}
			if allow && (rc.Protects[0].Namespace != "ns-000" || rc.Protects[len(rc.Protects)-1].Namespace != "ns-049") {
				t.Fatalf("sample is not deterministic: %+v", rc.Protects)
			}
			if refs[0].Namespace != "ns-059" {
				t.Fatal("mutated shared relationship input")
			}
		})
	}
}

type namespaceTargetCheckCounter struct {
	calls     int
	namespace string
}

func (c *namespaceTargetCheckCounter) CanRead(ctx context.Context, group, kind, namespace string) bool {
	c.calls++
	return namespace == c.namespace
}

func TestPolicyTargetContextDoesNotSpendBudgetOnRepeatedDeniedScope(t *testing.T) {
	refs := []topology.ResourceRef{{Kind: "Deployment", Group: "apps", Namespace: "payments", Name: "web"}}
	for i := 0; i < 100; i++ {
		refs = append(refs, topology.ResourceRef{Kind: "DaemonSet", Group: "apps", Namespace: "infra", Name: fmt.Sprint(i)})
	}
	counter := &namespaceTargetCheckCounter{namespace: "payments"}
	obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "cilium.io/v2", "kind": "CiliumClusterwideNetworkPolicy", "metadata": map[string]any{"name": "all"}}}
	rc := Build(context.Background(), obj, Options{Relationships: &topology.Relationships{Protects: refs}, AccessChecker: counter})
	if counter.calls != 2 || len(rc.Protects) != 1 || rc.Protects[0].Namespace != "payments" {
		t.Fatalf("denied prefix hid authorized targets: checks=%d context=%+v", counter.calls, rc)
	}
	for _, omission := range rc.Omitted {
		if omission.Field == "protects" && omission.Reason == OmittedBudgetExceeded {
			t.Fatal("complete scoped evaluation marked truncated")
		}
	}
}

func TestPolicyTargetContextOutputBudgetWithinOneAuthorizedScope(t *testing.T) {
	for _, count := range []int{maxPolicyTargetRefs, maxPolicyTargetRefs + 1} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			refs := make([]topology.ResourceRef, count)
			for i := range refs {
				refs[i] = topology.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "app", Name: fmt.Sprintf("web-%03d", i)}
			}
			counter := &policyTargetCheckCounter{allow: true}
			obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "networking.k8s.io/v1", "kind": "NetworkPolicy", "metadata": map[string]any{"name": "all", "namespace": "app"}}}
			rc := Build(context.Background(), obj, Options{Relationships: &topology.Relationships{Protects: refs}, AccessChecker: counter})
			if len(rc.Protects) != maxPolicyTargetRefs || counter.calls != 1 {
				t.Fatalf("output or scope bound wrong: targets=%d checks=%d", len(rc.Protects), counter.calls)
			}
			if slices.Contains(rc.Omitted, OmittedField{Field: "protects", Reason: OmittedBudgetExceeded}) != (count > maxPolicyTargetRefs) {
				t.Fatalf("exact-limit completeness wrong: count=%d omitted=%+v", count, rc.Omitted)
			}
		})
	}
}
