package ownership

import (
	"cmp"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// View answers ownership questions for one caller from one session.
//
// It captures each object's metadata once, whether it read the object or
// found it searching an owner's dependents, and answers every question from
// that capture, so an object reads the same in every answer. Member lists
// aren't captured: Members and RecordingManagers read the managers' status
// each time they are asked. Answers read the
// cache as it is when asked, so one can be a moment older than another, as
// any informer read can. It searches each owner's dependents at most once.
// It never reads an object the caller may not read, and says nothing of
// whether there is one. Its answers hold only if the session's
// ValidateCaches passes once they are built. They share the view's captures,
// so they must not be modified. A View is not safe for concurrent use.
type View struct {
	session  ReadSession
	access   Access
	reads    map[resourceid.Ref]accessRead
	searches map[resourceid.Ref]DependentCandidatesRead
}

type accessRead struct {
	read ObjectRead
	may  Decision
}

// NewView answers for the caller access describes, from s.
func NewView(s ReadSession, access Access) *View {
	return &View{
		session:  s,
		access:   access,
		reads:    map[resourceid.Ref]accessRead{},
		searches: map[resourceid.Ref]DependentCandidatesRead{},
	}
}

// read reads ref for the caller, once.
func (v *View) read(ref resourceid.Ref) (ObjectRead, Decision) {
	if r, ok := v.reads[ref]; ok {
		return r.read, r.may
	}
	r := accessRead{may: v.access.CanRead(ref.GroupKind(), ref.Namespace)}
	if r.may == Allowed {
		r.read = v.session.Object(ref)
	}
	v.reads[ref] = r
	return r.read, r.may
}

// Seed reads o as its object when the session can't, as when the caller
// fetched it from the API and Radar doesn't collect its kind. A read that
// holds the object stands, as does an excluded one or one the caller may not
// make. Seed an object before asking about it, so every answer reads it the
// same way. The view keeps o, which must not be modified afterwards.
func (v *View) Seed(o Observation) {
	if read, may := v.read(o.Ref); may == Allowed && (read.State == Unavailable || read.State == Absent) {
		v.reads[o.Ref] = accessRead{read: ObjectRead{State: Present, Object: o}, may: Allowed}
	}
}

// Subject is the object an answer is about, as the caller read it.
type Subject struct {
	Read ObjectRead
	// Access is whether the caller may read the object. When it may not,
	// Read is unset and the answer says nothing more.
	Access Decision
}

// Present reports whether the caller read the object, so the answer has an
// object to be about.
func (s Subject) Present() bool {
	return s.Access == Allowed && s.Read.State == Present
}

func (v *View) subject(ref resourceid.Ref) Subject {
	read, may := v.read(ref)
	return Subject{Read: read, Access: may}
}

func (v *View) resolve(dependent Observation, ref metav1.OwnerReference) OwnerResolution {
	return resolveOwner(v.session.Catalog(), dependent, ref, v.read)
}

// References resolves each of ref's owner references for the caller, in the
// object's order.
func (v *View) References(ref resourceid.Ref) (Subject, []OwnerResolution) {
	subject := v.subject(ref)
	if !subject.Present() {
		return subject, nil
	}
	refs := subject.Read.Object.OwnerReferences
	out := make([]OwnerResolution, len(refs))
	for i, r := range refs {
		out[i] = v.resolve(subject.Read.Object, r)
	}
	return subject, out
}

// OwnerAnswer is an object's owner reference: its controller reference, or
// its only reference when it has no controller.
type OwnerAnswer struct {
	Subject Subject
	// Reference is the owner reference chosen, one of the subject's own. It is
	// nil when there is none: the subject wasn't read, it has no owner
	// references, or several without a controller (Several). Resolution means
	// something only when Reference is set.
	Reference  *metav1.OwnerReference
	Resolution OwnerResolution
	Several    bool
}

// Owner resolves ref's controller reference, or its only reference. The
// reference answers in whatever state it resolves to: a withheld or replaced
// controller never gives way to a weaker owner.
func (v *View) Owner(ref resourceid.Ref) OwnerAnswer {
	subject, resolutions := v.References(ref)
	a := OwnerAnswer{Subject: subject}
	if !subject.Present() {
		return a
	}
	refs := subject.Read.Object.OwnerReferences
	i := slices.IndexFunc(refs, isController)
	if i < 0 && len(refs) == 1 {
		i = 0
	}
	if i < 0 {
		a.Several = len(refs) > 1
		return a
	}
	a.Reference, a.Resolution = &refs[i], resolutions[i]
	return a
}

func isController(ref metav1.OwnerReference) bool {
	return ref.Controller != nil && *ref.Controller
}

// ChildrenAnswer is what an object owns, as one caller may see it.
type ChildrenAnswer struct {
	Subject Subject
	// Children are the objects whose owner reference names the subject's
	// current incarnation, ordered by group, kind, namespace and name.
	Children []Observation
	// Gaps are the parts of the search the source couldn't read (see
	// DependentCandidatesRead.Gaps), objects the search found that the view couldn't
	// read, and references to the subject's UID that may resolve to it but
	// couldn't be resolved, such as one at a version discovery doesn't list.
	Gaps []Gap
	// Narrowed: the caller's access is restricted, so Children are the ones
	// it may read, and whether there are others isn't said.
	Narrowed bool
}

// Complete reports whether Children are every child the caller may read. An
// answer about an object the caller didn't read lists none and is never
// complete.
func (a ChildrenAnswer) Complete() bool {
	return a.Subject.Present() && len(a.Gaps) == 0
}

// Children finds the objects with an owner reference that resolves to ref's
// current incarnation, as Owner resolves it: ref's group, kind and name, a
// served version, and ref's UID. A namespaced owner's children are in its
// namespace, where Kubernetes resolves their references; a cluster-scoped
// owner's can be anywhere. A reference with the UID that names other
// coordinates is not one Kubernetes resolves to ref.
func (v *View) Children(ref resourceid.Ref) ChildrenAnswer {
	subject := v.subject(ref)
	a := ChildrenAnswer{Subject: subject}
	if !subject.Present() {
		return a
	}
	owner := subject.Read.Object
	namespaces, all := []string{ref.Namespace}, false
	if ref.Namespace == "" {
		namespaces, all = v.access.Namespaces()
	}
	scope := SearchScope{AllNamespaces: all, Namespaces: namespaces, IncludeClusterScoped: ref.Namespace == ""}
	found, searched := v.searches[ref]
	if !searched {
		found = v.session.DependentCandidates(owner.UID, scope)
		v.searches[ref] = found
	}
	candidates, unread, undecided := v.candidates(found)
	// Saying a check failed would say a child may be there.
	a.Narrowed = v.access != Unrestricted() || undecided
	// unresolved are the gaps of references to the owner's UID that Radar
	// can't resolve: the collector retries them, so their objects may be
	// children.
	var unresolved []Gap
	for _, c := range candidates {
		names := false
		var unknown []Gap
		for _, r := range c.OwnerReferences {
			if r.UID != owner.UID {
				continue
			}
			switch res := v.resolve(c, r); {
			case res.State == OwnerFound && res.Ref == ref:
				names = true
			case res.State == OwnerUnknown && mayResolveTo(res, r.Name, ref):
				unknown = append(unknown, res.Gap)
			}
		}
		if !names {
			unresolved = append(unresolved, unknown...)
			continue
		}
		a.Children = append(a.Children, c)
	}
	for _, g := range found.Gaps {
		if v.coversReadable(g, scope) {
			a.Gaps = append(a.Gaps, g)
		}
	}
	// Objects the view couldn't read: the caller may read them, the view read
	// them for it.
	a.Gaps = append(a.Gaps, unread...)
	a.Gaps = append(a.Gaps, unresolved...)
	slices.SortFunc(a.Gaps, compareGaps)
	a.Gaps = slices.Compact(a.Gaps)
	return a
}

// mayResolveTo reports whether a reference naming ref's UID, whose resolution
// is unknown, may still resolve to ref: it names ref's name, and a kind Radar
// resolved to ref's or couldn't resolve within ref's group. UIDs are unique,
// so a reference to other coordinates never resolves to the object holding
// its UID, however the read of those coordinates goes.
func mayResolveTo(res OwnerResolution, name string, ref resourceid.Ref) bool {
	switch {
	case name != ref.Name:
		return false
	case res.Ref != (resourceid.Ref{}):
		return res.Ref.GroupKind() == ref.GroupKind()
	}
	return res.Gap.Kind.Group == ref.Group
}

// candidates are the objects the search found that the caller may read, each
// as the view captured it: an object it already read answers as it was read,
// whatever the search found since, and one the search found is captured as
// found. unread are the gaps of found objects the view read before it could
// tell whether they exist: the search says they name the owner, so leaving
// them out says nothing of whether they are children. undecided reports a
// candidate whose access check failed.
func (v *View) candidates(found DependentCandidatesRead) (out []Observation, unread []Gap, undecided bool) {
	for _, c := range found.Candidates {
		r, read := v.reads[c.Ref]
		if !read {
			r = accessRead{may: v.access.CanRead(c.Ref.GroupKind(), c.Ref.Namespace)}
			if r.may == Allowed {
				r.read = ObjectRead{State: Present, Object: c}
			}
			v.reads[c.Ref] = r
		}
		switch {
		case r.may == Undecided:
			undecided = true
		case r.may != Allowed:
		case r.read.State == Present:
			out = append(out, r.read.Object)
		case r.read.State == Unavailable:
			unread = append(unread, r.read.Gap)
		}
	}
	return out, unread, undecided
}

// coversReadable reports whether gap covers something the caller may read
// within scope, so leaving it unread may have hidden a child. A gap on a
// namespaced kind without a namespace covers every namespace of the scope,
// and counts if the caller may read the kind in any of them.
func (v *View) coversReadable(g Gap, scope SearchScope) bool {
	if g.Kind.Kind == "" {
		return true
	}
	if g.Namespace != "" {
		return v.access.CanRead(g.Kind, g.Namespace) != Denied
	}
	k, known := v.session.Catalog().Kind(g.Kind)
	if !known || !k.Namespaced {
		return v.access.CanRead(g.Kind, "") != Denied
	}
	if scope.AllNamespaces {
		return v.access.CanRead(g.Kind, "") != Denied
	}
	return slices.ContainsFunc(scope.Namespaces, func(ns string) bool { return v.access.CanRead(g.Kind, ns) != Denied })
}

// ControllerChainAnswer is the chain of controller references from an object
// up to the first one that can't be followed.
type ControllerChainAnswer struct {
	Subject Subject
	// Chain is every object the walk reached, from the subject on.
	Chain []resourceid.Ref
	// UnfollowedReference is the controller reference the walk couldn't
	// follow. It is nil when the last object in Chain has no controller
	// reference, and when the walk ends in a Cycle or is Truncated. A Node
	// controller ends the walk outside Chain, and is UnfollowedReference only
	// when it isn't found.
	UnfollowedReference *OwnerResolution
	// Cycle: the references loop back into Chain.
	Cycle bool
	// Truncated: the chain is longer than maxControllerDepth references,
	// which only corrupt references produce.
	Truncated bool
}

// maxControllerDepth bounds the walk; real chains are a few hops deep.
const maxControllerDepth = 16

// ControllerChain follows ref's controller references to the first it can't
// follow, and says why it stopped.
func (v *View) ControllerChain(ref resourceid.Ref) ControllerChainAnswer {
	subject := v.subject(ref)
	a := ControllerChainAnswer{Subject: subject}
	if !subject.Present() {
		return a
	}
	a.Chain = []resourceid.Ref{ref}
	current := subject.Read.Object
	for {
		i := slices.IndexFunc(current.OwnerReferences, isController)
		if i < 0 {
			return a
		}
		r := v.resolve(current, current.OwnerReferences[i])
		if r.Ref.Group == "" && r.Ref.Kind == "Node" {
			if r.State != OwnerFound {
				a.UnfollowedReference = &r
			}
			return a
		}
		if r.State != OwnerFound {
			a.UnfollowedReference = &r
			return a
		}
		if slices.Contains(a.Chain, r.Ref) {
			a.Cycle = true
			return a
		}
		if len(a.Chain) > maxControllerDepth {
			a.Truncated = true
			return a
		}
		a.Chain = append(a.Chain, r.Ref)
		current = r.Owner
	}
}

func compareRefs(a, b resourceid.Ref) int {
	return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
}

// MembersAnswer is what a GitOps manager's status records of this cluster's
// objects, as one caller may see it.
type MembersAnswer struct {
	Subject Subject
	// Membership is what the manager's status records; Members and the counts
	// are set only when it is MembersRecorded. A remote manager's destination
	// isn't confirmed to be this cluster, so none of its list is shown here.
	Membership MembershipState
	// Members are the recorded objects Radar holds that the caller may read,
	// sorted.
	Members []Observation
	// Missing counts the recorded objects Radar has loaded the stores of and
	// doesn't hold. Withheld counts those the caller may not read, and
	// Unknown those Radar can't say whether it holds (Gaps say why). The
	// manager's status, which the caller can read, names them all; Events,
	// which ownership doesn't read, are left out.
	Missing, Withheld, Unknown int
	Gaps                       []Gap
}

// Members reads the member list in the status of manager, which must be of a
// RecordingManagers kind, and reads each member.
func (v *View) Members(manager resourceid.Ref) MembersAnswer {
	subject := v.subject(manager)
	a := MembersAnswer{Subject: subject}
	if !subject.Present() || !IsRecordingManager(manager.GroupKind()) {
		return a
	}
	inventory := v.session.Members(manager)
	switch {
	case inventory.Manager.State == Unavailable:
		// As when the caller fetched a manager Radar doesn't collect.
		a.Gaps = append(a.Gaps, inventory.Manager.Gap)
		return a
	case inventory.Manager.State != Present || inventory.Manager.Object.UID != subject.Read.Object.UID:
		// The cache doesn't hold the incarnation the caller fetched yet.
		a.Gaps = append(a.Gaps, Gap{Kind: manager.GroupKind(), Namespace: manager.Namespace, Reason: readreason.KindSyncPending})
		return a
	}
	a.Membership = inventory.Membership
	if a.Membership != MembersRecorded {
		return a
	}
	for _, m := range inventory.Members {
		if IsEvent(m.GroupKind()) {
			continue
		}
		read, may := v.read(m)
		switch {
		case may == Denied:
			a.Withheld++
		case may == Undecided:
			a.Unknown++
			a.Gaps = append(a.Gaps, Gap{Kind: m.GroupKind(), Namespace: m.Namespace, Reason: readreason.UserPermissionCheckFailed})
		case read.State == Present:
			a.Members = append(a.Members, read.Object)
		case read.State == Absent:
			a.Missing++
		case read.State == Unavailable:
			a.Unknown++
			a.Gaps = append(a.Gaps, read.Gap)
		}
	}
	slices.SortFunc(a.Gaps, compareGaps)
	a.Gaps = slices.Compact(a.Gaps)
	return a
}

func compareGaps(a, b Gap) int {
	return cmp.Or(cmp.Compare(a.Kind.Group, b.Kind.Group), cmp.Compare(a.Kind.Kind, b.Kind.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Reason, b.Reason))
}

// RecordingManagersAnswer is what the local GitOps managers say of one object, as one
// caller may see them.
type RecordingManagersAnswer struct {
	// Managers are the managers the caller may read whose member lists name
	// the object, sorted.
	Managers []Observation
	// Unreadable are the managers the caller may read whose member lists
	// can't be read, any of which may name the object, sorted.
	Unreadable []resourceid.Ref
	// Remote are the managers the caller may read whose lists name an
	// object at the same coordinates in a cluster Radar can't confirm is this
	// one, sorted.
	Remote []resourceid.Ref
	// Gaps are the search's gaps the caller may read; see RecordingManagersRead.Gaps.
	Gaps []Gap
	// searchGaps are all the search's gaps, for Searched.
	searchGaps []Gap
}

// Searched reports whether the search read the member list of manager, a
// manager the caller may read and Radar holds: no gap the search reported,
// whether or not the caller may read it, leaves manager's kind unread where
// manager is. A gap across all namespaces that stands for the namespaces
// Radar doesn't collect leaves manager's own read.
func (a RecordingManagersAnswer) Searched(manager resourceid.Ref) bool {
	return !slices.ContainsFunc(a.searchGaps, func(g Gap) bool { return covers(g, manager) })
}

// covers reports whether a search's gap left ref's kind and place unread, ref
// being an object Radar holds.
func covers(g Gap, ref resourceid.Ref) bool {
	switch {
	case g.Kind != ref.GroupKind():
		return false
	case g.Namespace == ref.Namespace:
		return true
	case g.Namespace != "":
		return false
	}
	return g.Reason != readreason.RadarNamespaceNotCollected
}

// RecordingManagers finds the local managers whose member lists name ref, among those
// the caller may read; a manager the caller may not read takes no part. A
// manager the view already read answers as it was read, and one it hadn't is
// captured as found.
func (v *View) RecordingManagers(ref resourceid.Ref) RecordingManagersAnswer {
	found := v.session.RecordingManagers(ref)
	a := RecordingManagersAnswer{searchGaps: found.Gaps}
	for _, g := range found.Gaps {
		// A gap the caller may not read would say what Radar collects where
		// the caller can't look.
		if v.access.CanRead(g.Kind, g.Namespace) == Allowed {
			a.Gaps = append(a.Gaps, g)
		}
	}
	for _, m := range found.Managers {
		r, read := v.reads[m.Ref]
		if !read {
			r = accessRead{may: v.access.CanRead(m.Ref.GroupKind(), m.Ref.Namespace)}
			if r.may == Allowed {
				r.read = ObjectRead{State: Present, Object: m}
			}
			v.reads[m.Ref] = r
		}
		if r.may == Allowed && r.read.State == Present {
			a.Managers = append(a.Managers, r.read.Object)
		}
	}
	for _, m := range found.Unreadable {
		if v.access.CanRead(m.GroupKind(), m.Namespace) == Allowed {
			a.Unreadable = append(a.Unreadable, m)
		}
	}
	for _, m := range found.Remote {
		if v.access.CanRead(m.GroupKind(), m.Namespace) == Allowed {
			a.Remote = append(a.Remote, m)
		}
	}
	return a
}
