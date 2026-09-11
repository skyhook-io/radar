package mcp

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
)

// Reasons Radar's MCP layer assigns on top of the engine's own, so a partial
// state always names its cause.
const (
	reasonRowEvidenceIncomplete = "row_evidence_incomplete"
	reasonNamespaceScopeLimited = "namespace_scope_limited"
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
	OOMEvidenceAvailable *bool                               `json:"oomEvidenceAvailable,omitempty"`
	ThrottleRatio        *string                             `json:"throttleRatio,omitempty"`
	LimitConflict        bool                                `json:"limitConflict,omitempty"`
	QueryError           string                              `json:"queryError,omitempty"`
}

type rightsizingWorkloadDTO struct {
	Kind      string `json:"kind"`
	Namespace string `json:"namespace"`
	Name      string `json:"name"`
	// Replicas is what turns a per-container recommendation into cluster
	// impact, so it is emitted even when zero rather than omitted.
	Replicas       int                              `json:"replicas"`
	ScaledToZero   bool                             `json:"scaledToZero,omitempty"`
	Classification prometheuspkg.RightsizingClass   `json:"classification,omitempty"`
	Impact         *prometheuspkg.RightsizingImpact `json:"impact,omitempty"`
	Rows           []rightsizingRowDTO              `json:"rows"`
}

// rankKey breaks impact ties on identity so repeated scans return the same
// order; an agent comparing two runs would otherwise read reshuffling as change.
func (w rightsizingWorkloadDTO) rankKey() string {
	return w.Namespace + "/" + w.Kind + "/" + w.Name
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
	TotalWorkloads  int                                    `json:"totalWorkloads,omitempty"`
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
	// Silently dropping a cap the caller set lets an agent believe it applied.
	if input.Limit > 0 {
		return nil, nil, errors.New(`limit applies to scope="namespace" and scope="cluster", which rank workloads; scope="workload" returns every row of the one workload you named`)
	}

	if !prometheuspkg.IsRightsizingKind(kind) {
		return nil, nil, errUnsupportedRightsizingKind(kind)
	}

	if !namespaceWithinPin(namespace) {
		return toJSONResult(rightsizingUnavailable("workload", ReasonOutsideNamespaceScope))
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

	// keepAll: the caller named one workload, so every row is the answer it
	// asked for — a handful of rows, and omitting any of them invites the agent
	// to report the remainder as the whole container set.
	filtered := filterRightsizingRows(resp.Rows, input.IncludeBalanced, true, resp.Replicas, resp.ScaledToZero)
	sampleAvailable := resp.SampleAvailable
	impact := filtered.impact
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
			Kind:           resp.Kind,
			Namespace:      resp.Namespace,
			Name:           resp.Name,
			Replicas:       resp.Replicas,
			ScaledToZero:   resp.ScaledToZero,
			Classification: filtered.classification,
			Impact:         &impact,
			Rows:           filtered.rows,
		}},
	}
	switch {
	case !sampleAvailable:
		out.State = prometheuspkg.RightsizingScanUnavailable
	case filtered.incompleteEvidence:
		out.State = prometheuspkg.RightsizingScanPartial
		if out.Reason == "" {
			out.Reason = reasonRowEvidenceIncomplete
		}
	}
	if filtered.omitted.total() > 0 {
		out.Omitted = &filtered.omitted
	}
	out.Remediation = rightsizingRemediation(out.Reason)
	out.Guidance = rightsizingGuidance(rightsizingGuidanceInput{
		state:            out.State,
		includeBalanced:  input.IncludeBalanced,
		scope:            "workload",
		reason:           out.Reason,
		omitted:          filtered.omitted,
		reductionLimited: filtered.reductionLimited,
		keepAll:          true,
	})
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
		reason := "access_denied"
		if pinReason := DeniedScopeReason(requestedNamespaces(namespace)); pinReason != "" {
			reason = pinReason
		}
		return toJSONResult(rightsizingScanUnavailable(scope, namespace, reason))
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
		out.Guidance = rightsizingGuidance(rightsizingGuidanceInput{
			state:          out.State,
			scope:          scope,
			reason:         out.Reason,
			namespaceScope: out.NamespaceScope,
		})
		return toJSONResult(out)
	}

	ranked := make([]rightsizingWorkloadDTO, 0, len(scan.Workloads))
	var omitted rightsizingOmissions
	incompleteEvidence := false
	reductionLimited := false
	totalRows := 0
	for _, workload := range scan.Workloads {
		filtered := filterRightsizingRows(workload.Rows, input.IncludeBalanced, false, workload.Replicas, workload.ScaledToZero)
		omitted.add(filtered.omitted)
		incompleteEvidence = incompleteEvidence || filtered.incompleteEvidence
		reductionLimited = reductionLimited || filtered.reductionLimited
		if len(filtered.rows) == 0 {
			continue
		}
		totalRows++
		impact := filtered.impact
		ranked = append(ranked, rightsizingWorkloadDTO{
			Kind:           workload.Kind,
			Namespace:      workload.Namespace,
			Name:           workload.Name,
			Replicas:       workload.Replicas,
			ScaledToZero:   workload.ScaledToZero,
			Classification: filtered.classification,
			Impact:         &impact,
			Rows:           filtered.rows,
		})
	}
	// Rank the way the Rightsizing screen does — class, then replica-weighted
	// absolute change — so the tool and the screen agree on where the waste is.
	sort.SliceStable(ranked, func(i, j int) bool {
		return prometheuspkg.RightsizingRankLess(
			ranked[i].Classification, *ranked[i].Impact, ranked[i].rankKey(),
			ranked[j].Classification, *ranked[j].Impact, ranked[j].rankKey(),
		)
	})
	if len(ranked) > limit {
		ranked = ranked[:limit]
		out.Truncated = true
		out.TotalWorkloads = totalRows
	}
	out.Workloads = ranked
	if omitted.total() > 0 {
		out.Omitted = &omitted
	}
	// Row-level gaps get their own reason. Reporting partial with reason null
	// leaves the agent no way to tell which of the coverage fields to read, and
	// the ones the generic guidance names may all be empty.
	if incompleteEvidence && out.State == prometheuspkg.RightsizingScanComplete {
		out.State = prometheuspkg.RightsizingScanPartial
		if out.Reason == "" {
			out.Reason = reasonRowEvidenceIncomplete
		}
	}
	// A narrowed scope is worth naming whether or not something else already
	// flipped the state: gating on state==complete left a pinned Radar
	// reporting partial with no reason at all.
	if len(out.NamespaceScope) > 0 {
		out.State = prometheuspkg.RightsizingScanPartial
		if out.Reason == "" {
			out.Reason = reasonNamespaceScopeLimited
		}
	}
	// Remediation travels with the reason on every path, not only the
	// unavailable one — it is the field the tool description points agents at.
	out.Remediation = rightsizingRemediation(out.Reason)
	out.Guidance = rightsizingGuidance(rightsizingGuidanceInput{
		state:            out.State,
		includeBalanced:  input.IncludeBalanced,
		scope:            scope,
		reason:           out.Reason,
		namespaceScope:   out.NamespaceScope,
		coverage:         &coverage,
		omitted:          omitted,
		reductionLimited: reductionLimited,
	})
	return toJSONResult(out)
}

// filteredRows is everything one pass over a workload's raw rows yields.
type filteredRows struct {
	rows    []rightsizingRowDTO
	omitted rightsizingOmissions
	// classification and impact are computed from the RAW rows: a need_data row
	// filtered out of the response still decides how the workload ranks.
	classification prometheuspkg.RightsizingClass
	impact         prometheuspkg.RightsizingImpact
	// incompleteEvidence reads the RAW rows, not what survived filtering, so
	// include_balanced=true still reports partial when evidence was missing.
	incompleteEvidence bool
	// reductionLimited reports whether any surviving row had its reduction
	// clamped, so the guidance can explain a recommendation that does not
	// follow from the observed value.
	reductionLimited bool
}

// filterRightsizingRows drops correctly-sized rows unless the caller asked for
// them, with two exceptions. keepAll returns every row for scope="workload",
// where the caller named the workload and a handful of rows is the whole answer.
// And a row carrying OOM history, a limit conflict, throttling or autoscaler
// involvement is never dropped: classifyRightsizingFit settles fit from the
// request alone and returns "balanced" before it reaches those checks, so the
// classic OOM shape — request fine, limit too low — would otherwise be filtered
// out as correctly sized, which is the row an agent is looking for.
func filterRightsizingRows(rows []prometheuspkg.RightsizingRow, includeBalanced, keepAll bool, replicas int, scaledToZero bool) filteredRows {
	var result filteredRows
	result.rows = make([]rightsizingRowDTO, 0, len(rows))
	result.classification = prometheuspkg.ClassifyRows(rows, replicas, scaledToZero)
	result.impact = prometheuspkg.CalculateImpact(rows, replicas)
	for _, row := range rows {
		if rowHasIncompleteEvidence(row) {
			result.incompleteEvidence = true
		}
		if !keepAll && !includeBalanced && !rightsizingRowActionable(row.Fit) && !prometheuspkg.NeedsManualReview(row) {
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
			LimitConflict:        row.LimitConflict,
			QueryError:           row.QueryError,
		}
		// OOM evidence only ever gates a memory recommendation; on a CPU row a
		// literal false reads as an evidence gap the agent should weigh.
		if row.Resource == "memory" {
			available := row.OOMEvidenceAvailable
			dto.OOMEvidenceAvailable = &available
		}
		if row.ReductionLimited {
			result.reductionLimited = true
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
	case reasonRowEvidenceIncomplete:
		return "The scan completed but some rows had no usable evidence, or had a recommendation withheld for missing HPA or OOM history. The omitted counts and each row's recommendationReason say which; treat those containers as unjudged, not as correctly sized."
	case "limited_scope_no_workloads":
		return "No workloads were found, and the scan did not cover everything asked for — coverage.restrictedKinds, unavailableKinds and partiallyCachedKinds say which kinds were narrowed. An empty result here is not evidence the cluster has no workloads."
	case "no_workloads":
		return "The scan covered the requested scope and found no Deployment, StatefulSet or DaemonSet in it."
	case "some_evidence_unavailable":
		return "The scan ran but some usage, restart or throttle queries did not answer, so a subset of rows is missing evidence. The warnings name which query families failed; treat the affected containers as unjudged rather than correctly sized."
	case "scan_incomplete":
		return "The scan did not finish every batch within its budget — coverage.completedBatches of coverage.batches says how far it got. The returned rows are a subset; narrow with scope=\"namespace\" or target one workload with scope=\"workload\" for a complete answer."
	case "no_usage_samples":
		return "Prometheus answered but held no workload usage samples for the 7-day window. Check that it is scraping cAdvisor/kubelet metrics and has 7 days of retention."
	case reasonNamespaceScopeLimited:
		if pinned, ok := NamespacePinned(); ok {
			return fmt.Sprintf("The scan succeeded but reached only namespace %s, which radar is pinned to with --namespace. Report it as that scope; this identity's permissions are not the limit.", pinned)
		}
		return "The scan succeeded but reached only the namespaces in namespaceScope — this identity cannot list workloads cluster-wide."
	case "access_denied":
		return "This identity cannot list workloads in the requested scope."
	case ReasonOutsideNamespaceScope:
		if pinned, ok := NamespacePinned(); ok {
			return fmt.Sprintf("radar is pinned to namespace %s with --namespace, so the requested scope is outside what it can see. This is a startup flag, not a permissions problem — restart radar without --namespace to scan cluster-wide.", pinned)
		}
		return "radar is pinned to a single namespace with --namespace, so the requested scope is outside what it can see. This is a startup flag, not a permissions problem."
	case "scan_deadline_exceeded":
		return "The scan ran out of its 45-second budget before finishing. Narrow it with scope=\"namespace\", or target one workload with scope=\"workload\"."
	default:
		return ""
	}
}

// rightsizingGuidanceInput is what the response actually contains, so the
// guidance can name the causes that are present instead of a fixed paragraph
// pointing at coverage fields that may all be empty.
type rightsizingGuidanceInput struct {
	state            prometheuspkg.RightsizingScanState
	includeBalanced  bool
	scope            string
	reason           string
	namespaceScope   []string
	coverage         *prometheuspkg.RightsizingScanCoverage
	omitted          rightsizingOmissions
	reductionLimited bool
	keepAll          bool
}

func rightsizingGuidance(in rightsizingGuidanceInput) string {
	parts := []string{
		"Recommendations come from 7 days of observed usage, not live metrics. Check confidence and coverage before acting: low confidence means insufficient history, not correctly sized.",
	}

	// An unavailable response has no rows, no omitted counts and no coverage to
	// read; telling the agent which fields explain them describes a response it
	// did not receive.
	if in.state == prometheuspkg.RightsizingScanUnavailable {
		parts = append(parts, "State is unavailable — no recommendation was produced. Read reason and remediation; do not report this as a finding that the workloads are correctly sized.")
		return strings.Join(parts, " ")
	}

	if in.state == prometheuspkg.RightsizingScanPartial {
		parts = append(parts, partialGuidance(in)...)
	}
	if len(in.namespaceScope) > 0 {
		parts = append(parts, fmt.Sprintf("This scan reached only the %d namespace(s) named in namespaceScope, not the whole cluster — report it as that scope, never as cluster-wide.", len(in.namespaceScope)))
	}
	if in.reductionLimited {
		parts = append(parts, "Rows with reductionLimited=true were clamped: the recommendation is a conservative step toward observed usage (at most halving the request), not the fitted value. Apply it, let the workload settle, then re-check rather than cutting straight to observed.")
	}
	if !in.includeBalanced && !in.keepAll {
		parts = append(parts, "Correctly-sized and unevidenced containers are omitted — see the omitted counts; pass include_balanced=true to see those rows. Rows carrying OOM history, a limit conflict, throttling or an autoscaler are always returned regardless of fit.")
	}
	return strings.Join(parts, " ")
}

// partialGuidance names only the causes this response carries. The generic
// "read restrictedKinds and unavailableKinds" paragraph was wrong whenever the
// cause was row-level evidence or a narrowed namespace scope, which is the
// common case.
func partialGuidance(in rightsizingGuidanceInput) []string {
	if in.scope == "workload" {
		if in.includeBalanced || in.keepAll {
			return []string{"State is partial — some of this workload's containers had no usable evidence. Do not treat their rows as a verdict that the container is correctly sized."}
		}
		return []string{"State is partial — some of this workload's containers had no usable evidence and were withheld; see the omitted counts. Do not report the returned rows as the whole workload."}
	}

	var causes []string
	if cov := in.coverage; cov != nil {
		// restrictedKinds/unavailableKinds name kinds, not workloads: a
		// restricted kind can still have returned workloads from the
		// namespaces the caller could read.
		if len(cov.RestrictedKinds) > 0 {
			causes = append(causes, fmt.Sprintf("coverage.restrictedKinds (%s) could not be listed by this identity", strings.Join(cov.RestrictedKinds, ", ")))
		}
		if len(cov.UnavailableKinds) > 0 {
			causes = append(causes, fmt.Sprintf("coverage.unavailableKinds (%s) had no usable metrics", strings.Join(cov.UnavailableKinds, ", ")))
		}
		if len(cov.PartiallyCachedKinds) > 0 {
			causes = append(causes, fmt.Sprintf("coverage.partiallyCachedKinds (%s) are cached for only some namespaces, so those kinds were read in a narrower scope than requested", strings.Join(cov.PartiallyCachedKinds, ", ")))
		}
		if cov.Batches > 0 && cov.CompletedBatches < cov.Batches {
			causes = append(causes, fmt.Sprintf("the scan stopped after %d of %d batches", cov.CompletedBatches, cov.Batches))
		}
	}
	if in.omitted.InsufficientHistory > 0 {
		causes = append(causes, fmt.Sprintf("%d row(s) had too little history to judge (omitted.insufficientHistory)", in.omitted.InsufficientHistory))
	}
	if in.omitted.QueryError > 0 {
		causes = append(causes, fmt.Sprintf("%d row(s) failed their usage query (omitted.queryError)", in.omitted.QueryError))
	}
	if in.reason == reasonRowEvidenceIncomplete {
		causes = append(causes, "some returned rows had a recommendation withheld for missing HPA or OOM evidence — see recommendationReason")
	}

	if len(causes) == 0 {
		return []string{fmt.Sprintf("State is partial (reason %q) — some evidence is missing. Do not describe partial rows as cluster-wide.", in.reason)}
	}
	return []string{fmt.Sprintf("State is partial because %s. Do not describe partial rows as cluster-wide.", joinCauses(causes))}
}

func joinCauses(causes []string) string {
	switch len(causes) {
	case 1:
		return causes[0]
	case 2:
		return causes[0] + " and " + causes[1]
	default:
		return strings.Join(causes[:len(causes)-1], ", ") + ", and " + causes[len(causes)-1]
	}
}
