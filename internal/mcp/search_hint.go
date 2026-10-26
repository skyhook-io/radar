package mcp

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"

	"github.com/skyhook-io/radar/internal/search"
	"github.com/skyhook-io/radar/pkg/readreason"
	"github.com/skyhook-io/radar/pkg/resourceapi"
)

// errSearchConnectionChanged rejects a search whose caches were replaced while
// it ran: its hits may mix two clusters, and a retry may reach a different one.
var errSearchConnectionChanged = errors.New("connection_changed: Radar's cluster connection changed during this search (a context switch or reconnect), so the result was discarded. Confirm which cluster Radar is connected to, then retry.")

// maxCoverageSentences bounds coverageHint; further reasons are only counted.
const maxCoverageSentences = 3

// hintEntry is one kind or namespace a search couldn't cover, flattened from
// the result's gaps: a named kind, or "*" for namespaces, for kinds too many
// to name (Count), or for an unknown set (Count 0). A search names every
// namespace a query asked for, so namespace gaps are always listed.
type hintEntry struct {
	Kind, Group string
	Reason      readreason.Code
	Namespaces  []string
	Count       int
	// SomeKinds: a namespace gap that missed only some of the kinds searched.
	SomeKinds           bool
	WholeNamespaceScope bool
}

func hintEntries(cov resourceapi.SearchCoverage) []hintEntry {
	var out []hintEntry
	for _, gap := range cov.Gaps {
		switch {
		case gap.Objects != nil:
			// Filter failures are explained from filter_errors.
		case gap.Kinds != nil && gap.Kinds.Extent == "" && gap.Kinds.Names == nil:
			out = append(out, hintEntry{Kind: "*", Reason: gap.Code, Count: gap.Kinds.Count})
		case gap.Kinds != nil && gap.Kinds.Extent == "":
			for _, k := range gap.Kinds.Names {
				out = append(out, hintEntry{Kind: k.Kind, Group: k.Group, Reason: gap.Code, WholeNamespaceScope: gap.Namespaces != nil && gap.Namespaces.Extent == resourceapi.ExtentAll})
			}
		case gap.Namespaces != nil && gap.Namespaces.Extent == "":
			out = append(out, hintEntry{Kind: "*", Reason: gap.Code, Namespaces: gap.Namespaces.Names, SomeKinds: gap.Kinds != nil && gap.Kinds.Extent != resourceapi.ExtentAll})
		default:
			out = append(out, hintEntry{Kind: "*", Reason: gap.Code})
		}
	}
	return out
}

// coverageTemplate turns the unsearched entries for one or more reasons into
// one sentence. Reasons sharing a template share the sentence. all is every
// entry of the result, for sentences another reason qualifies.
type coverageTemplate struct {
	reasons  []readreason.Code
	sentence func(entries, all []hintEntry) string
}

// catalogUnreadable reports a "*" read_error with no count: API discovery
// failed for some groups, so any kind could have been in them.
func catalogUnreadable(all []hintEntry) bool {
	return slices.ContainsFunc(all, func(e hintEntry) bool {
		return e.Kind == "*" && e.Reason == readreason.ReadError && e.Count == 0
	})
}

// filterErrors marks where CEL evaluation failures rank among the templates.
var filterErrors = coverageTemplate{}

// coverageTemplates is ordered most actionable first: what the agent can fix
// or retry now, then what it can only report.
var coverageTemplates = []coverageTemplate{
	{[]readreason.Code{readreason.KindUnknown}, func(e, all []hintEntry) string {
		names := joinKinds(e, "and", func(kind string) string { return "'" + kind + "'" })
		if catalogUnreadable(all) {
			return fmt.Sprintf("%s %s recognized, but some API groups couldn't be discovered: %s may be served by one of them; retry.",
				pluralWord(len(e), "Kind", "Kinds")+" "+names, pluralWord(len(e), "wasn't", "weren't"), pluralWord(len(e), "it", "they"))
		}
		if len(e) == 1 {
			return fmt.Sprintf("Kind %s isn't recognized on this cluster: it may not be installed here, or the name may be misspelled.", names)
		}
		return fmt.Sprintf("Kinds %s aren't recognized on this cluster: they may not be installed here, or the names may be misspelled.", names)
	}},
	{[]readreason.Code{readreason.KindNotSearchable}, func(e, _ []hintEntry) string {
		if len(e) == 1 {
			return fmt.Sprintf("Search doesn't cover %s; use list_resources kind=%s or get_resource.", kindPlural(e[0].Kind), e[0].Kind)
		}
		return fmt.Sprintf("Search doesn't cover %s; use list_resources or get_resource for them.", joinKinds(e, "and", kindPlural))
	}},
	{[]readreason.Code{readreason.KindNotWatchable}, func(e, _ []hintEntry) string {
		return fmt.Sprintf("Radar can't collect %s: the API doesn't support both list and watch, so search can't cover them; read them from the Kubernetes API directly.", joinKinds(e, "and", kindPlural))
	}},
	filterErrors,
	// A findings gap names report kinds; CEL failures name objects instead.
	{[]readreason.Code{readreason.FindingsScanIncomplete}, func(e, _ []hintEntry) string {
		return fmt.Sprintf("Radar ran out of time searching the findings of %s, so a CVE or package missing from the hits may be in a report it left out; narrow with kind:, ns: or image: to search them all.", joinKinds(e, "and", kindPlural))
	}},
	{[]readreason.Code{readreason.KindSyncPending}, func(e, _ []hintEntry) string {
		label, n := kindsLabel(e)
		return fmt.Sprintf("%s %s still loading; retry in a few seconds before concluding absence.", capitalize(label), pluralWord(n, "was", "were"))
	}},
	{[]readreason.Code{readreason.UserPermissionCheckFailed}, func(e, _ []hintEntry) string {
		label, _ := kindsLabel(e)
		return fmt.Sprintf("Radar couldn't check your access to %s; retry.", label)
	}},
	{[]readreason.Code{readreason.KindSyncTimedOut, readreason.ReadError, readreason.RadarCredentialsRejected}, func(e, all []hintEntry) string {
		if slices.ContainsFunc(all, func(u hintEntry) bool { return u.Reason == readreason.KindUnknown }) {
			// The unknown-kind sentence already explains undiscovered groups.
			e = slices.DeleteFunc(slices.Clone(e), func(u hintEntry) bool { return u.Kind == "*" && u.Count == 0 })
			if len(e) == 0 {
				return ""
			}
		}
		label, n := kindsLabel(e)
		return fmt.Sprintf("Couldn't read %s; matches of %s may be missing.", label, pluralWord(n, "that kind", "those kinds"))
	}},
	{[]readreason.Code{readreason.UserForbidden}, func(e, _ []hintEntry) string {
		hint := fmt.Sprintf("Your access doesn't include all %s; this result says nothing about the rest.", joinKinds(e, "or", kindPlural))
		if slices.ContainsFunc(e, func(entry hintEntry) bool { return entry.WholeNamespaceScope }) {
			hint += " Try narrowing with ns:<namespace>."
		}
		return hint
	}},
	{[]readreason.Code{readreason.UserNamespaceNotVisible}, func(e, _ []hintEntry) string {
		return namespaceSentence(e, "it isn't among the namespaces you can see, or doesn't exist", "they aren't among the namespaces you can see, or don't exist")
	}},
	{[]readreason.Code{readreason.NamespaceNotFound}, func(e, _ []hintEntry) string {
		var namespaces []string
		for _, entry := range e {
			namespaces = append(namespaces, entry.Namespaces...)
		}
		if len(namespaces) == 1 {
			return fmt.Sprintf("Namespace '%s' doesn't exist on this cluster.", namespaces[0])
		}
		return fmt.Sprintf("Namespaces %s don't exist on this cluster.", quoteJoin(namespaces))
	}},
	{[]readreason.Code{readreason.OutsideNamespacePin}, func(e, _ []hintEntry) string {
		return namespaceSentence(e, "Radar is pinned to another namespace with --namespace-scope", "Radar is pinned to another namespace with --namespace-scope")
	}},
	{[]readreason.Code{readreason.RadarNamespaceNotCollected}, func(e, _ []hintEntry) string {
		return namespaceSentence(e, "Radar here watches only some namespaces", "Radar here watches only some namespaces")
	}},
	{[]readreason.Code{readreason.RadarForbidden}, func(e, _ []hintEntry) string {
		return fmt.Sprintf("Radar isn't allowed to read %s on this cluster; granting you access won't help.", joinKinds(e, "or", kindPlural))
	}},
	{[]readreason.Code{readreason.RadarKindNotCollected}, func(e, _ []hintEntry) string {
		return fmt.Sprintf("Radar doesn't watch %s on this cluster, so they weren't searched.", joinKinds(e, "or", kindPlural))
	}},
}

// coverageHint explains a partial search result to an agent: one sentence per
// reason present, most actionable first, at most maxCoverageSentences and then
// a count of the rest. Every reason yields a sentence, so it is non-empty
// whenever the result is partial.
// scopeHint says how Radar's own setup limits a search that found something:
// an agent reads each answer alone and can't see the install, so without it
// hits read as everything. The caller's access and uncollected kinds stay
// quiet: the agent acts for the caller, and kind collection narrows nearly
// every broad search.
func scopeHint(cov resourceapi.SearchCoverage) string {
	var sentences []string
	if slices.Contains(cov.NarrowedBy, resourceapi.NarrowedByNamespaceCollection) {
		sentences = append(sentences, "For some kinds, Radar searches only the namespaces it collects; matches for them elsewhere aren't included.")
	}
	if slices.Contains(cov.NarrowedBy, resourceapi.NarrowedByNamespacePin) {
		sentences = append(sentences, "Radar is pinned to one namespace; namespaced matches elsewhere aren't included.")
	}
	return strings.Join(sentences, " ")
}

// searchCoverageHint is a search response's coverageHint: what a partial
// result missed, that an empty narrowed one doesn't rule out matches outside
// its scope, and, when there are hits, how Radar's setup limited them.
func searchCoverageHint(res search.Result, namedKinds bool) string {
	var hint string
	if res.Coverage.Status == resourceapi.CoveragePartial {
		hint = coverageHint(res, namedKinds)
	} else if len(res.Coverage.NarrowedBy) > 0 && len(res.Hits) == 0 {
		hint = "No matches within what your access and Radar's collection cover; that does not rule out matches outside them."
	}
	if len(res.Hits) > 0 {
		if scope := scopeHint(res.Coverage); scope != "" {
			hint = strings.TrimSpace(hint + " " + scope)
		}
	}
	return hint
}

func coverageHint(res search.Result, namedKinds bool) string {
	return entriesHint(hintEntries(res.Coverage), res.FilterErrors, res.FilterErrorSample, namedKinds)
}

func entriesHint(unsearched []hintEntry, filterErrors int, filterErrorSample string, namedKinds bool) string {
	var sentences []string
	retryIndex := -1
	retry := ""
	used := map[readreason.Code]bool{}
	for _, tmpl := range coverageTemplates {
		if tmpl.sentence == nil {
			if filterErrors > 0 {
				sentences = append(sentences, filterSentence(filterErrors, filterErrorSample))
			}
			continue
		}
		var entries []hintEntry
		for _, gap := range unsearched {
			if slices.Contains(tmpl.reasons, gap.Reason) {
				entries = append(entries, gap)
				used[gap.Reason] = true
			}
		}
		if len(entries) > 0 {
			if sentence := tmpl.sentence(entries, unsearched); sentence != "" {
				sentences = append(sentences, sentence)
				if !namedKinds && slices.Contains(tmpl.reasons, readreason.ReadError) {
					// Naming a kind restarts its watch; a broad search doesn't.
					// Kinds too many to name leave the agent to name the one
					// it needs.
					counted := false
					for _, entry := range entries {
						if entry.Reason != readreason.ReadError {
							continue
						}
						if entry.Kind == "*" {
							counted = counted || entry.Count > 0
							continue
						}
						if _, n := kindsLabel(entries); n == 1 {
							retry = fmt.Sprintf("Retry with kind:%s before concluding absence.", entry.Kind)
						} else {
							retry = fmt.Sprintf("Retry with kind:%s for a kind you need before concluding absence.", entry.Kind)
						}
						break
					}
					if retry == "" && counted {
						retry = "Retry with kind:<Kind> for a kind you need before concluding absence."
					}
					if retry != "" {
						retryIndex = len(sentences) - 1
					}
				}
			}
		}
	}
	var other []string
	for _, gap := range unsearched {
		if !used[gap.Reason] && !slices.Contains(other, string(gap.Reason)) {
			other = append(other, string(gap.Reason))
		}
	}
	if len(other) > 0 {
		sentences = append(sentences, fmt.Sprintf("Some kinds weren't searched (%s); see coverage.gaps.", strings.Join(other, ", ")))
	}
	if retryIndex >= 0 && retryIndex < maxCoverageSentences {
		sentenceCount := len(sentences)
		if filterErrors > 0 {
			sentenceCount++
		}
		if sentenceCount < maxCoverageSentences {
			sentences[retryIndex] += " " + retry
		} else {
			sentences[retryIndex] = strings.TrimSuffix(sentences[retryIndex], ".") + "; " + strings.ToLower(retry[:1]) + retry[1:]
		}
	}
	if extra := len(sentences) - maxCoverageSentences; extra > 0 {
		sentences = append(sentences[:maxCoverageSentences], fmt.Sprintf("+%d more %s.", extra, pluralWord(extra, "reason", "reasons")))
	}
	return strings.Join(sentences, " ")
}

// kindsLabel names the kinds entries stand for and how many there are: one
// named kind with its group, several as a count and the first few names, and
// "some kinds" (n = 0) when a "*" entry carries no count.
func kindsLabel(entries []hintEntry) (label string, n int) {
	var named []string
	for _, e := range entries {
		switch {
		case e.Kind != "*":
			n++
			named = append(named, e.Kind)
		case e.Count > 0:
			n += e.Count
		default:
			return "some kinds", 0
		}
	}
	if n == 1 && len(named) == 1 {
		return kindWithGroup(entries[0]), 1
	}
	label = fmt.Sprintf("%d %s", n, pluralWord(n, "kind", "kinds"))
	if len(named) > 0 {
		shown := named[:min(len(named), 3)]
		if n > len(shown) {
			shown = append(shown, "…")
		}
		label += " (" + strings.Join(shown, ", ") + ")"
	}
	return label, n
}

func capitalize(s string) string {
	if s == "" {
		return s
	}
	return strings.ToUpper(s[:1]) + s[1:]
}

func kindWithGroup(e hintEntry) string {
	if e.Group == "" {
		return e.Kind
	}
	return fmt.Sprintf("%s (%s)", e.Kind, e.Group)
}

// joinKinds lists the named kinds of entries, at most five, in prose.
func joinKinds(entries []hintEntry, conj string, format func(string) string) string {
	var names []string
	seen := map[string]bool{}
	for _, e := range entries {
		if e.Kind == "*" {
			continue
		}
		name := format(e.Kind)
		if seen[name] {
			continue
		}
		seen[name] = true
		if len(names) < 5 {
			names = append(names, name)
		}
	}
	if len(seen) > len(names) {
		return boundedCoverageNames(names, len(seen))
	}
	switch len(names) {
	case 0:
		return "some kinds"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " " + conj + " " + names[len(names)-1]
}

func quoteJoin(values []string) string {
	quoted := make([]string, min(len(values), 5))
	for i, v := range values[:len(quoted)] {
		quoted[i] = "'" + v + "'"
	}
	return boundedCoverageNames(quoted, len(values))
}

func boundedCoverageNames(shown []string, total int) string {
	text := strings.Join(shown, ", ")
	if total > len(shown) {
		text += fmt.Sprintf(" and %d more", total-len(shown))
	}
	return text
}

func namespaceSentence(entries []hintEntry, oneWhy, manyWhy string) string {
	var namespaces []string
	seen := map[string]bool{}
	for _, e := range entries {
		for _, ns := range e.Namespaces {
			if !seen[ns] {
				seen[ns] = true
				namespaces = append(namespaces, ns)
			}
		}
	}
	slices.Sort(namespaces)
	someKinds := slices.ContainsFunc(entries, func(e hintEntry) bool { return e.SomeKinds })
	if len(namespaces) == 1 {
		if someKinds {
			return fmt.Sprintf("Some kinds weren't searched in namespace %s: %s.", namespaces[0], oneWhy)
		}
		return fmt.Sprintf("Namespace %s wasn't searched: %s.", namespaces[0], oneWhy)
	}
	names := boundedCoverageNames(namespaces[:min(len(namespaces), 5)], len(namespaces))
	if someKinds {
		return fmt.Sprintf("Some kinds weren't searched in namespaces %s: %s.", names, manyWhy)
	}
	return fmt.Sprintf("Namespaces %s weren't searched: %s.", names, manyWhy)
}

// celNoSuchKey matches CEL's missing-field message so the hint can name the field.
var celNoSuchKey = regexp.MustCompile(`no such key: [A-Za-z0-9_.-]+`)

func filterSentence(failed int, sample string) string {
	cause := sample
	if m := celNoSuchKey.FindString(sample); m != "" {
		cause = m
	}
	if len(cause) > 80 {
		cause = strings.ToValidUTF8(cause[:80], "") + "…"
	}
	objects := pluralWord(failed, "object", "objects")
	const advice = "Kubernetes omits fields that are 0 or empty, so guard them with has() and match a zero with kind == \"K\" && (!has(x) || x == 0)."
	if cause == "" {
		return fmt.Sprintf("%d %s failed the filter: %s", failed, objects, advice)
	}
	return fmt.Sprintf("%d %s failed the filter (%s): %s", failed, objects, cause, advice)
}

func pluralWord(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// kindPlural applies regular English plural rules to a Kind name, leaving a
// name that already reads as a plural (Endpoints) alone.
func kindPlural(kind string) string {
	lower := strings.ToLower(kind)
	switch {
	case strings.HasSuffix(lower, "ss"), strings.HasSuffix(lower, "x"), strings.HasSuffix(lower, "ch"), strings.HasSuffix(lower, "sh"):
		return kind + "es"
	case strings.HasSuffix(lower, "s"):
		return kind
	case len(lower) > 1 && strings.HasSuffix(lower, "y") && !strings.ContainsRune("aeiou", rune(lower[len(lower)-2])):
		return kind[:len(kind)-1] + "ies"
	}
	return kind + "s"
}
