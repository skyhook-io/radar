// Package cachesource serves ownership reads from Radar's informer caches.
package cachesource

import (
	"slices"
	"sync/atomic"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/ownership"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceid"
)

// Caches are the caches one cluster connection reads through. Typed is
// required; without Dynamic or Discovery the kinds they serve are unreadable.
type Caches struct {
	Typed     *k8score.ResourceCache
	Dynamic   *k8score.DynamicResourceCache
	Discovery *k8score.ResourceDiscovery
}

// retired reports whether a cache has stopped following the cluster.
func (c Caches) retired() bool {
	return c.Typed.Stopped() || c.Dynamic.Stopped()
}

// Source reads from the caches current returns. It calls current when a
// session begins and again when it validates: what current returns must change
// only when the host replaces its caches.
type Source struct {
	current func() Caches
	catalog atomic.Pointer[catalogEntry]
}

type catalogEntry struct {
	discovery  *k8score.ResourceDiscovery
	generation uint64
	catalog    *ownership.Catalog
}

// New returns a source reading the caches current returns.
func New(current func() Caches) *Source {
	return &Source{current: current}
}

// Begin opens a session over the current caches.
func (s *Source) Begin() (ownership.Session, error) {
	caches := s.current()
	if caches.Typed == nil || caches.retired() {
		return nil, ownership.ErrNotConnected
	}
	return &session{caches: caches, current: s.current, catalog: s.catalogFor(caches.Discovery)}, nil
}

// catalogFor returns the catalog of discovery's current generation, building
// it once per generation.
func (s *Source) catalogFor(discovery *k8score.ResourceDiscovery) *ownership.Catalog {
	if e := s.catalog.Load(); e != nil && e.discovery == discovery && e.generation == discovery.Generation() {
		return e.catalog
	}
	snapshot := discovery.Catalog()
	served := make([]ownership.ServedResource, 0, len(snapshot.Resources))
	for _, r := range snapshot.Resources {
		served = append(served, ownership.ServedResource{
			Group: r.Group, Version: r.Version, Kind: r.Kind, Resource: r.Name, Namespaced: r.Namespaced, Verbs: r.Verbs,
		})
	}
	catalog := ownership.NewCatalog(served, snapshot.Generation > 0 && !snapshot.Partial)
	s.catalog.Store(&catalogEntry{discovery: discovery, generation: snapshot.Generation, catalog: catalog})
	return catalog
}

// typedKinds are the kinds the typed cache holds, keyed to the built-in that
// names their informer and the version it reads them at.
var typedKinds = func() map[resourceid.GroupKind]resourceid.Builtin {
	out := map[resourceid.GroupKind]resourceid.Builtin{}
	for _, kl := range k8score.AllKindListers() {
		if b, ok := resourceid.BuiltinForKind(kl.Kind()); ok && b.Group == kl.Group() {
			out[b.GroupKind()] = b
		}
	}
	return out
}()

type session struct {
	caches  Caches
	current func() Caches
	catalog *ownership.Catalog
	// watched holds the versions each dynamically watched resource is watched
	// at, most stable first, as of the session's first dynamic read.
	watched map[schema.GroupResource][]schema.GroupVersionResource
	// served records the informers that first answered each resource and
	// namespace the session read dynamically.
	served map[servedKey]k8score.DynamicReadServer
}

type servedKey struct {
	gvr       schema.GroupVersionResource
	namespace string
}

func (s *session) Catalog() *ownership.Catalog {
	return s.catalog
}

func (s *session) Object(ref resourceid.Ref) ownership.ObjectRead {
	if ref.Kind == "" || ref.Name == "" {
		return unavailable(ref, readreason.ReadError)
	}
	if b, ok := typedKinds[ref.GroupKind()]; ok {
		return s.typedObject(ref, b)
	}
	return s.dynamicObject(ref)
}

func (s *session) typedObject(ref resourceid.Ref, b resourceid.Builtin) ownership.ObjectRead {
	if reason, wrong := scopeMismatch(ref, b.Namespaced); wrong {
		return unavailable(ref, reason)
	}
	obj, found, ok := s.caches.Typed.ReadableObject(b.Resource, ref.Namespace, ref.Name)
	switch {
	case !ok:
		return unavailable(ref, typedReason(s.caches.Typed, b.Resource, ref.Namespace))
	case !found:
		return ownership.ObjectRead{State: ownership.Absent}
	}
	return observe(ref, b.APIVersion(), obj)
}

func (s *session) dynamicObject(ref resourceid.Ref) ownership.ObjectRead {
	kind, known := s.catalog.Kind(ref.GroupKind())
	if !known {
		return unavailable(ref, readreason.KindUnknown)
	}
	if reason, wrong := scopeMismatch(ref, kind.Namespaced); wrong {
		return unavailable(ref, reason)
	}
	if s.caches.Dynamic == nil {
		return unavailable(ref, readreason.KindNotWatched)
	}
	versions := s.watchedVersions(schema.GroupResource{Group: kind.Group, Resource: kind.Resource})
	for _, gvr := range versions {
		obj, server, found, ok := s.caches.Dynamic.ReadableObject(gvr, ref.Namespace, ref.Name)
		if !ok {
			continue
		}
		s.record(gvr, ref.Namespace, server)
		if !found {
			return ownership.ObjectRead{State: ownership.Absent}
		}
		return observe(ref, gvr.GroupVersion().String(), obj)
	}
	reasonFrom := schema.GroupVersionResource{Group: kind.Group, Version: mostStable(kind.Versions), Resource: kind.Resource}
	if len(versions) > 0 {
		reasonFrom = versions[0]
	}
	return unavailable(ref, dynamicReason(s.caches.Dynamic.NamespaceObservation(reasonFrom, ref.Namespace)))
}

func (s *session) watchedVersions(gr schema.GroupResource) []schema.GroupVersionResource {
	if s.watched == nil {
		s.watched = map[schema.GroupResource][]schema.GroupVersionResource{}
		for _, gvr := range s.caches.Dynamic.WatchedGVRs() {
			s.watched[gvr.GroupResource()] = append(s.watched[gvr.GroupResource()], gvr)
		}
		for _, gvrs := range s.watched {
			slices.SortFunc(gvrs, func(a, b schema.GroupVersionResource) int { return stabilityOrder(a.Version, b.Version) })
		}
	}
	return s.watched[gr]
}

func (s *session) record(gvr schema.GroupVersionResource, namespace string, server k8score.DynamicReadServer) {
	key := servedKey{gvr: gvr, namespace: namespace}
	if _, ok := s.served[key]; ok {
		return
	}
	if s.served == nil {
		s.served = map[servedKey]k8score.DynamicReadServer{}
	}
	s.served[key] = server
}

func (s *session) Validate() error {
	if now := s.current(); now != s.caches || s.caches.retired() {
		return ownership.ErrCachesReplaced
	}
	// A store replaced between two reads of one resource and namespace is no
	// longer the one first recorded, so this also catches a session whose
	// reads came from both.
	for key, server := range s.served {
		if current, _ := s.caches.Dynamic.ReadServer(key.gvr, key.namespace); !current.Same(server) {
			return ownership.ErrStoreReplaced
		}
	}
	return nil
}

// scopeMismatch reports a ref whose namespace can't hold its kind. Neither is
// absent: a namespaced kind read without a namespace has no single store to
// answer, and a cluster-scoped kind read in a namespace means the caller
// resolved scope wrongly, which an absence would hide as a dangling owner.
func scopeMismatch(ref resourceid.Ref, namespaced bool) (readreason.Code, bool) {
	switch {
	case namespaced && ref.Namespace == "":
		return readreason.NamespaceRequired, true
	case !namespaced && ref.Namespace != "":
		return readreason.ReadError, true
	}
	return "", false
}

func typedReason(rc *k8score.ResourceCache, resource, namespace string) readreason.Code {
	a := rc.AssessKindRead(resource, []string{namespace})
	switch {
	case a.Readiness == k8score.KindUnavailable:
		return readreason.KindNotWatched
	case len(a.Uncovered) > 0:
		return readreason.CollectorScopeLimited
	case len(a.Failed) > 0:
		return readreason.KindSyncFailed
	default:
		// Loading, or it finished loading after the read.
		return readreason.KindSyncPending
	}
}

func dynamicReason(o k8score.DynamicResourceObservation) readreason.Code {
	switch o.State {
	case k8score.DynamicObservationDenied:
		return readreason.CollectorForbidden
	case k8score.DynamicObservationUnsupported:
		return readreason.KindNotWatchable
	case k8score.DynamicObservationDeferred:
		switch o.ReasonCode {
		case "eager_watch_pending":
			return readreason.KindSyncPending
		case "watch_start_failed":
			return readreason.KindWatchFailed
		case "scope_probe_incomplete":
			// The probe for where Radar may watch never finished.
			return readreason.ReadError
		}
		return readreason.KindNotWatched
	case k8score.DynamicObservationSyncing:
		if o.ReasonCode == "sync_stalled" {
			return readreason.KindSyncFailed
		}
		return readreason.KindSyncPending
	case k8score.DynamicObservationSynced:
		// It finished loading after the read.
		return readreason.KindSyncPending
	default:
		return readreason.KindNotWatched
	}
}

func unavailable(ref resourceid.Ref, reason readreason.Code) ownership.ObjectRead {
	return ownership.ObjectRead{Gap: ownership.Gap{Kind: ref.GroupKind(), Namespace: ref.Namespace, Reason: reason}}
}

// observe copies what ownership needs out of a cached object, which must not
// be modified or kept.
func observe(ref resourceid.Ref, apiVersion string, obj apiruntime.Object) ownership.ObjectRead {
	m, err := apimeta.Accessor(obj)
	if err != nil {
		return unavailable(ref, readreason.ReadError)
	}
	o := ownership.Observation{
		Ref:             ref,
		APIVersion:      apiVersion,
		UID:             m.GetUID(),
		ResourceVersion: m.GetResourceVersion(),
		Finalizers:      slices.Clone(m.GetFinalizers()),
	}
	if refs := m.GetOwnerReferences(); len(refs) > 0 {
		o.OwnerReferences = make([]metav1.OwnerReference, len(refs))
		for i := range refs {
			refs[i].DeepCopyInto(&o.OwnerReferences[i])
		}
	}
	if ts := m.GetDeletionTimestamp(); ts != nil {
		o.DeletionTimestamp = ts.DeepCopy()
	}
	return ownership.ObjectRead{State: ownership.Present, Object: o}
}

func mostStable(versions []string) string {
	if len(versions) == 0 {
		return ""
	}
	return slices.MinFunc(versions, stabilityOrder)
}

// stabilityOrder sorts more stable versions first.
func stabilityOrder(a, b string) int {
	switch {
	case k8score.IsMoreStableVersion(a, b):
		return -1
	case k8score.IsMoreStableVersion(b, a):
		return 1
	}
	return 0
}
