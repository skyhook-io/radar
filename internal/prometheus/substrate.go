package prometheus

import (
	"context"
	"errors"
	"math"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
)

// Agent Substrate charts read the series Substrate's components export under
// their default names: the OTel Prometheus exporter, and Prometheus's own OTLP
// receiver, both escape names with underscores and add unit and type suffixes
// (ate.actor.crashes → ate_actor_crashes_total, a histogram in seconds →
// *_seconds_bucket). A pipeline configured to keep UTF-8 names, or to send
// native histograms, reads as "not found", naming the series that were checked.
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

	// SubstrateNotFound: Prometheus holds none of the series the charts read.
	SubstrateNotFound = "notFound"
	// SubstrateNotOnNode: atelet series carry node labels, none of them this node's.
	SubstrateNotOnNode = "notOnNode"
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
	Population string          `json:"population"`
	Source     string          `json:"source"`
	SeriesBy   string          `json:"seriesBy,omitempty"`
	State      string          `json:"state"`
	Reason     string          `json:"reason,omitempty"`
	Isolation  SeriesIsolation `json:"isolation"`
	Series     []prom.Series   `json:"series"`
	Omitted    int             `json:"omitted,omitempty"`
	Steps      int             `json:"steps"`
	Covered    int             `json:"covered"`
}

// SubstrateHealthResponse is the curated Substrate chart set for one WorkerPool or Node.
type SubstrateHealthResponse struct {
	Status      string           `json:"status"`
	Reason      string           `json:"reason,omitempty"`
	Range       string           `json:"range"`
	Start       int64            `json:"start"`
	End         int64            `json:"end"`
	StepSeconds int64            `json:"stepSeconds"`
	Charts      []SubstrateChart `json:"charts"`
}

type substrateQuery struct {
	label string
	expr  string
}

// substrateFamily is one exported instrument a chart derives from: its scope,
// presence and duplicate-source checks run on it.
type substrateFamily struct {
	metric string
	// additive: the chart sums or averages across emitters, so the same
	// process observed through two scrape jobs would be counted twice or weigh
	// twice. Only a chart that takes a maximum is unaffected by a duplicate.
	additive bool
}

type substrateChartDef struct {
	id, title, unit, population, source, seriesBy string
	family                                        substrateFamily
	// also: other families the chart's queries read. Each is checked like
	// family; the chart is drawn only when all of them resolve to one scope.
	also        []substrateFamily
	queries     func(sel, window string) []substrateQuery
	emptyReason string
	// presence: the series the chart's queries actually read, when that is
	// not family (a quantile reads _bucket; family is _count).
	presence string
}

func substrateRateWindow(step time.Duration) string {
	return max(2*time.Minute, 2*step).Round(time.Second).String()
}

// rate collapses copies of one series that differ only by the labels an HA
// Prometheus pair or a federation adds, so they are not added twice.
func rate(metric, sel, window string) string {
	return collapseHA("rate(" + metric + "{" + sel + "}[" + window + "])")
}

func collapseHA(expr string) string {
	return "max without (prometheus, prometheus_replica, replica) (" + expr + ")"
}

func quantile(q, bucket, sel, window string) string {
	return "histogram_quantile(" + q + ", sum by (le) (" + rate(bucket, sel, window) + "))"
}

// failureShare is failed observations as a percentage of all of them, over the
// same selector; no observations in a window leaves no point rather than 0%.
func failureShare(count, sel, window string) string {
	all := "sum(" + rate(count, sel, window) + ")"
	failed := "sum(" + rate(count, sel+`,error_type!=""`, window) + ")"
	return "100 * (" + failed + " or 0 * " + all + ") / (" + all + " > 0)"
}

func substratePoolCharts() []substrateChartDef {
	workers := substrateFamily{metric: "ate_workerpool_workers", additive: true}
	lifecycle := substrateFamily{metric: "ate_actor_lifecycle_operation_duration_seconds_count", additive: true}
	assignment := substrateFamily{metric: "ate_scheduler_assignment_duration_seconds_count", additive: true}
	return []substrateChartDef{
		{
			id: "workers", title: "Workers by state", unit: "workers", seriesBy: "state",
			population: "Every worker Pod of this pool, by how many actor slots are in use and by sandbox class. A worker is a Pod, not an actor slot. Each API replica reports the whole pool, so the counts are averaged across replicas and can be fractional while they disagree. Workers of class unknown are not capacity.",
			source:     "ate_workerpool_workers",
			family:     workers,
			queries: func(sel, _ string) []substrateQuery {
				return []substrateQuery{{expr: `sum by (ate_worker_state, ate_sandbox_class) (avg by (ate_workerpool_namespace, ate_workerpool_name, ate_worker_state, ate_sandbox_class) (` + collapseHA("ate_workerpool_workers{"+sel+"}") + `))`}}
			},
		},
		{
			id: "readiness", title: "Ready and desired workers", unit: "count", seriesBy: "series",
			population: "The pool's desired and ready worker counts as Substrate's controller recorded them over time.",
			source:     "ate_workerpool_desired_workers, ate_workerpool_ready_workers",
			family:     substrateFamily{metric: "ate_workerpool_desired_workers"},
			also:       []substrateFamily{{metric: "ate_workerpool_ready_workers"}},
			queries: func(sel, _ string) []substrateQuery {
				return []substrateQuery{
					{label: "desired", expr: "max(ate_workerpool_desired_workers{" + sel + "})"},
					{label: "ready", expr: "max(ate_workerpool_ready_workers{" + sel + "})"},
				}
			},
			emptyReason: "Substrate's controller exported no worker counts in this range",
		},
		{
			id: "resumeTime", presence: "ate_actor_lifecycle_operation_duration_seconds_bucket", title: "Resume time", unit: "seconds", seriesBy: "series",
			population:  "Successful resumes of actors placed on this pool. A resume that failed before a worker was assigned carries no pool and is not counted.",
			source:      "ate_actor_lifecycle_operation_duration_seconds",
			family:      lifecycle,
			emptyReason: "no actor resumed on this pool in this range",
			queries: func(sel, w string) []substrateQuery {
				ok := sel + `,ate_actor_operation_name="resume",error_type=""`
				return []substrateQuery{
					{label: "p50", expr: quantile("0.5", "ate_actor_lifecycle_operation_duration_seconds_bucket", ok, w)},
					{label: "p95", expr: quantile("0.95", "ate_actor_lifecycle_operation_duration_seconds_bucket", ok, w)},
				}
			},
		},
		{
			id: "resumeFailures", title: "Failed resumes", unit: "percent",
			population:  "Resumes of actors placed on this pool that returned an error, as a share of all of them. A resume that failed before a worker was assigned carries no pool and is not counted.",
			source:      "ate_actor_lifecycle_operation_duration_seconds_count",
			family:      lifecycle,
			emptyReason: "no actor resumed on this pool in this range",
			queries: func(sel, w string) []substrateQuery {
				return []substrateQuery{{label: "failed", expr: failureShare("ate_actor_lifecycle_operation_duration_seconds_count", sel+`,ate_actor_operation_name="resume"`, w)}}
			},
		},
		{
			id: "placementTime", presence: "ate_scheduler_assignment_duration_seconds_bucket", title: "Placement time", unit: "seconds", seriesBy: "series",
			population:  "How long the scheduler took to place an actor on a worker of this pool. Placements that found no capacity name no pool, so they are not counted here.",
			source:      "ate_scheduler_assignment_duration_seconds",
			family:      assignment,
			emptyReason: "no actor was placed on this pool in this range",
			queries: func(sel, w string) []substrateQuery {
				placed := sel + `,ate_scheduler_outcome="assigned"`
				return []substrateQuery{{label: "p95", expr: quantile("0.95", "ate_scheduler_assignment_duration_seconds_bucket", placed, w)}}
			},
		},
		{
			id: "crashes", title: "Actor crashes", unit: "per minute",
			population:  "Crashes of actors running on this pool's workers. An actor that crashed before it got a worker carries no pool and is not counted.",
			source:      "ate_actor_crashes_total",
			family:      substrateFamily{metric: "ate_actor_crashes_total", additive: true},
			emptyReason: "no actor on this pool crashed in this range",
			queries: func(sel, w string) []substrateQuery {
				return []substrateQuery{{label: "crashes", expr: "60 * sum(" + rate("ate_actor_crashes_total", sel, w) + ")"}}
			},
		},
	}
}

func substrateNodeCharts() []substrateChartDef {
	return []substrateChartDef{
		{
			id: "unsavedSnapshotAge", title: "Oldest snapshot not yet in storage", unit: "seconds",
			population:  "The age of the oldest actor snapshot on this node that is still only on local disk. While it grows, losing the node loses that actor's latest state.",
			source:      "atelet_snapshot_undurable_age_max_seconds",
			family:      substrateFamily{metric: "atelet_snapshot_undurable_age_max_seconds"},
			emptyReason: "atelet reported no snapshot age on this node in this range",
			queries: func(sel, _ string) []substrateQuery {
				return []substrateQuery{{label: "oldest", expr: "max(atelet_snapshot_undurable_age_max_seconds{" + sel + "})"}}
			},
		},
		{
			id: "overdueUploads", title: "Actors with overdue uploads", unit: "count", seriesBy: "band",
			population:  "Actors whose snapshot upload to object storage is late, by how late.",
			source:      "atelet_snapshot_upload_overdue_actors",
			family:      substrateFamily{metric: "atelet_snapshot_upload_overdue_actors"},
			emptyReason: "atelet reported no overdue uploads on this node in this range",
			queries: func(sel, _ string) []substrateQuery {
				return []substrateQuery{{expr: "max by (ate_snapshot_upload_overdue_band) (atelet_snapshot_upload_overdue_actors{" + sel + "})"}}
			},
		},
		{
			id: "uploadTime", presence: "atelet_snapshot_upload_duration_seconds_bucket", title: "Snapshot upload time", unit: "seconds", seriesBy: "series",
			population:  "Successful snapshot uploads from this node to object storage.",
			source:      "atelet_snapshot_upload_duration_seconds",
			family:      substrateFamily{metric: "atelet_snapshot_upload_duration_seconds_count", additive: true},
			emptyReason: "no snapshot was uploaded from this node in this range",
			queries: func(sel, w string) []substrateQuery {
				return []substrateQuery{{label: "p95", expr: quantile("0.95", "atelet_snapshot_upload_duration_seconds_bucket", sel+`,error_type=""`, w)}}
			},
		},
		{
			id: "uploadFailures", title: "Failed snapshot uploads", unit: "percent",
			population:  "Snapshot uploads from this node that failed, as a share of all of them.",
			source:      "atelet_snapshot_upload_duration_seconds_count",
			family:      substrateFamily{metric: "atelet_snapshot_upload_duration_seconds_count", additive: true},
			emptyReason: "no snapshot was uploaded from this node in this range",
			queries: func(sel, w string) []substrateQuery {
				return []substrateQuery{{label: "failed", expr: failureShare("atelet_snapshot_upload_duration_seconds_count", sel, w)}}
			},
		},
		{
			id: "restoreTime", presence: "ate_actor_restore_duration_seconds_bucket", title: "Restore and checkpoint time", unit: "seconds", seriesBy: "series",
			population:  "Whole restores and checkpoints of actors on this node. The two overlap in time and are not added together.",
			source:      "ate_actor_restore_duration_seconds, ate_actor_checkpoint_duration_seconds",
			family:      substrateFamily{metric: "ate_actor_restore_duration_seconds_count", additive: true},
			also:        []substrateFamily{{metric: "ate_actor_checkpoint_duration_seconds_count", additive: true}},
			emptyReason: "no actor was restored on this node in this range",
			queries: func(sel, w string) []substrateQuery {
				total := sel + `,ate_snapshot_phase="total"`
				return []substrateQuery{
					{label: "restore p95", expr: quantile("0.95", "ate_actor_restore_duration_seconds_bucket", total, w)},
					{label: "checkpoint p95", expr: quantile("0.95", "ate_actor_checkpoint_duration_seconds_bucket", total, w)},
				}
			},
		},
	}
}

// substrateFamilyCheck is what one family's probes established.
type substrateFamilyCheck struct {
	matchers  string
	isolation SeriesIsolation
	state     string
	reason    string
}

type substrateScopeResolver func(ctx context.Context, probe scopeProbe) (string, SeriesIsolation, error)

// checkSubstrateFamilies resolves the cluster scope of the families one chart
// reads, probed together so that two families from different clusters are
// caught even when no cluster label proves either, and refuses a summed or
// averaged family reported through more than one scrape job.
func checkSubstrateFamilies(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, families []substrateFamily, sel string, window time.Duration, requireEach bool) substrateFamilyCheck {
	extra := make([]string, 0, len(families)-1)
	for _, f := range families[1:] {
		extra = append(extra, f.metric)
	}
	matchers, iso, err := resolve(ctx, scopeProbe{metric: families[0].metric, additionalMetrics: extra, selectors: []string{sel}, window: window, singlePartition: true, allowUnlabeledProof: true, strict: true,
		unverifiedNote: "Radar couldn't confirm these Substrate series belong to this exact cluster (no cluster label it could check)"})
	switch {
	case errors.Is(err, ErrScopeAmbiguous):
		return substrateFamilyCheck{isolation: iso, state: SubstrateChartAmbiguous, reason: "these series appear under more than one cluster identity in this Prometheus, so any answer could mix clusters"}
	case errors.Is(err, ErrScopeMismatch):
		return substrateFamilyCheck{isolation: iso, state: SubstrateChartMismatch, reason: "this cluster's identity labels do not appear on these series"}
	case err != nil:
		return substrateFamilyCheck{isolation: iso, state: SubstrateChartError, reason: "Prometheus scope check failed: " + truncate(err.Error(), 200)}
	}
	check := substrateFamilyCheck{matchers: matchers, isolation: iso}
	// A verified scope is accepted once any of the families appears under it;
	// one that appears only outside it would leave a paired chart half drawn.
	if requireEach && matchers != "" && len(families) > 1 {
		for _, f := range families {
			present := func(s string) (bool, error) {
				res, err := strictQuerier{q}.Query(ctx, "count(count_over_time("+f.metric+"{"+s+"}["+window.String()+"]))")
				return err == nil && len(res.Series) > 0, err
			}
			scoped, err := present(withScope(sel, matchers))
			if err == nil && !scoped {
				var anywhere bool
				if anywhere, err = present(sel); err == nil && anywhere {
					check.state, check.reason = SubstrateChartMismatch, "this cluster's identity labels do not appear on "+f.metric+", which this chart combines"
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
		res, err := strictQuerier{q}.Query(ctx, "count by (job) (count_over_time("+f.metric+"{"+withScope(sel, matchers)+"}["+window.String()+"]))")
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

func (d substrateChartDef) families() []substrateFamily {
	return append([]substrateFamily{d.family}, d.also...)
}

func substrateFamiliesKey(families []substrateFamily) string {
	metrics := make([]string, len(families))
	for i, f := range families {
		metrics[i] = f.metric
	}
	return strings.Join(metrics, "|")
}

// substrateProbeWindow covers every sample a chart reads: the probes run now,
// up to a step after the aligned end, and the first point reads a rate window
// (or a gauge's lookback) before the start.
func substrateProbeWindow(start, end time.Time, step time.Duration) time.Duration {
	lookback := max(5*time.Minute, 2*time.Minute, 2*step)
	return (end.Sub(start) + step + lookback).Round(time.Second)
}

type substrateRequest struct {
	sel        string
	charts     []substrateChartDef
	rangeName  string
	start, end time.Time
	step       time.Duration
}

func querySubstrateCharts(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, req substrateRequest) []SubstrateChart {
	window := req.end.Sub(req.start)
	w := substrateRateWindow(req.step)
	steps := max(0, int(window/req.step)+1)

	probeWindow := substrateProbeWindow(req.start, req.end, req.step)
	checks := map[string]substrateFamilyCheck{}
	for _, d := range req.charts {
		families := append([]substrateFamily{d.family}, d.also...)
		key := substrateFamiliesKey(families)
		if _, ok := checks[key]; !ok {
			checks[key] = checkSubstrateFamilies(ctx, q, resolve, families, req.sel, probeWindow, true)
		}
	}

	charts := make([]SubstrateChart, len(req.charts))
	sem := make(chan struct{}, substrateConcurrency)
	var wg sync.WaitGroup
	for i, d := range req.charts {
		check := checks[substrateFamiliesKey(append([]substrateFamily{d.family}, d.also...))]
		c := &charts[i]
		*c = SubstrateChart{ID: d.id, Title: d.title, Unit: d.unit, Population: d.population, Source: d.source, SeriesBy: d.seriesBy, Isolation: check.isolation, Series: []prom.Series{}, Steps: steps}
		if check.state != "" {
			c.State, c.Reason = check.state, check.reason
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			select {
			case sem <- struct{}{}:
			case <-ctx.Done():
				c.State, c.Reason = SubstrateChartError, ctx.Err().Error()
				return
			}
			defer func() { <-sem }()
			runSubstrateChart(ctx, strictQuerier{q}, d, withScope(req.sel, check.matchers), w, req.start, req.end, req.step, c)
		}()
	}
	wg.Wait()
	return charts
}

func runSubstrateChart(ctx context.Context, q seriesQuerier, d substrateChartDef, sel, w string, start, end time.Time, step time.Duration, c *SubstrateChart) {
	series := []prom.Series{}
	var missing []string
	for _, query := range d.queries(sel, w) {
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
	covered := map[int64]bool{}
	for _, s := range series {
		for _, p := range s.DataPoints {
			if !math.IsNaN(p.Value) && !math.IsInf(p.Value, 0) {
				covered[p.Timestamp] = true
			}
		}
	}
	c.Series, c.Covered = series, len(covered)
	if c.Covered > 0 {
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
		res, err := q.Query(ctx, "count(last_over_time("+metric+"{"+sel+"}["+window+"]))")
		return err == nil && len(res.Series) > 0, err
	}
	presence := d.family.metric
	if d.presence != "" {
		presence = d.presence
	}
	found, err := present(presence)
	if err == nil && !found && presence != d.family.metric {
		// Observations without classic buckets: a native-histogram pipeline,
		// or one that dropped the buckets. Not the same as no activity.
		for _, f := range d.families() {
			var counted bool
			if counted, err = present(f.metric); err != nil {
				break
			}
			if counted {
				c.State, c.Reason = SubstrateChartNoSeries, "Prometheus has "+f.metric+" but none of the classic histogram buckets (_bucket) Radar reads"
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
		c.State, c.Reason = SubstrateChartNoSeries, "Prometheus has no "+d.source+" series for this in the range"
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

func substratePoolSelector(namespace, name string) string {
	return "ate_workerpool_namespace=" + strconv.Quote(namespace) + ",ate_workerpool_name=" + strconv.Quote(name)
}

func substrateNodeSelector(node string) string {
	return "node=" + strconv.Quote(node)
}

// substrateNodeAttribution decides whether atelet's series can be attributed
// to nodes at all, and to this node, over the window.
// substrateNodeFamilies are the per-node families atelet exports that the
// node charts read.
var substrateNodeFamilies = []string{
	"atelet_snapshot_undurable_age_max_seconds",
	"atelet_snapshot_upload_overdue_actors",
	"atelet_snapshot_upload_duration_seconds_count",
	"ate_actor_restore_duration_seconds_count",
	"ate_actor_checkpoint_duration_seconds_count",
}

// anyNodeFamily selects any of atelet's node families. Each is evaluated on
// its own before the union: a range function drops metric names, and series
// that differed only by name would collide.
func anyNodeFamily(sel string, window time.Duration) string {
	parts := make([]string, len(substrateNodeFamilies))
	for i, f := range substrateNodeFamilies {
		parts[i] = "last_over_time(" + f + "{" + sel + "}[" + window.String() + "])"
	}
	return strings.Join(parts, " or ")
}

func substrateNodeAttribution(ctx context.Context, q seriesQuerier, matchers, node string, window time.Duration) (string, string, error) {
	count := func(sel string) (bool, error) {
		res, err := strictQuerier{q}.Query(ctx, "count("+anyNodeFamily(withScope(sel, matchers), window)+")")
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
	mine, err := count(substrateNodeSelector(node))
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
func markUnattributedNodeCharts(ctx context.Context, q seriesQuerier, matchers string, window time.Duration, charts []SubstrateChart) {
	defs := substrateNodeCharts()
	for i := range charts {
		if charts[i].State != SubstrateChartNoSeries {
			continue
		}
		for _, f := range defs[i].families() {
			res, err := strictQuerier{q}.Query(ctx, "count(last_over_time("+f.metric+"{"+withScope(`node=""`, matchers)+"}["+window.String()+"]))")
			if err == nil && len(res.Series) > 0 {
				charts[i].State = SubstrateChartNotAttributable
				charts[i].Reason = f.metric + " is exported without a node label, so Radar cannot tell which node reported it"
				break
			}
		}
	}
}

func substrateRequestBounds(r *http.Request) (string, time.Time, time.Time, time.Duration) {
	rangeName := r.URL.Query().Get("range")
	if rangeName == "" {
		rangeName = "1h"
	}
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

// handleSubstrateWorkerPool returns the curated Substrate charts for one
// WorkerPool, gated on reading that WorkerPool.
func handleSubstrateWorkerPool(w http.ResponseWriter, r *http.Request) {
	namespace, name := chi.URLParam(r, "namespace"), chi.URLParam(r, "name")
	if !canRead(r, "ate.dev", "workerpools", namespace, "get") {
		writeError(w, http.StatusForbidden, "Substrate metrics for a WorkerPool require permission to get it.")
		return
	}
	serveSubstrate(w, r, func(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, rangeName string, start, end time.Time, step time.Duration) SubstrateHealthResponse {
		charts := querySubstrateCharts(ctx, q, resolve, substrateRequest{sel: substratePoolSelector(namespace, name), charts: substratePoolCharts(), rangeName: rangeName, start: start, end: end, step: step})
		status, reason := substrateResponseStatus(charts)
		return SubstrateHealthResponse{Status: status, Reason: reason, Range: rangeName, Charts: charts}
	})
}

// handleSubstrateNode returns atelet's curated charts for one Node, gated on
// reading Nodes like the Node resource charts.
func handleSubstrateNode(w http.ResponseWriter, r *http.Request) {
	node := chi.URLParam(r, "name")
	if !canRead(r, "", "nodes", "", "get") {
		writeError(w, http.StatusForbidden, "Substrate metrics for a Node require permission to get Nodes.")
		return
	}
	serveSubstrate(w, r, func(ctx context.Context, q seriesQuerier, resolve substrateScopeResolver, rangeName string, start, end time.Time, step time.Duration) SubstrateHealthResponse {
		// Attribution is decided within this cluster's series: another
		// cluster's node labels say nothing about this one's.
		window := substrateProbeWindow(start, end, step)
		families := make([]substrateFamily, len(substrateNodeFamilies))
		for i, f := range substrateNodeFamilies {
			families[i] = substrateFamily{metric: f}
		}
		// Scope only: a family absent here says nothing about the node's other charts.
		scope := checkSubstrateFamilies(ctx, q, resolve, families, "", window, false)
		if scope.state != "" {
			return SubstrateHealthResponse{Status: SubstrateChartError, Reason: scope.reason, Range: rangeName, Charts: []SubstrateChart{}}
		}
		status, reason, err := substrateNodeAttribution(ctx, q, scope.matchers, node, window)
		if err != nil {
			return SubstrateHealthResponse{Status: SubstrateChartError, Reason: "Prometheus query failed: " + truncate(err.Error(), 200), Range: rangeName, Charts: []SubstrateChart{}}
		}
		if status != SubstrateAvailable {
			return SubstrateHealthResponse{Status: status, Reason: reason, Range: rangeName, Charts: []SubstrateChart{}}
		}
		charts := querySubstrateCharts(ctx, q, resolve, substrateRequest{sel: substrateNodeSelector(node), charts: substrateNodeCharts(), rangeName: rangeName, start: start, end: end, step: step})
		markUnattributedNodeCharts(ctx, q, scope.matchers, window, charts)
		status, reason = substrateResponseStatus(charts)
		if status == SubstrateNotFound {
			// atelet reports for this node, just none of what these charts read.
			status = SubstrateAvailable
		}
		return SubstrateHealthResponse{Status: status, Reason: reason, Range: rangeName, Charts: charts}
	})
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
