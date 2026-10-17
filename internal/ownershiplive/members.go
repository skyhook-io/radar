package ownershiplive

import (
	"slices"
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// scopeOf says whether discovery serves a kind as namespaced, and whether it
// states the kind's scope at all.
type scopeOf func(resourceid.GroupKind) (namespaced, known bool)

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

func argoMembership(spec, status map[string]any, scope scopeOf) (ownership.MembershipState, []resourceid.Ref, string) {
	destination, _ := spec["destination"].(map[string]any)
	server, _ := destination["server"].(string)
	name, _ := destination["name"].(string)
	namespace, _ := destination["namespace"].(string)
	switch {
	case server == "" && name == "":
		return ownership.MembersUnreadable, nil, namespace
	case name != "in-cluster" && server != "https://kubernetes.default.svc":
		return ownership.MembersRemote, nil, namespace
	}
	raw := status["resources"]
	if raw == nil {
		if reconciled, _ := status["reconciledAt"].(string); reconciled != "" {
			return ownership.MembersRecorded, []resourceid.Ref{}, namespace
		}
		return ownership.MembersUnreadable, nil, namespace
	}
	list, ok := raw.([]any)
	if !ok {
		return ownership.MembersUnreadable, nil, namespace
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
	return ownership.MembersRecorded, sortedRefs(members), namespace
}

func fluxMembership(spec, status map[string]any, scope scopeOf) (ownership.MembershipState, []resourceid.Ref, string) {
	if _, elsewhere := spec["kubeConfig"].(map[string]any); elsewhere {
		return ownership.MembersRemote, nil, ""
	}
	inventory, ok := status["inventory"].(map[string]any)
	if !ok {
		return ownership.MembersUnreadable, nil, ""
	}
	members := []resourceid.Ref{}
	if inventory["entries"] == nil {
		return ownership.MembersRecorded, members, ""
	}
	list, ok := inventory["entries"].([]any)
	if !ok {
		return ownership.MembersUnreadable, nil, ""
	}
	for _, item := range list {
		e, _ := item.(map[string]any)
		id, _ := e["id"].(string)
		// namespace_name_group_kind; only a name can hold an underscore.
		parts := strings.Split(id, "_")
		n := len(parts)
		if n < 4 || parts[n-1] == "" || strings.Join(parts[1:n-2], "_") == "" {
			return ownership.MembersUnreadable, nil, ""
		}
		ref := resourceid.Ref{Group: parts[n-2], Kind: parts[n-1], Namespace: parts[0], Name: strings.Join(parts[1:n-2], "_")}
		members = append(members, aligned(ref, scope))
	}
	return ownership.MembersRecorded, sortedRefs(members), ""
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
