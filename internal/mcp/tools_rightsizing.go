package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

const (
	rightsizingDefaultLimit = 20
	rightsizingMaxLimit     = 100
	rightsizingScanBudget   = 45 * time.Second
)

type getRightsizingInput struct {
	Scope           string `json:"scope" jsonschema:"required. workload for one workload (needs kind, name, namespace) - cheap and precise. namespace scans one namespace (needs namespace). cluster scans every Deployment/StatefulSet/DaemonSet and runs 7-day range queries, taking up to 45s - call it once to find candidates, then drill in with scope=workload"`
	Kind            string `json:"kind,omitempty" jsonschema:"for scope=workload: Deployment, StatefulSet, or DaemonSet"`
	Name            string `json:"name,omitempty" jsonschema:"for scope=workload: the workload name"`
	Namespace       string `json:"namespace,omitempty" jsonschema:"required for scope=workload and scope=namespace; rejected for scope=cluster"`
	IncludeBalanced bool   `json:"include_balanced,omitempty" jsonschema:"also return correctly-sized and unevidenced containers (default false, which returns only oversized, under_requested, and missing_request rows and reports the rest as omitted counts)"`
	Limit           int    `json:"limit,omitempty" jsonschema:"max workloads returned, ranked by largest request change (default 20, max 100)"`
}

type rightsizingRowDTO struct {
	Container            string                              `json:"container"`
	Resource             string                              `json:"resource"`
	Fit                  prometheuspkg.RightsizingFit        `json:"fit"`
	Confidence           prometheuspkg.RightsizingConfidence `json:"confidence"`
	CurrentRequest       *string                             `json:"currentRequest,omitempty"`
	CurrentLimit         *string                             `json:"currentLimit,omitempty"`
	RecommendedRequest   *string                             `json:"recommendedRequest,omitempty"`
	Observed             string                              `json:"observed,omitempty"`
	Peak                 string                              `json:"peak,omitempty"`
	Coverage             float64                             `json:"coverage"`
	RecommendationReason string                              `json:"recommendationReason,omitempty"`
	ReductionLimited     bool                                `json:"reductionLimited,omitempty"`
	Bursty               bool                                `json:"bursty,omitempty"`
	HPAManaged           bool                                `json:"hpaManaged,omitempty"`
	HPAEvidenceAvailable bool                                `json:"hpaEvidenceAvailable"`
	CurrentPodOOM        bool                                `json:"currentPodOOM,omitempty"`
	WindowOOMEvidence    bool                                `json:"windowOomEvidence,omitempty"`
	OOMEvidenceAvailable bool                                `json:"oomEvidenceAvailable"`
	ThrottleRatio        *string                             `json:"throttleRatio,omitempty"`
	LimitConflict        bool                                `json:"limitConflict,omitempty"`
	QueryError           string                              `json:"queryError,omitempty"`
}

type rightsizingWorkloadDTO struct {
	Kind         string              `json:"kind"`
	Namespace    string              `json:"namespace"`
	Name         string              `json:"name"`
	Replicas     int                 `json:"replicas,omitempty"`
	ScaledToZero bool                `json:"scaledToZero,omitempty"`
	Rows         []rightsizingRowDTO `json:"rows"`

	// Ranking key for the scan scopes; unexported, so never serialized.
	requestDelta float64
}

// rightsizingOmissions counts rows withheld from the response. Categories are
// exclusive: a query-error row also carries fit=insufficient_history, and
// counting it twice would overstate how much history is missing.
type rightsizingOmissions struct {
	Balanced            int `json:"balanced,omitempty"`
	InsufficientHistory int `json:"insufficientHistory,omitempty"`
	QueryError          int `json:"queryError,omitempty"`
}

func (o rightsizingOmissions) total() int {
	return o.Balanced + o.InsufficientHistory + o.QueryError
}

func (o *rightsizingOmissions) add(other rightsizingOmissions) {
	o.Balanced += other.Balanced
	o.InsufficientHistory += other.InsufficientHistory
	o.QueryError += other.QueryError
}

func rowHasIncompleteEvidence(row prometheuspkg.RightsizingRow) bool {
	// A withheld recommendation is not a clean bill of health: the reason is
	// set only when absent evidence actually suppressed one.
	return row.QueryError != "" ||
		row.Fit == prometheuspkg.FitInsufficientHistory ||
		prometheuspkg.IsWithheldRecommendationReason(row.RecommendationReason)
}

type rightsizingResponse struct {
	Scope           string                                 `json:"scope"`
	State           prometheuspkg.RightsizingScanState     `json:"state"`
	Window          string                                 `json:"window"`
	Source          string                                 `json:"source,omitempty"`
	ScannedAt       string                                 `json:"scannedAt,omitempty"`
	Namespace       string                                 `json:"namespace,omitempty"`
	NamespaceScope  []string                               `json:"namespaceScope,omitempty"`
	SampleAvailable *bool                                  `json:"sampleAvailable,omitempty"`
	OwnerCoverage   prometheuspkg.OwnerCoverage            `json:"ownerCoverage,omitempty"`
	Coverage        *prometheuspkg.RightsizingScanCoverage `json:"coverage,omitempty"`
	Omitted         *rightsizingOmissions                  `json:"omitted,omitempty"`
	Workloads       []rightsizingWorkloadDTO               `json:"workloads"`
	Warnings        []prometheuspkg.RightsizingScanWarning `json:"warnings,omitempty"`
	Reason          string                                 `json:"reason,omitempty"`
	Remediation     string                                 `json:"remediation,omitempty"`
	Truncated       bool                                   `json:"truncated,omitempty"`
	Guidance        string                                 `json:"guidance,omitempty"`
}

func handleGetRightsizing(ctx context.Context, _ *mcp.CallToolRequest, input getRightsizingInput) (*mcp.CallToolResult, any, error) {
	scope := strings.ToLower(strings.TrimSpace(input.Scope))

	switch scope {
	case "workload":
		return rightsizingWorkloadScope(ctx, input)
	case "namespace", "cluster":
		return rightsizingScanScope(ctx, input, scope)
	default:
		return nil, nil, errRightsizingScope(input.Scope)
	}
}

func errRightsizingScope(given string) error {
	if strings.TrimSpace(given) == "" {
		return errors.New("scope is required: " + rightsizingScopeHelp)
	}
	return fmt.Errorf("unknown scope %q: %s", given, rightsizingScopeHelp)
}

const rightsizingScopeHelp = `use scope="workload" with kind+name+namespace for one workload (cheap), ` +
	`scope="namespace" with namespace to scan one namespace, or scope="cluster" to scan every ` +
	`Deployment/StatefulSet/DaemonSet (7-day range queries, up to 45s — call it once, then drill in with scope="workload")`

func errUnsupportedRightsizingKind(kind string) error {
	return fmt.Errorf("rightsizing supports only Deployment, StatefulSet, and DaemonSet, not %q — recommendations are per container template, so Pods are the wrong granularity", kind)
}

func rightsizingWorkloadScope(ctx context.Context, input getRightsizingInput) (*mcp.CallToolResult, any, error) {
	kind := strings.TrimSpace(input.Kind)
	namespace := strings.TrimSpace(input.Namespace)
	name := strings.TrimSpace(input.Name)
	if kind == "" || namespace == "" || name == "" {
		return nil, nil, errors.New(`scope="workload" needs kind, namespace, and name — omit them and use scope="namespace" or scope="cluster" to find candidates first`)
	}

	if !prometheuspkg.IsRightsizingKind(kind) {
		return nil, nil, errUnsupportedRightsizingKind(kind)
	}

	if !namespaceWithinPin(namespace) {
		return toJSONResult(rightsizingUnavailable("workload", "access_denied"))
	}

	// The informer cache reads under Radar's ServiceAccount, so without this
	// gate any caller could fetch any namespace's container spec and P95 by
	// guessing names. "get" matches a normal resource-detail read.
	if !canReadInNamespace(ctx, "apps", prometheuspkg.ScanKindResource(kind), namespace, "get") {
		return nil, nil, fmt.Errorf("forbidden: no access to %s %s/%s", kind, namespace, name)
	}

	resp, err := prometheuspkg.RightsizingForWorkload(ctx, kind, namespace, name)
	if err != nil {
		switch {
		case errors.Is(err, prometheuspkg.ErrPrometheusUnavailable):
			return toJSONResult(rightsizingUnavailable("workload", "prometheus_unavailable"))
		case errors.Is(err, prometheuspkg.ErrRightsizingKindUnsupported):
			return nil, nil, errUnsupportedRightsizingKind(kind)
		default:
			return nil, nil, err
		}
	}

	filtered := filterRightsizingRows(resp.Rows, input.IncludeBalanced)
	sampleAvailable := resp.SampleAvailable
	out := rightsizingResponse{
		Scope:           "workload",
		State:           prometheuspkg.RightsizingScanComplete,
		Window:          resp.Window,
		Source:          resp.Source,
		Namespace:       resp.Namespace,
		SampleAvailable: &sampleAvailable,
		OwnerCoverage:   resp.OwnerCoverage,
		Reason:          resp.Reason,
		Workloads: []rightsizingWorkloadDTO{{
			Kind:         resp.Kind,
			Namespace:    resp.Namespace,
			Name:         resp.Name,
			ScaledToZero: resp.ScaledToZero,
			Rows:         filtered.rows,
		}},
	}
	switch {
	case !sampleAvailable:
		out.State = prometheuspkg.RightsizingScanUnavailable
	case filtered.incompleteEvidence:
		out.State = prometheuspkg.RightsizingScanPartial
	}
	if filtered.omitted.total() > 0 {
		out.Omitted = &filtered.omitted
	}
	out.Guidance = rightsizingGuidance(out.State, input.IncludeBalanced, "workload", nil)
	return toJSONResult(out)
}

func rightsizingScanScope(ctx context.Context, input getRightsizingInput, scope string) (*mcp.CallToolResult, any, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = rightsizingDefaultLimit
	}
	if limit > rightsizingMaxLimit {
		limit = rightsizingMaxLimit
	}

	namespace := strings.TrimSpace(input.Namespace)
	switch scope {
	case "namespace":
		if namespace == "" {
			return nil, nil, errors.New(`scope="namespace" needs a namespace — use scope="cluster" to scan the whole cluster`)
		}
		if strings.TrimSpace(input.Kind) != "" || strings.TrimSpace(input.Name) != "" {
			return nil, nil, errors.New(`scope="namespace" takes no kind or name — use scope="workload" to target one workload, which is far cheaper than scanning the namespace`)
		}
	case "cluster":
		if namespace != "" || strings.TrimSpace(input.Kind) != "" || strings.TrimSpace(input.Name) != "" {
			return nil, nil, errors.New(`scope="cluster" takes no namespace, kind, or name — use scope="namespace" or scope="workload" to narrow`)
		}
	}

	// The budget covers authorization too: the SAR fanout below is unbounded in
	// namespace count and MCP has no outer deadline the way the REST route sits
	// behind the server's request timeout.
	scanCtx, cancel := context.WithTimeout(ctx, rightsizingScanBudget)
	defer cancel()

	allowed := scopedNamespacesForUser(scanCtx, requestedNamespaces(namespace))
	if scanCtx.Err() != nil {
		return toJSONResult(rightsizingScanUnavailable(scope, namespace, "scan_deadline_exceeded"))
	}
	if allowed != nil && len(allowed) == 0 {
		return toJSONResult(rightsizingScanUnavailable(scope, namespace, "access_denied"))
	}

	scanScope := prometheuspkg.ResolveScanScope(allowed, mcpScanAuthorizer{ctx: scanCtx})

	if scanCtx.Err() != nil {
		return toJSONResult(rightsizingScanUnavailable(scope, namespace, "scan_deadline_exceeded"))
	}
	scan := prometheuspkg.ScanRightsizing(scanCtx, scanScope)
	coverage := scan.Coverage

	out := rightsizingResponse{
		Scope:     scope,
		State:     scan.State,
		Window:    scan.Window,
		Source:    scan.Source,
		Namespace: namespace,
		Reason:    scan.Reason,
		// Counters are always emitted: "0 of 1 batches completed" is precisely
		// the case a reader needs, and omitempty would hide it.
		Coverage: &coverage,
	}
	// scope="cluster" resolves to whatever this identity can list, or to the
	// server's --namespace pin. Without naming that set the caller cannot tell
	// a two-namespace scan from a cluster-wide one.
	if scope == "cluster" && allowed != nil {
		out.NamespaceScope = append([]string(nil), allowed...)
		sort.Strings(out.NamespaceScope)
	}
	if !scan.ScannedAt.IsZero() {
		out.ScannedAt = scan.ScannedAt.Format(time.RFC3339)
	}
	out.Warnings = scan.Warnings
	if scan.State == prometheuspkg.RightsizingScanUnavailable {
		out.Remediation = rightsizingRemediation(scan.Reason)
		out.Workloads = []rightsizingWorkloadDTO{}
		out.Guidance = rightsizingGuidance(out.State, input.IncludeBalanced, scope, out.NamespaceScope)
		return toJSONResult(out)
	}

	ranked := make([]rightsizingWorkloadDTO, 0, len(scan.Workloads))
	var omitted rightsizingOmissions
	incompleteEvidence := false
	for _, workload := range scan.Workloads {
		filtered := filterRightsizingRows(workload.Rows, input.IncludeBalanced)
		omitted.add(filtered.omitted)
		incompleteEvidence = incompleteEvidence || filtered.incompleteEvidence
		if len(filtered.rows) == 0 {
			continue
		}
		ranked = append(ranked, rightsizingWorkloadDTO{
			Kind:         workload.Kind,
			Namespace:    workload.Namespace,
			Name:         workload.Name,
			Replicas:     workload.Replicas,
			ScaledToZero: workload.ScaledToZero,
			Rows:         filtered.rows,
			requestDelta: filtered.requestDelta,
		})
	}
	// Rank once per workload rather than recomputing the row scan inside every
	// comparison, which sorting would otherwise do O(log n) times each.
	sort.SliceStable(ranked, func(i, j int) bool { return ranked[i].requestDelta > ranked[j].requestDelta })
	if len(ranked) > limit {
		ranked = ranked[:limit]
		out.Truncated = true
	}
	out.Workloads = ranked
	if omitted.total() > 0 {
		out.Omitted = &omitted
	}
	if incompleteEvidence && out.State == prometheuspkg.RightsizingScanComplete {
		out.State = prometheuspkg.RightsizingScanPartial
	}
	if len(out.NamespaceScope) > 0 && out.State == prometheuspkg.RightsizingScanComplete {
		out.State = prometheuspkg.RightsizingScanPartial
		if out.Reason == "" {
			out.Reason = "namespace_scope_limited"
		}
	}
	out.Guidance = rightsizingGuidance(out.State, input.IncludeBalanced, scope, out.NamespaceScope)
	return toJSONResult(out)
}

// filteredRows is everything one pass over a workload's raw rows yields.
type filteredRows struct {
	rows    []rightsizingRowDTO
	omitted rightsizingOmissions
	// requestDelta ranks the workload: the largest relative request change any
	// surviving container asks for, so a small container wanting 10x outranks
	// a large one shaving 5%.
	requestDelta float64
	// incompleteEvidence reads the RAW rows, not what survived filtering, so
	// include_balanced=true still reports partial when evidence was missing.
	incompleteEvidence bool
}

func filterRightsizingRows(rows []prometheuspkg.RightsizingRow, includeBalanced bool) filteredRows {
	var result filteredRows
	result.rows = make([]rightsizingRowDTO, 0, len(rows))
	for _, row := range rows {
		if rowHasIncompleteEvidence(row) {
			result.incompleteEvidence = true
		}
		if !includeBalanced && !rightsizingRowActionable(row.Fit) {
			switch {
			case row.QueryError != "":
				result.omitted.QueryError++
			case row.Fit == prometheuspkg.FitInsufficientHistory:
				result.omitted.InsufficientHistory++
			default:
				result.omitted.Balanced++
			}
			continue
		}
		dto := rightsizingRowDTO{
			Container:            row.Container,
			Resource:             row.Resource,
			Fit:                  row.Fit,
			Confidence:           row.Confidence,
			CurrentRequest:       row.CurrentRequest,
			CurrentLimit:         row.CurrentLimit,
			RecommendedRequest:   row.RecommendedReq,
			Coverage:             row.Coverage,
			RecommendationReason: row.RecommendationReason,
			ReductionLimited:     row.ReductionLimited,
			Bursty:               row.Bursty,
			HPAManaged:           row.HPAManaged,
			HPAEvidenceAvailable: row.HPAEvidenceAvailable,
			CurrentPodOOM:        row.CurrentPodOOM,
			WindowOOMEvidence:    row.WindowOOMEvidence,
			OOMEvidenceAvailable: row.OOMEvidenceAvailable,
			LimitConflict:        row.LimitConflict,
			QueryError:           row.QueryError,
		}
		if row.Observed != nil {
			dto.Observed = row.Observed.Formatted
		}
		if row.Peak != nil {
			dto.Peak = row.Peak.Formatted
		}
		if row.ThrottleAvailable && row.ThrottleRatio != nil {
			formatted := fmt.Sprintf("%.1f%%", *row.ThrottleRatio*100)
			dto.ThrottleRatio = &formatted
		}
		result.rows = append(result.rows, dto)

		switch {
		// No recommendation means evidence was blocked (HPA, OOM, query
		// error); there is nothing to act on, so it ranks last.
		case row.RecommendedReq == nil:
		// A container with no effective request is the strongest actionable
		// signal and has no ratio to compute, so it sorts above every
		// proportional change. Key on the fit: an explicit 0 request is still
		// a missing request, and would otherwise rank at the bottom.
		case row.Fit == prometheuspkg.FitMissingRequest || row.CurrentRequest == nil:
			result.requestDelta = math.Inf(1)
		default:
			if ratio := requestChangeRatio(row); ratio > result.requestDelta {
				result.requestDelta = ratio
			}
		}
	}
	return result
}

func rightsizingRowActionable(fit prometheuspkg.RightsizingFit) bool {
	switch fit {
	case prometheuspkg.FitOversized, prometheuspkg.FitUnderRequested, prometheuspkg.FitMissingRequest:
		return true
	}
	return false
}

func requestChangeRatio(row prometheuspkg.RightsizingRow) float64 {
	if row.CurrentRequestValue == nil || row.RecommendedRequestValue == nil || *row.CurrentRequestValue <= 0 {
		return 0
	}
	ratio := *row.RecommendedRequestValue / *row.CurrentRequestValue
	if ratio < 1 {
		ratio = 1 / ratio
	}
	return ratio
}

// mcpScanAuthorizer answers scope questions for the impersonated MCP caller.
type mcpScanAuthorizer struct {
	ctx context.Context
}

func (a mcpScanAuthorizer) CanListAllNamespaces(resource string) bool {
	return canReadInNamespace(a.ctx, "apps", resource, "", "list")
}

func (a mcpScanAuthorizer) FilterNamespaces(resource string, namespaces []string) []string {
	return filterNamespacesByCanRead(a.ctx, "apps", resource, "list", namespaces)
}

func rightsizingScanUnavailable(scope, namespace, reason string) rightsizingResponse {
	out := rightsizingUnavailable(scope, reason)
	out.Namespace = namespace
	return out
}

func rightsizingUnavailable(scope, reason string) rightsizingResponse {
	return rightsizingResponse{
		Scope:       scope,
		State:       prometheuspkg.RightsizingScanUnavailable,
		Window:      "7d",
		Reason:      reason,
		Remediation: rightsizingRemediation(reason),
		Workloads:   []rightsizingWorkloadDTO{},
	}
}

func rightsizingRemediation(reason string) string {
	switch reason {
	case "prometheus_unavailable":
		return "No Prometheus found. Radar auto-discovers it, or start radar with --prometheus-url. Recommendations also need kube-state-metrics for 7 days of workload history."
	case "owner_metrics_query_failed", "deployment_owner_metrics_query_failed":
		return "Prometheus is reachable but the workload-ownership query failed. Check that it is healthy and scraping kube-state-metrics; the warnings carry the query error."
	case "owner_metrics_missing":
		return "Prometheus is reachable but has no kube_pod_owner series. Install kube-state-metrics and let it scrape — recommendations need it to map pods back to their workload."
	case "deployment_owner_metrics_missing":
		return "kube_pod_owner is present but kube_replicaset_owner is not, so Deployments cannot be mapped through their ReplicaSets. Check that kube-state-metrics exposes ReplicaSet metrics; StatefulSets and DaemonSets are unaffected."
	case "resource_cache_unavailable":
		return "Radar is not connected to a cluster yet. Retry once the resource cache has synced."
	case "workload_kinds_unavailable":
		return "Deployments, StatefulSets, and DaemonSets are all unreadable in the requested scope — either this identity cannot list them, or the informer cache has not synced them. coverage.restrictedKinds and coverage.unavailableKinds separate the two."
	case "namespace_scope_limited":
		return "The scan succeeded but reached only the namespaces in namespaceScope — this identity cannot list workloads cluster-wide, or radar is pinned to a namespace with --namespace."
	case "access_denied":
		return "This identity cannot list workloads in the requested scope."
	case "scan_deadline_exceeded":
		return "The scan ran out of its 45-second budget before finishing. Narrow it with scope=\"namespace\", or target one workload with scope=\"workload\"."
	default:
		return ""
	}
}

func rightsizingGuidance(state prometheuspkg.RightsizingScanState, includeBalanced bool, scope string, namespaceScope []string) string {
	parts := []string{
		"Recommendations come from 7 days of observed usage, not live metrics. Check confidence and coverage before acting: low confidence means insufficient history, not correctly sized.",
	}
	if state == prometheuspkg.RightsizingScanPartial {
		if scope == "workload" {
			if includeBalanced {
				parts = append(parts, "State is partial — some of this workload's containers had no usable evidence. Do not treat their rows as a verdict that the container is correctly sized.")
			} else {
				parts = append(parts, "State is partial — some of this workload's containers had no usable evidence and were withheld; see the omitted counts. Do not report the returned rows as the whole workload.")
			}
		} else {
			// restrictedKinds/unavailableKinds name kinds, not workloads: a
			// restricted kind can still have returned workloads from the
			// namespaces the caller could read.
			parts = append(parts, "State is partial — some evidence is missing. Read coverage: restrictedKinds and unavailableKinds name workload kinds that could not be fully evaluated, completedBatches below batches means the scan stopped early, and warnings cover the rest. Do not describe partial rows as cluster-wide.")
		}
	}
	if len(namespaceScope) > 0 {
		parts = append(parts, fmt.Sprintf("This scan reached only the %d namespace(s) named in namespaceScope, not the whole cluster — report it as that scope, never as cluster-wide.", len(namespaceScope)))
	}
	if !includeBalanced {
		parts = append(parts, "Correctly-sized and unevidenced containers are omitted — see the omitted counts; pass include_balanced=true to see those rows.")
	}
	return strings.Join(parts, " ")
}
