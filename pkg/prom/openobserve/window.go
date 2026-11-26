package openobserve

import (
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/prometheus/prometheus/model/labels"
	"github.com/prometheus/prometheus/promql/parser"
)

// errGatewayWindow refuses a query that reads name-only metrics from before
// the span discovery checked the collector cluster name over.
var errGatewayWindow = errors.New("openobserve: metrics identified only by the collector's cluster name can be read for the last 7 days, the span over which Radar checked that no other cluster sent that name; this query reads earlier data (through its time range, an offset or an @ time)")

// selectorLookback is how far before its evaluation time an instant selector
// reads: the default lookback delta of Prometheus and OpenObserve.
const selectorLookback = 5 * time.Minute

// gatewayRead is how far before its anchor one name-only selector reads. The
// anchor is the evaluation time, unless the selector or an enclosing subquery
// pins it with @ (a fixed time, start() or end()).
type gatewayRead struct {
	back       time.Duration
	at         *int64
	startOrEnd parser.ItemType
}

// gatewayReads lists the reads of every selector in exprs that relies on
// GatewayMetricsAllowed: a collector-dialect selector scoped by the cluster
// name alone, without k8s_cluster_uid. exprs are already rewritten.
func gatewayReads(exprs []string, s *scope) ([]gatewayRead, error) {
	if !s.collector || !s.gatewayAllowed {
		return nil, nil
	}
	var reads []gatewayRead
	for _, q := range exprs {
		expr, err := parser.ParseExpr(q)
		if err != nil {
			return nil, err
		}
		var bad error
		parser.Inspect(expr, func(node parser.Node, path []parser.Node) error {
			vs, ok := node.(*parser.VectorSelector)
			if !ok || bad != nil || hasUIDMatcher(s.required(vs.Name)) {
				return nil
			}
			read, ok := selectorRead(vs, path)
			if !ok {
				bad = errGatewayWindow
				return bad
			}
			reads = append(reads, read)
			return nil
		})
		if bad != nil {
			return nil, bad
		}
	}
	return reads, nil
}

func hasUIDMatcher(required []*labels.Matcher) bool {
	for _, m := range required {
		if m.Name == collectorUIDLabel {
			return true
		}
	}
	return false
}

// selectorRead walks from vs up through its enclosing range selector and
// subqueries, adding up how far back each reaches until an @ pins the time.
// Durations written as expressions are not resolved here and are refused.
func selectorRead(vs *parser.VectorSelector, path []parser.Node) (gatewayRead, bool) {
	if vs.OriginalOffsetExpr != nil {
		return gatewayRead{}, false
	}
	read := gatewayRead{back: vs.OriginalOffset + selectorLookback}
	if len(path) > 0 {
		if ms, ok := path[len(path)-1].(*parser.MatrixSelector); ok {
			if ms.RangeExpr != nil {
				return gatewayRead{}, false
			}
			read.back += ms.Range - selectorLookback
		}
	}
	if vs.Timestamp != nil || vs.StartOrEnd != 0 {
		read.at, read.startOrEnd = vs.Timestamp, vs.StartOrEnd
		return read, true
	}
	for i := len(path) - 1; i >= 0; i-- {
		sq, ok := path[i].(*parser.SubqueryExpr)
		if !ok {
			continue
		}
		if sq.RangeExpr != nil || sq.OriginalOffsetExpr != nil {
			return gatewayRead{}, false
		}
		read.back += sq.Range + sq.OriginalOffset
		if sq.Timestamp != nil || sq.StartOrEnd != 0 {
			read.at, read.startOrEnd = sq.Timestamp, sq.StartOrEnd
			return read, true
		}
	}
	return read, true
}

// boundGatewayWindow keeps every name-only read at or after now minus
// nameHistory. A request with a start (a range query, a series or label
// lookup) has its start moved forward, on its step grid, when that alone
// keeps the reads inside; a read anchored elsewhere (an instant query's time,
// an offset, @ end() or a fixed @) that reaches back is refused.
func boundGatewayWindow(out url.Values, ranged bool, exprs []string, s *scope, now time.Time) error {
	reads, err := gatewayReads(exprs, s)
	if err != nil || len(reads) == 0 {
		return err
	}
	cutoff := now.Add(-nameHistoryDuration)
	start, end := now, now
	if ranged {
		var okStart, okEnd bool
		start, okStart = parsePromTime(out.Get("start"))
		end, okEnd = parsePromTime(out.Get("end"))
		if !okStart || !okEnd {
			return errors.New("openobserve: a start and an end are required to read metrics identified only by the collector's cluster name")
		}
	} else if v := out.Get("time"); v != "" {
		t, ok := parsePromTime(v)
		if !ok {
			return errors.New("openobserve: unreadable query time")
		}
		start, end = t, t
	}
	need := start
	for _, r := range reads {
		switch {
		case r.at != nil:
			if time.UnixMilli(*r.at).Add(-r.back).Before(cutoff) {
				return errGatewayWindow
			}
		case r.startOrEnd == parser.END:
			if end.Add(-r.back).Before(cutoff) {
				return errGatewayWindow
			}
		default:
			if earliest := cutoff.Add(r.back); earliest.After(need) {
				need = earliest
			}
		}
	}
	if !need.After(start) {
		return nil
	}
	if step, ok := parsePromDuration(out.Get("step")); ok && step > 0 {
		need = start.Add((need.Sub(start) + step - 1) / step * step)
	}
	if !ranged || need.After(end) {
		return errGatewayWindow
	}
	out.Set("start", strconv.FormatFloat(float64(need.UnixMilli())/1000, 'f', -1, 64))
	return nil
}

// parsePromDuration reads a Prometheus API step: seconds (possibly
// fractional) or a Go duration.
func parsePromDuration(v string) (time.Duration, bool) {
	if f, err := strconv.ParseFloat(v, 64); err == nil {
		return time.Duration(f * float64(time.Second)), true
	}
	d, err := time.ParseDuration(v)
	return d, err == nil
}
