package cachesource

import (
	"slices"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// recordedMembers is what a manager's status says about its members.
type recordedMembers struct {
	state ownership.MembershipState
	gitops.MemberList
}

func membersOf(gk resourceid.GroupKind, u *unstructured.Unstructured) recordedMembers {
	var list gitops.MemberList
	var ok bool
	switch gk {
	case ownership.ArgoApplication:
		list, ok = gitops.ArgoMembers(u)
	case ownership.FluxHelmRelease, ownership.FluxKustomization:
		list, ok = gitops.FluxMembers(u)
	}
	switch {
	case !ok && appliesElsewhere(gk, u):
		return recordedMembers{state: ownership.MembersRemote}
	case !ok:
		return recordedMembers{}
	}
	return recordedMembers{state: located(gk, u), MemberList: list}
}

// memberState is membersOf's state alone. A search reads it for every manager
// whose list names the object; building that list would cost as much as the
// list is long.
func memberState(gk resourceid.GroupKind, u *unstructured.Unstructured) ownership.MembershipState {
	var ok bool
	switch gk {
	case ownership.ArgoApplication:
		ok = gitops.ArgoMembersReadable(u)
	case ownership.FluxHelmRelease, ownership.FluxKustomization:
		ok = gitops.FluxMembersReadable(u)
	}
	switch {
	case !ok && appliesElsewhere(gk, u):
		return ownership.MembersRemote
	case !ok:
		return ownership.MembersUnreadable
	}
	return located(gk, u)
}

// appliesElsewhere reports whether a manager is known to apply to another
// cluster, whose objects alone its list can name, whether Radar can read the
// list or not.
func appliesElsewhere(gk resourceid.GroupKind, u *unstructured.Unstructured) bool {
	if gk == ownership.ArgoApplication {
		return gitops.ArgoAppliesElsewhere(u)
	}
	return !gitops.FluxTargetsLocalCluster(u)
}

// located is whether a manager whose list Radar reads applies it here.
func located(gk resourceid.GroupKind, u *unstructured.Unstructured) ownership.MembershipState {
	if appliesHere(gk, u) {
		return ownership.MembersRecorded
	}
	return ownership.MembersRemote
}

// appliesHere reports whether a manager's member list names objects of this
// cluster: an Argo CD list compared at the in-cluster destination, or a Flux
// object without spec.kubeConfig.
func appliesHere(gk resourceid.GroupKind, u *unstructured.Unstructured) bool {
	if gk == ownership.ArgoApplication {
		name, server, _ := gitops.ArgoComparedDestination(u)
		return gitops.IsInClusterArgoDestination(name, server)
	}
	return gitops.FluxTargetsLocalCluster(u)
}

func (s *session) Members(manager resourceid.Ref) ownership.MembersRead {
	gk := manager.GroupKind()
	if !ownership.IsRecordingManager(gk) {
		return ownership.MembersRead{Manager: unavailable(manager, readreason.ReadError)}
	}
	var members recordedMembers
	read := s.readObject(manager, func(u *unstructured.Unstructured) { members = membersOf(gk, u) })
	r := ownership.MembersRead{Manager: read}
	if read.State != ownership.Present {
		return r
	}
	r.Membership = members.state
	if members.state == ownership.MembersUnreadable {
		return r
	}
	r.Members = make([]resourceid.Ref, 0, len(members.Entries))
	for _, e := range members.Entries {
		r.Members = append(r.Members, s.align(e, members.DestinationNamespace))
	}
	slices.SortFunc(r.Members, compareRefs)
	r.Members = slices.Compact(r.Members)
	return r
}

func (s *session) Membership(manager resourceid.Ref) ownership.MembersRead {
	gk := manager.GroupKind()
	if !ownership.IsRecordingManager(gk) {
		return ownership.MembersRead{Manager: unavailable(manager, readreason.ReadError)}
	}
	var state ownership.MembershipState
	read := s.readObject(manager, func(u *unstructured.Unstructured) { state = memberState(gk, u) })
	r := ownership.MembersRead{Manager: read}
	if read.State == ownership.Present {
		r.Membership = state
	}
	return r
}

// builtinKind returns the built-in kind group/kind names, if it is one.
func builtinKind(group, kind string) (resourceid.Builtin, bool) {
	b, ok := resourceid.BuiltinForKind(kind)
	return b, ok && b.Group == group
}

// scopeOf says whether gk is namespaced, and whether Radar can tell: a
// built-in kind's scope is its own unless discovery is ambiguous about the
// kind or serves it with the other scope; any other kind's is the catalog's.
func (s *session) scopeOf(gk resourceid.GroupKind) (namespaced, known bool) {
	if s.catalog.Ambiguous(gk) {
		return false, false
	}
	k, served := s.catalog.Kind(gk)
	if b, builtin := builtinKind(gk.Group, gk.Kind); builtin {
		return b.Namespaced, !served || k.Namespaced == b.Namespaced
	}
	return k.Namespaced, served
}

// align reads an entry as its kind's scope says, or as recorded when Radar
// can't tell the scope.
func (s *session) align(e gitops.Member, destination string) resourceid.Ref {
	ref := resourceid.Ref{Group: e.Group, Kind: e.Kind, Namespace: e.Namespace, Name: e.Name}
	if namespaced, known := s.scopeOf(ref.GroupKind()); known {
		ref.Namespace = gitops.MemberNamespace(namespaced, e.Namespace, destination)
	}
	return ref
}

// placements returns the GitOpsMembersIndex keys under which an entry naming ref
// is filed, as its kind's scope says, or why no entry can name ref.
func (s *session) placements(ref resourceid.Ref) ([]string, readreason.Code) {
	if ref.Kind == "" || ref.Name == "" {
		return nil, readreason.ReadError
	}
	gk := ref.GroupKind()
	namespaced, known := s.scopeOf(gk)
	_, builtin := builtinKind(ref.Group, ref.Kind)
	switch {
	case !known && (builtin || s.catalog.Ambiguous(gk) || !s.catalog.Complete()):
		// Radar can't tell the kind's scope.
		return nil, readreason.ReadError
	case !known:
		// No object of the kind can exist; entries name it as recorded.
		return []string{k8score.MemberKey(k8score.MemberAsRecorded, ref.Group, ref.Kind, ref.Namespace, ref.Name)}, ""
	case namespaced && ref.Namespace == "":
		return nil, readreason.NamespaceRequired
	case !namespaced && ref.Namespace != "":
		return nil, readreason.ReadError
	case builtin:
		return []string{k8score.MemberKey(k8score.MemberMeant, ref.Group, ref.Kind, ref.Namespace, ref.Name)}, ""
	case namespaced:
		return []string{k8score.MemberKey(k8score.MemberAsRecorded, ref.Group, ref.Kind, ref.Namespace, ref.Name), k8score.MemberKey(k8score.MemberInDestination, ref.Group, ref.Kind, ref.Namespace, ref.Name)}, ""
	}
	return []string{k8score.MemberKey(k8score.MemberAsRecorded, ref.Group, ref.Kind, "", ref.Name), k8score.MemberKey(k8score.MemberClusterScoped, ref.Group, ref.Kind, "", ref.Name)}, ""
}

func (s *session) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	keys, reason := s.placements(ref)
	if reason != "" {
		var r ownership.RecordingManagersRead
		for _, gk := range ownership.RecordingManagerKinds() {
			r.Gaps = append(r.Gaps, ownership.Gap{Kind: gk, Reason: reason})
		}
		slices.SortFunc(r.Gaps, compareGaps)
		return r
	}
	q := &search{scope: ownership.Everywhere()}
	var managers []candidate
	var remote, unreadable []resourceid.Ref
	for _, gk := range ownership.RecordingManagerKinds() {
		kind, served := s.catalog.Kind(gk)
		if !served {
			// Not installed, unless discovery missed it.
			if !s.catalog.Complete() || s.catalog.Ambiguous(gk) {
				q.gap(gk, "", readreason.ReadError)
			}
			continue
		}
		m := s.readMembers(kind, keys)
		managers = append(managers, m.managers...)
		remote = append(remote, m.remote...)
		unreadable = append(unreadable, m.unreadable...)
		q.gaps = append(q.gaps, m.gaps...)
	}
	var r ownership.RecordingManagersRead
	slices.SortFunc(managers, func(a, b candidate) int { return compareRefs(a.ref, b.ref) })
	managers = slices.CompactFunc(managers, func(a, b candidate) bool { return a.ref == b.ref })
	for _, c := range managers {
		if read := observe(c.ref, c.obj); read.State == ownership.Present {
			r.Managers = append(r.Managers, read.Object)
		} else {
			q.gaps = append(q.gaps, read.Gap)
		}
	}
	slices.SortFunc(remote, compareRefs)
	r.Remote = slices.Compact(remote)
	slices.SortFunc(unreadable, compareRefs)
	r.Unreadable = slices.Compact(unreadable)
	slices.SortFunc(q.gaps, compareGaps)
	r.Gaps = slices.Compact(q.gaps)
	return r
}

// membersRead is what one manager kind's index reads found.
type membersRead struct {
	managers           []candidate
	remote, unreadable []resourceid.Ref
	gaps               []ownership.Gap
}

// readMembers reads the managers of kind filed under the unreadable key or any
// of keys, each store of kind once for all of them. A manager that changes
// while RecordingManagers runs, from an unreadable list to one naming the object or
// from one placement of its entry to another, is then found in one of its
// states: reads of one key at a time could find it in none.
func (s *session) readMembers(kind ownership.CatalogKind, keys []string) membersRead {
	gk := kind.GroupKind
	var m membersRead
	q := &search{scope: ownership.Everywhere()}
	keys = append([]string{k8score.UnreadableMembersKey}, keys...)
	s.dynamicIndexed(q, kind, k8score.GitOpsMembersIndex, keys, func(u *unstructured.Unstructured) {
		manager := resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: u.GetNamespace(), Name: u.GetName()}
		switch memberState(gk, u) {
		case ownership.MembersUnreadable:
			m.unreadable = append(m.unreadable, manager)
		case ownership.MembersRemote:
			m.remote = append(m.remote, manager)
		default:
			m.managers = append(m.managers, candidate{ref: manager, obj: u})
		}
	})
	if s.recordingManagersRead != nil {
		s.recordingManagersRead(gk, keys)
	}
	m.gaps = q.gaps
	return m
}
