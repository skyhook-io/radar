// Package resourceapi defines the coverage metadata Radar's resource reads
// share across REST, MCP and search: what an answer is about, what Radar read
// of it, and why anything in it is missing. Coverage qualifies a read of one
// kind, SearchCoverage a search across kinds; both report gaps with the same
// Gap and narrowing with the same Narrowing.
package resourceapi

import (
	"cmp"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/pkg/readreason"
)

type CoverageStatus string

const (
	// CoverageComplete: Radar read all of the answer's scope.
	CoverageComplete CoverageStatus = "complete"
	// CoveragePartial: at least one gap; rows prove nothing about the gaps.
	CoveragePartial CoverageStatus = "partial"
)

// The sets below list their members only when there are at most a
// response's cap of them: a REST response is rendered by Radar's UI, an MCP
// response is read by a model. Above the cap a set carries only its count.
const (
	RESTSetLimit = 64
	MCPSetLimit  = 16
)

type ScopeType string

const (
	ScopeCluster       ScopeType = "cluster"
	ScopeAllNamespaces ScopeType = "all_namespaces"
	ScopeNamespaces    ScopeType = "namespaces"
)

// Narrowing says why an implicitly broad request covers less than everything.
// Narrowing applies per dimension: namespaces the request names are never
// narrowed, nor are kinds a search names, and what of them Radar could not
// read is a gap. The values are a separate enum from
// readreason.Action even where the spelling matches: narrowedBy user_access
// says the answer covers only what the user may read, the action user_access
// that the user's access has to change.
type Narrowing string

const (
	// NarrowedByUserAccess: the namespaces the user may read.
	NarrowedByUserAccess Narrowing = "user_access"
	// NarrowedByNamespacePin: Radar's --namespace-scope pin.
	NarrowedByNamespacePin Narrowing = "namespace_pin"
	// NarrowedByNamespaceSelection: a namespace selection the server applied
	// to an implicitly broad request. The picker's stored selection is not
	// one: data reads never inherit it, and the UI sends it as named
	// namespaces.
	NarrowedByNamespaceSelection Narrowing = "namespace_selection"
	// NarrowedByNamespaceCollection: the namespaces Radar collects a kind in,
	// when its identity can't list the kind cluster-wide. Whoever runs Radar
	// widens it.
	NarrowedByNamespaceCollection Narrowing = "namespace_collection"
	// NarrowedByKindCollection: the kinds Radar collects. A search that names
	// no kind skips the kinds Radar isn't watching without listing each one;
	// naming a kind Radar can watch but hasn't started watching searches it.
	NarrowedByKindCollection Narrowing = "kind_collection"
)

// Extent says how much of the answer's scope a gap reaches along one axis
// when the gap can't list it.
type Extent string

const (
	// ExtentAll: every member of the scope along that axis.
	ExtentAll Extent = "all"
	// ExtentUnknown: some members, and Radar can't say which.
	ExtentUnknown Extent = "unknown"
)

// NameSet is a set of names. Either it is listed: Count says exactly how many
// members there are, and Names, when present, is all of them, sorted (absent
// when there are more than the response can carry, at any size; a reader
// learns membership from Names or not at all). Or Extent says the gap reaches
// all of the scope, or an unknown part of it, and carries no count.
type NameSet struct {
	Count  int      `json:"count"`
	Names  []string `json:"names,omitzero"`
	Extent Extent   `json:"extent,omitempty"`
}

// AllNames is a gap's namespaces when it reaches every namespace in scope.
func AllNames() *NameSet { return &NameSet{Extent: ExtentAll} }

// UnknownNames is a gap's namespaces when it reaches some, Radar can't say
// which.
func UnknownNames() *NameSet { return &NameSet{Extent: ExtentUnknown} }

// MarshalJSON writes an extent set as {"extent": …} alone: it has no count.
func (s NameSet) MarshalJSON() ([]byte, error) {
	if s.Extent != "" {
		return json.Marshal(struct {
			Extent Extent `json:"extent"`
		}{s.Extent})
	}
	type wire NameSet
	return json.Marshal(wire(s))
}

// Names builds a NameSet from names, which must already be safe to show the
// caller. It does not modify names.
func Names(names []string, limit int) *NameSet {
	names = slices.Clone(names)
	slices.Sort(names)
	names = slices.Compact(names)
	return &NameSet{Count: len(names), Names: listed(names, len(names), limit)}
}

// Capped is the set as a response with a smaller cap would carry it.
func (s *NameSet) Capped(limit int) *NameSet {
	if s == nil || s.Extent != "" {
		return s
	}
	return &NameSet{Count: s.Count, Names: listed(s.Names, s.Count, limit)}
}

// KindRef names a kind. An omitted group is the core group.
type KindRef struct {
	Kind  string `json:"kind"`
	Group string `json:"group,omitempty"`
}

// KindSet is a NameSet of kinds.
type KindSet struct {
	Count  int       `json:"count"`
	Names  []KindRef `json:"names,omitzero"`
	Extent Extent    `json:"extent,omitempty"`
}

// AllKinds is a gap's kinds when it reaches every kind in scope.
func AllKinds() *KindSet { return &KindSet{Extent: ExtentAll} }

// UnknownKinds is a gap's kinds when it reaches some, Radar can't say which:
// the catalog of kinds itself couldn't be read.
func UnknownKinds() *KindSet { return &KindSet{Extent: ExtentUnknown} }

// MarshalJSON writes an extent set as {"extent": …} alone, as NameSet does.
func (s KindSet) MarshalJSON() ([]byte, error) {
	if s.Extent != "" {
		return json.Marshal(struct {
			Extent Extent `json:"extent"`
		}{s.Extent})
	}
	type wire KindSet
	return json.Marshal(wire(s))
}

// Kinds builds a KindSet from kinds, as Names does from names: sorted by kind
// then group, without duplicates, listed only up to limit.
func Kinds(kinds []KindRef, limit int) *KindSet {
	kinds = slices.Clone(kinds)
	slices.SortFunc(kinds, func(a, b KindRef) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Group, b.Group))
	})
	kinds = slices.Compact(kinds)
	return &KindSet{Count: len(kinds), Names: listed(kinds, len(kinds), limit)}
}

// Capped is the set as a response with a smaller cap would carry it.
func (s *KindSet) Capped(limit int) *KindSet {
	if s == nil || s.Extent != "" {
		return s
	}
	return &KindSet{Count: s.Count, Names: listed(s.Names, s.Count, limit)}
}

// ObjectRef names one object. An omitted group is the core group; an omitted
// namespace is a cluster-scoped object.
type ObjectRef struct {
	Kind      string `json:"kind"`
	Group     string `json:"group,omitempty"`
	Namespace string `json:"namespace,omitempty"`
	Name      string `json:"name"`
}

// ObjectSet is a listed set of objects, as NameSet without the extent form.
type ObjectSet struct {
	Count int         `json:"count"`
	Names []ObjectRef `json:"names,omitzero"`
}

// Objects builds an ObjectSet of count members (at least as many as known
// holds). known holds the ones the caller collected, which may be fewer; the
// set lists them, sorted, only when they are all of it.
func Objects(known []ObjectRef, count, limit int) *ObjectSet {
	known = slices.Clone(known)
	slices.SortFunc(known, func(a, b ObjectRef) int {
		return cmp.Or(strings.Compare(a.Namespace, b.Namespace), strings.Compare(a.Kind, b.Kind),
			strings.Compare(a.Group, b.Group), strings.Compare(a.Name, b.Name))
	})
	known = slices.Compact(known)
	count = max(count, len(known))
	return &ObjectSet{Count: count, Names: listed(known, count, limit)}
}

// Capped is the set as a response with a smaller cap would carry it.
func (s *ObjectSet) Capped(limit int) *ObjectSet {
	if s == nil {
		return nil
	}
	return &ObjectSet{Count: s.Count, Names: listed(s.Names, s.Count, limit)}
}

// listed is items as a set of count members lists them: all of them, or
// nothing when they are not all known or there are more than limit.
func listed[T any](items []T, count, limit int) []T {
	if len(items) != count || count > max(0, limit) {
		return nil
	}
	return append([]T{}, items...)
}

// Scope is what an answer is about. ScopeCluster (a cluster-scoped kind) and
// ScopeAllNamespaces carry no Namespaces; ScopeNamespaces always does. On a
// successful answer Scope and Read each name at least one namespace when they
// carry a set. An error's Scope may be an empty set: the user's access
// reaches no namespace.
type Scope struct {
	Type       ScopeType `json:"type"`
	Namespaces *NameSet  `json:"namespaces,omitempty"`
}

// Gap is one reason the answer isn't complete, and where it applies. It
// covers every combination of its axes: kinds × namespaces, or the objects
// named. An axis that applies is always present, listed or as an Extent; an
// absent axis doesn't apply to the gap. Which axes apply depends on the
// surface: a list of a namespaced kind gives every gap its namespaces, a list
// of a cluster-scoped kind none, a search kinds or namespaces, and a filter
// failure only objects. Within one Coverage a namespace is in at most one gap.
//
// Action is what the code calls for, from the same table as readreason, so a
// client that doesn't know a newer code still knows what to do. Producers
// leave it empty; marshaling fills it.
type Gap struct {
	Code       readreason.Code   `json:"code"`
	Action     readreason.Action `json:"action,omitempty"`
	Kinds      *KindSet          `json:"kinds,omitempty"`
	Namespaces *NameSet          `json:"namespaces,omitempty"`
	Objects    *ObjectSet        `json:"objects,omitempty"`
}

// MarshalJSON writes the code's action beside it.
func (g Gap) MarshalJSON() ([]byte, error) {
	if g.Action == "" {
		g.Action, _ = readreason.ActionFor(g.Code)
	}
	type wire Gap
	return json.Marshal(wire(g))
}

// Coverage qualifies a single-kind read: a list, or the per-kind entries of a
// count. Status is relative to Scope: complete means Radar's initial read of
// all of it has loaded, and will keep meaning that; freshness or paging, if
// added, come as separate fields. An explicit target (namespaces the request
// named) stays the scope, and what of it could not be read is a gap; an
// implicitly broad request's scope is what remained after NarrowedBy, so
// "complete" there means complete within that narrowed scope only.
//
// Read is what Radar read within Scope, on every successful answer. Read and
// the gaps don't overlap and together make up Scope, so a complete answer's
// Read equals its Scope. An error body never has Read: it returns no rows,
// though Radar may have read some and discarded them (its connection changed
// mid-read, or the answer was over its size budget).
//
// A client meeting a value it doesn't know treats it conservatively: an
// unknown code as an incomplete answer with no remedy guessed beyond its
// Action, an unknown narrowing as a narrowed one, and an unknown scope type as
// grounds for no completeness or absence claim.
type Coverage struct {
	Status     CoverageStatus `json:"status"`
	Scope      Scope          `json:"scope"`
	NarrowedBy []Narrowing    `json:"narrowedBy,omitempty"`
	Read       *Scope         `json:"read,omitempty"`
	Gaps       []Gap          `json:"gaps"`
}

// Validate reports the first way c breaks the contract above, for producers'
// tests. It checks status and the namespace partition, not scope type values
// or a gap's kinds and objects: status agrees with the gaps; scope and read carry a set exactly when
// their type is namespaces; a namespaced scope's gaps all state namespaces
// and a cluster scope's none; listed sets are sorted, unique and as large as
// their count; gaps and read don't overlap; and, where everything is listed,
// read and the gaps make up scope.
func (c Coverage) Validate() error {
	if (c.Status == CoverageComplete) != (len(c.Gaps) == 0) || (c.Status != CoverageComplete && c.Status != CoveragePartial) {
		return fmt.Errorf("status %q with %d gaps", c.Status, len(c.Gaps))
	}
	for _, sc := range []*Scope{&c.Scope, c.Read} {
		if sc == nil {
			continue
		}
		if (sc.Type == ScopeNamespaces) != (sc.Namespaces != nil) {
			return fmt.Errorf("scope type %q with namespaces %v", sc.Type, sc.Namespaces)
		}
		if sc.Namespaces != nil && sc.Namespaces.Extent != "" {
			return fmt.Errorf("an extent belongs on a gap, not a scope: %+v", *sc.Namespaces)
		}
		if err := sc.Namespaces.validListed(); err != nil {
			return err
		}
	}
	if c.Read != nil && c.Read.Type != c.Scope.Type && !(c.Scope.Type == ScopeAllNamespaces && c.Read.Type == ScopeNamespaces) {
		return fmt.Errorf("read type %q within scope type %q", c.Read.Type, c.Scope.Type)
	}
	if c.Status == CoverageComplete && c.Read != nil && !sameScope(c.Scope, *c.Read) {
		return fmt.Errorf("a complete answer's read %+v isn't its scope %+v", *c.Read, c.Scope)
	}
	inScope := func(ns string) bool {
		return c.Scope.Namespaces == nil || c.Scope.Namespaces.Names == nil || slices.Contains(c.Scope.Namespaces.Names, ns)
	}
	wholeGap := slices.ContainsFunc(c.Gaps, func(g Gap) bool { return g.Namespaces != nil && g.Namespaces.Extent == ExtentAll })
	if wholeGap && (len(c.Gaps) > 1 || c.Read != nil) {
		return fmt.Errorf("a gap over the whole scope leaves nothing for read or another gap")
	}
	seen := map[string]bool{}
	if c.Read != nil && c.Read.Namespaces != nil {
		for _, ns := range c.Read.Namespaces.Names {
			if !inScope(ns) {
				return fmt.Errorf("read namespace %q is outside scope", ns)
			}
			seen[ns] = true
		}
	}
	listedTotal := 0
	allListed := c.Read == nil || c.Read.Type == ScopeNamespaces
	if c.Read != nil && c.Read.Namespaces != nil {
		listedTotal = c.Read.Namespaces.Count
	}
	for _, gap := range c.Gaps {
		if gap.Code == "" {
			return fmt.Errorf("gap without a code")
		}
		if gap.Objects != nil {
			if gap.Kinds != nil || gap.Namespaces != nil {
				return fmt.Errorf("gap %s: objects with kinds or namespaces", gap.Code)
			}
			continue
		}
		switch {
		case c.Scope.Type == ScopeCluster && gap.Namespaces != nil:
			return fmt.Errorf("gap %s: namespaces on a cluster-scoped answer", gap.Code)
		case c.Scope.Type != ScopeCluster && gap.Namespaces == nil:
			return fmt.Errorf("gap %s: no namespaces on a namespaced answer", gap.Code)
		}
		if gap.Namespaces == nil {
			continue
		}
		if gap.Namespaces.Extent != "" {
			if gap.Namespaces.Extent != ExtentAll && gap.Namespaces.Extent != ExtentUnknown || gap.Namespaces.Count != 0 || gap.Namespaces.Names != nil {
				return fmt.Errorf("gap %s: extent set %+v", gap.Code, *gap.Namespaces)
			}
			allListed = false
			continue
		}
		if err := gap.Namespaces.validListed(); err != nil {
			return fmt.Errorf("gap %s: %w", gap.Code, err)
		}
		listedTotal += gap.Namespaces.Count
		for _, ns := range gap.Namespaces.Names {
			if !inScope(ns) {
				return fmt.Errorf("gap %s: namespace %q is outside scope", gap.Code, ns)
			}
			if seen[ns] {
				return fmt.Errorf("namespace %q is in two of read and the gaps", ns)
			}
			seen[ns] = true
		}
	}
	if allListed && c.Read != nil && c.Scope.Namespaces != nil && listedTotal != c.Scope.Namespaces.Count {
		return fmt.Errorf("read and gaps count %d namespaces, scope %d", listedTotal, c.Scope.Namespaces.Count)
	}
	if allListed && c.Read != nil && c.Scope.Namespaces != nil && c.Scope.Namespaces.Names != nil && len(seen) == c.Scope.Namespaces.Count {
		for _, ns := range c.Scope.Namespaces.Names {
			if !seen[ns] {
				return fmt.Errorf("scope namespace %q is in neither read nor a gap", ns)
			}
		}
	}
	return nil
}

func sameScope(a, b Scope) bool {
	if a.Type != b.Type || (a.Namespaces == nil) != (b.Namespaces == nil) {
		return false
	}
	return a.Namespaces == nil || a.Namespaces.Count == b.Namespaces.Count && slices.Equal(a.Namespaces.Names, b.Namespaces.Names)
}

// validListed checks a listed set: count not negative, names (when present)
// sorted, unique and exactly count of them. A nil set or an extent set passes.
func (s *NameSet) validListed() error {
	if s == nil || s.Extent != "" {
		return nil
	}
	if s.Count < 0 || s.Names != nil && (len(s.Names) != s.Count || !slices.IsSorted(s.Names) || len(slices.Compact(slices.Clone(s.Names))) != len(s.Names)) {
		return fmt.Errorf("set %+v isn't a sorted list of its %d members", *s, s.Count)
	}
	return nil
}

// MarshalJSON writes no gaps as [], never null: consumers can rely on gaps
// being an array whichever producer built the coverage.
func (c Coverage) MarshalJSON() ([]byte, error) {
	type wire Coverage
	w := wire(c)
	if w.Gaps == nil {
		w.Gaps = []Gap{}
	}
	return json.Marshal(w)
}

// ReadError is the body of a failed resource read: REST responses send it as
// is, and MCP results (structuredContent and the text block) add `next` after
// error_code. A read fails when it returns no usable
// inventory: Radar read nothing in its scope, the answer would be too large,
// or its connection changed mid-read. A failed list's Coverage keeps every
// reason and where it applies, including the ones ErrorCode does not name, and
// never has Read; Error names the same causes in words.
type ReadError struct {
	Error     string          `json:"error"`
	ErrorCode readreason.Code `json:"error_code"`
	Coverage  *Coverage       `json:"coverage,omitempty"`
}

// SearchCoverage qualifies a search. A search reads many kinds across
// namespaces and cluster scope, so it has no single scope or read set: Status
// says whether everything the query named was searched with nothing failing,
// and Gaps what was not. A broad query searches what the user and Radar can
// read without listing each kind or namespace it skipped for access or
// collection; NarrowedBy says that happened, so "complete" never stands for
// an exhaustive negative.
type SearchCoverage struct {
	Status     CoverageStatus `json:"status"`
	NarrowedBy []Narrowing    `json:"narrowedBy,omitempty"`
	Gaps       []Gap          `json:"gaps"`
}

// MarshalJSON writes no gaps as [], never null, as Coverage does.
func (c SearchCoverage) MarshalJSON() ([]byte, error) {
	type wire SearchCoverage
	w := wire(c)
	if w.Gaps == nil {
		w.Gaps = []Gap{}
	}
	return json.Marshal(w)
}
