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
	ref        resourceid.Ref
	apiVersion string
	obj        apiruntime.Object
}

// search collects one DependentCandidates call's candidates and gaps.
type search struct {
	uid        string
	domain     ownership.SearchScope
	namespaces []string
	found      []candidate
	gaps       []ownership.Gap
}

func (s *session) DependentCandidates(uid types.UID, domain ownership.SearchScope, limit int) ownership.DependentCandidatesRead {
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
	if limit > 0 && len(q.found) > limit {
		q.found, r.Truncated = q.found[:limit], true
	}
	for _, c := range q.found {
		if read := observe(c.ref, c.apiVersion, c.obj); read.State == ownership.Present {
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

func (q *search) add(gk resourceid.GroupKind, apiVersion string, obj apiruntime.Object, namespace, name string) {
	q.found = append(q.found, candidate{
		ref:        resourceid.Ref{Group: gk.Group, Kind: gk.Kind, Namespace: namespace, Name: name},
		apiVersion: apiVersion,
		obj:        obj,
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
			q.add(gk, b.APIVersion(), obj, m.GetNamespace(), m.GetName())
		}
	}
}

func (s *session) dynamicCandidates(q *search, kind ownership.CatalogKind) {
	s.dynamicIndexed(q, kind, k8score.OwnerUIDIndex, []string{q.uid}, func(gvr schema.GroupVersionResource, u *unstructured.Unstructured) {
		q.add(kind.GroupKind, gvr.GroupVersion().String(), u, u.GetNamespace(), u.GetName())
	})
}

// dynamicIndexed reads the objects index files under any of keys for a
// dynamic kind across q's domain, each store once for all the keys, handing
// each object found to found and recording a gap for each part of the domain
// it can't read. Every watched version the catalog serves is read, and
// versions that disagree leave their namespace unread: one of their watches
// hasn't caught up.
func (s *session) dynamicIndexed(q *search, kind ownership.CatalogKind, index string, keys []string, found func(schema.GroupVersionResource, *unstructured.Unstructured)) {
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
	// A version's cluster-wide informer serves every namespace, so it is read
	// once per search and its matches split by namespace: reading it per
	// namespace would gather all its matches once for each.
	type clusterWideRead struct {
		all         []*unstructured.Unstructured
		byNamespace map[string][]*unstructured.Unstructured
	}
	clusterWide := map[schema.GroupVersionResource]*clusterWideRead{}
	readVersion := func(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, bool) {
		cw, seen := clusterWide[gvr]
		if !seen {
			if objs, ok := s.caches.Dynamic.ReadableIndexed(gvr, "", index, keys...); ok {
				cw = &clusterWideRead{all: objs}
			}
			clusterWide[gvr] = cw
		}
		if cw != nil {
			if namespace == "" {
				return cw.all, true
			}
			if cw.byNamespace == nil {
				cw.byNamespace = map[string][]*unstructured.Unstructured{}
				for _, u := range cw.all {
					cw.byNamespace[u.GetNamespace()] = append(cw.byNamespace[u.GetNamespace()], u)
				}
			}
			return cw.byNamespace[namespace], true
		}
		if namespace == "" {
			return nil, false
		}
		return s.caches.Dynamic.ReadableIndexed(gvr, namespace, index, keys...)
	}
	// read searches the informers serving namespace at every watched version
	// that can answer, and says which versions answered.
	read := func(namespace string) (objs []*unstructured.Unstructured, at schema.GroupVersionResource, answered []schema.GroupVersionResource, reason readreason.Code) {
		for _, gvr := range versions {
			got, ok := readVersion(gvr, namespace)
			if !ok {
				continue
			}
			if len(answered) == 0 {
				objs, at = got, gvr
			} else if !sameIncarnations(objs, got) {
				return nil, at, nil, readreason.KindSyncPending
			}
			answered = append(answered, gvr)
		}
		if len(answered) == 0 {
			return nil, at, nil, s.unansweredReason(versions, namespace)
		}
		return objs, at, answered, ""
	}
	hand := func(objs []*unstructured.Unstructured, at schema.GroupVersionResource, skip map[string]bool) {
		for _, u := range objs {
			if !skip[u.GetNamespace()] {
				found(at, u)
			}
		}
	}
	readNamespaces := func(namespaces []string) {
		for _, ns := range namespaces {
			if objs, at, _, reason := read(ns); reason != "" {
				q.gap(gk, ns, reason)
			} else {
				hand(objs, at, nil)
			}
		}
	}
	if kind.Namespaced && !q.domain.AllNamespaces {
		clusterWideOnly := !slices.ContainsFunc(versions, func(gvr schema.GroupVersionResource) bool {
			_, byNamespace := s.namespaceWatches(gvr)
			return byNamespace
		})
		if clusterWideOnly && !slices.ContainsFunc(versions, func(gvr schema.GroupVersionResource) bool {
			_, ok := readVersion(gvr, "")
			return ok
		}) {
			// Each version's one informer serves every namespace and none can
			// answer, so the kind's state is the same in each: one gap.
			q.gapEverywhere(gk, true, s.unansweredReason(versions, ""))
			return
		}
		readNamespaces(q.namespaces)
		return
	}
	objs, at, answered, reason := read("")
	// A version watched only namespace by namespace holds evidence of its
	// own about those namespaces.
	var namespaces []string
	for _, gvr := range versions {
		if watched, byNamespace := s.namespaceWatches(gvr); byNamespace && !slices.Contains(answered, gvr) {
			namespaces = append(namespaces, watched...)
		}
	}
	slices.Sort(namespaces)
	namespaces = slices.Compact(namespaces)
	if reason == "" {
		// Its namespaces must agree with the cluster-wide answer.
		pending := map[string]bool{}
		for _, ns := range namespaces {
			if _, _, _, reason := read(ns); reason != "" {
				pending[ns] = true
				q.gap(gk, ns, reason)
			}
		}
		hand(objs, at, pending)
		return
	}
	if !kind.Namespaced || len(namespaces) == 0 {
		q.gap(gk, "", reason)
		return
	}
	// Read the namespaces watched one by one. The rest are unread: still
	// loading if a version is watched cluster-wide, otherwise uncollected.
	readNamespaces(namespaces)
	rest := readreason.RadarNamespaceNotCollected
	for _, gvr := range versions {
		if _, byNamespace := s.namespaceWatches(gvr); !byNamespace {
			rest = dynamicReason(s.caches.Dynamic.NamespaceObservation(gvr, ""))
		}
	}
	q.gap(gk, "", rest)
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

// sameIncarnations reports whether two stores returned the same objects, each
// at the same update: an object's resourceVersion is the same at every API
// version, so a difference means one watch hasn't caught up.
func sameIncarnations(a, b []*unstructured.Unstructured) bool {
	if len(a) != len(b) {
		return false
	}
	type copyOf struct {
		uid             types.UID
		resourceVersion string
	}
	seen := make(map[string]copyOf, len(a))
	for _, u := range a {
		seen[u.GetNamespace()+"/"+u.GetName()] = copyOf{u.GetUID(), u.GetResourceVersion()}
	}
	for _, u := range b {
		if c, ok := seen[u.GetNamespace()+"/"+u.GetName()]; !ok || c != (copyOf{u.GetUID(), u.GetResourceVersion()}) {
			return false
		}
	}
	return true
}

func compareRefs(a, b resourceid.Ref) int {
	return cmp.Or(cmp.Compare(a.Group, b.Group), cmp.Compare(a.Kind, b.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Name, b.Name))
}

func compareGaps(a, b ownership.Gap) int {
	return cmp.Or(cmp.Compare(a.Kind.Group, b.Kind.Group), cmp.Compare(a.Kind.Kind, b.Kind.Kind), cmp.Compare(a.Namespace, b.Namespace), cmp.Compare(a.Reason, b.Reason))
}
