// Package resourceapi defines the coverage metadata Radar's resource reads
// share across REST, MCP and search: what an answer is about, what Radar read
// of it, and why anything in it is missing.
package resourceapi

import (
	"cmp"
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
type Narrowing string

const (
	// NarrowedByCallerAccess: the namespaces the caller may read.
	NarrowedByCallerAccess Narrowing = "caller_access"
	// NarrowedByNamespacePin: Radar's --namespace-scope pin.
	NarrowedByNamespacePin Narrowing = "namespace_pin"
	// NarrowedByNamespaceSelection: the caller's saved namespace selection.
	NarrowedByNamespaceSelection Narrowing = "namespace_selection"
	// NarrowedByCollection: what Radar collects — the namespaces it can list a
	// kind in when its identity can't list it cluster-wide, and on a broad
	// search also the kinds it doesn't watch, which a search skips without
	// listing each one. A namespace the request names that Radar doesn't
	// collect is a gap, not a narrowing.
	NarrowedByCollection Narrowing = "collection"
)

// NameSet is a known, finite set of names. Names, when present, is all of
// them, sorted; it is absent when there are more than the response's cap, and
// then only Count says how many. A reader learns membership from Names or not
// at all.
type NameSet struct {
	Count int      `json:"count"`
	Names []string `json:"names,omitzero"`
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
	if s == nil {
		return nil
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
	Count int       `json:"count"`
	Names []KindRef `json:"names,omitzero"`
}

func Kinds(kinds []KindRef, limit int) *KindSet {
	kinds = slices.Clone(kinds)
	slices.SortFunc(kinds, func(a, b KindRef) int {
		return cmp.Or(strings.Compare(a.Kind, b.Kind), strings.Compare(a.Group, b.Group))
	})
	kinds = slices.Compact(kinds)
	return &KindSet{Count: len(kinds), Names: listed(kinds, len(kinds), limit)}
}

func (s *KindSet) Capped(limit int) *KindSet {
	if s == nil {
		return nil
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

// ObjectSet is a NameSet of objects.
type ObjectSet struct {
	Count int         `json:"count"`
	Names []ObjectRef `json:"names,omitzero"`
}

// Objects builds an ObjectSet of count members. known holds the ones the
// caller collected, which may be fewer than count; the set lists them only
// when they are all of it.
func Objects(known []ObjectRef, count, limit int) *ObjectSet {
	count = max(count, len(known))
	return &ObjectSet{Count: count, Names: listed(known, count, limit)}
}

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

type Scope struct {
	Type       ScopeType `json:"type"`
	Namespaces *NameSet  `json:"namespaces,omitempty"`
}

// Gap is part of the scope Radar did not read, and why. It covers every
// combination of its present dimensions: kinds × namespaces, or the objects
// named. An absent dimension is not narrowed — the gap may reach any kind or
// namespace of the answer, to an unknown extent — never "none".
type Gap struct {
	Code       readreason.Code `json:"code"`
	Kinds      *KindSet        `json:"kinds,omitempty"`
	Namespaces *NameSet        `json:"namespaces,omitempty"`
	Objects    *ObjectSet      `json:"objects,omitempty"`
}

// Coverage qualifies a single-kind read: a list, or the per-kind entries of a
// count. Status is relative to Scope. An explicit target (namespaces the
// request named) stays the scope, and what of it could not be read is a gap;
// an implicitly broad request's scope is what remained after NarrowedBy, so
// "complete" there means complete within that narrowed scope only. It does
// not assert continuous watch health.
//
// Read is what Radar read within Scope. Every successful answer has it; on an
// error it is absent: the error returns no rows, though Radar may have read
// some and discarded them (its connection changed mid-read, or the answer was
// over its size budget).
type Coverage struct {
	Status     CoverageStatus `json:"status"`
	Scope      Scope          `json:"scope"`
	NarrowedBy []Narrowing    `json:"narrowedBy,omitempty"`
	Read       *Scope         `json:"read,omitempty"`
	Gaps       []Gap          `json:"gaps"`
}

// ReadError is the body of a failed resource read on every surface: REST
// responses, and MCP results (structuredContent and the text block). A read
// fails when Radar read nothing in its scope, when the answer would be too
// large, or when its connection changed mid-read. Coverage is present when
// Radar assessed the scope; it keeps every reason, including the ones
// ErrorCode does not name.
type ReadError struct {
	Error     string          `json:"error"`
	ErrorCode readreason.Code `json:"error_code"`
	Coverage  *Coverage       `json:"coverage,omitempty"`
}
