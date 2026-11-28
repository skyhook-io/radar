package relations

import (
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/kargo"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

var (
	kindKargoStage     = kargo.GroupKind(kargo.KindStage)
	kindKargoFreight   = kargo.GroupKind(kargo.KindFreight)
	kindKargoWarehouse = kargo.GroupKind(kargo.KindWarehouse)
	kindKargoPromotion = kargo.GroupKind(kargo.KindPromotion)
	kindArgoCDApp      = resourceid.GroupKind{Group: "argoproj.io", Kind: "Application"}
)

// KargoPipeline is the family of what a Kargo pipeline's objects name in
// their project: a Promotion's Stage and Freight, a Freight's Warehouse, and
// a Stage's upstream Stages and the Warehouses whose Freight it requests.
// Every reference stays in the project's namespace.
var KargoPipeline = &Family{
	Name:       "kargoPipeline",
	Projection: project("uses", false, "Dependencies", "Dependents"),
	Sources: []Source{
		{Kind: kindKargoPromotion, Targets: []resourceid.GroupKind{kindKargoStage, kindKargoFreight}},
		{Kind: kindKargoFreight, Targets: []resourceid.GroupKind{kindKargoWarehouse}},
		{Kind: kindKargoStage, Targets: []resourceid.GroupKind{kindKargoStage, kindKargoWarehouse}},
	},
	Targets: []resourceid.GroupKind{kindKargoStage, kindKargoFreight, kindKargoWarehouse},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil
		}
		ns := u.GetNamespace()
		name := func(fields ...string) string {
			s, _, _ := unstructured.NestedString(u.Object, fields...)
			return s
		}
		switch {
		case kargo.Is(u, kargo.KindPromotion):
			if stage := name("spec", "stage"); stage != "" {
				visit(Declaration{Target: resourceid.NewRef(kargo.Group, kargo.KindStage, ns, stage), Path: "spec.stage"})
			}
			if freight := name("spec", "freight"); freight != "" {
				visit(Declaration{Target: resourceid.NewRef(kargo.Group, kargo.KindFreight, ns, freight), Path: "spec.freight"})
			}
		case kargo.Is(u, kargo.KindFreight):
			if f := kargo.ParseFreight(u); f.Origin.Kind == kargo.KindWarehouse && f.Origin.Name != "" {
				visit(Declaration{Target: resourceid.NewRef(kargo.Group, kargo.KindWarehouse, ns, f.Origin.Name), Path: "origin"})
			}
		case kargo.Is(u, kargo.KindStage):
			for _, req := range kargo.ParseStage(u).Requests {
				if req.Origin.Kind == kargo.KindWarehouse && req.Origin.Name != "" {
					visit(Declaration{Target: resourceid.NewRef(kargo.Group, kargo.KindWarehouse, ns, req.Origin.Name), Path: "spec.requestedFreight[].origin"})
				}
				for _, up := range req.Stages {
					if up != "" && up != u.GetName() {
						visit(Declaration{Target: resourceid.NewRef(kargo.Group, kargo.KindStage, ns, up), Path: "spec.requestedFreight[].sources.stages[]"})
					}
				}
			}
		}
		return nil
	},
}

// KargoArgoCDApps is the family of the Argo CD Applications a Kargo Stage's
// last Promotion updated or waited on, which the Stage's argocd-context
// annotation records; the Stage's health is read from them. They are usually
// in another namespace. An Application's authorized-stage annotation is not
// a declaration here: it says which Stages may update it, not which did.
var KargoArgoCDApps = &Family{
	Name:           "kargoArgoCDApps",
	Projection:     project("uses", false, "Dependencies", "Dependents"),
	Sources:        []Source{{Kind: kindKargoStage}},
	Targets:        []resourceid.GroupKind{kindArgoCDApp},
	CrossNamespace: true,
	ReferrersIndex: referrersIndex("kargoArgoCDApps"),
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		u, ok := unstructuredOf(obj, kindKargoStage)
		if !ok {
			return nil
		}
		apps, err := kargo.ArgoCDApps(u)
		for _, app := range apps {
			if app.Namespace != "" {
				visit(Declaration{Target: resourceid.NewRef("argoproj.io", "Application", app.Namespace, app.Name), Path: "metadata.annotations[" + kargo.ArgoCDContextAnnotation + "]"})
			}
		}
		return err
	},
}
