package topology

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// ControllerTeardown is what a GitOps controller deletes when its object is
// deleted. It is controller policy carried out through a finalizer, not
// garbage collection: force deletion strips the finalizer, and with it the
// teardown.
type ControllerTeardown struct {
	Destination string `json:"destination"`
	Controller  string `json:"controller"` // "Argo CD" or "Flux"
	// Action is "prune" (managed resources are deleted) or "uninstall" (the
	// Helm release is uninstalled).
	Action string `json:"action"`
	// Resources are the managed resources Radar observes; the controller acts
	// on its own inventory, which can hold more, and skips resources that opt
	// out of deletion.
	Resources []ResourceRef    `json:"resources,omitempty"`
	Coverage  *MembersCoverage `json:"coverage,omitempty"`
	// Terminating means the object is already being deleted, so the
	// controller may already be tearing down and force delete can't stop it.
	Terminating bool `json:"terminating,omitempty"`
}

// The finalizers through which each controller tears down on deletion,
// matching gitops/insights' finalizer catalog: Argo CD's current key (with or
// without a /background or /foreground suffix) and its deprecated
// foreground-cascade key; Flux's shared key and its per-controller keys.
const argoResourcesFinalizer = "resources-finalizer.argocd.argoproj.io"

var fluxTeardownFinalizers = map[string]bool{
	"finalizers.fluxcd.io":                   true,
	"finalizers.kustomize.toolkit.fluxcd.io": true,
	"finalizers.helm.toolkit.fluxcd.io":      true,
}

func tearsDown(obj *unstructured.Unstructured, controller, action string) bool {
	switch controller {
	case "Argo CD":
		for _, f := range obj.GetFinalizers() {
			if f == argoResourcesFinalizer || strings.HasPrefix(f, argoResourcesFinalizer+"/") || f == "foreground-cascade.argocd.argoproj.io" {
				return true
			}
		}
		return false
	case "Flux":
		hasFinalizer := false
		for _, f := range obj.GetFinalizers() {
			hasFinalizer = hasFinalizer || fluxTeardownFinalizers[f]
		}
		// Both controllers skip teardown for a suspended object and only
		// release the finalizer.
		suspended, _, _ := unstructured.NestedBool(obj.Object, "spec", "suspend")
		if !hasFinalizer || suspended {
			return false
		}
		if action == "uninstall" {
			return true
		}
		policy, _, _ := unstructured.NestedString(obj.Object, "spec", "deletionPolicy")
		switch policy {
		case "Delete", "WaitForTermination":
			return true
		case "Orphan":
			return false
		default: // MirrorPrune, the default, follows spec.prune.
			prune, _, _ := unstructured.NestedBool(obj.Object, "spec", "prune")
			return prune
		}
	}
	return false
}
