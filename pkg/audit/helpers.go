package audit

import (
	"fmt"
	"sort"
	"strings"
)

// ResourceKey returns the index key for a resource:
// "group|Kind|namespace|name". Group goes first because both group and
// namespace can legitimately be empty independently — encoding group
// last would leave a cluster-scoped CRD key ambiguous with a
// namespaced core-group key under any 3-part parse. "|" is a safe
// delimiter — Kubernetes API groups follow DNS subdomain rules and
// can't contain it. Mirrors the same shape as the issue-source key in
// internal/summarycontext.
func ResourceKey(group, kind, namespace, name string) string {
	return fmt.Sprintf("%s|%s|%s|%s", group, kind, namespace, name)
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

// GroupForBuiltinKind maps a built-in Kubernetes Kind to the API group
// the audit suite scans it under. Returns "" for kinds the suite
// doesn't recognize — those don't get a populated Finding.Group, which
// means cross-group collision risk is bounded to the listed built-ins
// vs. third-party CRDs sharing the same Kind name.
//
// Kept here (next to ResourceKey) so the Kind→Group mapping lives in
// one place rather than every Finding{} emission site. buildResults
// populates Finding.Group via this helper before the index is built;
// per-check code stays terse and group-agnostic.
//
// Also reused by internal/issues to resolve Group on Problem-sourced
// rows that pre-date group-aware emission — keeps the (Kind→Group)
// table in one place across packages.
func GroupForBuiltinKind(kind string) string {
	switch kind {
	case "Pod", "Service", "ConfigMap", "Secret", "Node", "Namespace",
		"PersistentVolume", "PersistentVolumeClaim", "ServiceAccount":
		return ""
	case "Deployment", "DaemonSet", "StatefulSet", "ReplicaSet":
		return "apps"
	case "Job", "CronJob":
		return "batch"
	case "HorizontalPodAutoscaler":
		return "autoscaling"
	case "Ingress", "NetworkPolicy":
		return "networking.k8s.io"
	case "PodDisruptionBudget":
		return "policy"
	}
	return ""
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

	return &ScanResults{
		Summary: ScanSummary{
			Warning:    totalWarning,
			Danger:     totalDanger,
			Categories: categories,
		},
		Findings: filtered,
		Groups:   groups,
		Checks:   results.Checks,
	}
}
