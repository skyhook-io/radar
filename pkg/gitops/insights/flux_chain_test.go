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
		"status": map[string]any{"lastAppliedRevision": "main@sha1:aaa", "conditions": []any{map[string]any{"type": "Ready", "status": "True"}}, "inventory": map[string]any{"entries": []any{
			map[string]any{"id": "team_a__ConfigMap"}, map[string]any{"id": "team_b__ConfigMap"}, map[string]any{"id": "team_c__ConfigMap"}}}},
	}}
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{sourceNode("GitRepository", "repo", "main@sha1:bbb", "2026-11-10T09:00:00Z")}, Summary: gitopstree.Summary{Declared: 7}}
	s := Build(root, tree, nil).Summary
	if s.SourceRevision != "main@sha1:bbb" || s.SourceFetchedAt != "2026-11-10T09:00:00Z" || s.AppliedRevision != "main@sha1:aaa" || s.Pending == nil || !*s.Pending || s.Interval != "10m" || s.Objects != 3 {
		t.Fatalf("summary = %+v", s)
	}
}

// The tree is filtered for the caller, so its count would understate what
// Flux applied; without an inventory the count stays unknown.
func TestFluxChainObjectsIgnoreTreeWithoutInventory(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"status":   map[string]any{"lastAppliedRevision": "main@sha1:aaa"},
	}}
	if s := Build(root, &gitopstree.ResourceTree{Summary: gitopstree.Summary{Declared: 4}}, nil).Summary; s.Objects != 0 {
		t.Fatalf("objects = %d, want 0", s.Objects)
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

// A HelmChart is compared by version, so a switch to another chart with the
// same version that failed to deploy must still read pending.
func TestFluxChainHelmChartSwitchToAnotherChartIsPending(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"spec":     map[string]any{"chart": map[string]any{"spec": map[string]any{"chart": "chart-b", "sourceRef": map[string]any{"kind": "HelmRepository", "name": "charts"}}}},
		"status": map[string]any{"helmChart": "team/team-app", "history": []any{
			map[string]any{"version": int64(4), "status": "failed", "chartName": "chart-b", "chartVersion": "1.0.0"},
			map[string]any{"version": int64(3), "status": "deployed", "chartName": "chart-a", "chartVersion": "1.0.0"},
		}},
	}}
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{sourceNode("HelmChart", "team-app", "1.0.0", "")}}
	if s := Build(root, tree, nil).Summary; s.Pending == nil || !*s.Pending {
		t.Fatalf("different chart, same version: %+v", s)
	}
	root.Object["spec"].(map[string]any)["chart"].(map[string]any)["spec"].(map[string]any)["chart"] = "chart-a"
	if s := Build(root, tree, nil).Summary; s.Pending == nil || *s.Pending {
		t.Fatalf("same chart, same version: %+v", s)
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

func TestFluxChainHelmChartWithDigestMatchesChartVersion(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"spec":     map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "app"}},
		"status":   map[string]any{"history": []any{map[string]any{"version": int64(3), "status": "deployed", "chartVersion": "5.5.0"}}},
	}}
	tree := &gitopstree.ResourceTree{Nodes: []gitopstree.Node{sourceNode("HelmChart", "app", "5.5.0@sha256:abc", "")}}
	if s := Build(root, tree, nil).Summary; s.Pending == nil || *s.Pending || s.SourceRevision != "5.5.0" {
		t.Fatalf("summary = %+v", s)
	}
}

func TestFluxChainIntervalOnlyWhileReady(t *testing.T) {
	root := func(ready string, reason string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": "kustomize.toolkit.fluxcd.io/v1", "kind": "Kustomization",
			"metadata": map[string]any{"namespace": "team", "name": "app"},
			"spec":     map[string]any{"interval": "10m", "retryInterval": "2m"},
			"status": map[string]any{"conditions": []any{
				map[string]any{"type": "Ready", "status": ready, "reason": reason},
			}},
		}}
	}
	if s := Build(root("True", "ReconciliationSucceeded"), nil, nil).Summary; s.Interval != "10m" {
		t.Fatalf("ready: summary = %+v", s)
	}
	// A failure before apply adds no history entry, and a dependency wait
	// requeues on its own timer: the last recorded run says nothing about the next.
	for _, reason := range []string{"ArtifactFailed", "BuildFailed", "DependencyNotReady", "ReconciliationFailed"} {
		if s := Build(root("False", reason), nil, nil).Summary; s.Interval != "" {
			t.Fatalf("%s: interval = %q", reason, s.Interval)
		}
	}
	if s := Build(root("Unknown", "Progressing"), nil, nil).Summary; s.Interval != "" {
		t.Fatalf("reconciling: interval = %q", s.Interval)
	}
}
