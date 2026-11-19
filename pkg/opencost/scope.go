package opencost

import (
	"context"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

// The metrics the metrics-store cost paths read.
const (
	MetricCPUAllocation       = "container_cpu_allocation"
	MetricMemoryAllocation    = "container_memory_allocation_bytes"
	MetricCPUCostTotal        = "opencost_container_cpu_cost_total"
	MetricMemoryCostTotal     = "opencost_container_memory_cost_total"
	MetricNodeTotalHourlyCost = "node_total_hourly_cost"
	MetricNodeCPUHourlyCost   = "node_cpu_hourly_cost"
	MetricNodeRAMHourlyCost   = "node_ram_hourly_cost"
	MetricNodeGPUCount        = "node_gpu_count"
	MetricNodeGPUHourlyCost   = "node_gpu_hourly_cost"
	MetricPVHourlyCost        = "pv_hourly_cost"
	MetricPVCapacityBytes     = "kube_persistentvolume_capacity_bytes"
	MetricPVClaimRef          = "kube_persistentvolume_claim_ref"
	MetricCPUUsage            = "container_cpu_usage_seconds_total"
	MetricMemoryWorkingSet    = "container_memory_working_set_bytes"
	MetricPodOwner            = "kube_pod_owner"
	MetricReplicaSetOwner     = "kube_replicaset_owner"
)

// StoreScope keeps the metrics-store cost queries to one cluster in a store
// several clusters share. The zero value sends the queries exactly as they
// were before scoping existed.
type StoreScope struct {
	// Filter returns the matchers that keep one metric to this cluster.
	Filter prom.ClusterFilter
	// Strict makes a partial or warning answer to any cost query an
	// unavailable result (ReasonPartialData) instead of totals that may be
	// missing series.
	Strict bool
	// UsageWithheld says the usage metrics could not be kept to this cluster
	// while prices and allocations could: the usage queries are skipped and
	// efficiency and idle read unavailable instead of zero.
	UsageWithheld bool
	// ClusterScope and UsageClusterScope are echoed on the response.
	ClusterScope      *ClusterScope
	UsageClusterScope *ClusterScope
}

// ClusterScope is how a cost answer was kept to this cluster in a metrics
// store that may hold several, as Radar's other metrics views report it.
type ClusterScope struct {
	State  string            `json:"state"`
	Labels map[string]string `json:"labels,omitempty"`
	Note   string            `json:"note"`
}

// SummaryMetrics names what ComputeCostSummaryFromProm reads: the prices,
// allocations and volume metrics the costs need, and the usage metrics only
// efficiency and idle need.
func SummaryMetrics(skipNodeCost bool) (costs, usage []string) {
	costs = []string{
		MetricCPUAllocation, MetricMemoryAllocation, MetricCPUCostTotal, MetricMemoryCostTotal,
		MetricNodeCPUHourlyCost, MetricNodeRAMHourlyCost,
		MetricPVHourlyCost, MetricPVCapacityBytes, MetricPVClaimRef,
	}
	if !skipNodeCost {
		costs = append(costs, MetricNodeTotalHourlyCost, MetricNodeGPUCount, MetricNodeGPUHourlyCost)
	}
	return costs, []string{MetricCPUUsage, MetricMemoryWorkingSet}
}

// WorkloadsMetrics names what ComputeWorkloadsFromPromScoped reads; Pod
// ownership comes from the caller, not the store.
func WorkloadsMetrics() (costs, usage []string) {
	return []string{
		MetricCPUAllocation, MetricMemoryAllocation, MetricCPUCostTotal, MetricMemoryCostTotal,
		MetricNodeCPUHourlyCost, MetricNodeRAMHourlyCost,
	}, []string{MetricCPUUsage, MetricMemoryWorkingSet}
}

// NodeMetrics names what ComputeNodeCostsScoped reads.
func NodeMetrics() []string {
	return []string{MetricNodeTotalHourlyCost, MetricNodeCPUHourlyCost, MetricNodeRAMHourlyCost}
}

// TrendMetrics names what ComputeCostTrendFromProm reads.
func TrendMetrics() []string {
	return []string{MetricCPUAllocation, MetricMemoryAllocation, MetricNodeCPUHourlyCost, MetricNodeRAMHourlyCost}
}

// WorkloadTrendMetrics names what one workload's cost trend reads: a
// Deployment is joined to its Pods through ReplicaSet ownership, other kinds
// through Pod ownership alone.
func WorkloadTrendMetrics(kind string) []string {
	out := []string{
		MetricCPUAllocation, MetricMemoryAllocation, MetricCPUCostTotal, MetricMemoryCostTotal,
		MetricNodeCPUHourlyCost, MetricNodeRAMHourlyCost, MetricPodOwner,
	}
	if canonical, _ := CanonicalWorkloadKind(kind); canonical == "Deployment" {
		out = append(out, MetricReplicaSetOwner)
	}
	return out
}

// ApplicationTrendMetrics names what the application trend reads for one
// namespace's workloads.
func ApplicationTrendMetrics(refs []ApplicationWorkloadRef) []string {
	for _, ref := range refs {
		if canonical, _ := CanonicalWorkloadKind(ref.Kind); canonical == "Deployment" {
			return WorkloadTrendMetrics("Deployment")
		}
	}
	return WorkloadTrendMetrics("")
}

// sel builds one metric's selector from its own matchers and its cluster
// filter, leaving a metric with neither bare as the queries always spelled it.
func sel(f prom.ClusterFilter, metric, matchers string) string {
	filter := ""
	if f != nil {
		filter = f(metric)
	}
	switch {
	case matchers == "" && filter == "":
		return metric
	case matchers == "":
		return metric + "{" + filter + "}"
	case filter == "":
		return metric + "{" + matchers + "}"
	}
	return metric + "{" + matchers + "," + filter + "}"
}

// costQuerier runs one answer's queries and remembers whether any came back
// partial or with a warning.
type costQuerier struct {
	client     *prom.Client
	incomplete bool
}

func (q *costQuerier) note(res *prom.QueryResult) {
	if res != nil && (res.Annotations.Partial || len(res.Annotations.Warnings) > 0) {
		q.incomplete = true
	}
}

func (q *costQuerier) Query(ctx context.Context, query string) (*prom.QueryResult, error) {
	res, err := q.client.Query(ctx, query)
	q.note(res)
	return res, err
}

func (q *costQuerier) QueryRange(ctx context.Context, query string, start, end time.Time, step time.Duration) (*prom.QueryResult, error) {
	res, err := q.client.QueryRange(ctx, query, start, end, step)
	q.note(res)
	return res, err
}

// partial reports whether a strict scope must discard the answer.
func (s StoreScope) partial(q *costQuerier) bool {
	return s.Strict && q.incomplete
}
