package prometheus

import (
	"context"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"
)

// The foreign-object check runs on the metrics a view accepted unfiltered
// because they hold this cluster's objects. kube-state-metrics, carrying the
// cluster labels, is the inventory of which objects belong to which cluster:
// a metric whose series name an object listed under another cluster's labels
// and never under this cluster's holds another cluster's series. Another
// cluster's labels are this cluster's label keys with another value; a
// kube-state-metrics series without them names no cluster.
//
// It runs in two steps. The first asks each reference whether it holds this
// cluster's series at all (without them nothing can be checked) and whether
// it lists any object outside this cluster; a reference that lists none
// passes every metric checked against it, so a store whose kube-state-metrics
// holds one cluster costs that one probe. The second looks for those objects
// in each metric, within the view's subject and over the same span.

const (
	viewScopeReferenceLabel = "radar_reference"
	viewScopeOtherLabel     = "radar_other_cluster"
	viewScopeForeignLabel   = "radar_foreign"
)

// foreignChecks are the metrics accepted unfiltered that carry a check.
func foreignChecks(metrics []ViewMetric, unfiltered []string) []ViewMetric {
	var out []ViewMetric
	for _, m := range metrics {
		if m.Foreign.Reference != "" && slices.Contains(unfiltered, m.Name) {
			out = append(out, m)
		}
	}
	return out
}

// foreignCheckedNames are the unfiltered metrics checked against one of the
// given references.
func foreignCheckedNames(metrics []ViewMetric, unfiltered, references []string) []string {
	var out []string
	for _, m := range foreignChecks(metrics, unfiltered) {
		if slices.Contains(references, m.Foreign.Reference) {
			out = append(out, m.Name)
		}
	}
	return out
}

func (f ForeignCheck) referenceKeys() []string {
	keys := make([]string, len(f.Keys))
	for i, k := range f.Keys {
		keys[i] = k.Reference
	}
	slices.Sort(keys)
	return keys
}

// id tells references apart in a batched answer.
func (f ForeignCheck) id() string {
	return f.Reference + "/" + strings.Join(f.referenceKeys(), ",")
}

// referenceKeySet is the reference's distinct objects over the span, within
// sel. kube-state-metrics scraped without honor_labels moves the object's
// namespace to exported_namespace and puts its own in namespace, so the
// object's namespace is read from exported_namespace when that is set; both
// of the check's object sets are read this way, or the same Pod would be two
// objects.
func referenceKeySet(f ForeignCheck, sel string, span time.Duration) string {
	keys := f.referenceKeys()
	inner := "count_over_time(" + vectorSelector(f.Reference, sel) + "[" + promSeconds(span) + "])"
	if slices.Contains(keys, "namespace") {
		byLabels := slices.Sorted(slices.Values(append(slices.Clone(keys), "exported_namespace")))
		inner = "label_replace(count by (" + strings.Join(byLabels, ",") + ") (" + inner + `), "namespace", "$1", "exported_namespace", "(.+)")`
	}
	return "count by (" + strings.Join(keys, ",") + ") (" + inner + ")"
}

// otherClusterSelectors select the series naming another cluster: each one
// carries every one of this cluster's label keys, and one of them with
// another value. A series missing one of the keys names no cluster (a scrape
// job that left the label off, in this cluster as in any other), so it is no
// evidence of another cluster. ok is false when ours isn't a list of exact
// label matches.
func otherClusterSelectors(ours string) (sels []string, ok bool) {
	matchers := parseViewMatchers(ours)
	present := make([]string, len(matchers))
	for i, m := range matchers {
		if m.label == "" || m.op != "=" {
			return nil, false
		}
		present[i] = m.label + `!=""`
	}
	for _, m := range matchers {
		sels = append(sels, strings.Join(present, ",")+","+m.label+"!="+strconv.Quote(m.value))
	}
	return sels, len(sels) > 0
}

// otherClusterObjects is the objects the reference lists over the span under
// another cluster's labels but never under all of this cluster's. A series is
// this cluster's only when it matches every label, so the complement is taken
// on the objects, not with negative matchers alone.
func otherClusterObjects(f ForeignCheck, ours string, span time.Duration) string {
	sels, _ := otherClusterSelectors(ours)
	others := make([]string, len(sels))
	for i, sel := range sels {
		others[i] = referenceKeySet(f, sel, span)
	}
	return "(" + strings.Join(others, " or ") + ") unless on(" + strings.Join(f.referenceKeys(), ",") + ") " + referenceKeySet(f, ours, span)
}

// viewReferenceParts ask, once per reference, whether it holds this
// cluster's series and whether it lists any object outside this cluster.
func viewReferenceParts(checks []ViewMetric, ours string, span time.Duration) []viewProbePart {
	var parts []viewProbePart
	seen := map[string]bool{}
	for _, m := range checks {
		f := m.Foreign
		id := f.id()
		if seen[id] {
			continue
		}
		seen[id] = true
		parts = append(parts,
			viewProbePart{tag: viewScopeReferenceLabel, metric: id, expr: "count(count_over_time(" + vectorSelector(f.Reference, ours) + "[" + promSeconds(span) + "]))"},
			viewProbePart{tag: viewScopeOtherLabel, metric: id, expr: "count(" + otherClusterObjects(f, ours, span) + ")"},
		)
	}
	return parts
}

// viewForeignParts look for objects outside this cluster among each metric's
// keys within its subject. The metric's keys are aggregated first and only
// then renamed to the reference's labels, so the rename never meets two
// series with one label set. With exported_namespace read, each spelling of
// the namespace is its own part, any of them finding a foreign object.
func viewForeignParts(checks []ViewMetric, ours string, span time.Duration) []viewProbePart {
	var parts []viewProbePart
	window := "[" + promSeconds(span) + "]"
	for _, m := range checks {
		f := m.Foreign
		refKeys := strings.Join(f.referenceKeys(), ",")
		subject := parseViewMatchers(m.Subject)
		type spelling struct {
			labels []string
			extra  []viewMatcher
		}
		plain := make([]string, len(f.Keys))
		for i, k := range f.Keys {
			plain[i] = k.Label
		}
		var inNamespace []viewMatcher
		if f.Namespace != "" {
			inNamespace = []viewMatcher{{label: "namespace", op: "=", value: f.Namespace}}
		}
		spellings := []spelling{{labels: plain, extra: inNamespace}}
		if f.NamespaceExported {
			exported := slices.Clone(plain)
			for i, l := range exported {
				if l == "namespace" {
					exported[i] = "exported_namespace"
				}
			}
			exportedIn := viewMatcher{label: "exported_namespace", op: "!=", value: ""}
			if f.Namespace != "" {
				exportedIn = viewMatcher{label: "exported_namespace", op: "=", value: f.Namespace}
			}
			spellings = []spelling{
				{labels: exported, extra: []viewMatcher{exportedIn}},
				{labels: plain, extra: append([]viewMatcher{{label: "exported_namespace", op: "=", value: ""}}, inNamespace...)},
			}
		}
		for _, sp := range spellings {
			var renames []string
			for i, k := range f.Keys {
				if sp.labels[i] != k.Reference {
					renames = append(renames, `, "`+k.Reference+`", "$1", "`+sp.labels[i]+`", "(.*)")`)
				}
			}
			byLabels := slices.Clone(sp.labels)
			slices.Sort(byLabels)
			parts = append(parts, viewProbePart{
				tag: viewScopeForeignLabel, metric: m.Name,
				head: "count(count by (" + refKeys + ") (" + strings.Repeat("label_replace(", len(renames)) +
					"count by (" + strings.Join(byLabels, ",") + ") (count_over_time(",
				tail:     window + "))" + strings.Join(renames, "") + ") and on(" + refKeys + ") (" + otherClusterObjects(f, ours, span) + "))",
				matchers: append(slices.Clone(subject), sp.extra...),
			})
		}
	}
	return parts
}

// findForeignObjects runs the check on metrics accepted unfiltered. foreign
// names the metrics holding an object listed only under another cluster;
// uncheckable names the references holding none of this cluster's series,
// which leave their metrics unchecked.
func findForeignObjects(ctx context.Context, q instantQuerier, at time.Time, checks []ViewMetric, ours string, span time.Duration) (foreign, uncheckable []string, err error) {
	if len(checks) == 0 {
		return nil, nil, nil
	}
	if _, ok := otherClusterSelectors(ours); !ok {
		return nil, nil, fmt.Errorf("foreign-object check needs exact cluster label matches, got %q", ours)
	}
	_, marks, err := runViewProbes(ctx, q, at, viewReferenceParts(checks, ours, span))
	if err != nil {
		return nil, nil, err
	}
	var shared []ViewMetric
	for _, m := range checks {
		id := m.Foreign.id()
		switch {
		case !marks[viewScopeReferenceLabel][id]:
			if !slices.Contains(uncheckable, m.Foreign.Reference) {
				uncheckable = append(uncheckable, m.Foreign.Reference)
			}
		case marks[viewScopeOtherLabel][id]:
			shared = append(shared, m)
		}
	}
	if len(uncheckable) > 0 {
		slices.Sort(uncheckable)
		return nil, uncheckable, nil
	}
	if len(shared) == 0 {
		return nil, nil, nil
	}
	_, marks, err = runViewProbes(ctx, q, at, viewForeignParts(shared, ours, span))
	if err != nil {
		return nil, nil, err
	}
	for _, m := range shared {
		if marks[viewScopeForeignLabel][m.Name] {
			foreign = append(foreign, m.Name)
		}
	}
	slices.Sort(foreign)
	return foreign, nil, nil
}
