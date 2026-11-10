package k8score

import (
	"strings"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/cache"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// GitOpsMembersIndex indexes Argo CD Applications and Flux Kustomizations and
// HelmReleases by the objects their status lists, so the managers recording
// one object are found without reading every manager's list. A manager whose
// list can't be read is filed under UnreadableMembersKey instead.
const GitOpsMembersIndex = "gitopsMembers"

// UnreadableMembersKey is the GitOpsMembersIndex key of the managers whose
// status lists no members Radar can trust, any of which may name any object.
// A manager that applies to another cluster is filed under no key when its
// list can't be read: it can name no object here.
const UnreadableMembersKey = "\x00unreadable"

// MemberPlacement is one way to place a status entry: the namespace its object
// is taken to be in. A built-in kind's scope is fixed, so the index files its
// entries by the object they mean. For any other kind the index can't ask
// discovery the scope — an index function runs when an object changes, not
// when discovery does — so it files an entry under each placement the kind's
// scope could call for, and a reader looks up only the placements the scope
// discovery serves the kind with calls for.
type MemberPlacement byte

const (
	// MemberMeant: the object an entry of a built-in kind names.
	MemberMeant MemberPlacement = 'm'
	// MemberAsRecorded: the namespace the entry records.
	MemberAsRecorded MemberPlacement = 'r'
	// MemberClusterScoped: an entry recording a namespace, read for a
	// cluster-scoped kind, which ignores it.
	MemberClusterScoped MemberPlacement = 'c'
	// MemberInDestination: an Argo CD entry recording no namespace, read for
	// a namespaced kind, which is then in the Application's destination
	// namespace.
	MemberInDestination MemberPlacement = 'd'
)

// MemberKey is the GitOpsMembersIndex key of an entry naming group, kind,
// namespace and name, read r's way.
func MemberKey(r MemberPlacement, group, kind, namespace, name string) string {
	return strings.Join([]string{string(r), group, kind, namespace, name}, "\x00")
}

// gitopsMembersIndexers are the GitOps managers' members indexes, by resource,
// whatever version it is watched at.
var gitopsMembersIndexers = map[schema.GroupResource]cache.Indexers{
	{Group: "argoproj.io", Resource: "applications"}:                   {GitOpsMembersIndex: memberKeys(gitops.ArgoMembers, gitops.ArgoAppliesElsewhere)},
	{Group: "helm.toolkit.fluxcd.io", Resource: "helmreleases"}:        {GitOpsMembersIndex: memberKeys(gitops.FluxMembers, fluxAppliesElsewhere)},
	{Group: "kustomize.toolkit.fluxcd.io", Resource: "kustomizations"}: {GitOpsMembersIndex: memberKeys(gitops.FluxMembers, fluxAppliesElsewhere)},
}

func fluxAppliesElsewhere(obj *unstructured.Unstructured) bool {
	return !gitops.FluxTargetsLocalCluster(obj)
}

// memberKeys files a manager under each entry of its list, in every placement
// the entry's kind's scope could call for.
func memberKeys(members func(*unstructured.Unstructured) (gitops.MemberList, bool), elsewhere func(*unstructured.Unstructured) bool) cache.IndexFunc {
	return func(obj any) ([]string, error) {
		if q, isQuery := obj.(indexQuery); isQuery {
			return q, nil
		}
		u, ok := obj.(*unstructured.Unstructured)
		if !ok {
			return nil, nil
		}
		list, readable := members(u)
		switch {
		case !readable && elsewhere(u):
			return nil, nil
		case !readable:
			return []string{UnreadableMembersKey}, nil
		}
		keys := make([]string, 0, len(list.Entries))
		for _, e := range list.Entries {
			if b, builtin := resourceid.BuiltinForKind(e.Kind); builtin && b.Group == e.Group {
				keys = append(keys, MemberKey(MemberMeant, e.Group, e.Kind, gitops.MemberNamespace(b.Namespaced, e.Namespace, list.DestinationNamespace), e.Name))
				continue
			}
			keys = append(keys, MemberKey(MemberAsRecorded, e.Group, e.Kind, e.Namespace, e.Name))
			switch {
			case e.Namespace != "":
				keys = append(keys, MemberKey(MemberClusterScoped, e.Group, e.Kind, "", e.Name))
			case list.DestinationNamespace != "":
				keys = append(keys, MemberKey(MemberInDestination, e.Group, e.Kind, list.DestinationNamespace, e.Name))
			}
		}
		return keys, nil
	}
}
