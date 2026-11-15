package prometheus

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"time"
)

// Substrate's instruments reach Prometheus under one of two spellings:
//
//   - escaped, the default of the OTel Prometheus exporter and of Prometheus's
//     OTLP receiver: dots become underscores and unit and type suffixes are
//     added (ate.actor.crashes → ate_actor_crashes_total, a histogram in
//     seconds → ate_..._seconds_bucket); attributes are escaped the same way.
//   - dotted (UTF-8), kept by Google Managed Prometheus and by a receiver set
//     to NoTranslation: names and attributes keep their dots, with or without
//     the suffixes. GMP rejects dotted labels in a by clause, so aggregations
//     copy them to their escaped names first (norm).
//
// One request reads one spelling throughout, so every selector, scope probe
// and aggregation agrees on the label names.

type instrumentKind int

const (
	instrumentGauge instrumentKind = iota
	instrumentCounter
	instrumentHistogram
)

// substrateInstrument is one OTel instrument Substrate exports, as named in
// its metric registry.
type substrateInstrument struct {
	name    string
	kind    instrumentKind
	seconds bool
	// additive: a chart sums or averages it across emitters, so the same
	// process observed through two scrape jobs would be counted twice. A chart
	// that takes a maximum is unaffected by a duplicate.
	additive bool
}

var (
	instWorkers    = substrateInstrument{name: "ate.workerpool.workers", additive: true}
	instDesired    = substrateInstrument{name: "ate.workerpool.desired_workers"}
	instReady      = substrateInstrument{name: "ate.workerpool.ready_workers"}
	instLifecycle  = substrateInstrument{name: "ate.actor.lifecycle.operation.duration", kind: instrumentHistogram, seconds: true, additive: true}
	instAssignment = substrateInstrument{name: "ate.scheduler.assignment.duration", kind: instrumentHistogram, seconds: true, additive: true}
	instCrashes    = substrateInstrument{name: "ate.actor.crashes", kind: instrumentCounter, additive: true}
	instUndurable  = substrateInstrument{name: "atelet.snapshot.undurable.age.max", seconds: true}
	instOverdue    = substrateInstrument{name: "atelet.snapshot.upload.overdue.actors"}
	instUpload     = substrateInstrument{name: "atelet.snapshot.upload.duration", kind: instrumentHistogram, seconds: true, additive: true}
	instRestore    = substrateInstrument{name: "ate.actor.restore.duration", kind: instrumentHistogram, seconds: true, additive: true}
	instCheckpoint = substrateInstrument{name: "ate.actor.checkpoint.duration", kind: instrumentHistogram, seconds: true, additive: true}
)

// stems are the names, before a histogram's _bucket or _count, Prometheus can
// hold the instrument under in one spelling, most likely first.
func (i substrateInstrument) stems(dotted bool) []string {
	unit, total := "", ""
	if i.seconds {
		unit = "_seconds"
	}
	if i.kind == instrumentCounter {
		total = "_total"
	}
	base := i.name
	if !dotted {
		base = strings.ReplaceAll(i.name, ".", "_")
	}
	out := []string{base + unit + total}
	for _, s := range []string{base + total, base} {
		if s != out[len(out)-1] && s != out[0] {
			out = append(out, s)
		}
	}
	return out
}

// probeSuffix is the part of the instrument whose presence stands for it.
func (i substrateInstrument) probeSuffix() string {
	if i.kind == instrumentHistogram {
		return "_count"
	}
	return ""
}

type substrateSpelling struct {
	dotted bool
	// stems: the stem found for each instrument; one not found reads its most
	// likely stem, so "not found" names what was looked for.
	stems map[string]string
}

func (s substrateSpelling) stem(i substrateInstrument) string {
	if stem, ok := s.stems[i.name]; ok {
		return stem
	}
	return i.stems(s.dotted)[0]
}

func (s substrateSpelling) metric(i substrateInstrument, part string) string {
	return s.stem(i) + part
}

func (s substrateSpelling) probe(i substrateInstrument) string {
	return s.metric(i, i.probeSuffix())
}

func (s substrateSpelling) label(attr string) string {
	if s.dotted {
		return attr
	}
	return escapeOTelName(attr)
}

// match renders one label matcher for an OTel attribute, or for a label the
// scrape adds (node, job), which has no dots in either spelling.
func (s substrateSpelling) match(attr, op, value string) string {
	name := s.label(attr)
	if !isLegacyName(name) {
		name = strconv.Quote(name)
	}
	return name + op + strconv.Quote(value)
}

func (s substrateSpelling) source(instruments []substrateInstrument) string {
	names := make([]string, len(instruments))
	for i, inst := range instruments {
		names[i] = s.stem(inst)
	}
	return strings.Join(names, ", ")
}

func escapeOTelName(name string) string { return strings.ReplaceAll(name, ".", "_") }

func isLegacyName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		switch {
		case r == '_' || r == ':' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'):
		case i > 0 && r >= '0' && r <= '9':
		default:
			return false
		}
	}
	return true
}

// vectorSelector selects metric with sel. A UTF-8 name goes inside the braces,
// quoted; a legacy one stays outside so older Prometheus versions parse it.
func vectorSelector(metric, sel string) string {
	if isLegacyName(metric) {
		return metric + "{" + sel + "}"
	}
	if sel == "" {
		return "{" + strconv.Quote(metric) + "}"
	}
	return "{" + strconv.Quote(metric) + "," + sel + "}"
}

// substrateExpr builds one request's queries in its spelling.
type substrateExpr struct {
	sp substrateSpelling
	// sel: the pool or node matchers and the cluster scope.
	sel    string
	window string
}

func (x substrateExpr) m(attr, op, value string) string { return x.sp.match(attr, op, value) }

func (x substrateExpr) vec(i substrateInstrument, part string, extra ...string) string {
	sel := x.sel
	for _, e := range extra {
		sel = withScope(sel, e)
	}
	return vectorSelector(x.sp.metric(i, part), sel)
}

// norm copies dotted attributes to their escaped names, so a by clause can
// name them under either spelling.
func (x substrateExpr) norm(expr string, attrs ...string) string {
	if !x.sp.dotted {
		return expr
	}
	for _, a := range attrs {
		expr = "label_replace(" + expr + ", " + strconv.Quote(escapeOTelName(a)) + `, "$1", ` + strconv.Quote(a) + `, "(.*)")`
	}
	return expr
}

// rate collapses copies of one series that differ only by the labels an HA
// Prometheus pair or a federation adds, so they are not added twice.
func (x substrateExpr) rate(i substrateInstrument, part string, extra ...string) string {
	return collapseHA("rate(" + x.vec(i, part, extra...) + "[" + x.window + "])")
}

func collapseHA(expr string) string {
	return "max without (prometheus, prometheus_replica, replica) (" + expr + ")"
}

func (x substrateExpr) quantile(q string, i substrateInstrument, extra ...string) string {
	return "histogram_quantile(" + q + ", sum by (le) (" + x.rate(i, "_bucket", extra...) + "))"
}

// failureShare is failed observations as a percentage of all of them; no
// observations in a window leaves no point rather than 0%.
func (x substrateExpr) failureShare(i substrateInstrument, extra ...string) string {
	all := "sum(" + x.rate(i, "_count", extra...) + ")"
	failed := "sum(" + x.rate(i, "_count", append(extra, x.m("error.type", "!=", ""))...) + ")"
	return "100 * (" + failed + " or 0 * " + all + ") / (" + all + " > 0)"
}

// substrateNameLister lists the metric names Prometheus holds samples for.
type substrateNameLister interface {
	MetricNames(ctx context.Context, matches []string, start, end time.Time) ([]string, error)
}

const substrateNameMatch = `{__name__=~"(ate|atelet)[._].+"}`

// resolveSubstrateSpelling picks the spelling, and each instrument's name in
// it, that this Prometheus holds over the window. A name list holding one
// candidate per instrument, in one spelling, is taken as it is. Otherwise each
// candidate is checked for series of this pool or node within this cluster's
// scope: the one with series wins, and two with series are ambiguous, since
// the same activity could be counted under either name.
func resolveSubstrateSpelling(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, instruments []substrateInstrument, base func(substrateSpelling) string, window time.Duration) (substrateSpelling, string, error) {
	lister, ok := q.(substrateNameLister)
	if !ok {
		return substrateSpelling{}, "", nil
	}
	now := time.Now()
	names, err := lister.MetricNames(ctx, []string{substrateNameMatch}, now.Add(-window), now)
	if err != nil {
		return substrateSpelling{}, "", err
	}
	held := make(map[string]bool, len(names))
	for _, n := range names {
		held[n] = true
	}
	// present: per spelling, per instrument, the candidate stems the name list holds.
	present := func(dotted bool) map[string][]string {
		out := map[string][]string{}
		for _, inst := range instruments {
			for _, stem := range inst.stems(dotted) {
				if held[stem+inst.probeSuffix()] {
					out[inst.name] = append(out[inst.name], stem)
				}
			}
		}
		return out
	}
	escaped, dotted := present(false), present(true)
	unique := func(p map[string][]string) bool {
		for _, stems := range p {
			if len(stems) > 1 {
				return false
			}
		}
		return true
	}
	switch {
	case len(dotted) == 0 && unique(escaped):
		return spellingFrom(false, escaped, instruments), "", nil
	case len(escaped) == 0 && unique(dotted):
		return spellingFrom(true, dotted, instruments), "", nil
	}

	// live narrows each spelling's candidates to those with series for this
	// subject in this cluster.
	live := func(dotted bool, candidates map[string][]string) (map[string][]string, error) {
		out := map[string][]string{}
		if len(candidates) == 0 {
			return out, nil
		}
		sp := substrateSpelling{dotted: dotted}
		sel := base(sp)
		var first string
		for _, inst := range instruments {
			if stems := candidates[inst.name]; len(stems) > 0 {
				first = stems[0] + inst.probeSuffix()
				break
			}
		}
		matchers, _, err := resolve(ctx, scopeProbe{metric: first, selectors: []string{sel}, window: window, singlePartition: true, allowUnlabeledProof: true, strict: true})
		switch {
		case errors.Is(err, ErrScopeMismatch):
			// These series are another cluster's.
			return out, nil
		case errors.Is(err, ErrScopeAmbiguous):
			matchers = ""
		case err != nil:
			return nil, err
		}
		for _, inst := range instruments {
			for _, stem := range candidates[inst.name] {
				res, err := strictQuerier{q}.Query(ctx, "count(last_over_time("+vectorSelector(stem+inst.probeSuffix(), withScope(sel, matchers))+"["+window.String()+"]))")
				if err != nil {
					return nil, err
				}
				if len(res.Series) > 0 {
					out[inst.name] = append(out[inst.name], stem)
				}
			}
		}
		return out, nil
	}
	liveEscaped, err := live(false, escaped)
	if err != nil {
		return substrateSpelling{}, "", err
	}
	liveDotted, err := live(true, dotted)
	if err != nil {
		return substrateSpelling{}, "", err
	}
	const switched = "After a pipeline changes how it names metrics, this lasts until the range no longer spans the change"
	switch {
	case len(liveEscaped) > 0 && len(liveDotted) > 0:
		return substrateSpelling{}, "these series appear under both the underscore and the dotted (UTF-8) metric names in this range, so Radar cannot tell which holds the whole answer. " + switched, nil
	}
	isDotted, chosen, candidates := len(liveDotted) > 0 || (len(liveEscaped) == 0 && len(escaped) == 0), liveEscaped, escaped
	if isDotted {
		chosen, candidates = liveDotted, dotted
	}
	for _, inst := range instruments {
		if stems := chosen[inst.name]; len(stems) > 1 {
			return substrateSpelling{}, inst.name + " appears as both " + strings.Join(stems, " and ") + " for this in the range, so Radar cannot tell which holds the whole answer. " + switched, nil
		}
	}
	// Instruments with no series here fall back to a name the list holds.
	for name, stems := range candidates {
		if _, ok := chosen[name]; !ok {
			chosen[name] = stems[:1]
		}
	}
	return spellingFrom(isDotted, chosen, instruments), "", nil
}

// spellingFrom builds a spelling from one stem found per instrument. An
// instrument not found is named in the style of those found (with or without
// suffixes), so "not found" names what this pipeline would have produced.
func spellingFrom(dotted bool, found map[string][]string, instruments []substrateInstrument) substrateSpelling {
	sp := substrateSpelling{dotted: dotted, stems: map[string]string{}}
	style := 0
	for _, inst := range instruments {
		stems := inst.stems(dotted)
		if got := found[inst.name]; len(got) > 0 && len(stems) > 1 {
			// Only an instrument with suffixes to add or drop shows the style.
			style = slices.Index(stems, got[0])
			break
		}
	}
	for _, inst := range instruments {
		if got := found[inst.name]; len(got) > 0 {
			sp.stems[inst.name] = got[0]
			continue
		}
		stems := inst.stems(dotted)
		sp.stems[inst.name] = stems[min(style, len(stems)-1)]
	}
	return sp
}
