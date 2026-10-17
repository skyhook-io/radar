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

// search collects one OwnerCandidates call's candidates and gaps.
type search struct {
	uid        string
	domain     ownership.Domain
	namespaces []string
	found      []candidate
	gaps       []ownership.Gap
}

func (s *session) OwnerCandidates(uid types.UID, domain ownership.Domain, limit int) ownership.CandidateRead {
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
	if searches {
		for _, gk := range s.catalog.AmbiguousKinds() {
			q.gap(gk, "", readreason.ReadError)
		}
	}
	slices.SortFunc(q.found, func(a, b candidate) int { return compareRefs(a.ref, b.ref) })
	r := ownership.CandidateRead{Gaps: q.gaps}
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

// gap records that kind in namespace went unread; "" stands for the whole kind
// on an all-namespaces search, or the cluster for a cluster-scoped kind.
func (q *search) gap(gk resourceid.GroupKind, namespace string, reason readreason.Code) {
	q.gaps = append(q.gaps, ownership.Gap{Kind: gk, Namespace: namespace, Reason: reason})
}

// gapEverywhere records that a kind went unread across the whole search.
func (q *search) gapEverywhere(gk resourceid.GroupKind, namespaced bool, reason readreason.Code) {
	if !namespaced || q.domain.AllNamespaces {
		q.gap(gk, "", reason)
		return
	}
	for _, ns := range q.namespaces {
		q.gap(gk, ns, reason)
	}
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
		q.gapEverywhere(gk, b.Namespaced, readreason.KindNotWatched)
		return
	}
	read := a.Read
	if a.AllNamespaces && target == nil {
		// One informer holds the whole kind; Read is unset for it.
		switch a.Readiness {
		case k8score.KindReady:
		case k8score.KindFailed:
			q.gap(gk, "", readreason.KindSyncFailed)
			return
		default:
			q.gap(gk, "", readreason.KindSyncPending)
			return
		}
	} else {
		for _, ns := range a.Loading {
			q.gap(gk, ns, readreason.KindSyncPending)
		}
		for _, ns := range a.Failed {
			q.gap(gk, ns, readreason.KindSyncFailed)
		}
		for _, ns := range a.Uncovered {
			q.gap(gk, ns, readreason.CollectorScopeLimited)
		}
		if target == nil {
			// The collector holds only some namespaces; the rest are unread.
			q.gap(gk, "", readreason.CollectorScopeLimited)
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

func (s *session) dynamicCandidates(q *search, kind ownership.Kind) {
	gk := kind.GroupKind
	if s.caches.Dynamic == nil {
		q.gapEverywhere(gk, kind.Namespaced, readreason.KindNotWatched)
		return
	}
	versions := s.servingVersions(kind)
	if reason, unwatched := s.unwatchedReason(kind, versions); unwatched {
		q.gapEverywhere(gk, kind.Namespaced, reason)
		return
	}
	// read searches the informers serving namespace at every watched version
	// that can answer. Versions that disagree leave the namespace unread: one
	// of their watches hasn't caught up.
	read := func(namespace string) (readreason.Code, bool) {
		var found []*unstructured.Unstructured
		var at schema.GroupVersionResource
		answered := false
		for _, gvr := range versions {
			objs, server, ok := s.caches.Dynamic.ReadableDependents(gvr, namespace, q.uid)
			if !ok {
				continue
			}
			s.record(gvr, namespace, server)
			if !answered {
				found, at, answered = objs, gvr, true
			} else if !sameIncarnations(found, objs) {
				return readreason.KindSyncPending, false
			}
		}
		if !answered {
			return dynamicReason(s.caches.Dynamic.NamespaceObservation(versions[0], namespace)), false
		}
		for _, u := range found {
			q.add(gk, at.GroupVersion().String(), u, u.GetNamespace(), u.GetName())
		}
		return "", true
	}
	if kind.Namespaced && !q.domain.AllNamespaces {
		for _, ns := range q.namespaces {
			if reason, ok := read(ns); !ok {
				q.gap(gk, ns, reason)
			}
		}
		return
	}
	reason, ok := read("")
	if ok {
		return
	}
	namespaces, byNamespace := s.namespaceWatches(versions[0])
	if !kind.Namespaced || !byNamespace {
		q.gap(gk, "", reason)
		return
	}
	// Watched only namespace by namespace: read those, and the rest are unread.
	for _, ns := range namespaces {
		if reason, ok := read(ns); !ok {
			q.gap(gk, ns, reason)
		}
	}
	q.gap(gk, "", readreason.CollectorScopeLimited)
}

// unwatchedReason reports a kind none of whose served versions is watched,
// and why, as of the session's first look: most kinds a cluster serves aren't
// watched, and every search would otherwise ask again.
func (s *session) unwatchedReason(kind ownership.Kind, versions []schema.GroupVersionResource) (readreason.Code, bool) {
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

// sameIncarnations reports whether two stores returned the same objects.
func sameIncarnations(a, b []*unstructured.Unstructured) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]types.UID, len(a))
	for _, u := range a {
		seen[u.GetNamespace()+"/"+u.GetName()] = u.GetUID()
	}
	for _, u := range b {
		if uid, ok := seen[u.GetNamespace()+"/"+u.GetName()]; !ok || uid != u.GetUID() {
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
