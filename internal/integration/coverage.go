package integration

import (
	"sort"
)

// Coverage states for one kind a workspace lists.
const (
	KindCoverageFull    = "full"
	KindCoveragePartial = "partial"
	KindCoverageDenied  = "denied"
	// kindCoverageUncached: the caller may list the kind, but Radar's informer
	// holds none of the Namespaces in scope, so nothing was read.
	KindCoverageUncached     = "uncached"
	KindCoverageNotInstalled = "notInstalled"
	KindCoverageSyncing      = "syncing"
	KindCoverageError        = "error"
)

// KindCoverage states how much of one kind the caller could see.
// DeniedNamespaces (the caller may not list there) and UncachedNamespaces
// (the caller may, but Radar's informer does not hold them) list only
// Namespaces already in the caller's scope, so either may be omitted on a
// partial State; AllowedNamespaces is always set on a partial State and is
// the authority for which Namespaces were read.
type KindCoverage struct {
	State              string   `json:"state"`
	DeniedNamespaces   []string `json:"deniedNamespaces,omitempty"`
	UncachedNamespaces []string `json:"uncachedNamespaces,omitempty"`
	AllowedNamespaces  []string `json:"allowedNamespaces,omitempty"`
}

// kindAccess is the resolved read scope for one kind. All means every
// namespace in the request's scope (or the cluster-scoped kind itself).
// Denied and Uncached name the in-scope Namespaces left unread, under the
// disclosure rule of listScope.
type KindAccess struct {
	State      string
	All        bool
	Namespaces map[string]bool
	Denied     []string
	Uncached   []string
}

func (a KindAccess) Covers(namespace string) bool {
	if a.State != KindCoverageFull && a.State != KindCoveragePartial {
		return false
	}
	return a.All || a.Namespaces[namespace]
}

func (a KindAccess) Coverage() KindCoverage {
	cov := KindCoverage{State: a.State, DeniedNamespaces: a.Denied, UncachedNamespaces: a.Uncached}
	if a.State == KindCoveragePartial {
		cov.AllowedNamespaces = make([]string, 0, len(a.Namespaces))
		for ns := range a.Namespaces {
			cov.AllowedNamespaces = append(cov.AllowedNamespaces, ns)
		}
		sort.Strings(cov.AllowedNamespaces)
	}
	return cov
}

func AccessFromScope(allowed []string, partial bool) KindAccess {
	acc := KindAccess{State: KindCoverageFull, All: allowed == nil}
	if partial {
		acc.State = KindCoveragePartial
	}
	if allowed != nil {
		acc.Namespaces = make(map[string]bool, len(allowed))
		for _, ns := range allowed {
			acc.Namespaces[ns] = true
		}
	}
	return acc
}
