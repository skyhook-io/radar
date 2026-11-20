package tree

import (
	"context"
	"fmt"
	"slices"
	"sort"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func chainObj(apiVersion, kind, ns, name string, spec map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind,
		"metadata": map[string]any{"name": name, "namespace": ns}, "spec": spec}}
}

func chainRef(group, kind, name string) ResourceRef {
	return ResourceRef{Group: group, Kind: kind, Namespace: "team", Name: name}
}

func chainEdges(tree *ResourceTree) []string {
	var out []string
	for _, e := range tree.Edges {
		out = append(out, fmt.Sprintf("%s -> %s (%s)", e.Source, e.Target, e.Type))
	}
	sort.Strings(out)
	return out
}

const (
	chainSrc = "source.toolkit.fluxcd.io"
	chainExt = "source.extensions.fluxcd.io"
)

var chainGenRef = map[string]any{"apiVersion": "source.extensions.fluxcd.io/v1beta1", "kind": "ArtifactGenerator", "name": "gen"}

// Kustomization → ExternalArtifact → generator → source.
func TestFluxTreeKustomizationFollowsArtifactChain(t *testing.T) {
	ks := chainObj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "ExternalArtifact", "name": "art"}})
	ea := chainObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "team", "art", map[string]any{"sourceRef": chainGenRef})
	gen := chainObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "team", "gen", map[string]any{"sources": []any{map[string]any{"alias": "repo", "kind": "GitRepository", "name": "platform"}}})
	repo := chainObj("source.toolkit.fluxcd.io/v1", "GitRepository", "team", "platform", nil)
	ksr := chainRef("kustomize.toolkit.fluxcd.io", "Kustomization", "app")
	ear, genr, repor := chainRef(chainSrc, "ExternalArtifact", "art"), chainRef(chainExt, "ArtifactGenerator", "gen"), chainRef(chainSrc, "GitRepository", "platform")
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(ksr): ks, refKey(ear): ea, refKey(genr): gen, refKey(repor): repo}}
	tree, _, err := newTestBuilder(t, getter, nil).Build(context.Background(), "Kustomization", "team", "app", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Nodes) != 4 {
		t.Fatalf("nodes = %d", len(tree.Nodes))
	}
	if findNode(t, tree, repor).Data["alias"] != "repo" || findNode(t, tree, genr).Data["relationship"] != "generator" {
		t.Fatalf("data wrong")
	}
	want := []string{
		fmt.Sprintf("%s -> %s (%s)", nodeID(ksr), nodeID(ear), EdgeSource),
		fmt.Sprintf("%s -> %s (%s)", nodeID(ear), nodeID(genr), EdgeSource),
		fmt.Sprintf("%s -> %s (%s)", nodeID(genr), nodeID(repor), EdgeSource),
	}
	sort.Strings(want)
	if got := chainEdges(tree); !slices.Equal(got, want) {
		t.Fatalf("edges = %v, want %v", got, want)
	}
}

// inline HelmRelease → HelmChart → HelmRepository unchanged through the loop.
func TestFluxTreeInlineChartChainThroughUpstreamWalk(t *testing.T) {
	hr := helmRelease("team", "app")
	hr.Object["spec"] = map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": "podinfo", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo"}}}}
	hr.Object["status"] = map[string]any{"helmChart": "team/team-app"}
	chart := chainObj("source.toolkit.fluxcd.io/v1", "HelmChart", "team", "team-app", map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo"}})
	repo := chainObj("source.toolkit.fluxcd.io/v1", "HelmRepository", "team", "repo", nil)
	hrr := chainRef("helm.toolkit.fluxcd.io", "HelmRelease", "app")
	chr, repor := chainRef(chainSrc, "HelmChart", "team-app"), chainRef(chainSrc, "HelmRepository", "repo")
	getter := &fakeDynamic{objects: map[string]*unstructured.Unstructured{refKey(hrr): hr, refKey(chr): chart, refKey(repor): repo}}
	tree, _, err := newTestBuilder(t, getter, nil).Build(context.Background(), "HelmRelease", "team", "app", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	got := chainEdges(tree)
	want := []string{
		fmt.Sprintf("%s -> %s (%s)", nodeID(hrr), nodeID(repor), EdgeSource),
		fmt.Sprintf("%s -> %s (%s)", nodeID(hrr), nodeID(chr), EdgeSource),
		fmt.Sprintf("%s -> %s (%s)", nodeID(chr), nodeID(repor), EdgeSource),
	}
	sort.Strings(want)
	if !slices.Equal(got, want) {
		t.Fatalf("edges = %v, want %v", got, want)
	}
}

// generators chained through artifacts terminate; record what the cap cuts.
func TestFluxTreeUpstreamWalkStopsAtThreeLevels(t *testing.T) {
	ks := chainObj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "ExternalArtifact", "name": "out2"}})
	out2 := chainObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "team", "out2", map[string]any{"sourceRef": map[string]any{"apiVersion": "source.extensions.fluxcd.io/v1beta1", "kind": "ArtifactGenerator", "name": "gen2"}})
	gen2 := chainObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "team", "gen2", map[string]any{"sources": []any{map[string]any{"alias": "base", "kind": "ExternalArtifact", "name": "out1"}}})
	out1 := chainObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "team", "out1", map[string]any{"sourceRef": chainGenRef})
	gen := chainObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "team", "gen", map[string]any{"sources": []any{map[string]any{"alias": "repo", "kind": "GitRepository", "name": "platform"}}})
	repo := chainObj("source.toolkit.fluxcd.io/v1", "GitRepository", "team", "platform", nil)
	ksr := chainRef("kustomize.toolkit.fluxcd.io", "Kustomization", "app")
	objs := map[string]*unstructured.Unstructured{refKey(ksr): ks,
		refKey(chainRef(chainSrc, "ExternalArtifact", "out2")): out2, refKey(chainRef(chainExt, "ArtifactGenerator", "gen2")): gen2,
		refKey(chainRef(chainSrc, "ExternalArtifact", "out1")): out1, refKey(chainRef(chainExt, "ArtifactGenerator", "gen")): gen,
		refKey(chainRef(chainSrc, "GitRepository", "platform")): repo}
	tree, _, err := newTestBuilder(t, &fakeDynamic{objects: objs}, nil).Build(context.Background(), "Kustomization", "team", "app", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, n := range tree.Nodes {
		names = append(names, n.Ref.Kind+"/"+n.Ref.Name)
	}
	sort.Strings(names)
	// Three levels: out2 → gen2 → out1 → gen. gen's own source is past the cap.
	want := []string{"ArtifactGenerator/gen", "ArtifactGenerator/gen2", "ExternalArtifact/out1", "ExternalArtifact/out2", "Kustomization/app"}
	if !slices.Equal(names, want) {
		t.Fatalf("nodes = %v, want %v", names, want)
	}
}

// a generator that lists the artifact the root consumes, plus a source shared by two generators.
func TestFluxTreeGeneratorCycleAndSharedSource(t *testing.T) {
	ks := chainObj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "ExternalArtifact", "name": "art"}})
	ea := chainObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "team", "art", map[string]any{"sourceRef": chainGenRef})
	gen := chainObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "team", "gen", map[string]any{"sources": []any{
		map[string]any{"alias": "self", "kind": "ExternalArtifact", "name": "art"},
		map[string]any{"alias": "repo", "kind": "GitRepository", "name": "platform"},
		map[string]any{"alias": "up", "kind": "ExternalArtifact", "name": "art2"},
	}})
	ea2 := chainObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "team", "art2", map[string]any{"sourceRef": map[string]any{"apiVersion": "source.extensions.fluxcd.io/v1beta1", "kind": "ArtifactGenerator", "name": "gen2"}})
	gen2 := chainObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "team", "gen2", map[string]any{"sources": []any{map[string]any{"alias": "shared", "kind": "GitRepository", "name": "platform"}}})
	repo := chainObj("source.toolkit.fluxcd.io/v1", "GitRepository", "team", "platform", nil)
	ksr := chainRef("kustomize.toolkit.fluxcd.io", "Kustomization", "app")
	objs := map[string]*unstructured.Unstructured{refKey(ksr): ks,
		refKey(chainRef(chainSrc, "ExternalArtifact", "art")): ea, refKey(chainRef(chainExt, "ArtifactGenerator", "gen")): gen,
		refKey(chainRef(chainSrc, "ExternalArtifact", "art2")): ea2, refKey(chainRef(chainExt, "ArtifactGenerator", "gen2")): gen2,
		refKey(chainRef(chainSrc, "GitRepository", "platform")): repo}
	getter := &fakeDynamic{objects: objs}
	tree, _, err := newTestBuilder(t, getter, nil).Build(context.Background(), "Kustomization", "team", "app", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	seen := map[string]int{}
	for _, n := range tree.Nodes {
		seen[n.ID]++
		if seen[n.ID] > 1 {
			t.Fatalf("duplicate node %s", n.ID)
		}
	}
	if len(tree.Nodes) != 6 {
		t.Fatalf("nodes = %d", len(tree.Nodes))
	}
	if got := findNode(t, tree, chainRef(chainSrc, "GitRepository", "platform")).Data["alias"]; got != "repo" {
		t.Fatalf("shared source alias = %v", got)
	}
	ksR, genR, artR := chainRef("kustomize.toolkit.fluxcd.io", "Kustomization", "app"), chainRef(chainExt, "ArtifactGenerator", "gen"), chainRef(chainSrc, "ExternalArtifact", "art")
	gen2R, art2R, repoR := chainRef(chainExt, "ArtifactGenerator", "gen2"), chainRef(chainSrc, "ExternalArtifact", "art2"), chainRef(chainSrc, "GitRepository", "platform")
	edge := func(a, b ResourceRef) string { return fmt.Sprintf("%s -> %s (%s)", nodeID(a), nodeID(b), EdgeSource) }
	// The generator lists the artifact the root consumes, so that edge points back.
	want := []string{edge(ksR, artR), edge(artR, genR), edge(genR, artR), edge(genR, repoR), edge(genR, art2R), edge(art2R, gen2R)}
	sort.Strings(want)
	if got := chainEdges(tree); !slices.Equal(got, want) {
		t.Fatalf("edges = %v, want %v", got, want)
	}
}

// a user who can't read ArtifactGenerators sees no generator or its sources and gets the notice.
func TestFluxTreeHidesUnreadableGenerator(t *testing.T) {
	hr := helmRelease("team", "app")
	hr.Object["spec"] = map[string]any{"chartRef": map[string]any{"kind": "ExternalArtifact", "name": "art"}}
	ea := chainObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "team", "art", map[string]any{"sourceRef": chainGenRef})
	gen := chainObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "team", "gen", map[string]any{"sources": []any{map[string]any{"alias": "repo", "kind": "GitRepository", "name": "platform"}}})
	repo := chainObj("source.toolkit.fluxcd.io/v1", "GitRepository", "team", "platform", nil)
	hrr := chainRef("helm.toolkit.fluxcd.io", "HelmRelease", "app")
	objs := map[string]*unstructured.Unstructured{refKey(hrr): hr,
		refKey(chainRef(chainSrc, "ExternalArtifact", "art")): ea, refKey(chainRef(chainExt, "ArtifactGenerator", "gen")): gen,
		refKey(chainRef(chainSrc, "GitRepository", "platform")): repo}
	getter := &fakeDynamic{objects: objs}
	tree, _, err := newTestBuilder(t, getter, nil).WithReadCheck(func(ref ResourceRef) bool { return ref.Kind != "ArtifactGenerator" }).Build(context.Background(), "HelmRelease", "team", "app", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	for _, n := range tree.Nodes {
		if n.Ref.Kind == "ArtifactGenerator" || n.Ref.Kind == "GitRepository" {
			t.Fatalf("leaked %s", n.ID)
		}
	}
	for _, c := range getter.recordedCalls() {
		if c.Kind == "ArtifactGenerator" || c.Kind == "GitRepository" {
			t.Fatalf("fetched denied/unreachable %+v", c)
		}
	}
	if !slices.Contains(tree.Warnings, "Some managed resources are hidden by RBAC.") {
		t.Fatalf("warnings = %v", tree.Warnings)
	}
	if len(tree.Nodes) != 2 {
		t.Fatalf("nodes = %d", len(tree.Nodes))
	}
	// Middle-hop denial for the ExternalArtifact itself.
	tree, _, err = newTestBuilder(t, &fakeDynamic{objects: objs}, nil).WithReadCheck(func(ref ResourceRef) bool { return ref.Kind != "ExternalArtifact" }).Build(context.Background(), "HelmRelease", "team", "app", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Nodes) != 1 || !slices.Contains(tree.Warnings, "Some managed resources are hidden by RBAC.") {
		t.Fatalf("hidden middle hop: nodes=%d warnings=%v", len(tree.Nodes), tree.Warnings)
	}
}

// A repository a generator lists directly and through a HelmChart it also
// lists is one node with both edges.
func TestFluxTreeSourceSharedByGeneratorAndChart(t *testing.T) {
	ks := chainObj("kustomize.toolkit.fluxcd.io/v1", "Kustomization", "team", "app", map[string]any{"sourceRef": map[string]any{"kind": "ExternalArtifact", "name": "art"}})
	ea := chainObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "team", "art", map[string]any{"sourceRef": chainGenRef})
	gen := chainObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "team", "gen", map[string]any{"sources": []any{
		map[string]any{"alias": "repo", "kind": "GitRepository", "name": "platform"},
		map[string]any{"alias": "chart", "kind": "HelmChart", "name": "podinfo"},
	}})
	chart := chainObj("source.toolkit.fluxcd.io/v1", "HelmChart", "team", "podinfo", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "platform"}})
	repo := chainObj("source.toolkit.fluxcd.io/v1", "GitRepository", "team", "platform", nil)
	ksr, ear, genr := chainRef("kustomize.toolkit.fluxcd.io", "Kustomization", "app"), chainRef(chainSrc, "ExternalArtifact", "art"), chainRef(chainExt, "ArtifactGenerator", "gen")
	chr, repor := chainRef(chainSrc, "HelmChart", "podinfo"), chainRef(chainSrc, "GitRepository", "platform")
	objs := map[string]*unstructured.Unstructured{refKey(ksr): ks, refKey(ear): ea, refKey(genr): gen, refKey(chr): chart, refKey(repor): repo}
	tree, _, err := newTestBuilder(t, &fakeDynamic{objects: objs}, nil).Build(context.Background(), "Kustomization", "team", "app", "kustomize.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	if len(tree.Nodes) != 5 {
		t.Fatalf("nodes = %d", len(tree.Nodes))
	}
	edge := func(a, b ResourceRef) string { return fmt.Sprintf("%s -> %s (%s)", nodeID(a), nodeID(b), EdgeSource) }
	want := []string{edge(ksr, ear), edge(ear, genr), edge(genr, repor), edge(genr, chr), edge(chr, repor)}
	sort.Strings(want)
	if got := chainEdges(tree); !slices.Equal(got, want) {
		t.Fatalf("edges = %v, want %v", got, want)
	}
}

// A generator paused with source-watcher's reconcile annotation keeps its last
// Ready condition; it reads as suspended, not failing.
func TestFluxTreePausedGeneratorIsSuspended(t *testing.T) {
	hr := helmRelease("team", "app")
	hr.Object["spec"] = map[string]any{"chartRef": map[string]any{"kind": "ExternalArtifact", "name": "art"}}
	ea := chainObj("source.toolkit.fluxcd.io/v1", "ExternalArtifact", "team", "art", map[string]any{"sourceRef": chainGenRef})
	gen := chainObj("source.extensions.fluxcd.io/v1beta1", "ArtifactGenerator", "team", "gen", map[string]any{"sources": []any{map[string]any{"alias": "repo", "kind": "GitRepository", "name": "platform"}}})
	gen.SetAnnotations(map[string]string{"source.extensions.fluxcd.io/reconcile": "Disabled"})
	gen.Object["status"] = map[string]any{"conditions": []any{map[string]any{"type": "Ready", "status": "False", "reason": "BuildFailed", "message": "old failure"}}}
	hrr, ear, genr := chainRef("helm.toolkit.fluxcd.io", "HelmRelease", "app"), chainRef(chainSrc, "ExternalArtifact", "art"), chainRef(chainExt, "ArtifactGenerator", "gen")
	objs := map[string]*unstructured.Unstructured{refKey(hrr): hr, refKey(ear): ea, refKey(genr): gen}
	tree, _, err := newTestBuilder(t, &fakeDynamic{objects: objs}, nil).Build(context.Background(), "HelmRelease", "team", "app", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	node := findNode(t, tree, genr)
	if node.Health != "Suspended" || node.TopologyStatus != "degraded" || node.Data["paused"] != true {
		t.Fatalf("paused generator: health=%s topo=%s data=%v", node.Health, node.TopologyStatus, node.Data)
	}
	gen.SetAnnotations(nil)
	tree, _, err = newTestBuilder(t, &fakeDynamic{objects: objs}, nil).Build(context.Background(), "HelmRelease", "team", "app", "helm.toolkit.fluxcd.io")
	if err != nil {
		t.Fatal(err)
	}
	if node := findNode(t, tree, genr); node.Health != "Degraded" || node.Data["paused"] != nil {
		t.Fatalf("unpaused generator: health=%s data=%v", node.Health, node.Data)
	}
}
