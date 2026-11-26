package resourcecontext

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
	"github.com/skyhook-io/radar/pkg/topology"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func fluxPreviewFixture(manyNamespaces bool) *contextFixtureSession {
	const sourceGroup = "source.toolkit.fluxcd.io"
	source := resourceid.NewRef(sourceGroup, "GitRepository", "flux-system", "platform")
	object := func(ref resourceid.Ref) *unstructured.Unstructured {
		return &unstructured.Unstructured{Object: map[string]any{
			"apiVersion": ref.Group + "/v1", "kind": ref.Kind,
			"metadata": map[string]any{"name": ref.Name, "namespace": ref.Namespace},
		}}
	}
	repo := object(source)
	session := newContextFixtureSession(source, repo, nil)
	chart := resourceid.NewRef(sourceGroup, "HelmChart", source.Namespace, "rare-chart")
	for i := range 801 {
		consumer := chart
		if i < 800 {
			ns := source.Namespace
			if manyNamespaces {
				ns = fmt.Sprintf("tenant-%04d", i)
			}
			consumer = resourceid.NewRef("kustomize.toolkit.fluxcd.io", "Kustomization", ns, fmt.Sprintf("app-%04d", i))
		}
		obj := object(consumer)
		obj.Object["spec"] = map[string]any{"sourceRef": map[string]any{"kind": source.Kind, "name": source.Name, "namespace": source.Namespace}}
		session.objects = append(session.objects, obj)
	}

	return session
}

func TestResourceContextFluxPreview(t *testing.T) {
	for _, manyNamespaces := range []bool{false, true} {
		t.Run(fmt.Sprintf("manyNamespaces=%t", manyNamespaces), func(t *testing.T) {
			session := fluxPreviewFixture(manyNamespaces)
			var first string
			for run := range 2 {
				upstream, final := &countingRefChecker{}, &countingRefChecker{}
				access := fixtureAccess{t.Context(), upstream}
				rel, extras := topology.RelationsContextAnswer(session, access, access, ownership.NewView(session, access), session.subject, session.obj, nil, nil)
				if len(rel.Dependents) != 801 {
					t.Fatalf("family returned %d consumers", len(rel.Dependents))
				}
				rc := Build(t.Context(), session.obj, Options{Relationships: rel, RelationshipExtras: extras, AccessChecker: final})
				field, err := json.Marshal(map[string]any{"dependents": rc.Dependents})
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("refs=%d field_bytes=%d family_checks=%d builder_checks=%d", len(rc.Dependents), len(field), upstream.calls, final.calls)
				distinct := make(map[ContextRef]bool)
				for _, r := range rc.Dependents {
					distinct[r] = true
				}
				rare := ContextRef{Group: session.subject.Group, Kind: "HelmChart", Namespace: session.subject.Namespace, Name: "rare-chart"}
				wantChecks := 2
				if manyNamespaces {
					wantChecks = 21
				}
				if len(rc.Dependents) != 20 || len(distinct) != 20 || !distinct[rare] || final.calls != wantChecks {
					t.Fatalf("preview %v, final checks %d (want %d)", rc.Dependents, final.calls, wantChecks)
				}
				gaps := slices.DeleteFunc(slices.Clone(rc.Omitted), func(g OmittedField) bool { return g.Field != "dependents" })
				if !slices.Equal(gaps, []OmittedField{{Field: "dependents", Reason: OmittedBudgetExceeded}}) {
					t.Fatalf("dependents omissions %v", gaps)
				}
				if run == 1 && first != string(field) {
					t.Fatal("insertion order changed the preview")
				}
				first = string(field)
				slices.Reverse(session.objects)
			}
		})
	}
}

func BenchmarkResourceContextFluxPreview(b *testing.B) {
	for _, manyNamespaces := range []bool{false, true} {
		b.Run(fmt.Sprintf("manyNamespaces=%t", manyNamespaces), func(b *testing.B) {
			session := fluxPreviewFixture(manyNamespaces)
			access := fixtureAccess{b.Context(), allowAllChecker{}}
			b.ReportAllocs()
			for b.Loop() {
				rel, extras := topology.RelationsContextAnswer(session, access, access, ownership.NewView(session, access), session.subject, session.obj, nil, nil)
				Build(b.Context(), session.obj, Options{Relationships: rel, RelationshipExtras: extras, AccessChecker: allowAllChecker{}})
			}
		})
	}
}
