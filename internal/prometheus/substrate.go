package prometheus

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

// Agent Substrate charts read the series Substrate's components export, under
// either name spelling (substrate_naming.go). Histograms are read from their
// classic buckets; a native-histogram pipeline reads as "not found", naming the
// series that were checked.
//
// Substrate forbids actor identity in metric labels, so every chart is an
// aggregate. Pool charts read what Substrate attributes to a pool; node charts
// read atelet's series by the `node` label a Kubernetes scrape adds.

const (
	SubstrateChartOK               = "ok"
	SubstrateChartEmpty            = "empty"
	SubstrateChartNoSeries         = "noSeries"
	SubstrateChartNotAttributable  = "notAttributable"
	SubstrateChartDuplicateSources = "duplicateSources"
	SubstrateChartAmbiguous        = "ambiguous"
	SubstrateChartMismatch         = "mismatch"
	SubstrateChartError            = "error"

	// SubstrateNotFound: no chart has series for this pool or node in the
	// range (a histogram without classic buckets counts as none). It does not
	// say whether Substrate or the object exists.
	SubstrateNotFound = "notFound"
	// SubstrateNotOnNode: atelet series carry node labels, none of them this node's.
	SubstrateNotOnNode = "notOnNode"
	// SubstrateAvailable: some chart has data or a refusal of its own; each
	// chart's state, and the verdict's, still has to be read.
	SubstrateAvailable = "available"

	substrateMaxSeries   = 12
	substrateConcurrency = 4
)

// SubstrateChart is one chart: its series, or why there are none.
type SubstrateChart struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	Unit  string `json:"unit"`
	// Population says which observations the chart counts.
	Population string `json:"population"`
	// Source names the metrics read, in the spelling Prometheus holds them.
	Source string `json:"source"`
	State  string `json:"state"`
	// Reason says why a chart has no series; on an ok chart, which of its
	// named series has no data.
	Reason    string          `json:"reason,omitempty"`
	Isolation SeriesIsolation `json:"isolation"`
	// Series carry one display label, under the key series.
	Series []prom.Series `json:"series"`
	// Omitted counts the series beyond the first 12.
	Omitted int `json:"omitted,omitempty"`
}

// SubstrateHealthResponse is the curated Substrate chart set for one WorkerPool or Node.
type SubstrateHealthResponse struct {
	// Status is available, notFound, notOnNode or notAttributable (a node),
	// or error when every chart failed or was refused.
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
	// Range is the range served: one of parseTimeRange's names, 1h when the
	// request named none or an unknown one. Start and End are aligned to the
	// step, in Unix seconds.
	Range       string           `json:"range"`
	Start       int64            `json:"start"`
	End         int64            `json:"end"`
	StepSeconds int64            `json:"stepSeconds"`
	Charts      []SubstrateChart `json:"charts"`
	// Verdict is set when Status is available.
	Verdict *SubstrateVerdict `json:"verdict,omitempty"`
}

type substrateQuery struct {
	label string
	expr  string
}

type substrateChartDef struct {
	id, title, unit, population string
	inst                        substrateInstrument
	// also: other instruments the chart's queries read. Each is checked like
	// inst; the chart is drawn only when all of them resolve to one scope.
	also        []substrateInstrument
	queries     func(x substrateExpr) []substrateQuery
	emptyReason string
	// bucketed: the queries read the histogram's _bucket series, while its
	// presence is judged by _count.
	bucketed bool
}

func (d substrateChartDef) families() []substrateInstrument {
	return append([]substrateInstrument{d.inst}, d.also...)
}

func substrateRateWindow(step time.Duration) string {
	return max(2*time.Minute, 2*step).Round(time.Second).String()
}

func substratePoolCharts() []substrateChartDef {
	return []substrateChartDef{
		{
			id: "workers", title: "Workers by state", unit: "workers",
			population:  "Every worker Pod of this pool, by how many actor slots are in use and by sandbox class. A worker is a Pod, not an actor slot. Each API replica reports the whole pool, so the counts are averaged across replicas and can be fractional while they disagree. Workers of class unknown are not capacity.",
			inst:        instWorkers,
			emptyReason: "Substrate's API reported no workers for this pool in this range",
			queries: func(x substrateExpr) []substrateQuery {
				workers := x.norm(x.vec(instWorkers, ""), "ate.workerpool.namespace", "ate.workerpool.name", "ate.worker.state", "ate.sandbox.class")
				return []substrateQuery{{expr: `sum by (ate_worker_state, ate_sandbox_class) (avg by (ate_workerpool_namespace, ate_workerpool_name, ate_worker_state, ate_sandbox_class) (` + collapseHA(workers) + `))`}}
			},
		},
		{
			id: "readiness", title: "Ready and desired workers", unit: "count",
			population: "The pool's desired and ready worker counts as Substrate's controller recorded them over time.",
			inst:       instDesired,
			also:       []substrateInstrument{instReady},
			queries: func(x substrateExpr) []substrateQuery {
				return []substrateQuery{
					{label: "desired", expr: "max(" + x.vec(instDesired, "") + ")"},
					{label: "ready", expr: "max(" + x.vec(instReady, "") + ")"},
				}
			},
			emptyReason: "Substrate's controller exported no worker counts in this range",
		},
		{
			id: "resumeTime", bucketed: true, title: "Resume time", unit: "seconds",
			population:  "Successful resumes of actors placed on this pool. A resume that failed before a worker was assigned carries no pool and is not counted.",
			inst:        instLifecycle,
			emptyReason: "no actor resumed on this pool in this range",
			queries: func(x substrateExpr) []substrateQuery {
				ok := []string{x.m("ate.actor.operation.name", "=", "resume"), x.m("error.type", "=", "")}
				return []substrateQuery{
					{label: "p50", expr: x.quantile("0.5", instLifecycle, ok...)},
					{label: "p95", expr: x.quantile("0.95", instLifecycle, ok...)},
				}
			},
		},
		{
			id: "resumeFailures", title: "Failed resumes", unit: "percent",
			population:  "Resumes of actors placed on this pool that returned an error, as a share of all of them. A resume that failed before a worker was assigned carries no pool and is not counted.",
			inst:        instLifecycle,
			emptyReason: "no actor resumed on this pool in this range",
			queries: func(x substrateExpr) []substrateQuery {
				return []substrateQuery{{label: "failed", expr: x.failureShare(instLifecycle, x.m("ate.actor.operation.name", "=", "resume"))}}
			},
		},
		{
			id: "placementTime", bucketed: true, title: "Placement time", unit: "seconds",
			population:  "How long the scheduler took to place an actor on a worker of this pool. Placements that found no capacity name no pool, so they are not counted here.",
			inst:        instAssignment,
			emptyReason: "no actor was placed on this pool in this range",
			queries: func(x substrateExpr) []substrateQuery {
				return []substrateQuery{{label: "p95", expr: x.quantile("0.95", instAssignment, x.m("ate.scheduler.outcome", "=", "assigned"))}}
			},
		},
		{
			id: "crashes", title: "Actor crashes", unit: "per minute",
			population:  "Crashes of actors running on this pool's workers. An actor that crashed before it got a worker carries no pool and is not counted.",
			inst:        instCrashes,
			emptyReason: "no actor on this pool crashed in this range",
			queries: func(x substrateExpr) []substrateQuery {
				return []substrateQuery{{label: "crashes", expr: "60 * sum(" + x.rate(instCrashes, "") + ")"}}
			},
		},
	}
}

func substrateNodeCharts() []substrateChartDef {
	return []substrateChartDef{
		{
			id: "unsavedSnapshotAge", title: "Oldest snapshot not yet in storage", unit: "seconds",
			population:  "The age of the oldest actor snapshot on this node that is still only on local disk. While it grows, losing the node loses that actor's latest state.",
			inst:        instUndurable,
			emptyReason: "atelet reported no snapshot age on this node in this range",
			queries: func(x substrateExpr) []substrateQuery {
				return []substrateQuery{{label: "oldest", expr: "max(" + x.vec(instUndurable, "") + ")"}}
			},
		},
		{
			id: "overdueUploads", title: "Actors with overdue uploads", unit: "count",
			population:  "Actors whose snapshot upload to object storage is late, by how late.",
			inst:        instOverdue,
			emptyReason: "atelet reported no overdue uploads on this node in this range",
			queries: func(x substrateExpr) []substrateQuery {
				return []substrateQuery{{expr: "max by (ate_snapshot_upload_overdue_band) (" + x.norm(x.vec(instOverdue, ""), "ate.snapshot.upload_overdue_band") + ")"}}
			},
		},
		{
			id: "uploadTime", bucketed: true, title: "Snapshot upload time", unit: "seconds",
			population:  "Successful snapshot uploads from this node to object storage.",
			inst:        instUpload,
			emptyReason: "no snapshot was uploaded from this node in this range",
			queries: func(x substrateExpr) []substrateQuery {
				return []substrateQuery{{label: "p95", expr: x.quantile("0.95", instUpload, x.m("error.type", "=", ""))}}
			},
		},
		{
			id: "uploadFailures", title: "Failed snapshot uploads", unit: "percent",
			population:  "Snapshot uploads from this node that failed, as a share of all of them.",
			inst:        instUpload,
			emptyReason: "no snapshot was uploaded from this node in this range",
			queries: func(x substrateExpr) []substrateQuery {
				return []substrateQuery{{label: "failed", expr: x.failureShare(instUpload)}}
			},
		},
		{
			id: "restoreTime", bucketed: true, title: "Restore and checkpoint time", unit: "seconds",
			population:  "Whole restores and checkpoints of actors on this node. The two overlap in time and are not added together.",
			inst:        instRestore,
			also:        []substrateInstrument{instCheckpoint},
			emptyReason: "no actor was restored on this node in this range",
			queries: func(x substrateExpr) []substrateQuery {
				total := x.m("ate.snapshot.phase", "=", "total")
				return []substrateQuery{
					{label: "restore p95", expr: x.quantile("0.95", instRestore, total)},
					{label: "checkpoint p95", expr: x.quantile("0.95", instCheckpoint, total)},
				}
			},
		},
	}
}

// substrateFamilyCheck is what one chart's family probes established.
type substrateFamilyCheck struct {
	matchers  string
	isolation SeriesIsolation
	state     string
	reason    string
}

type substrateScopeResolver func(ctx context.Context, probe scopeProbe) (string, SeriesIsolation, error)

// checkSubstrateFamilies resolves the cluster scope of the instruments one
// chart reads, probed together so that two from different clusters are caught
// even when no cluster label proves either, and refuses a summed or averaged
// instrument reported through more than one scrape job. Each family selects
// the subject (base) in its own component's spelling. scopeOnly resolves the
// scope alone: the per-chart checks judge presence and duplicates within the
// subject.
func checkSubstrateFamilies(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, sps substrateSpellings, families []substrateInstrument, base func(substrateSpelling) string, window time.Duration, scopeOnly bool) substrateFamilyCheck {
	for _, f := range families {
		if ambiguous := sps.of(f).ambiguity(f); ambiguous != "" {
			return substrateFamilyCheck{state: SubstrateChartAmbiguous, reason: ambiguous}
		}
	}
	probe := func(f substrateInstrument) string { return sps.of(f).probe(f) }
	sel := func(f substrateInstrument) string { return base(sps.of(f)) }
	p := scopeProbe{metric: probe(families[0]), selectors: []string{sel(families[0])}, window: window, singlePartition: true, allowUnlabeledProof: true, strict: true,
		unverifiedNote: "Radar couldn't confirm these Substrate series belong to this exact cluster (no cluster label it could check)"}
	mixed := false
	for _, f := range families[1:] {
		p.additionalMetrics = append(p.additionalMetrics, probe(f))
		mixed = mixed || sel(f) != sel(families[0])
	}
	if mixed {
		// Components spelled differently name the subject's labels differently.
		p.selectors = []string{""}
		p.metricSelectors = make(map[string]string, len(families))
		for _, f := range families {
			p.metricSelectors[probe(f)] = sel(f)
		}
	}
	matchers, iso, err := resolve(ctx, p)
	switch {
	case errors.Is(err, ErrScopeAmbiguous):
		return substrateFamilyCheck{isolation: iso, state: SubstrateChartAmbiguous, reason: "these series appear under more than one cluster identity in this Prometheus, so any answer could mix clusters"}
	case errors.Is(err, ErrScopeMismatch):
		return substrateFamilyCheck{isolation: iso, state: SubstrateChartMismatch, reason: "this cluster's identity labels do not appear on these series"}
	case err != nil:
		return substrateFamilyCheck{isolation: iso, state: SubstrateChartError, reason: "Prometheus scope check failed: " + truncate(err.Error(), 200)}
	}
	check := substrateFamilyCheck{matchers: matchers, isolation: iso}
	if scopeOnly {
		return check
	}
	// A verified scope is accepted once any of the families appears under it;
	// one that appears only outside it would leave a paired chart half drawn.
	if matchers != "" && len(families) > 1 {
		for _, f := range families {
			present := func(s string) (bool, error) {
				res, err := strictQuerier{q}.Query(ctx, "count(count_over_time("+vectorSelector(probe(f), s)+"["+window.String()+"]))")
				return err == nil && len(res.Series) > 0, err
			}
			scoped, err := present(withScope(sel(f), matchers))
			if err == nil && !scoped {
				var anywhere bool
				if anywhere, err = present(sel(f)); err == nil && anywhere {
					check.state, check.reason = SubstrateChartMismatch, "this cluster's identity labels do not appear on "+sps.of(f).stem(f)+", which this chart combines"
					return check
				}
			}
			if err != nil {
				check.state, check.reason = SubstrateChartError, "Prometheus query failed: "+truncate(err.Error(), 200)
				return check
			}
		}
	}
	for _, f := range families {
		if !f.additive {
			continue
		}
		res, err := strictQuerier{q}.Query(ctx, "count by (job) (count_over_time("+vectorSelector(probe(f), withScope(sel(f), matchers))+"["+window.String()+"]))")
		if err != nil {
			check.state, check.reason = SubstrateChartError, "Prometheus query failed: "+truncate(err.Error(), 200)
			return check
		}
		if len(res.Series) > 1 {
			jobs := make([]string, 0, len(res.Series))
			for _, s := range res.Series {
				jobs = append(jobs, s.Labels["job"])
			}
			sort.Strings(jobs)
			check.state = SubstrateChartDuplicateSources
			check.reason = "reported through more than one scrape job (" + strings.Join(jobs, ", ") + "), so adding them up would count the same events twice"
			return check
		}
	}
	return check
}

func substrateFamiliesKey(families []substrateInstrument) string {
	names := make([]string, len(families))
	for i, f := range families {
		names[i] = f.name
	}
	return strings.Join(names, "|")
}

// substrateProbeWindow covers every sample a chart reads: the probes run now,
// up to a step after the aligned end, and the first point reads a rate window
// (or a gauge's lookback) before the start.
func substrateProbeWindow(start, end time.Time, step time.Duration) time.Duration {
	lookback := max(5*time.Minute, 2*time.Minute, 2*step)
	return (end.Sub(start) + step + lookback).Round(time.Second)
}

type substrateRequest struct {
	sps substrateSpellings
	// base selects the pool or node in one spelling.
	base       func(substrateSpelling) string
	charts     []substrateChartDef
	rangeName  string
	start, end time.Time
	step       time.Duration
}

// substrateRun is one request's charts and verdict, sharing their scope checks.
type substrateRun struct {
	ctx     context.Context
	q       seriesQuerier
	resolve substrateScopeResolver
	req     substrateRequest
	window  time.Duration
	checks  map[string]substrateFamilyCheck
}

func newSubstrateRun(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, req substrateRequest) *substrateRun {
	return &substrateRun{ctx: ctx, q: q, resolve: resolve, req: req, window: substrateProbeWindow(req.start, req.end, req.step), checks: map[string]substrateFamilyCheck{}}
}

// check is not safe for concurrent use; every check runs before the charts' queries.
func (r *substrateRun) check(families []substrateInstrument) substrateFamilyCheck {
	key := substrateFamiliesKey(families)
	if c, ok := r.checks[key]; ok {
		return c
	}
	c := checkSubstrateFamilies(r.ctx, r.q, r.resolve, r.req.sps, families, r.req.base, r.window, false)
	r.checks[key] = c
	return c
}

// expr builds queries over inst's component, in that component's spelling.
func (r *substrateRun) expr(inst substrateInstrument, matchers, window string) substrateExpr {
	sp := r.req.sps.of(inst)
	return substrateExpr{sp: sp, sel: withScope(r.req.base(sp), matchers), window: window}
}

func querySubstrateCharts(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, req substrateRequest) []SubstrateChart {
	return newSubstrateRun(ctx, q, resolve, req).charts()
}

func (r *substrateRun) charts() []SubstrateChart {
	req := r.req
	w := substrateRateWindow(req.step)
	checks := make([]substrateFamilyCheck, len(req.charts))
	for i, d := range req.charts {
		checks[i] = r.check(d.families())
	}

	charts := make([]SubstrateChart, len(req.charts))
	sem := make(chan struct{}, substrateConcurrency)
	var wg sync.WaitGroup
	for i, d := range req.charts {
		check := checks[i]
		c := &charts[i]
		*c = SubstrateChart{ID: d.id, Title: d.title, Unit: d.unit, Population: d.population, Source: req.sps.source(d.families()), Isolation: check.isolation, Series: []prom.Series{}}
		if check.state != "" {
			c.State, c.Reason = check.state, check.reason
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-r.ctx.Done():
				c.State, c.Reason = SubstrateChartError, r.ctx.Err().Error()
				return
			}
			defer func() { <-sem }()
			runSubstrateChart(r.ctx, strictQuerier{r.q}, d, r.expr(d.inst, check.matchers, w), req.start, req.end, req.step, c)
		}()
	}
	wg.Wait()
	return charts
}

func runSubstrateChart(ctx context.Context, q seriesQuerier, d substrateChartDef, x substrateExpr, start, end time.Time, step time.Duration, c *SubstrateChart) {
	series := []prom.Series{}
	var missing []string
	for _, query := range d.queries(x) {
		res, err := q.QueryRange(ctx, query.expr, start, end, step)
		if err != nil {
			c.State, c.Reason, c.Series = SubstrateChartError, "Prometheus query failed: "+truncate(err.Error(), 200), []prom.Series{}
			return
		}
		if query.label != "" && !hasSample(res.Series) {
			missing = append(missing, query.label)
		}
		for _, s := range res.Series {
			series = append(series, prom.Series{Labels: substrateSeriesLabels(d, query, s.Labels), DataPoints: s.DataPoints})
		}
	}
	sort.SliceStable(series, func(i, j int) bool { return series[i].Labels["series"] < series[j].Labels["series"] })
	if len(series) > substrateMaxSeries {
		c.Omitted = len(series) - substrateMaxSeries
		series = series[:substrateMaxSeries]
	}
	c.Series = series
	if hasSample(series) {
		c.State = SubstrateChartOK
		// A chart of several series must not pass for complete with one missing.
		if len(missing) > 0 {
			c.Reason = "no data for " + strings.Join(missing, ", ")
		}
		return
	}
	c.Series = []prom.Series{}
	window := end.Sub(start).String()
	present := func(metric string) (bool, error) {
		res, err := q.Query(ctx, "count(last_over_time("+vectorSelector(metric, x.sel)+"["+window+"]))")
		return err == nil && len(res.Series) > 0, err
	}
	sp := x.sp
	presence := sp.probe(d.inst)
	if d.bucketed {
		presence = sp.metric(d.inst, "_bucket")
	}
	found, err := present(presence)
	if err == nil && !found && d.bucketed {
		// Observations without classic buckets: a native-histogram pipeline,
		// or one that dropped the buckets. Not the same as no activity.
		for _, f := range d.families() {
			var counted bool
			if counted, err = present(sp.probe(f)); err != nil {
				break
			}
			if counted {
				c.State, c.Reason = SubstrateChartNoSeries, "Prometheus has "+sp.probe(f)+" but none of the classic histogram buckets (_bucket) Radar reads"
				return
			}
		}
	}
	switch {
	case err != nil:
		c.State, c.Reason = SubstrateChartError, "Prometheus query failed: "+truncate(err.Error(), 200)
	case found:
		c.State, c.Reason = SubstrateChartEmpty, d.emptyReason
	default:
		c.State, c.Reason = SubstrateChartNoSeries, "Prometheus has no "+c.Source+" series for this in the range"
	}
}

func hasSample(series []prom.Series) bool {
	for _, s := range series {
		for _, p := range s.DataPoints {
			if !math.IsNaN(p.Value) && !math.IsInf(p.Value, 0) {
				return true
			}
		}
	}
	return false
}

// substrateSeriesLabels keeps one display label per series.
func substrateSeriesLabels(d substrateChartDef, query substrateQuery, labels map[string]string) map[string]string {
	switch {
	case query.label != "":
		return map[string]string{"series": query.label}
	case d.id == "workers":
		name := labels["ate_worker_state"]
		if class := labels["ate_sandbox_class"]; class != "" {
			name += " (" + class + ")"
		}
		return map[string]string{"series": name}
	case d.id == "overdueUploads":
		return map[string]string{"series": labels["ate_snapshot_upload_overdue_band"]}
	}
	return map[string]string{"series": ""}
}

// substrateResponseStatus folds the charts into one status. Nothing found
// anywhere is notFound; a node whose atelet series exist only for other nodes
// is notOnNode.
func substrateResponseStatus(charts []SubstrateChart) (string, string) {
	notFound, failed := true, true
	reason := ""
	for _, c := range charts {
		switch c.State {
		case SubstrateChartNoSeries:
			failed = false
		case SubstrateChartError, SubstrateChartAmbiguous, SubstrateChartMismatch:
			notFound = false
			if reason == "" {
				reason = c.Reason
			}
		default:
			notFound, failed = false, false
		}
	}
	switch {
	case len(charts) > 0 && notFound:
		return SubstrateNotFound, ""
	case len(charts) > 0 && failed:
		return SubstrateChartError, reason
	}
	return SubstrateAvailable, ""
}

func substratePoolSelector(sp substrateSpelling, namespace, name string) string {
	return sp.match("ate.workerpool.namespace", "=", namespace) + "," + sp.match("ate.workerpool.name", "=", name)
}

func substrateNodeSelector(sp substrateSpelling, node string) string {
	return sp.match("node", "=", node)
}

// substrateNodeFamilies are the per-node instruments atelet exports that the
// node charts read.
var substrateNodeFamilies = []substrateInstrument{instUndurable, instOverdue, instUpload, instRestore, instCheckpoint}

// anyNodeFamily selects any of atelet's node families. Each is evaluated on
// its own before the union: a range function drops metric names, and series
// that differed only by name would collide.
func anyNodeFamily(sp substrateSpelling, sel string, window time.Duration) string {
	parts := make([]string, len(substrateNodeFamilies))
	for i, f := range substrateNodeFamilies {
		parts[i] = "last_over_time(" + vectorSelector(sp.probe(f), sel) + "[" + window.String() + "])"
	}
	return strings.Join(parts, " or ")
}

// substrateNodeAttribution decides whether atelet's series can be attributed
// to nodes at all, and to this node, over the window.
func substrateNodeAttribution(ctx context.Context, q seriesQuerier, sp substrateSpelling, matchers, node string, window time.Duration) (string, string, error) {
	count := func(sel string) (bool, error) {
		res, err := strictQuerier{q}.Query(ctx, "count("+anyNodeFamily(sp, withScope(sel, matchers), window)+")")
		if err != nil {
			return false, err
		}
		return len(res.Series) > 0, nil
	}
	present, err := count("")
	if err != nil || !present {
		return SubstrateNotFound, "", err
	}
	labelled, err := count(`node!=""`)
	if err != nil {
		return "", "", err
	}
	const noLabel = "atelet's series carry no node label, so Radar cannot tell which node reported them. A Kubernetes scrape adds one; see Substrate's reference Prometheus configuration"
	if !labelled {
		return SubstrateChartNotAttributable, noLabel, nil
	}
	mine, err := count(substrateNodeSelector(sp, node))
	if err != nil {
		return "", "", err
	}
	if !mine {
		// Unlabelled series could be this node's.
		unlabelled, err := count(`node=""`)
		if err != nil {
			return "", "", err
		}
		if unlabelled {
			return SubstrateChartNotAttributable, "some of atelet's series carry no node label, so Radar cannot tell whether this node reported any. A Kubernetes scrape adds one; see Substrate's reference Prometheus configuration", nil
		}
		return SubstrateNotOnNode, "", nil
	}
	return SubstrateAvailable, "", nil
}

// markUnattributedNodeCharts explains a node chart with no series of its own
// when its family is exported without a node label: those series could be
// this node's.
func markUnattributedNodeCharts(ctx context.Context, q seriesQuerier, sp substrateSpelling, matchers string, window time.Duration, charts []SubstrateChart) {
	defs := substrateNodeCharts()
	for i := range charts {
		if charts[i].State != SubstrateChartNoSeries {
			continue
		}
		for _, f := range defs[i].families() {
			res, err := strictQuerier{q}.Query(ctx, "count(last_over_time("+vectorSelector(sp.probe(f), withScope(`node=""`, matchers))+"["+window.String()+"]))")
			if err == nil && len(res.Series) > 0 {
				charts[i].State = SubstrateChartNotAttributable
				charts[i].Reason = sp.probe(f) + " is exported without a node label, so Radar cannot tell which node reported it"
				break
			}
		}
	}
}

// substrateRequestBounds reads the range parameter: one of parseTimeRange's
// names, 1h when it is missing or unknown. The name returned is the one served.
func substrateRequestBounds(r *http.Request) (string, time.Time, time.Time, time.Duration) {
	rangeName := normalizeTimeRange(r.URL.Query().Get("range"))
	start, end, step := alignSubstrateRange(parseTimeRange(rangeName))
	return rangeName, start, end, step
}

// alignSubstrateRange snaps both ends to the step so the window is a whole
// duration: PromQL range selectors reject fractional seconds.
func alignSubstrateRange(start, end time.Time, step time.Duration) (time.Time, time.Time, time.Duration) {
	window := end.Sub(start).Round(time.Second)
	end = end.Truncate(step)
	return end.Add(-window), end, step
}

func clientScopeResolver(client *Client) substrateScopeResolver {
	cache := k8s.GetResourceCache()
	return func(ctx context.Context, probe scopeProbe) (string, SeriesIsolation, error) {
		return client.resolveScope(ctx, trafficAnchorNamespace(cache), probe, nil, cache)
	}
}

// handleSubstrateWorkerPool returns the curated Substrate charts and the
// capacity verdict for one WorkerPool, gated on reading that WorkerPool.
func handleSubstrateWorkerPool(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !canRead(r, "ate.dev", "workerpools", namespace, "get") {
		writeError(w, http.StatusForbidden, "Substrate metrics for a WorkerPool require permission to get it.")
		return
	}
	serveSubstrate(w, r, func(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, rangeName string, start, end time.Time, step time.Duration) SubstrateHealthResponse {
		return collectSubstratePool(ctx, q, resolve, namespace, name, rangeName, start, end, step)
	})
}

var substratePoolInstruments = []substrateInstrument{instWorkers, instDesired, instReady, instLifecycle, instAssignment, instCrashes}

func collectSubstratePool(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, namespace, name, rangeName string, start, end time.Time, step time.Duration) SubstrateHealthResponse {
	failed := func(reason string) SubstrateHealthResponse {
		return SubstrateHealthResponse{Status: SubstrateChartError, Reason: reason, Range: rangeName, Charts: []SubstrateChart{}}
	}
	base := func(sp substrateSpelling) string { return substratePoolSelector(sp, namespace, name) }
	sps, err := resolveSubstrateSpellings(ctx, q, resolve, substratePoolInstruments, base, substrateProbeWindow(start, end, step))
	if err != nil {
		return failed(queryFailed(err))
	}
	run := newSubstrateRun(ctx, q, resolve, substrateRequest{sps: sps, base: base, charts: substratePoolCharts(), rangeName: rangeName, start: start, end: end, step: step})
	charts := run.charts()
	status, reason := substrateResponseStatus(charts)
	resp := SubstrateHealthResponse{Status: status, Reason: reason, Range: rangeName, Charts: charts}
	if status == SubstrateAvailable {
		resp.Verdict = run.poolVerdict(charts)
	}
	return resp
}

// handleSubstrateNode returns atelet's curated charts and the snapshot
// verdict for one Node, gated on reading Nodes like the Node resource charts.
func handleSubstrateNode(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "name")
	if !canRead(r, "", "nodes", "", "get") {
		writeError(w, http.StatusForbidden, "Substrate metrics for a Node require permission to get Nodes.")
		return
	}
	serveSubstrate(w, r, func(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, rangeName string, start, end time.Time, step time.Duration) SubstrateHealthResponse {
		return collectSubstrateNode(ctx, q, resolve, node, rangeName, start, end, step)
	})
}

func collectSubstrateNode(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, node, rangeName string, start, end time.Time, step time.Duration) SubstrateHealthResponse {
	failed := func(status, reason string) SubstrateHealthResponse {
		return SubstrateHealthResponse{Status: status, Reason: reason, Range: rangeName, Charts: []SubstrateChart{}}
	}
	window := substrateProbeWindow(start, end, step)
	base := func(sp substrateSpelling) string { return substrateNodeSelector(sp, node) }
	sps, err := resolveSubstrateSpellings(ctx, q, resolve, substrateNodeFamilies, base, window)
	if err != nil {
		return failed(SubstrateChartError, queryFailed(err))
	}
	sp := sps.of(instUndurable)
	if sp.ambiguous != "" {
		return failed(SubstrateChartError, sp.ambiguous)
	}
	// Attribution is decided within this cluster's series: another cluster's
	// node labels say nothing about this one's. Scope only: a family absent
	// here, or reported twice for another node, says nothing about this node's
	// charts.
	scope := checkSubstrateFamilies(ctx, q, resolve, sps, substrateNodeFamilies, func(substrateSpelling) string { return "" }, window, true)
	if scope.state != "" {
		return failed(SubstrateChartError, scope.reason)
	}
	status, reason, err := substrateNodeAttribution(ctx, q, sp, scope.matchers, node, window)
	if err != nil {
		return failed(SubstrateChartError, queryFailed(err))
	}
	if status != SubstrateAvailable {
		return failed(status, reason)
	}
	run := newSubstrateRun(ctx, q, resolve, substrateRequest{sps: sps, base: base, charts: substrateNodeCharts(), rangeName: rangeName, start: start, end: end, step: step})
	charts := run.charts()
	markUnattributedNodeCharts(ctx, q, sp, scope.matchers, window, charts)
	status, reason = substrateResponseStatus(charts)
	if status == SubstrateNotFound {
		// atelet reports for this node, just none of what these charts read.
		status = SubstrateAvailable
	}
	resp := SubstrateHealthResponse{Status: status, Reason: reason, Range: rangeName, Charts: charts}
	if status == SubstrateAvailable {
		resp.Verdict = run.nodeVerdict(charts)
	}
	return resp
}

type substrateCollect func(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, rangeName string, start, end time.Time, step time.Duration) SubstrateHealthResponse

func serveSubstrate(w http.ResponseWriter, r *http.Request, collect substrateCollect) {
	client, connectionErr := ClientForOperation()
	if connectionErr != nil {
		writeError(w, http.StatusServiceUnavailable, connectionErr.Error())
		return
	}
	generation, epoch := client.DiscoveryGeneration(), client.backendEpoch()
	rangeName, start, end, step := substrateRequestBounds(r)
	resp := collect(r.Context(), client, clientScopeResolver(client), rangeName, start, end, step)
	resp.Start, resp.End, resp.StepSeconds = start.Unix(), end.Unix(), int64(step/time.Second)
	// The charts and their scope checks must all come from one connection.
	if client != GetClient() || generation != client.DiscoveryGeneration() || epoch != client.backendEpoch() {
		writeError(w, http.StatusConflict, "metrics connection changed; retry the request")
		return
	}
	writeJSON(w, http.StatusOK, resp)
}
