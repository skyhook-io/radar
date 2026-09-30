package traffic

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// flowAccumulator collects per-flow L7 details during aggregation.
type flowAccumulator struct {
	agg         *AggregatedFlow
	latencies   []float64        // per-response latencies from L7 records (ms)
	means       []float64        // latencies already averaged by a metric-based source (ms)
	statusCount map[string]int64 // "2xx", "3xx", "4xx", "5xx"
	pathStats   map[string]*pathAcc
	dnsStats    map[string]*dnsAcc
	verdicts    map[string]int64
	dropReasons map[string]int64
	l7Votes     map[string]int64
}

type pathAcc struct {
	count        int64
	latencyCount int64 // flows carrying a measured latency
	latencySumMs float64
	errors       int64 // 4xx + 5xx
}

type dnsAcc struct {
	count   int64
	nxCount int64
	ttlSum  uint64
}

// AggregateFlows aggregates flows by service pair with rich L7 statistics.
func AggregateFlows(flows []Flow) []AggregatedFlow {
	accumulators := make(map[string]*flowAccumulator)

	for _, f := range flows {
		// Whether the direction is known is part of the identity, not a property to
		// be reconciled afterwards. A source that cannot orient some of its traffic
		// reports it with port 0 — the same port every flow carries when the port
		// attribute is absent — so oriented and unoriented traffic between one pair
		// would otherwise land in a single aggregate, and any single answer about
		// its direction is wrong for half of what it contains. Sources that always
		// orient their flows contribute a constant here and are unaffected.
		key := fmt.Sprintf("%s/%s|%s/%s|%d|%t",
			f.Source.Namespace, f.Source.Name,
			f.Destination.Namespace, f.Destination.Name,
			f.Port, f.DirectionUnknown)

		acc, ok := accumulators[key]
		if !ok {
			acc = &flowAccumulator{
				agg: &AggregatedFlow{
					Source:           f.Source,
					Destination:      f.Destination,
					Protocol:         f.Protocol,
					Port:             f.Port,
					LastSeen:         f.LastSeen,
					DirectionUnknown: f.DirectionUnknown,
				},
				statusCount: make(map[string]int64),
				pathStats:   make(map[string]*pathAcc),
				dnsStats:    make(map[string]*dnsAcc),
				verdicts:    make(map[string]int64),
				dropReasons: make(map[string]int64),
				l7Votes:     make(map[string]int64),
			}
			accumulators[key] = acc
		}

		agg := acc.agg
		agg.FlowCount++
		agg.BytesSent += f.BytesSent
		agg.BytesRecv += f.BytesRecv
		agg.Connections += f.Connections
		agg.RequestCount += RoundRate(f.RequestRate)
		// RoundRate, not a bare round: it returns 0 for a zero rate, so it never
		// invents an error, and its floor of 1 keeps a real but slow failure visible
		// instead of rounding a genuine 5xx away to no error at all. It also rejects
		// NaN, which a ratio of two rates produces at zero traffic and which has no
		// defined conversion to int64.
		agg.ErrorCount += RoundRate(f.ErrorRate)
		agg.RequestRate += finiteRate(f.RequestRate)
		agg.ErrorRate += finiteRate(f.ErrorRate)
		if f.LastSeen.After(agg.LastSeen) {
			agg.LastSeen = f.LastSeen
		}

		// L7 protocol voting (use min weight of 1 so zero-connection L7 flows still count)
		if f.L7Protocol != "" {
			weight := f.Connections
			if weight <= 0 {
				weight = 1
			}
			acc.l7Votes[f.L7Protocol] += weight
		}

		// Latency from any flow that carries a measurement, excluding request
		// records: for a source that emits L7 records, the latency belongs to the
		// response. A metric-based source reports a measured duration without
		// emitting record types at all, and must not be excluded for that — but
		// its figure is already an average over the window, so it is kept apart:
		// percentiles taken over averages are not percentiles of anything.
		if f.LatencyNs > 0 && f.L7Type != "REQUEST" {
			ms := float64(f.LatencyNs) / 1e6
			if f.L7Type == "" {
				acc.means = append(acc.means, ms)
			} else {
				acc.latencies = append(acc.latencies, ms)
			}
		}

		// HTTP status bucketing
		if f.HTTPStatus > 0 {
			bucket := fmt.Sprintf("%dxx", f.HTTPStatus/100)
			acc.statusCount[bucket]++
		}

		// HTTP path accumulation
		if f.HTTPMethod != "" {
			pathKey := f.HTTPMethod + " " + f.HTTPPath
			pa, exists := acc.pathStats[pathKey]
			if !exists {
				pa = &pathAcc{}
				acc.pathStats[pathKey] = pa
			}
			pa.count++
			// Same rule as the edge latency above.
			if f.LatencyNs > 0 && f.L7Type != "REQUEST" {
				pa.latencySumMs += float64(f.LatencyNs) / 1e6
				pa.latencyCount++
			}
			if f.HTTPStatus >= 400 {
				pa.errors++
			}
		}

		// DNS query accumulation
		if f.DNSQuery != "" {
			da, exists := acc.dnsStats[f.DNSQuery]
			if !exists {
				da = &dnsAcc{}
				acc.dnsStats[f.DNSQuery] = da
			}
			da.count++
			if f.DNSRCode == 3 { // NXDOMAIN
				da.nxCount++
			}
			da.ttlSum += uint64(f.DNSTTL)
		}

		// Verdict and drop reasons
		if f.Verdict != "" {
			acc.verdicts[f.Verdict]++
		}
		if f.DropReasonDesc != "" {
			acc.dropReasons[f.DropReasonDesc]++
		}
	}

	// Finalize each accumulator
	result := make([]AggregatedFlow, 0, len(accumulators))
	for _, acc := range accumulators {
		agg := acc.agg

		// L7 protocol majority vote
		if len(acc.l7Votes) > 0 {
			var bestProto string
			var bestCount int64
			for proto, count := range acc.l7Votes {
				if count > bestCount {
					bestProto = proto
					bestCount = count
				}
			}
			agg.L7Protocol = bestProto
		}

		// Latency percentiles, only from per-response measurements. Averages
		// reported by a metric-based source yield the average alone.
		if len(acc.latencies) > 0 {
			sort.Float64s(acc.latencies)
			agg.LatencyP50Ms = PercentileFloat64(acc.latencies, 0.50)
			agg.LatencyP95Ms = PercentileFloat64(acc.latencies, 0.95)
			agg.LatencyP99Ms = PercentileFloat64(acc.latencies, 0.99)
			agg.AvgLatencyMs = mean(acc.latencies)
		} else if len(acc.means) > 0 {
			agg.AvgLatencyMs = mean(acc.means)
		}

		// HTTP status distribution
		if len(acc.statusCount) > 0 {
			agg.HTTPStatusCounts = acc.statusCount
		}

		// Top HTTP paths (up to 10)
		if len(acc.pathStats) > 0 {
			paths := make([]HTTPPathStat, 0, len(acc.pathStats))
			for key, pa := range acc.pathStats {
				parts := strings.SplitN(key, " ", 2)
				method, path := parts[0], ""
				if len(parts) > 1 {
					path = parts[1]
				}
				stat := HTTPPathStat{
					Method: method,
					Path:   path,
					Count:  pa.count,
				}
				if pa.latencyCount > 0 {
					stat.AvgMs = pa.latencySumMs / float64(pa.latencyCount)
				}
				if pa.count > 0 {
					stat.ErrorPct = float64(pa.errors) / float64(pa.count) * 100
				}
				paths = append(paths, stat)
			}
			sort.Slice(paths, func(i, j int) bool { return paths[i].Count > paths[j].Count })
			if len(paths) > 10 {
				paths = paths[:10]
			}
			agg.TopHTTPPaths = paths
		}

		// Top DNS queries (up to 10)
		if len(acc.dnsStats) > 0 {
			queries := make([]DNSQueryStat, 0, len(acc.dnsStats))
			for query, da := range acc.dnsStats {
				stat := DNSQueryStat{
					Query:   query,
					Count:   da.count,
					NXCount: da.nxCount,
				}
				if da.count > 0 && da.ttlSum > 0 {
					stat.AvgTTL = uint32(da.ttlSum / uint64(da.count))
				}
				queries = append(queries, stat)
			}
			sort.Slice(queries, func(i, j int) bool { return queries[i].Count > queries[j].Count })
			if len(queries) > 10 {
				queries = queries[:10]
			}
			agg.TopDNSQueries = queries
		}

		// Verdict counts
		if len(acc.verdicts) > 0 {
			agg.VerdictCounts = acc.verdicts
		}

		// Drop reasons
		if len(acc.dropReasons) > 0 {
			agg.DropReasons = acc.dropReasons
		}

		result = append(result, *agg)
	}
	return result
}

func mean(values []float64) float64 {
	var sum float64
	for _, v := range values {
		sum += v
	}
	return sum / float64(len(values))
}

// PercentileFloat64 returns the p-th percentile from a sorted slice.
func PercentileFloat64(sorted []float64, p float64) float64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := int(float64(len(sorted)-1) * p)
	return sorted[idx]
}

// RoundRate converts a per-second rate to an int64 count, ensuring that any
// positive rate maps to at least 1 (so low-traffic services aren't invisible).
func RoundRate(rate float64) int64 {
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0
	}
	r := int64(math.Round(rate))
	if r == 0 {
		return 1
	}
	return r
}

// finiteRate drops a rate that cannot be summed: negative, NaN (a ratio of two
// rates at zero traffic) or infinite.
func finiteRate(rate float64) float64 {
	if rate <= 0 || math.IsNaN(rate) || math.IsInf(rate, 0) {
		return 0
	}
	return rate
}

// DefaultFlowOptions returns sensible defaults for flow queries.
func DefaultFlowOptions() FlowOptions {
	return FlowOptions{
		Since: 5 * time.Minute,
		Limit: 1000,
	}
}
