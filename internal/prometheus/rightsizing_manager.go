package prometheus

import (
	"fmt"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/subject"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
)

// WorkloadManager names what owns a workload's spec, so a request change is
// made at that source rather than patched live where it would be overwritten.
// Signal says which metadata produced the answer: labels get copied between
// templates, and a reader weighing a surprising owner needs to know why.
type WorkloadManager struct {
	Tool      string `json:"tool"`
	Kind      string `json:"kind,omitempty"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name,omitempty"`
	Signal    string `json:"signal"`
}

const addonManagerModeLabel = "addonmanager.kubernetes.io/mode"

// detectWorkloadManager returns nil when no signal is present, which is not
// evidence the workload is unmanaged: Argo CD's default label tracking,
// Terraform and kubectl apply leave nothing distinguishable to read.
func detectWorkloadManager(obj metav1.Object) *WorkloadManager {
	if obj == nil {
		return nil
	}
	// An operator reconciles its workloads continuously, so it outranks any
	// GitOps labels the operator's own manifest happened to copy down.
	if owner := metav1.GetControllerOfNoCopy(obj); owner != nil {
		group := ""
		if gv, err := schema.ParseGroupVersion(owner.APIVersion); err == nil {
			group = gv.Group
		}
		// A namespaced object may be owned by a cluster-scoped resource, such
		// as a GPU operator's ClusterPolicy; naming a namespace would point at
		// an object that does not exist. Unknown scope keeps the namespace,
		// which is the only one a namespaced owner can have.
		namespace := obj.GetNamespace()
		if clusterScoped, _, _ := k8s.ClassifyKindScope(owner.Kind, group); clusterScoped {
			namespace = ""
		}
		return &WorkloadManager{Tool: "controller", Kind: owner.Kind, Group: group, Namespace: namespace, Name: owner.Name, Signal: "controller ownerReference"}
	}
	if overlay := subject.ResolveOverlay(obj, false); overlay != nil && overlay.Winner.Tier <= subject.TierHelmRelease {
		winner := overlay.Winner
		tool, signal := managerToolForTier(winner.Tier)
		return &WorkloadManager{Tool: tool, Kind: winner.Ref.Kind, Group: winner.Ref.Group, Namespace: winner.Ref.Namespace, Name: winner.Ref.Name, Signal: signal}
	}
	// EnsureExists only recreates a deleted object; edits survive it.
	if mode := obj.GetLabels()[addonManagerModeLabel]; mode == "Reconcile" {
		return &WorkloadManager{Tool: "addon-manager", Signal: fmt.Sprintf("label %s=%s", addonManagerModeLabel, mode)}
	}
	return nil
}

func managerToolForTier(tier subject.Tier) (tool, signal string) {
	switch tier {
	case subject.TierFluxHelmRelease:
		return "flux", "labels helm.toolkit.fluxcd.io/name and namespace"
	case subject.TierFluxKustomize:
		return "flux", "labels kustomize.toolkit.fluxcd.io/name and namespace"
	case subject.TierArgoTrackingID:
		return "argocd", "annotation argocd.argoproj.io/tracking-id"
	case subject.TierArgoInstance:
		return "argocd", "label argocd.argoproj.io/instance"
	default:
		return "helm", "annotation meta.helm.sh/release-name"
	}
}
