package tree

import (
	"testing"

	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestClassifyGitOpsKind pins the catalog of kinds that count as
// "GitOps detail-page CRs" — clicking these in a parent's tree must
// route to their own GitOps detail view rather than the standard
// resource drawer. New Argo/Flux CRDs that should also act as portals
// must be added here, and any kind removed from the catalog will
// silently regress nested-navigation behavior.
func TestClassifyGitOpsKind(t *testing.T) {
	mk := func(api, kind string) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": api,
			"kind":       kind,
		}}
	}
	tests := []struct {
		name     string
		obj      *unstructured.Unstructured
		wantTool string
		wantKind string
	}{
		{"argo Application", mk("argoproj.io/v1alpha1", "Application"), "argocd", "Application"},
		{"argo ApplicationSet", mk("argoproj.io/v1alpha1", "ApplicationSet"), "argocd", "ApplicationSet"},
		{"argo AppProject", mk("argoproj.io/v1alpha1", "AppProject"), "argocd", "AppProject"},
		{"flux Kustomization", mk("kustomize.toolkit.fluxcd.io/v1", "Kustomization"), "fluxcd", "Kustomization"},
		{"flux HelmRelease", mk("helm.toolkit.fluxcd.io/v2", "HelmRelease"), "fluxcd", "HelmRelease"},
		// Flux source CRs are NOT portals — they have no downstream tree and
		// the standard resource drawer renders their spec/status cleanly.
		// Routing them to a GitOps detail page yielded a degenerate one-node view.
		{"flux GitRepository (not portal)", mk("source.toolkit.fluxcd.io/v1", "GitRepository"), "", ""},
		{"flux OCIRepository (not portal)", mk("source.toolkit.fluxcd.io/v1beta2", "OCIRepository"), "", ""},
		{"flux HelmRepository (not portal)", mk("source.toolkit.fluxcd.io/v1", "HelmRepository"), "", ""},
		{"flux Bucket (not portal)", mk("source.toolkit.fluxcd.io/v1beta2", "Bucket"), "", ""},
		// Negatives: same group but unfamiliar kind, and kinds that
		// share a name with a GitOps CR but live elsewhere (e.g. core
		// Service vs Knative Service). These must NOT classify.
		{"unknown argo kind", mk("argoproj.io/v1alpha1", "Rollout"), "", ""},
		{"core Service", mk("v1", "Service"), "", ""},
		{"knative Service shares name but different group", mk("serving.knative.dev/v1", "Service"), "", ""},
		{"nil object", nil, "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, kind := classifyGitOpsKind(tt.obj)
			if tool != tt.wantTool || kind != tt.wantKind {
				t.Fatalf("got (%q, %q), want (%q, %q)", tool, kind, tt.wantTool, tt.wantKind)
			}
		})
	}
}

func TestRolloutTopologyInfoAndPriority(t *testing.T) {
	info := infoFromTopology(topology.Node{
		Kind: topology.KindRollout,
		Data: map[string]any{
			"readyReplicas": int64(2),
			"totalReplicas": int64(3),
		},
	})
	if len(info) != 1 || info[0].Name != "Ready" || info[0].Value != "2/3" {
		t.Fatalf("rollout info = %#v, want Ready 2/3", info)
	}

	if got, want := kindPriority("Rollout"), kindPriority("Deployment"); got != want {
		t.Fatalf("rollout priority = %d, want deployment priority %d", got, want)
	}
}

func TestSummarize_ExcludesRootAndGroupFromDegraded(t *testing.T) {
	nodes := []Node{
		{Role: RoleRoot, Ref: ResourceRef{Kind: "Application", Name: "app"}, Health: "Degraded", Sync: "OutOfSync"}, // the app itself — must NOT count
		{Role: RoleDeclared, Ref: ResourceRef{Kind: "HTTPRoute", Name: "r"}, Health: "Degraded"},
		{Role: RoleDeclared, Ref: ResourceRef{Kind: "Deployment", Name: "d"}, Health: "Healthy", Sync: "OutOfSync"},
		{Role: RoleGroup, Ref: ResourceRef{Kind: "ConfigMap", Name: "3 ConfigMaps"}, Health: "Degraded", Count: 3}, // synthetic bucket — must NOT count
	}
	s := summarize(nodes)
	if s.Degraded != 1 {
		t.Errorf("Degraded = %d, want 1 (only the managed HTTPRoute; not the app or the group)", s.Degraded)
	}
	if s.OutOfSync != 1 {
		t.Errorf("OutOfSync = %d, want 1 (only the managed Deployment; not the app root)", s.OutOfSync)
	}
}
