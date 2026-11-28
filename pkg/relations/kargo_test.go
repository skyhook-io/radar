package relations

import (
	"slices"
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

func kargoObject(kind, name string, fields map[string]any) *unstructured.Unstructured {
	u := object("kargo.akuity.io/v1alpha1", kind, name, nil)
	delete(u.Object, "spec")
	for k, v := range fields {
		u.Object[k] = v
	}
	return u
}

func TestKargoPipelineNamesWhatEachObjectComesFrom(t *testing.T) {
	warehouse := kargoObject("Warehouse", "shop", nil)
	freight := kargoObject("Freight", "3f2a9c1", map[string]any{"origin": map[string]any{"kind": "Warehouse", "name": "shop"}})
	dev := kargoObject("Stage", "dev", map[string]any{"spec": map[string]any{"requestedFreight": []any{
		map[string]any{"origin": map[string]any{"kind": "Warehouse", "name": "shop"}, "sources": map[string]any{"direct": true}},
	}}})
	uat := kargoObject("Stage", "uat", map[string]any{"spec": map[string]any{"requestedFreight": []any{
		map[string]any{"origin": map[string]any{"kind": "Warehouse", "name": "shop"}, "sources": map[string]any{"stages": []any{"dev"}}},
	}}})
	promotion := kargoObject("Promotion", "uat.01", map[string]any{"spec": map[string]any{"stage": "uat", "freight": "3f2a9c1"}})
	objects := []runtime.Object{warehouse, freight, dev, uat, promotion}

	checkFamily(t, KargoPipeline, []runtime.Object{warehouse, freight}, freight, refOf(warehouse), "origin")
	checkFamily(t, KargoPipeline, []runtime.Object{warehouse, dev}, dev, refOf(warehouse), "spec.requestedFreight[].origin")

	v := NewView(allNamespaces{cluster: cluster{objects: objects}}, ownership.Unrestricted())
	declared := func(obj runtime.Object) []resourceid.Ref {
		var out []resourceid.Ref
		for _, d := range v.References(KargoPipeline, refOf(obj), obj).Declared {
			if d.State != TargetPresent {
				t.Errorf("%v: %+v not present", refOf(obj), d)
			}
			out = append(out, d.Ref)
		}
		return out
	}
	if got := declared(uat); !slices.Equal(got, []resourceid.Ref{refOf(dev), refOf(warehouse)}) {
		t.Errorf("uat declares %v", got)
	}
	if got := declared(promotion); !slices.Equal(got, []resourceid.Ref{refOf(freight), refOf(uat)}) {
		t.Errorf("promotion declares %v", got)
	}
	if got := referrerRefs(v.Referrers(KargoPipeline, refOf(dev), nil)); !slices.Equal(got, []resourceid.Ref{refOf(uat)}) {
		t.Errorf("dev's downstream = %v", got)
	}

	// Another tool's Stage or Promotion is not Kargo's.
	other := object("example.com/v1", "Promotion", "uat.02", map[string]any{"stage": "uat"})
	if got := v.References(KargoPipeline, refOf(other), other).Declared; len(got) != 0 {
		t.Errorf("a Promotion of another group declares %+v", got)
	}
}

func TestKargoArgoCDAppsCrossNamespaces(t *testing.T) {
	app := inNamespace(object("argoproj.io/v1alpha1", "Application", "shop-uat", nil), "argocd")
	stage := kargoObject("Stage", "uat", nil)
	stage.SetAnnotations(map[string]string{"kargo.akuity.io/argocd-context": `[{"name":"shop-uat","namespace":"argocd"}]`})
	session := allNamespaces{cluster: cluster{objects: []runtime.Object{app, stage}}}
	checkFamilyIn(t, session, KargoArgoCDApps, stage, refOf(app), "metadata.annotations[kargo.akuity.io/argocd-context]")

	shopOnly := NewView(session, namespaceAccess{namespace: "shop"})
	if got := shopOnly.References(KargoArgoCDApps, refOf(stage), stage); len(got.Declared) != 1 || got.Declared[0].State != TargetWithheld {
		t.Errorf("an app the caller can't read: %+v", got)
	}
	argoOnly := NewView(session, namespaceAccess{namespace: "argocd"})
	if got := argoOnly.Referrers(KargoArgoCDApps, refOf(app), nil); len(got.Referrers) != 0 || !got.Coverage.Narrowed {
		t.Errorf("a Stage the caller can't read: %+v", got)
	}

	// authorized-stage is permission, not evidence: it declares nothing.
	app.SetAnnotations(map[string]string{"kargo.akuity.io/authorized-stage": "shop:uat"})
	stage.SetAnnotations(nil)
	if got := referrerRefs(NewView(session, ownership.Unrestricted()).Referrers(KargoArgoCDApps, refOf(app), nil)); len(got) != 0 {
		t.Errorf("referrers = %v", got)
	}
}

func TestKargoArgoCDAppsReportsAMalformedAnnotation(t *testing.T) {
	stage := kargoObject("Stage", "uat", nil)
	stage.SetAnnotations(map[string]string{"kargo.akuity.io/argocd-context": "{"})
	if err := KargoArgoCDApps.Declared(stage, func(Declaration) { t.Error("declared from a malformed annotation") }); err == nil {
		t.Error("a malformed annotation is a read error")
	}
}
