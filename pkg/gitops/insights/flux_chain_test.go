package insights

import (
	"testing"

	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxRevisionsMatch(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		want bool
	}{
		{"v1@sha256:abc", "sha256:abc", true},
		{"latest@sha256:x", "latest@sha256:y", false},
		{"main@sha1:a", "main@sha1:a", true},
		{"5.5.0", "5.4.0", false},
	} {
		if got := fluxRevisionsMatch(tc.a, tc.b); got != tc.want {
			t.Errorf("fluxRevisionsMatch(%q, %q) = %v, want %v", tc.a, tc.b, got, tc.want)
		}
	}
}

func sourceNode(kind, name, revision, updated string) gitopstree.Node {
	data := map[string]any{"artifactRevision": revision}
	if updated != "" {
		data["artifactUpdatedAt"] = updated
	}
	return gitopstree.Node{Ref: gitopstree.ResourceRef{Group: "source.toolkit.fluxcd.io", Kind: kind, Namespace: "team", Name: name}, Dependency: true, Data: data}
}

func TestFluxChainKustomizationPending(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"spec":     map[string]any{"interval": "10m", "sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}},
		"status": map[string]any{"lastAppliedRevision": "main@sha1:aaa", "inventory": map[string]any{"entries": []any{
			map[string]any{"id": "team_a__ConfigMap"}, map[string]any{"id": "team_b__ConfigMap"}, map[string]any{"id": "team_c__ConfigMap"}}}},
	}}
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{sourceNode("GitRepository", "repo", "main@sha1:bbb", "2026-11-10T09:00:00Z")}, Summary: gitopstree.Summary{Declared: 7}}
	s := Build(root, tree, nil).Summary
	if s.SourceRevision != "main@sha1:bbb" || s.SourceFetchedAt != "2026-11-10T09:00:00Z" || s.AppliedRevision != "main@sha1:aaa" || s.Pending == nil || !*s.Pending || s.Interval != "10m" || s.Objects != 3 {
		t.Fatalf("summary = %+v", s)
	}
}

func TestFluxChainObjectsFallBackToTree(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"status":   map[string]any{"lastAppliedRevision": "main@sha1:aaa"},
	}}
	if s := Build(root, &gitopstree.ResourceTree{Summary: gitopstree.Summary{Declared: 4}}, nil).Summary; s.Objects != 4 {
		t.Fatalf("objects = %d, want 4", s.Objects)
	}
}

func TestFluxChainHelmReleaseExternalArtifact(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"spec":     map[string]any{"chartRef": map[string]any{"kind": "ExternalArtifact", "name": "chart"}},
		"status":   map[string]any{"history": []any{map[string]any{"version": int64(3), "status": "deployed", "chartVersion": "1.0.0", "ociDigest": "sha256:abc"}}},
	}}
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{sourceNode("ExternalArtifact", "chart", "latest@sha256:abc", "")}}
	s := Build(root, tree, nil).Summary
	if s.AppliedRevision != "sha256:abc" || s.Pending == nil || *s.Pending {
		t.Fatalf("summary = %+v", s)
	}
}

func TestFluxChainInlineHelmReleaseUsesHelmChart(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"spec":     map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": "app", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "charts"}}}},
		"status":   map[string]any{"helmChart": "team/team-app", "history": []any{map[string]any{"version": int64(3), "status": "deployed", "chartVersion": "5.4.0"}}},
	}}
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{
		sourceNode("HelmRepository", "charts", "sha256:index", ""),
		sourceNode("HelmChart", "team-app", "5.5.0", ""),
	}}
	s := Build(root, tree, nil).Summary
	if s.SourceRevision != "5.5.0" || s.AppliedRevision != "5.4.0" || s.Pending == nil || !*s.Pending {
		t.Fatalf("summary = %+v", s)
	}
}

func TestFluxChainUnknownWithoutSourceNode(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"spec":     map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "repo"}},
		"status":   map[string]any{"lastAppliedRevision": "main@sha1:aaa"},
	}}
	remote := sourceNode("GitRepository", "repo", "main@sha1:bbb", "")
	remote.Remote = true
	s := Build(root, &gitopstree.ResourceTree{Nodes: []gitopstree.Node{remote}}, nil).Summary
	if s.SourceRevision != "" || s.Pending != nil || s.AppliedRevision != "main@sha1:aaa" {
		t.Fatalf("summary = %+v", s)
	}
}
