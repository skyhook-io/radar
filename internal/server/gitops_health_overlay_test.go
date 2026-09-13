package server

import (
	"context"
	"testing"

	"github.com/skyhook-io/radar/pkg/argoapi"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	gitopstree "github.com/skyhook-io/radar/pkg/gitops/tree"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/types"
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

func apiHealthFetch(h *argoapi.ApplicationHealth, calls *int) func(context.Context, string, string) *argoapi.ApplicationHealth {
	return func(context.Context, string, string) *argoapi.ApplicationHealth {
		*calls++
		return h
	}
}

func TestOverlayArgoAPIHealth_AppliesVerdictsAndClearsRadarFill(t *testing.T) {
	tree := overlayTree(gitopstree.HealthModeAppTree, false)
	app := overlayApp("Degraded")
	app.SetUID("app-uid")
	health := &argoapi.ApplicationHealth{UID: "app-uid", ResourceHealthSource: "appTree", Resources: []argoapi.ResourceHealth{
		{Group: "external-secrets.io", Kind: "ClusterSecretStore", Name: "platform", Health: "Degraded", Message: "no route to host"},
		{Kind: "ConfigMap", Namespace: "prod", Name: "vars"}, // Argo has no check
	}}
	var calls int
	if !overlayArgoAPIHealth(context.Background(), tree, app, apiHealthFetch(health, &calls)) {
		t.Fatal("expected the API answer to be applied")
	}
	if !tree.HealthFromAPI {
		t.Error("HealthFromAPI must be set")
	}
	css := tree.Nodes[1]
	if css.Health != "Degraded" || css.HealthSource != gitopstree.HealthSourceControllerAPI || css.HealthMessage != "no route to host" || css.TopologyStatus != "unhealthy" {
		t.Errorf("ClusterSecretStore = %+v, want Argo's Degraded verdict from the API", css)
	}
	if dep := tree.Nodes[2]; dep.Health != "" || dep.HealthSource != "" || dep.TopologyStatus != "unknown" {
		t.Errorf("Radar's topology fill must be cleared when Argo has no verdict for the kind, got %+v", dep)
	}
	if pod := tree.Nodes[5]; pod.Role != gitopstree.RoleGenerated {
		t.Errorf("generated nodes untouched, got %+v", pod)
	}
}

// TestOverlayArgoAPIHealth_HealthlessAnswerIsStillArgos: a tree in which
// Argo has no check for anything is an answer — every node is cleared,
// including a value the CR still carried from before, and no Radar read
// runs on top.
func TestOverlayArgoAPIHealth_HealthlessAnswerIsStillArgos(t *testing.T) {
	tree := overlayTree(gitopstree.HealthModeAppTree, false)
	tree.Nodes[1].Health, tree.Nodes[1].HealthSource = "Degraded", gitopstree.HealthSourceController // stale inline value
	app := overlayApp("Degraded")
	app.SetUID("app-uid")
	var calls int
	if !overlayArgoAPIHealth(context.Background(), tree, app, apiHealthFetch(&argoapi.ApplicationHealth{UID: "app-uid", Resources: []argoapi.ResourceHealth{{Kind: "Namespace", Name: "x"}}}, &calls)) {
		t.Fatal("a healthless answer with identity must be applied")
	}
	if !tree.HealthFromAPI {
		t.Error("HealthFromAPI must be set")
	}
	for _, n := range tree.Nodes {
		if n.Role == gitopstree.RoleDeclared && n.Health != "" {
			t.Errorf("declared node must be cleared to Argo's (empty) verdict, got %+v", n)
		}
	}
}

func TestOverlayArgoAPIHealth_Refusals(t *testing.T) {
	answer := &argoapi.ApplicationHealth{UID: "app-uid", Resources: []argoapi.ResourceHealth{{Kind: "ClusterSecretStore", Group: "external-secrets.io", Name: "platform", Health: "Degraded"}}}
	cases := []struct {
		name   string
		tree   *gitopstree.ResourceTree
		uid    string
		health *argoapi.ApplicationHealth
		calls  int
	}{
		{"inline mode never asks", overlayTree(gitopstree.HealthModeInline, false), "app-uid", answer, 0},
		{"remote destination never asks", overlayTree(gitopstree.HealthModeAppTree, true), "app-uid", answer, 0},
		{"no answer", overlayTree(gitopstree.HealthModeAppTree, false), "app-uid", nil, 1},
		{"another install's app", overlayTree(gitopstree.HealthModeAppTree, false), "other-uid", answer, 1},
		{"answer without identity", overlayTree(gitopstree.HealthModeAppTree, false), "app-uid", &argoapi.ApplicationHealth{Resources: answer.Resources}, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app := overlayApp("Degraded")
			app.SetUID(types.UID(tc.uid))
			var calls int
			if overlayArgoAPIHealth(context.Background(), tc.tree, app, apiHealthFetch(tc.health, &calls)) {
				t.Fatal("must not apply")
			}
			if calls != tc.calls {
				t.Errorf("fetch calls = %d, want %d", calls, tc.calls)
			}
			if tc.tree.HealthFromAPI || tc.tree.Nodes[1].Health != "" {
				t.Errorf("nothing may be applied, got %+v", tc.tree.Nodes[1])
			}
		})
	}
}
