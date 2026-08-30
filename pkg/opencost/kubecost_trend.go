package opencost

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"
)

var ErrKubecostTrendQuery = errors.New("Kubecost trend query failed")

type KubecostTrendOptions struct {
	Range      string
	MaxSeries  int
	Namespaces []string
	Currency   string
	ClusterID  string
	now        time.Time
}

func ComputeKubecostTrend(ctx context.Context, client *KubecostClient, opts KubecostTrendOptions) (*CostTrendResponse, error) {
	if client == nil {
		return nil, fmt.Errorf("kubecost client is not configured")
	}
	rangeLabel, rangeConfig := kubecostTrendConfig(opts.Range)
	if opts.Currency == "" {
		opts.Currency = DefaultCurrency
	}
	windowEnd := opts.now.UTC()
	if windowEnd.IsZero() {
		windowEnd = time.Now().UTC()
	}
	response := &CostTrendResponse{
		Currency:    opts.Currency,
		Range:       rangeLabel,
		Source:      "kubecost",
		WindowStart: windowEnd.Add(-rangeConfig.duration).Unix(),
		WindowEnd:   windowEnd.Unix(),
	}
	allocationOptions := func(window string) KubecostAllocationOptions {
		return KubecostAllocationOptions{
			Window:     window,
			Step:       rangeConfig.step,
			Aggregate:  "cluster,namespace",
			Accumulate: "false",
			Idle:       false,
			ShareIdle:  false,
			Filter:     kubecostFilter(opts.ClusterID, ""),
		}
	}
	resp, err := client.GetAllocation(ctx, allocationOptions(rangeConfig.queryWindow))
	if err != nil {
		return nil, fmt.Errorf("%w: %w", ErrKubecostTrendQuery, err)
	}

	var allowed map[string]struct{}
	if opts.Namespaces != nil {
		allowed = make(map[string]struct{}, len(opts.Namespaces))
		for _, namespace := range opts.Namespaces {
			allowed[namespace] = struct{}{}
		}
	}

	pointsByNamespace, timestamps, err := collectKubecostTrendPoints(resp, opts.ClusterID, allowed)
	if err != nil {
		return nil, err
	}
	if len(pointsByNamespace) == 0 {
		response.Reason = ReasonNoMetrics
		return response, nil
	}
	latestTimestamp := latestKubecostTrendTimestamp(timestamps)
	response.WindowEnd = latestTimestamp
	response.WindowStart = latestTimestamp - int64(rangeConfig.duration.Seconds())
	queryStart := windowEnd.Add(-rangeConfig.queryDuration).Unix()
	if response.WindowStart < queryStart {
		exactWindow := fmt.Sprintf(
			"%s,%s",
			time.Unix(response.WindowStart, 0).UTC().Format(time.RFC3339),
			time.Unix(response.WindowEnd, 0).UTC().Format(time.RFC3339),
		)
		resp, err = client.GetAllocation(ctx, allocationOptions(exactWindow))
		if err != nil {
			return nil, fmt.Errorf("%w: %w", ErrKubecostTrendQuery, err)
		}
		exactPoints, exactTimestamps, collectErr := collectKubecostTrendPoints(resp, opts.ClusterID, allowed)
		if collectErr != nil {
			return nil, collectErr
		}
		if len(exactPoints) > 0 {
			pointsByNamespace = exactPoints
			timestamps = exactTimestamps
		}
		latestTimestamp = latestKubecostTrendTimestamp(timestamps)
		response.WindowEnd = latestTimestamp
		response.WindowStart = latestTimestamp - int64(rangeConfig.duration.Seconds())
	}
	response.DataThrough = time.Unix(latestTimestamp, 0).UTC().Format(time.RFC3339)

	for namespace, values := range pointsByNamespace {
		for timestamp := range values {
			if timestamp < response.WindowStart {
				delete(values, timestamp)
				delete(timestamps, timestamp)
			}
		}
		if len(values) == 0 {
			delete(pointsByNamespace, namespace)
		}
	}
	if len(timestamps) < 2 {
		response.Reason = ReasonInsufficientHistory
		return response, nil
	}

	type rankedSeries struct {
		namespace string
		lastCost  float64
	}
	ranked := make([]rankedSeries, 0, len(pointsByNamespace))
	for namespace, values := range pointsByNamespace {
		ranked = append(ranked, rankedSeries{namespace: namespace, lastCost: values[latestTimestamp]})
	}
	sort.Slice(ranked, func(i, j int) bool {
		if ranked[i].lastCost == ranked[j].lastCost {
			return ranked[i].namespace < ranked[j].namespace
		}
		return ranked[i].lastCost > ranked[j].lastCost
	})

	maxSeries := opts.MaxSeries
	if maxSeries <= 0 {
		maxSeries = 8
	}
	for i, series := range ranked {
		if i >= maxSeries {
			break
		}
		response.Series = append(response.Series, CostTrendSeries{
			Namespace:  series.namespace,
			DataPoints: kubecostTrendPoints(pointsByNamespace[series.namespace]),
		})
	}
	if len(ranked) > maxSeries {
		otherByTimestamp := map[int64]float64{}
		for _, series := range ranked[maxSeries:] {
			for timestamp, value := range pointsByNamespace[series.namespace] {
				otherByTimestamp[timestamp] += value
			}
		}
		other := CostTrendSeries{Namespace: "other", DataPoints: make([]CostDataPoint, 0, len(otherByTimestamp))}
		for timestamp, value := range otherByTimestamp {
			other.DataPoints = append(other.DataPoints, CostDataPoint{Timestamp: timestamp, Value: roundTo(value, 4)})
		}
		sort.Slice(other.DataPoints, func(i, j int) bool { return other.DataPoints[i].Timestamp < other.DataPoints[j].Timestamp })
		response.Series = append(response.Series, other)
	}
	response.Available = true
	return response, nil
}

func collectKubecostTrendPoints(
	resp *KubecostAllocationResponse,
	clusterID string,
	allowed map[string]struct{},
) (map[string]map[int64]float64, map[int64]struct{}, error) {
	pointsByNamespace := map[string]map[int64]float64{}
	timestamps := map[int64]struct{}{}
	for bucketIndex, bucket := range resp.Data {
		bucketCosts := map[string]float64{}
		bucketTimestamp := kubecostBucketTimestamp(bucket)
		for key, allocation := range bucket {
			if allocation == nil || kubecostIdleRow(key, allocation) {
				continue
			}
			if err := requireKubecostCluster(allocation.Properties, clusterID); err != nil {
				return nil, nil, err
			}
			namespace := propertyString(allocation.Properties, "namespace")
			if namespace == "__unallocated__" || strings.Contains(key, "__unallocated__") {
				continue
			}
			if namespace == "" {
				return nil, nil, fmt.Errorf("allocation bucket %d row %q is missing namespace identity", bucketIndex, key)
			}
			if allowed != nil {
				if _, ok := allowed[namespace]; !ok {
					continue
				}
			}
			hours, err := kubecostRowHours(allocation.Start, allocation.End, allocation.Minutes)
			if err != nil {
				return nil, nil, fmt.Errorf("allocation bucket %d row %q: %w", bucketIndex, key, err)
			}
			if _, err := parseKubecostTimestamp(allocation.End); err != nil {
				return nil, nil, fmt.Errorf("allocation bucket %d row %q: %w", bucketIndex, key, err)
			}
			total := allocation.TotalCost
			if total == 0 {
				total = allocation.CPUCost + allocation.RAMCost + allocation.PVCost + allocation.NetworkCost + allocation.LoadBalancerCost + allocation.SharedCost + allocation.ExternalCost
			}
			bucketCosts[namespace] += total / hours
		}
		if bucketTimestamp == 0 {
			continue
		}
		for namespace, value := range bucketCosts {
			if pointsByNamespace[namespace] == nil {
				pointsByNamespace[namespace] = map[int64]float64{}
			}
			pointsByNamespace[namespace][bucketTimestamp] = roundTo(value, 4)
			timestamps[bucketTimestamp] = struct{}{}
		}
	}
	return pointsByNamespace, timestamps, nil
}

func latestKubecostTrendTimestamp(timestamps map[int64]struct{}) int64 {
	var latest int64
	for timestamp := range timestamps {
		if timestamp > latest {
			latest = timestamp
		}
	}
	return latest
}

func kubecostBucketTimestamp(bucket map[string]*KubecostAllocation) int64 {
	var timestamp int64
	for _, allocation := range bucket {
		if allocation == nil {
			continue
		}
		end, err := parseKubecostTimestamp(allocation.End)
		if err == nil && end.Unix() > timestamp {
			timestamp = end.Unix()
		}
	}
	return timestamp
}

func kubecostTrendPoints(values map[int64]float64) []CostDataPoint {
	points := make([]CostDataPoint, 0, len(values))
	for timestamp, value := range values {
		points = append(points, CostDataPoint{Timestamp: timestamp, Value: value})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Timestamp < points[j].Timestamp })
	return points
}

type kubecostTrendRangeSettings struct {
	duration      time.Duration
	queryDuration time.Duration
	queryWindow   string
	step          string
}

func kubecostTrendConfig(value string) (string, kubecostTrendRangeSettings) {
	switch value {
	case "6h":
		return value, kubecostTrendRangeSettings{
			duration:      6 * time.Hour,
			queryDuration: 12 * time.Hour,
			queryWindow:   "12h",
			step:          "1h",
		}
	case "7d":
		return value, kubecostTrendRangeSettings{
			duration:      7 * 24 * time.Hour,
			queryDuration: 8 * 24 * time.Hour,
			queryWindow:   "8d",
			step:          "24h",
		}
	default:
		return "24h", kubecostTrendRangeSettings{
			duration:      24 * time.Hour,
			queryDuration: 30 * time.Hour,
			queryWindow:   "30h",
			step:          "1h",
		}
	}
}
