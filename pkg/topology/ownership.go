package topology

import (
	"slices"

	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// maxOwnershipRefs caps Children, and a GitOps manager's Manages, on the
// wire: a Deployment's ReplicaSets or a DaemonSet's Pods can number in the
// thousands, an Application's members in the tens of thousands. Another
// object's Manages, the graph's own edges, aren't capped.
const maxOwnershipRefs = 500

// ApplyOwnership answers rel's ownership from v, which reads for the caller,
// instead of from manages edges, which also stand for GitOps management,
// spec links and display shortcuts:
//
//   - Owner, Owners, Children and Deployment come from owner references, so
//     they include objects the topology doesn't draw, such as grouped Pods and
//     older ReplicaSets.
//   - Manages and Managers are the manages edges with evidence they are
//     management, never an edge matching an owner reference or a display
//     shortcut to or from an object on the controller chain; a GitOps
//     manager's Manages are the members its status records.
//
// fetched is the object rel describes, as the response shows it. It answers
// for itself when Radar doesn't collect its kind; if the caches hold another
// incarnation, rel answers nothing and reports a read error.
func ApplyOwnership(rel *Relationships, v *ownership.View, fetched ownership.Observation) {
	if rel == nil || v == nil {
		return
	}
	subject := fetched.Ref
	sources, targets := rel.manageSources, rel.manageTargets
	cov, owner, answered := applyOwner(rel, v, fetched)
	if !answered {
		return
	}
	_, references := v.References(subject)
	owners := map[resourceid.Ref]bool{}
	for _, r := range references {
		if r.State == ownership.OwnerFound && !owners[r.Ref] {
			owners[r.Ref] = true
			rel.Owners = append(rel.Owners, refOf(r.Ref))
		}
	}
	named := namedOwners(owner.Subject.Read.Object)
	rel.Deployment = deploymentOf(v, subject)

	children := v.Children(subject)
	cov.Children = ChildrenCoverage{Complete: children.Complete(), Narrowed: children.Narrowed, NotChecked: notChecked(children.Gaps)}
	isChild := map[resourceid.Ref]bool{}
	for _, c := range children.Children {
		isChild[c.Ref] = true
		if len(rel.Children) == maxOwnershipRefs {
			cov.Children.Truncated, cov.Children.Complete = true, false
			continue
		}
		rel.Children = append(rel.Children, refOf(c.Ref))
	}

	// The GitOps managers whose member lists name the subject manage it,
	// whether or not the graph draws the edge. A GitOps manager's edge counts
	// only where the search couldn't read its list; a list it read says
	// itself. The graph draws a HelmRelease's edges from its members' labels,
	// and an Application's or Kustomization's from the same status list, for
	// the workload and network kinds it draws.
	managers := v.RecordingManagers(subject)
	if len(managers.Unreadable)+len(managers.Remote)+len(managers.Gaps) > 0 {
		cov.Managers = &ManagersCoverage{Unreadable: len(managers.Unreadable), Remote: len(managers.Remote), NotChecked: notChecked(managers.Gaps)}
	}
	for _, m := range managers.Managers {
		rel.Managers = appendResourceRef(rel.Managers, refOf(m.Ref))
	}
	labelled := func(r resourceid.Ref) bool {
		return slices.Contains(managers.Unreadable, r) || !managers.Searched(r)
	}

	// Any other manages edge is management only with evidence that it isn't
	// ownership or a shortcut past the hops of a controller chain, such as a
	// CronJob's edge to its Job's Pod: the chain is known to its end.
	chain := v.ControllerChain(subject)
	for _, s := range sources {
		r := refIdentity(s)
		switch {
		case named[r] || slices.Contains(chain.Chain, r):
		case !present(v, r):
			// The caller may not read the manager, or it is gone since the
			// graph drew its edge.
		case ownership.IsRecordingManager(r.GroupKind()) && labelled(r), !ownership.IsRecordingManager(r.GroupKind()) && settled(chain):
			rel.Managers = appendResourceRef(rel.Managers, s)
		}
	}

	if ownership.IsRecordingManager(subject.GroupKind()) {
		members := v.Members(subject)
		cov.Manages = &MembersCoverage{Membership: members.Membership.String(), Missing: members.Missing, Withheld: members.Withheld, Unknown: members.Unknown}
		if members.Membership == ownership.MembersUnreadable && len(members.Gaps) > 0 {
			// The list couldn't be read, as opposed to the status having none.
			cov.Manages.UnavailableReason = string(members.Gaps[0].Reason)
		}
		switch members.Membership {
		case ownership.MembersRecorded:
			for i, m := range members.Members {
				if i == maxOwnershipRefs {
					cov.Manages.Truncated = true
					break
				}
				rel.Manages = append(rel.Manages, refOf(m.Ref))
			}
			return
		case ownership.MembersRemote:
			// The list names another cluster's objects.
			return
		}
		// With no list to read, the graph's edges are all there is: by the
		// members' labels for a HelmRelease, from the same status list for an
		// Application or Kustomization.
	}
	gitops := ownership.IsRecordingManager(subject.GroupKind())
	listed := map[resourceid.Ref]bool{}
	for _, t := range targets {
		r := refIdentity(t)
		if isChild[r] || listed[r] {
			continue
		}
		target, _ := v.References(r)
		if !target.Present() || namedOwners(target.Read.Object)[subject] {
			continue
		}
		if tr := v.ControllerChain(r); slices.Contains(tr.Chain, subject) || !gitops && !settled(tr) {
			continue
		}
		// A GitOps manager's coverage can say its list was cut short.
		if cov.Manages != nil && len(rel.Manages) == maxOwnershipRefs {
			cov.Manages.Truncated = true
			break
		}
		listed[r] = true
		rel.Manages = append(rel.Manages, t)
	}
}

// ApplyOwner answers only rel's Owner, and why there is none, from v: what a
// resource context reads, which needs no search for children and no member
// list. The rest of rel's ownership is cleared and left unanswered.
func ApplyOwner(rel *Relationships, v *ownership.View, fetched ownership.Observation) {
	if rel != nil && v != nil {
		applyOwner(rel, v, fetched)
	}
}

// applyOwner clears rel's ownership and answers its Owner, reporting whether
// the subject was read as fetched, so the rest can be answered.
func applyOwner(rel *Relationships, v *ownership.View, fetched ownership.Observation) (*OwnershipCoverage, ownership.OwnerAnswer, bool) {
	v.Seed(fetched)
	cov := clearOwnership(rel)
	owner := v.Owner(fetched.Ref)
	switch {
	case owner.Subject.Access == ownership.Denied:
		cov.UnavailableReason = string(readreason.UserForbidden)
		return cov, owner, false
	case owner.Subject.Access == ownership.Undecided:
		cov.UnavailableReason = string(readreason.UserPermissionCheckFailed)
		return cov, owner, false
	case owner.Subject.Read.State == ownership.Excluded:
		// Ownership doesn't read Events, so it says nothing of one.
		rel.Ownership = nil
		return nil, owner, false
	case owner.Subject.Read.State != ownership.Present:
		// Seed answers for an object the caches hold no copy of, so only an
		// unavailable read is left.
		cov.UnavailableReason = string(owner.Subject.Read.Gap.Reason)
		return cov, owner, false
	case owner.Subject.Read.Object.UID != fetched.UID:
		// Deleted and recreated since it was fetched: the answers would be
		// about another object.
		cov.UnavailableReason = string(readreason.ReadError)
		return cov, owner, false
	}

	switch {
	case owner.Several:
		cov.PrimaryOwnerState = "several"
	case owner.Reference != nil && owner.Resolution.State == ownership.OwnerFound:
		ref := refOf(owner.Resolution.Ref)
		rel.Owner = &ref
	case owner.Reference != nil:
		cov.PrimaryOwnerState = owner.Resolution.State.String()
		switch owner.Resolution.State {
		case ownership.OwnerGone, ownership.OwnerReplaced, ownership.OwnerUnknown:
			// The object's own reference names it; the user can read that.
			cov.OwnerRef = namedOwner(owner)
		}
		if owner.Resolution.State == ownership.OwnerUnknown {
			cov.OwnerReason = string(owner.Resolution.Gap.Reason)
		}
	}
	return cov, owner, true
}

// notChecked lists the kinds a search for children couldn't read, once per
// kind and reason. A search across a caller's namespaces reports a kind still
// loading in each of them, which would otherwise list the kind once per
// namespace: a user with a hundred namespaces opening a Node would get it a
// hundred times.
func notChecked(gaps []ownership.Gap) []OwnershipGap {
	type key struct{ group, kind, reason string }
	namespaces := map[key][]string{}
	var order []key
	for _, g := range gaps {
		k := key{group: g.Kind.Group, kind: g.Kind.Kind, reason: string(g.Reason)}
		if _, seen := namespaces[k]; !seen {
			order = append(order, k)
		}
		namespaces[k] = append(namespaces[k], g.Namespace)
	}
	var out []OwnershipGap
	for _, k := range order {
		g := OwnershipGap{Group: k.group, Kind: k.kind, Reason: k.reason}
		if ns := namespaces[k]; len(ns) == 1 {
			g.Namespace = ns[0]
		}
		out = append(out, g)
	}
	return out
}

// namedOwners are the coordinates o's owner references name, found or not,
// at either scope: a manages edge to one is ownership, if only of an earlier
// incarnation or through a version the API no longer serves.
func namedOwners(o ownership.Observation) map[resourceid.Ref]bool {
	named := map[resourceid.Ref]bool{}
	for _, r := range o.OwnerReferences {
		gv, err := schema.ParseGroupVersion(r.APIVersion)
		if err != nil || r.Kind == "" || r.Name == "" {
			continue
		}
		named[resourceid.Ref{Group: gv.Group, Kind: r.Kind, Namespace: o.Ref.Namespace, Name: r.Name}] = true
		named[resourceid.Ref{Group: gv.Group, Kind: r.Kind, Name: r.Name}] = true
	}
	return named
}

// present reports whether the caller reads ref as it exists now.
func present(v *ownership.View, ref resourceid.Ref) bool {
	subject, _ := v.References(ref)
	return subject.Present()
}

// settled reports whether a controller walk is known to its end: it reached
// an object without a controller reference, or a Node. A walk that stopped at
// a reference it couldn't follow, looped or gave up may continue past where
// it stopped, as the graph's edges may: one drawn through an owner that has
// since gone, or that no longer resolves, is still a shortcut.
func settled(chain ownership.ControllerChainAnswer) bool {
	return chain.Subject.Present() && chain.UnfollowedReference == nil && !chain.Cycle && !chain.Truncated
}

// namedOwner is the owner o's reference names, as the reference names it:
// with its namespace only when its kind's scope is known.
func namedOwner(owner ownership.OwnerAnswer) *ResourceRef {
	if owner.Resolution.Ref.Kind != "" {
		ref := refOf(owner.Resolution.Ref)
		return &ref
	}
	r := owner.Reference
	gv, err := schema.ParseGroupVersion(r.APIVersion)
	if err != nil || r.Kind == "" || r.Name == "" {
		return nil
	}
	return &ResourceRef{Group: gv.Group, Kind: r.Kind, Name: r.Name}
}

// OwnershipUnavailable records that Radar couldn't answer rel's ownership,
// with a readreason code saying why. No answer from manages edges stands in.
func OwnershipUnavailable(rel *Relationships, reason string) {
	if rel != nil {
		clearOwnership(rel).UnavailableReason = reason
	}
}

func clearOwnership(rel *Relationships) *OwnershipCoverage {
	rel.Owner, rel.Owners, rel.Deployment, rel.Children, rel.OlderRevisions, rel.Managers, rel.Manages = nil, nil, nil, nil, nil, nil, nil
	rel.Ownership = &OwnershipCoverage{}
	return rel.Ownership
}

// deploymentOf is the Deployment controlling a Pod's ReplicaSet, read up
// their controller references: a reference that isn't a controller's makes
// neither the ReplicaSet nor its Deployment the Pod's workload.
func deploymentOf(v *ownership.View, subject resourceid.Ref) *ResourceRef {
	if subject.GroupKind() != (resourceid.GroupKind{Kind: "Pod"}) {
		return nil
	}
	chain := v.ControllerChain(subject).Chain
	if len(chain) < 3 || chain[1].GroupKind() != (resourceid.GroupKind{Group: "apps", Kind: "ReplicaSet"}) ||
		chain[2].GroupKind() != (resourceid.GroupKind{Group: "apps", Kind: "Deployment"}) {
		return nil
	}
	ref := refOf(chain[2])
	return &ref
}

func refOf(r resourceid.Ref) ResourceRef {
	return ResourceRef{Kind: r.Kind, Group: r.Group, Namespace: r.Namespace, Name: r.Name}
}

// refIdentity is the identity of a ResourceRef built from a topology node,
// whose group is empty for the core group.
func refIdentity(r ResourceRef) resourceid.Ref {
	return resourceid.Ref{Group: r.Group, Kind: r.Kind, Namespace: r.Namespace, Name: r.Name}
}
