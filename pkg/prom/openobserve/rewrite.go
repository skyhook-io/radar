package openobserve

import (
	"errors"
	"fmt"
	"strings"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// Matcher is an exact-equality cluster matcher, e.g. {radar_cluster_uid, <uid>}.
type Matcher struct{ Name, Value string }

// Dialect names how the scraped data is shaped in OpenObserve.
type Dialect string

const (
	DialectStandard Dialect = "standard"
	// DialectCollector is data shipped by the OpenObserve collector, whose
	// metric and label names differ from a Prometheus scrape.
	DialectCollector Dialect = "collector"
)

// RewriteOptions is the scope one cluster's queries are confined to.
type RewriteOptions struct {
	// Matchers are the canonical cluster matchers: forced on every selector
	// and required on every returned series.
	Matchers []Matcher
	Dialect  Dialect
	// CollectorUID (collector dialect) is the k8s_cluster_uid value forced, in
	// addition to Matchers, on the collector agent's metrics, the only ones
	// that carry it.
	CollectorUID string
	// GatewayMetricsAllowed (collector dialect): when false, selectors on
	// metrics without k8s_cluster_uid (scraped kube-state-metrics, cAdvisor
	// disk metrics, apiserver, coredns...) are refused with ErrNotCollected,
	// because the cluster name alone does not identify the cluster.
	GatewayMetricsAllowed bool
}

// ErrNotCollected wraps the metric name of a selector this source cannot
// answer for the cluster. Callers show it as "not collected by this source".
var ErrNotCollected = errors.New("openobserve: not collected by this source")

var errNoMatchers = errors.New("openobserve: no cluster matcher configured")

// ErrScalarUnsupported refuses scalar() over a vector. Its result is a bare
// number with no labels, so FilterResponse can't tell whether the vector it
// came from held only this cluster's series.
var ErrScalarUnsupported = errors.New("openobserve: OpenObserve-backed metrics don't support scalar(), because its result carries no cluster label to check. Query the vector instead, for example sum(...) rather than scalar(sum(...)), and read the single value from the result")

// ErrClusterLabelWrite refuses a query that writes a cluster label itself
// (label_replace, label_join, count_values).
var ErrClusterLabelWrite = errors.New("openobserve: queries may not write a cluster label")

// ReservedValuePrefix starts OpenObserve's dashboard placeholder ("_o2_all_"
// by default, configurable with ZO_DASHBOARD_PLACEHOLDER). OpenObserve drops
// every matcher whose value equals the placeholder before running a query,
// so a cluster matcher with such a value would filter nothing.
const ReservedValuePrefix = "_o2_"

// IsReservedValue reports whether v may be OpenObserve's dashboard
// placeholder and so can never scope a query.
func IsReservedValue(v string) bool {
	return strings.HasPrefix(strings.ToLower(v), ReservedValuePrefix)
}

// ErrReservedName refuses a cluster name OpenObserve would treat as its
// dashboard placeholder.
var ErrReservedName = errors.New(`openobserve: cluster names starting with "_o2_" can't identify a cluster: OpenObserve treats its dashboard placeholder (_o2_all_ by default) as "any value" and drops the filter`)

const (
	collectorUIDLabel     = "k8s_cluster_uid"
	collectorClusterLabel = "k8s_cluster"
)

type scope struct {
	forced         []*labels.Matcher
	collector      bool
	uid            *labels.Matcher
	gatewayAllowed bool
}

func (s *scope) forcedNames() []string {
	names := make([]string, len(s.forced))
	for i, f := range s.forced {
		names[i] = f.Name
	}
	return names
}

// protected reports whether a query may not write label name: a forced
// matcher label or, on collector data, either collector cluster label.
func (s *scope) protected(name string) bool {
	name = strings.ToLower(name)
	if s.collector && (name == collectorUIDLabel || name == collectorClusterLabel) {
		return true
	}
	for _, f := range s.forced {
		if f.Name == name {
			return true
		}
	}
	return false
}

// required is every matcher a selector on metric name must carry.
func (s *scope) required(name string) []*labels.Matcher {
	if s.collector && s.uid != nil && isCollectorAgentMetric(name) {
		return append(append([]*labels.Matcher(nil), s.forced...), s.uid)
	}
	return s.forced
}

// Rewrite returns q with every metric and label name lowercased, every
// selector carrying opts.Matchers (in addition to any matcher q had on those
// labels, which can only narrow the answer further), and every step that would drop labels keeping the matcher labels,
// so that FilterResponse can require them on every answer.
//
// Refused: scalar() over a vector (ErrScalarUnsupported). Not rewritten:
// absent()/absent_over_time() already return the equality matchers' labels.
func Rewrite(q string, opts RewriteOptions) (string, error) {
	s, err := prepare(opts)
	if err != nil {
		return "", err
	}
	expr, err := parser.ParseExpr(q)
	if err != nil {
		return "", fmt.Errorf("openobserve: unparseable query: %w", err)
	}
	r := rewriter{s: s}
	out, pending, err := r.walk(expr)
	if err != nil {
		return "", err
	}
	if len(pending) > 0 {
		if out.Type() != parser.ValueTypeVector {
			return "", errors.New("openobserve: a bare range selector on a translated metric cannot be answered; wrap it in a range function")
		}
		out = wrapRenames(out, pending)
	}
	str := out.String()
	if err := verifyScoped(str, s); err != nil {
		return "", err
	}
	return str, nil
}

// RewriteSeriesSelector is Rewrite for a match[] series selector. Translated
// metrics keep the collector's label names: a series answer cannot be relabelled.
func RewriteSeriesSelector(sel string, opts RewriteOptions) (string, error) {
	s, err := prepare(opts)
	if err != nil {
		return "", err
	}
	expr, err := parser.ParseExpr(sel)
	if err != nil {
		return "", fmt.Errorf("openobserve: unparseable series selector: %w", err)
	}
	vs, ok := expr.(*parser.VectorSelector)
	if !ok {
		return "", errors.New("openobserve: series selector must be a plain vector selector")
	}
	if vs.Timestamp != nil || vs.StartOrEnd != 0 || vs.OriginalOffset != 0 || vs.OriginalOffsetExpr != nil {
		return "", errors.New("openobserve: series selector must not use offset or @")
	}
	r := rewriter{s: s}
	if _, err := r.rewriteSelector(vs); err != nil {
		return "", err
	}
	out := vs.String()
	if err := verifyScoped(out, s); err != nil {
		return "", err
	}
	return out, nil
}

// SelectorForLabelValues is the match[] selector for a label lookup that
// named none: just the cluster matchers.
func SelectorForLabelValues(matchers []Matcher) string {
	parts := make([]string, 0, len(matchers))
	for _, m := range matchers {
		lm := &labels.Matcher{Type: labels.MatchEqual, Name: strings.ToLower(m.Name), Value: m.Value}
		parts = append(parts, lm.String())
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func prepare(opts RewriteOptions) (*scope, error) {
	if len(opts.Matchers) == 0 {
		return nil, errNoMatchers
	}
	s := &scope{gatewayAllowed: true}
	switch opts.Dialect {
	case "", DialectStandard:
	case DialectCollector:
		s.collector = true
		s.gatewayAllowed = opts.GatewayMetricsAllowed
	default:
		return nil, fmt.Errorf("openobserve: unknown dialect %q", opts.Dialect)
	}
	seen := map[string]bool{}
	for _, m := range opts.Matchers {
		name := strings.ToLower(m.Name)
		if name == "" || name == labels.MetricName {
			return nil, fmt.Errorf("openobserve: invalid cluster matcher label %q", m.Name)
		}
		if m.Value == "" {
			return nil, fmt.Errorf("openobserve: cluster matcher %q has an empty value", m.Name)
		}
		if IsReservedValue(m.Value) {
			return nil, fmt.Errorf("%w (cluster matcher %s=%q)", ErrReservedName, name, m.Value)
		}
		if seen[name] {
			return nil, fmt.Errorf("openobserve: duplicate cluster matcher %q", name)
		}
		seen[name] = true
		s.forced = append(s.forced, &labels.Matcher{Type: labels.MatchEqual, Name: name, Value: m.Value})
	}
	if s.collector {
		if opts.CollectorUID != "" {
			if IsReservedValue(opts.CollectorUID) {
				return nil, fmt.Errorf("%w (collector UID %q)", ErrReservedName, opts.CollectorUID)
			}
			if seen[collectorUIDLabel] {
				return nil, fmt.Errorf("openobserve: %s is both a cluster matcher and the collector UID", collectorUIDLabel)
			}
			s.uid = &labels.Matcher{Type: labels.MatchEqual, Name: collectorUIDLabel, Value: opts.CollectorUID}
		} else if !s.gatewayAllowed && !seen[collectorUIDLabel] {
			return nil, errors.New("openobserve: collector scope without a cluster UID must be identified by a unique cluster name")
		}
	} else if opts.CollectorUID != "" || opts.GatewayMetricsAllowed {
		return nil, errors.New("openobserve: collector options set on a non-collector scope")
	}
	return s, nil
}

type rewriter struct{ s *scope }

// walk rewrites e in place where it can and returns the node to put in its
// parent's place. pending is non-empty when e is (or wraps) a range selector
// on a translated metric, whose labels the enclosing function must rename.
func (r *rewriter) walk(e parser.Expr) (parser.Expr, []rename, error) {
	switch n := e.(type) {
	case *parser.VectorSelector:
		renames, err := r.rewriteSelector(n)
		return n, renames, err
	case *parser.MatrixSelector:
		vs, ok := n.VectorSelector.(*parser.VectorSelector)
		if !ok {
			return nil, nil, fmt.Errorf("openobserve: unexpected range selector %s", n)
		}
		renames, err := r.rewriteSelector(vs)
		return n, renames, err
	case *parser.ParenExpr:
		inner, pending, err := r.walk(n.Expr)
		if err != nil {
			return nil, nil, err
		}
		n.Expr = inner
		return n, pending, nil
	case *parser.SubqueryExpr:
		inner, err := r.instant(n.Expr)
		if err != nil {
			return nil, nil, err
		}
		n.Expr = inner
		return n, nil, nil
	case *parser.UnaryExpr:
		inner, err := r.instant(n.Expr)
		if err != nil {
			return nil, nil, err
		}
		n.Expr = inner
		return n, nil, nil
	case *parser.StepInvariantExpr:
		inner, err := r.instant(n.Expr)
		if err != nil {
			return nil, nil, err
		}
		n.Expr = inner
		return n, nil, nil
	case *parser.AggregateExpr:
		inner, err := r.instant(n.Expr)
		if err != nil {
			return nil, nil, err
		}
		n.Expr = inner
		if n.Param != nil {
			if n.Param, err = r.instant(n.Param); err != nil {
				return nil, nil, err
			}
		}
		lowerAll(n.Grouping)
		if n.Op == parser.COUNT_VALUES {
			lowerStringArg(n.Param)
			if dst, ok := stringArg(n.Param); ok && r.s.protected(dst) {
				return nil, nil, fmt.Errorf("%w: count_values may not write the cluster label %q", ErrClusterLabelWrite, dst)
			}
		}
		if n.Without {
			n.Grouping = removeNames(n.Grouping, r.s.forcedNames())
		} else {
			n.Grouping = appendMissing(n.Grouping, r.s.forcedNames())
		}
		return n, nil, nil
	case *parser.BinaryExpr:
		var err error
		if n.LHS, err = r.instant(n.LHS); err != nil {
			return nil, nil, err
		}
		if n.RHS, err = r.instant(n.RHS); err != nil {
			return nil, nil, err
		}
		if vm := n.VectorMatching; vm != nil {
			lowerAll(vm.MatchingLabels)
			lowerAll(vm.Include)
			if vm.On {
				vm.MatchingLabels = appendMissing(vm.MatchingLabels, r.s.forcedNames())
			} else {
				vm.MatchingLabels = removeNames(vm.MatchingLabels, r.s.forcedNames())
			}
			// A label may not be in on() and group_x() at once.
			vm.Include = removeNames(vm.Include, r.s.forcedNames())
		}
		return n, nil, nil
	case *parser.Call:
		return r.call(n)
	case *parser.NumberLiteral, *parser.StringLiteral:
		return e, nil, nil
	}
	return nil, nil, fmt.Errorf("openobserve: unsupported expression %T", e)
}

// instant walks an instant-vector (or scalar) child and applies any renames
// right there, so the parent sees Prometheus label names.
func (r *rewriter) instant(e parser.Expr) (parser.Expr, error) {
	out, pending, err := r.walk(e)
	if err != nil {
		return nil, err
	}
	if len(pending) > 0 {
		out = wrapRenames(out, pending)
	}
	return out, nil
}

func (r *rewriter) call(c *parser.Call) (parser.Expr, []rename, error) {
	if c.Func == nil {
		return nil, nil, errors.New("openobserve: call without a function")
	}
	if c.Func.Name == "scalar" && !allNumberLiterals(c.Args) {
		return nil, nil, ErrScalarUnsupported
	}
	// absent() derives its labels from the selector's equality matchers, so
	// a translated selector is renamed after absent, not before.
	deferSelector := c.Func.Name == "absent"
	var pending []rename
	for i, arg := range c.Args {
		if arg.Type() == parser.ValueTypeMatrix || (deferSelector && isSelector(arg)) {
			out, p, err := r.walk(arg)
			if err != nil {
				return nil, nil, err
			}
			c.Args[i] = out
			pending = mergeRenames(pending, p)
			continue
		}
		out, err := r.instant(arg)
		if err != nil {
			return nil, nil, err
		}
		c.Args[i] = out
	}
	lowerLabelArgs(c)
	if err := r.checkWrittenLabel(c); err != nil {
		return nil, nil, err
	}
	var out parser.Expr = c
	if len(pending) > 0 {
		out = wrapRenames(c, pending)
	}
	if c.Func.Name == "vector" || absentOverComputed(c) {
		// vector() has no labels, nor has absent() over anything but a plain
		// selector; give them the cluster's so `x or vector(0)` and
		// `absent(sum(x))` survive FilterResponse.
		for _, f := range r.s.forced {
			out = labelReplace(out, f.Name, strings.ReplaceAll(f.Value, "$", "$$"), "", "")
		}
	}
	return out, nil, nil
}

// checkWrittenLabel refuses a query's own label_replace/label_join that
// writes a cluster label: on a store that ignores the forced matcher it could
// stamp this cluster's value onto another cluster's series. The rewriter's
// own label_replace calls are added after this check and never walked.
func (r *rewriter) checkWrittenLabel(c *parser.Call) error {
	if c.Func.Name != "label_replace" && c.Func.Name != "label_join" {
		return nil
	}
	if len(c.Args) < 2 {
		return nil
	}
	dst, ok := stringArg(c.Args[1])
	if !ok {
		return fmt.Errorf("openobserve: %s destination label must be a string literal", c.Func.Name)
	}
	if r.s.protected(dst) {
		return fmt.Errorf("%w: %s may not write the cluster label %q", ErrClusterLabelWrite, c.Func.Name, dst)
	}
	return nil
}

func allNumberLiterals(args parser.Expressions) bool {
	for _, a := range args {
		if _, ok := unparen(a).(*parser.NumberLiteral); !ok {
			return false
		}
	}
	return true
}

func stringArg(e parser.Expr) (string, bool) {
	for {
		p, ok := e.(*parser.ParenExpr)
		if !ok {
			break
		}
		e = p.Expr
	}
	s, ok := e.(*parser.StringLiteral)
	if !ok {
		return "", false
	}
	return strings.ToLower(s.Val), true
}

// rangeScalarAsVector wraps a range query whose result is a plain number
// (time(), a literal) in vector(), which Rewrite labels with the
// cluster: the matrix such a query returns has a series with no labels, which
// FilterResponse would otherwise drop. An instant query keeps its scalar
// result type, which FilterResponse leaves alone.
func rangeScalarAsVector(q string) string {
	expr, err := parser.ParseExpr(q)
	if err != nil || expr.Type() != parser.ValueTypeScalar {
		return q
	}
	return "vector(" + q + ")"
}

// absentOverComputed reports whether c is absent or absent_over_time over an
// expression its result can't take labels from (an aggregation, a function,
// a subquery): such a result carries no labels at all.
func absentOverComputed(c *parser.Call) bool {
	if len(c.Args) != 1 {
		return false
	}
	switch c.Func.Name {
	case "absent":
		return !isSelector(c.Args[0])
	case "absent_over_time":
		_, ok := unparen(c.Args[0]).(*parser.MatrixSelector)
		return !ok
	}
	return false
}

func unparen(e parser.Expr) parser.Expr {
	for {
		p, ok := e.(*parser.ParenExpr)
		if !ok {
			return e
		}
		e = p.Expr
	}
}

func isSelector(e parser.Expr) bool {
	for {
		p, ok := e.(*parser.ParenExpr)
		if !ok {
			break
		}
		e = p.Expr
	}
	_, ok := e.(*parser.VectorSelector)
	return ok
}

func (r *rewriter) rewriteSelector(vs *parser.VectorSelector) ([]rename, error) {
	vs.Name = strings.ToLower(vs.Name)
	matchers := make([]*labels.Matcher, 0, len(vs.LabelMatchers)+len(r.s.forced)+1)
	for _, m := range vs.LabelMatchers {
		name := strings.ToLower(m.Name)
		value := m.Value
		if name == labels.MetricName {
			value = lowerMetricNameValue(m.Type, value)
		}
		lm, err := labels.NewMatcher(m.Type, name, value)
		if err != nil {
			return nil, fmt.Errorf("openobserve: rewrite matcher %s: %w", m, err)
		}
		matchers = append(matchers, lm)
	}

	var renames []rename
	if r.s.collector {
		if vs.Name == "" {
			for _, m := range matchers {
				if m.Name == labels.MetricName && m.Type == labels.MatchEqual {
					vs.Name = m.Value
				}
			}
		}
		if vs.Name == "" {
			if !r.s.gatewayAllowed {
				return nil, fmt.Errorf("%w: selector %s names no metric", ErrNotCollected, vs)
			}
		} else {
			name, translated, rn, err := translateCollector(vs.Name, matchers)
			if err != nil {
				return nil, err
			}
			vs.Name, matchers, renames = name, translated, rn
			if !r.s.gatewayAllowed && !isCollectorAgentMetric(vs.Name) {
				return nil, fmt.Errorf("%w: %s (the collector's scraped metrics carry no cluster ID and its cluster name is not unique)", ErrNotCollected, vs.Name)
			}
		}
	}

	required := r.s.required(vs.Name)
	kept := make([]*labels.Matcher, 0, len(matchers)+len(required)+1)
	for _, m := range matchers {
		// The query's own matchers on a cluster label stay beside the forced
		// one, which narrows them: {cluster!="mine"} answers nothing rather
		// than this cluster's series. Only an exact copy is dropped.
		if isForcedMatcher(m, required) {
			continue
		}
		if vs.Name != "" && m.Name == labels.MetricName && m.Type == labels.MatchEqual {
			continue
		}
		kept = append(kept, m)
	}
	if vs.Name != "" {
		kept = append(kept, labels.MustNewMatcher(labels.MatchEqual, labels.MetricName, vs.Name))
	}
	for _, f := range required {
		kept = append(kept, &labels.Matcher{Type: labels.MatchEqual, Name: f.Name, Value: f.Value})
	}
	vs.LabelMatchers = kept
	return renames, nil
}

func isForcedMatcher(m *labels.Matcher, required []*labels.Matcher) bool {
	for _, f := range required {
		if m.Type == labels.MatchEqual && m.Name == f.Name && m.Value == f.Value {
			return true
		}
	}
	return false
}

// rename copies a collector label to its Prometheus name in query output.
type rename struct{ from, to string }

func mergeRenames(a, b []rename) []rename {
	for _, rn := range b {
		dup := false
		for _, have := range a {
			if have == rn {
				dup = true
				break
			}
		}
		if !dup {
			a = append(a, rn)
		}
	}
	return a
}

func wrapRenames(e parser.Expr, renames []rename) parser.Expr {
	for _, rn := range renames {
		// (.+) leaves the target alone on series without the source label.
		e = labelReplace(e, rn.to, "$1", rn.from, "(.+)")
	}
	return e
}

func labelReplace(e parser.Expr, dst, replacement, src, regex string) parser.Expr {
	return &parser.Call{
		Func: parser.Functions["label_replace"],
		Args: parser.Expressions{
			e,
			&parser.StringLiteral{Val: dst},
			&parser.StringLiteral{Val: replacement},
			&parser.StringLiteral{Val: src},
			&parser.StringLiteral{Val: regex},
		},
	}
}

func appendMissing(names, add []string) []string {
	for _, a := range add {
		found := false
		for _, n := range names {
			if n == a {
				found = true
				break
			}
		}
		if !found {
			names = append(names, a)
		}
	}
	return names
}

func removeNames(names, drop []string) []string {
	if len(names) == 0 {
		return names
	}
	out := names[:0]
	for _, n := range names {
		keep := true
		for _, d := range drop {
			if n == d {
				keep = false
				break
			}
		}
		if keep {
			out = append(out, n)
		}
	}
	return out
}

// lowerMetricNameValue keeps the letter after a backslash in a regex as is,
// since \S and \s (or \D and \d) mean different things.
func lowerMetricNameValue(t labels.MatchType, v string) string {
	if t != labels.MatchRegexp && t != labels.MatchNotRegexp {
		return strings.ToLower(v)
	}
	var b strings.Builder
	b.Grow(len(v))
	escaped := false
	for _, r := range v {
		switch {
		case escaped:
			b.WriteRune(r)
			escaped = false
		case r == '\\':
			b.WriteRune(r)
			escaped = true
		default:
			b.WriteString(strings.ToLower(string(r)))
		}
	}
	return b.String()
}

func lowerAll(names []string) {
	for i, n := range names {
		names[i] = strings.ToLower(n)
	}
}

func lowerStringArg(e parser.Expr) {
	for {
		p, ok := e.(*parser.ParenExpr)
		if !ok {
			break
		}
		e = p.Expr
	}
	if s, ok := e.(*parser.StringLiteral); ok {
		s.Val = strings.ToLower(s.Val)
	}
}

func lowerLabelArgs(c *parser.Call) {
	if c.Func == nil {
		return
	}
	var idx []int
	switch c.Func.Name {
	case "label_replace":
		idx = []int{1, 3}
	case "label_join":
		idx = []int{1}
		for i := 3; i < len(c.Args); i++ {
			idx = append(idx, i)
		}
	case "sort_by_label", "sort_by_label_desc":
		for i := 1; i < len(c.Args); i++ {
			idx = append(idx, i)
		}
	}
	for _, i := range idx {
		if i < len(c.Args) {
			lowerStringArg(c.Args[i])
		}
	}
}

// verifyScoped re-parses the output so that a printer quirk can never let an
// unscoped selector through.
func verifyScoped(out string, s *scope) error {
	expr, err := parser.ParseExpr(out)
	if err != nil {
		return fmt.Errorf("openobserve: rewritten query does not parse: %w", err)
	}
	var bad error
	parser.Inspect(expr, func(node parser.Node, _ []parser.Node) error {
		vs, ok := node.(*parser.VectorSelector)
		if !ok || bad != nil {
			return nil
		}
		if s.collector && !s.gatewayAllowed && !isCollectorAgentMetric(vs.Name) {
			bad = fmt.Errorf("openobserve: selector %s is not a collector agent metric", vs)
			return bad
		}
		for _, f := range s.required(vs.Name) {
			forced := false
			for _, m := range vs.LabelMatchers {
				if m.Name == f.Name && m.Type == labels.MatchEqual && m.Value == f.Value {
					forced = true
				}
			}
			if !forced {
				bad = fmt.Errorf("openobserve: selector %s is not scoped to %s", vs, f)
				return bad
			}
		}
		return nil
	})
	return bad
}
