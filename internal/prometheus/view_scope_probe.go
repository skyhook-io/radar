package prometheus

import (
	"context"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// A view's scope probes batch every metric's checks into one query when they
// fit, since metrics stores limit query length. A probe that would not fit is
// split: by metric first, then by chunks of its longest alternation (a Pod
// list, the anchors), since each check's answer over a union of selections is
// the union of its answers over the parts. A check no split brings under the
// limit fails the decision; part of it is never dropped.

// viewMatcher is one label matcher of a selector. raw keeps a matcher as it
// was written, so an unsplit selector reads exactly as its caller built it.
type viewMatcher struct {
	label, op, value string
	raw              string
}

func (m viewMatcher) String() string {
	if m.raw != "" {
		return m.raw
	}
	return m.label + m.op + strconv.Quote(m.value)
}

var viewMatcherHead = regexp.MustCompile(`^\s*([a-zA-Z_][a-zA-Z0-9_]*)\s*(=~|!~|!=|=)\s*`)

// parseViewMatchers takes apart a comma-separated matcher list. One it can't
// read whole stays a single opaque matcher, which is neither split nor
// dropped.
func parseViewMatchers(sel string) []viewMatcher {
	if strings.TrimSpace(sel) == "" {
		return nil
	}
	var out []viewMatcher
	rest := sel
	for {
		head := viewMatcherHead.FindStringSubmatch(rest)
		if head == nil {
			return []viewMatcher{{raw: sel}}
		}
		quoted, err := strconv.QuotedPrefix(rest[len(head[0]):])
		if err != nil {
			return []viewMatcher{{raw: sel}}
		}
		value, err := strconv.Unquote(quoted)
		if err != nil {
			return []viewMatcher{{raw: sel}}
		}
		n := len(head[0]) + len(quoted)
		out = append(out, viewMatcher{label: head[1], op: head[2], value: value, raw: strings.TrimSpace(rest[:n])})
		rest = strings.TrimLeft(rest[n:], " \t")
		if rest == "" {
			return out
		}
		if rest[0] != ',' {
			return []viewMatcher{{raw: sel}}
		}
		rest = rest[1:]
		if strings.TrimSpace(rest) == "" {
			return out
		}
	}
}

// matches reports whether a label value satisfies the matcher, as the store
// would decide it (regexes fully anchored). ok is false when it can't tell.
func (m viewMatcher) matches(value string) (match, ok bool) {
	switch m.op {
	case "=":
		return value == m.value, true
	case "!=":
		return value != m.value, true
	case "=~", "!~":
		re, err := regexp.Compile("^(?:" + m.value + ")$")
		if err != nil {
			return false, false
		}
		return re.MatchString(value) == (m.op == "=~"), true
	}
	return false, false
}

// viewProbePart is one check of one metric: an expression around a single
// selector, tagged with the metric it answers for. A part with expr is that
// whole expression instead, tagged with what it answers for, and is never
// split.
type viewProbePart struct {
	tag, metric string
	head, tail  string
	matchers    []viewMatcher
	expr        string
}

func (p viewProbePart) String() string {
	if p.expr != "" {
		return viewTagged(p.tag, p.metric, p.expr)
	}
	sel := make([]string, len(p.matchers))
	for i, m := range p.matchers {
		sel[i] = m.String()
	}
	return viewTagged(p.tag, p.metric, p.head+vectorSelector(p.metric, strings.Join(sel, ","))+p.tail)
}

func viewTagged(label, metric, expr string) string {
	return "label_replace(" + expr + ", " + strconv.Quote(label) + ", " + strconv.Quote(metric) + ", \"\", \"\")"
}

func joinViewProbeParts(parts []viewProbePart) string {
	out := make([]string, len(parts))
	for i, p := range parts {
		out[i] = p.String()
	}
	return strings.Join(out, " or ")
}

// viewReadbackParts count each metric's filtered series by the filter's own
// labels, so a store that ignores a matcher shows itself in the values it
// returns. Range functions drop metric names, hence the tag.
func viewReadbackParts(metrics []ViewMetric, keys []string, matchers string, span time.Duration) []viewProbePart {
	scope := parseViewMatchers(matchers)
	parts := make([]viewProbePart, len(metrics))
	for i, m := range metrics {
		parts[i] = viewProbePart{
			tag: viewScopeMetricLabel, metric: m.Name,
			head:     "count by (" + strings.Join(keys, ",") + ") (count_over_time(",
			tail:     "[" + promSeconds(span) + "]))",
			matchers: append(parseViewMatchers(m.Subject), scope...),
		}
	}
	return parts
}

// viewIdentityParts read up to two cluster identities per metric and, with
// anchors, whether the metric holds any of this cluster's live objects.
func viewIdentityParts(metrics []ViewMetric, keys []string, span time.Duration, anchors bool) []viewProbePart {
	var parts []viewProbePart
	window := "[" + promSeconds(span) + "]"
	for _, m := range metrics {
		subject := parseViewMatchers(m.Subject)
		parts = append(parts, viewProbePart{
			tag: viewScopeMetricLabel, metric: m.Name,
			head:     "topk(2, count by (" + strings.Join(keys, ",") + ") (count_over_time(",
			tail:     window + ")))",
			matchers: subject,
		})
		if anchors && m.AnchorLabel != "" && len(m.AnchorValues) > 0 {
			for _, anchor := range viewAnchorMatchers(m) {
				parts = append(parts, viewProbePart{
					tag: viewScopeAnchorLabel, metric: m.Name,
					head:     "count(count_over_time(",
					tail:     window + "))",
					matchers: append(viewAnchorSubject(m, subject), anchor...),
				})
			}
		}
	}
	return parts
}

// viewAnchorMatchers select the metric's series holding one of this
// cluster's live objects, in the anchor namespace when there is one: one
// selector, or with exported_namespace accepted, one per spelling of the
// namespace (each its own anchor check, any of them vouching).
func viewAnchorMatchers(m ViewMetric) [][]viewMatcher {
	names := make([]string, len(m.AnchorValues))
	for i, v := range m.AnchorValues {
		names[i] = regexp.QuoteMeta(v)
	}
	pattern := strings.Join(names, "|")
	if m.AnchorOptionalPort {
		pattern = "(" + pattern + ")(:[0-9]+)?"
	}
	anchor := viewMatcher{label: m.AnchorLabel, op: "=~", value: pattern}
	switch {
	case m.AnchorNamespace == "":
		return [][]viewMatcher{{anchor}}
	case m.AnchorNamespaceExported:
		return [][]viewMatcher{
			{{label: "exported_namespace", op: "=", value: m.AnchorNamespace}, anchor},
			{{label: "namespace", op: "=", value: m.AnchorNamespace}, {label: "exported_namespace", op: "=", value: ""}, anchor},
		}
	}
	return [][]viewMatcher{{{label: "namespace", op: "=", value: m.AnchorNamespace}, anchor}}
}

// viewAnchorSubject is the subject's part the anchor check still needs: a
// matcher every anchor already satisfies (the Pod list the anchors were
// drawn from, the anchor namespace) selects nothing more, so leaving it out
// keeps the check's meaning (this cluster's objects exist in that metric
// inside the view's selection) without repeating it. An anchor with an
// optional port matches more than its names, so it vouches for no subject
// matcher on its label.
func viewAnchorSubject(m ViewMetric, subject []viewMatcher) []viewMatcher {
	var out []viewMatcher
	for _, sm := range subject {
		if sm.raw != "" && sm.label == "" {
			out = append(out, sm)
			continue
		}
		if sm.label == "namespace" && sm.op == "=" && m.AnchorNamespace != "" && !m.AnchorNamespaceExported && sm.value == m.AnchorNamespace {
			continue
		}
		if sm.label == m.AnchorLabel && !m.AnchorOptionalPort && allAnchorsMatch(sm, m.AnchorValues) {
			continue
		}
		out = append(out, sm)
	}
	return out
}

func allAnchorsMatch(m viewMatcher, anchors []string) bool {
	for _, v := range anchors {
		if match, ok := m.matches(v); !match || !ok {
			return false
		}
	}
	return true
}

// viewIdentityQuery is the identity check batched in one query, before any
// split.
func viewIdentityQuery(metrics []ViewMetric, keys []string, span time.Duration, anchors bool) string {
	return joinViewProbeParts(viewIdentityParts(metrics, keys, span, anchors))
}

// viewReadbackQuery is the readback batched in one query, before any split.
func viewReadbackQuery(metrics []ViewMetric, keys []string, matchers string, span time.Duration) string {
	return joinViewProbeParts(viewReadbackParts(metrics, keys, matchers, span))
}

// viewProbeMaxBytes bounds every probe query; tests lower it to force splits.
var viewProbeMaxBytes = prom.MaxWorkloadQueryBytes

type viewProbe struct {
	query   string
	metrics map[string]bool
}

// planViewProbes packs the parts, in order, into as few queries of at most
// limit bytes as they fit, splitting a part that alone exceeds it.
func planViewProbes(parts []viewProbePart, limit int) ([]viewProbe, error) {
	var split []viewProbePart
	for _, p := range parts {
		pieces, err := splitViewProbePart(p, limit)
		if err != nil {
			return nil, err
		}
		split = append(split, pieces...)
	}
	var probes []viewProbe
	for _, p := range split {
		expr := p.String()
		if n := len(probes); n > 0 && len(probes[n-1].query)+len(" or ")+len(expr) <= limit {
			probes[n-1].query += " or " + expr
			probes[n-1].metrics[p.metric] = true
			continue
		}
		probes = append(probes, viewProbe{query: expr, metrics: map[string]bool{p.metric: true}})
	}
	return probes, nil
}

// splitViewProbePart halves the part's longest splittable alternation until
// every piece fits.
func splitViewProbePart(p viewProbePart, limit int) ([]viewProbePart, error) {
	if len(p.String()) <= limit {
		return []viewProbePart{p}, nil
	}
	best, bestLen := -1, 0
	var pre, suf string
	var alts []string
	for i, m := range p.matchers {
		if m.op != "=~" || len(m.value) <= bestLen {
			continue
		}
		if a, b, c, ok := regexAlternatives(m.value); ok && len(b) > 1 {
			best, bestLen, pre, alts, suf = i, len(m.value), a, b, c
		}
	}
	if best < 0 {
		return nil, fmt.Errorf("cluster scope check for %s: %w", p.metric, prom.ErrWorkloadQueryTooLarge)
	}
	half := len(alts) / 2
	var out []viewProbePart
	for _, chunk := range [][]string{alts[:half], alts[half:]} {
		piece := p
		piece.matchers = slices.Clone(p.matchers)
		piece.matchers[best] = viewMatcher{label: p.matchers[best].label, op: "=~", value: pre + strings.Join(chunk, "|") + suf}
		pieces, err := splitViewProbePart(piece, limit)
		if err != nil {
			return nil, err
		}
		out = append(out, pieces...)
	}
	return out, nil
}

// regexAlternatives splits a regex into its top-level alternatives, seen
// through ^…$ and one group wrapping it whole (as `^(a|b)$`): pre and suf
// rebuild a regex from any subset of them, which matches exactly the union of
// what those alternatives match. ok is false for a regex it can't take apart
// safely (unbalanced, or flags that would apply across alternatives).
func regexAlternatives(re string) (pre string, alts []string, suf string, ok bool) {
	s := re
	if len(s) >= 2 && s[0] == '^' && s[len(s)-1] == '$' && !escapedAt(s, len(s)-1) {
		pre, suf, s = "^", "$", s[1:len(s)-1]
	}
	for _, open := range []string{"(?:", "("} {
		if strings.HasPrefix(s, open) && (open != "(" || !strings.HasPrefix(s, "(?")) {
			if end, ok := groupEnd(s); ok && end == len(s)-1 {
				pre, suf, s = pre+open, ")"+suf, s[len(open):len(s)-1]
			}
			break
		}
	}
	depth, class, start := 0, false, 0
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			if i+1 == len(s) {
				return "", nil, "", false
			}
			i++
		case class:
			if c == ']' {
				class = false
			}
		case c == '[':
			class = true
			if i+1 < len(s) && s[i+1] == '^' {
				i++
			}
			if i+1 < len(s) && s[i+1] == ']' {
				i++
			}
		case c == '(':
			if depth == 0 && strings.HasPrefix(s[i:], "(?") && !strings.HasPrefix(s[i:], "(?:") {
				return "", nil, "", false
			}
			depth++
		case c == ')':
			depth--
			if depth < 0 {
				return "", nil, "", false
			}
		case c == '|' && depth == 0:
			alts = append(alts, s[start:i])
			start = i + 1
		}
	}
	if depth != 0 || class {
		return "", nil, "", false
	}
	return pre, append(alts, s[start:]), suf, true
}

// groupEnd finds the index of the parenthesis closing the group s opens.
func groupEnd(s string) (int, bool) {
	depth, class := 0, false
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\':
			i++
		case class:
			if c == ']' {
				class = false
			}
		case c == '[':
			class = true
			if i+1 < len(s) && s[i+1] == '^' {
				i++
			}
			if i+1 < len(s) && s[i+1] == ']' {
				i++
			}
		case c == '(':
			depth++
		case c == ')':
			depth--
			if depth == 0 {
				return i, true
			}
		}
	}
	return 0, false
}

// escapedAt reports whether s[i] follows an odd run of backslashes.
func escapedAt(s string, i int) bool {
	n := 0
	for j := i - 1; j >= 0 && s[j] == '\\'; j-- {
		n++
	}
	return n%2 == 1
}

// viewScopeMarkLabels tag the parts whose answer is only whether a row came
// back: the name in the tag is what that row vouches for.
var viewScopeMarkLabels = []string{viewScopeAnchorLabel, viewScopeReferenceLabel, viewScopeOtherLabel, viewScopeForeignLabel}

// runViewProbes sends the parts' probes and merges their answers: each
// metric's distinct rows, and per mark label the names a row came back for.
func runViewProbes(ctx context.Context, q instantQuerier, at time.Time, parts []viewProbePart) (map[string][]map[string]string, map[string]map[string]bool, error) {
	probes, err := planViewProbes(parts, viewProbeMaxBytes)
	if err != nil {
		return nil, nil, err
	}
	rows := map[string][]map[string]string{}
	marks := map[string]map[string]bool{}
	seen := map[string]bool{}
	for _, probe := range probes {
		res, err := q.QueryAtPost(ctx, probe.query, at)
		if err != nil {
			return nil, nil, err
		}
		got, gotMarks, err := groupViewScopeRows(probe.metrics, res)
		if err != nil {
			return nil, nil, err
		}
		for tag, names := range gotMarks {
			if marks[tag] == nil {
				marks[tag] = map[string]bool{}
			}
			for name := range names {
				marks[tag][name] = true
			}
		}
		for name, list := range got {
			for _, row := range list {
				if key := viewRowKey(row); !seen[key] {
					seen[key] = true
					rows[name] = append(rows[name], row)
				}
			}
		}
	}
	return rows, marks, nil
}

func viewRowKey(row map[string]string) string {
	var b strings.Builder
	for _, k := range slices.Sorted(maps.Keys(row)) {
		b.WriteString(strconv.Quote(k) + "=" + strconv.Quote(row[k]) + ",")
	}
	return b.String()
}

// groupViewScopeRows splits a batched answer by metric: rows tagged with the
// metric label, and per mark label the names a row came back for. A row for
// nothing requested means the store didn't evaluate the query as written.
func groupViewScopeRows(requested map[string]bool, res *prom.QueryResult) (map[string][]map[string]string, map[string]map[string]bool, error) {
	rows := map[string][]map[string]string{}
	marks := map[string]map[string]bool{}
	if res == nil {
		return rows, marks, nil
	}
series:
	for _, series := range res.Series {
		for _, tag := range viewScopeMarkLabels {
			if name := series.Labels[tag]; name != "" && requested[name] {
				if marks[tag] == nil {
					marks[tag] = map[string]bool{}
				}
				marks[tag][name] = true
				continue series
			}
		}
		name := series.Labels[viewScopeMetricLabel]
		if !requested[name] {
			return nil, nil, fmt.Errorf("unexpected series in cluster scope answer: %v", series.Labels)
		}
		rows[name] = append(rows[name], series.Labels)
	}
	return rows, marks, nil
}
