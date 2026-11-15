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
// Each Substrate component's series are read in one spelling, so every
// selector, scope probe and aggregation over them agrees on the label names.
// Components can differ: an API server scraped under escaped names alongside a
// controller pushing dotted names over OTLP is a valid setup.

type instrumentKind int

const (
	instrumentGauge instrumentKind = iota
	instrumentCounter
	instrumentHistogram
)

// substrateInstrument is one OTel instrument Substrate exports, as named in
// its metric registry.
type substrateInstrument struct {
	name string
	// component is the process that emits it (the registry's emitted_by).
	component string
	kind      instrumentKind
	seconds   bool
	// additive: a chart sums or averages it across emitters, so the same
	// process observed through two scrape jobs would be counted twice. A chart
	// that takes a maximum is unaffected by a duplicate.
	additive bool
}

const (
	componentAPI        = "ateapi"
	componentController = "atecontroller"
	componentAtelet     = "atelet"
)

var (
	instWorkers    = substrateInstrument{name: "ate.workerpool.workers", component: componentAPI, additive: true}
	instDesired    = substrateInstrument{name: "ate.workerpool.desired_workers", component: componentController}
	instReady      = substrateInstrument{name: "ate.workerpool.ready_workers", component: componentController}
	instLifecycle  = substrateInstrument{name: "ate.actor.lifecycle.operation.duration", component: componentAPI, kind: instrumentHistogram, seconds: true, additive: true}
	instAssignment = substrateInstrument{name: "ate.scheduler.assignment.duration", component: componentAPI, kind: instrumentHistogram, seconds: true, additive: true}
	instCrashes    = substrateInstrument{name: "ate.actor.crashes", component: componentAPI, kind: instrumentCounter, additive: true}
	instUndurable  = substrateInstrument{name: "atelet.snapshot.undurable.age.max", component: componentAtelet, seconds: true}
	instOverdue    = substrateInstrument{name: "atelet.snapshot.upload.overdue.actors", component: componentAtelet}
	instUpload     = substrateInstrument{name: "atelet.snapshot.upload.duration", component: componentAtelet, kind: instrumentHistogram, seconds: true, additive: true}
	instRestore    = substrateInstrument{name: "ate.actor.restore.duration", component: componentAtelet, kind: instrumentHistogram, seconds: true, additive: true}
	instCheckpoint = substrateInstrument{name: "ate.actor.checkpoint.duration", component: componentAtelet, kind: instrumentHistogram, seconds: true, additive: true}
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
	// ambiguous: why this component's series cannot be read in one spelling.
	ambiguous string
}

// substrateSpellings is the spelling of each component's series.
type substrateSpellings map[string]substrateSpelling

// of returns the spelling i's component uses; a component never resolved
// reads the escaped default.
func (s substrateSpellings) of(i substrateInstrument) substrateSpelling { return s[i.component] }

func (s substrateSpellings) source(instruments []substrateInstrument) string {
	names := make([]string, len(instruments))
	for i, inst := range instruments {
		names[i] = s.of(inst).stem(inst)
	}
	return strings.Join(names, ", ")
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
	return "max without (prometheus, prometheus_replica, replica, __replica__) (" + expr + ")"
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

// resolveSubstrateSpellings resolves each component's spelling on its own
// (resolveSubstrateSpelling), from one name list. A component whose series
// two names could hold is marked ambiguous, which refuses only the charts
// that read it. A component Prometheus holds nothing for is named in the
// spelling of one it holds, so "not found" names what this pipeline would
// have produced.
func resolveSubstrateSpellings(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, instruments []substrateInstrument, base func(substrateSpelling) string, window time.Duration) (substrateSpellings, error) {
	held, err := substrateNamesHeld(ctx, q, window)
	if err != nil || held == nil {
		return substrateSpellings{}, err
	}
	var components []string
	groups := map[string][]substrateInstrument{}
	for _, inst := range instruments {
		if _, ok := groups[inst.component]; !ok {
			components = append(components, inst.component)
		}
		groups[inst.component] = append(groups[inst.component], inst)
	}
	sps := substrateSpellings{}
	var unheld []string
	fallback, haveFallback := substrateSpelling{}, false
	for _, c := range components {
		sp, found, ambiguous, err := resolveSpellingFrom(ctx, q, resolve, held, groups[c], base, window)
		if err != nil {
			return nil, err
		}
		sp.ambiguous = ambiguous
		sps[c] = sp
		switch {
		case !found:
			unheld = append(unheld, c)
		case !haveFallback && ambiguous == "":
			fallback, haveFallback = sp, true
		}
	}
	if haveFallback {
		for _, c := range unheld {
			sps[c] = spellingFrom(fallback.dotted, nil, groups[c])
		}
	}
	return sps, nil
}

// substrateNamesHeld is the set of Substrate metric names Prometheus holds
// samples for over the window, or nil when q cannot list names.
func substrateNamesHeld(ctx context.Context, q seriesQuerier, window time.Duration) (map[string]bool, error) {
	lister, ok := q.(substrateNameLister)
	if !ok {
		return nil, nil
	}
	now := time.Now()
	names, err := lister.MetricNames(ctx, []string{substrateNameMatch}, now.Add(-window), now)
	if err != nil {
		return nil, err
	}
	held := make(map[string]bool, len(names))
	for _, n := range names {
		held[n] = true
	}
	return held, nil
}

// resolveSubstrateSpelling picks the spelling, and each instrument's name in
// it, that this Prometheus holds over the window for one component's
// instruments. A name list holding one candidate per instrument, in one
// spelling, is taken as it is. Otherwise each candidate is checked for series
// of this pool or node within this cluster's scope: the one with series wins,
// and two with series are ambiguous, since the same activity could be counted
// under either name.
func resolveSubstrateSpelling(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, instruments []substrateInstrument, base func(substrateSpelling) string, window time.Duration) (substrateSpelling, string, error) {
	held, err := substrateNamesHeld(ctx, q, window)
	if err != nil || held == nil {
		return substrateSpelling{}, "", err
	}
	sp, _, ambiguous, err := resolveSpellingFrom(ctx, q, resolve, held, instruments, base, window)
	return sp, ambiguous, err
}

// resolveSpellingFrom is resolveSubstrateSpelling over a fetched name list;
// found reports whether the list holds any candidate name.
func resolveSpellingFrom(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, held map[string]bool, instruments []substrateInstrument, base func(substrateSpelling) string, window time.Duration) (sp substrateSpelling, found bool, ambiguous string, err error) {
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
	found = len(escaped) > 0 || len(dotted) > 0
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
		return spellingFrom(false, escaped, instruments), found, "", nil
	case len(escaped) == 0 && unique(dotted):
		return spellingFrom(true, dotted, instruments), found, "", nil
	}

	// live narrows each spelling's candidates to those with series for this
	// subject in this cluster.
	live := func(dotted bool, candidates map[string][]string) (map[string][]string, error) {
		out := map[string][]string{}
		if len(candidates) == 0 {
			return out, nil
		}
		sel := base(substrateSpelling{dotted: dotted})
		for _, inst := range instruments {
			for _, stem := range candidates[inst.name] {
				// Each name is scoped on its own: one held only by another
				// cluster says nothing about the others.
				matchers, _, err := resolve(ctx, scopeProbe{metric: stem + inst.probeSuffix(), selectors: []string{sel}, window: window, singlePartition: true, allowUnlabeledProof: true, strict: true})
				switch {
				case errors.Is(err, ErrScopeMismatch):
					continue
				case errors.Is(err, ErrScopeAmbiguous):
					matchers = ""
				case err != nil:
					return nil, err
				}
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
		return substrateSpelling{}, found, "", err
	}
	liveDotted, err := live(true, dotted)
	if err != nil {
		return substrateSpelling{}, found, "", err
	}
	const switched = "This clears once the range no longer spans a change in how the pipeline names metrics; if both keep arriving, the component is exported twice"
	switch {
	case len(liveEscaped) > 0 && len(liveDotted) > 0:
		return substrateSpelling{}, found, instruments[0].component + "'s series appear under both the underscore and the dotted (UTF-8) metric names in this range, so Radar cannot tell which holds the whole answer. " + switched, nil
	}
	isDotted, chosen, candidates := len(liveDotted) > 0 || (len(liveEscaped) == 0 && len(escaped) == 0), liveEscaped, escaped
	if isDotted {
		chosen, candidates = liveDotted, dotted
	}
	for _, inst := range instruments {
		if stems := chosen[inst.name]; len(stems) > 1 {
			return substrateSpelling{}, found, inst.name + " appears as both " + strings.Join(stems, " and ") + " for this in the range, so Radar cannot tell which holds the whole answer. " + switched, nil
		}
	}
	// Instruments with no series here fall back to a name the list holds.
	for name, stems := range candidates {
		if _, ok := chosen[name]; !ok {
			chosen[name] = stems[:1]
		}
	}
	return spellingFrom(isDotted, chosen, instruments), found, "", nil
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
