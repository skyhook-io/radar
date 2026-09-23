// Package resourceid is Radar's model of Kubernetes resource identity: what
// makes two references name the same object, which facts identify a built-in
// resource, and how a reference that is missing part of its identity gets
// resolved (or refused).
//
// It is a leaf package (stdlib only, no internal/ or other pkg/ imports), so
// every identity consumer — topology, subject, issues, audit, applications —
// can depend on it without depending on each other.
//
// The model, in brief:
//
//   - An object is identified by API group + Kind + namespace + name (Ref).
//     Version is not identity: one object is served at several versions, and a
//     CRD version bump changes apiVersion without changing the object. Within
//     a cluster the Ref is unique; across clusters the caller adds the cluster.
//   - An incarnation is a Ref plus the object's UID. Deleting and recreating an
//     object keeps its Ref and changes its UID.
//   - An object's identity is never partial, but a reference to it can be: an
//     event without apiVersion, a user-typed "jobs/train", a persisted pin. A
//     Reference records what is known and where the group came from.
//   - Resolving a partial Reference is policy, not parsing. ResolveCurrent
//     serves live joins and user commands and may infer from current state;
//     ResolveHistorical serves stored observations and never guesses.
package resourceid

import "fmt"

// ResourceKey returns the index key for a resource:
// "group|Kind|namespace|name". Group goes first because both group and
// namespace can legitimately be empty independently — encoding group last would
// leave a cluster-scoped CRD key ambiguous with a namespaced core-group key
// under any 3-part parse. "|" is a safe delimiter — Kubernetes API groups follow
// DNS subdomain rules and can't contain it.
func ResourceKey(group, kind, namespace, name string) string {
	return fmt.Sprintf("%s|%s|%s|%s", group, kind, namespace, name)
}
