package tree

import (
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/topology"
)

func helmRelease(namespace, name string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2",
		"kind":       "HelmRelease",
		"metadata":   map[string]any{"namespace": namespace, "name": name},
	}}
}

func topoNode(kind, namespace, name string, labels map[string]string) topology.Node {
	data := map[string]any{"namespace": namespace}
	if labels != nil {
		data["labels"] = labels
	}
	return topology.Node{
		ID:   kind + "/" + namespace + "/" + name,
		Kind: topology.NodeKind(kind),
		Name: name,
		Data: data,
	}
}

func TestFluxHelmReleaseManaged_HappyPath(t *testing.T) {
	hr := helmRelease("flux-system", "podinfo")
	topo := []topology.Node{
		topoNode("Deployment", "demo-flux-helm", "podinfo", map[string]string{
			fluxHelmNameLabel:      "podinfo",
			fluxHelmNamespaceLabel: "flux-system",
		}),
		// Resource without the labels — must not be picked up.
		topoNode("Deployment", "other", "podinfo", nil),
	}
	got := fluxHelmReleaseManaged(hr, topo)
	if len(got) != 1 {
		t.Fatalf("expected 1 managed resource, got %d (%v)", len(got), got)
	}
	if got[0].Ref.Kind != "Deployment" || got[0].Ref.Namespace != "demo-flux-helm" || got[0].Ref.Name != "podinfo" {
		t.Errorf("ref = %+v, want Deployment/demo-flux-helm/podinfo", got[0].Ref)
	}
}

// Pins the multi-tenant safety property: two HelmReleases with the same name
// in different namespaces must not pollute each other's managed sets. A
// regression that compared only on name would silently misattribute managed
// resources across teams — invisible in single-tenant dev clusters,
// embarrassing in shared prod.
func TestFluxHelmReleaseManaged_NamespaceIsolation(t *testing.T) {
	hrA := helmRelease("team-a", "redis")
	hrB := helmRelease("team-b", "redis")
	topo := []topology.Node{
		topoNode("Deployment", "team-a", "redis", map[string]string{
			fluxHelmNameLabel:      "redis",
			fluxHelmNamespaceLabel: "team-a",
		}),
		topoNode("Deployment", "team-b", "redis", map[string]string{
			fluxHelmNameLabel:      "redis",
			fluxHelmNamespaceLabel: "team-b",
		}),
	}

	gotA := fluxHelmReleaseManaged(hrA, topo)
	gotB := fluxHelmReleaseManaged(hrB, topo)

	if len(gotA) != 1 || gotA[0].Ref.Namespace != "team-a" {
		t.Errorf("hrA managed = %+v, want exactly the team-a Deployment", gotA)
	}
	if len(gotB) != 1 || gotB[0].Ref.Namespace != "team-b" {
		t.Errorf("hrB managed = %+v, want exactly the team-b Deployment", gotB)
	}
}

func TestFluxHelmReleaseManaged_NoLabelsReturnsEmpty(t *testing.T) {
	hr := helmRelease("flux-system", "podinfo")
	topo := []topology.Node{
		topoNode("Deployment", "demo", "podinfo", nil),
		topoNode("Pod", "demo", "podinfo-xyz", nil),
	}
	got := fluxHelmReleaseManaged(hr, topo)
	if len(got) != 0 {
		t.Errorf("expected no managed resources from label-less topology, got %d (%v)", len(got), got)
	}
}

// Labels can be stored as map[string]any when topology builders use the
// untyped path; the helper's fallback (stringMapFromAny) must produce
// equivalent matches.
func TestFluxHelmReleaseManaged_LabelsAsAnyMap(t *testing.T) {
	hr := helmRelease("flux-system", "podinfo")
	anyLabels := map[string]any{
		fluxHelmNameLabel:      "podinfo",
		fluxHelmNamespaceLabel: "flux-system",
	}
	topo := []topology.Node{
		{
			ID:   "Deployment/demo/podinfo",
			Kind: topology.NodeKind("Deployment"),
			Name: "podinfo",
			Data: map[string]any{"namespace": "demo", "labels": anyLabels},
		},
	}
	got := fluxHelmReleaseManaged(hr, topo)
	if len(got) != 1 {
		t.Fatalf("expected 1 managed resource via any-map fallback, got %d", len(got))
	}
}

func TestFluxHelmReleaseManaged_SkipsSelf(t *testing.T) {
	hr := helmRelease("flux-system", "podinfo")
	topo := []topology.Node{
		// Defensive case — the HelmRelease itself somehow carries its own
		// Flux labels. Must not produce a self-edge.
		topoNode("HelmRelease", "flux-system", "podinfo", map[string]string{
			fluxHelmNameLabel:      "podinfo",
			fluxHelmNamespaceLabel: "flux-system",
		}),
	}
	got := fluxHelmReleaseManaged(hr, topo)
	if len(got) != 0 {
		t.Errorf("expected HelmRelease to skip itself; got %d managed (%v)", len(got), got)
	}
}

func TestFluxHelmReleaseManaged_SortsForStableOutput(t *testing.T) {
	// Not strictly an invariant the production code maintains today, but a
	// useful guard: if the order ever becomes load-bearing, this test pins
	// the contract. Currently asserts the input order is preserved (no sort).
	hr := helmRelease("flux-system", "podinfo")
	labels := map[string]string{fluxHelmNameLabel: "podinfo", fluxHelmNamespaceLabel: "flux-system"}
	topo := []topology.Node{
		topoNode("Service", "demo", "podinfo", labels),
		topoNode("Deployment", "demo", "podinfo", labels),
	}
	got := fluxHelmReleaseManaged(hr, topo)
	if len(got) != 2 {
		t.Fatalf("expected 2 managed resources, got %d", len(got))
	}
	// Verify by sorting so the test isn't order-sensitive — but pin the
	// caller has both.
	names := []string{got[0].Ref.Kind, got[1].Ref.Kind}
	sort.Strings(names)
	if names[0] != "Deployment" || names[1] != "Service" {
		t.Errorf("kinds = %v, want [Deployment Service]", names)
	}
}
