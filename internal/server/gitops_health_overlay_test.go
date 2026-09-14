package server

import (
	"testing"

	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func overlayApp(health string) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "argoproj.io/v1alpha1",
		"kind":       "Application",
		"metadata":   map[string]any{"name": "app", "namespace": "argocd"},
		"status":     map[string]any{"health": map[string]any{"status": health}, "resourceHealthSource": "appTree"},
	}}
}

func overlayTree(mode gitopstree.HealthMode, remote bool) *gitopstree.ResourceTree {
	return &gitopstree.ResourceTree{
		HealthMode:        mode,
		RemoteDestination: remote,
		Nodes: []gitopstree.Node{
			{Role: gitopstree.RoleRoot, Ref: gitopstree.ResourceRef{Group: "argoproj.io", Kind: "Application", Namespace: "argocd", Name: "app"}, Health: "Degraded"},
			{Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Group: "external-secrets.io", Kind: "ClusterSecretStore", Name: "platform"}},
			{Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "web"}, Health: "Progressing", HealthSource: gitopstree.HealthSourceRadar, TopologyStatus: "degraded"},
			{Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Group: "apps", Kind: "Deployment", Namespace: "prod", Name: "api"}, Health: "Healthy", HealthSource: gitopstree.HealthSourceController, TopologyStatus: "healthy"},
			{Role: gitopstree.RoleDeclared, Ref: gitopstree.ResourceRef{Kind: "ConfigMap", Namespace: "prod", Name: "vars"}},
			{Role: gitopstree.RoleGenerated, Ref: gitopstree.ResourceRef{Kind: "Pod", Namespace: "prod", Name: "web-1"}},
		},
	}
}

func fakeProblems(calls *[]string) func(group, kind, namespace, name string) []gitopsinsights.ResourceProblem {
	return func(group, kind, namespace, name string) []gitopsinsights.ResourceProblem {
		*calls = append(*calls, kind+"/"+name)
		switch name {
		case "platform":
			return []gitopsinsights.ResourceProblem{
				{Reason: "Ready: InvalidProviderConfig", Message: "no route to host", Category: "condition_false", Severity: "warning"},
			}
		case "web", "api":
			return []gitopsinsights.ResourceProblem{{Reason: "CrashLoopBackOff", Message: "back-off", Severity: "critical"}}
		}
		return nil
	}
}

func TestOverlayRadarHealth_FillsOnlyEmptyDeclaredNodes(t *testing.T) {
	tree := overlayTree(gitopstree.HealthModeAppTree, false)
	var calls []string
	overlayRadarHealth(tree, overlayApp("Degraded"), fakeProblems(&calls))

	css := tree.Nodes[1]
	if css.Health != "Degraded" || css.HealthSource != gitopstree.HealthSourceRadar || css.HealthReason != "Ready: InvalidProviderConfig" || css.HealthMessage != "no route to host" || css.HealthSeverity != "warning" || css.TopologyStatus != "unhealthy" {
		t.Errorf("ClusterSecretStore should carry the engine's finding as Radar-sourced Degraded, got %+v", css)
	}
	if dep := tree.Nodes[2]; dep.Health != "Degraded" || dep.HealthReason != "CrashLoopBackOff" || dep.HealthSeverity != "critical" || dep.TopologyStatus != "unhealthy" {
		t.Errorf("Radar's topology read (Progressing) must give way to the engine's classified finding, got %+v", dep)
	}
	if api := tree.Nodes[3]; api.Health != "Healthy" || api.HealthSource != gitopstree.HealthSourceController || api.HealthReason != "" {
		t.Errorf("controller-sourced health is final; the engine must not overwrite it, got %+v", api)
	}
	if cm := tree.Nodes[4]; cm.Health != "" || cm.HealthSource != "" {
		t.Errorf("a node the engine has nothing on must stay without health, got %+v", cm)
	}
	for _, c := range calls {
		if c == "Pod/web-1" || c == "Deployment/api" {
			t.Errorf("generated nodes and controller-assessed nodes must not be looked up; saw %q", c)
		}
	}
}

func TestOverlayRadarHealth_SkipsInlineModeRemoteDestinationAndHealthyApp(t *testing.T) {
	cases := []struct {
		name string
		tree *gitopstree.ResourceTree
		app  *unstructured.Unstructured
	}{
		{"inline mode", overlayTree(gitopstree.HealthModeInline, false), overlayApp("Degraded")},
		{"remote destination", overlayTree(gitopstree.HealthModeAppTree, true), overlayApp("Degraded")},
		{"healthy app", overlayTree(gitopstree.HealthModeAppTree, false), overlayApp("Healthy")},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var calls []string
			overlayRadarHealth(tc.tree, tc.app, fakeProblems(&calls))
			if len(calls) != 0 {
				t.Errorf("engine must not be consulted, got calls %v", calls)
			}
			if tc.tree.Nodes[1].Health != "" {
				t.Errorf("no health may be overlaid, got %+v", tc.tree.Nodes[1])
			}
		})
	}
}
