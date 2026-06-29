package opencost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
)

// RESTClient talks to OpenCost's HTTP API via an injected Transport.
//
// Why this exists alongside the PromQL client: OpenCost computes cost
// internally (combining Kubernetes allocation data with cloud pricing),
// then exposes results two ways:
//
//  1. REST at /allocation, /assets, /cloudCost — this package's surface.
//  2. Prometheus-format metrics at /metrics — requires a scrape config
//     in a reachable Prometheus instance. Covered by pkg/prom.
//
// Many clusters have (1) working but (2) not wired up (Prometheus exists
// but no scrape job for OpenCost's /metrics). REST works everywhere OpenCost
// works, so it's the default compute path.
type RESTClient struct {
	t Transport
}

// NewRESTClient wraps the given Transport.
func NewRESTClient(t Transport) *RESTClient {
	return &RESTClient{t: t}
}

// AllocationOptions controls an /allocation query.
type AllocationOptions struct {
	// Window is a human-readable duration or a start/end range. Default "1h".
	// Examples: "1h", "24h", "7d", "2024-01-01T00:00:00Z,2024-01-08T00:00:00Z"
	Window string

	// Aggregate controls how rows are grouped. Any value OpenCost supports:
	// "namespace" (default), "controller", "pod", "container",
	// "cluster", "label:<name>", etc.
	Aggregate string

	// Step controls time-bucketing. "1h", "1d", "1w". Empty => single bucket.
	Step string

	// IncludeIdle adds a synthetic __idle__ row representing unallocated
	// node capacity. Usually "true" so the UI can surface idle cost.
	IncludeIdle bool

	// IncludeSharedCost includes shared/overhead costs in the result.
	IncludeSharedCost bool

	// Filter is a comma-separated OpenCost filter expression (v1.106+).
	// Empty means no filter.
	Filter string
}

func (o AllocationOptions) toQuery() url.Values {
	q := url.Values{}
	if o.Window != "" {
		q.Set("window", o.Window)
	} else {
		q.Set("window", "1h")
	}
	if o.Aggregate != "" {
		q.Set("aggregate", o.Aggregate)
	}
	if o.Step != "" {
		q.Set("step", o.Step)
	}
	if o.IncludeIdle {
		q.Set("includeIdle", "true")
	}
	if o.IncludeSharedCost {
		q.Set("includeSharedCost", "true")
	}
	if o.Filter != "" {
		q.Set("filter", o.Filter)
	}
	return q
}

// Allocation is the per-row allocation data OpenCost returns. Fields are
// the subset of OpenCost's schema this package's compute path consumes;
// full field list is in OpenCost's documentation.
//
// Costs are in the configured currency (USD by default) and sum to the
// given window (not per-hour unless window=1h).
type Allocation struct {
	Name  string `json:"name"`
	Start string `json:"start,omitempty"`
	End   string `json:"end,omitempty"`

	CPUCores              float64 `json:"cpuCores,omitempty"`
	CPUCoreRequestAverage float64 `json:"cpuCoreRequestAverage,omitempty"`
	CPUCoreUsageAverage   float64 `json:"cpuCoreUsageAverage,omitempty"`
	CPUCost               float64 `json:"cpuCost,omitempty"`

	RAMBytes              float64 `json:"ramBytes,omitempty"`
	RAMByteRequestAverage float64 `json:"ramByteRequestAverage,omitempty"`
	RAMByteUsageAverage   float64 `json:"ramByteUsageAverage,omitempty"`
	RAMCost               float64 `json:"ramCost,omitempty"`

	GPUCount float64 `json:"gpuCount,omitempty"`
	GPUCost  float64 `json:"gpuCost,omitempty"`

	PVCost           float64 `json:"pvCost,omitempty"`
	NetworkCost      float64 `json:"networkCost,omitempty"`
	LoadBalancerCost float64 `json:"loadBalancerCost,omitempty"`
	SharedCost       float64 `json:"sharedCost,omitempty"`
	ExternalCost     float64 `json:"externalCost,omitempty"`

	TotalCost       float64 `json:"totalCost,omitempty"`
	TotalEfficiency float64 `json:"totalEfficiency,omitempty"` // 0..1

	// Properties holds arbitrary dimension values (namespace, cluster, labels…).
	// Populated per OpenCost's response shape.
	Properties map[string]interface{} `json:"properties,omitempty"`
}

// AllocationResponse is the envelope OpenCost returns from /allocation.
// The `data` field is an array of time-window dicts: each dict maps an
// aggregate row name (e.g. a namespace name, or "__idle__") → Allocation.
type AllocationResponse struct {
	Code    int                      `json:"code"`
	Status  string                   `json:"status,omitempty"`
	Data    []map[string]*Allocation `json:"data"`
	Message string                   `json:"message,omitempty"`
}

// GetAllocation issues a GET /allocation call.
func (c *RESTClient) GetAllocation(ctx context.Context, opts AllocationOptions) (*AllocationResponse, error) {
	body, err := c.t.Do(ctx, "GET", "/allocation", opts.toQuery())
	if err != nil {
		return nil, fmt.Errorf("opencost.GetAllocation: %w", err)
	}
	var resp AllocationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("opencost.GetAllocation: parse response from %s: %w", c.t.Address(), err)
	}
	if resp.Code != 0 && resp.Code != 200 {
		return &resp, fmt.Errorf("opencost: HTTP %d: %s", resp.Code, resp.Message)
	}
	return &resp, nil
}
