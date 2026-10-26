package ownership

import (
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ManagerState sums up what the evidence says manages an object. It never
// names a winner the evidence doesn't support: a disagreement stays one.
//
// "Claims" in the states are the claims that count: a copied claim names
// another object's manager and never counts, and a native Helm release's
// counts only when no list names the object and no GitOps claim exists, since
// Flux's helm-controller and adopted installs carry its annotations too.
// Every claim stays in the evidence either way.
type ManagerState string

const (
	// ManagerShared: two or more local managers' status lists name it.
	ManagerShared ManagerState = "shared"
	// ManagerDisagree: one manager's list names it and its metadata names
	// another.
	ManagerDisagree ManagerState = "disagree"
	// ManagerAgree: one manager's list names it and every claim its metadata
	// makes names that manager.
	ManagerAgree ManagerState = "agree"
	// ManagerListed: one manager's list names it; no claim counts.
	ManagerListed ManagerState = "listed"
	// ManagerClaimed: only its metadata names a manager, whose list couldn't
	// confirm it (see each claim's Check).
	ManagerClaimed ManagerState = "claimed"
	// ManagerUnknown: no evidence names a manager, and Radar couldn't look
	// everywhere it would be.
	ManagerUnknown ManagerState = "unknown"
	// ManagerNone: no evidence names a manager in what Radar collects and
	// the caller may read. It is not a statement that nothing manages it.
	ManagerNone ManagerState = "none"
)

// ClaimCheck is what checking a claim against its manager found.
type ClaimCheck string

const (
	// ClaimRecorded: the claimed manager's list names the object.
	ClaimRecorded ClaimCheck = "recorded"
	// ClaimOmitted: the claimed manager's list was read and doesn't name it,
	// as while the list lags an apply or for a Helm hook.
	ClaimOmitted ClaimCheck = "omitted"
	// ClaimUnreadable: the claimed manager's list can't be read.
	ClaimUnreadable ClaimCheck = "unreadable"
	// ClaimRemote: the claimed manager applies to a cluster Radar can't
	// confirm is this one.
	ClaimRemote ClaimCheck = "remote"
	// ClaimMissing: no such manager exists in this cluster.
	ClaimMissing ClaimCheck = "missing"
	// ClaimNotRecording: the claimed manager keeps no list, as a native Helm
	// release.
	ClaimNotRecording ClaimCheck = "not-recording"
	// ClaimUnresolved: the claim names no namespace and no manager whose list
	// names the object has that name.
	ClaimUnresolved ClaimCheck = "unresolved"
	// ClaimWithheld: the caller may not read the claimed manager, so nothing
	// is said of it.
	ClaimWithheld ClaimCheck = "withheld"
	// ClaimUnchecked: Radar couldn't establish what the manager's list says:
	// it couldn't read or decide access to the manager, the manager was
	// replaced since the search, or the search didn't cover it. Unlike
	// ClaimUnreadable, the list itself may be fine.
	ClaimUnchecked ClaimCheck = "unchecked"
)

// CheckedClaim is a claim an object's metadata makes, and what checking it
// found.
type CheckedClaim struct {
	gitops.Claim
	Check ClaimCheck
	// Weak: the claim doesn't stop the walk up the controller chain: it was
	// copied from another object, or it is metadata an object a controller
	// owns commonly inherits, an Argo CD instance label from its template or
	// a Helm release's annotations from its Deployment. A weak claim that
	// isn't copied can still answer when nothing on the chain settles it.
	Weak bool
}

// ManagerEvidence is what names the managers of one object.
type ManagerEvidence struct {
	Object resourceid.Ref
	// Recorders are the local managers the caller may read whose lists name
	// the object, sorted.
	Recorders []resourceid.Ref
	Claims    []CheckedClaim
	// Unreadable and Remote count the managers the caller may read that may
	// name the object without Radar being able to tell; Gaps are the kinds
	// and places the search couldn't read.
	Unreadable, Remote int
	Gaps               []Gap
	// Undecided: Radar couldn't check whether the caller may read some of
	// what the search found.
	Undecided bool
}

func (e ManagerEvidence) complete() bool {
	return e.Unreadable == 0 && e.Remote == 0 && len(e.Gaps) == 0 && !e.Undecided
}

// strong reports whether e settles which managers the object has: a list
// names it, or a claim that isn't weak does.
func (e ManagerEvidence) strong() bool {
	return len(e.Recorders) > 0 || slices.ContainsFunc(e.Claims, func(c CheckedClaim) bool { return !c.Weak })
}

// ChainEnd says why a walk up the controller chain that found no evidence
// settling the question stopped short. It is empty when evidence settled it
// first, whatever lies above, or when the walk reached the chain's top.
type ChainEnd string

const (
	ChainEndUnfollowed ChainEnd = "unfollowed"
	ChainEndCycle      ChainEnd = "cycle"
	ChainEndTruncated  ChainEnd = "truncated"
)

// ManagerAnswer is what manages one object, as one caller may see it.
type ManagerAnswer struct {
	Subject Subject
	State   ManagerState
	// Lead is the manager to show first, never a verdict: the recorder for
	// agree and listed; for shared, the one recorder a claim names, if exactly
	// one; for claimed, the manager every claim names, if they agree; none
	// otherwise. Its namespace is empty when it comes from a claim that
	// names none, and it may be a manager Radar couldn't read.
	Lead *resourceid.Ref
	// Direct is the evidence on the object itself.
	Direct ManagerEvidence
	// Via is the evidence of the nearest controller ancestor that settles
	// the question, when the object's own doesn't, or, when no object does
	// and the object makes no claim that isn't copied, of the nearest
	// ancestor that does; State and Lead are then derived from it.
	Via *ManagerEvidence
	// Between is the evidence of other objects the walk passed, in chain
	// order, that make claims, or that Radar couldn't search fully where it
	// could the object's: evidence that didn't settle the question, kept so
	// an answer can say why. When Via is a fallback to the nearest claim,
	// claims farther up the chain stay here.
	Between  []ManagerEvidence
	ChainEnd ChainEnd
	// Stop is the controller reference the walk couldn't follow, when
	// ChainEnd is unfollowed.
	Stop *ChainStop
	// Narrowed: the caller's access is restricted, so the evidence is the
	// part the caller may read.
	Narrowed bool
}

// ChainStop is a controller reference a walk up the chain couldn't follow.
type ChainStop struct {
	// At is the last object the walk reached, whose reference it is.
	At resourceid.Ref
	// Reference is At's controller reference, as At's metadata has it.
	Reference metav1.OwnerReference
	Owner     OwnerResolution
}

// Manager says what manages ref: the local managers whose lists name it and
// the claims its metadata makes, or, when those don't settle it, the same of
// its nearest controller ancestor that does (see Via). Evidence that
// disagrees stays a disagreement. The answer means something only when
// Subject is Present; otherwise its State is empty and Subject says why.
func (v *View) Manager(ref resourceid.Ref) ManagerAnswer {
	chain := v.ControllerChain(ref)
	a := ManagerAnswer{Subject: chain.Subject, Narrowed: v.access != Unrestricted()}
	if !chain.Subject.Present() {
		return a
	}
	complete := true
	for i, r := range chain.Chain {
		e := v.managerEvidence(r)
		if i == 0 {
			a.Direct = e
		}
		if e.strong() {
			if i > 0 {
				a.Via = &e
			}
			a.State, a.Lead = derive(e, e.complete())
			return a
		}
		complete = complete && e.complete()
		if i > 0 && (len(e.Claims) > 0 || !e.complete() && a.Direct.complete()) {
			a.Between = append(a.Between, e)
		}
	}
	switch {
	case chain.UnfollowedReference != nil:
		a.ChainEnd = ChainEndUnfollowed
		at := chain.Chain[len(chain.Chain)-1]
		a.Stop = &ChainStop{At: at, Owner: *chain.UnfollowedReference}
		if read, _ := v.read(at); read.State == Present {
			if i := slices.IndexFunc(read.Object.OwnerReferences, isController); i >= 0 {
				a.Stop.Reference = read.Object.OwnerReferences[i]
			}
		}
	case chain.Cycle:
		a.ChainEnd = ChainEndCycle
	case chain.Truncated:
		a.ChainEnd = ChainEndTruncated
	}
	// No object settled it. A claim the walk passed is still the best lead
	// it found, unless the object's own metadata makes one.
	if !slices.ContainsFunc(a.Direct.Claims, active) {
		if i := slices.IndexFunc(a.Between, func(e ManagerEvidence) bool { return slices.ContainsFunc(e.Claims, active) }); i >= 0 {
			e := a.Between[i]
			a.Via, a.Between = &e, slices.Delete(slices.Clone(a.Between), i, i+1)
			a.State, a.Lead = derive(e, chainSettled(a) && complete)
			return a
		}
	}
	a.State, a.Lead = derive(a.Direct, chainSettled(a) && complete)
	return a
}

// active reports whether c counts as a claim: one copied from another object
// names that object's manager.
func active(c CheckedClaim) bool { return !c.Copied }

// chainSettled reports whether the walk read the whole chain: it reached the
// top, or an owner that is gone, was replaced or is an Event, which ends the
// chain with an answer rather than leaving a manager above it unread.
func chainSettled(a ManagerAnswer) bool {
	switch a.ChainEnd {
	case "":
		return true
	case ChainEndUnfollowed:
		switch a.Stop.Owner.State {
		case OwnerGone, OwnerReplaced, OwnerExcluded:
			return true
		}
	}
	return false
}

// managerEvidence reads the evidence on ref, an object the view holds.
func (v *View) managerEvidence(ref resourceid.Ref) ManagerEvidence {
	read, _ := v.read(ref)
	found := v.RecordingManagers(ref)
	e := ManagerEvidence{Object: ref, Unreadable: len(found.Unreadable), Remote: len(found.Remote), Gaps: found.Gaps, Undecided: found.Undecided}
	for _, m := range found.Managers {
		e.Recorders = append(e.Recorders, m.Ref)
	}
	controlled := slices.ContainsFunc(read.Object.OwnerReferences, isController)
	for _, c := range read.Object.Claims {
		cc := CheckedClaim{Claim: c, Weak: c.Copied || controlled && inherited(c.Source)}
		cc.Check = v.checkClaim(c.Manager, e.Recorders, found)
		e.Claims = append(e.Claims, cc)
	}
	return e
}

// inherited reports whether a claim from source is metadata a controller
// copies onto the objects it creates.
func inherited(source gitops.ClaimSource) bool {
	return source == gitops.ClaimArgoInstanceLabel || source == gitops.ClaimHelmRelease
}

func (v *View) checkClaim(claimed resourceid.Ref, recorders []resourceid.Ref, found RecordingManagersAnswer) ClaimCheck {
	if i := slices.IndexFunc(recorders, func(r resourceid.Ref) bool { return names(claimed, r) }); i >= 0 {
		return ClaimRecorded
	}
	switch {
	case !IsRecordingManager(claimed.GroupKind()):
		return ClaimNotRecording
	case claimed.Namespace == "":
		return ClaimUnresolved
	case slices.Contains(found.Unreadable, claimed):
		return ClaimUnreadable
	case slices.Contains(found.Remote, claimed):
		return ClaimRemote
	}
	subject := v.subject(claimed)
	switch {
	case subject.Access == Denied:
		return ClaimWithheld
	case subject.Read.State == Absent:
		return ClaimMissing
	case !subject.Present():
		return ClaimUnchecked
	}
	// Only the list's state matters here: building its members would cost
	// as much as a large Application lists.
	members := v.session.Membership(claimed)
	if members.Manager.State != Present || members.Manager.Object.UID != subject.Read.Object.UID {
		return ClaimUnchecked
	}
	switch members.Membership {
	case MembersRemote:
		return ClaimRemote
	case MembersUnreadable:
		return ClaimUnreadable
	case MembersRecorded:
		if found.Searched(claimed) {
			return ClaimOmitted
		}
	}
	return ClaimUnchecked
}

// names reports whether a claim of claimed names manager: the same manager,
// or, for a claim without a namespace, one of that kind and name.
func names(claimed, manager resourceid.Ref) bool {
	if claimed.Namespace == "" {
		claimed.Namespace = manager.Namespace
	}
	return claimed == manager
}

// derive sums up e. settled says whether nothing beyond e could name a
// manager, e itself or an ancestor: without it, finding no evidence is
// unknown rather than none.
func derive(e ManagerEvidence, settled bool) (ManagerState, *resourceid.Ref) {
	var active []gitops.Claim
	for _, c := range e.Claims {
		if !c.Copied {
			active = append(active, c.Claim)
		}
	}
	// A native Helm release names only how its chart was installed: Flux's
	// helm-controller and Argo CD's own installs carry its annotations too.
	if len(e.Recorders) > 0 || slices.ContainsFunc(active, func(c gitops.Claim) bool { return c.Source != gitops.ClaimHelmRelease }) {
		active = slices.DeleteFunc(active, func(c gitops.Claim) bool { return c.Source == gitops.ClaimHelmRelease })
	}
	named := func(manager resourceid.Ref) bool {
		return slices.ContainsFunc(active, func(c gitops.Claim) bool { return names(c.Manager, manager) })
	}
	switch {
	case len(e.Recorders) > 1:
		claimed := slices.DeleteFunc(slices.Clone(e.Recorders), func(r resourceid.Ref) bool { return !named(r) })
		if len(claimed) == 1 {
			return ManagerShared, &claimed[0]
		}
		return ManagerShared, nil
	case len(e.Recorders) == 1:
		r := e.Recorders[0]
		switch {
		case slices.ContainsFunc(active, func(c gitops.Claim) bool { return !names(c.Manager, r) }):
			return ManagerDisagree, nil
		case len(active) > 0:
			return ManagerAgree, &r
		}
		return ManagerListed, &r
	case len(active) > 0:
		// Claims name one manager when each names the lead, a claim without a
		// namespace naming any of its kind and name; the lead is the one that
		// says its namespace.
		lead := active[0].Manager
		for _, c := range active {
			if c.Manager.Namespace != "" {
				lead = c.Manager
				break
			}
		}
		if slices.ContainsFunc(active, func(c gitops.Claim) bool { return !names(c.Manager, lead) }) {
			return ManagerClaimed, nil
		}
		return ManagerClaimed, &lead
	case !settled:
		return ManagerUnknown, nil
	}
	return ManagerNone, nil
}
