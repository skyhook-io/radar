package opencost

// Unavailability reasons — returned in the "reason" field when available=false
// so the frontend can show contextual guidance to the user.
const (
	ReasonNoPrometheus = "no_prometheus" // Prometheus/VictoriaMetrics not found in cluster
	ReasonNoMetrics    = "no_metrics"    // Prometheus found but OpenCost metrics not present
	ReasonQueryError   = "query_error"   // Prometheus found but cost queries failed
	ReasonAccessDenied = "access_denied" // user cannot access the requested resource
	ReasonNotFound     = "not_found"     // requested resource no longer exists
)

// CostSummary is the response for the /api/opencost/summary endpoint.
type CostSummary struct {
	Available         bool            `json:"available"`
	Reason            string          `json:"reason,omitempty"` // Set when available=false: no_prometheus, no_metrics, query_error
	Currency          string          `json:"currency,omitempty"`
	Window            string          `json:"window,omitempty"`
	TotalHourlyCost   float64         `json:"totalHourlyCost,omitempty"`
	TotalStorageCost  float64         `json:"totalStorageCost,omitempty"`
	TotalNetworkCost  float64         `json:"totalNetworkCost,omitempty"`
	TotalIdleCost     float64         `json:"totalIdleCost,omitempty"`
	ClusterEfficiency float64         `json:"clusterEfficiency,omitempty"` // 0-100
	Namespaces        []NamespaceCost `json:"namespaces,omitempty"`
}

// NamespaceCost holds per-row cost breakdown. The name reflects the
// default aggregation; the struct is also used for controller and pod
// rows — Kind disambiguates (empty = namespace).
type NamespaceCost struct {
	Name            string  `json:"name"`
	Kind            string  `json:"kind,omitempty"`      // "namespace" (default if empty) | "controller" | "pod"
	Namespace       string  `json:"namespace,omitempty"` // populated for controller/pod rows
	HourlyCost      float64 `json:"hourlyCost"`
	CPUCost         float64 `json:"cpuCost"`
	MemoryCost      float64 `json:"memoryCost"`
	StorageCost     float64 `json:"storageCost,omitempty"`
	NetworkCost     float64 `json:"networkCost,omitempty"`
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

type WorkloadCostDetailResponse struct {
	Available bool          `json:"available"`
	Reason    string        `json:"reason,omitempty"`
	Namespace string        `json:"namespace"`
	Kind      string        `json:"kind"`
	Name      string        `json:"name"`
	Current   *WorkloadCost `json:"current,omitempty"`
}

// WorkloadCost holds per-workload cost breakdown within a namespace.
type WorkloadCost struct {
	Name                 string  `json:"name"`
	Kind                 string  `json:"kind"` // Deployment, StatefulSet, DaemonSet, Job, standalone, staticpod
	HourlyCost           float64 `json:"hourlyCost"`
	CPUCost              float64 `json:"cpuCost"`
	MemoryCost           float64 `json:"memoryCost"`
	Replicas             int     `json:"replicas"`
	CPUUsageCost         float64 `json:"cpuUsageCost,omitempty"`
	MemoryUsageCost      float64 `json:"memoryUsageCost,omitempty"`
	CPUUsageAvailable    bool    `json:"cpuUsageAvailable"`
	MemoryUsageAvailable bool    `json:"memoryUsageAvailable"`
	CPUAllocationUse     float64 `json:"cpuAllocationUse"`
	MemoryAllocationUse  float64 `json:"memoryAllocationUse"`
	Efficiency           float64 `json:"efficiency,omitempty"` // 0-100
	IdleCost             float64 `json:"idleCost,omitempty"`
}

// CostTrendResponse is the response for the /api/opencost/trend endpoint.
type CostTrendResponse struct {
	Available bool              `json:"available"`
	Reason    string            `json:"reason,omitempty"`
	Range     string            `json:"range"`
	Series    []CostTrendSeries `json:"series,omitempty"`
}

type WorkloadCostTrendResponse struct {
	Available       bool            `json:"available"`
	Reason          string          `json:"reason,omitempty"`
	Namespace       string          `json:"namespace"`
	Kind            string          `json:"kind"`
	Name            string          `json:"name"`
	Range           string          `json:"range"`
	WindowTotalCost float64         `json:"windowTotalCost,omitempty"`
	DataPoints      []CostDataPoint `json:"dataPoints,omitempty"`
}

type ApplicationWorkloadRef struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
}

type ApplicationWorkloadCostInput struct {
	ApplicationWorkloadRef
	DesiredReplicas int `json:"desiredReplicas,omitempty"`
}

type ApplicationCostCoverage struct {
	Total       int                         `json:"total"`
	Included    int                         `json:"included"`
	Unavailable []ApplicationWorkloadStatus `json:"unavailable,omitempty"`
	Unsupported []ApplicationWorkloadRef    `json:"unsupported,omitempty"`
}

type ApplicationWorkloadStatus struct {
	ApplicationWorkloadRef
	Reason       string `json:"reason"`
	ScaledToZero bool   `json:"scaledToZero,omitempty"`
}

type ApplicationCostTotals struct {
	HourlyCost           float64 `json:"hourlyCost"`
	CPUCost              float64 `json:"cpuCost"`
	MemoryCost           float64 `json:"memoryCost"`
	Replicas             int     `json:"replicas"`
	CPUUsageCost         float64 `json:"cpuUsageCost,omitempty"`
	MemoryUsageCost      float64 `json:"memoryUsageCost,omitempty"`
	CPUUsageAvailable    bool    `json:"cpuUsageAvailable"`
	MemoryUsageAvailable bool    `json:"memoryUsageAvailable"`
	CPUAllocationUse     float64 `json:"cpuAllocationUse"`
	MemoryAllocationUse  float64 `json:"memoryAllocationUse"`
}

type ApplicationWorkloadCost struct {
	ApplicationWorkloadRef
	Available    bool          `json:"available"`
	Reason       string        `json:"reason,omitempty"`
	ScaledToZero bool          `json:"scaledToZero,omitempty"`
	Current      *WorkloadCost `json:"current,omitempty"`
}

type ApplicationCostResponse struct {
	Available bool                      `json:"available"`
	Reason    string                    `json:"reason,omitempty"`
	Partial   bool                      `json:"partial,omitempty"`
	Totals    ApplicationCostTotals     `json:"totals"`
	Coverage  ApplicationCostCoverage   `json:"coverage"`
	Workloads []ApplicationWorkloadCost `json:"workloads,omitempty"`
}

type ApplicationCostTrendSeries struct {
	ApplicationWorkloadRef
	WindowTotalCost float64         `json:"windowTotalCost,omitempty"`
	DataPoints      []CostDataPoint `json:"dataPoints,omitempty"`
}

type ApplicationCostTrendResponse struct {
	Available       bool                         `json:"available"`
	Reason          string                       `json:"reason,omitempty"`
	Range           string                       `json:"range"`
	Partial         bool                         `json:"partial,omitempty"`
	WindowTotalCost float64                      `json:"windowTotalCost,omitempty"`
	DataPoints      []CostDataPoint              `json:"dataPoints,omitempty"`
	Series          []ApplicationCostTrendSeries `json:"series,omitempty"`
	Coverage        ApplicationCostCoverage      `json:"coverage"`
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
	ProviderID   string  `json:"providerID,omitempty"`
	InstanceType string  `json:"instanceType,omitempty"`
	Region       string  `json:"region,omitempty"`
	HourlyCost   float64 `json:"hourlyCost"`
	CPUCost      float64 `json:"cpuCost"`
	MemoryCost   float64 `json:"memoryCost"`
}
