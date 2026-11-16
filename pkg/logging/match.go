// Package logging interprets the Logging operator's (logging.banzaicloud.io)
// Flows and outputs: which Flows forward a pod's logs, and what an output is,
// without its configuration.
package logging

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
	"strings"
)

const Group = "logging.banzaicloud.io"

// Rule is one entry of a Flow's spec.match: every given condition must hold.
type Rule struct {
	Exclude        bool
	Labels         map[string]string
	ContainerNames []string
	Hosts          []string
	Namespaces     []string // ClusterFlow only
	// NamespacesRegex (ClusterFlow only): the namespace must match one pattern,
	// unanchored, as the operator's label router does with Ruby's Regexp.
	NamespacesRegex []string
	// NamespaceLabels is not evaluated (Radar doesn't read namespace labels
	// here): a rule using it that would otherwise decide makes the answer
	// uncertain.
	NamespaceLabels map[string]string
	// nsRegex is NamespacesRegex compiled by FlowFrom, nil for a pattern Go
	// can't evaluate; compiled marks it set, so matching never recompiles.
	nsRegex  []*regexp.Regexp
	compiled bool
}

var compileRegex = regexp.Compile

// compileNamespaceRegex keeps nil for a pattern RE2 can't compile (Ruby-only
// syntax) or one containing a comma, which fluentd's array parameter splits
// into separate patterns.
func compileNamespaceRegex(patterns []string) []*regexp.Regexp {
	out := make([]*regexp.Regexp, len(patterns))
	for i, p := range patterns {
		if re, err := compileRegex(p); err == nil && !strings.Contains(p, ",") {
			out[i] = re
		}
	}
	return out
}

type Flow struct {
	Namespace  string
	Name       string
	Cluster    bool
	LoggingRef string
	Match      []Rule
	// MatchSet: spec.match is present, even empty. An empty list gives the
	// operator a route with no match rules, which selects nothing.
	MatchSet       bool
	LegacySelector map[string]string // spec.selectors, used only when Match is empty
	GlobalOutputs  []string          // spec.globalOutputRefs
	LocalOutputs   []string          // spec.localOutputRefs
	// DeprecatedOutputs: spec.outputRefs, which the operator never renders.
	DeprecatedOutputs []string
	HasFilters        bool
	// Invalid: why the operator rejects the Flow (it never renders it); empty when valid.
	Invalid string
}

type PodFacts struct {
	Namespace  string
	Name       string // identifies the pod in answers; not used for matching
	Labels     map[string]string
	Containers []string
	Node       string
}

type Decision struct {
	Selected  bool
	Uncertain bool     // a rule using namespace_labels, or a regex Radar can't evaluate, would decide
	Rule      int      // the deciding rule, -1 when none decided or match is empty
	Unmet     []string // what the nearest select rule lacks, when no rule decided
}

// InScope: a namespaced Flow only sees pods in its own namespace; a
// ClusterFlow sees every namespace (its rules may narrow that).
func InScope(flow Flow, podNamespace string) bool {
	return flow.Cluster || flow.Namespace == podNamespace
}

// Selects reports whether the Flow forwards the logs of at least one of the
// pod's containers, as the operator evaluates spec.match: per container, the
// first rule whose conditions all hold decides (select or exclude), and no
// matching rule means not selected.
func Selects(flow Flow, pod PodFacts) Decision {
	if !InScope(flow, pod.Namespace) {
		return Decision{Rule: -1}
	}
	if len(flow.Match) == 0 && flow.MatchSet {
		return Decision{Rule: -1, Unmet: []string{"a match rule"}}
	}
	if len(flow.Match) == 0 {
		missing := missingLabels(flow.LegacySelector, pod.Labels)
		return Decision{Selected: len(missing) == 0, Rule: -1, Unmet: missing}
	}
	firstRule, uncertainRule := -1, -1
	for ci, container := range pod.Containers {
		for ri, r := range flow.Match {
			if !ruleHolds(r, pod, container) {
				continue
			}
			if _, known := namespaceRegexHolds(r, pod.Namespace); len(r.NamespaceLabels) > 0 || !known {
				if uncertainRule < 0 {
					uncertainRule = ri
				}
			} else if !r.Exclude {
				return Decision{Selected: true, Rule: ri}
			}
			if ci == 0 && firstRule < 0 {
				firstRule = ri
			}
			break
		}
	}
	if uncertainRule >= 0 {
		return Decision{Uncertain: true, Rule: uncertainRule}
	}
	if firstRule >= 0 {
		return Decision{Rule: firstRule}
	}
	return Decision{Rule: -1, Unmet: nearestUnmet(flow.Match, pod)}
}

func ruleHolds(r Rule, pod PodFacts, container string) bool {
	return labelsHold(r.Labels, pod.Labels) &&
		(len(r.ContainerNames) == 0 || slices.Contains(r.ContainerNames, container)) &&
		(len(r.Hosts) == 0 || slices.Contains(r.Hosts, pod.Node)) &&
		(len(r.Namespaces) == 0 || slices.Contains(r.Namespaces, pod.Namespace)) &&
		regexHoldsOrUnknown(r, pod.Namespace)
}

func regexHoldsOrUnknown(r Rule, namespace string) bool {
	holds, known := namespaceRegexHolds(r, namespace)
	return holds || !known
}

// namespaceRegexHolds evaluates namespaces_regex with Go's regexp; known is
// false when a pattern Go can't evaluate could decide.
func namespaceRegexHolds(r Rule, namespace string) (holds, known bool) {
	if len(r.NamespacesRegex) == 0 {
		return true, true
	}
	compiled := r.nsRegex
	if !r.compiled {
		compiled = compileNamespaceRegex(r.NamespacesRegex)
	}
	known = true
	for _, re := range compiled {
		if re == nil {
			known = false
		} else if re.MatchString(namespace) {
			holds = true
		}
	}
	return holds, known || holds
}

// nearestUnmet lists what the select rule closest to holding lacks, at pod
// level (a container condition is met when any container matches).
func nearestUnmet(rules []Rule, pod PodFacts) []string {
	var best []string
	found := false
	for _, r := range rules {
		if r.Exclude {
			continue
		}
		unmet := missingLabels(r.Labels, pod.Labels)
		if len(r.ContainerNames) > 0 && !slices.ContainsFunc(pod.Containers, func(c string) bool { return slices.Contains(r.ContainerNames, c) }) {
			unmet = append(unmet, "container in "+list(r.ContainerNames))
		}
		if len(r.Hosts) > 0 && !slices.Contains(r.Hosts, pod.Node) {
			unmet = append(unmet, "node in "+list(r.Hosts))
		}
		if len(r.Namespaces) > 0 && !slices.Contains(r.Namespaces, pod.Namespace) {
			unmet = append(unmet, "namespace in "+list(r.Namespaces))
		}
		if !regexHoldsOrUnknown(r, pod.Namespace) {
			unmet = append(unmet, "namespace matching "+list(r.NamespacesRegex))
		}
		sort.Strings(unmet)
		if !found || len(unmet) < len(best) {
			best, found = unmet, true
		}
	}
	return best
}

func labelsHold(want, have map[string]string) bool {
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			return false
		}
	}
	return true
}

func missingLabels(want, have map[string]string) []string {
	var out []string
	for k, v := range want {
		if got, ok := have[k]; !ok || got != v {
			out = append(out, fmt.Sprintf("label %s=%s", k, v))
		}
	}
	sort.Strings(out)
	return out
}

func list(values []string) string { return "[" + strings.Join(values, ", ") + "]" }
