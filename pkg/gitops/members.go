package gitops

import "k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

// Member is one object a GitOps manager's status lists, as recorded.
type Member struct {
	Group, Kind, Namespace, Name string
}

// MemberList is what a GitOps manager's status records: for an Argo CD
// Application, the objects its last comparison covered, including desired
// objects not yet created, live ones awaiting pruning and ones another
// Application also tracks; for Flux, the inventory of its last apply. An
// entry says the manager recorded the object, not that it applied it or
// alone manages it.
type MemberList struct {
	Entries []Member
	// DestinationNamespace is the namespace of the destination an Argo CD
	// Application's list was compared against, where an entry recording no
	// namespace is; empty for Flux.
	DestinationNamespace string
}

// ArgoMembers reads an Application's status.resources, as of the destination
// Argo CD compared it against. ok is false when the list can't be trusted, so
// that any object may be a member: the Application has no destination, the
// list is left out before Argo CD has reconciled it (reconciledAt unset), the
// list is empty or left out after a failed comparison, or an entry names no
// object. A nonempty list from a failed comparison is still what Argo CD
// recorded, though it may leave objects out.
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
		if argoComparisonFailed(app) {
			return MemberList{}, false
		}
		return list, true
	}
	items, isList := raw.([]any)
	if !isList {
		return MemberList{}, false
	}
	if len(items) == 0 && argoComparisonFailed(app) {
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
// false when there is no inventory or an entry's ID doesn't parse. Flux
// records no destination with its inventory, so after a spec.kubeConfig
// change the inventory lists the previous destination's objects until a
// reconcile applies the change.
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

// argoComparisonFailed reports whether Argo CD's last comparison failed. A
// failed comparison still sets reconciledAt and can leave an empty, partial
// or previous resource list. An empty one then says nothing about what the
// Application covers; a nonempty one is still what it recorded, though it
// may leave objects out.
func argoComparisonFailed(app *unstructured.Unstructured) bool {
	if status, _, _ := unstructured.NestedString(app.Object, "status", "sync", "status"); status == "Unknown" {
		return true
	}
	conditions, _, _ := unstructured.NestedSlice(app.Object, "status", "conditions")
	for _, c := range conditions {
		if m, ok := c.(map[string]any); ok && m["type"] == "ComparisonError" {
			return true
		}
	}
	return false
}
