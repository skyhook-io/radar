package tree

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func unstr(apiVersion, kind, ns, name string, annotations, labels map[string]string) *unstructured.Unstructured {
	meta := map[string]any{
		"name":      name,
		"namespace": ns,
	}
	if len(annotations) > 0 {
		m := map[string]any{}
		for k, v := range annotations {
			m[k] = v
		}
		meta["annotations"] = m
	}
	if len(labels) > 0 {
		m := map[string]any{}
		for k, v := range labels {
			m[k] = v
		}
		meta["labels"] = m
	}
	return &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": apiVersion,
		"kind":       kind,
		"metadata":   meta,
	}}
}

func TestArgoTrackingMatches_AnnotationPrefix(t *testing.T) {
	cases := []struct {
		name string
		ann  map[string]string
		lbl  map[string]string
		app  string
		want bool
	}{
		{
			name: "annotation exact app prefix",
			ann:  map[string]string{ArgoTrackingAnnotation: "web:apps/Deployment:prod/web"},
			app:  "web",
			want: true,
		},
		{
			name: "annotation prefix-anchored — web shouldn't match web-staging",
			ann:  map[string]string{ArgoTrackingAnnotation: "web-staging:apps/Deployment:staging/web-staging"},
			app:  "web",
			want: false,
		},
		{
			name: "label fallback",
			lbl:  map[string]string{ArgoInstanceLabel: "web"},
			app:  "web",
			want: true,
		},
		{
			name: "label mismatch",
			lbl:  map[string]string{ArgoInstanceLabel: "other-app"},
			app:  "web",
			want: false,
		},
		{
			name: "annotation wins over label",
			ann:  map[string]string{ArgoTrackingAnnotation: "web:apps/Deployment:prod/web"},
			lbl:  map[string]string{ArgoInstanceLabel: "different"},
			app:  "web",
			want: true,
		},
		{
			name: "no tracking signal",
			app:  "web",
			want: false,
		},
		{
			name: "empty app never matches",
			ann:  map[string]string{ArgoTrackingAnnotation: "web:apps/Deployment:prod/web"},
			app:  "",
			want: false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			obj := unstr("apps/v1", "Deployment", "prod", "web", tc.ann, tc.lbl)
			if got := ArgoTrackingMatches(obj, tc.app); got != tc.want {
				t.Errorf("ArgoTrackingMatches = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildManagedTree_SyntheticRootAndDeclaredChildren(t *testing.T) {
	matched := []*unstructured.Unstructured{
		unstr("apps/v1", "Deployment", "prod", "web",
			map[string]string{ArgoTrackingAnnotation: "web:apps/Deployment:prod/web"}, nil),
		unstr("v1", "Service", "prod", "web",
			map[string]string{ArgoTrackingAnnotation: "web:/Service:prod/web"}, nil),
		// Sort verification: 'apps' namespace alphabetically before 'prod'.
		unstr("v1", "ConfigMap", "apps", "web-cfg",
			map[string]string{ArgoTrackingAnnotation: "web:/ConfigMap:apps/web-cfg"}, nil),
	}

	tree := BuildManagedTree("web", "argocd", matched)
	if tree == nil {
		t.Fatal("BuildManagedTree returned nil")
	}

	// Root is synthetic Application.
	if tree.Root.Ref.Kind != "Application" {
		t.Errorf("root Kind = %q, want Application", tree.Root.Ref.Kind)
	}
	if tree.Root.Ref.Name != "web" {
		t.Errorf("root Name = %q, want web", tree.Root.Ref.Name)
	}
	if tree.Root.Tool != ToolArgoCD {
		t.Errorf("root Tool = %q, want %q", tree.Root.Tool, ToolArgoCD)
	}

	// 1 root + 3 declared.
	if len(tree.Nodes) != 4 {
		t.Fatalf("Nodes = %d, want 4 (root + 3 declared)", len(tree.Nodes))
	}
	if len(tree.Edges) != 3 {
		t.Errorf("Edges = %d, want 3 (one per declared child)", len(tree.Edges))
	}

	// Summary counts.
	if tree.Summary.Declared != 3 {
		t.Errorf("Summary.Declared = %d, want 3", tree.Summary.Declared)
	}

	// Sort: ConfigMap (apps ns) first, then Deployment (prod), then Service (prod).
	wantOrder := []string{"web", "web-cfg", "web", "web"} // root, cm, deployment, service — sorted by ns then kind
	if got := tree.Nodes[0].Ref.Name; got != wantOrder[0] {
		t.Errorf("Nodes[0].Name = %q, want %q (root)", got, wantOrder[0])
	}
	if got := tree.Nodes[1].Ref.Kind; got != "ConfigMap" {
		t.Errorf("Nodes[1].Kind = %q, want ConfigMap (apps ns sorts first)", got)
	}
	if got := tree.Nodes[2].Ref.Kind; got != "Deployment" {
		t.Errorf("Nodes[2].Kind = %q, want Deployment (prod ns, kind D < S)", got)
	}
	if got := tree.Nodes[3].Ref.Kind; got != "Service" {
		t.Errorf("Nodes[3].Kind = %q, want Service", got)
	}

	// Every edge emanates from the synthetic root.
	for _, e := range tree.Edges {
		if e.Source != tree.Root.ID {
			t.Errorf("edge Source = %q, want root ID %q", e.Source, tree.Root.ID)
		}
		if e.Type != EdgeOwns {
			t.Errorf("edge Type = %q, want %q", e.Type, EdgeOwns)
		}
	}
}

func TestBuildManagedTree_EmptyMatchedListYieldsRootOnly(t *testing.T) {
	tree := BuildManagedTree("nobody-deploys-me", "argocd", nil)
	if tree == nil {
		t.Fatal("nil tree")
	}
	if len(tree.Nodes) != 1 {
		t.Errorf("Nodes = %d, want 1 (just the synthetic root)", len(tree.Nodes))
	}
	if len(tree.Edges) != 0 {
		t.Errorf("Edges = %d, want 0", len(tree.Edges))
	}
	if tree.Summary.Declared != 0 {
		t.Errorf("Summary.Declared = %d, want 0", tree.Summary.Declared)
	}
}
