package ownershiptest

import (
	"cmp"
	"slices"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/gitops"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Scan is a source answering from the World by brute force. The World must
// not change while the source is in use.
func Scan(w World) ownership.Source {
	return scanSource{w}
}

type scanSource struct{ w World }

func (s scanSource) Begin() (ownership.ReadSession, error) {
	session := scanSession{w: s.w, catalog: ownership.NewCatalog(s.w.Resources, !s.w.Partial), byRef: map[resourceid.Ref]Object{}, byKind: map[resourceid.GroupKind][]Object{}}
	for _, o := range s.w.Objects {
		session.byRef[o.Ref] = o
		session.byKind[o.Ref.GroupKind()] = append(session.byKind[o.Ref.GroupKind()], o)
	}
	session.ambiguousKinds = session.findAmbiguous()
	session.dependents = session.dependentKinds()
	return session, nil
}

// scanSession answers from the World. It looks objects up by reference, and
// groups them by kind only to keep a search to the kinds it covers: which
// objects name a UID is always found by scanning their owner references.
type scanSession struct {
	w          World
	catalog    *ownership.Catalog
	byRef      map[resourceid.Ref]Object
	byKind     map[resourceid.GroupKind][]Object
	dependents []dependentKind
	// ambiguousKinds are the kinds two resources of one group serve, or that
	// versions serve at different scopes, with the scopes in which one of
	// them serves the kind with delete, list and watch.
	ambiguousKinds map[resourceid.GroupKind]ambiguity
}

func (s scanSession) Catalog() *ownership.Catalog { return s.catalog }

func (s scanSession) ValidateCaches() error { return nil }

// scope says whether discovery serves gk and whether it is namespaced.
func (s scanSession) scope(gk resourceid.GroupKind) (namespaced, served bool) {
	for _, r := range s.w.Resources {
		if r.Group == gk.Group && r.Kind == gk.Kind {
			return r.Namespaced, true
		}
	}
	return false, false
}

// ambiguity is where the garbage collector acts on an ambiguous kind.
type ambiguity struct {
	namespaced, clusterScoped bool
}

// findAmbiguous returns the kinds two resources of their group serve, or that
// versions serve at different scopes.
func (s scanSession) findAmbiguous() map[resourceid.GroupKind]ambiguity {
	first := map[resourceid.GroupKind]ownership.ServedResource{}
	ambiguous := map[resourceid.GroupKind]bool{}
	for _, r := range s.w.Resources {
		gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
		if seen, ok := first[gk]; !ok {
			first[gk] = r
		} else if seen.Resource != r.Resource || seen.Namespaced != r.Namespaced {
			ambiguous[gk] = true
		}
	}
	out := map[resourceid.GroupKind]ambiguity{}
	for gk := range ambiguous {
		out[gk] = ambiguity{}
	}
	for _, r := range s.w.Resources {
		gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
		a, ok := out[gk]
		if !ok || !slices.Contains(r.Verbs, "delete") || !slices.Contains(r.Verbs, "list") || !slices.Contains(r.Verbs, "watch") {
			continue
		}
		if r.Namespaced {
			a.namespaced = true
		} else {
			a.clusterScoped = true
		}
		out[gk] = a
	}
	return out
}

func (s scanSession) ambiguous(gk resourceid.GroupKind) bool {
	_, ok := s.ambiguousKinds[gk]
	return ok
}

// heldReason says why Radar doesn't hold a kind's objects in namespace ("" for
// the whole kind), or "" when it holds them. A namespace outside what Radar
// collects stays unread for good, so that reason wins over loading.
func heldReason(c Collection, namespaced bool, namespace string) readreason.Code {
	switch c.State {
	case NotCollected:
		return readreason.RadarKindNotCollected
	case Refused:
		if !c.OnDemand {
			// The typed cache knows only that Radar's access probe left the
			// kind off, not why.
			return readreason.RadarKindNotCollected
		}
		return readreason.RadarForbidden
	case Unwatchable:
		return readreason.KindNotWatchable
	}
	if namespaced && namespace != "" && c.Namespaces != nil && !slices.Contains(c.Namespaces, namespace) {
		if c.OnDemand {
			return readreason.RadarKindNotCollected
		}
		return readreason.RadarNamespaceNotCollected
	}
	if c.State == Loading {
		return readreason.KindSyncPending
	}
	return ""
}

func unavailable(ref resourceid.Ref, reason readreason.Code) ownership.ObjectRead {
	return ownership.ObjectRead{Gap: ownership.Gap{Kind: ref.GroupKind(), Namespace: ref.Namespace, Reason: reason}}
}

func (s scanSession) Object(ref resourceid.Ref) ownership.ObjectRead {
	if ref.Kind == "" || ref.Name == "" {
		return unavailable(ref, readreason.ReadError)
	}
	gk := ref.GroupKind()
	if gk.Kind == "Event" && (gk.Group == "" || gk.Group == "events.k8s.io") {
		return ownership.ObjectRead{State: ownership.Excluded}
	}
	namespaced, served := s.scope(gk)
	switch {
	case s.ambiguous(gk):
		return unavailable(ref, readreason.ReadError)
	case !served && s.w.Partial:
		// The group discovery missed may serve it.
		return unavailable(ref, readreason.ReadError)
	case !served:
		return unavailable(ref, readreason.KindUnknown)
	case namespaced && ref.Namespace == "":
		return unavailable(ref, readreason.NamespaceRequired)
	case !namespaced && ref.Namespace != "":
		return unavailable(ref, readreason.ReadError)
	}
	if reason := heldReason(s.w.collection(gk), namespaced, ref.Namespace); reason != "" {
		return unavailable(ref, reason)
	}
	if o, ok := s.byRef[ref]; ok {
		return ownership.ObjectRead{State: ownership.Present, Object: observation(o)}
	}
	return ownership.ObjectRead{State: ownership.Absent}
}

// dependentKind is one kind the garbage collector acts on.
type dependentKind struct {
	gk         resourceid.GroupKind
	namespaced bool
}

// dependentKinds are the kinds discovery serves with delete, list and watch
// in some version, without Events, in group and kind order.
func (s scanSession) dependentKinds() []dependentKind {
	seen := map[resourceid.GroupKind]bool{}
	var out []dependentKind
	for _, r := range s.w.Resources {
		gk := resourceid.GroupKind{Group: r.Group, Kind: r.Kind}
		gc := slices.Contains(r.Verbs, "delete") && slices.Contains(r.Verbs, "list") && slices.Contains(r.Verbs, "watch")
		event := r.Kind == "Event" && (r.Group == "" || r.Group == "events.k8s.io")
		if gc && !event && !seen[gk] && !s.ambiguous(gk) {
			seen[gk] = true
			out = append(out, dependentKind{gk: gk, namespaced: r.Namespaced})
		}
	}
	slices.SortFunc(out, func(a, b dependentKind) int {
		return cmp.Or(cmp.Compare(a.gk.Group, b.gk.Group), cmp.Compare(a.gk.Kind, b.gk.Kind))
	})
	return out
}

func (s scanSession) DependentCandidates(uid types.UID, scope ownership.SearchScope) ownership.DependentCandidatesRead {
	var r ownership.DependentCandidatesRead
	gap := func(gk resourceid.GroupKind, namespace string, reason readreason.Code) {
		r.Gaps = append(r.Gaps, ownership.Gap{Kind: gk, Namespace: namespace, Reason: reason})
	}
	searches := scope.AllNamespaces || len(scope.Namespaces) > 0 || scope.IncludeClusterScoped
	if s.w.Partial && searches {
		gap(resourceid.GroupKind{}, "", readreason.ReadError)
	}
	var namespaces []string
	if !scope.AllNamespaces {
		for _, ns := range scope.Namespaces {
			if ns == "" {
				gap(resourceid.GroupKind{}, "", readreason.NamespaceRequired)
			} else {
				namespaces = append(namespaces, ns)
			}
		}
	}
	var found []Object
	for _, k := range s.dependents {
		if k.namespaced && !scope.AllNamespaces && len(scope.Namespaces) == 0 || !k.namespaced && !scope.IncludeClusterScoped {
			continue
		}
		c := s.w.collection(k.gk)
		readable := map[string]bool{}
		all := false
		switch {
		case !k.namespaced || scope.AllNamespaces && (c.Namespaces == nil || c.State != Collected && c.State != Loading):
			if reason := heldReason(c, k.namespaced, ""); reason != "" {
				gap(k.gk, "", reason)
				continue
			}
			all = true
		case scope.AllNamespaces:
			// Collected in some namespaces only: the rest are unread.
			gap(k.gk, "", readreason.RadarNamespaceNotCollected)
			for _, ns := range c.Namespaces {
				if reason := heldReason(c, true, ns); reason != "" {
					gap(k.gk, ns, reason)
				} else {
					readable[ns] = true
				}
			}
		case c.State == NotCollected || c.State == Refused || c.State == Unwatchable || c.State == Loading && c.Namespaces == nil:
			// The whole kind's state, or that of the one store serving every
			// namespace, leaves it unread in each namespace the scope names:
			// one gap says so.
			if len(namespaces) > 0 {
				gap(k.gk, "", heldReason(c, true, ""))
			}
			continue
		default:
			for _, ns := range namespaces {
				if reason := heldReason(c, true, ns); reason != "" {
					gap(k.gk, ns, reason)
				} else {
					readable[ns] = true
				}
			}
		}
		read := func(o Object) bool { return all || readable[o.Ref.Namespace] }
		for _, o := range s.byKind[k.gk] {
			if read(o) && namesOwner(o.OwnerReferences, uid) {
				found = append(found, o)
			}
		}
	}
	for gk, a := range s.ambiguousKinds {
		event := gk.Kind == "Event" && (gk.Group == "" || gk.Group == "events.k8s.io")
		namespacedSearched := scope.AllNamespaces || len(scope.Namespaces) > 0
		if !event && (a.namespaced && namespacedSearched || a.clusterScoped && scope.IncludeClusterScoped) {
			gap(gk, "", readreason.ReadError)
		}
	}
	slices.SortFunc(found, func(a, b Object) int { return compareRefs(a.Ref, b.Ref) })
	for _, o := range found {
		r.Candidates = append(r.Candidates, observation(o))
	}
	slices.SortFunc(r.Gaps, compareGaps)
	r.Gaps = slices.Compact(r.Gaps)
	return r
}

func namesOwner(owners []metav1.OwnerReference, uid types.UID) bool {
	for _, ref := range owners {
		if ref.UID == uid && uid != "" {
			return true
		}
	}
	return false
}

// Observe returns what a source should observe of o.
func Observe(o Object) ownership.Observation {
	return observation(o)
}

func observation(o Object) ownership.Observation {
	obs := ownership.Observation{
		Ref:             o.Ref,
		UID:             o.UID,
		ResourceVersion: o.ResourceVersion,
	}
	for _, ref := range o.OwnerReferences {
		obs.OwnerReferences = append(obs.OwnerReferences, *ref.DeepCopy())
	}
	obs.Claims = gitops.Claims(o.Ref, o.Labels, o.Annotations)
	return obs
}

func compareRefs(a, b resourceid.Ref) int {
	return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
}

func compareGaps(a, b ownership.Gap) int {
	return cmp.Or(cmp.Compare(a.Kind.Group, b.Kind.Group), cmp.Compare(a.Kind.Kind, b.Kind.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Reason, b.Reason))
}

// managers are the kinds whose status lists their members, as the contract
// names them.
var managers = []resourceid.GroupKind{ownership.ArgoApplication, ownership.FluxHelmRelease, ownership.FluxKustomization}

func (s scanSession) Members(manager resourceid.Ref) ownership.MembersRead {
	if !slices.Contains(managers, manager.GroupKind()) {
		return ownership.MembersRead{Manager: unavailable(manager, readreason.ReadError)}
	}
	r := ownership.MembersRead{Manager: s.Object(manager)}
	if r.Manager.State != ownership.Present {
		return r
	}
	o := s.byRef[manager]
	r.Membership = o.Membership
	if o.Membership != ownership.MembersUnreadable {
		r.Members = append([]resourceid.Ref{}, o.Members...)
		slices.SortFunc(r.Members, compareRefs)
		r.Members = slices.Compact(r.Members)
	}
	return r
}

func (s scanSession) Membership(manager resourceid.Ref) ownership.MembersRead {
	r := s.Members(manager)
	r.Members = nil
	return r
}

// nameable says why no manager can list ref, or "" when one can: a reference
// must name an object of a kind whose scope Radar can tell, in that scope, or
// of a custom kind complete discovery doesn't serve, matched as recorded. A
// built-in kind's scope is its own, unless discovery serves the kind with the
// other scope.
func (s scanSession) nameable(ref resourceid.Ref) readreason.Code {
	if ref.Kind == "" || ref.Name == "" {
		return readreason.ReadError
	}
	gk := ref.GroupKind()
	namespaced, served := s.scope(gk)
	if b, ok := resourceid.BuiltinForKind(gk.Kind); ok && b.Group == gk.Group && !s.ambiguous(gk) {
		if served && namespaced != b.Namespaced {
			return readreason.ReadError
		}
		namespaced, served = b.Namespaced, true
	}
	switch {
	case s.ambiguous(gk), !served && s.w.Partial:
		return readreason.ReadError
	case !served:
		return ""
	case namespaced && ref.Namespace == "":
		return readreason.NamespaceRequired
	case !namespaced && ref.Namespace != "":
		return readreason.ReadError
	}
	return ""
}

func (s scanSession) RecordingManagers(ref resourceid.Ref) ownership.RecordingManagersRead {
	var r ownership.RecordingManagersRead
	if reason := s.nameable(ref); reason != "" {
		for _, gk := range managers {
			r.Gaps = append(r.Gaps, ownership.Gap{Kind: gk, Reason: reason})
		}
		slices.SortFunc(r.Gaps, compareGaps)
		return r
	}
	gap := func(gk resourceid.GroupKind, namespace string, reason readreason.Code) {
		r.Gaps = append(r.Gaps, ownership.Gap{Kind: gk, Namespace: namespace, Reason: reason})
	}
	for _, gk := range managers {
		namespaced, served := s.scope(gk)
		if !served || s.ambiguous(gk) {
			if s.w.Partial || s.ambiguous(gk) {
				gap(gk, "", readreason.ReadError)
			}
			continue
		}
		c := s.w.collection(gk)
		readable := map[string]bool{}
		all := false
		if !namespaced || c.Namespaces == nil || c.State != Collected && c.State != Loading {
			if reason := heldReason(c, namespaced, ""); reason != "" {
				gap(gk, "", reason)
				continue
			}
			all = true
		} else {
			gap(gk, "", readreason.RadarNamespaceNotCollected)
			for _, ns := range c.Namespaces {
				if reason := heldReason(c, true, ns); reason != "" {
					gap(gk, ns, reason)
				} else {
					readable[ns] = true
				}
			}
		}
		for _, o := range s.byKind[gk] {
			if !all && !readable[o.Ref.Namespace] {
				continue
			}
			switch {
			case o.Membership == ownership.MembersUnreadable:
				r.Unreadable = append(r.Unreadable, o.Ref)
			case !slices.Contains(o.Members, ref):
			case o.Membership == ownership.MembersRecorded:
				r.Managers = append(r.Managers, observation(o))
			case o.Membership == ownership.MembersRemote:
				r.Remote = append(r.Remote, o.Ref)
			}
		}
	}
	slices.SortFunc(r.Managers, func(a, b ownership.Observation) int { return compareRefs(a.Ref, b.Ref) })
	slices.SortFunc(r.Remote, compareRefs)
	slices.SortFunc(r.Unreadable, compareRefs)
	slices.SortFunc(r.Gaps, compareGaps)
	r.Gaps = slices.Compact(r.Gaps)
	return r
}
