package search

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	apierrors "k8s.io/apimachinery/pkg/api/errors"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// NamespacedSearchKinds lists the typed kinds that need the caller's exact
// list permission, not namespace visibility alone — LimitRanges as on the
// resource list and get routes.
var NamespacedSearchKinds = []struct{ Kind, Group, Resource string }{
	{"Secret", "", "secrets"},
	{"Role", "rbac.authorization.k8s.io", "roles"},
	{"RoleBinding", "rbac.authorization.k8s.io", "rolebindings"},
	{"LimitRange", "", "limitranges"},
}

// reasonClass groups coverage reasons by how a search reports them.
type reasonClass int

const (
	// classScope covers the caller's access, Radar's setup and query feedback.
	// They describe what a search covers rather than what went wrong, so they
	// are reported as gaps only for a kind or namespace the query names; on a
	// broad query they narrow the search instead.
	classScope reasonClass = iota
	// classTransient is data still loading; a retry can change the answer.
	classTransient
	// classFailure is a read or check that failed for a kind the caller may read.
	classFailure
)

func classify(reason readreason.Code) reasonClass {
	switch reason {
	case readreason.KindSyncPending:
		return classTransient
	case readreason.KindSyncTimedOut, readreason.ReadError, readreason.UserPermissionCheckFailed, readreason.RadarCredentialsRejected:
		return classFailure
	}
	return classScope
}

// narrowingFor is what a scope reason a broad query does not report narrowed.
// What Radar collects narrows by kind when the reason is about a kind (one
// Radar isn't watching, which naming it would search), and by namespace when
// it is about a namespace.
func narrowingFor(reason readreason.Code, byKind bool) resourceapi.Narrowing {
	switch reason {
	case readreason.UserForbidden, readreason.UserNoNamespaceAccess, readreason.UserNamespaceNotVisible:
		return resourceapi.NarrowedByUserAccess
	case readreason.OutsideNamespacePin:
		return resourceapi.NarrowedByNamespacePin
	case readreason.OutsideNamespaceSelection:
		return resourceapi.NarrowedByNamespaceSelection
	}
	if byKind {
		return resourceapi.NarrowedByKindCollection
	}
	return resourceapi.NarrowedByNamespaceCollection
}

// maxNamedFailures bounds the kinds one reason names on a query with no kind:
// terms; past it the reason only counts them.
const maxNamedFailures = 5

// maxFilterFailedObjects bounds the objects a filter_eval_failed gap names.
const maxFilterFailedObjects = 20

// typedNamespaceReader is a Provider that can say which namespaces of a typed
// kind are readable while others still load, and whether Radar collects the
// kind in only part of them.
type typedNamespaceReader interface {
	TypedReadable(kind string, namespaces []string) []string
	TypedCollectionLimited(kind string, namespaces []string) bool
}

// unlistableKindProvider is a Provider that can name the discovered APIs
// that can't be listed at all, which search therefore never scans.
type unlistableKindProvider interface {
	UnlistableKinds() []k8score.APIResource
}

// readableWithin narrows a kind's list scope (nil: every namespace) to the
// namespaces readable now; with no readable namespaces it is empty.
func readableWithin(listNs, readable []string) []string {
	if listNs == nil {
		return readable
	}
	return slices.DeleteFunc(slices.Clone(listNs), func(ns string) bool { return !slices.Contains(readable, ns) })
}

// coverage collects one search's gaps and applies the reporting rules: scope
// reasons only for what the query names (on a broad query they narrow the
// search instead), every transient and failure reason, folded so the response
// never grows with the number of kinds on the cluster. A gap names either
// kinds or namespaces, never both, so folding never invents a combination.
type coverage struct {
	namedKinds      bool
	namedNamespaces map[string]bool
	kinds           map[readreason.Code][]resourceapi.KindRef
	kindSeen        map[coverageKey]bool
	nsGaps          map[readreason.Code][]string
	namespaceSeen   map[readreason.Code]map[string]bool
	// unbounded marks reasons that reach an unknown set of kinds (the catalog
	// itself could not be read).
	unbounded map[readreason.Code]bool
	narrowed  map[resourceapi.Narrowing]bool
}

type coverageKey struct {
	kind, group string
	reason      readreason.Code
}

func newCoverage(q Query) *coverage {
	namespaces := map[string]bool{}
	for _, ns := range q.NSFilter {
		namespaces[ns] = true
	}
	return &coverage{
		namedKinds:      len(q.KindFilter) > 0,
		namedNamespaces: namespaces,
		kinds:           map[readreason.Code][]resourceapi.KindRef{},
		kindSeen:        map[coverageKey]bool{},
		nsGaps:          map[readreason.Code][]string{},
		namespaceSeen:   map[readreason.Code]map[string]bool{},
		unbounded:       map[readreason.Code]bool{},
		narrowed:        map[resourceapi.Narrowing]bool{},
	}
}

// quiet reports whether a kind-level reason goes unreported for this query,
// so callers can skip work that could only produce it. A quiet reason still
// narrowed the search, and is recorded as such — except a kind search never
// covers (list-only APIs, and kinds Radar reads directly such as Endpoints and
// Leases): those are outside every search by definition, so they would narrow
// every one.
func (c *coverage) quiet(reason readreason.Code) bool {
	if classify(reason) != classScope || c.namedKinds {
		return false
	}
	if reason != readreason.KindNotWatchable && reason != readreason.KindNotSearchable {
		c.narrowed[narrowingFor(reason, true)] = true
	}
	return true
}

// kind records a kind-level reason for one scanned kind.
func (c *coverage) kind(kind, group string, reason readreason.Code) {
	if c.quiet(reason) {
		return
	}
	key := coverageKey{kind, group, reason}
	if c.kindSeen[key] {
		return
	}
	c.kindSeen[key] = true
	c.kinds[reason] = append(c.kinds[reason], resourceapi.KindRef{Kind: kind, Group: group})
}

// catalog records a reason that applies to an unknown set of kinds, such as
// discovery not being ready.
func (c *coverage) catalog(reason readreason.Code) {
	if c.quiet(reason) {
		return
	}
	c.unbounded[reason] = true
}

// narrow records that a scope reason narrowed the search without a gap.
func (c *coverage) narrow(reason readreason.Code) {
	c.narrowed[narrowingFor(reason, false)] = true
}

// namespaces records a namespace-level reason. Namespaces the query names are
// reported, merged into one gap per reason; any other namespace ("" for every
// namespace) narrowed the search.
func (c *coverage) namespaces(reason readreason.Code, namespaces ...string) {
	for _, ns := range namespaces {
		if !c.namedNamespaces[ns] {
			c.narrow(reason)
			continue
		}
		if c.namespaceSeen[reason][ns] {
			continue
		}
		if c.namespaceSeen[reason] == nil {
			c.namespaceSeen[reason] = map[string]bool{}
		}
		c.namespaceSeen[reason][ns] = true
		c.nsGaps[reason] = append(c.nsGaps[reason], ns)
	}
}

// result renders the gaps, most actionable first. On a query that names kinds
// every kind-level gap names all of them. Otherwise loading kinds are only
// counted — at startup they can be most of the catalog — and each failure
// reason names its kinds, by kind then group, when there are at most
// maxNamedFailures, and otherwise only counts them. A reason that already reaches an unknown set of kinds needs no
// count of loading kinds beside it.
func (c *coverage) result() []resourceapi.Gap {
	out := []resourceapi.Gap{}
	for reason, kinds := range c.kinds {
		transient := classify(reason) == classTransient && !c.namedKinds
		if transient && c.unbounded[reason] {
			continue
		}
		limit := maxNamedFailures
		switch {
		case c.namedKinds:
			limit = len(kinds)
		case transient:
			limit = 0
		}
		out = append(out, resourceapi.Gap{Code: reason, Kinds: resourceapi.Kinds(kinds, limit)})
	}
	for reason, namespaces := range c.nsGaps {
		out = append(out, resourceapi.Gap{Code: reason, Namespaces: resourceapi.Names(namespaces, len(namespaces))})
	}
	for reason := range c.unbounded {
		out = append(out, resourceapi.Gap{Code: reason})
	}
	sortGaps(out)
	return out
}

// narrowedBy is how a broad query's scope narrowed beyond what it reports.
func (c *coverage) narrowedBy(namespaces []resourceapi.Narrowing) []resourceapi.Narrowing {
	for _, n := range namespaces {
		c.narrowed[n] = true
	}
	out := make([]resourceapi.Narrowing, 0, len(c.narrowed))
	for n := range c.narrowed {
		out = append(out, n)
	}
	slices.Sort(out)
	if len(out) == 0 {
		return nil
	}
	return out
}

// sortGaps orders gaps by readreason precedence, then kinds before namespaces
// before an unknown extent, so a response is stable for equal input.
func sortGaps(gaps []resourceapi.Gap) {
	dimension := func(g resourceapi.Gap) int {
		switch {
		case g.Kinds != nil:
			return 0
		case g.Namespaces != nil:
			return 1
		default:
			return 2
		}
	}
	slices.SortStableFunc(gaps, func(a, b resourceapi.Gap) int {
		if a.Code != b.Code {
			if readreason.Primary([]readreason.Code{a.Code, b.Code}) == a.Code {
				return -1
			}
			return 1
		}
		return cmp.Compare(dimension(a), dimension(b))
	})
}

// ScopeNamespaces sets the namespace fields of o for q. visible is what the
// caller may search after any namespace selection (a saved pick or the
// --namespace-scope pin): nil means every namespace, empty means none. ceiling
// is the caller's RBAC visibility without that selection. A namespace an ns:
// term names but visible leaves out is the caller's RBAC when ceiling excludes
// it too, and the selection otherwise.
func (o *Options) ScopeNamespaces(q Query, visible, ceiling []string) {
	o.Namespaces = visible
	o.ExcludedNamespaces = nil
	o.SelectionExcludedNamespaces = nil
	o.seesAllNamespaces = ceiling == nil
	if len(q.NSFilter) > 0 {
		o.Namespaces = make([]string, 0, len(q.NSFilter))
		for _, ns := range q.NSFilter {
			into := &o.ExcludedNamespaces
			switch {
			case visible == nil || slices.Contains(visible, ns):
				into = &o.Namespaces
			case ceiling == nil || slices.Contains(ceiling, ns):
				into = &o.SelectionExcludedNamespaces
			}
			if !slices.Contains(*into, ns) {
				*into = append(*into, ns)
			}
		}
	}
	o.NamespaceExcluded = o.Namespaces != nil && len(o.Namespaces) == 0
	// Every narrowing that applied to a search that named no namespace, as REST
	// and MCP lists report it: the caller's access when it is restricted, and
	// the pin, or else a selection that leaves namespaces out.
	o.namespaceNarrowing = nil
	if len(q.NSFilter) == 0 {
		if ceiling != nil {
			o.namespaceNarrowing = append(o.namespaceNarrowing, resourceapi.NarrowedByUserAccess)
		}
		switch {
		case o.Pinned:
			o.namespaceNarrowing = append(o.namespaceNarrowing, resourceapi.NarrowedByNamespacePin)
		case visible != nil && (ceiling == nil || len(visible) < len(ceiling)):
			o.namespaceNarrowing = append(o.namespaceNarrowing, resourceapi.NarrowedByNamespaceSelection)
		}
	}
}

// excludeMissingNamespaces reports a namespace an ns: term names that doesn't
// exist on the cluster as namespace_not_found, instead of searching it to a
// silent empty answer or blaming the namespace selection. Only for a caller
// whose RBAC covers every namespace: for anyone else, which namespaces exist
// is not theirs to learn, and a missing namespace stays namespace_not_visible.
func (o *Options) excludeMissingNamespaces(p Provider, q Query) {
	if len(q.NSFilter) == 0 || !o.seesAllNamespaces {
		return
	}
	split := func(namespaces []string) (kept []string) {
		if namespaces == nil {
			return nil
		}
		kept = make([]string, 0, len(namespaces))
		for _, ns := range namespaces {
			if exists, known := p.NamespaceExists(ns); known && !exists {
				o.MissingNamespaces = append(o.MissingNamespaces, ns)
			} else {
				kept = append(kept, ns)
			}
		}
		return kept
	}
	o.Namespaces = split(o.Namespaces)
	o.SelectionExcludedNamespaces = split(o.SelectionExcludedNamespaces)
	o.NamespaceExcluded = o.Namespaces != nil && len(o.Namespaces) == 0
}

// errIncompleteDiscovery marks a dynamic catalog missing the API groups whose
// discovery failed.
var errIncompleteDiscovery = errors.New("some API groups could not be discovered")

// ErrConnectionChanged ends a search whose caches were replaced while it ran:
// its hits could mix two clusters.
var ErrConnectionChanged = errors.New("Radar's cluster connection changed during this search (a context switch or reconnect); confirm which cluster Radar is connected to, then retry")

// listErrorReason classifies a failed collector list or warm-up. The typed
// cache reports a probe-disabled kind as a "forbidden:" error, and
// ErrUnknownKind marks a kind Radar reads directly instead of watching. A 401
// stays collector_forbidden: the probes and retained observations behind most
// collector denials do not keep 401 apart from 403.
func listErrorReason(err error) readreason.Code {
	if apierrors.IsForbidden(err) || apierrors.IsUnauthorized(err) || strings.HasPrefix(err.Error(), "forbidden:") {
		return readreason.RadarForbidden
	}
	if errors.Is(err, k8s.ErrUnknownKind) {
		return readreason.KindNotWatchable
	}
	if errors.Is(err, k8s.ErrDynamicNotReady) || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || k8score.IsClientRateLimited(err) {
		return readreason.KindSyncPending
	}
	return readreason.ReadError
}

func dynamicObservationReason(observation k8score.DynamicResourceObservation) readreason.Code {
	if observation.ProbeRetrying {
		return readreason.KindSyncPending
	}
	if len(observation.UnansweredNamespaces) > 0 || observation.ReasonCode == "scope_probe_incomplete" {
		return readreason.ReadError
	}
	switch observation.State {
	case k8score.DynamicObservationSynced:
		return ""
	case k8score.DynamicObservationDenied:
		if observation.ReasonCode == "credentials_rejected" {
			return readreason.RadarCredentialsRejected
		}
		return readreason.RadarForbidden
	case k8score.DynamicObservationSyncing:
		if observation.ReasonCode == "sync_stalled" {
			return readreason.KindSyncTimedOut
		}
		return readreason.KindSyncPending
	case k8score.DynamicObservationDeferred:
		switch observation.ReasonCode {
		case "eager_watch_pending":
			return readreason.KindSyncPending
		case "scope_probe_failed":
			// Radar's access check failed for a reason that isn't a
			// refusal: not a limit of what it collects.
			return readreason.ReadError
		}
		return readreason.RadarKindNotCollected
	case k8score.DynamicObservationUnsupported:
		if observation.ReasonCode == "direct_read_only" {
			// Radar reads these directly instead of watching them, so
			// list_resources reads them; only search can't.
			return readreason.KindNotSearchable
		}
		return readreason.KindNotWatchable
	default:
		return readreason.RadarKindNotCollected
	}
}
