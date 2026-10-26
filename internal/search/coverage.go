package search

import (
	"cmp"
	"context"
	"errors"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/readplan"
	"github.com/skyhook-io/radar/pkg/k8score"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

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
// never grows with the number of kinds on the cluster. A gap lists either
// kinds or namespaces and gives the other axis as an extent, so folding never
// invents a listed combination: a kind gap reaches all namespaces, or an
// unknown part of them when a kind failed only in some.
type coverage struct {
	namedKinds      bool
	namedNamespaces map[string]bool
	searchedKinds   int
	namespaceKinds  map[readreason.Code]map[string]int
	// Kinds are scanned serially; deduplication needs to retain only the current kind.
	namespaceKindSeen map[readreason.Code]map[string]bool
	kinds             map[readreason.Code][]resourceapi.KindRef
	kindSeen          map[coverageKey]bool
	// someNamespaces marks reasons recorded for a kind in only some
	// namespaces.
	someNamespaces map[readreason.Code]bool
	nsGaps         map[readreason.Code][]string
	namespaceSeen  map[readreason.Code]map[string]bool
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
		namespaceKinds:  map[readreason.Code]map[string]int{},
		kinds:           map[readreason.Code][]resourceapi.KindRef{},
		kindSeen:        map[coverageKey]bool{},
		someNamespaces:  map[readreason.Code]bool{},
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

// kind records a kind-level reason for one scanned kind, in every namespace
// searched.
func (c *coverage) kind(kind, group string, reason readreason.Code) {
	c.kindIn(kind, group, reason, "")
}

// kindGap preserves whether a reason reaches only part of the searched scope.
func (c *coverage) kindGap(kind, group string, reason readreason.Code, someNamespaces bool) {
	c.kind(kind, group, reason)
	if someNamespaces {
		c.someNamespaces[reason] = true
	}
}

func partOfScope(plan readplan.Plan, reason readreason.Code) bool {
	return plan.CollectionLimited || slices.ContainsFunc(plan.Partitions, func(p readplan.Partition) bool {
		return p.Extent == readplan.UnknownNamespaces || p.Verdict.State != readplan.Gap || p.Verdict.Code != reason
	})
}

// kindIn records a kind-level reason for one scanned kind in namespace ns,
// "" for every namespace searched.
func (c *coverage) kindIn(kind, group string, reason readreason.Code, ns string) {
	if c.quiet(reason) {
		return
	}
	if ns != "" {
		c.someNamespaces[reason] = true
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

func (c *coverage) kindNamespaces(reason readreason.Code, namespaces ...string) {
	c.namespaces(reason, namespaces...)
	for _, ns := range namespaces {
		if !c.namedNamespaces[ns] || c.namespaceKindSeen[reason][ns] {
			continue
		}
		if c.namespaceKinds[reason] == nil {
			c.namespaceKinds[reason] = map[string]int{}
		}
		if c.namespaceKindSeen[reason] == nil {
			c.namespaceKindSeen[reason] = map[string]bool{}
		}
		c.namespaceKindSeen[reason][ns] = true
		c.namespaceKinds[reason][ns]++
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
		where := resourceapi.AllNames()
		if c.someNamespaces[reason] {
			where = resourceapi.UnknownNames()
		}
		out = append(out, resourceapi.Gap{Code: reason, Kinds: resourceapi.Kinds(kinds, limit), Namespaces: where})
	}
	for reason, namespaces := range c.nsGaps {
		kinds := resourceapi.AllKinds()
		for _, ns := range namespaces {
			if affected := c.namespaceKinds[reason][ns]; affected > 0 && affected != c.searchedKinds {
				kinds = resourceapi.UnknownKinds()
				break
			}
		}
		out = append(out, resourceapi.Gap{Code: reason, Kinds: kinds, Namespaces: resourceapi.Names(namespaces, len(namespaces))})
	}
	for reason := range c.unbounded {
		out = append(out, resourceapi.Gap{Code: reason, Kinds: resourceapi.UnknownKinds(), Namespaces: resourceapi.AllNames()})
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

// sortGaps orders gaps by readreason precedence, then gaps listing kinds
// before gaps listing namespaces before an unknown set of kinds, so a
// response is stable for equal input.
func sortGaps(gaps []resourceapi.Gap) {
	dimension := func(g resourceapi.Gap) int {
		switch {
		case g.Kinds != nil && g.Kinds.Extent == "":
			return 0
		case g.Namespaces != nil && g.Namespaces.Extent == "":
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

// errIncompleteDiscovery marks a dynamic catalog missing the API groups whose
// discovery failed.
var errIncompleteDiscovery = errors.New("some API groups could not be discovered")

// ErrConnectionChanged ends a search whose caches were replaced while it ran:
// its hits could mix two clusters.
var ErrConnectionChanged = errors.New("Radar's cluster connection changed during this search (a context switch or reconnect); confirm which cluster Radar is connected to, then retry")

// listErrorReason keeps warm-up budget exhaustion pending: retrying search
// can finish collection without changing Radar's setup.
func listErrorReason(err error) readreason.Code {
	if code, ok := readplan.CollectorErrorCode(err); ok {
		switch code {
		case readreason.RadarCredentialsRejected, readreason.RadarForbidden, readreason.KindSyncPending:
			return code
		}
	}
	if strings.HasPrefix(err.Error(), "forbidden:") {
		return readreason.RadarForbidden
	}
	if errors.Is(err, k8s.ErrUnknownKind) {
		return readreason.KindNotWatchable
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) || k8score.IsClientRateLimited(err) {
		return readreason.KindSyncPending
	}
	return readreason.ReadError
}
