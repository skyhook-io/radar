package prometheus

import (
	"context"
	"errors"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// A verdict answers the question the charts are opened for: does this pool
// have workers to place actors on, and is this node's actor state safe in
// storage. The verdict and the capacity and snapshot facts read now; resume
// p95, failed resumes and crashes summarize the request's range.

const (
	// SubstrateVerdictWorkerShortfall: Substrate's own
	// SubstrateWorkerPoolCapacityNotDelivered alert condition has held for five minutes: fewer schedulable workers than
	// the fewest desired. It needs the controller's desired worker count.
	SubstrateVerdictWorkerShortfall = "workerShortfall"
	// SubstrateVerdictNoFreeActorSlots: no API replica sees a free actor slot.
	SubstrateVerdictNoFreeActorSlots = "noFreeActorSlots"
	// SubstrateVerdictFreeActorSlots: some worker has a free actor slot. Slots are
	// counted by actors alone; an actor can still fail to place for lack of
	// CPU or memory, and such a placement names no pool.
	SubstrateVerdictFreeActorSlots = "freeActorSlots"
	// SubstrateVerdictNoSchedulableWorkers: no schedulable worker of a known class.
	SubstrateVerdictNoSchedulableWorkers = "noSchedulableWorkers"
	// SubstrateVerdictUploadsOverdue: some actor's snapshot upload is overdue.
	SubstrateVerdictUploadsOverdue = "uploadsOverdue"
	// SubstrateVerdictNoUploadsOverdue: no overdue upload is counted. Snapshots with no
	// storage destination are not counted, so it does not prove all are saved.
	SubstrateVerdictNoUploadsOverdue = "noUploadsOverdue"
	SubstrateVerdictUnknown          = "unknown"

	SubstrateFactOK          = "ok"
	SubstrateFactEmpty       = "empty"
	SubstrateFactUnavailable = "unavailable"
)

// SubstrateVerdict is the one-line reading of a pool or node.
type SubstrateVerdict struct {
	State string `json:"state"`
	// Reason says why the state is unknown, or qualifies a known one: a pool
	// whose shortfall Radar could not judge.
	Reason string          `json:"reason,omitempty"`
	Facts  []SubstrateFact `json:"facts"`
}

// SubstrateFact is one number behind a verdict. Value is set only when State
// is ok; empty means the series exist and recorded nothing for this subject.
// Label is display text.
type SubstrateFact struct {
	ID string `json:"id"`
	// Label is display text.
	Label string `json:"label"`
	// Unit is workers, seconds, percent or count.
	Unit  string   `json:"unit"`
	State string   `json:"state"`
	Value *float64 `json:"value,omitempty"`
	// Of is the total Value is out of, set only on an ok fact that compares:
	// workersWithFreeActorSlots, out of the schedulable workers of known classes
	// averaged across API replicas; schedulableWorkers, out of the fewest desired
	// over five minutes.
	Of     *float64 `json:"of,omitempty"`
	Reason string   `json:"reason,omitempty"`
	// Parts breaks an ok overdueUploads down by upload_overdue_band, worst
	// first, listing only bands with actors in them.
	Parts []SubstrateFactPart `json:"parts,omitempty"`
}

type SubstrateFactPart struct {
	// Label is Substrate's band key (1m_to_5m, 5m_to_15m, over_15m).
	Label string  `json:"label"`
	Value float64 `json:"value"`
}

func instantValue(ctx context.Context, q seriesQuerier, expr string) (*float64, error) {
	res, err := strictQuerier{q}.Query(ctx, expr)
	if err != nil {
		return nil, err
	}
	for _, s := range res.Series {
		if v, ok := lastValue(s); ok {
			return &v, nil
		}
	}
	return nil, nil
}

func lastValue(s prom.Series) (float64, bool) {
	if n := len(s.DataPoints); n > 0 {
		v := s.DataPoints[n-1].Value
		if !math.IsNaN(v) && !math.IsInf(v, 0) {
			return v, true
		}
	}
	return 0, false
}

func unavailableFact(id, label, unit, reason string) SubstrateFact {
	return SubstrateFact{ID: id, Label: label, Unit: unit, State: SubstrateFactUnavailable, Reason: reason}
}

func queryFailed(err error) string { return "Prometheus query failed: " + truncate(err.Error(), 200) }

// chartFact reads one number from the same series, scope and checks as the
// chart it summarizes: a chart Radar refused to draw leaves its fact
// unavailable for the same reason. build reads over x.window, the request's
// range, or now; population is the matchers of the observations it
// summarizes, which must still be reported now.
func (r *substrateRun) chartFact(chart SubstrateChart, def substrateChartDef, id, label, unit string, population func(x substrateExpr) []string, build func(x substrateExpr) string) SubstrateFact {
	if chart.State != SubstrateChartOK && chart.State != SubstrateChartEmpty {
		return unavailableFact(id, label, unit, chart.Reason)
	}
	const stopped = "Prometheus has no current value for this; the series stopped reporting"
	check := r.check(def.families())
	x := r.expr(def.inst, check.matchers, r.rangeWindow())
	v, err := instantValue(r.ctx, r.q, build(x))
	switch {
	case err != nil:
		return unavailableFact(id, label, unit, queryFailed(err))
	case v == nil && chart.State == SubstrateChartOK:
		// The range has data and now has none: the series stopped, which says
		// nothing about the value.
		return unavailableFact(id, label, unit, stopped)
	case v == nil:
		return SubstrateFact{ID: id, Label: label, Unit: unit, State: SubstrateFactEmpty, Reason: def.emptyReason}
	}
	// A summary over the range still has a value after its series stopped,
	// counting only the time before; say nothing rather than a stale number.
	// Another population of the same instrument (a suspend, a failed resume)
	// still reporting proves nothing about this one.
	var matchers []string
	if population != nil {
		matchers = population(x)
	}
	fresh, err := instantValue(r.ctx, r.q, "count("+x.vec(def.inst, def.inst.probeSuffix(), matchers...)+")")
	switch {
	case err != nil:
		return unavailableFact(id, label, unit, queryFailed(err))
	case fresh == nil:
		return unavailableFact(id, label, unit, stopped)
	}
	return SubstrateFact{ID: id, Label: label, Unit: unit, State: SubstrateFactOK, Value: v}
}

func (r *substrateRun) rangeWindow() string {
	return r.req.end.Sub(r.req.start).Round(time.Second).String()
}

func chartDef(defs []substrateChartDef, id string) substrateChartDef {
	for _, d := range defs {
		if d.id == id {
			return d
		}
	}
	return substrateChartDef{}
}

// poolVerdict judges the pool's capacity now, in the order an operator acts
// on it: short of workers, then no schedulable workers or full, then free slots. Everything
// but the shortfall reads the API server's worker counts alone; the shortfall
// also needs the controller's desired count, which a setup may not collect.
func (r *substrateRun) poolVerdict(charts []SubstrateChart) *SubstrateVerdict {
	defs := r.req.charts
	v := &SubstrateVerdict{State: SubstrateVerdictUnknown}
	freeFact := unavailableFact("workersWithFreeActorSlots", "Workers with free slots", "workers", "")
	// The shortfall's own evidence leads when the pool is short: free slots
	// alone read as room.
	var shortfall []SubstrateFact
	defer func() {
		v.Facts = append(append(shortfall, freeFact),
			r.chartFact(findSubstrateChart(charts, "resumeTime"), chartDef(defs, "resumeTime"), "resumeP95", "Resume p95", "seconds", succeededResumes, func(x substrateExpr) string {
				return x.quantile("0.95", instLifecycle, succeededResumes(x)...)
			}),
			r.chartFact(findSubstrateChart(charts, "resumeFailures"), chartDef(defs, "resumeFailures"), "resumeFailures", "Failed resumes", "percent", resumes, func(x substrateExpr) string {
				return x.failureShare(instLifecycle, resumes(x)...)
			}),
			r.chartFact(findSubstrateChart(charts, "crashes"), chartDef(defs, "crashes"), "crashes", "Crashes", "count", nil, func(x substrateExpr) string {
				return "sum(" + collapseHA("increase("+x.vec(instCrashes, "")+"["+x.window+"])") + ")"
			}),
		)
	}()
	unknown := func(reason string) *SubstrateVerdict {
		v.Reason = reason
		if freeFact.State != SubstrateFactOK {
			freeFact.Reason = reason
		}
		return v
	}
	check := r.check([]substrateInstrument{instWorkers})
	if check.state != "" {
		return unknown(check.reason)
	}
	x := r.expr(instWorkers, check.matchers, "")
	// Only the states this reading knows count as capacity; a state Substrate
	// adds later could mean either.
	const knownStates = "idle|partial|at_capacity"
	knownClass := x.m("ate.sandbox.class", "!=", "unknown")
	schedulable := []string{x.m("ate.worker.state", "=~", knownStates), knownClass}
	free := []string{x.m("ate.worker.state", "=~", "idle|partial"), knownClass}
	// The counts the workers chart draws: averaged across API replicas.
	averaged := func(extra []string) string {
		return "sum(avg by (ate_worker_state, ate_sandbox_class) (" + collapseHA(x.norm(x.vec(instWorkers, "", extra...), "ate.worker.state", "ate.sandbox.class")) + "))"
	}

	other, err := strictQuerier{r.q}.Query(r.ctx, "max by (ate_worker_state) ("+x.norm(x.vec(instWorkers, "", x.m("ate.worker.state", "!~", knownStates+"|unschedulable")), "ate.worker.state")+") > 0")
	if err != nil {
		return unknown(queryFailed(err))
	}
	if len(other.Series) > 0 {
		var states []string
		for _, s := range other.Series {
			states = append(states, s.Labels["ate_worker_state"])
		}
		sort.Strings(states)
		return unknown("Substrate reports workers in a state Radar does not read (" + strings.Join(states, ", ") + "), so it cannot tell whether they have room")
	}

	const noCounts = "Substrate reported no worker counts for this pool in the last 5 minutes"
	total, err := instantValue(r.ctx, r.q, averaged(schedulable))
	switch {
	case err != nil:
		return unknown(queryFailed(err))
	case total == nil:
		return unknown(noCounts)
	}
	freeNow, err := instantValue(r.ctx, r.q, averaged(free))
	if err != nil {
		return unknown(queryFailed(err))
	}
	if freeNow == nil {
		zero := 0.0
		freeNow = &zero
	}
	freeFact = SubstrateFact{ID: "workersWithFreeActorSlots", Label: "Workers with free slots", Unit: "workers", State: SubstrateFactOK, Value: freeNow, Of: total}

	short, evidence, err := r.poolShort()
	switch {
	case err != nil:
		v.Reason = err.Error()
	case short:
		v.State = SubstrateVerdictWorkerShortfall
		if evidence != nil {
			shortfall = []SubstrateFact{*evidence}
		}
		return v
	}
	if *total == 0 {
		v.State = SubstrateVerdictNoSchedulableWorkers
		return v
	}
	// Full only when no replica sees a free slot: one that lags cannot
	// report a pool full that another replica already sees room in.
	anyFree, err := instantValue(r.ctx, r.q, "max("+x.vec(instWorkers, "", free...)+")")
	switch {
	case err != nil:
		return unknown(queryFailed(err))
	case anyFree == nil || *anyFree == 0:
		v.State = SubstrateVerdictNoFreeActorSlots
	default:
		v.State = SubstrateVerdictFreeActorSlots
	}
	return v
}

func resumes(x substrateExpr) []string {
	return []string{x.m("ate.actor.operation.name", "=", "resume")}
}

func succeededResumes(x substrateExpr) []string {
	return append(resumes(x), x.m("error.type", "=", ""))
}

// poolShort reports whether Substrate's capacity alert condition holds for
// the pool. Its error says why the shortfall cannot be judged.
//
// When the pool is short it also returns the comparison now, as Substrate's
// alert makes it: schedulable workers counted as the highest any API replica
// reports for each state and class, summed (with replicas that disagree, more
// than any one replica's total), out of the fewest desired over five minutes.
// That fact is unavailable when the counts are missing.
func (r *substrateRun) poolShort() (bool, *SubstrateFact, error) {
	cannot := func(why string) error {
		return errors.New("Radar cannot tell whether the pool is short of workers: " + why)
	}
	check := r.check([]substrateInstrument{instWorkers, instDesired})
	if check.state != "" {
		return false, nil, cannot(check.reason)
	}
	xw, xd := r.expr(instWorkers, check.matchers, ""), r.expr(instDesired, check.matchers, "")
	// Substrate's alert, for one pool: schedulable workers, the highest count
	// any replica reports for each state and class summed, below the fewest
	// desired over five minutes, held for five minutes. Maxima, so a lagging
	// replica cannot raise it.
	schedulable := []string{xw.m("ate.worker.state", "!=", "unschedulable"), xw.m("ate.sandbox.class", "!=", "unknown")}
	actual := "sum(max by (ate_worker_state, ate_sandbox_class) (" + xw.norm(xw.vec(instWorkers, "", schedulable...), "ate.worker.state", "ate.sandbox.class") + "))"
	desired := "min(min_over_time(" + xd.vec(instDesired, "") + "[5m]))"
	// Held means true at every one-minute point of the last six minutes, all
	// six present (the earliest is at least five minutes old), and true now:
	// a minimum over whatever points exist would call one minute of history,
	// or a recovery since the last point, five minutes short.
	cond := "(" + actual + " < bool " + desired + ")"
	held := "min_over_time(" + cond + "[6m:1m]) * (count_over_time(" + cond + "[6m:1m]) >= bool 6) * " + cond
	// One query, so the verdict and the numbers behind it are read at the
	// same instant: a pool that recovers between two queries would otherwise
	// show a shortfall beside equal counts.
	operand := func(expr, name string) string {
		return `label_replace(` + expr + `, "operand", "` + name + `", "", "")`
	}
	res, err := strictQuerier{r.q}.Query(r.ctx, operand(held, "held")+" or "+operand(actual, "have")+" or "+operand(desired, "want"))
	if err != nil {
		return false, nil, cannot(queryFailed(err))
	}
	values := map[string]float64{}
	for _, s := range res.Series {
		if v, ok := lastValue(s); ok {
			values[s.Labels["operand"]] = v
		}
	}
	short, judged := values["held"]
	switch {
	case !judged:
		// The worker counts are there (poolVerdict read them), so the desired count is not.
		return false, nil, cannot("Substrate's controller reported no desired worker count for it in the last 5 minutes. Substrate's bundled Prometheus does not collect the controller's metrics")
	case short != 1:
		return false, nil, nil
	}
	have, okHave := values["have"]
	want, okWant := values["want"]
	if !okHave || !okWant {
		f := unavailableFact("schedulableWorkers", "Schedulable workers", "workers", "Prometheus returned the shortfall without the counts it compares")
		return true, &f, nil
	}
	return true, &SubstrateFact{ID: "schedulableWorkers", Label: "Schedulable workers", Unit: "workers", State: SubstrateFactOK, Value: &have, Of: &want}, nil
}

// nodeVerdict judges whether actor state on this node is reaching storage.
func (r *substrateRun) nodeVerdict(charts []SubstrateChart) *SubstrateVerdict {
	defs := r.req.charts
	v := &SubstrateVerdict{State: SubstrateVerdictUnknown}
	overdue := unavailableFact("overdueUploads", "Overdue uploads", "count", "")
	oldest := r.chartFact(findSubstrateChart(charts, "unsavedSnapshotAge"), chartDef(defs, "unsavedSnapshotAge"), "oldestUnsaved", "Oldest unsaved snapshot", "seconds", nil, func(x substrateExpr) string {
		return "max(" + x.vec(instUndurable, "") + ")"
	})
	defer func() { v.Facts = []SubstrateFact{overdue, oldest} }()

	chart := findSubstrateChart(charts, "overdueUploads")
	if chart.State != SubstrateChartOK && chart.State != SubstrateChartEmpty {
		v.Reason, overdue.Reason = chart.Reason, chart.Reason
		return v
	}
	check := r.check([]substrateInstrument{instOverdue})
	x := r.expr(instOverdue, check.matchers, "")
	res, err := strictQuerier{r.q}.Query(r.ctx, "max by (ate_snapshot_upload_overdue_band) ("+x.norm(x.vec(instOverdue, ""), "ate.snapshot.upload_overdue_band")+")")
	if err != nil {
		v.Reason, overdue.Reason = queryFailed(err), queryFailed(err)
		return v
	}
	total := 0.0
	var parts []SubstrateFactPart
	for _, s := range res.Series {
		if val, ok := lastValue(s); ok {
			total += val
			if val > 0 {
				parts = append(parts, SubstrateFactPart{Label: s.Labels["ate_snapshot_upload_overdue_band"], Value: val})
			}
		}
	}
	sort.Slice(parts, func(i, j int) bool { return overdueBandRank(parts[i].Label) > overdueBandRank(parts[j].Label) })
	switch {
	case len(res.Series) == 0 && chart.State == SubstrateChartOK:
		const stale = "atelet has not reported overdue uploads for this node in the last 5 minutes"
		v.Reason, overdue.Reason = stale, stale
		return v
	case len(res.Series) == 0:
		// The family exists in this cluster and this node recorded no overdue band.
		overdue = SubstrateFact{ID: "overdueUploads", Label: "Overdue uploads", Unit: "count", State: SubstrateFactEmpty, Reason: chartDef(defs, "overdueUploads").emptyReason}
	default:
		overdue = SubstrateFact{ID: "overdueUploads", Label: "Overdue uploads", Unit: "count", State: SubstrateFactOK, Value: &total, Parts: parts}
	}
	if total > 0 {
		v.State = SubstrateVerdictUploadsOverdue
	} else {
		v.State = SubstrateVerdictNoUploadsOverdue
	}
	return v
}

func overdueBandRank(band string) int {
	switch band {
	case "over_15m":
		return 3
	case "5m_to_15m":
		return 2
	case "1m_to_5m":
		return 1
	}
	return 0
}

func findSubstrateChart(charts []SubstrateChart, id string) SubstrateChart {
	for _, c := range charts {
		if c.ID == id {
			return c
		}
	}
	return SubstrateChart{State: SubstrateChartError, Reason: "chart " + id + " was not computed"}
}
