package mcp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/opencost"
	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
	"github.com/skyhook-io/radar/pkg/prom"
)

// The Costs UI projects monthly spend as the hourly rate times this constant;
// the tool emits the projection server-side so an agent reporting a monthly
// figure to a user cannot disagree with the screen next to them.
const monthlyProjectionHours = 730

const (
	costDefaultLimit  = 20
	costMaxLimit      = 100
	costRateExplainer = "Costs are hourly rates. projectedMonthlyCost = hourlyCost x 730."
	// The denominator is allocation — max(requested, observed) — not the
	// request, so the ratio caps at 100 and a low value is not by itself a
	// defect. Neither fact is derivable from the numbers in the response.
	costEfficiencyExplainer = "efficiency compares observed use against cost allocation, which is the greater of requested and observed usage — not against the request. It therefore caps at 100, and a low value is not by itself a defect or recoverable money (bursty workloads, P95-sized requests and HPA headroom all read low). Use get_rightsizing to judge whether a request should change; it applies OOM and HPA gating a ratio cannot."
	// Enumerating what the workload total leaves out would go stale: the
	// namespace row's basis is the cost source's own total, which carries
	// storage, network, shared and external cost on Kubecost.
	costWorkloadTotalExplainer = "totals here sum CPU and memory allocation only, so they do not reconcile with this namespace's row in view=summary, whose basis is whatever the cost source totals."
	costScopeEmptyRemediation  = "The cost source is reachable but reported no allocation for the namespaces in namespaceScope. Nothing needs installing — either no workloads are running in that scope, or the scope is narrower than expected."
	costRequestedViewFmt       = "Totals cover only namespace %s, not the whole cluster."
	costPartialViewFmt         = "Totals cover only the %d namespace(s) this identity can read, not the whole cluster."
)

type getCostInput struct {
	View      string `json:"view,omitempty" jsonschema:"summary (default) for cluster totals plus per-namespace spend, workloads for one namespace broken down by workload (requires namespace), nodes for per-node spend, trend for spend over time"`
	Namespace string `json:"namespace,omitempty" jsonschema:"required for view=workloads; filters summary and trend to one namespace"`
	Range     string `json:"range,omitempty" jsonschema:"trend only: 6h, 24h (default), or 7d"`
	Limit     int    `json:"limit,omitempty" jsonschema:"max rows for summary/workloads/nodes (default 20, max 100)"`
}

type costTotals struct {
	HourlyCost           float64 `json:"hourlyCost"`
	ProjectedMonthlyCost float64 `json:"projectedMonthlyCost"`
	StorageCost          float64 `json:"storageCost,omitempty"`
	NetworkCost          float64 `json:"networkCost,omitempty"`
	IdleCost             float64 `json:"idleCost,omitempty"`
	ClusterEfficiency    float64 `json:"clusterEfficiency,omitempty"`
}

type namespaceCostRow struct {
	Name             string  `json:"name"`
	Kind             string  `json:"kind,omitempty"`
	Namespace        string  `json:"namespace,omitempty"`
	HourlyCost       float64 `json:"hourlyCost"`
	CPUCost          float64 `json:"cpuCost"`
	MemoryCost       float64 `json:"memoryCost"`
	StorageCost      float64 `json:"storageCost,omitempty"`
	NetworkCost      float64 `json:"networkCost,omitempty"`
	IdleCost         float64 `json:"idleCost,omitempty"`
	Efficiency       float64 `json:"efficiency,omitempty"`
	UsageUnavailable bool    `json:"usageUnavailable,omitempty"`
}

type workloadCostRow struct {
	Name             string  `json:"name"`
	Kind             string  `json:"kind"`
	Replicas         int     `json:"replicas"`
	HourlyCost       float64 `json:"hourlyCost"`
	CPUCost          float64 `json:"cpuCost"`
	MemoryCost       float64 `json:"memoryCost"`
	IdleCost         float64 `json:"idleCost,omitempty"`
	Efficiency       float64 `json:"efficiency,omitempty"`
	UsageUnavailable bool    `json:"usageUnavailable,omitempty"`
}

type nodeCostRow struct {
	Name         string  `json:"name"`
	InstanceType string  `json:"instanceType,omitempty"`
	Region       string  `json:"region,omitempty"`
	HourlyCost   float64 `json:"hourlyCost"`
	CPUCost      float64 `json:"cpuCost"`
	MemoryCost   float64 `json:"memoryCost"`
}

type costResponse struct {
	View           string                        `json:"view"`
	Available      bool                          `json:"available"`
	Reason         string                        `json:"reason,omitempty"`
	Remediation    string                        `json:"remediation,omitempty"`
	Source         string                        `json:"source,omitempty"`
	Currency       string                        `json:"currency"`
	Window         string                        `json:"window,omitempty"`
	Range          string                        `json:"range,omitempty"`
	DataThrough    string                        `json:"dataThrough,omitempty"`
	Namespace      string                        `json:"namespace,omitempty"`
	NamespaceScope []string                      `json:"namespaceScope,omitempty"`
	Totals         *costTotals                   `json:"totals,omitempty"`
	Namespaces     []namespaceCostRow            `json:"namespaces,omitempty"`
	Workloads      []workloadCostRow             `json:"workloads,omitempty"`
	Nodes          []nodeCostRow                 `json:"nodes,omitempty"`
	Series         []pkgopencost.CostTrendSeries `json:"series,omitempty"`
	Truncated      bool                          `json:"truncated,omitempty"`
	Guidance       string                        `json:"guidance,omitempty"`
}

func handleGetCost(ctx context.Context, _ *mcp.CallToolRequest, input getCostInput) (*mcp.CallToolResult, any, error) {
	view := strings.ToLower(strings.TrimSpace(input.View))
	if view == "" {
		view = "summary"
	}
	// Normalize before validating: validating a trimmed value and dispatching
	// the raw one lets " 7d " pass the check and then resolve as 24h.
	input.Range = strings.TrimSpace(input.Range)

	limit := input.Limit
	if limit <= 0 {
		limit = costDefaultLimit
	}
	if limit > costMaxLimit {
		limit = costMaxLimit
	}

	// Silently dropping a parameter the caller set lets an agent believe a
	// filter applied. Reject the combination instead.
	if view != "trend" && input.Range != "" {
		return nil, nil, fmt.Errorf("range applies only to view=trend, not view=%s", view)
	}
	if view == "trend" && input.Limit > 0 {
		return nil, nil, errors.New("limit applies to the row views (summary, workloads, nodes), not view=trend")
	}

	switch view {
	case "summary":
		return costSummaryView(ctx, input, limit)
	case "workloads":
		return costWorkloadsView(ctx, input, limit)
	case "nodes":
		if strings.TrimSpace(input.Namespace) != "" {
			return nil, nil, fmt.Errorf("view=nodes is cluster-wide and takes no namespace — use view=summary with a namespace, or view=workloads, for namespace-scoped spend")
		}
		return costNodesView(ctx, limit)
	case "trend":
		if !pkgopencost.SupportedTrendRange(input.Range) {
			return nil, nil, fmt.Errorf("unsupported range %q — use 6h, 24h (default), or 7d; anything else would silently return 24h", input.Range)
		}
		return costTrendView(ctx, input)
	default:
		return nil, nil, fmt.Errorf("unknown view %q — use summary (cluster totals + per-namespace), workloads (one namespace, requires namespace), nodes, or trend", input.View)
	}
}

func costSummaryView(ctx context.Context, input getCostInput, limit int) (*mcp.CallToolResult, any, error) {
	base := costResponse{View: "summary", Namespace: input.Namespace}

	requested := requestedNamespaces(input.Namespace)
	allowed := scopedNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		return toJSONResult(base.unavailable(pkgopencost.ReasonAccessDenied, pkgopencost.DefaultCurrency, ""))
	}

	// Selected() first: currency detection consults the selected source, so
	// resolving currency before the source is picked caches the default.
	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, ""))
	}

	var summary *pkgopencost.CostSummary
	if connection.Source == opencost.SourceKubecost {
		summary, err = pkgopencost.ComputeKubecostSummary(ctx, connection.Client, pkgopencost.KubecostCurrentOptions{
			Currency: currency, ClusterID: connection.ClusterID,
		})
		if err != nil {
			log.Printf("[mcp] Kubecost summary failed: %s", k8s.SanitizeForLog(err.Error()))
			return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"))
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return toJSONResult(base.unavailable(reason, currency, "prometheus"))
		}
		summary = pkgopencost.ComputeCostSummaryFromProm(ctx, client, pkgopencost.SummaryOptions{Currency: currency})
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
		if summary.Reason == pkgopencost.ReasonNoMetrics && len(summary.NamespaceScope) > 0 {
			resp.Remediation = costScopeEmptyRemediation
			resp.Guidance = costGuidance(summary.NamespaceScope, strings.TrimSpace(input.Namespace))
		} else {
			resp.Remediation = costRemediation(summary.Reason)
		}
		return toJSONResult(resp)
	}

	resp.Totals = hourlyTotals(summary.TotalHourlyCost)
	resp.Totals.StorageCost = summary.TotalStorageCost
	resp.Totals.NetworkCost = summary.TotalNetworkCost
	resp.Totals.IdleCost = summary.TotalIdleCost
	resp.Totals.ClusterEfficiency = summary.ClusterEfficiency
	rows, truncated := truncateRows(summary.Namespaces, limit)
	resp.Truncated = truncated
	resp.Namespaces = make([]namespaceCostRow, 0, len(rows))
	for _, row := range rows {
		resp.Namespaces = append(resp.Namespaces, namespaceCostRow{
			Name:             row.Name,
			Kind:             row.Kind,
			Namespace:        row.Namespace,
			HourlyCost:       row.HourlyCost,
			CPUCost:          row.CPUCost,
			MemoryCost:       row.MemoryCost,
			StorageCost:      row.StorageCost,
			NetworkCost:      row.NetworkCost,
			IdleCost:         row.IdleCost,
			Efficiency:       row.Efficiency,
			UsageUnavailable: row.UsageUnavailable,
		})
	}
	resp.Guidance = costGuidance(summary.NamespaceScope, strings.TrimSpace(input.Namespace)) + " " + costEfficiencyExplainer
	return toJSONResult(resp)
}

func costWorkloadsView(ctx context.Context, input getCostInput, limit int) (*mcp.CallToolResult, any, error) {
	namespace := strings.TrimSpace(input.Namespace)
	if namespace == "" {
		return nil, nil, fmt.Errorf("view=workloads needs a namespace — use view=summary first to find the expensive namespaces, then pass one here")
	}

	base := costResponse{View: "workloads", Namespace: namespace}

	if allowed := scopedNamespacesForUser(ctx, []string{namespace}); allowed != nil && len(allowed) == 0 {
		return toJSONResult(base.unavailable(pkgopencost.ReasonAccessDenied, pkgopencost.DefaultCurrency, ""))
	}

	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, ""))
	}

	var workloads *pkgopencost.WorkloadCostResponse
	if connection.Source == opencost.SourceKubecost {
		workloads, err = pkgopencost.ComputeKubecostWorkloads(ctx, connection.Client, namespace, pkgopencost.KubecostCurrentOptions{
			Currency: currency, ClusterID: connection.ClusterID, Owners: opencost.BuildPodOwnerLookup(namespace),
		})
		if err != nil {
			log.Printf("[mcp] Kubecost workloads failed for namespace %q: %s", k8s.SanitizeForLog(namespace), k8s.SanitizeForLog(err.Error()))
			return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"))
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return toJSONResult(base.unavailable(reason, currency, "prometheus"))
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
	resp.Window = workloads.Window
	resp.DataThrough = workloads.DataThrough
	if !workloads.Available {
		resp.Remediation = costRemediation(workloads.Reason)
		return toJSONResult(resp)
	}

	var total float64
	for _, row := range workloads.Workloads {
		total += row.HourlyCost
	}
	resp.Totals = hourlyTotals(total)

	rows, truncated := truncateRows(workloads.Workloads, limit)
	resp.Truncated = truncated
	resp.Workloads = make([]workloadCostRow, 0, len(rows))
	for _, row := range rows {
		resp.Workloads = append(resp.Workloads, workloadCostRow{
			Name:             row.Name,
			Kind:             row.Kind,
			Replicas:         row.Replicas,
			HourlyCost:       row.HourlyCost,
			CPUCost:          row.CPUCost,
			MemoryCost:       row.MemoryCost,
			IdleCost:         row.IdleCost,
			Efficiency:       row.Efficiency,
			UsageUnavailable: !row.CPUUsageAvailable || !row.MemoryUsageAvailable,
		})
	}
	resp.Guidance = costRateExplainer + " " + costWorkloadTotalExplainer + " " + costEfficiencyExplainer
	return toJSONResult(resp)
}

func costNodesView(ctx context.Context, limit int) (*mcp.CallToolResult, any, error) {
	base := costResponse{View: "nodes"}

	if !canReadClusterScopedKind(ctx, "Node", "", "list") {
		return toJSONResult(base.unavailable(pkgopencost.ReasonAccessDenied, pkgopencost.DefaultCurrency, ""))
	}

	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, ""))
	}

	var nodes *pkgopencost.NodeCostResponse
	if connection.Source == opencost.SourceKubecost {
		nodes, err = pkgopencost.ComputeKubecostNodes(ctx, connection.Client, pkgopencost.KubecostCurrentOptions{
			Currency: currency, ClusterID: connection.ClusterID,
		})
		if err != nil {
			log.Printf("[mcp] Kubecost nodes failed: %s", k8s.SanitizeForLog(err.Error()))
			return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"))
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return toJSONResult(base.unavailable(reason, currency, "prometheus"))
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
	if !nodes.Available {
		resp.Remediation = costRemediation(nodes.Reason)
		return toJSONResult(resp)
	}

	var total float64
	for _, row := range nodes.Nodes {
		total += row.HourlyCost
	}
	resp.Totals = hourlyTotals(total)

	rows, truncated := truncateRows(nodes.Nodes, limit)
	resp.Truncated = truncated
	resp.Nodes = make([]nodeCostRow, 0, len(rows))
	for _, row := range rows {
		resp.Nodes = append(resp.Nodes, nodeCostRow{
			Name:         row.Name,
			InstanceType: row.InstanceType,
			Region:       row.Region,
			HourlyCost:   row.HourlyCost,
			CPUCost:      row.CPUCost,
			MemoryCost:   row.MemoryCost,
		})
	}
	resp.Guidance = costRateExplainer
	return toJSONResult(resp)
}

func costTrendView(ctx context.Context, input getCostInput) (*mcp.CallToolResult, any, error) {
	base := costResponse{View: "trend", Namespace: input.Namespace, Range: input.Range}

	requested := requestedNamespaces(input.Namespace)
	allowed := scopedNamespacesForUser(ctx, requested)
	if allowed != nil && len(allowed) == 0 {
		return toJSONResult(base.unavailable(pkgopencost.ReasonAccessDenied, pkgopencost.DefaultCurrency, ""))
	}

	connection, err := opencost.Selected(ctx)
	currency := opencost.ResolveCurrency()
	if err != nil {
		return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, ""))
	}

	var trend *pkgopencost.CostTrendResponse
	if connection.Source == opencost.SourceKubecost {
		trend, err = pkgopencost.ComputeKubecostTrend(ctx, connection.Client, pkgopencost.KubecostTrendOptions{
			Range: input.Range, Namespaces: allowed, Currency: currency, ClusterID: connection.ClusterID,
		})
		if err != nil {
			log.Printf("[mcp] Kubecost trend failed: %s", k8s.SanitizeForLog(err.Error()))
			return toJSONResult(base.unavailable(opencost.ConnectionFailureReason(err), currency, "kubecost"))
		}
	} else {
		client, reason := promCostClient(ctx)
		if reason != "" {
			return toJSONResult(base.unavailable(reason, currency, "prometheus"))
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
	resp.Range = trend.Range
	resp.DataThrough = trend.DataThrough
	if allowed != nil {
		resp.NamespaceScope = allowed
	}
	resp.Guidance = costGuidance(allowed, strings.TrimSpace(input.Namespace))
	if !trend.Available {
		resp.Remediation = costRemediation(trend.Reason)
		return toJSONResult(resp)
	}
	resp.Series = trend.Series
	return toJSONResult(resp)
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

func costUnavailable(view, reason, currency string) costResponse {
	return costResponse{View: view}.unavailable(reason, currency, "")
}

// hourlyTotals keeps the monthly projection on one multiply: the constant must
// not disagree with the Costs UI, and four independent call sites drift.
func hourlyTotals(hourly float64) *costTotals {
	return &costTotals{HourlyCost: hourly, ProjectedMonthlyCost: hourly * monthlyProjectionHours}
}

func truncateRows[T any](rows []T, limit int) ([]T, bool) {
	if len(rows) > limit {
		return rows[:limit], true
	}
	return rows, false
}

func costRemediation(reason string) string {
	switch reason {
	case pkgopencost.ReasonNoPrometheus:
		return "No Prometheus found. Radar auto-discovers it, or start radar with --prometheus-url. Cost data additionally needs OpenCost or Kubecost installed."
	case pkgopencost.ReasonNoCostSource:
		return "Neither OpenCost metrics nor Kubecost was found. Install one of them in the cluster; OpenCost also needs a Prometheus for Radar to read it through."
	case pkgopencost.ReasonNoMetrics:
		return "Prometheus is reachable but no cost metrics are present. Install OpenCost (or Kubecost) in the cluster and let it scrape for a few minutes."
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
func costGuidance(scope []string, requestedNamespace string) string {
	if len(scope) == 0 {
		return costRateExplainer
	}
	if requestedNamespace != "" {
		return costRateExplainer + " " + fmt.Sprintf(costRequestedViewFmt, requestedNamespace)
	}
	return costRateExplainer + " " + fmt.Sprintf(costPartialViewFmt, len(scope))
}
