package prometheus

import (
	"context"
	"errors"
	"math"
	"net/url"
	"slices"
	"strconv"
	"sync"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/rightsizing"
)

const (
	sizingPodQueryChunk = 24
	// At a 5m step, 64 container series carry at most 129,088 sample pairs
	// over seven days. Leave space below the transport's 10 MiB limit for
	// additional cgroups, sidecars and scrape history.
	sizingQueryContainers = 64
	// Leave room for the range parameters and a proxied API path on an
	// HTTP server with an 8 KiB request-line limit.
	sizingQueryEncodedBytes = 7 * 1024
)

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
	out := scanBatchEvidence{ownerCoverage: OwnerCoverageCurrentPods, restarts: map[scanKey]float64{}, terminations: map[scanKey]terminationEvidence{}, errors: map[string]error{}}
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
			if history := w.workload.podRestartEvidence[c.name]; history.knownPods == w.replicas {
				out.restarts[k] = float64(history.restarts)
				out.terminations[k] = terminationEvidence{Any: history.restarts > 0}
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
	for len(targets) > 0 {
		groupSize, containers := 0, 0
		for _, target := range targets[:min(len(targets), sizingPodQueryChunk)] {
			containers += len(target.workload.workload.containers)
			if containers > sizingQueryContainers && groupSize > 0 {
				break
			}
			groupSize++
		}
		for {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			group := targets[:groupSize]
			query := sizingPodRangeQuery(family, group)
			if len(url.QueryEscape(query)) > sizingQueryEncodedBytes {
				if groupSize == 1 {
					return nil, prom.ErrWorkloadQueryTooLarge
				}
				groupSize = (groupSize + 1) / 2
				continue
			}
			result, err := requireComplete(client.QueryRange(ctx, query, start, end, rightsizingStep))
			if errors.Is(err, prom.ErrResponseTooLarge) && groupSize > 1 {
				groupSize = (groupSize + 1) / 2
				continue
			}
			if err != nil {
				return nil, err
			}
			addSizingPodBuckets(buckets, expected, group, result, start, end, family)
			targets = targets[groupSize:]
			break
		}
	}
	values := map[scanKey][]float64{}
	for key, times := range buckets {
		for _, b := range times {
			// One verified replica can establish throttling severe enough to
			// withhold a cut. Quiet or missing siblings cannot negate it.
			if b.count == expected[key] || (family == "throttle" && b.max >= rightsizing.ThrottleReviewRatio) {
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
	matcher := "{" + labelRegexMatcher("namespace", ns) + ",id=~" + strconv.Quote(pattern) + `,container!="",container!="POD"`
	if family == "cpu" {
		// Per-core counters are shards, not duplicate scrapes of the full container.
		matcher += `,cpu=~"total|"`
	}
	matcher += "}"
	rate := func(metric string) string { return "rate(" + metric + matcher + "[5m])" }
	var usage string
	switch family {
	case "memory":
		usage = metricMemoryWorkingSet + matcher
	case "throttle":
		usage = rate(metricThrottledPeriods) + " / " + rate(metricCFSPeriods)
	default:
		usage = rate(metricCPUUsage)
	}
	// Merge scrapes of one cgroup after computing each observer's rate or
	// ratio. Keeping id separates overlapping container incarnations.
	return "max by (namespace,pod,container,id) (" + usage + ")"
}

// Each timestamp records the hottest Pod and the number of replicas with
// exactly one observation. Overlapping incarnations invalidate that Pod's sample.
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
