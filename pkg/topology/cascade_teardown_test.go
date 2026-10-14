package topology

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

func TestCascadePreviewDisclosesGitOpsTeardownByPolicy(t *testing.T) {
	kinds := map[string]struct {
		gvr  schema.GroupVersionResource
		kind string
	}{
		"Application":   {schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}, "Application"},
		"Kustomization": {schema.GroupVersionResource{Group: "kustomize.toolkit.fluxcd.io", Version: "v1", Resource: "kustomizations"}, "Kustomization"},
		"HelmRelease":   {schema.GroupVersionResource{Group: "helm.toolkit.fluxcd.io", Version: "v2", Resource: "helmreleases"}, "HelmRelease"},
	}
	for _, tc := range []struct {
		name, kind string
		finalizers []string
		spec       map[string]any
		want       string // controller/action, "" for no teardown
	}{
		{"argo cascade", "Application", []string{"resources-finalizer.argocd.argoproj.io"}, nil, "Argo CD/prune"},
		{"argo background cascade", "Application", []string{"resources-finalizer.argocd.argoproj.io/background"}, nil, "Argo CD/prune"},
		{"argo non-cascading", "Application", nil, nil, ""},
		{"argo deprecated cascade key", "Application", []string{"foreground-cascade.argocd.argoproj.io"}, nil, "Argo CD/prune"},
		{"flux controller-specific key", "Kustomization", []string{"finalizers.kustomize.toolkit.fluxcd.io"}, map[string]any{"prune": true}, "Flux/prune"},
		{"helm controller-specific key", "HelmRelease", []string{"finalizers.helm.toolkit.fluxcd.io"}, map[string]any{}, "Flux/uninstall"},
		{"flux prune", "Kustomization", []string{"finalizers.fluxcd.io"}, map[string]any{"prune": true}, "Flux/prune"},
		{"flux no prune", "Kustomization", []string{"finalizers.fluxcd.io"}, map[string]any{"prune": false}, ""},
		{"flux delete policy", "Kustomization", []string{"finalizers.fluxcd.io"}, map[string]any{"prune": false, "deletionPolicy": "Delete"}, "Flux/prune"},
		{"flux orphan policy", "Kustomization", []string{"finalizers.fluxcd.io"}, map[string]any{"prune": true, "deletionPolicy": "Orphan"}, ""},
		{"flux never reconciled", "Kustomization", nil, map[string]any{"prune": true}, ""},
		{"helm release", "HelmRelease", []string{"finalizers.fluxcd.io"}, map[string]any{}, "Flux/uninstall"},
		// Both Flux controllers release a suspended object's finalizer without
		// tearing anything down.
		{"suspended helm release", "HelmRelease", []string{"finalizers.fluxcd.io"}, map[string]any{"suspend": true}, ""},
		{"suspended flux prune", "Kustomization", []string{"finalizers.fluxcd.io"}, map[string]any{"prune": true, "suspend": true}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			k := kinds[tc.kind]
			obj := genericIdentityObject(k.gvr, k.kind, "gitops", "apps")
			obj.SetFinalizers(tc.finalizers)
			if tc.kind == "Application" {
				obj.Object["spec"] = map[string]any{"destination": map[string]any{"name": "in-cluster"}}
			}
			if tc.spec != nil {
				obj.Object["spec"] = tc.spec
			}
			dynamic := &genericIdentityDynamic{
				watched:   []schema.GroupVersionResource{k.gvr},
				kinds:     map[schema.GroupVersionResource]string{k.gvr: k.kind},
				resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{k.gvr: {obj}},
				listCalls: map[schema.GroupVersionResource]int{},
			}
			rootID := map[string]string{"Application": "application", "Kustomization": "kustomization", "HelmRelease": "helmrelease"}[tc.kind] + "/gitops/apps"
			topo := &Topology{
				Nodes: []Node{
					{ID: rootID, Kind: NodeKind(tc.kind), Name: "apps", Data: map[string]any{"namespace": "gitops", "apiVersion": obj.GetAPIVersion()}},
					{ID: "deployment/prod/web", Kind: KindDeployment, Name: "web", Data: map[string]any{"namespace": "prod"}},
				},
				Edges: []Edge{{ID: "inventory", Source: rootID, Target: "deployment/prod/web", Type: EdgeManages}},
			}
			p := GetCascadeDeletePreview(ResourceRef{Kind: tc.kind, Namespace: "gitops", Name: "apps", Group: k.gvr.Group}, topo, dynamic)
			if !p.RootResolved || p.Basis != "ownerReferences" {
				t.Fatalf("preview = %+v", p)
			}
			if len(p.Dependents) != 0 {
				t.Errorf("inventory reported as garbage collection: %+v", p.Dependents)
			}
			got := ""
			if p.ControllerTeardown != nil {
				got = p.ControllerTeardown.Controller + "/" + p.ControllerTeardown.Action
				if len(p.ControllerTeardown.Resources) != 1 || p.ControllerTeardown.Resources[0].Name != "web" {
					t.Errorf("teardown resources = %+v", p.ControllerTeardown.Resources)
				}
			}
			if got != tc.want {
				t.Errorf("teardown = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestRemoteTeardownDoesNotHydrateLocalInventory(t *testing.T) {
	gvr := schema.GroupVersionResource{Group: "argoproj.io", Version: "v1alpha1", Resource: "applications"}
	app := genericIdentityObject(gvr, "Application", "gitops", "remote")
	app.SetFinalizers([]string{argoResourcesFinalizer})
	app.Object["spec"] = map[string]any{"destination": map[string]any{"name": "other-cluster"}}
	dynamic := &genericIdentityDynamic{watched: []schema.GroupVersionResource{gvr}, kinds: map[schema.GroupVersionResource]string{gvr: "Application"}, resources: map[schema.GroupVersionResource][]*unstructured.Unstructured{gvr: {app}}}
	root := Node{ID: "application/gitops/remote", Kind: KindApplication, Name: "remote", Data: map[string]any{"namespace": "gitops", "apiVersion": app.GetAPIVersion()}}
	topo := &Topology{Nodes: []Node{root, {ID: "deployment/team/web", Kind: KindDeployment, Name: "web"}}, Edges: []Edge{{Source: root.ID, Target: "deployment/team/web", Type: EdgeManages}}}
	for _, dest := range []string{"remote", "unknown"} {
		if dest == "unknown" {
			delete(app.Object, "spec")
		}
		notice := controllerTeardown(&root, topo, dynamic)
		if notice == nil || notice.Destination != dest || len(notice.Resources) != 0 {
			t.Fatalf("destination %s: %+v", dest, notice)
		}
	}
}
