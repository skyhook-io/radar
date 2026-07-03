package audit

import (
	"sort"
	"strings"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// ResourceKey re-exports the neutral identity key from pkg/resourceid (the
// canonical home) so existing audit callers keep working unchanged.
func ResourceKey(group, kind, namespace, name string) string {
	return resourceid.ResourceKey(group, kind, namespace, name)
}

// GroupForBuiltinKind re-exports the builtin Kind→group table from pkg/resourceid
// so callers resolving a finding's key (which backfills group the same way) don't
// need a second import.
func GroupForBuiltinKind(kind string) string {
	return resourceid.GroupForBuiltinKind(kind)
}

// IndexByResource builds a lookup map from ResourceKey → []Finding.
func IndexByResource(findings []Finding) map[string][]Finding {
	m := make(map[string][]Finding)
	for _, f := range findings {
		key := ResourceKey(f.Group, f.Kind, f.Namespace, f.Name)
		m[key] = append(m[key], f)
	}
	return m
}

// GroupByResource aggregates findings into per-resource groups,
// sorted by severity (most danger first), then by name.
func GroupByResource(findings []Finding) []ResourceGroup {
	index := IndexByResource(findings)

	groups := make([]ResourceGroup, 0, len(index))
	for _, fs := range index {
		g := ResourceGroup{
			Kind:      fs[0].Kind,
			Group:     fs[0].Group,
			Namespace: fs[0].Namespace,
			Name:      fs[0].Name,
			Findings:  fs,
		}
		for _, f := range fs {
			switch f.Severity {
			case SeverityDanger:
				g.Danger++
			case SeverityWarning:
				g.Warning++
			}
		}
		groups = append(groups, g)
	}

	sort.Slice(groups, func(i, j int) bool {
		if groups[i].Danger != groups[j].Danger {
			return groups[i].Danger > groups[j].Danger
		}
		if groups[i].Warning != groups[j].Warning {
			return groups[i].Warning > groups[j].Warning
		}
		return ResourceKey(groups[i].Group, groups[i].Kind, groups[i].Namespace, groups[i].Name) <
			ResourceKey(groups[j].Group, groups[j].Kind, groups[j].Namespace, groups[j].Name)
	})

	return groups
}

// ApplySettings filters audit results based on ignored namespaces (with wildcard
// patterns like *-system) and disabled checks. This is the shared implementation
// used by all consumers (HTTP handlers, MCP, skyhook-connector).
func ApplySettings(results *ScanResults, ignoredNamespaces, disabledChecks []string) *ScanResults {
	if results == nil || (len(ignoredNamespaces) == 0 && len(disabledChecks) == 0) {
		return results
	}

	ignoreNS := make(map[string]bool, len(ignoredNamespaces))
	var ignorePatterns []string
	for _, ns := range ignoredNamespaces {
		if strings.HasPrefix(ns, "*") || strings.HasSuffix(ns, "*") {
			ignorePatterns = append(ignorePatterns, ns)
		} else {
			ignoreNS[ns] = true
		}
	}
	disabled := make(map[string]bool, len(disabledChecks))
	for _, c := range disabledChecks {
		disabled[c] = true
	}

	matchesIgnoredNS := func(ns string) bool {
		if ignoreNS[ns] {
			return true
		}
		for _, p := range ignorePatterns {
			if strings.HasSuffix(p, "*") && strings.HasPrefix(ns, p[:len(p)-1]) {
				return true
			}
			if strings.HasPrefix(p, "*") && strings.HasSuffix(ns, p[1:]) {
				return true
			}
		}
		return false
	}

	var filtered []Finding
	for _, f := range results.Findings {
		if matchesIgnoredNS(f.Namespace) || disabled[f.CheckID] {
			continue
		}
		filtered = append(filtered, f)
	}

	// Rebuild groups and summary
	groups := GroupByResource(filtered)
	categories := map[string]CategorySummary{}
	for _, cat := range []string{CategorySecurity, CategoryReliability, CategoryEfficiency} {
		categories[cat] = CategorySummary{}
	}
	for _, f := range filtered {
		cs := categories[f.Category]
		switch f.Severity {
		case SeverityWarning:
			cs.Warning++
		case SeverityDanger:
			cs.Danger++
		}
		categories[f.Category] = cs
	}
	totalWarning, totalDanger := 0, 0
	for _, cs := range categories {
		totalWarning += cs.Warning
		totalDanger += cs.Danger
	}

	// Filter the evaluation denominators onto fresh maps — the input is the
	// server's shared cached scan, so mutating its maps would corrupt every
	// other consumer for the cache TTL.
	var evalByNS map[string]map[string]int
	if results.EvaluatedByNamespace != nil {
		evalByNS = make(map[string]map[string]int, len(results.EvaluatedByNamespace))
		for id, byNS := range results.EvaluatedByNamespace {
			if disabled[id] {
				continue
			}
			nsCopy := make(map[string]int, len(byNS))
			for ns, n := range byNS {
				if matchesIgnoredNS(ns) {
					continue
				}
				nsCopy[ns] = n
			}
			if len(nsCopy) > 0 {
				evalByNS[id] = nsCopy
			}
		}
	}
	checkCounts, totalPassing := deriveCheckCounts(evalByNS, filtered, categories)

	return &ScanResults{
		Summary: ScanSummary{
			Passing:    totalPassing,
			Warning:    totalWarning,
			Danger:     totalDanger,
			Categories: categories,
		},
		Findings:             filtered,
		Groups:               groups,
		Checks:               results.Checks,
		CheckCounts:          checkCounts,
		EvaluatedByNamespace: evalByNS,
		MissingInputs:        results.MissingInputs,
	}
}
