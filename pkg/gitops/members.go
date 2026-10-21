package gitops

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// Member is one object a GitOps manager's status lists, as recorded.
type Member struct {
	Group, Kind, Namespace, Name string
}

// MemberList is what a GitOps manager's status lists as the objects it
// applies.
type MemberList struct {
	Entries []Member
	// DestinationNamespace is an Argo CD Application's destination namespace.
	DestinationNamespace string
}

// ArgoMembers reads an Application's status.resources, as of the destination
// Argo CD compared it against. ok is false when the list can't be trusted, so
// that any object may be a member: the Application has no destination, Argo CD
// hasn't reconciled it, or an entry names no object.
func ArgoMembers(app *unstructured.Unstructured) (MemberList, bool) {
	return argoMembers(app, true)
}

// ArgoMembersReadable reports whether ArgoMembers reads app's list, making the
// same checks without building the list, whose cost grows with it.
func ArgoMembersReadable(app *unstructured.Unstructured) bool {
	_, ok := argoMembers(app, false)
	return ok
}

func argoMembers(app *unstructured.Unstructured, collect bool) (list MemberList, ok bool) {
	name, server, destination := ArgoComparedDestination(app)
	if name == "" && server == "" {
		return MemberList{}, false
	}
	list.DestinationNamespace = destination
	raw, _, err := unstructured.NestedFieldNoCopy(app.Object, "status", "resources")
	if err != nil {
		return MemberList{}, false
	}
	if raw == nil {
		// Argo CD omits an empty list, so once it has reconciled the
		// Application, no list is an empty one.
		if reconciled, _, _ := unstructured.NestedString(app.Object, "status", "reconciledAt"); reconciled == "" {
			return MemberList{}, false
		}
		return list, true
	}
	items, isList := raw.([]any)
	if !isList {
		return MemberList{}, false
	}
	if collect && len(items) > 0 {
		list.Entries = make([]Member, 0, len(items))
	}
	for _, item := range items {
		e, isMap := item.(map[string]any)
		if !isMap {
			return MemberList{}, false
		}
		kind, kindOK := e["kind"].(string)
		name, nameOK := e["name"].(string)
		group, groupOK := optionalString(e["group"])
		namespace, namespaceOK := optionalString(e["namespace"])
		if !kindOK || !nameOK || kind == "" || name == "" || !groupOK || !namespaceOK {
			return MemberList{}, false
		}
		if collect {
			list.Entries = append(list.Entries, Member{Group: group, Kind: kind, Namespace: namespace, Name: name})
		}
	}
	return list, true
}

// FluxMembers reads a Kustomization's or HelmRelease's status.inventory. ok is
// false when there is no inventory, an entry's ID doesn't parse, or the
// controller hasn't observed the current spec: Flux records no destination
// with its inventory, so one written before a spec.kubeConfig change may list
// another cluster's objects. That holds until the controller reconciles the
// change, and while kustomize-controller's reconciles of it fail. A
// controller can also record a generation as observed without applying it:
// on suspension (helm-controller always, kustomize-controller while the
// object is Ready), after a failure it won't retry, and while a HelmRelease
// waits out a failed release's retry interval. A destination changed in such
// a generation isn't noticed until a reconcile applies it.
func FluxMembers(obj *unstructured.Unstructured) (MemberList, bool) {
	return fluxMembers(obj, true)
}

// FluxMembersReadable reports whether FluxMembers reads obj's list, making the
// same checks without building the list, whose cost grows with it.
func FluxMembersReadable(obj *unstructured.Unstructured) bool {
	_, ok := fluxMembers(obj, false)
	return ok
}

func fluxMembers(obj *unstructured.Unstructured, collect bool) (list MemberList, ok bool) {
	if observed, found, err := unstructured.NestedInt64(obj.Object, "status", "observedGeneration"); err == nil && found && observed < obj.GetGeneration() {
		return MemberList{}, false
	}
	raw, _, err := unstructured.NestedFieldNoCopy(obj.Object, "status", "inventory")
	inventory, isMap := raw.(map[string]any)
	if err != nil || !isMap {
		return MemberList{}, false
	}
	items, isList := inventory["entries"].([]any)
	if !isList && inventory["entries"] != nil {
		return MemberList{}, false
	}
	if collect && len(items) > 0 {
		list.Entries = make([]Member, 0, len(items))
	}
	for _, item := range items {
		e, isMap := item.(map[string]any)
		if !isMap {
			return MemberList{}, false
		}
		id, isString := e["id"].(string)
		if !isString {
			return MemberList{}, false
		}
		if !collect {
			if _, _, _, _, parsed := fluxInventoryParts(id); !parsed {
				return MemberList{}, false
			}
			continue
		}
		group, kind, namespace, name, parsed := ParseFluxInventoryID(id)
		if !parsed {
			return MemberList{}, false
		}
		list.Entries = append(list.Entries, Member{Group: group, Kind: kind, Namespace: namespace, Name: name})
	}
	return list, true
}

// MemberNamespace is the namespace of the object an entry recording namespace
// means, for a kind of known scope: none for a cluster-scoped kind, and the
// destination for a namespaced one recorded without a namespace.
func MemberNamespace(namespaced bool, namespace, destination string) string {
	switch {
	case !namespaced:
		return ""
	case namespace == "":
		return destination
	}
	return namespace
}

// optionalString reads a field that is either absent or a string.
func optionalString(v any) (string, bool) {
	if v == nil {
		return "", true
	}
	s, ok := v.(string)
	return s, ok
}
