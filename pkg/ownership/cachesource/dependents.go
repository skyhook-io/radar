package cachesource

import (
	"cmp"
	"slices"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// candidate is a cached object an owner index returned, not yet copied.
type candidate struct {
	ref resourceid.Ref
	obj apiruntime.Object
}

// search collects one DependentCandidates call's candidates and gaps.
type search struct {
	uid        string
	domain     ownership.SearchScope
	namespaces []string
	found      []candidate
	gaps       []ownership.Gap
}

func (s *session) DependentCandidates(uid types.UID, domain ownership.SearchScope) ownership.DependentCandidatesRead {
	q := &search{uid: string(uid), domain: domain}
	if !domain.AllNamespaces {
		for _, ns := range slices.Compact(slices.Sorted(slices.Values(domain.Namespaces))) {
			if ns == "" {
				// A store read for "" would answer for every namespace.
				q.gaps = append(q.gaps, ownership.Gap{Reason: readreason.NamespaceRequired})
				continue
			}
			q.namespaces = append(q.namespaces, ns)
		}
	}
	searches := domain.Searches(true) || domain.Searches(false)
	if !s.catalog.Complete() && searches {
		q.gaps = append(q.gaps, ownership.Gap{Reason: readreason.ReadError})
	}
	for _, kind := range s.catalog.DependentKinds() {
		if !domain.Searches(kind.Namespaced) {
			continue
		}
		if b, typed := typedKinds[kind.GroupKind]; typed {
			s.typedCandidates(q, b)
		} else {
			s.dynamicCandidates(q, kind)
		}
	}
	for _, a := range s.catalog.AmbiguousDependentKinds() {
		if a.Namespaced && domain.Searches(true) || a.ClusterScoped && domain.Searches(false) {
			q.gap(a.GroupKind, "", readreason.ReadError)
		}
	}
	slices.SortFunc(q.found, func(a, b candidate) int { return compareRefs(a.ref, b.ref) })
	r := ownership.DependentCandidatesRead{Gaps: q.gaps}
	for _, c := range q.found {
		if read := observe(c.ref, c.obj); read.State == ownership.Present {
			r.Candidates = append(r.Candidates, read.Object)
		} else {
			r.Gaps = append(r.Gaps, read.Gap)
		}
	}
	slices.SortFunc(r.Gaps, compareGaps)
	r.Gaps = slices.Compact(r.Gaps)
	return r
}

// gap records that kind in namespace went unread; "" stands for the kind
// wherever the search looked, or the cluster for a cluster-scoped kind.
func (q *search) gap(gk resourceid.GroupKind, namespace string, reason readreason.Code) {
	q.gaps = append(q.gaps, ownership.Gap{Kind: gk, Namespace: namespace, Reason: reason})
}

// gapEverywhere records that a kind went unread wherever the search looked,
// for a reason of the whole kind, such as Radar not collecting it: one gap,
// however many namespaces the search covers.
func (q *search) gapEverywhere(gk resourceid.GroupKind, namespaced bool, reason readreason.Code) {
	if namespaced && !q.domain.AllNamespaces && len(q.namespaces) == 0 {
		// The domain named no namespace to look in.
		return
	}
	q.gap(gk, "", reason)
}

func (q *search) add(gk resourceid.GroupKind, obj apiruntime.Object, namespace, name string) {
	q.found = append(q.found, candidate{
		ref: resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: namespace, Name: name},
		obj: obj,
	})
}

func (s *session) typedCandidates(q *search, b resourceid.Builtin) {
	gk := b.GroupKind()
	var target []string
	if b.Namespaced && !q.domain.AllNamespaces {
		target = append([]string{}, q.namespaces...)
	}
	a := s.caches.Typed.AssessKindRead(b.Resource, target)
	if a.Readiness == k8score.KindUnavailable {
		q.gapEverywhere(gk, b.Namespaced, readreason.RadarKindNotCollected)
		return
	}
	read := a.Read
	if a.AllNamespaces {
		// One informer holds the whole kind, so its state is the kind's in
		// every namespace the search names: one gap says so.
		switch a.Readiness {
		case k8score.KindReady:
		case k8score.KindFailed:
			q.gapEverywhere(gk, b.Namespaced, readreason.KindSyncTimedOut)
			return
		default:
			q.gapEverywhere(gk, b.Namespaced, readreason.KindSyncPending)
			return
		}
	} else {
		for _, ns := range a.Loading {
			q.gap(gk, ns, readreason.KindSyncPending)
		}
		for _, ns := range a.Failed {
			q.gap(gk, ns, readreason.KindSyncTimedOut)
		}
		for _, ns := range a.Uncovered {
			q.gap(gk, ns, readreason.RadarNamespaceNotCollected)
		}
		if target == nil {
			// The collector holds only some namespaces; the rest are unread.
			q.gap(gk, "", readreason.RadarNamespaceNotCollected)
		}
		if len(read) == 0 {
			return
		}
	}
	objs, ok := s.caches.Typed.ReadableDependents(b.Resource, read, q.uid)
	if !ok {
		q.gapEverywhere(gk, b.Namespaced, readreason.ReadError)
		return
	}
	for _, obj := range objs {
		if m, err := apimeta.Accessor(obj); err == nil {
			q.add(gk, obj, m.GetNamespace(), m.GetName())
		}
	}
}

func (s *session) dynamicCandidates(q *search, kind ownership.CatalogKind) {
	s.dynamicIndexed(q, kind, k8score.OwnerUIDIndex, []string{q.uid}, func(u *unstructured.Unstructured) {
		q.add(kind.GroupKind, u, u.GetNamespace(), u.GetName())
	})
}

// dynamicIndexed reads the objects index files under any of keys for a
// dynamic kind across q's domain, each store once for all the keys, handing
// each object found to found and recording a gap for each part of the domain
// it can't read. A resource watched at several versions answers from one:
// the most stable version whose cluster-wide store has loaded, or else,
// namespace by namespace, the most stable version that answers there. Every
// version receives the same objects, so another can only be a moment behind
// or ahead of it.
func (s *session) dynamicIndexed(q *search, kind ownership.CatalogKind, index string, keys []string, found func(*unstructured.Unstructured)) {
	gk := kind.GroupKind
	if s.caches.Dynamic == nil {
		q.gapEverywhere(gk, kind.Namespaced, readreason.RadarKindNotCollected)
		return
	}
	versions := s.servingVersions(kind)
	if reason, unwatched := s.unwatchedReason(kind, versions); unwatched {
		q.gapEverywhere(gk, kind.Namespaced, reason)
		return
	}
	// A cluster-wide store serves every namespace, so it is read once and its
	// matches kept by namespace: reading it per namespace would gather all of
	// them for each.
	var inDomain map[string]bool
	if kind.Namespaced && !q.domain.AllNamespaces {
		inDomain = make(map[string]bool, len(q.namespaces))
		for _, ns := range q.namespaces {
			inDomain[ns] = true
		}
	}
	for _, gvr := range versions {
		objs, ok := s.caches.Dynamic.ReadableIndexed(gvr, "", index, keys...)
		if !ok {
			continue
		}
		for _, u := range objs {
			if inDomain == nil || inDomain[u.GetNamespace()] {
				found(u)
			}
		}
		return
	}
	if !kind.Namespaced {
		q.gap(gk, "", s.unansweredReason(versions, ""))
		return
	}
	var watchedByNamespace []string
	for _, gvr := range versions {
		if watched, byNamespace := s.namespaceWatches(gvr); byNamespace {
			watchedByNamespace = append(watchedByNamespace, watched...)
		}
	}
	if len(watchedByNamespace) == 0 {
		// Each version's one informer serves every namespace and none can
		// answer, so the kind's state is the same in each: one gap.
		q.gapEverywhere(gk, true, s.unansweredReason(versions, ""))
		return
	}
	namespaces := q.namespaces
	if q.domain.AllNamespaces {
		slices.Sort(watchedByNamespace)
		namespaces = slices.Compact(watchedByNamespace)
	}
	for _, ns := range namespaces {
		s.readNamespace(q, gk, versions, ns, index, keys, found)
	}
	if q.domain.AllNamespaces {
		// The namespaces not watched one by one are unread: still loading if
		// a version is watched cluster-wide, otherwise uncollected.
		rest := readreason.RadarNamespaceNotCollected
		for _, gvr := range versions {
			if _, byNamespace := s.namespaceWatches(gvr); !byNamespace {
				rest = dynamicReason(s.caches.Dynamic.NamespaceObservation(gvr, ""))
			}
		}
		q.gap(gk, "", rest)
	}
}

// readNamespace reads namespace from the most stable version that answers
// there, or records why none did.
func (s *session) readNamespace(q *search, gk resourceid.GroupKind, versions []schema.GroupVersionResource, namespace, index string, keys []string, found func(*unstructured.Unstructured)) {
	for _, gvr := range versions {
		if objs, ok := s.caches.Dynamic.ReadableIndexed(gvr, namespace, index, keys...); ok {
			for _, u := range objs {
				found(u)
			}
			return
		}
	}
	q.gap(gk, namespace, s.unansweredReason(versions, namespace))
}

// unwatchedReason reports a kind none of whose served versions is watched,
// and why, as of the session's first look: most kinds a cluster serves aren't
// watched, and every search would otherwise ask again.
func (s *session) unwatchedReason(kind ownership.CatalogKind, versions []schema.GroupVersionResource) (readreason.Code, bool) {
	gr := schema.GroupResource{Group: kind.Group, Resource: kind.Resource}
	if reason, seen := s.unwatched[gr]; seen {
		return reason, reason != ""
	}
	var reason readreason.Code
	if !slices.ContainsFunc(s.watched[gr], func(gvr schema.GroupVersionResource) bool { return gvr == versions[0] }) {
		reason = dynamicReason(s.caches.Dynamic.NamespaceObservation(versions[0], ""))
	}
	if s.unwatched == nil {
		s.unwatched = map[schema.GroupResource]readreason.Code{}
	}
	s.unwatched[gr] = reason
	return reason, reason != ""
}

// namespaceWatches returns the namespaces gvr is watched in when it is
// watched namespace by namespace, as of the session's first look, so a watch
// started mid-session can't widen one answer and not another.
func (s *session) namespaceWatches(gvr schema.GroupVersionResource) ([]string, bool) {
	if w, seen := s.byNamespace[gvr]; seen {
		return w, w != nil
	}
	o := s.caches.Dynamic.NamespaceObservation(gvr, "")
	var namespaces []string
	if o.Scope == k8score.DynamicObservationScopeExplicitNamespaces {
		namespaces = append([]string{}, o.Namespaces...)
	}
	if s.byNamespace == nil {
		s.byNamespace = map[schema.GroupVersionResource][]string{}
	}
	s.byNamespace[gvr] = namespaces
	return namespaces, namespaces != nil
}

func compareRefs(a, b resourceid.Ref) int {
	return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
}

func compareGaps(a, b ownership.Gap) int {
	return cmp.Or(cmp.Compare(a.Kind.Group, b.Kind.Group), cmp.Compare(a.Kind.Kind, b.Kind.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Reason, b.Reason))
}
