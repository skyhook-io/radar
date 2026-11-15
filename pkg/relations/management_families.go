package relations

import (
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// FluxHelmOrigins is the origin-label claim on the seven kinds the graph
// associates with inventory-less HelmReleases. Projection checks the release's
// inventory and destination before treating a claim as management.
var FluxHelmOrigins = &Family{
	Name:           "fluxHelmOrigins",
	Sources:        []Source{{Kind: kindDeployment}, {Kind: kindService}, {Kind: kindStatefulSet}, {Kind: kindDaemonSet}, {Kind: kindJob}, {Kind: kindCronJob}, {Kind: kindRollout}},
	Targets:        []resourceid.GroupKind{{Group: "helm.toolkit.fluxcd.io", Kind: "HelmRelease"}},
	CrossNamespace: true,
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		m, err := apimeta.Accessor(obj)
		if err != nil {
			return err
		}
		if r, ok := gitops.FluxHelmLabelledRelease(m); ok {
			visit(Declaration{Target: r, Path: "metadata.labels"})
		}
		return nil
	},
}

// CronWorkflowRuns preserves Argo's controller reference before its cron-workflow
// label fallback, using the same producer rule as the display graph.
var CronWorkflowRuns = &Family{
	Name:    "cronWorkflowRuns",
	Sources: []Source{{Kind: kindWorkflow}},
	Targets: []resourceid.GroupKind{kindCronWorkflow},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil
		}
		var name string
		for _, r := range u.GetOwnerReferences() {
			if r.Kind == "CronWorkflow" && resourceid.GroupFromAPIVersion(r.APIVersion) == "argoproj.io" && r.Name != "" && r.Controller != nil && *r.Controller {
				name = r.Name
				break
			}
		}
		if name == "" {
			name = u.GetLabels()["workflows.argoproj.io/cron-workflow"]
		}
		if name != "" {
			visit(Declaration{Target: resourceid.NewRef("argoproj.io", "CronWorkflow", u.GetNamespace(), name), Path: "metadata"})
		}
		return nil
	},
}
