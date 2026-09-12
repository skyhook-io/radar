package prom

import (
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// WorkloadMetricsScope is an operator assertion, never inferred from labels
// returned by a backend: a missing cluster label does not prove isolation.
type WorkloadMetricsScope struct {
	SingleCluster bool
	ClusterLabels map[string]string
}

var metricLabelName = regexp.MustCompile(`^[a-zA-Z_][a-zA-Z0-9_]*$`)

func (s WorkloadMetricsScope) Matchers() (string, error) {
	if s.SingleCluster && len(s.ClusterLabels) > 0 {
		return "", fmt.Errorf("choose single-cluster trust or exact cluster labels, not both")
	}
	if !s.SingleCluster && len(s.ClusterLabels) == 0 {
		return "", fmt.Errorf("workload metrics require an explicit single-cluster endpoint or exact cluster labels")
	}
	keys := make([]string, 0, len(s.ClusterLabels))
	for key, value := range s.ClusterLabels {
		if !metricLabelName.MatchString(key) || strings.HasPrefix(key, "__") || value == "" {
			return "", fmt.Errorf("invalid cluster label %q: use a label name and nonempty exact value", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	matchers := make([]string, 0, len(keys))
	for _, key := range keys {
		matchers = append(matchers, key+"="+strconv.Quote(s.ClusterLabels[key]))
	}
	return strings.Join(matchers, ","), nil
}

func workloadMetricSelector(metric, scope, target string) string {
	if scope != "" {
		target += "," + scope
	}
	return metric + "{" + target + "}"
}

func WorkloadRateWindow(step time.Duration) time.Duration {
	return max(5*time.Minute, 2*step).Round(time.Second)
}

func BuildThrottleQuery(step time.Duration, sel PodSelection, scope WorkloadMetricsScope) (string, error) {
	matchers, err := scope.Matchers()
	if err != nil {
		return "", err
	}
	if sel.IsEmpty() {
		return "", fmt.Errorf("no current pods")
	}
	target := fmt.Sprintf("namespace=%s%s,container!='',container!='POD'", strconv.Quote(sel.Namespace), scopeClause(sel))
	rate := func(metric string) string {
		// Deduplicate scrape targets before summing containers. Summing duplicate
		// scrapes weights containers by the number of scrape jobs observing them.
		return "sum by (pod) (max by (pod,container) (rate(" + workloadMetricSelector(metric, matchers, target) + "[" + WorkloadRateWindow(step).String() + "])))"
	}
	return "100 * (" + rate("container_cpu_cfs_throttled_periods_total") + ") / (" + rate("container_cpu_cfs_periods_total") + ")", nil
}

func BuildWorkloadResourceQuery(step time.Duration, sel PodSelection, scope WorkloadMetricsScope, category MetricCategory) (string, error) {
	matchers, err := scope.Matchers()
	if err != nil {
		return "", err
	}
	if sel.IsEmpty() {
		return "", fmt.Errorf("no current pods")
	}
	target := fmt.Sprintf("namespace=%s%s,container!='',container!='POD'", strconv.Quote(sel.Namespace), scopeClause(sel))
	var inner string
	switch category {
	case CategoryCPU:
		inner = "rate(" + workloadMetricSelector("container_cpu_usage_seconds_total", matchers, target) + "[" + WorkloadRateWindow(step).String() + "])"
	case CategoryMemory:
		inner = workloadMetricSelector("container_memory_working_set_bytes", matchers, target)
	default:
		return "", fmt.Errorf("unsupported workload comparison category %q", category)
	}
	return "sum by (pod) (max by (pod,container) (" + inner + "))", nil
}
