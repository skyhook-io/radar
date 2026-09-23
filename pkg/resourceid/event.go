package resourceid

// EventSubject is the Reference an Event's involvedObject makes to the object
// the Event is about, from the involvedObject fields and the namespace the
// Event itself is stored in.
//
// The Event's own namespace is not its subject's: client-go records events for
// cluster-scoped objects (Nodes, PersistentVolumes) in "default". The subject's
// namespace is involvedObject.namespace; the Event's namespace stands in only
// when involvedObject.namespace is empty and the reference proves a built-in
// namespaced Kind by its group.
//
// kubelet reports Node events with the node's name in place of its UID (and no
// apiVersion); that UID identifies no incarnation, so it is dropped.
func EventSubject(apiVersion, kind, namespace, name, uid, eventNamespace string) Reference {
	ref := ReferenceFromAPIVersion(apiVersion, kind, namespace, name)
	b, builtin := BuiltinForKind(kind)
	if ref.Namespace == "" && builtin && b.Namespaced && ref.HasGroup() && ref.Group == b.Group {
		ref.Namespace = eventNamespace
	}
	ref.UID = uid
	if kind == "Node" && uid == name && (!ref.HasGroup() || ref.Group == "") {
		ref.UID = ""
	}
	return ref
}
