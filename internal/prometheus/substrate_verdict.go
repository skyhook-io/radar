package prometheus

import (
	"context"
	"math"
	"sort"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// A verdict answers the question the charts are opened for: does this pool
// have workers to place actors on, and is this node's actor state safe in
// storage. It is read now, and its facts over the request's range.

const (
	// SubstrateVerdictShort: Substrate's own SubstrateWorkerPoolCapacityNotDelivered
	// alert condition has held for five minutes: fewer schedulable workers than
	// the fewest desired.
	SubstrateVerdictShort = "short"
	// SubstrateVerdictFull: no API replica sees a free worker slot.
	SubstrateVerdictFull      = "full"
	SubstrateVerdictServing   = "serving"
	SubstrateVerdictNoWorkers = "noWorkers"
	// SubstrateVerdictBehind: some actor's snapshot upload is overdue.
	SubstrateVerdictBehind  = "behind"
	SubstrateVerdictCurrent = "current"
	SubstrateVerdictUnknown = "unknown"

	SubstrateFactOK          = "ok"
	SubstrateFactEmpty       = "empty"
	SubstrateFactUnavailable = "unavailable"
)

// SubstrateVerdict is the one-line reading of a pool or node.
type SubstrateVerdict struct {
	State  string          `json:"state"`
	Reason string          `json:"reason,omitempty"`
	Facts  []SubstrateFact `json:"facts"`
}

// SubstrateFact is one number behind a verdict. Value is set only when State
// is ok; empty means the series exist and recorded nothing for this subject.
type SubstrateFact struct {
	ID     string              `json:"id"`
	Label  string              `json:"label"`
	Unit   string              `json:"unit"`
	State  string              `json:"state"`
	Value  *float64            `json:"value,omitempty"`
	Of     *float64            `json:"of,omitempty"`
	Reason string              `json:"reason,omitempty"`
	Parts  []SubstrateFactPart `json:"parts,omitempty"`
}

type SubstrateFactPart struct {
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

// chartFact reads one number over the request's range from the same series,
// scope and checks as the chart it summarizes: a chart Radar refused to draw
// leaves its fact unavailable for the same reason.
func (r *substrateRun) chartFact(chart SubstrateChart, def substrateChartDef, id, label, unit string, build func(x substrateExpr) string) SubstrateFact {
	if chart.State != SubstrateChartOK && chart.State != SubstrateChartEmpty {
		return unavailableFact(id, label, unit, chart.Reason)
	}
	check := r.check(def.families())
	v, err := instantValue(r.ctx, r.q, build(r.expr(check.matchers, r.rangeWindow())))
	switch {
	case err != nil:
		return unavailableFact(id, label, unit, queryFailed(err))
	case v == nil && chart.State == SubstrateChartOK:
		// The range has data and now has none: the series stopped, which says
		// nothing about the value.
		return unavailableFact(id, label, unit, "Prometheus has no current value for this; the series stopped reporting")
	case v == nil:
		return SubstrateFact{ID: id, Label: label, Unit: unit, State: SubstrateFactEmpty, Reason: def.emptyReason}
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
// on it: short of workers, then full, then serving.
func (r *substrateRun) poolVerdict(charts []SubstrateChart) *SubstrateVerdict {
	defs := r.req.charts
	check := r.check([]substrateInstrument{instWorkers, instDesired})
	x := r.expr(check.matchers, "")
	schedulable := []string{x.m("ate.worker.state", "!=", "unschedulable"), x.m("ate.sandbox.class", "!=", "unknown")}
	free := []string{x.m("ate.worker.state", "=~", "idle|partial"), x.m("ate.sandbox.class", "!=", "unknown")}
	// The counts the workers chart draws: averaged across API replicas.
	averaged := func(extra []string) string {
		return "sum(avg by (ate_worker_state, ate_sandbox_class) (" + collapseHA(x.norm(x.vec(instWorkers, "", extra...), "ate.worker.state", "ate.sandbox.class")) + "))"
	}

	v := &SubstrateVerdict{State: SubstrateVerdictUnknown}
	freeFact := unavailableFact("freeWorkers", "Free workers", "workers", "")
	defer func() {
		v.Facts = append([]SubstrateFact{freeFact},
			r.chartFact(findSubstrateChart(charts, "resumeTime"), chartDef(defs, "resumeTime"), "resumeP95", "Resume p95", "seconds", func(x substrateExpr) string {
				return x.quantile("0.95", instLifecycle, x.m("ate.actor.operation.name", "=", "resume"), x.m("error.type", "=", ""))
			}),
			r.chartFact(findSubstrateChart(charts, "resumeFailures"), chartDef(defs, "resumeFailures"), "resumeFailures", "Failed resumes", "percent", func(x substrateExpr) string {
				return x.failureShare(instLifecycle, x.m("ate.actor.operation.name", "=", "resume"))
			}),
			r.chartFact(findSubstrateChart(charts, "crashes"), chartDef(defs, "crashes"), "crashes", "Crashes", "count", func(x substrateExpr) string {
				return "sum(" + collapseHA("increase("+x.vec(instCrashes, "")+"["+x.window+"])") + ")"
			}),
		)
	}()
	if check.state != "" {
		v.Reason, freeFact.Reason = check.reason, check.reason
		return v
	}

	// Substrate's alert, for one pool: the most any replica reports
	// schedulable is below the fewest desired over five minutes, held for
	// five minutes. A maximum, so a lagging replica cannot raise it.
	actual := "sum(max by (ate_worker_state, ate_sandbox_class) (" + x.norm(x.vec(instWorkers, "", schedulable...), "ate.worker.state", "ate.sandbox.class") + "))"
	desired := "min(min_over_time(" + x.vec(instDesired, "") + "[5m]))"
	// Held means true at every one-minute point of the last six minutes, all
	// six present (the earliest is at least five minutes old), and true now:
	// a minimum over whatever points exist would call one minute of history,
	// or a recovery since the last point, five minutes short.
	cond := "(" + actual + " < bool " + desired + ")"
	short, err := instantValue(r.ctx, r.q, "min_over_time("+cond+"[6m:1m]) * (count_over_time("+cond+"[6m:1m]) >= bool 6) * "+cond)
	if err != nil {
		v.Reason, freeFact.Reason = queryFailed(err), queryFailed(err)
		return v
	}
	const noCounts = "Substrate reported no worker counts for this pool in the last 5 minutes"
	if short == nil {
		v.Reason, freeFact.Reason = noCounts, noCounts
		return v
	}

	total, err := instantValue(r.ctx, r.q, averaged(schedulable))
	if err == nil && total != nil {
		var freeNow *float64
		if freeNow, err = instantValue(r.ctx, r.q, averaged(free)); err == nil {
			if freeNow == nil {
				zero := 0.0
				freeNow = &zero
			}
			freeFact = SubstrateFact{ID: "freeWorkers", Label: "Free workers", Unit: "workers", State: SubstrateFactOK, Value: freeNow, Of: total}
		}
	}
	if err != nil {
		freeFact.Reason = queryFailed(err)
	} else if total == nil {
		freeFact.Reason = noCounts
	}

	switch {
	case *short == 1:
		v.State = SubstrateVerdictShort
		return v
	case err != nil:
		v.Reason = queryFailed(err)
		return v
	case total == nil:
		v.Reason = noCounts
		return v
	case *total == 0:
		v.State = SubstrateVerdictNoWorkers
		return v
	}
	// Full only when no replica sees a free slot: one that lags cannot
	// report a pool full that another replica already sees room in.
	anyFree, err := instantValue(r.ctx, r.q, "max("+x.vec(instWorkers, "", free...)+")")
	switch {
	case err != nil:
		v.Reason = queryFailed(err)
	case anyFree == nil || *anyFree == 0:
		v.State = SubstrateVerdictFull
	default:
		v.State = SubstrateVerdictServing
	}
	return v
}

// nodeVerdict judges whether actor state on this node is reaching storage.
func (r *substrateRun) nodeVerdict(charts []SubstrateChart) *SubstrateVerdict {
	defs := r.req.charts
	v := &SubstrateVerdict{State: SubstrateVerdictUnknown}
	overdue := unavailableFact("overdueUploads", "Overdue uploads", "count", "")
	oldest := r.chartFact(findSubstrateChart(charts, "unsavedSnapshotAge"), chartDef(defs, "unsavedSnapshotAge"), "oldestUnsaved", "Oldest unsaved snapshot", "seconds", func(x substrateExpr) string {
		return "max(" + x.vec(instUndurable, "") + ")"
	})
	defer func() { v.Facts = []SubstrateFact{overdue, oldest} }()

	chart := findSubstrateChart(charts, "overdueUploads")
	if chart.State != SubstrateChartOK && chart.State != SubstrateChartEmpty {
		v.Reason, overdue.Reason = chart.Reason, chart.Reason
		return v
	}
	check := r.check([]substrateInstrument{instOverdue})
	x := r.expr(check.matchers, "")
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
		v.State = SubstrateVerdictBehind
	} else {
		v.State = SubstrateVerdictCurrent
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
