package opencost

// Unavailability reasons — returned in the "reason" field when available=false
// so the frontend can show contextual guidance to the user.
const (
	ReasonNoPrometheus = "no_prometheus" // Prometheus/VictoriaMetrics not found in cluster
	ReasonNoMetrics    = "no_metrics"    // Prometheus found but OpenCost metrics not present
	ReasonQueryError   = "query_error"   // Prometheus found but cost queries failed
)

// CostSummary is the response for the /api/opencost/summary endpoint.
type CostSummary struct {
	Available         bool            `json:"available"`
	Reason            string          `json:"reason,omitempty"` // Set when available=false: no_prometheus, no_metrics, query_error
	Currency          string          `json:"currency,omitempty"`
	Window            string          `json:"window,omitempty"`
	TotalHourlyCost   float64         `json:"totalHourlyCost,omitempty"`
	TotalStorageCost  float64         `json:"totalStorageCost,omitempty"`
	TotalIdleCost     float64         `json:"totalIdleCost,omitempty"`
	ClusterEfficiency float64         `json:"clusterEfficiency,omitempty"` // 0-100
	Namespaces        []NamespaceCost `json:"namespaces,omitempty"`
}

// NamespaceCost holds per-namespace cost breakdown.
type NamespaceCost struct {
	Name            string  `json:"name"`
	HourlyCost      float64 `json:"hourlyCost"`
	CPUCost         float64 `json:"cpuCost"`
	MemoryCost      float64 `json:"memoryCost"`
	StorageCost     float64 `json:"storageCost,omitempty"`
	CPUUsageCost    float64 `json:"cpuUsageCost,omitempty"`
	MemoryUsageCost float64 `json:"memoryUsageCost,omitempty"`
	Efficiency      float64 `json:"efficiency,omitempty"` // 0-100
	IdleCost        float64 `json:"idleCost,omitempty"`
}

// WorkloadCostResponse is the response for the /api/opencost/workloads endpoint.
type WorkloadCostResponse struct {
	Available bool           `json:"available"`
	Reason    string         `json:"reason,omitempty"`
	Namespace string         `json:"namespace"`
	Workloads []WorkloadCost `json:"workloads"`
}

// WorkloadCost holds per-workload cost breakdown within a namespace.
type WorkloadCost struct {
	Name            string  `json:"name"`
	Kind            string  `json:"kind"` // Deployment, StatefulSet, DaemonSet, Job, standalone
	HourlyCost      float64 `json:"hourlyCost"`
	CPUCost         float64 `json:"cpuCost"`
	MemoryCost      float64 `json:"memoryCost"`
	Replicas        int     `json:"replicas"`
	CPUUsageCost    float64 `json:"cpuUsageCost,omitempty"`
	MemoryUsageCost float64 `json:"memoryUsageCost,omitempty"`
	Efficiency      float64 `json:"efficiency,omitempty"` // 0-100
	IdleCost        float64 `json:"idleCost,omitempty"`
}

// CostTrendResponse is the response for the /api/opencost/trend endpoint.
type CostTrendResponse struct {
	Available bool              `json:"available"`
	Reason    string            `json:"reason,omitempty"`
	Range     string            `json:"range"`
	Series    []CostTrendSeries `json:"series,omitempty"`
}

// CostTrendSeries holds cost data points for a single namespace.
type CostTrendSeries struct {
	Namespace  string          `json:"namespace"`
	DataPoints []CostDataPoint `json:"dataPoints"`
}

// CostDataPoint is a single (timestamp, value) pair for cost trends.
type CostDataPoint struct {
	Timestamp int64   `json:"timestamp"`
	Value     float64 `json:"value"`
}

// NodeCostResponse is the response for the /api/opencost/nodes endpoint.
type NodeCostResponse struct {
	Available bool       `json:"available"`
	Reason    string     `json:"reason,omitempty"`
	Nodes     []NodeCost `json:"nodes,omitempty"`
}

// NodeCost holds per-node cost breakdown.
type NodeCost struct {
	Name         string  `json:"name"`
	InstanceType string  `json:"instanceType,omitempty"`
	Region       string  `json:"region,omitempty"`
	HourlyCost   float64 `json:"hourlyCost"`
	CPUCost      float64 `json:"cpuCost"`
	MemoryCost   float64 `json:"memoryCost"`
}
