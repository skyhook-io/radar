package gitops

import (
	"reflect"
	"testing"

	"github.com/skyhook-io/radar/pkg/resourceid"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFluxSourceReferencesContracts(t *testing.T) {
	for _, tc := range []struct {
		group, kind, target, namespace, role string
		spec                                 map[string]any
	}{
		{"kustomize.toolkit.fluxcd.io", "Kustomization", "Bucket", "shared", "source", map[string]any{"sourceRef": map[string]any{"kind": "Bucket", "name": "artifact", "namespace": "shared"}}},
		{"kustomize.toolkit.fluxcd.io", "Kustomization", "ExternalArtifact", "team", "source", map[string]any{"sourceRef": map[string]any{"kind": "ExternalArtifact", "name": "artifact"}}},
		{"helm.toolkit.fluxcd.io", "HelmRelease", "OCIRepository", "team", "chart reference", map[string]any{"chartRef": map[string]any{"kind": "OCIRepository", "name": "artifact"}}},
		{"helm.toolkit.fluxcd.io", "HelmRelease", "HelmChart", "shared", "chart reference", map[string]any{"chartRef": map[string]any{"kind": "HelmChart", "name": "artifact", "namespace": "shared"}}},
		{"helm.toolkit.fluxcd.io", "HelmRelease", "HelmRepository", "shared", "chart source", map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "artifact", "namespace": "shared"}}}}},
		// HelmChart sources are local references; namespace is not in their API.
		{"source.toolkit.fluxcd.io", "HelmChart", "GitRepository", "team", "source", map[string]any{"sourceRef": map[string]any{"kind": "GitRepository", "name": "artifact", "namespace": "ignored"}}},
	} {
		t.Run(tc.kind+"/"+tc.target, func(t *testing.T) {
			root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": tc.group + "/v1", "kind": tc.kind, "metadata": map[string]any{"name": "app", "namespace": "team"}, "spec": tc.spec}}
			before := root.DeepCopy()
			want := []FluxSourceRef{{Ref: resourceid.NewRef("source.toolkit.fluxcd.io", tc.target, tc.namespace, "artifact"), Role: tc.role}}
			if got := FluxSourceReferences(root); !reflect.DeepEqual(got, want) {
				t.Fatalf("source = %+v, want %+v", got, want)
			}
			if !reflect.DeepEqual(root, before) {
				t.Fatal("mutated source")
			}
			root.SetAPIVersion("custom.example/v1")
			if got := FluxSourceReferences(root); len(got) != 0 {
				t.Fatal("accepted unrelated group", got)
			}
		})
	}
}

func TestFluxSourceReferencesRejectInvalidDeclarations(t *testing.T) {
	for _, ref := range []map[string]any{{"name": "artifact"}, {"kind": "Secret", "name": "artifact"}, {"kind": "HelmChart"}, {"kind": "OCIRepository", "name": "artifact", "apiVersion": "custom.example/v1"}} {
		root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease", "metadata": map[string]any{"namespace": "team"}, "spec": map[string]any{"chartRef": ref, "chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "do-not-fallback"}}}}}}
		if got := FluxSourceReferences(root); len(got) != 0 {
			t.Fatalf("invalid reference fell back or guessed: %+v", got)
		}
	}
	if got := FluxSourceReferences(nil); len(got) != 0 {
		t.Fatal(got)
	}
}

func TestInlineHelmReleaseUsesRecordedChart(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": "helm.toolkit.fluxcd.io/v2", "kind": "HelmRelease",
		"metadata": map[string]any{"namespace": "team", "name": "app"},
		"spec":     map[string]any{"chart": map[string]any{"spec": map[string]any{"sourceRef": map[string]any{"kind": "HelmRepository", "name": "repo", "namespace": "shared"}}}},
		"status":   map[string]any{"helmChart": "shared/team-app"},
	}}
	want := []FluxSourceRef{
		{Ref: resourceid.NewRef("source.toolkit.fluxcd.io", "HelmRepository", "shared", "repo"), Role: "chart source"},
		{Ref: resourceid.NewRef("source.toolkit.fluxcd.io", "HelmChart", "shared", "team-app"), Role: "chart reference"},
	}
	if got := FluxSourceReferences(root); !reflect.DeepEqual(got, want) {
		t.Fatalf("generated chart = %+v, want %+v", got, want)
	}
	for _, recorded := range []string{"", "unqualified", "/chart", "shared/", "shared/chart/extra"} {
		root.Object["status"] = map[string]any{"helmChart": recorded}
		got := FluxSourceReferences(root)
		if len(got) != 1 || got[0].Kind != "HelmRepository" || got[0].Namespace != "shared" {
			t.Fatalf("unrecorded chart = %+v", got)
		}
	}
	root.Object["status"] = map[string]any{"helmChart": "shared/stale"}
	root.Object["spec"].(map[string]any)["chartRef"] = map[string]any{"kind": "OCIRepository", "name": "current"}
	got := FluxSourceReferences(root)
	if len(got) != 1 || got[0].Kind != "OCIRepository" || got[0].Name != "current" {
		t.Fatalf("chartRef followed stale generated chart: %+v", got)
	}
}
