package ownershiplive

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// scopeOf says whether a kind is namespaced, and whether the check can tell.
type scopeOf func(resourceid.GroupKind) (namespaced, known bool)

// discoveredScope is the scope of each kind as served: a built-in kind's
// own, unless discovery serves it with the other scope; a kind two resources
// serve has none.
func discoveredScope(served []ownership.ServedResource) scopeOf {
	scopes := map[resourceid.GroupKind]bool{}
	resource := map[resourceid.GroupKind]string{}
	ambiguous := map[resourceid.GroupKind]bool{}
	for _, r := range served {
		gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
		if seen, ok := resource[gk]; ok && seen != r.Resource {
			ambiguous[gk] = true
		}
		resource[gk], scopes[gk] = r.Resource, r.Namespaced
	}
	return func(gk resourceid.GroupKind) (bool, bool) {
		if ambiguous[gk] {
			return false, false
		}
		namespaced, known := scopes[gk]
		if b, ok := resourceid.BuiltinForKind(gk.Kind); ok && b.Group == gk.Group {
			return b.Namespaced, !known || namespaced == b.Namespaced
		}
		return namespaced, known
	}
}

// liveMembership is this check's own reading of what a GitOps manager's status
// says of its members, written apart from the source's so the two can't share
// a mistake: Argo CD's status.resources and Flux's status.inventory, each
// entry aligned with the scope discovery states for its kind.
func liveMembership(gk resourceid.GroupKind, u *unstructured.Unstructured, scope scopeOf) (ownership.MembershipState, []resourceid.Ref, string) {
	spec, _ := u.Object["spec"].(map[string]any)
	status, _ := u.Object["status"].(map[string]any)
	switch gk {
	case ownership.ArgoApplication:
		return argoMembership(spec, status, scope)
	case ownership.FluxHelmRelease, ownership.FluxKustomization:
		return fluxMembership(spec, status, scope)
	}
	return ownership.MembersUnreadable, nil, ""
}

// inClusterServer reports whether an Argo CD destination server is the
// in-cluster API address, in any of the forms Argo CD accepts: with or
// without a scheme, port or trailing dot, short or fully qualified.
func inClusterServer(server string) bool {
	host := strings.ToLower(strings.TrimSpace(server))
	if _, rest, ok := strings.Cut(host, "://"); ok {
		host = rest
	}
	host, _, _ = strings.Cut(host, "/")
	host, _, _ = strings.Cut(host, "?")
	host, _, _ = strings.Cut(host, "#")
	if i := strings.LastIndex(host, ":"); i >= 0 {
		host = host[:i]
	}
	host = strings.TrimSuffix(host, ".")
	switch host {
	case "kubernetes", "kubernetes.default", "kubernetes.default.svc":
		return true
	}
	return strings.HasPrefix(host, "kubernetes.default.svc.")
}

func argoMembership(spec, status map[string]any, scope scopeOf) (ownership.MembershipState, []resourceid.Ref, string) {
	// status.resources describes the destination Argo CD last compared, which
	// lags a changed spec until it compares again.
	sync, _ := status["sync"].(map[string]any)
	compared, _ := sync["comparedTo"].(map[string]any)
	destination, _ := compared["destination"].(map[string]any)
	server, _ := destination["server"].(string)
	name, _ := destination["name"].(string)
	if strings.TrimSpace(server) == "" && strings.TrimSpace(name) == "" {
		destination, _ = spec["destination"].(map[string]any)
		server, _ = destination["server"].(string)
		name, _ = destination["name"].(string)
	}
	namespace, _ := destination["namespace"].(string)
	server, name = strings.TrimSpace(server), strings.TrimSpace(name)
	if server == "" && name == "" {
		return ownership.MembersUnreadable, nil, namespace
	}
	state := ownership.MembersRemote
	if strings.EqualFold(name, "in-cluster") || inClusterServer(server) {
		state = ownership.MembersRecorded
	}
	raw := status["resources"]
	list, ok := raw.([]any)
	if raw != nil && !ok {
		return ownership.MembersUnreadable, nil, namespace
	}
	if len(list) == 0 {
		// After a failed comparison Argo CD records no resources and still
		// sets reconciledAt.
		failed := sync["status"] == "Unknown"
		conditions, _ := status["conditions"].([]any)
		for _, c := range conditions {
			if m, _ := c.(map[string]any); m["type"] == "ComparisonError" {
				failed = true
			}
		}
		if reconciled, _ := status["reconciledAt"].(string); reconciled == "" || failed {
			return ownership.MembersUnreadable, nil, namespace
		}
		if raw == nil {
			return state, []resourceid.Ref{}, namespace
		}
	}
	members := []resourceid.Ref{}
	for _, item := range list {
		e, _ := item.(map[string]any)
		ref := resourceid.Ref{}
		var kindOK, nameOK bool
		ref.Kind, kindOK = e["kind"].(string)
		ref.Name, nameOK = e["name"].(string)
		group, groupOK := e["group"].(string)
		ns, nsOK := e["namespace"].(string)
		if !kindOK || !nameOK || ref.Kind == "" || ref.Name == "" || e["group"] != nil && !groupOK || e["namespace"] != nil && !nsOK {
			return ownership.MembersUnreadable, nil, namespace
		}
		ref.Group, ref.Namespace = group, ns
		if namespaced, known := scope(ref.GroupKind()); known && namespaced && ref.Namespace == "" {
			ref.Namespace = namespace
		}
		members = append(members, aligned(ref, scope))
	}
	return state, sortedRefs(members), namespace
}

func fluxMembership(spec, status map[string]any, scope scopeOf) (ownership.MembershipState, []resourceid.Ref, string) {
	state := ownership.MembersRecorded
	if _, elsewhere := spec["kubeConfig"].(map[string]any); elsewhere {
		state = ownership.MembersRemote
	}
	inventory, ok := status["inventory"].(map[string]any)
	if !ok {
		return ownership.MembersUnreadable, nil, ""
	}
	members := []resourceid.Ref{}
	if inventory["entries"] == nil {
		return state, members, ""
	}
	list, ok := inventory["entries"].([]any)
	if !ok {
		return ownership.MembersUnreadable, nil, ""
	}
	for _, item := range list {
		e, _ := item.(map[string]any)
		id, _ := e["id"].(string)
		// namespace_name_group_kind; only a name can hold an underscore, and
		// Flux writes a colon in a name as two.
		parts := strings.Split(id, "_")
		n := len(parts)
		if n < 4 || parts[n-1] == "" || strings.Join(parts[1:n-2], "_") == "" {
			return ownership.MembersUnreadable, nil, ""
		}
		group := parts[n-2]
		if group == "core" {
			group = ""
		}
		name := strings.ReplaceAll(strings.Join(parts[1:n-2], "_"), "__", ":")
		ref := resourceid.Ref{Group: group, Kind: parts[n-1], Namespace: parts[0], Name: name}
		members = append(members, aligned(ref, scope))
	}
	return state, sortedRefs(members), ""
}

// aligned drops the namespace an entry records for a cluster-scoped kind.
func aligned(ref resourceid.Ref, scope scopeOf) resourceid.Ref {
	if namespaced, known := scope(ref.GroupKind()); known && !namespaced {
		ref.Namespace = ""
	}
	return ref
}

func sortedRefs(refs []resourceid.Ref) []resourceid.Ref {
	slices.SortFunc(refs, func(a, b resourceid.Ref) int { return strings.Compare(refKey(a), refKey(b)) })
	return slices.Compact(refs)
}

func refKey(r resourceid.Ref) string {
	return r.Group + "\x00" + r.Kind + "\x00" + r.Namespace + "\x00" + r.Name
}
