package ownership

import (
	"slices"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ManagerState sums up what the evidence says manages an object. It never
// names a winner the evidence doesn't support: a disagreement stays one.
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
	// ManagerListed: one manager's list names it; its metadata names none.
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
	// ClaimMissing: Radar holds no claimed manager.
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
	// ClaimUnchecked: Radar couldn't read the claimed manager.
	ClaimUnchecked ClaimCheck = "unchecked"
)

// CheckedClaim is a claim an object's metadata makes, and what checking it
// found.
type CheckedClaim struct {
	gitops.Claim
	Check ClaimCheck
	// Weak: the claim doesn't settle the question for its object: it was
	// copied from another object, or it is an Argo CD instance label on an
	// object a controller owns, which commonly inherits it from a template.
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
}

func (e ManagerEvidence) complete() bool {
	return e.Unreadable == 0 && e.Remote == 0 && len(e.Gaps) == 0
}

// strong reports whether e settles which managers the object has: a list
// names it, or a claim that isn't weak does.
func (e ManagerEvidence) strong() bool {
	return len(e.Recorders) > 0 || slices.ContainsFunc(e.Claims, func(c CheckedClaim) bool { return !c.Weak })
}

// ChainEnd says why a walk up the controller chain stopped before an
// ancestor's evidence could answer; empty when it reached the chain's top.
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
	// Lead is the manager to show first, when the state has one: never a
	// verdict the evidence doesn't support.
	Lead *resourceid.Ref
	// Direct is the evidence on the object itself.
	Direct ManagerEvidence
	// Via is the evidence of the nearest controller ancestor that settles
	// the question, when the object's own doesn't; State and Lead are then
	// derived from it.
	Via      *ManagerEvidence
	ChainEnd ChainEnd
	// Narrowed: the caller's access is restricted, so the evidence is the
	// part the caller may read.
	Narrowed bool
}

// Manager says what manages ref: the local managers whose lists name it and
// the claims its metadata makes, or, when those don't settle it, the same of
// its nearest controller ancestor that does. Evidence that disagrees stays
// a disagreement.
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
		complete = complete && e.complete()
		if e.strong() {
			if i > 0 {
				a.Via = &e
			}
			a.State, a.Lead = derive(e, e.complete())
			return a
		}
	}
	switch {
	case chain.UnfollowedReference != nil:
		a.ChainEnd = ChainEndUnfollowed
	case chain.Cycle:
		a.ChainEnd = ChainEndCycle
	case chain.Truncated:
		a.ChainEnd = ChainEndTruncated
	}
	a.State, a.Lead = derive(a.Direct, a.ChainEnd == "" && complete)
	return a
}

// managerEvidence reads the evidence on ref, an object the view holds.
func (v *View) managerEvidence(ref resourceid.Ref) ManagerEvidence {
	read, _ := v.read(ref)
	found := v.RecordingManagers(ref)
	e := ManagerEvidence{Object: ref, Unreadable: len(found.Unreadable), Remote: len(found.Remote), Gaps: found.Gaps}
	for _, m := range found.Managers {
		e.Recorders = append(e.Recorders, m.Ref)
	}
	controlled := slices.ContainsFunc(read.Object.OwnerReferences, isController)
	for _, c := range read.Object.Claims {
		cc := CheckedClaim{Claim: c, Weak: c.Copied || c.Source == gitops.ClaimArgoInstanceLabel && controlled}
		cc.Check = v.checkClaim(c.Manager, e.Recorders, found)
		e.Claims = append(e.Claims, cc)
	}
	return e
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
	members := v.Members(claimed)
	switch {
	case members.Subject.Access == Denied:
		return ClaimWithheld
	case members.Subject.Read.State == Absent:
		return ClaimMissing
	case !members.Subject.Present():
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
		lead := active[0].Manager
		if slices.ContainsFunc(active, func(c gitops.Claim) bool { return c.Manager != lead }) {
			return ManagerClaimed, nil
		}
		return ManagerClaimed, &lead
	case !settled:
		return ManagerUnknown, nil
	}
	return ManagerNone, nil
}
