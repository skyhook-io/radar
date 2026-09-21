package opencost

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
)

type KubecostClient struct {
	t Transport
}

func NewKubecostClient(t Transport) *KubecostClient {
	return &KubecostClient{t: t}
}

type KubecostAllocationOptions struct {
	Window     string
	Step       string
	Aggregate  string
	Accumulate string
	Idle       bool
	ShareIdle  bool
	Filter     string
}

func (o KubecostAllocationOptions) toQuery() url.Values {
	q := url.Values{}
	if o.Window == "" {
		q.Set("window", "1h")
	} else {
		q.Set("window", o.Window)
	}
	if o.Step != "" {
		q.Set("step", o.Step)
	}
	if o.Aggregate != "" {
		q.Set("aggregate", o.Aggregate)
	}
	if o.Accumulate != "" {
		q.Set("accumulate", o.Accumulate)
	}
	q.Set("idle", strconv.FormatBool(o.Idle))
	q.Set("shareIdle", strconv.FormatBool(o.ShareIdle))
	if o.Filter != "" {
		q.Set("filter", o.Filter)
	}
	return q
}

type KubecostAllocation struct {
	Name       string                 `json:"name"`
	Properties map[string]interface{} `json:"properties"`
	Start      string                 `json:"start"`
	End        string                 `json:"end"`
	Minutes    float64                `json:"minutes"`

	CPUCoreRequestAverage *float64 `json:"cpuCoreRequestAverage"`
	CPUCoreUsageAverage   *float64 `json:"cpuCoreUsageAverage"`
	CPUCost               float64  `json:"cpuCost"`
	RAMByteRequestAverage *float64 `json:"ramByteRequestAverage"`
	RAMByteUsageAverage   *float64 `json:"ramByteUsageAverage"`
	RAMCost               float64  `json:"ramCost"`
	PVCost                float64  `json:"pvCost"`
	NetworkCost           float64  `json:"networkCost"`
	LoadBalancerCost      float64  `json:"loadBalancerCost"`
	SharedCost            float64  `json:"sharedCost"`
	ExternalCost          float64  `json:"externalCost"`
	TotalCost             float64  `json:"totalCost"`
	TotalEfficiency       *float64 `json:"totalEfficiency"`
}

type KubecostAllocationResponse struct {
	Code    int                              `json:"code"`
	Status  string                           `json:"status,omitempty"`
	Data    []map[string]*KubecostAllocation `json:"data"`
	Message string                           `json:"message,omitempty"`
}

func (c *KubecostClient) GetAllocation(ctx context.Context, opts KubecostAllocationOptions) (*KubecostAllocationResponse, error) {
	body, err := c.t.Do(ctx, "GET", "/allocation", opts.toQuery())
	if err != nil {
		return nil, fmt.Errorf("kubecost.GetAllocation: %w", err)
	}
	var resp KubecostAllocationResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("kubecost.GetAllocation: %w from %s: %v", ErrKubecostMalformedResponse, c.t.Address(), err)
	}
	if resp.Code != 0 && resp.Code != 200 {
		return &resp, fmt.Errorf("kubecost allocation API returned code %d: %s", resp.Code, resp.Message)
	}
	return &resp, nil
}

type KubecostAssetOptions struct {
	Window     string
	Accumulate string
	Filter     string
}

func (o KubecostAssetOptions) toQuery() url.Values {
	q := url.Values{}
	if o.Window == "" {
		q.Set("window", "1d")
	} else {
		q.Set("window", o.Window)
	}
	if o.Accumulate != "" {
		q.Set("accumulate", o.Accumulate)
	}
	if o.Filter != "" {
		q.Set("filter", o.Filter)
	}
	return q
}

type KubecostAsset struct {
	Type       string                 `json:"type"`
	Properties map[string]interface{} `json:"properties"`
	Labels     map[string]string      `json:"labels"`
	Start      string                 `json:"start"`
	End        string                 `json:"end"`
	Minutes    float64                `json:"minutes"`
	NodeType   string                 `json:"nodeType"`
	CPUCost    float64                `json:"cpuCost"`
	RAMCost    float64                `json:"ramCost"`
	GPUCost    float64                `json:"gpuCost"`
	TotalCost  float64                `json:"totalCost"`
}

type KubecostAssetsResponse struct {
	Code    int                         `json:"code"`
	Status  string                      `json:"status,omitempty"`
	Data    []map[string]*KubecostAsset `json:"data"`
	Message string                      `json:"message,omitempty"`
}

func (c *KubecostClient) GetAssets(ctx context.Context, opts KubecostAssetOptions) (*KubecostAssetsResponse, error) {
	body, err := c.t.Do(ctx, "GET", "/assets", opts.toQuery())
	if err != nil {
		return nil, fmt.Errorf("kubecost.GetAssets: %w", err)
	}
	var resp KubecostAssetsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("kubecost.GetAssets: %w from %s: %v", ErrKubecostMalformedResponse, c.t.Address(), err)
	}
	if resp.Code != 0 && resp.Code != 200 {
		return &resp, fmt.Errorf("kubecost assets API returned code %d: %s", resp.Code, resp.Message)
	}
	return &resp, nil
}
