// Package cachesource serves ownership reads from Radar's informer caches.
package cachesource

import (
	"slices"

	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
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

// Source reads from the caches current returns. It calls current when a
// session begins and again when it validates. A host replaces all of its
// caches on a switch, the typed cache with them, so a new typed cache marks a
// new connection.
type Source struct {
	current func() Caches
}

// New returns a source reading the caches current returns.
func New(current func() Caches) *Source {
	return &Source{current: current}
}

// Begin opens a session over the current caches. Open one per request, not
// one per item: a session's first read of a custom kind lists every dynamic
// watch, which costs milliseconds with thousands of watched namespaces.
func (s *Source) Begin() (ownership.ReadSession, error) {
	caches := s.current()
	if caches.Typed == nil {
		return nil, ownership.ErrNotConnected
	}
	return &session{caches: caches, current: s.current, catalog: catalogOf(caches.Discovery)}, nil
}

func catalogOf(discovery *k8score.ResourceDiscovery) *ownership.Catalog {
	snapshot := discovery.Catalog()
	served := make([]ownership.ServedResource, 0, len(snapshot.Resources))
	for _, r := range snapshot.Resources {
		served = append(served, ownership.ServedResource{
			Group: r.Group, Version: r.Version, Kind: r.Kind, Resource: r.Name, Namespaced: r.Namespaced, Verbs: r.Verbs,
		})
	}
	return ownership.NewCatalog(served, snapshot.Complete)
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
	// at, as of the session's first dynamic read.
	watched map[schema.GroupResource][]schema.GroupVersionResource
	// byNamespace holds the namespaces each resource version is watched in
	// when it is watched namespace by namespace (nil otherwise).
	byNamespace map[schema.GroupVersionResource][]string
	// unwatched holds why each resource with no watched version is unread
	// ("" for a watched one).
	unwatched map[schema.GroupResource]readreason.Code
	// recordingManagersRead, set only by tests, runs after RecordingManagers reads a manager
	// kind, with the keys it read together.
	recordingManagersRead func(kind resourceid.GroupKind, keys []string)
}

// TypedCache returns the typed cache s reads from, when this package began s.
// What a caller reads through it comes from the same cluster connection as s's
// own reads, and s's ValidateCaches refuses it with them once the caches are
// replaced.
func TypedCache(s ownership.ReadSession) (*k8score.ResourceCache, bool) {
	cs, ok := s.(*session)
	if !ok || cs.caches.Typed == nil {
		return nil, false
	}
	return cs.caches.Typed, true
}

func (s *session) Catalog() *ownership.Catalog {
	return s.catalog
}

func (s *session) Object(ref resourceid.Ref) ownership.ObjectRead {
	return s.readObject(ref, nil)
}

// readObject is Object, lending the cached object it answered from, when
// present, to borrow, which must not modify or keep it.
func (s *session) readObject(ref resourceid.Ref, borrow func(*unstructured.Unstructured)) ownership.ObjectRead {
	if ref.Kind == "" || ref.Name == "" {
		return unavailable(ref, readreason.ReadError)
	}
	if ownership.IsEvent(ref.GroupKind()) {
		return ownership.ObjectRead{State: ownership.Excluded}
	}
	if b, ok := typedKinds[ref.GroupKind()]; ok {
		return s.typedObject(ref, b)
	}
	return s.dynamicObject(ref, borrow)
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
	return observe(ref, obj)
}

func (s *session) dynamicObject(ref resourceid.Ref, borrow func(*unstructured.Unstructured)) ownership.ObjectRead {
	if s.catalog.Ambiguous(ref.GroupKind()) {
		return unavailable(ref, readreason.ReadError)
	}
	kind, known := s.catalog.Kind(ref.GroupKind())
	if !known {
		if !s.catalog.Complete() {
			// The group discovery missed may serve it.
			return unavailable(ref, readreason.ReadError)
		}
		return unavailable(ref, readreason.KindUnknown)
	}
	if reason, wrong := scopeMismatch(ref, kind.Namespaced); wrong {
		return unavailable(ref, reason)
	}
	if s.caches.Dynamic == nil {
		return unavailable(ref, readreason.RadarKindNotCollected)
	}
	// A resource watched at two versions answers from the most stable one
	// that has loaded: every version receives the same objects with the same
	// owner references, so the one read can be behind another, for as long as
	// its watch lags or fails, but never contradicts it.
	versions := s.servingVersions(kind)
	for _, gvr := range versions {
		obj, found, ok := s.caches.Dynamic.ReadableObject(gvr, ref.Namespace, ref.Name)
		switch {
		case !ok:
			continue
		case !found:
			return ownership.ObjectRead{State: ownership.Absent}
		}
		read := observe(ref, obj)
		if borrow != nil && read.State == ownership.Present {
			borrow(obj)
		}
		return read
	}
	return unavailable(ref, unansweredReason(s.caches.Dynamic, versions, ref.Namespace))
}

// observer reports how Radar collects a resource version in a namespace: the
// dynamic cache, or a view of it that builds each version's scope once.
type observer interface {
	NamespaceObservation(gvr schema.GroupVersionResource, namespace string) k8score.DynamicResourceObservation
}

// unansweredReason says why none of versions answered in namespace: the first
// version watched there, which answers once it loads, or else the first.
func unansweredReason(obs observer, versions []schema.GroupVersionResource, namespace string) readreason.Code {
	var first readreason.Code
	for i, gvr := range versions {
		o := obs.NamespaceObservation(gvr, namespace)
		if o.State != k8score.DynamicObservationUnwatched {
			return dynamicReason(o)
		}
		if i == 0 {
			first = dynamicReason(o)
		}
	}
	return first
}

// servingVersions returns the versions the session reads kind's resource at,
// most stable first: the watched versions the catalog serves. With none
// watched it is the most stable served version, whose observation says why.
func (s *session) servingVersions(kind ownership.CatalogKind) []schema.GroupVersionResource {
	gr := schema.GroupResource{Group: kind.Group, Resource: kind.Resource}
	if s.watched == nil {
		s.watched = map[schema.GroupResource][]schema.GroupVersionResource{}
		for _, gvr := range s.caches.Dynamic.WatchedGVRs() {
			s.watched[gvr.GroupResource()] = append(s.watched[gvr.GroupResource()], gvr)
		}
	}
	var versions []schema.GroupVersionResource
	for _, gvr := range s.watched[gr] {
		if slices.Contains(kind.Versions, gvr.Version) {
			versions = append(versions, gvr)
		}
	}
	slices.SortFunc(versions, func(a, b schema.GroupVersionResource) int { return stabilityOrder(a.Version, b.Version) })
	if len(versions) == 0 {
		versions = []schema.GroupVersionResource{{Group: kind.Group, Version: mostStable(kind.Versions), Resource: kind.Resource}}
	}
	return versions
}

func (s *session) ValidateCaches() error {
	if s.caches.Typed != s.current().Typed {
		return ownership.ErrCachesReplaced
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
		// Radar's access probe leaves a kind off when its identity is
		// refused, when the probe can't tell, and when the kind isn't
		// installed; the cache knows only that it is off, and the host can
		// say which.
		return readreason.RadarKindNotCollected
	case len(a.Uncovered) > 0:
		return readreason.RadarNamespaceNotCollected
	case len(a.Failed) > 0:
		return readreason.KindSyncTimedOut
	default:
		// Loading, or it finished loading after the read.
		return readreason.KindSyncPending
	}
}

func dynamicReason(o k8score.DynamicResourceObservation) readreason.Code {
	switch o.State {
	case k8score.DynamicObservationDenied:
		if o.ReasonCode == "credentials_rejected" {
			return readreason.RadarCredentialsRejected
		}
		return readreason.RadarForbidden
	case k8score.DynamicObservationUnsupported:
		return readreason.KindNotWatchable
	case k8score.DynamicObservationDeferred:
		switch o.ReasonCode {
		case "eager_watch_pending":
			return readreason.KindSyncPending
		case "scope_probe_incomplete":
			// The probe for where Radar may watch never finished.
			return readreason.ReadError
		}
		return readreason.RadarKindNotCollected
	case k8score.DynamicObservationSyncing:
		if o.ReasonCode == "sync_stalled" {
			return readreason.KindSyncTimedOut
		}
		return readreason.KindSyncPending
	case k8score.DynamicObservationSynced:
		// It finished loading after the read.
		return readreason.KindSyncPending
	case k8score.DynamicObservationUnwatched:
		if o.ReasonCode == "cache_stopped" {
			// The connection was switched or closed after the session began.
			return readreason.ConnectionChanged
		}
		return readreason.RadarKindNotCollected
	default:
		return readreason.RadarKindNotCollected
	}
}

func unavailable(ref resourceid.Ref, reason readreason.Code) ownership.ObjectRead {
	return ownership.ObjectRead{Gap: ownership.Gap{Kind: ref.GroupKind(), Namespace: ref.Namespace, Reason: reason}}
}

// observe copies what ownership needs out of a cached object, which must not
// be modified or kept.
func observe(ref resourceid.Ref, obj apiruntime.Object) ownership.ObjectRead {
	m, err := apimeta.Accessor(obj)
	if err != nil {
		return unavailable(ref, readreason.ReadError)
	}
	return ownership.ObjectRead{State: ownership.Present, Object: ownership.Observe(ref, m)}
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
