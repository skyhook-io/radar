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
	Controller string `json:"controller"` // "Argo CD" or "Flux"
	// Action is "prune" (managed resources are deleted) or "uninstall" (the
	// Helm release is uninstalled).
	Action string `json:"action"`
	// Resources are the managed resources Radar observes; the controller acts
	// on its own inventory, which can hold more.
	Resources []ResourceRef `json:"resources,omitempty"`
}

const (
	argoResourcesFinalizer = "resources-finalizer.argocd.argoproj.io"
	fluxFinalizer          = "finalizers.fluxcd.io"
)

// controllerTeardown reports the teardown root's controller performs on
// deletion, or nil when it leaves its resources in place.
func controllerTeardown(root *Node, topo *Topology, dp DynamicProvider) *ControllerTeardown {
	if root == nil || dp == nil {
		return nil
	}
	group := nodeAPIGroupFromData(root)
	var controller, action string
	switch {
	case root.Kind == KindApplication && group == "argoproj.io":
		controller, action = "Argo CD", "prune"
	case root.Kind == KindKustomization && group == "kustomize.toolkit.fluxcd.io":
		controller, action = "Flux", "prune"
	case root.Kind == KindHelmRelease && group == "helm.toolkit.fluxcd.io":
		controller, action = "Flux", "uninstall"
	default:
		return nil
	}
	gvr, ok := dp.GetGVRWithGroup(string(root.Kind), group)
	if !ok {
		return nil
	}
	namespace, _ := root.Data["namespace"].(string)
	obj, err := dp.Get(gvr, namespace, root.Name)
	if err != nil || obj == nil || !tearsDown(obj, controller, action) {
		return nil
	}

	teardown := &ControllerTeardown{Controller: controller, Action: action}
	nodeByID := make(map[string]*Node, len(topo.Nodes))
	for i := range topo.Nodes {
		nodeByID[topo.Nodes[i].ID] = &topo.Nodes[i]
	}
	seen := map[string]bool{}
	for _, edge := range topo.Edges {
		if edge.Source != root.ID || edge.Type != EdgeManages || edge.verifiedOwner() || edge.SkipIfKindVisible != "" || seen[edge.Target] {
			continue
		}
		seen[edge.Target] = true
		if ref := resourceRefForNode(nodeByID[edge.Target], dp); ref != nil {
			teardown.Resources = append(teardown.Resources, *ref)
		}
	}
	return teardown
}

func tearsDown(obj *unstructured.Unstructured, controller, action string) bool {
	switch controller {
	case "Argo CD":
		// The finalizer, with or without its /background or /foreground
		// suffix, is what makes deleting an Application delete its resources.
		for _, f := range obj.GetFinalizers() {
			if f == argoResourcesFinalizer || strings.HasPrefix(f, argoResourcesFinalizer+"/") {
				return true
			}
		}
		return false
	case "Flux":
		hasFinalizer := false
		for _, f := range obj.GetFinalizers() {
			hasFinalizer = hasFinalizer || f == fluxFinalizer
		}
		if !hasFinalizer {
			return false
		}
		if action == "uninstall" {
			// Deleting a HelmRelease uninstalls its release, suspended or not.
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
