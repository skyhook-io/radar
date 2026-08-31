package subject

import metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

// ControllerOwnerResolver adapts already-observed Kubernetes objects to the
// controller-only owner contracts used by subject resolution.
type ControllerOwnerResolver struct {
	Lookup func(Ref) (metav1.Object, bool)
}

var _ OwnerResolver = ControllerOwnerResolver{}
var _ OwnerLookup = ControllerOwnerResolver{}

func (r ControllerOwnerResolver) ParentOf(child Ref) (Ref, bool) {
	if r.Lookup == nil {
		return Ref{}, false
	}
	obj, ok := r.Lookup(child)
	if !ok || obj == nil {
		return Ref{}, false
	}
	owner := metav1.GetControllerOf(obj)
	if owner == nil || owner.APIVersion == "" || owner.Kind == "" || owner.Name == "" {
		return Ref{}, false
	}
	return Ref{
		Group:     groupFromAPIVersion(owner.APIVersion),
		Kind:      owner.Kind,
		Namespace: child.Namespace,
		Name:      owner.Name,
	}, true
}

func (r ControllerOwnerResolver) ImmediateOwner(child Ref) (Ref, bool) {
	return r.ParentOf(child)
}
