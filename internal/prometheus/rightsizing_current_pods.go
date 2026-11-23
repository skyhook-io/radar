package prometheus

import (
	"context"
	"math"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// Limit both the PromQL sent through proxies and the range matrix returned for
// one read. A workload larger than one chunk still needs every Pod in a bucket.
const sizingPodQueryChunk = 8

func hasCurrentPodSizingCohort(w scanWorkload) bool {
	pods := w.workload.podIdentities
	if w.workload.liveInventoryUnavailable || w.workload.podIdentityIncomplete || len(pods) == 0 || len(pods) != w.replicas {
		return false
	}
	names, uids := map[string]bool{}, map[string]bool{}
	for _, p := range pods {
		if _, err := prom.PodCgroupPattern(p.UID); err != nil || p.Name == "" || names[p.Name] || uids[p.UID] {
			return false
		}
		names[p.Name], uids[p.UID] = true, true
	}
	return true
}

type sizingPodTarget struct {
	identity prom.WorkloadPodIdentity
	workload *scanWorkload
}

func queryCurrentPodRightsizing(ctx context.Context, client rightsizingScanQuerier, input scanWorkload, now time.Time) scanBatchEvidence {
	return queryCurrentPodBatch(ctx, client, []scanWorkload{input}, now)
}

type sizingBucket struct {
	count int
	max   float64
}

func queryCurrentPodBatch(ctx context.Context, client rightsizingScanQuerier, workloads []scanWorkload, now time.Time) scanBatchEvidence {
	out := scanBatchEvidence{ownerCoverage: OwnerCoverageCurrentPods, restarts: map[scanKey]float64{}, errors: map[string]error{}}
	var targets []sizingPodTarget
	expected := map[scanKey]int{}
	for i := range workloads {
		w := &workloads[i]
		if !hasCurrentPodSizingCohort(*w) {
			continue
		}
		for _, p := range w.workload.podIdentities {
			targets = append(targets, sizingPodTarget{p, w})
		}
		for _, c := range w.workload.containers {
			k := scanKey{w.namespace, w.kind, w.name, c.name}
			expected[k] = w.replicas
			if w.workload.podZeroRestarts[c.name] == w.replicas {
				out.restarts[k] = 0
			}
		}
	}
	end := now.Truncate(rightsizingStep)
	start := end.Add(-rightsizingWindow)
	families := []struct {
		name   string
		values *map[scanKey][]float64
		err    error
	}{{name: "cpu", values: &out.cpu}, {name: "memory", values: &out.memory}, {name: "throttle", values: &out.throttle}}
	var workers sync.WaitGroup
	limit := make(chan struct{}, 2)
	for i := range families {
		family := &families[i]
		limit <- struct{}{}
		workers.Go(func() {
			defer func() { <-limit }()
			*family.values, family.err = currentPodRangeValues(ctx, client, family.name, targets, expected, start, end)
		})
	}
	workers.Wait()
	for _, family := range families {
		if family.err != nil {
			out.errors[family.name] = family.err
		}
	}
	return out
}

func currentPodRangeValues(ctx context.Context, client rightsizingScanQuerier, family string, targets []sizingPodTarget, expected map[scanKey]int, start, end time.Time) (map[scanKey][]float64, error) {
	buckets := map[scanKey]map[int64]sizingBucket{}
	for group := range slices.Chunk(targets, sizingPodQueryChunk) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		query := sizingPodRangeQuery(family, group)
		result, err := requireComplete(client.QueryRange(ctx, query, start, end, rightsizingStep))
		if err != nil {
			return nil, err
		}
		addSizingPodBuckets(buckets, expected, group, result, start, end, family)
	}
	values := map[scanKey][]float64{}
	for key, times := range buckets {
		for _, b := range times {
			if b.count == expected[key] {
				values[key] = append(values[key], b.max)
			}
		}
	}
	return values, nil
}

func sizingPodRangeQuery(family string, targets []sizingPodTarget) string {
	namespaces, uids := map[string]bool{}, make([]string, 0, len(targets))
	for _, t := range targets {
		namespaces[t.workload.namespace] = true
		uids = append(uids, t.identity.UID)
	}
	ns := make([]string, 0, len(namespaces))
	for name := range namespaces {
		ns = append(ns, name)
	}
	slices.Sort(ns)
	slices.Sort(uids)
	pattern, _ := prom.PodCgroupPatternForUIDs(uids)
	matcher := "{" + labelRegexMatcher("namespace", ns) + ",id=~" + strconv.Quote(pattern) + `,container!="",container!="POD"}`
	rate := func(metric string) string { return "rate(" + metric + matcher + "[5m])" }
	switch family {
	case "memory":
		return metricMemoryWorkingSet + matcher
	case "throttle":
		return rate(metricThrottledPeriods) + " / " + rate(metricCFSPeriods)
	default:
		return rate(metricCPUUsage)
	}
}

// For each timestamp keep the hottest Pod, only if all replicas have exactly
// one observation. Duplicate scrapes and overlapping container incarnations
// cannot stand in for a missing replica.
func addSizingPodBuckets(out map[scanKey]map[int64]sizingBucket, expected map[scanKey]int, targets []sizingPodTarget, result *prom.QueryResult, start, end time.Time, family string) {
	if result == nil {
		return
	}
	byUID := map[string]sizingPodTarget{}
	for _, t := range targets {
		byUID[t.identity.UID] = t
	}
	type podSeries struct {
		key scanKey
		uid string
	}
	pods := map[podSeries]map[int64]float64{}
	for _, series := range result.Series {
		uid := prom.PodUIDFromCgroupID(series.Labels["id"])
		t, ok := byUID[uid]
		if !ok || series.Labels["namespace"] != t.workload.namespace || series.Labels["pod"] != t.identity.Name {
			continue
		}
		k := scanKey{t.workload.namespace, t.workload.kind, t.workload.name, series.Labels["container"]}
		if expected[k] == 0 {
			continue
		}
		p := podSeries{k, uid}
		if pods[p] == nil {
			pods[p] = map[int64]float64{}
		}
		for _, point := range series.DataPoints {
			if point.Timestamp < start.Unix() || point.Timestamp > end.Unix() || point.Timestamp%int64(rightsizingStep/time.Second) != 0 {
				continue
			}
			v := point.Value
			if _, dup := pods[p][point.Timestamp]; dup || v < 0 || math.IsInf(v, 0) || (family == "throttle" && v > 1) {
				v = math.NaN()
			}
			pods[p][point.Timestamp] = v
		}
	}
	for p, times := range pods {
		if out[p.key] == nil {
			out[p.key] = map[int64]sizingBucket{}
		}
		for timestamp, v := range times {
			if math.IsNaN(v) {
				continue
			}
			b := out[p.key][timestamp]
			b.count++
			b.max = max(b.max, v)
			out[p.key][timestamp] = b
		}
	}
}
