package mcp

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/capacity"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
)

// The Costs UI projects monthly spend as the hourly rate times this constant;
// the tool emits the projection server-side so an agent reporting a monthly
// figure to a user cannot disagree with the screen next to them.
const monthlyProjectionHours = 730

const (
	costDefaultLimit    = 20
	costMaxLimit        = 100
	costCallBudget      = 45 * time.Second
	costHourlyExplainer = "Costs are hourly rates."
	costRateExplainer   = costHourlyExplainer + " Monthly projections = the corresponding hourly rate x 730."
	// The denominator is allocation — max(requested, observed) — not the
	// request, so the ratio caps at 100 and a low value is not by itself a
	// defect. Neither fact is derivable from the numbers in the response.
	costEfficiencyExplainer = "efficiencyPercent compares observed use against cost allocation, which is the greater of requested and observed usage — not against the request. It therefore caps at 100, and a low value is not by itself a defect or recoverable money (bursty workloads, P95-sized requests and HPA headroom all read low). Use get_rightsizing to judge whether a request should change; it applies OOM and HPA gating a ratio cannot."
	// Enumerating what the workload total leaves out would go stale: the
	// namespace row's basis is the cost source's own total, which carries
	// storage, network, shared and external cost on Kubecost.
	costWorkloadTotalExplainer = "totals here sum CPU and memory allocation only, so they do not reconcile with this namespace's row in view=summary, whose basis is whatever the cost source totals."
	// With a kind/name selector the rows are not a truncation of the namespace,
	// so the namespace total would be read as the workload's own spend.
	costSelectedWorkloadTotalExplainer = "totals cover only the workload named by kind and name, not the namespace — call view=workloads without kind/name for the namespace's spend."
	// The Prometheus path returns no_metrics both when the source is healthy
	// with nothing in scope and when no cost source is installed at all, so this
	// text must not assert either one.
	costScopeEmptyRemediation = "No allocation was reported for the namespaces in scope. Either the cost source is healthy and that scope holds nothing (no workloads running, or a narrower scope than expected), or no cost source is installed yet — the two are indistinguishable here. Check whether OpenCost or Kubecost is running before telling the user to install it."
	costRequestedViewFmt      = "Totals cover only namespace %s, not the whole cluster."
	costPartialViewFmt        = "Totals cover only the %d namespace(s) this identity can read, not the whole cluster."
	// A pin and an RBAC limit produce the same namespace list. Reporting the
	// pin as a permission problem would have an agent tell a cluster-admin
	// their access is restricted.
	costPinnedViewFmt               = "Totals cover only namespace %s, which radar is pinned to with --namespace-scope — not the whole cluster, and not a limit on this identity's permissions."
	costWasteExplainer              = "unallocatedHourlyCost is node compute capacity no workload requested or used; unusedRequestHourlyCost is capacity requested but not used. The two do not overlap, and neither is money saved until nodes are actually removed. unusedRequestHourlyCost covers CPU and memory requests only, so idle GPUs are not in it."
	costWorkloadNotFoundRemediation = "The cost source answered for this namespace but reported no allocation for the requested workload. Check the kind and name, or call view=workloads without kind/name to see what the namespace does have."
	costTrendSeriesExplainer        = "Series values are hourly rates at each point, not cumulative spend. Each series and trendTotal carry startHourlyCost, endHourlyCost and changePercent for net change, plus minHourlyCost, peakHourlyCost and peakAt for observed excursions. A small net change does not mean the interval was flat; use include_points=true to inspect its shape. These extrema describe returned samples, not spikes between samples. changePercent spans the trendTotal's from..to, which is shorter than range when the source retains less history."
	// The cost source caps the series and sums the remainder into one named
	// "other". Undeclared, an agent reads the few it got as the whole cluster.
	costTrendCappedFmt = "Of %d namespaces in scope, %d have named series; the remaining %d are grouped into the type=remainder series. trendTotal covers every namespace, so it is a whole-scope figure even though the series are not."
)

// reasonWorkloadNotFound is Radar's own reason: the cost source was healthy and
// the namespace had rows, but none matched the requested workload.
const reasonWorkloadNotFound = "workload_not_found"

// reasonCostDeadlineExceeded marks a call the budget cut short, so a slow cost
// source is not reported as a broken one.
const reasonCostDeadlineExceeded = "cost_deadline_exceeded"

// Cost figures are floats accumulated across rows, so they arrive as
// 0.14640000000000006. Agents echo them verbatim into user-facing answers.
func roundHourly(v float64) float64  { return math.Round(v*1e4) / 1e4 }
func roundMonthly(v float64) float64 { return math.Round(v*100) / 100 }

type getCostInput struct {
	IncludePoints *bool  `json:"include_points,omitempty" jsonschema:"trend only: include raw time-series points (default false); summaries are always returned"`
	View          string `json:"view,omitempty" jsonschema:"summary (default) for cluster totals plus per-namespace spend, workloads for one namespace broken down by workload (requires namespace), nodes for per-node spend, trend for spend over time"`
	Namespace     string `json:"namespace,omitempty" jsonschema:"required for view=workloads; filters summary and trend to one namespace"`
	Range         string `json:"range,omitempty" jsonschema:"trend only: 6h, 24h (default), or 7d"`
	Limit         int    `json:"limit,omitempty" jsonschema:"max rows for summary/workloads/nodes (default 20, max 100)"`
	Kind          string `json:"kind,omitempty" jsonschema:"with view=workloads only: return just this workload (Deployment, StatefulSet, DaemonSet, ...) instead of the namespace's top spenders; requires name"`
	Name          string `json:"name,omitempty" jsonschema:"workloads: exact workload name, requires kind; nodes: exact node name"`
}

type costTotals struct {
	AllocatedHourlyCost        *float64      `json:"allocatedHourlyCost,omitempty"`
	AllocatedMonthlyProjection *float64      `json:"allocatedMonthlyProjection,omitempty"`
	NodeHourlyCost             *nullableCost `json:"nodeHourlyCost,omitempty"`
	NodeMonthlyProjection      *nullableCost `json:"nodeMonthlyProjection,omitempty"`
	UnallocatedCost            *nullableCost `json:"unallocatedHourlyCost,omitempty"`
	UnusedRequestCost          *float64      `json:"unusedRequestHourlyCost,omitempty"`
	StorageCost                float64       `json:"storageHourlyCost,omitempty"`
	NetworkCost                float64       `json:"networkHourlyCost,omitempty"`
	ClusterEfficiency          *nullableCost `json:"efficiencyPercent,omitempty"`
}

// nullableCost marshals as a number or an explicit null. A summary that could
// not measure a figure says null, where 0 would claim there was none.
type nullableCost struct {
	value *float64
}

func (c nullableCost) MarshalJSON() ([]byte, error) {
	if c.value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(roundHourly(*c.value))
}

type namespaceCostRow struct {
	Name        string  `json:"name"`
	HourlyCost  float64 `json:"hourlyCost"`
	CPUCost     float64 `json:"cpuHourlyCost"`
	MemoryCost  float64 `json:"memoryHourlyCost"`
	StorageCost float64 `json:"storageHourlyCost,omitempty"`
	NetworkCost float64 `json:"networkHourlyCost,omitempty"`
	// Pointer: nil drops it when usage is unavailable, while a measured 0 stays.
	UnusedRequestCost *float64 `json:"unusedRequestHourlyCost,omitempty"`
	// Pointer, not omitempty: 0% is a measurement on a row using nothing.
	Efficiency       *float64 `json:"efficiencyPercent"`
	UsageUnavailable bool     `json:"usageUnavailable,omitempty"`
}

type workloadCostRow struct {
	Name       string  `json:"name"`
	Kind       string  `json:"kind"`
	Replicas   int     `json:"replicas"`
	HourlyCost float64 `json:"hourlyCost"`
	CPUCost    float64 `json:"cpuHourlyCost"`
	MemoryCost float64 `json:"memoryHourlyCost"`
	// Pointer: nil drops it when usage is unavailable, while a measured 0 stays.
	UnusedRequestCost *float64 `json:"unusedRequestHourlyCost,omitempty"`
	// Pointer, not omitempty: 0% is a measurement on a row using nothing.
	Efficiency       *float64 `json:"efficiencyPercent"`
	UsageUnavailable bool     `json:"usageUnavailable,omitempty"`
}

// nodeCostRow deliberately omits the per-node CPU and memory components. The
// OpenCost path reports them as per-vCPU-hour and per-GiB-hour unit prices
// while Kubecost reports whole-node totals, so one field name carries two
// different quantities. hourlyCost, instanceType and region answer "which
// nodes cost most" without that ambiguity.
type nodeCostRow struct {
	Name         string       `json:"name"`
	InstanceType string       `json:"instanceType,omitempty"`
	Region       string       `json:"region,omitempty"`
	Pool         *nodePoolRef `json:"pool,omitempty"`
	CapacityType string       `json:"capacityType,omitempty"`
	HourlyCost   float64      `json:"hourlyCost"`
}

type nodePoolRef struct {
	Name   string `json:"name"`
	Source string `json:"source"`
}

// costTrendSummary is the question an agent asks of a trend — did spend grow —
// answered server-side without requiring raw points.
type costTrendSummary struct {
	*costTrendExtrema
	// Basis names what the trend sums, which differs by source, so the total
	// can be compared with the right summary field.
	Basis string `json:"basis,omitempty"`
	// From and To are the first and last points, which span less than the
	// requested range when the source retains less history.
	From          string   `json:"from"`
	To            string   `json:"to"`
	Start         float64  `json:"startHourlyCost"`
	End           float64  `json:"endHourlyCost"`
	ChangePercent *float64 `json:"changePercent"`
	Points        int      `json:"sampleCount"`
}

type costTrendExtrema struct {
	MinHourlyCost  float64 `json:"minHourlyCost"`
	PeakHourlyCost float64 `json:"peakHourlyCost"`
	PeakAt         string  `json:"peakAt"`
}

type costTrendPoint struct {
	Timestamp string  `json:"timestamp"`
	Value     float64 `json:"hourlyCost"`
}

type costTrendSeriesDTO struct {
	*costTrendExtrema
	Type          string           `json:"type"`
	Namespace     string           `json:"namespace,omitempty"`
	Start         float64          `json:"startHourlyCost"`
	End           float64          `json:"endHourlyCost"`
	ChangePercent *float64         `json:"changePercent"`
	DataPoints    []costTrendPoint `json:"dataPoints,omitempty"`
}

type costResponse struct {
	View           string               `json:"view"`
	Available      bool                 `json:"available"`
	Reason         string               `json:"reason,omitempty"`
	Remediation    string               `json:"remediation,omitempty"`
	Source         string               `json:"source,omitempty"`
	Currency       string               `json:"currency"`
	Window         string               `json:"window,omitempty"`
	Range          string               `json:"range,omitempty"`
	DataThrough    string               `json:"dataThrough,omitempty"`
	Namespace      string               `json:"namespace,omitempty"`
	NamespaceScope []string             `json:"effectiveNamespaces,omitempty"`
	Totals         *costTotals          `json:"totals,omitempty"`
	Namespaces     []namespaceCostRow   `json:"namespaces,omitempty"`
	Workloads      []workloadCostRow    `json:"workloads,omitempty"`
	Nodes          []nodeCostRow        `json:"nodes,omitempty"`
	Series         []costTrendSeriesDTO `json:"series,omitempty"`
	TrendTotal     *costTrendSummary    `json:"trendTotal,omitempty"`
	SeriesCapped   bool                 `json:"seriesCapped,omitempty"`
	Truncated      bool                 `json:"truncated,omitempty"`
	// Row counts accompany truncated: without them the rows cannot be
	// reconciled against the totals, and "top 20 of N" is unsayable.
	NamespaceCount int `json:"namespaceCount,omitempty"`
	WorkloadCount  int `json:"workloadCount,omitempty"`
	NodeCount      int `json:"nodeCount,omitempty"`
	// NodesNotInCluster counts nodes the cost source still reports that the
	// cluster no longer has, which is why their rows carry no pool.
	NodesNotInCluster int      `json:"nodesNotInCluster,omitempty"`
	Guidance          []string `json:"guidance,omitempty"`
}

func handleGetCost(ctx context.Context, _ *mcp.CallToolRequest, input getCostInput) (*mcp.CallToolResult, any, error) {
	// MCP tool calls sit outside the router's 60s timeout group, and the
	// MCP prom client's 200s socket backstop is a hang guard, not a query
	// budget. Without this, a slow source holds the call for minutes past
	// the point the caller gave up. Same shape as the rightsizing scan budget.
	ctx, cancel := context.WithTimeout(ctx, costCallBudget)
	defer cancel()

	view := strings.ToLower(strings.TrimSpace(input.View))
	if view == "" {
		view = "summary"
	}
	// Normalize before validating: validating a trimmed value and dispatching
	// the raw one lets " 7d " pass the check and then resolve as 24h.
	input.Range = strings.TrimSpace(input.Range)

	limit := input.Limit
	if limit < 0 {
		return nil, nil, errors.New("limit must be between 1 and 100")
	}
	if limit == 0 {
		limit = costDefaultLimit
	}

	// Silently dropping a parameter the caller set lets an agent believe a
	// filter applied. Reject the combination instead.
	if view != "trend" && input.IncludePoints != nil {
		return nil, nil, fmt.Errorf("include_points applies only to view=trend, not view=%s", view)
	}
	if view != "trend" && input.Range != "" {
		return nil, nil, fmt.Errorf("range applies only to view=trend, not view=%s", view)
	}
	if view == "trend" && input.Limit > 0 {
		return nil, nil, errors.New("limit applies to the row views (summary, workloads, nodes), not view=trend")
	}
	// Clamping instead would return costMaxLimit rows with no sign the cap
	// moved, which reads as the whole list.
	if limit > costMaxLimit {
		return nil, nil, fmt.Errorf("limit %d exceeds the maximum of %d — pass %d or fewer; silently returning %d would look like every row", input.Limit, costMaxLimit, costMaxLimit, costMaxLimit)
	}
	input.Kind = strings.TrimSpace(input.Kind)
	input.Name = strings.TrimSpace(input.Name)
	if view != "workloads" && (input.Kind != "" || (input.Name != "" && view != "nodes")) {
		return nil, nil, fmt.Errorf("kind applies only to view=workloads; name applies to workloads or nodes, not view=%s", view)
	}
	if view == "workloads" && (input.Kind == "") != (input.Name == "") {
		return nil, nil, errors.New("kind and name go together — pass both to target one workload, or neither to rank the namespace's top spenders")
	}

	resp, err := costView(ctx, input, view, limit)
	if err != nil {
		return nil, nil, err
	}
	return toJSONResult(costResponseForDeadline(ctx, resp, view, input))
}

// costResponseForDeadline reports a budget that expired before the view could
// answer. A failed response assembled after the budget expired was built from
// queries cancelled mid-flight: the mandatory ones surface as query_error,
// whose remediation sends the operator to check pod health for what is really
// a slow query. Only a failed one is replaced — the usage and node queries fail
// soft, so the budget can expire with a complete answer already in hand, and
// discarding it would report computed spend as a source that never answered.
// Same rule as the rightsizing scan, which keeps a scan that finished.
func costResponseForDeadline(ctx context.Context, resp costResponse, view string, input getCostInput) costResponse {
	if ctx.Err() == nil || resp.Available {
		return resp
	}
	return costResponse{
		View: view, Namespace: input.Namespace, Range: input.Range,
		Available: false, Reason: reasonCostDeadlineExceeded,
		Remediation: costRemediation(reasonCostDeadlineExceeded),
		Currency:    opencost.ResolveCurrency(),
	}
}

func costView(ctx context.Context, input getCostInput, view string, limit int) (costResponse, error) {
	switch view {
	case "summary":
		return costSummaryView(ctx, input, limit)
	case "workloads":
		return costWorkloadsView(ctx, input, limit)
	case "nodes":
		if strings.TrimSpace(input.Namespace) != "" {
			return costResponse{}, fmt.Errorf("view=nodes is cluster-wide and takes no namespace — use view=summary with a namespace, or view=workloads, for namespace-scoped spend")
		}
		return costNodesView(ctx, input.Name, limit)
	case "trend":
		if !pkgopencost.SupportedTrendRange(input.Range) {
			return costResponse{}, fmt.Errorf("unsupported range %q — use 6h, 24h (default), or 7d; anything else would silently return 24h", input.Range)
		}
		return costTrendView(ctx, input)
	default:
		return costResponse{}, fmt.Errorf("unknown view %q — use summary (cluster totals + per-namespace), workloads (one namespace, requires namespace), nodes, or trend", input.View)
	}
}

func costSummaryView(ctx context.Context, input getCostInput, limit int) (costResponse, error) {
	base := costResponse{View: "summary", Namespace: input.Namespace}

	requested := requestedNamespaces(input.Namespace)
	allowed := scopedNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		return base.unavailable(deniedScopeReason(requested), opencost.ResolveCurrency(), ""), nil
	}

	// Selected() first on the success path: currency detection consults the
	// selected source. The denial paths above resolve it directly — an
	// explicit --opencost-currency override short-circuits detection, and the
	// undetectable cases return the default without caching it.
	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return base.unavailable(opencost.ConnectionFailureReason(err), currency, ""), nil
	}

	var summary *pkgopencost.CostSummary
	if connection.Source == opencost.SourceKubecost {
		summary, err = pkgopencost.ComputeKubecostSummary(ctx, connection.Client, pkgopencost.KubecostCurrentOptions{
			Currency: currency, ClusterID: connection.ClusterID,
		})
		if err != nil {
			log.Printf("[mcp] Kubecost summary failed: %s", k8s.SanitizeForLog(err.Error()))
			return base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"), nil
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return base.unavailable(reason, currency, "prometheus"), nil
		}
		summary = pkgopencost.ComputeCostSummaryFromProm(ctx, client, pkgopencost.SummaryOptions{Currency: currency, SkipNodeCost: allowed != nil})
		summary.Source = "prometheus"
	}

	// Reuse the REST filter rather than re-deriving it: it also recomputes the
	// totals and efficiency from the surviving rows, so a partially-authorized
	// caller never sees cluster-wide spend.
	if allowed != nil {
		opencost.FilterCostSummary(summary, allowed)
	}

	resp := base
	resp.Available = summary.Available
	resp.Reason = summary.Reason
	resp.Source = summary.Source
	resp.Currency = summary.Currency
	resp.Window = summary.Window
	resp.DataThrough = summary.DataThrough
	resp.NamespaceScope = summary.NamespaceScope
	if !summary.Available {
		// FilterCostSummary reports a filtered-to-empty result as no_metrics,
		// which otherwise renders as "install a cost source" for a source that
		// is healthy and simply has no rows in the caller's namespaces.
		if scopedEmptyResult(summary.Reason, summary.NamespaceScope) {
			resp.Remediation = costScopeEmptyRemediation
			resp.Guidance = costGuidance(summary.NamespaceScope, strings.TrimSpace(input.Namespace))
		} else {
			resp.Remediation = costRemediation(summary.Reason)
		}
		return resp, nil
	}

	resp.Totals = hourlyTotals(summary.TotalAllocatedCost)
	resp.Totals.NodeHourlyCost = &nullableCost{value: summary.TotalNodeCost}
	resp.Totals.NodeMonthlyProjection = &nullableCost{}
	if summary.TotalNodeCost != nil {
		monthly := roundMonthly(*summary.TotalNodeCost * monthlyProjectionHours)
		resp.Totals.NodeMonthlyProjection.value = &monthly
	}
	resp.Totals.UnallocatedCost = &nullableCost{value: summary.TotalUnallocatedCost}
	resp.Totals.UnusedRequestCost = measuredUnusedRequestCost(summary)
	resp.Totals.StorageCost = roundHourly(summary.TotalStorageCost)
	resp.Totals.NetworkCost = roundHourly(summary.TotalNetworkCost)
	// Cluster efficiency is derived only from rows that HAVE usage evidence, so
	// when none of them do the 0 it lands on is an absence, not a measurement.
	resp.Totals.ClusterEfficiency = &nullableCost{value: measuredValue(summary.ClusterEfficiency, allUsageUnavailable(summary.Namespaces))}
	resp.NamespaceCount = len(summary.Namespaces)
	rows, truncated := truncateRows(summary.Namespaces, limit)
	resp.Truncated = truncated
	resp.Namespaces = make([]namespaceCostRow, 0, len(rows))
	for _, row := range rows {
		resp.Namespaces = append(resp.Namespaces, namespaceCostRow{
			Name:              row.Name,
			HourlyCost:        roundHourly(row.HourlyCost),
			CPUCost:           roundHourly(row.CPUCost),
			MemoryCost:        roundHourly(row.MemoryCost),
			StorageCost:       roundHourly(row.StorageCost),
			NetworkCost:       roundHourly(row.NetworkCost),
			UnusedRequestCost: measuredValue(row.IdleCost, row.UsageUnavailable),
			Efficiency:        measuredValue(row.Efficiency, row.UsageUnavailable),
			UsageUnavailable:  row.UsageUnavailable,
		})
	}
	resp.Guidance = append(costGuidance(summary.NamespaceScope, strings.TrimSpace(input.Namespace)), costEfficiencyExplainer)
	if partial := partialUsageGuidance(summary.Namespaces); partial != "" {
		resp.Guidance = append(resp.Guidance, strings.TrimSpace(partial))
	}
	resp.Guidance = append(resp.Guidance, costSplitGuidance(summary, len(summary.NamespaceScope) > 0)...)
	return resp, nil
}

// measuredUnusedRequestCost is nil when no row has usage evidence, like
// efficiencyPercent: a 0 there would claim nothing is wasted rather than that
// nothing was measured.
func measuredUnusedRequestCost(summary *pkgopencost.CostSummary) *float64 {
	return measuredValue(summary.TotalUnusedRequestCost, allUsageUnavailable(summary.Namespaces))
}

func costSplitGuidance(summary *pkgopencost.CostSummary, scoped bool) []string {
	parts := []string{"allocatedHourlyCost covers workload allocation; nodeHourlyCost independently measures node capacity and already includes unallocatedHourlyCost. Do not add them together. Storage and GPU coverage can differ, so allocation can exceed node cost."}
	if summary.TotalNodeCost == nil {
		parts = append(parts, "nodeHourlyCost and nodeMonthlyProjection are null: node capacity cost was not measured for this scope/source.")
	}
	if summary.Source == "prometheus" {
		parts = append(parts, "On this source namespace rows and allocatedHourlyCost count CPU, memory and storage allocation only; GPU and network cost are not in them.")
	}
	parts = append(parts, costWasteExplainer)
	if summary.TotalUnallocatedCost == nil {
		if scoped {
			parts = append(parts, "unallocatedHourlyCost is null: unrequested node capacity belongs to the cluster, not to a namespace.")
		} else {
			parts = append(parts, "unallocatedHourlyCost is null: the cost source did not report unallocated node capacity, or GPU spend on the nodes keeps it from being separated — not the same as none.")
		}
	}
	return parts
}

// partialUsageGuidance fires when only SOME rows carry usage evidence. The
// aggregates are then computed over that subset and labelled as cluster
// figures, and a truncated row list can hide the rows they left out.
func partialUsageGuidance(rows []pkgopencost.NamespaceCost) string {
	missing := 0
	for _, row := range rows {
		if row.UsageUnavailable {
			missing++
		}
	}
	if missing == 0 || missing == len(rows) {
		return ""
	}
	return fmt.Sprintf(" %d of %d namespaces reported no usage evidence, so efficiencyPercent and unusedRequestHourlyCost cover only the rest — they are not whole-cluster figures, and the rows they exclude may be past the returned list.", missing, len(rows))
}

// allUsageUnavailable reports whether every row lacked usage evidence, which is
// the only case where the aggregate has nothing to average over.
func allUsageUnavailable(rows []pkgopencost.NamespaceCost) bool {
	if len(rows) == 0 {
		return true
	}
	for _, row := range rows {
		if !row.UsageUnavailable {
			return false
		}
	}
	return true
}

func costWorkloadsView(ctx context.Context, input getCostInput, limit int) (costResponse, error) {
	namespace := strings.TrimSpace(input.Namespace)
	if namespace == "" {
		return costResponse{}, fmt.Errorf("view=workloads needs a namespace — use view=summary first to find the expensive namespaces, then pass one here")
	}

	base := costResponse{View: "workloads", Namespace: namespace}

	if allowed := scopedNamespacesForUser(ctx, []string{namespace}); allowed != nil && len(allowed) == 0 {
		return base.unavailable(deniedScopeReason([]string{namespace}), opencost.ResolveCurrency(), ""), nil
	}

	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return base.unavailable(opencost.ConnectionFailureReason(err), currency, ""), nil
	}

	var workloads *pkgopencost.WorkloadCostResponse
	if connection.Source == opencost.SourceKubecost {
		workloads, err = pkgopencost.ComputeKubecostWorkloads(ctx, connection.Client, namespace, pkgopencost.KubecostCurrentOptions{
			Currency: currency, ClusterID: connection.ClusterID, Owners: opencost.BuildPodOwnerLookup(namespace),
		})
		if err != nil {
			log.Printf("[mcp] Kubecost workloads failed for namespace %q: %s", k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(err.Error()))
			return base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"), nil
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return base.unavailable(reason, currency, "prometheus"), nil
		}
		workloads = pkgopencost.ComputeWorkloadsFromProm(ctx, client, namespace, opencost.BuildPodOwnerLookup(namespace))
		workloads.Currency = currency
		workloads.Source = "prometheus"
	}

	resp := base
	resp.Available = workloads.Available
	resp.Reason = workloads.Reason
	resp.Source = workloads.Source
	resp.Currency = workloads.Currency
	resp.DataThrough = workloads.DataThrough
	// Unavailable paths carry no window; the agent still needs to state what
	// window the call was for.
	resp.Window = workloads.Window
	if resp.Window == "" {
		resp.Window = pkgopencost.DefaultCurrentWindow
	}
	if !workloads.Available {
		// Same remap summary already applies: a healthy source with no rows in
		// the caller's scope is an empty scope, not a missing installation.
		if scopedEmptyResult(workloads.Reason, []string{namespace}) {
			resp.Remediation = costScopeEmptyRemediation
		} else {
			resp.Remediation = costRemediation(workloads.Reason)
		}
		return resp, nil
	}

	// The ranking path's totals describe the namespace, so they are summed
	// before truncation narrows the rows. A kind/name selector replaces them
	// below, where the rows answer a different question than "top spenders".
	resp.Totals = hourlyTotals(sumWorkloadHourly(workloads.Workloads))
	resp.WorkloadCount = len(workloads.Workloads)

	selected := workloads.Workloads
	if input.Kind != "" {
		// Match before truncation: a workload ranked past limit is otherwise
		// unreachable, and the agent cannot tell "not found" from "not top-N".
		selected = selectWorkloadCost(workloads.Workloads, input.Kind, input.Name)
		// Totals describe what the caller asked for. Left namespace-wide, the
		// one row returned here sits beside a total covering every workload in
		// the namespace, and the agent reports that as the workload's spend.
		// The ranking path keeps its namespace totals: those rows ARE a
		// truncation of the namespace, so the total is the whole they came from.
		resp.Totals = hourlyTotals(sumWorkloadHourly(selected))
		resp.WorkloadCount = len(selected)
		if len(selected) == 0 {
			resp.Available = false
			resp.Reason = reasonWorkloadNotFound
			resp.Remediation = costWorkloadNotFoundRemediation
			resp.Totals = nil
			resp.Guidance = []string{costRateExplainer}
			return resp, nil
		}
	}

	rows, truncated := truncateRows(selected, limit)
	resp.Truncated = truncated
	resp.Workloads = make([]workloadCostRow, 0, len(rows))
	for _, row := range rows {
		unavailable := !row.CPUUsageAvailable || !row.MemoryUsageAvailable
		resp.Workloads = append(resp.Workloads, workloadCostRow{
			Name:              row.Name,
			Kind:              row.Kind,
			Replicas:          row.Replicas,
			HourlyCost:        roundHourly(row.HourlyCost),
			CPUCost:           roundHourly(row.CPUCost),
			MemoryCost:        roundHourly(row.MemoryCost),
			UnusedRequestCost: measuredValue(row.IdleCost, unavailable),
			Efficiency:        measuredValue(row.Efficiency, unavailable),
			UsageUnavailable:  unavailable,
		})
	}
	totalsExplainer := costWorkloadTotalExplainer
	if input.Kind != "" {
		// Narrowing the rows does not widen what the total sums.
		totalsExplainer = costSelectedWorkloadTotalExplainer + " " + costWorkloadTotalExplainer
	}
	resp.Guidance = []string{costRateExplainer, totalsExplainer, costEfficiencyExplainer}
	return resp, nil
}

// sumWorkloadHourly totals the rows it is given, so the namespace total and a
// selector's total are the same arithmetic over different row sets.
func sumWorkloadHourly(rows []pkgopencost.WorkloadCost) float64 {
	var total float64
	for _, row := range rows {
		total += row.HourlyCost
	}
	return total
}

// selectWorkloadCost matches on kind and name case-insensitively: an agent
// carrying a kind from another tool's output may spell it "deployment".
func selectWorkloadCost(rows []pkgopencost.WorkloadCost, kind, name string) []pkgopencost.WorkloadCost {
	var matched []pkgopencost.WorkloadCost
	for _, row := range rows {
		if strings.EqualFold(row.Kind, kind) && strings.EqualFold(row.Name, name) {
			matched = append(matched, row)
		}
	}
	return matched
}

// scopedEmptyResult separates a healthy source with nothing in the caller's
// scope from a source that is not installed. Both arrive as no_metrics, and
// only the second one has anything to remediate.
func scopedEmptyResult(reason string, scope []string) bool {
	return reason == pkgopencost.ReasonNoMetrics && len(scope) > 0
}

func costNodesView(ctx context.Context, name string, limit int) (costResponse, error) {
	base := costResponse{View: "nodes"}

	if !canReadClusterScopedKind(ctx, "Node", "", "list") {
		return base.unavailable(pkgopencost.ReasonAccessDenied, opencost.ResolveCurrency(), ""), nil
	}

	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return base.unavailable(opencost.ConnectionFailureReason(err), currency, ""), nil
	}

	var nodes *pkgopencost.NodeCostResponse
	if connection.Source == opencost.SourceKubecost {
		nodes, err = pkgopencost.ComputeKubecostNodes(ctx, connection.Client, pkgopencost.KubecostCurrentOptions{
			Currency: currency, ClusterID: connection.ClusterID,
		})
		if err != nil {
			log.Printf("[mcp] Kubecost nodes failed: %s", k8s.SanitizeForLog(err.Error()))
			return base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"), nil
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return base.unavailable(reason, currency, "prometheus"), nil
		}
		nodes = pkgopencost.ComputeNodeCosts(ctx, client)
		nodes.Currency = currency
		nodes.Source = "prometheus"
	}

	resp := base
	resp.Available = nodes.Available
	resp.Reason = nodes.Reason
	resp.Source = nodes.Source
	resp.Currency = nodes.Currency
	resp.DataThrough = nodes.DataThrough
	resp.Window = nodes.Window
	if !nodes.Available {
		resp.Remediation = costRemediation(nodes.Reason)
		return resp, nil
	}

	selected := nodes.Nodes
	if name != "" {
		selected = nil
		for _, row := range nodes.Nodes {
			if row.Name == name {
				selected = append(selected, row)
			}
		}
		if len(selected) == 0 {
			return base.unavailable("node_not_found", currency, nodes.Source), nil
		}
	}
	var total float64
	for _, row := range selected {
		total += row.HourlyCost
	}
	monthly := roundMonthly(total * monthlyProjectionHours)
	resp.Totals = &costTotals{NodeHourlyCost: &nullableCost{value: &total}, NodeMonthlyProjection: &nullableCost{value: &monthly}}
	resp.NodeCount = len(selected)

	rows, truncated := truncateRows(selected, limit)
	resp.Truncated = truncated
	resp.Nodes = make([]nodeCostRow, 0, len(rows))
	for _, row := range rows {
		resp.Nodes = append(resp.Nodes, nodeCostRow{
			Name:         row.Name,
			InstanceType: row.InstanceType,
			Region:       row.Region,
			HourlyCost:   roundHourly(row.HourlyCost),
		})
	}
	resp.NodesNotInCluster = labelNodeRows(resp.Nodes, selected, cachedNode)
	resp.Guidance = nodeCostGuidance(resp.NodesNotInCluster)
	if name != "" {
		resp.Guidance = append(resp.Guidance, "Totals cover only the selected node; omit name to get the whole fleet.")
	}
	return resp, nil
}

func nodeCostGuidance(nodesNotInCluster int) []string {
	guidance := []string{costRateExplainer, "Per-node CPU and memory components are deliberately not reported: the two cost sources define them differently, so use hourlyCost with instanceType to compare nodes.", costNodeLabelsExplainer}
	if nodesNotInCluster > 0 {
		// Each row is a node's rate while it ran, so a node replaced inside the
		// window and its replacement are both in the sum.
		guidance = append(guidance, "Totals sum every selected node the source reported over the window, including the nodesNotInCluster, so they overstate the current run rate after node churn.")
	}
	return guidance
}

const costNodeLabelsExplainer = "pool and capacityType come from node labels and are omitted when unknown; nodesNotInCluster counts nodes the cost source still reports that no longer exist, which carry neither. on-demand does not mean list price — committed-use and savings-plan discounts are not visible. Whether a pool can shrink (autoscaler limits) is not in this response."

type nodeLookup func(name string) (node *corev1.Node, known bool)

// cachedNode reads a node from the informer cache. known is false when the
// cache cannot answer, which must not be counted as a node that is gone.
func cachedNode(name string) (*corev1.Node, bool) {
	cache := k8s.GetResourceCache()
	if cache == nil {
		return nil, false
	}
	lister := cache.Nodes()
	if lister == nil {
		return nil, false
	}
	node, err := lister.Get(name)
	if err != nil {
		return nil, apierrors.IsNotFound(err)
	}
	return node, true
}

// labelNodeRows adds pool and capacity type to the returned rows and counts,
// across every row the source reported, the nodes the cluster no longer has.
func labelNodeRows(returned []nodeCostRow, all []pkgopencost.NodeCost, lookup nodeLookup) int {
	for i := range returned {
		node, _ := lookup(returned[i].Name)
		if node == nil {
			continue
		}
		if name, source, ok := capacity.NodePool(node); ok {
			returned[i].Pool = &nodePoolRef{Name: name, Source: source}
		}
		returned[i].CapacityType = capacity.NodeCapacityType(node)
	}
	missing := 0
	for _, row := range all {
		if node, known := lookup(row.Name); node == nil && known {
			missing++
		}
	}
	return missing
}

func costTrendView(ctx context.Context, input getCostInput) (costResponse, error) {
	base := costResponse{View: "trend", Namespace: input.Namespace, Range: input.Range}

	requested := requestedNamespaces(input.Namespace)
	allowed := scopedNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		return base.unavailable(deniedScopeReason(requested), opencost.ResolveCurrency(), ""), nil
	}

	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return base.unavailable(opencost.ConnectionFailureReason(err), currency, ""), nil
	}

	var trend *pkgopencost.CostTrendResponse
	if connection.Source == opencost.SourceKubecost {
		trend, err = pkgopencost.ComputeKubecostTrend(ctx, connection.Client, pkgopencost.KubecostTrendOptions{
			Range: input.Range, Namespaces: allowed, Currency: currency, ClusterID: connection.ClusterID,
		})
		if err != nil {
			log.Printf("[mcp] Kubecost trend failed: %s", k8s.SanitizeForLog(err.Error()))
			return base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"), nil
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return base.unavailable(reason, currency, "prometheus"), nil
		}
		trend = pkgopencost.ComputeCostTrendFromProm(ctx, client, pkgopencost.TrendPromOptions{
			Range: input.Range, Namespaces: allowed,
		})
		trend.Currency = currency
		trend.Source = "prometheus"
	}

	resp := base
	resp.Available = trend.Available
	resp.Reason = trend.Reason
	resp.Source = trend.Source
	resp.Currency = trend.Currency
	resp.DataThrough = trend.DataThrough
	// Keep the echoed request: the backends return an empty Range on their
	// failure paths, and dropping it leaves an agent that asked for 7d unable
	// to tell which range failed.
	if trend.Range != "" {
		resp.Range = trend.Range
	}
	resp.NamespaceScope = allowed
	resp.Guidance = costTrendGuidance(allowed, strings.TrimSpace(input.Namespace))
	if !trend.Available {
		if scopedEmptyResult(trend.Reason, allowed) {
			resp.Remediation = costScopeEmptyRemediation
		} else {
			resp.Remediation = costRemediation(trend.Reason)
		}
		return resp, nil
	}
	resp.Series, resp.TrendTotal = summarizeTrend(trend.Series, input.IncludePoints != nil && *input.IncludePoints)
	if input.IncludePoints == nil || !*input.IncludePoints {
		resp.Guidance = append(resp.Guidance, "Raw time-series points are omitted. Call get_cost again with view=trend, the same namespace/range, and include_points=true to include raw points.")
	}
	resp.NamespaceCount = trend.NamespaceCount
	for _, series := range trend.Series {
		resp.SeriesCapped = resp.SeriesCapped || series.Remainder
	}
	if resp.SeriesCapped {
		resp.Guidance = append(resp.Guidance, fmt.Sprintf(costTrendCappedFmt, trend.NamespaceCount, len(resp.Series)-1, trend.NamespaceCount-(len(resp.Series)-1)))
	}
	resp.Guidance = append(resp.Guidance, costTrendSeriesExplainer)
	if resp.TrendTotal != nil {
		resp.TrendTotal.Basis = trendBasis(connection.Source)
		resp.Guidance = append(resp.Guidance, trendBasisExplainer(connection.Source))
	}
	return resp, nil
}

const (
	trendBasisCPUMemoryAllocation = "cpu_memory_allocation"
	trendBasisNamespaceAllocation = "namespace_allocation"
)

// trendBasis names what the trend sums. The OpenCost query adds CPU and memory
// allocation only, while Kubecost's rows carry every allocated component.
func trendBasis(source opencost.Source) string {
	if source == opencost.SourceKubecost {
		return trendBasisNamespaceAllocation
	}
	return trendBasisCPUMemoryAllocation
}

func trendBasisExplainer(source opencost.Source) string {
	if trendBasis(source) == trendBasisNamespaceAllocation {
		return "trendTotal.basis is namespace_allocation: the same components as view=summary totals.allocatedHourlyCost, idle excluded. Small differences come from window alignment — the summary covers the latest window, the trend's last point its last bucket."
	}
	return "trendTotal.basis is cpu_memory_allocation: CPU and memory allocation only, excluding storage and unallocated node capacity. Compare trendTotal.endHourlyCost with view=summary totals.allocatedHourlyCost minus totals.storageHourlyCost, not with totals.nodeHourlyCost."
}

// summarizeTrend answers "is spend growing" in the response. The raw points
// are opt-in; the summaries always use all timestamps.
func summarizeTrend(series []pkgopencost.CostTrendSeries, includePoints bool) ([]costTrendSeriesDTO, *costTrendSummary) {
	if len(series) == 0 {
		return nil, nil
	}
	// Totals sum across series per timestamp, so a series that starts late
	// does not read as a cluster-wide drop.
	totalByTimestamp := map[int64]float64{}
	for _, s := range series {
		for _, point := range s.DataPoints {
			totalByTimestamp[point.Timestamp] += point.Value
		}
	}
	stamps := make([]int64, 0, len(totalByTimestamp))
	for stamp := range totalByTimestamp {
		stamps = append(stamps, stamp)
	}
	sort.Slice(stamps, func(i, j int) bool { return stamps[i] < stamps[j] })

	out := make([]costTrendSeriesDTO, 0, len(series))
	for _, s := range series {
		dto := costTrendSeriesDTO{Type: "namespace", Namespace: s.Namespace}
		if s.Remainder {
			dto.Type, dto.Namespace = "remainder", ""
		}
		if includePoints {
			dto.DataPoints = make([]costTrendPoint, 0, len(s.DataPoints))
		}
		byTimestamp := make(map[int64]float64, len(s.DataPoints))
		for _, point := range s.DataPoints {
			if includePoints {
				dto.DataPoints = append(dto.DataPoints, costTrendPoint{
					Timestamp: time.Unix(point.Timestamp, 0).UTC().Format(time.RFC3339),
					Value:     roundHourly(point.Value),
				})
			}
			byTimestamp[point.Timestamp] = point.Value
		}
		dto.costTrendExtrema = summarizeCostTrendExtrema(byTimestamp)
		// Read at the range's own endpoints, absent as zero: a namespace that
		// appeared mid-range measured from its first point reads as flat.
		if len(stamps) > 0 {
			start, end := byTimestamp[stamps[0]], byTimestamp[stamps[len(stamps)-1]]
			dto.Start = roundHourly(start)
			dto.End = roundHourly(end)
			dto.ChangePercent = changePercent(len(stamps), start, end)
		}
		out = append(out, dto)
	}

	if len(stamps) == 0 {
		return out, nil
	}
	first, last := totalByTimestamp[stamps[0]], totalByTimestamp[stamps[len(stamps)-1]]
	return out, &costTrendSummary{
		costTrendExtrema: summarizeCostTrendExtrema(totalByTimestamp),
		From:             time.Unix(stamps[0], 0).UTC().Format(time.RFC3339),
		To:               time.Unix(stamps[len(stamps)-1], 0).UTC().Format(time.RFC3339),
		Start:            roundHourly(first),
		End:              roundHourly(last),
		ChangePercent:    changePercent(len(stamps), first, last),
		Points:           len(stamps),
	}
}

func summarizeCostTrendExtrema(values map[int64]float64) *costTrendExtrema {
	if len(values) == 0 {
		return nil
	}
	minimum, peak := math.Inf(1), math.Inf(-1)
	var peakAt int64
	for stamp, value := range values {
		minimum = min(minimum, value)
		if value > peak || (value == peak && stamp < peakAt) {
			peak, peakAt = value, stamp
		}
	}
	return &costTrendExtrema{
		MinHourlyCost:  roundHourly(minimum),
		PeakHourlyCost: roundHourly(peak),
		PeakAt:         time.Unix(peakAt, 0).UTC().Format(time.RFC3339),
	}
}

// changePercent is nil rather than zero when the starting value is zero: a
// percentage change from nothing is undefined, and reporting 0 would say spend
// held flat when it actually appeared.
func changePercent(points int, start, end float64) *float64 {
	// One point is not a change: start and end are the same sample, and the
	// resulting 0 reads as "spend held flat" over an interval never observed.
	if points < 2 || start == 0 {
		return nil
	}
	pct := math.Round((end-start)/start*1000) / 10
	return &pct
}

// promCostClient returns the reason a cost query cannot run, empty when it can.
// A missing client and a failed connection are different failures — collapsing
// them would tell a user with an auth error to go install OpenCost.
//
// PromForMCP, not Prom: the shared Prom() client carries a 10s socket backstop
// sized for REST callers, which a 7d trend query on a large cluster outruns.
func promCostClient(ctx context.Context) (*prom.Client, string) {
	client := prometheuspkg.GetClient()
	if client == nil {
		return nil, pkgopencost.ReasonNoPrometheus
	}
	if _, _, err := client.EnsureConnected(ctx); err != nil {
		log.Printf("[mcp] Prometheus EnsureConnected failed for cost query: %v", err)
		return nil, opencost.ConnectionFailureReason(err)
	}
	promClient := client.PromForMCP()
	if promClient == nil {
		return nil, pkgopencost.ReasonNoPrometheus
	}
	return promClient, ""
}

func requestedNamespaces(namespace string) []string {
	if strings.TrimSpace(namespace) == "" {
		return nil
	}
	return []string{strings.TrimSpace(namespace)}
}

// unavailable stamps a failure onto the response identity the view already
// built, so the scope fields (namespace, range) travel with every failure path
// instead of being re-stamped at each one — the omission that reads as a
// cluster-wide answer.
func (r costResponse) unavailable(reason, currency, source string) costResponse {
	r.Available = false
	r.Reason = reason
	r.Remediation = costRemediation(reason)
	r.Currency = currency
	if source != "" {
		r.Source = source
	}
	return r
}

// hourlyTotals keeps the monthly projection on one multiply: the constant must
// not disagree with the Costs UI, and four independent call sites drift.
func hourlyTotals(hourly float64) *costTotals {
	monthly := roundMonthly(hourly * monthlyProjectionHours)
	return &costTotals{AllocatedHourlyCost: measuredValue(hourly, false), AllocatedMonthlyProjection: &monthly}
}

// measuredValue separates "used nothing" from "we could not measure".
// Both are zero on the wire otherwise, and only the second is missing data.
func measuredValue(value float64, unavailable bool) *float64 {
	if unavailable {
		return nil
	}
	rounded := roundHourly(value)
	return &rounded
}

func truncateRows[T any](rows []T, limit int) ([]T, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

func costRemediation(reason string) string {
	switch reason {
	case ReasonOutsideNamespaceScope:
		// The pinned namespace is not named: this caller was denied, and may have
		// no access to it either.
		return "radar is pinned to a single namespace with --namespace-scope, so it cannot report on the requested scope. This is a startup flag, not a permissions problem — restart radar without --namespace-scope for cluster-wide cost."
	case "node_not_found":
		return "Check the node name, or call view=nodes without name to list nodes reported by the cost source."
	case reasonWorkloadNotFound:
		return costWorkloadNotFoundRemediation
	case reasonCostDeadlineExceeded:
		return fmt.Sprintf("The cost request exceeded its %s budget before answering. The budget also covers permission discovery and source selection, so this does not establish that the cost source was reached — retry, and for view=trend use a shorter range. A namespace does not shorten the query: summary and trend read every namespace and filter afterwards.", costCallBudget)
	case pkgopencost.ReasonNoPrometheus:
		return "No Prometheus found. Radar auto-discovers it, or start radar with --prometheus-url. Cost data additionally needs OpenCost or Kubecost installed."
	case pkgopencost.ReasonNoCostSource:
		return "Neither OpenCost metrics nor Kubecost was found. Install one of them in the cluster; OpenCost also needs a Prometheus for Radar to read it through."
	case pkgopencost.ReasonNoMetrics:
		// Kubecost answers with this reason too, so the text cannot name
		// Prometheus as the thing that is reachable. The source field says
		// which one answered.
		return "The cost source answered but reported no cost data. If OpenCost or Kubecost was just installed, let it scrape for a few minutes; if neither is installed, install one — the source field says which one Radar asked."
	case pkgopencost.ReasonAccessDenied:
		return "This identity cannot read the requested scope. Costs are reported only for namespaces (or nodes) its RBAC allows."
	case pkgopencost.ReasonAuthentication:
		return "The cost source rejected Radar's credentials — check the Kubecost API key in Radar's settings, or the Prometheus credentials if cost comes from OpenCost. The source field says which one answered."
	case pkgopencost.ReasonConfigMismatch:
		return "The configured cost source does not match the connected cluster. Check Radar's cost settings against the current kubeconfig context."
	case pkgopencost.ReasonDeploymentConfig:
		return "The cost source is installed but misconfigured. Check the OpenCost/Kubecost deployment's own configuration."
	case pkgopencost.ReasonInsufficientHistory, pkgopencost.ReasonHistoryUnsupported:
		return "The cost source has not retained enough history for this range. Try a shorter range, or wait for it to accumulate."
	case pkgopencost.ReasonNotFound:
		return "The requested resource no longer exists."
	case pkgopencost.ReasonQueryError, pkgopencost.ReasonSourceUnavailable:
		return "The cost source is installed but the query failed. Check that OpenCost/Kubecost pods are healthy."
	default:
		return ""
	}
}

// costGuidance distinguishes a scope the caller asked for from one RBAC imposed
// — reporting an explicit namespace filter as a permission limit would have an
// agent tell the user their access is restricted when they simply asked for one
// namespace.
func costGuidance(scope []string, requestedNamespace string) []string {
	return withScopeGuidance(costRateExplainer, scope, requestedNamespace)
}

// costTrendGuidance leaves out the monthly projection: trend carries no totals.
func costTrendGuidance(scope []string, requestedNamespace string) []string {
	return withScopeGuidance(costHourlyExplainer, scope, requestedNamespace)
}

func withScopeGuidance(rate string, scope []string, requestedNamespace string) []string {
	if len(scope) == 0 {
		return []string{rate}
	}
	if requestedNamespace != "" {
		return []string{rate, fmt.Sprintf(costRequestedViewFmt, requestedNamespace)}
	}
	// The pin is checked before RBAC: both narrow the scope identically, and
	// only the pin is knowable from configuration rather than from the answer.
	if pinned, ok := NamespacePinned(); ok {
		return []string{rate, fmt.Sprintf(costPinnedViewFmt, pinned)}
	}
	return []string{rate, fmt.Sprintf(costPartialViewFmt, len(scope))}
}
