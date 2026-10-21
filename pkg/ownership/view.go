package ownership

import (
	"cmp"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// View answers ownership questions for one caller from one session.
//
// It captures each object once, whether it read the object or found it
// searching an owner's dependents, and answers every question from that
// capture, so its answers agree with each other even while the cluster
// changes; RecordingManagers says where its answers about different objects fall
// short of that. It searches each owner's dependents and reads each manager's
// member list at most once. An object it first reads after a search that
// should have found it means the cluster changed in a way one capture can't
// reconcile, and ValidateAnswers says so. It never reads an object the caller may
// not read, and says nothing of whether there is one. Its answers hold only
// if ValidateAnswers passes once they are built. They share the view's captures, so
// they must not be modified. A View is not safe for concurrent use.
type View struct {
	session ReadSession
	access  Access
	reads   map[resourceid.Ref]accessRead
	// named holds the objects the view captured under each UID their owner
	// references name.
	named       map[types.UID][]resourceid.Ref
	searches    map[resourceid.Ref]DependentCandidatesRead
	searched    map[types.UID]searchScope
	inventories map[resourceid.Ref]MembersRead
	// managerAnswers holds the answer for each object RecordingManagers was
	// asked about, and managerSearches the search behind it.
	managerAnswers  map[resourceid.Ref]RecordingManagersAnswer
	managerSearches map[resourceid.Ref]managerSearch
	// readable holds, for each manager the caller may read that a search or
	// an inventory read, whether its member list could be read.
	readable map[resourceid.Ref]bool
	// memberSets holds the members of each inventory checked against a
	// search.
	memberSets map[resourceid.Ref]map[resourceid.Ref]bool
	changed    bool
}

// managerSearch is one search for the managers recording an object, with
// the managers the caller may read it found as listing it and as unreadable.
type managerSearch struct {
	found      RecordingManagersRead
	managers   map[resourceid.Ref]bool
	unreadable map[resourceid.Ref]bool
}

// searchScope is where the view answered for an owner's children, and the
// gaps that answer reported.
type searchScope struct {
	domain SearchScope
	gaps   []Gap
}

type accessRead struct {
	read ObjectRead
	may  Decision
}

// NewView answers for the caller access describes, from s.
func NewView(s ReadSession, access Access) *View {
	return &View{
		session:         s,
		access:          access,
		reads:           map[resourceid.Ref]accessRead{},
		named:           map[types.UID][]resourceid.Ref{},
		searches:        map[resourceid.Ref]DependentCandidatesRead{},
		searched:        map[types.UID]searchScope{},
		inventories:     map[resourceid.Ref]MembersRead{},
		managerAnswers:  map[resourceid.Ref]RecordingManagersAnswer{},
		managerSearches: map[resourceid.Ref]managerSearch{},
		readable:        map[resourceid.Ref]bool{},
		memberSets:      map[resourceid.Ref]map[resourceid.Ref]bool{},
	}
}

// ValidateAnswers returns the session's ValidateCaches error, if any, and
// ErrObjectsChanged if an object the view read contradicts a search it made, a
// seed replaced a read an answer used, or a manager changed between its two
// reads.
func (v *View) ValidateAnswers() error {
	if err := v.session.ValidateCaches(); err != nil {
		return err
	}
	if v.changed {
		return ErrObjectsChanged
	}
	return nil
}

// read reads ref for the caller, once.
func (v *View) read(ref resourceid.Ref) (ObjectRead, Decision) {
	if r, ok := v.reads[ref]; ok {
		return r.read, r.may
	}
	r := accessRead{may: v.access.CanRead(ref.GroupKind(), ref.Namespace)}
	if r.may == Allowed {
		r.read = v.session.Object(ref)
		if r.read.State == Present {
			v.captured(r.read.Object, true)
		}
	}
	v.reads[ref] = r
	return r.read, r.may
}

// captured records an object the view has just captured under the UIDs it
// names, and checks it against the owners whose children it already answered
// for. An answer counts only objects the view had captured, so one it names,
// whose answer covered its kind and place, was answered for before the object
// named it. collected says the session holds o's kind in o's namespace.
func (v *View) captured(o Observation, collected bool) {
	for i, r := range o.OwnerReferences {
		if slices.ContainsFunc(o.OwnerReferences[:i], func(p metav1.OwnerReference) bool { return p.UID == r.UID }) {
			continue
		}
		v.named[r.UID] = append(v.named[r.UID], o.Ref)
		s, ok := v.searched[r.UID]
		if !ok || !v.searchedIn(o.Ref, s.domain) {
			continue
		}
		if slices.ContainsFunc(s.gaps, func(g Gap) bool { return covers(g, o.Ref, collected) }) {
			continue
		}
		v.changed = true
	}
}

// covers reports whether a search's gap left ref's kind and place unread. A
// gap across all namespaces that stands for the namespaces Radar doesn't
// collect leaves the ones it does collect read.
func covers(g Gap, ref resourceid.Ref, collected bool) bool {
	switch {
	case g.Kind != ref.GroupKind():
		return false
	case g.Namespace == ref.Namespace:
		return true
	case g.Namespace != "":
		return false
	}
	return !collected || g.Reason != readreason.RadarNamespaceNotCollected
}

// Seed reads o as its object when the session can't, as when the caller
// fetched it from the API and Radar doesn't collect its kind. A read that
// holds the object stands, as does an excluded one or one the caller may not
// make. Seed an object before asking about it: replacing a read an earlier
// answer used makes ValidateAnswers fail. The view keeps o, which must not be
// modified afterwards.
func (v *View) Seed(o Observation) {
	_, answered := v.reads[o.Ref]
	if read, may := v.read(o.Ref); may == Allowed && (read.State == Unavailable || read.State == Absent) {
		v.reads[o.Ref] = accessRead{read: ObjectRead{State: Present, Object: o}, may: Allowed}
		v.changed = v.changed || answered
		v.captured(o, read.State == Absent)
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
	// nil when there is none: no owner references at all, or several without
	// a controller (Several).
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
	// Truncated: the limit cut Children short, so the caller may read more.
	Truncated bool
	// Narrowed: the caller may read only some of the places children could
	// be, so Children are the ones it may read, and whether there are others
	// isn't said.
	Narrowed bool
}

// Complete reports whether Children are every child the caller may read. An
// answer about an object the caller didn't read lists none and is never
// complete.
func (a ChildrenAnswer) Complete() bool {
	return a.Subject.Present() && len(a.Gaps) == 0 && !a.Truncated
}

// Children finds the objects with an owner reference that resolves to ref's
// current incarnation, as Owner resolves it: ref's group, kind and name, a
// served version, and ref's UID. A namespaced owner's children are in its
// namespace, where Kubernetes resolves their references; a cluster-scoped
// owner's can be anywhere. A reference with the UID that names other
// coordinates is not one Kubernetes resolves to ref. limit caps the children
// returned, 0 for no limit; it counts only children the caller may read. It
// caps the answer, not the work: every candidate is read and captured first.
func (v *View) Children(ref resourceid.Ref, limit int) ChildrenAnswer {
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
	domain := SearchScope{AllNamespaces: all, Namespaces: namespaces, IncludeClusterScoped: ref.Namespace == ""}
	a.Narrowed = v.narrows(ref)
	found, searched := v.searches[ref]
	if !searched {
		found = v.session.DependentCandidates(owner.UID, domain, 0)
		v.searches[ref] = found
	}
	candidates, unread, undecided := v.candidates(found, owner.UID, domain)
	// Saying a check failed would say a child may be there.
	a.Narrowed = a.Narrowed || undecided
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
		if limit > 0 && len(a.Children) == limit {
			a.Truncated = true
			break
		}
		a.Children = append(a.Children, c)
	}
	for _, g := range found.Gaps {
		if v.coversReadable(g, domain) {
			a.Gaps = append(a.Gaps, g)
		}
	}
	if !searched {
		v.searched[owner.UID] = searchScope{domain: domain, gaps: slices.Clone(a.Gaps)}
	}
	// Objects the view couldn't read (the caller may read them: the view read
	// them for it) and references it couldn't resolve leave no kind or place
	// unsearched, so they excuse no object the search should have found.
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

// candidates are the objects the caller may read that name uid in domain,
// each as the view captured it: an object it already read answers as it was
// read, whatever the search found since, and one the search found is
// captured as found. Captured objects that still name uid are kept even if
// the search no longer finds them, so every answer reflects one capture of
// each object. unread are the gaps of found objects the view read before it
// could tell whether they exist: the search says they name uid, so leaving
// them out says nothing of whether they are children. undecided reports a
// candidate whose access check failed.
func (v *View) candidates(found DependentCandidatesRead, uid types.UID, domain SearchScope) (out []Observation, unread []Gap, undecided bool) {
	for _, c := range found.Candidates {
		r, read := v.reads[c.Ref]
		if !read {
			r = accessRead{may: v.access.CanRead(c.Ref.GroupKind(), c.Ref.Namespace)}
			if r.may == Allowed {
				r.read = ObjectRead{State: Present, Object: c}
				v.captured(c, true)
			}
			v.reads[c.Ref] = r
		}
		undecided = undecided || r.may == Undecided
		if r.may == Allowed && r.read.State == Unavailable {
			unread = append(unread, r.read.Gap)
		}
	}
	for _, ref := range v.named[uid] {
		if v.searchedIn(ref, domain) {
			out = append(out, v.reads[ref].read.Object)
		}
	}
	slices.SortFunc(out, func(a, b Observation) int { return compareRefs(a.Ref, b.Ref) })
	return out, unread, undecided
}

// searchedIn reports whether a dependents search of domain covers ref's kind
// and place.
func (v *View) searchedIn(ref resourceid.Ref, domain SearchScope) bool {
	k, known := v.session.Catalog().Kind(ref.GroupKind())
	if !known || !k.HasGCVerbs || IsEvent(ref.GroupKind()) {
		return false
	}
	if !k.Namespaced {
		return domain.IncludeClusterScoped
	}
	return domain.AllNamespaces || slices.Contains(domain.Namespaces, ref.Namespace)
}

// coversReadable reports whether gap covers something the caller may read
// within domain, so leaving it unread may have hidden a child. A gap on a
// namespaced kind without a namespace covers every namespace of the domain,
// and counts if the caller may read the kind in any of them.
func (v *View) coversReadable(g Gap, domain SearchScope) bool {
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
	if domain.AllNamespaces {
		return v.access.CanRead(g.Kind, "") != Denied
	}
	return slices.ContainsFunc(domain.Namespaces, func(ns string) bool { return v.access.CanRead(g.Kind, ns) != Denied })
}

// narrows reports whether the caller may read less than every place ref's
// children could be: anywhere for a cluster-scoped owner, its namespace for a
// namespaced one.
func (v *View) narrows(ref resourceid.Ref) bool {
	if v.access.CanReadEverything() {
		return false
	}
	return ref.Namespace == "" || !v.access.CanReadAllKindsIn(ref.Namespace)
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

// MembersAnswer is what a GitOps manager's status records it applies to
// this cluster, as one caller may see it.
type MembersAnswer struct {
	Subject Subject
	// Membership is what the manager's status records; Members and the counts
	// are set only when it is MembersRecorded. A remote manager's list names
	// objects of another cluster.
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
	inventory, read := v.inventories[manager]
	if !read {
		inventory = v.session.Members(manager)
		v.inventories[manager] = inventory
	}
	captured := subject.Read.Object
	switch {
	case inventory.Manager.State == Unavailable:
		// As when the caller fetched a manager Radar doesn't collect.
		a.Gaps = append(a.Gaps, inventory.Manager.Gap)
		return a
	case inventory.Manager.State != Present || inventory.Manager.Object.UID != captured.UID ||
		captured.ResourceVersion != "" && inventory.Manager.Object.ResourceVersion != captured.ResourceVersion:
		// The manager changed between the two reads, which one capture can't
		// reconcile.
		v.changed = true
		a.Gaps = append(a.Gaps, Gap{Kind: manager.GroupKind(), Namespace: manager.Namespace, Reason: readreason.KindSyncPending})
		return a
	}
	for ref, search := range v.managerSearches {
		if !v.managersAgree(search, ref, manager, inventory) {
			v.changed = true
		}
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
	// TargetUnresolvable reports that the object can't be named, so no manager's list
	// was searched; see RecordingManagersRead.TargetUnresolvable.
	TargetUnresolvable bool
	// Gaps are the search's gaps the caller may read; see RecordingManagersRead.Gaps.
	Gaps []Gap
	// searchGaps are all the search's gaps, for Searched.
	searchGaps []Gap
}

// Searched reports whether the search read the member list of manager, a
// manager the caller may read and Radar holds: the object could be named, and
// no gap the search reported, whether or not the caller may read it, leaves
// manager's kind unread where manager is. A gap across all namespaces that
// stands for the namespaces Radar doesn't collect leaves manager's own read.
func (a RecordingManagersAnswer) Searched(manager resourceid.Ref) bool {
	return !a.TargetUnresolvable && !slices.ContainsFunc(a.searchGaps, func(g Gap) bool { return covers(g, manager, true) })
}

// RecordingManagers finds the local managers whose member lists name ref, among those
// the caller may read; a manager the caller may not read takes no part. Asked
// again for ref, it gives the same answer.
//
// A manager it finds listing ref is captured as found and must match any other
// capture of it. Whether a manager's list can be read must agree across every
// search and member-list read. And a member list the view read must agree with
// each search, in either order. ValidateAnswers reports any disagreement. A search for
// another object reads the index again, so when the view read no member list
// of a manager, a change to that list between two searches is caught only if a
// search returns the manager at another version or in another state.
func (v *View) RecordingManagers(ref resourceid.Ref) RecordingManagersAnswer {
	if a, asked := v.managerAnswers[ref]; asked {
		return a
	}
	found := v.session.RecordingManagers(ref)
	a := RecordingManagersAnswer{TargetUnresolvable: found.TargetUnresolvable, searchGaps: found.Gaps}
	for _, g := range found.Gaps {
		// A gap the caller may not read would say what Radar collects where
		// the caller can't look.
		if v.access.CanRead(g.Kind, g.Namespace) == Allowed {
			a.Gaps = append(a.Gaps, g)
		}
	}
	search := managerSearch{found: found, managers: map[resourceid.Ref]bool{}, unreadable: map[resourceid.Ref]bool{}}
	for _, m := range found.Managers {
		r, read := v.reads[m.Ref]
		if !read {
			r = accessRead{may: v.access.CanRead(m.Ref.GroupKind(), m.Ref.Namespace)}
			if r.may == Allowed {
				r.read = ObjectRead{State: Present, Object: m}
				v.captured(m, true)
			}
			v.reads[m.Ref] = r
		}
		if r.may != Allowed {
			continue
		}
		search.managers[m.Ref] = true
		v.listRead(m.Ref, true)
		if r.read.State != Present || r.read.Object.UID != m.UID || r.read.Object.ResourceVersion != m.ResourceVersion {
			v.changed = true
			continue
		}
		a.Managers = append(a.Managers, r.read.Object)
	}
	may := func(m resourceid.Ref) bool { return v.access.CanRead(m.GroupKind(), m.Namespace) == Allowed }
	for _, m := range found.Remote {
		if may(m) {
			v.listRead(m, true)
		}
	}
	for _, m := range found.Unreadable {
		if may(m) {
			search.unreadable[m] = true
			v.listRead(m, false)
			a.Unreadable = append(a.Unreadable, m)
		}
	}
	// The search returns every manager whose list can't be read wherever it
	// read, so one read as unreadable before that it didn't return changed.
	for m, readable := range v.readable {
		if !readable && !search.unreadable[m] && searched(found, m) {
			v.changed = true
		}
	}
	for manager, inventory := range v.inventories {
		if inventory.Manager.State == Present && !v.managersAgree(search, ref, manager, inventory) {
			v.changed = true
		}
	}
	v.managerSearches[ref] = search
	v.managerAnswers[ref] = a
	return a
}

// listRead records whether manager's member list could be read, as one read of
// the manager said; a read that says otherwise saw it change.
func (v *View) listRead(manager resourceid.Ref, readable bool) {
	if was, read := v.readable[manager]; read && was != readable {
		v.changed = true
	}
	v.readable[manager] = readable
}

// searched reports whether found, a search for the managers recording an
// object, read the member list of manager, a manager Radar holds.
func searched(found RecordingManagersRead, manager resourceid.Ref) bool {
	return !found.TargetUnresolvable && !slices.ContainsFunc(found.Gaps, func(g Gap) bool { return covers(g, manager, true) })
}

// managersAgree reports whether search, for the managers recording ref,
// agrees with inventory, a read of manager's member list: the search finds the
// manager as listing it exactly when its list is readable, local and names
// ref, and as unreadable exactly when its list can't be read. A search that
// couldn't read the manager's list says nothing of it.
func (v *View) managersAgree(search managerSearch, ref, manager resourceid.Ref, inventory MembersRead) bool {
	if !searched(search.found, manager) {
		return true
	}
	listed, unreadable := search.managers[manager], search.unreadable[manager]
	switch inventory.Membership {
	case MembersUnreadable:
		return unreadable && !listed
	case MembersRecorded:
		members, built := v.memberSets[manager]
		if !built {
			members = make(map[resourceid.Ref]bool, len(inventory.Members))
			for _, m := range inventory.Members {
				members[m] = true
			}
			v.memberSets[manager] = members
		}
		return !unreadable && listed == members[ref]
	}
	// A remote manager is found, if at all, as elsewhere.
	return !unreadable && !listed
}
