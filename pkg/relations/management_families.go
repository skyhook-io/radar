package relations

import (
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/configrefs"
	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// FluxHelmOrigins is the origin-label claim on the seven kinds the graph
// associates with inventory-less HelmReleases. Projection checks the release's
// inventory and destination before treating a claim as management.
var FluxHelmOrigins = &Family{
	ScanAccepted:   "Asked only for a HelmRelease that records no inventory, whose label-claimed members are workload kinds, never Pods.",
	Name:           "fluxHelmOrigins",
	Projection:     project("manages", true, "Managers", "Manages"),
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

// ArgoWorkflowCronWorkflows is the family of the CronWorkflow that started a
// Workflow. It prefers Argo's controller reference to its cron-workflow
// label fallback, using the same producer rule as the display graph.
var ArgoWorkflowCronWorkflows = &Family{
	Name:       "argoWorkflowCronWorkflows",
	Projection: project("manages", true, "Managers", "Manages"),
	Sources:    []Source{{Kind: kindWorkflow}},
	Targets:    []resourceid.GroupKind{kindCronWorkflow},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil
		}
		name := configrefs.ArgoWorkflowCronOwnerName(u)
		if name != "" {
			visit(Declaration{Target: resourceid.NewRef("argoproj.io", "CronWorkflow", u.GetNamespace(), name), Path: "metadata"})
		}
		return nil
	},
}

// ArgoPodWorkflows is the family of the Workflow a Pod's workflow label
// names, so a Pod whose owner reference is unavailable still shows it.
var ArgoPodWorkflows = &Family{
	Name:       "argoPodWorkflows",
	Projection: project("manages", true, "Managers", "Manages"),
	Sources:    []Source{{Kind: kindPod}},
	Targets:    []resourceid.GroupKind{kindWorkflow},
	Declared: func(obj runtime.Object, visit func(Declaration)) error {
		m, err := apimeta.Accessor(obj)
		if err != nil {
			return err
		}
		if name := m.GetLabels()["workflows.argoproj.io/workflow"]; name != "" {
			visit(Declaration{Target: resourceid.NewRef("argoproj.io", "Workflow", m.GetNamespace(), name), Path: "metadata.labels[workflows.argoproj.io/workflow]"})
		}
		return nil
	},
}
