package subject

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ControllerOwnerResolver adapts already-observed Kubernetes objects to the
// controller-only owner contracts used by subject resolution.
type ControllerOwnerResolver struct {
	Lookup func(Ref) (metav1.Object, bool)
	// IsNamespaced must resolve the exact API group and kind. OwnerReference
	// omits namespace, so namespaced children fail closed when scope is unknown.
	IsNamespaced func(group, kind string) (namespaced, known bool)
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
	group := resourceid.GroupFromAPIVersion(owner.APIVersion)
	namespaced, known := false, false
	if r.IsNamespaced != nil {
		namespaced, known = r.IsNamespaced(group, owner.Kind)
	}
	namespace := ""
	if child.Namespace == "" {
		// A cluster-scoped dependent can only have cluster-scoped owners; the
		// garbage collector rejects a namespaced one as unresolvable.
		if known && namespaced {
			return Ref{}, false
		}
	} else {
		if !known {
			return Ref{}, false
		}
		if namespaced {
			namespace = child.Namespace
		}
	}
	return Ref{
		Group:     group,
		Kind:      owner.Kind,
		Namespace: namespace,
		Name:      owner.Name,
	}, true
}

func (r ControllerOwnerResolver) ImmediateOwner(child Ref) (Ref, bool) {
	return r.ParentOf(child)
}
