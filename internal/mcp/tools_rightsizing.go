package mcp

import (
	"context"
	"errors"
	"fmt"
	"math"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	prometheuspkg "github.com/skyhook-io/radar/internal/prometheus"
	pkgopencost "github.com/skyhook-io/radar/pkg/opencost"
)

// Reasons Radar's MCP layer assigns on top of the engine's own, so a partial
// state always names its cause.
const (
	reasonRowEvidenceIncomplete = "row_evidence_incomplete"
	reasonNamespaceScopeLimited = "namespace_scope_limited"
	reasonNamespacesExcluded    = "requested_namespaces_excluded"
	// excludedNamespaces reasons. access_denied is this identity's own limit;
	// not_cached is Radar's, whose informer cache holds no workload kind for
	// that namespace however much the caller may read there.
	reasonNamespaceAccessDenied = pkgopencost.ReasonAccessDenied
	reasonNamespaceNotCached    = "not_cached"
	// Assigned by the scan engine; named here because this layer overrides it.
	reasonOnlyDaemonSetsWithoutNodes = "only_daemonsets_without_nodes"
)

const (
	rightsizingDefaultLimit = 20
	rightsizingMaxLimit     = 100
	rightsizingScanBudget   = 20 * time.Second
	// Skipped DaemonSets are named so they can be drilled into; a managed
	// cluster can carry dozens, so the list is capped and the count stays exact.
	skippedDaemonSetsMax = 50
)

type getRightsizingInput struct {
	ScanID         string   `json:"scan_id,omitempty" jsonschema:"scan only: retrieve this existing scan with the same scope and namespaces; never starts a replacement. Omit to reuse the latest scan or start one."`
	Refresh        bool     `json:"refresh,omitempty" jsonschema:"scan only: start fresh instead of reusing a retained result (up to 15 minutes old). An already running scan is reused. Cannot combine with scan_id."`
	Classification string   `json:"classification,omitempty" jsonschema:"scan only: reduction, increase, review, need_data, or in_range; filters before limit and returns all rows of matching workloads; does not reduce scan effort"`
	Scope          string   `json:"scope" jsonschema:"required. workload for one workload (needs kind, name, namespace) - cheap and precise. namespace scans one namespace (needs namespace). cluster scans every Deployment/StatefulSet/DaemonSet and runs 7-day range queries, running in the background for up to 3 minutes. Follow nextCall while scanStatus=running; retained results are reused for up to 15 minutes unless refresh=true"`
	Kind           string   `json:"kind,omitempty" jsonschema:"for scope=workload: Deployment, StatefulSet, or DaemonSet"`
	Name           string   `json:"name,omitempty" jsonschema:"for scope=workload: the workload name"`
	Namespace      string   `json:"namespace,omitempty" jsonschema:"one namespace: required for scope=workload, and for scope=namespace unless namespaces is set; rejected for scope=cluster"`
	Namespaces     []string `json:"namespaces,omitempty" jsonschema:"for scope=namespace only, instead of namespace: scan several namespaces in one call. The background scan budget is shared across these namespaces"`
	IncludeAll     bool     `json:"include_all,omitempty" jsonschema:"also return correctly-sized and unevidenced containers (default false, which returns only oversized, under_requested, and missing_request rows and reports the rest as omitted counts). Ignored for scope=workload, which returns every row of the named workload"`
	Limit          int      `json:"limit,omitempty" jsonschema:"max workloads returned, ranked by safety priority then replica-weighted impact (default 20, max 100). Rejected for scope=workload"`
}

type rightsizingRowDTO struct {
	Container      string                              `json:"container"`
	Resource       string                              `json:"resource"`
	Fit            prometheuspkg.RightsizingFit        `json:"fit"`
	Confidence     prometheuspkg.RightsizingConfidence `json:"confidence"`
	CurrentRequest *string                             `json:"currentRequest,omitempty"`
	CurrentLimit   *string                             `json:"currentLimit,omitempty"`
	// No omitempty: a withheld recommendation must arrive as an explicit null
	// beside its recommendationReason. Omitting the field turns "Radar declined
	// to recommend, and here is why" into a key the reader never sees.
	RecommendedRequest   *string `json:"recommendedRequest"`
	Observed             string  `json:"observed,omitempty"`
	ObservedStatistic    string  `json:"observedStatistic,omitempty"`
	Peak                 string  `json:"p99Usage,omitempty"`
	Coverage             float64 `json:"sampleCoverage"`
	RecommendationReason string  `json:"recommendationReason,omitempty"`
	ReductionLimited     bool    `json:"reductionLimited,omitempty"`
	// Nested and named apart from recommendedRequest so it reads as the end
	// state a clamped step is heading toward, not a second value to apply.
	DemandTarget         *rightsizingDemandTarget `json:"demandTarget,omitempty"`
	Bursty               bool                     `json:"bursty,omitempty"`
	HPAManaged           bool                     `json:"hpaManaged,omitempty"`
	HPAEvidenceAvailable bool                     `json:"hpaEvidenceAvailable"`
	CurrentPodOOM        bool                     `json:"currentPodOOM,omitempty"`
	WindowOOMEvidence    bool                     `json:"windowOomEvidence,omitempty"`
	OOMEvidenceAvailable *bool                    `json:"oomEvidenceAvailable,omitempty"`
	ThrottleAvailable    *bool                    `json:"throttleAvailable,omitempty"`
	ThrottleRatio        *float64                 `json:"throttlePercent,omitempty"`
	LimitConflict        bool                     `json:"limitConflict,omitempty"`
	// The verdict travels with the recommendation. Its absence from impact is a
	// negative signal a reader has to go looking for; this is the positive one.
	NeedsManualReview bool     `json:"needsManualReview,omitempty"`
	ReviewReasons     []string `json:"reviewReasons,omitempty"`
	QueryError        string   `json:"queryError,omitempty"`
}

type rightsizingDemandTarget struct {
	Value string `json:"value"`
	Basis string `json:"basis"`
}

type excludedNamespace struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type rightsizingRequestDelta struct {
	CPU    string `json:"cpu,omitempty"`
	Memory string `json:"memory,omitempty"`
}

type rightsizingWorkloadDTO struct {
	LiveInventoryUnavailable bool                     `json:"liveInventoryUnavailable,omitempty"`
	RequestDelta             *rightsizingRequestDelta `json:"requestDelta,omitempty"`
	Kind                     string                   `json:"kind"`
	Namespace                string                   `json:"namespace"`
	Name                     string                   `json:"name"`
	// Replicas is what turns a per-container recommendation into cluster
	// impact, so it is emitted even when zero rather than omitted.
	Replicas       int                              `json:"replicas"`
	ScaledToZero   bool                             `json:"scaledToZero,omitempty"`
	ManagedBy      *prometheuspkg.WorkloadManager   `json:"managedBy,omitempty"`
	Classification prometheuspkg.RightsizingClass   `json:"classification,omitempty"`
	Impact         *prometheuspkg.RightsizingImpact `json:"-"`
	Rows           []rightsizingRowDTO              `json:"rows"`
	// rankKey breaks impact ties on identity so repeated scans return the same
	// order; an agent comparing two runs would otherwise read reshuffling as
	// change. Built once, not per comparison.
	rankKey  string
	priority int
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
	return prometheuspkg.RowUnevidenced(row) ||
		prometheuspkg.IsWithheldRecommendationReason(row.RecommendationReason)
}

type rightsizingCoverage struct {
	AttemptedBatches       int      `json:"attemptedBatches"`
	WorkloadsDiscovered    int      `json:"workloadsDiscovered"`
	WorkloadsEvaluated     int      `json:"workloadsEvaluated"`
	WorkloadsWithData      int      `json:"workloadsWithData"`
	Batches                int      `json:"batches"`
	SuccessfulBatches      int      `json:"successfulBatches"`
	RestrictedKinds        []string `json:"restrictedKinds,omitempty"`
	UnavailableKinds       []string `json:"unavailableKinds,omitempty"`
	PartiallyCachedKinds   []string `json:"partiallyCachedKinds,omitempty"`
	DaemonSetsWithoutNodes int      `json:"daemonSetsWithoutNodes,omitempty"`
}

func newRightsizingCoverage(coverage prometheuspkg.RightsizingScanCoverage) *rightsizingCoverage {
	return &rightsizingCoverage{
		AttemptedBatches:       coverage.AttemptedBatches,
		WorkloadsDiscovered:    coverage.WorkloadsDiscovered,
		WorkloadsEvaluated:     coverage.WorkloadsEvaluated,
		WorkloadsWithData:      coverage.WorkloadsWithData,
		Batches:                coverage.Batches,
		SuccessfulBatches:      coverage.CompletedBatches,
		RestrictedKinds:        coverage.RestrictedKinds,
		UnavailableKinds:       coverage.UnavailableKinds,
		PartiallyCachedKinds:   coverage.PartiallyCachedKinds,
		DaemonSetsWithoutNodes: coverage.DaemonSetsWithoutNodes,
	}
}

type rightsizingResponse struct {
	prometheuspkg.RightsizingScanProgress
	NextCall             *rightsizingNextCall                   `json:"nextCall,omitempty"`
	ClassificationCounts map[prometheuspkg.RightsizingClass]int `json:"classificationCounts,omitempty"`
	Scope                string                                 `json:"scope"`
	State                prometheuspkg.RightsizingScanState     `json:"state"`
	Window               string                                 `json:"window"`
	Source               string                                 `json:"source,omitempty"`
	ScannedAt            string                                 `json:"evaluatedAt,omitempty"`
	Namespace            string                                 `json:"namespace,omitempty"`
	NamespaceScope       []string                               `json:"effectiveNamespaces,omitempty"`
	// Excluded requested namespaces are listed by name: a namespace dropped
	// silently reads as one that had nothing to change.
	ExcludedNamespaces []excludedNamespace `json:"excludedNamespaces,omitempty"`
	// A requested namespace holding no scannable workload is usually a typo.
	NamespacesWithoutWorkloads []string `json:"namespacesWithoutWorkloads,omitempty"`
	// SkippedDaemonSets names coverage.daemonSetsWithoutNodes as namespace/name.
	SkippedDaemonSets          []string                               `json:"skippedDaemonSets,omitempty"`
	SkippedDaemonSetsTruncated bool                                   `json:"skippedDaemonSetsTruncated,omitempty"`
	SampleAvailable            *bool                                  `json:"sampleAvailable,omitempty"`
	OwnerCoverage              prometheuspkg.OwnerCoverage            `json:"ownerCoverage,omitempty"`
	Coverage                   *rightsizingCoverage                   `json:"coverage,omitempty"`
	Omitted                    *rightsizingOmissions                  `json:"omitted,omitempty"`
	Workloads                  []rightsizingWorkloadDTO               `json:"workloads"`
	Warnings                   []prometheuspkg.RightsizingScanWarning `json:"warnings,omitempty"`
	Reason                     string                                 `json:"reason,omitempty"`
	Remediation                string                                 `json:"remediation,omitempty"`
	Truncated                  bool                                   `json:"truncated,omitempty"`
	TotalWorkloads             *int                                   `json:"totalWorkloads,omitempty"`
	Guidance                   []string                               `json:"guidance,omitempty"`
}

type rightsizingNextCall struct {
	Tool      string              `json:"tool"`
	Arguments getRightsizingInput `json:"arguments"`
}

func handleGetRightsizing(ctx context.Context, _ *mcp.CallToolRequest, input getRightsizingInput) (*mcp.CallToolResult, any, error) {
	scope := strings.ToLower(strings.TrimSpace(input.Scope))
	if input.ScanID != "" && input.Refresh {
		return nil, nil, errors.New("refresh cannot be combined with scan_id")
	}
	if scope == "workload" && (input.ScanID != "" || input.Refresh) {
		return nil, nil, errors.New("scan_id and refresh apply only to namespace and cluster scans")
	}
	if input.Classification != "" {
		if scope == "workload" {
			return nil, nil, errors.New("classification applies only to namespace and cluster scans")
		}
		switch prometheuspkg.RightsizingClass(input.Classification) {
		case prometheuspkg.ClassReduction, prometheuspkg.ClassIncrease, prometheuspkg.ClassReview, prometheuspkg.ClassNeedData, prometheuspkg.ClassInRange:
		default:
			return nil, nil, errors.New("classification must be reduction, increase, review, need_data, or in_range")
		}
	}
	if input.Limit < 0 {
		return nil, nil, errors.New("limit must be between 1 and 100")
	}

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
	`Deployment/StatefulSet/DaemonSet (7-day range queries, up to 3 minutes in the background — follow nextCall while running, then drill in with scope="workload")`

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
	if len(input.Namespaces) > 0 {
		return nil, nil, errors.New(`namespaces applies to scope="namespace"; scope="workload" takes the one namespace the workload lives in`)
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
	// guessing names. Both checks, like the REST route's prometheusAuthGate:
	// the namespace allow-list, and an exact "get" SAR on the workload kind.
	if !checkNamespaceAccess(ctx, namespace) || !canReadInNamespace(ctx, "apps", prometheuspkg.ScanKindResource(kind), namespace, "get") {
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
	filtered := filterRightsizingRows(resp.Rows, input.IncludeAll, true, resp.Replicas, resp.ScaledToZero)
	sampleAvailable := resp.SampleAvailable
	impact := filtered.impact
	pointQueryErrorsAtWarnings(filtered.rows)
	out := rightsizingResponse{
		Scope:           "workload",
		State:           prometheuspkg.RightsizingScanComplete,
		Window:          resp.Window,
		Source:          resp.Source,
		Namespace:       resp.Namespace,
		SampleAvailable: &sampleAvailable,
		OwnerCoverage:   resp.OwnerCoverage,
		Warnings:        resp.Warnings,
		Reason:          rightsizingReasonCode(resp.Reason),
		ScannedAt:       time.Now().UTC().Format(time.RFC3339),
		Workloads: []rightsizingWorkloadDTO{{
			Kind:                     resp.Kind,
			Namespace:                resp.Namespace,
			Name:                     resp.Name,
			Replicas:                 resp.Replicas,
			ScaledToZero:             resp.ScaledToZero,
			ManagedBy:                resp.ManagedBy,
			Classification:           filtered.classification,
			Impact:                   &impact,
			RequestDelta:             &rightsizingRequestDelta{CPU: impact.CPU, Memory: impact.Memory},
			LiveInventoryUnavailable: filtered.liveInventoryUnavailable,
			Rows:                     filtered.rows,
		}},
	}
	out.State, out.Reason = workloadScopeState(sampleAvailable, filtered.incompleteEvidence, out.Warnings, out.Reason)
	if filtered.omitted.total() > 0 {
		out.Omitted = &filtered.omitted
	}
	out.Remediation = rightsizingRemediation("workload", out.Reason)
	out.Guidance = rightsizingGuidance(rightsizingGuidanceInput{
		state:             out.State,
		includeAll:        input.IncludeAll,
		scope:             "workload",
		reason:            out.Reason,
		omitted:           filtered.omitted,
		reductionLimited:  rowGaps(out.Workloads).reductionLimited,
		needsManualReview: rowGaps(out.Workloads).needsManualReview,
		workloadsShown:    true,
	})
	return toJSONResult(out)
}

// workloadScopeState settles what a workload-scope answer claims about itself.
// The order is precedence: a row that lost its own evidence outranks a query
// that failed behind intact rows, because the two send a reader to different
// places — the rows, or the warnings.
func workloadScopeState(sampleAvailable, incompleteEvidence bool, warnings []prometheuspkg.RightsizingScanWarning, reason string) (prometheuspkg.RightsizingScanState, string) {
	fallback := func(candidate string) string {
		// A reason the engine already set is the more specific one.
		if reason != "" {
			return reason
		}
		return candidate
	}
	switch {
	case !sampleAvailable:
		return prometheuspkg.RightsizingScanUnavailable, reason
	case incompleteEvidence:
		return prometheuspkg.RightsizingScanPartial, fallback(reasonRowEvidenceIncomplete)
	case len(warnings) > 0:
		// The rows are intact, so row_evidence_incomplete would send the reader
		// hunting for an incomplete row that does not exist. The failure sits
		// behind them, and the warnings name it.
		return prometheuspkg.RightsizingScanPartial, fallback(prometheuspkg.ReasonSomeEvidenceUnavailable)
	}
	return prometheuspkg.RightsizingScanComplete, reason
}

func rightsizingScanScope(ctx context.Context, input getRightsizingInput, scope string) (*mcp.CallToolResult, any, error) {
	limit := input.Limit
	if limit <= 0 {
		limit = rightsizingDefaultLimit
	}
	if limit > rightsizingMaxLimit {
		return nil, nil, fmt.Errorf("limit %d exceeds the maximum of %d — pass %d or fewer, or narrow the scope; silently returning %d would look like the whole ranking", input.Limit, rightsizingMaxLimit, rightsizingMaxLimit, rightsizingMaxLimit)
	}

	namespace := strings.TrimSpace(input.Namespace)
	requested, err := rightsizingScanNamespaces(scope, input)
	if err != nil {
		return nil, nil, err
	}
	listForm := len(input.Namespaces) > 0

	// The budget covers authorization too: the SAR fanout below is unbounded in
	// namespace count and MCP has no outer deadline the way the REST route sits
	// behind the server's request timeout.
	scanCtx, cancel := context.WithTimeout(ctx, rightsizingScanBudget)
	defer cancel()

	manager := prometheuspkg.RightsizingScans()
	generation := manager.Generation()
	allowed := scopedNamespacesForUser(scanCtx, requested)
	if scanCtx.Err() != nil {
		return nil, nil, fmt.Errorf("could not authorize scan scope within the request budget: %w", scanCtx.Err())
	}
	var excluded []excludedNamespace
	if listForm {
		excluded = excludedRequestedNamespaces(requested, allowed)
	}
	if allowed != nil && len(allowed) == 0 {
		out := rightsizingScanUnavailable(scope, namespace, deniedScopeReason(requested))
		out.ExcludedNamespaces = excluded
		setRightsizingScanGuidance(&out, input)
		return toJSONResult(out)
	}

	scanScope := prometheuspkg.ResolveScanScope(allowed, mcpScanAuthorizer{ctx: scanCtx})

	if scanCtx.Err() != nil {
		return nil, nil, fmt.Errorf("could not authorize scan scope within the request budget: %w", scanCtx.Err())
	}
	snapshot, err := manager.Resolve(scanCtx, prometheuspkg.RightsizingScanRequest{
		Generation: generation, Namespaces: allowed, Scope: scanScope, ID: input.ScanID,
		Start: input.ScanID == "", Refresh: input.Refresh, Wait: rightsizingScanBudget,
	})
	if err != nil {
		return nil, nil, err
	}
	scan := *snapshot
	deadlineCut := scan.ScanStatus == "timed_out"
	// Both narrowings land here, after the scan, because the second one is the
	// scope the engine resolved: the namespace check above is a broad sentinel
	// that per-kind access can still deny outright, and the informer cache can
	// then hold nothing for a namespace this identity may read. A namespace
	// either survives both or is named with the reason that dropped it.
	var scanned []string
	if listForm {
		scanned = requested
		if allowed != nil {
			scanned = allowed
		}
		var denied, uncached []string
		scanned, denied = splitByKindAccess(scanned, scanScope.NamespacesByKind)
		scanned, uncached = splitByKindAccess(scanned, scan.Coverage.ScannedNamespacesByKind)
		excluded = append(excluded, excludedWithReason(denied, reasonNamespaceAccessDenied)...)
		excluded = append(excluded, excludedWithReason(uncached, reasonNamespaceNotCached)...)
	}
	coverage := scan.Coverage

	out := rightsizingResponse{
		RightsizingScanProgress: scan.RightsizingScanProgress,
		Scope:                   scope,
		State:                   scan.State,
		Window:                  scan.Window,
		Source:                  scan.Source,
		Namespace:               namespace,
		Reason:                  scan.Reason,
		// Counters are always emitted: "0 of 1 batches succeeded" is precisely
		// the case a reader needs, and omitempty would hide it.
		Coverage: newRightsizingCoverage(coverage),
	}
	// scope="cluster" resolves to whatever this identity can list, or to the
	// server's --namespace pin. Without naming that set the caller cannot tell
	// a two-namespace scan from a cluster-wide one.
	if scope == "cluster" && allowed != nil {
		// Same narrowing as a namespaces list: a namespace no workload kind was
		// read in, by RBAC or by the cache, was not scanned.
		kept, _ := splitByKindAccess(allowed, scan.Coverage.ScannedNamespacesByKind)
		out.NamespaceScope = slices.Sorted(slices.Values(kept))
	}
	if listForm {
		out.NamespaceScope = slices.Sorted(slices.Values(scanned))
		out.ExcludedNamespaces = excluded
	}
	if !scan.ScannedAt.IsZero() {
		out.ScannedAt = scan.ScannedAt.UTC().Format(time.RFC3339)
	}
	out.SkippedDaemonSets, out.SkippedDaemonSetsTruncated = truncateRows(scan.Coverage.SkippedDaemonSets, skippedDaemonSetsMax)
	out.Warnings = scan.Warnings
	if scan.State == prometheuspkg.RightsizingScanUnavailable {
		out.Remediation = rightsizingRemediation(scope, scan.Reason)
		out.Workloads = []rightsizingWorkloadDTO{}
		out.Guidance = rightsizingGuidance(rightsizingGuidanceInput{
			state:               out.State,
			scope:               scope,
			reason:              out.Reason,
			effectiveNamespaces: clusterNamespaceScope(scope, out.NamespaceScope),
		})
		setRightsizingScanGuidance(&out, input)
		return toJSONResult(out)
	}
	if listForm && scan.ScanStatus != "running" && scanCoveredEveryWorkload(scan.Coverage) {
		out.NamespacesWithoutWorkloads = namespacesWithoutWorkloads(scanned, scan.Workloads, scan.Coverage.SkippedDaemonSets)
	}

	ranked, omitted, incompleteEvidence, counts := selectRightsizingWorkloads(scan.Workloads, input)
	out.ClassificationCounts = counts
	kept := rowGaps(ranked)
	totalWorkloads := len(ranked)
	out.TotalWorkloads = &totalWorkloads
	if len(ranked) > limit {
		ranked = ranked[:limit]
		out.Truncated = true
	}
	out.Workloads = ranked
	// Read after truncation: the guidance sentence is about rows the caller can
	// see, and a clamped or failed row ranked past limit is not in the response.
	returned := rowGaps(ranked)
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
	// flipped the state, or a pinned Radar reports partial with no reason.
	if scope == "cluster" && len(out.NamespaceScope) > 0 {
		out.State = prometheuspkg.RightsizingScanPartial
		// no_workloads claims the requested scope was covered; for a cluster
		// request narrowed to effectiveNamespaces that is exactly what did not happen.
		if out.Reason == "" || scanClaimsFullCoverage(out.Reason) {
			out.Reason = reasonNamespaceScopeLimited
		}
	}
	if len(out.ExcludedNamespaces) > 0 {
		out.State = prometheuspkg.RightsizingScanPartial
		if out.Reason == "" || scanClaimsFullCoverage(out.Reason) {
			out.Reason = reasonNamespacesExcluded
		}
	}
	// Remediation travels with the reason on every path, not only the
	// unavailable one — it is the field the tool description points agents at.
	out.Remediation = rightsizingRemediation(scope, out.Reason)
	out.Guidance = rightsizingGuidance(rightsizingGuidanceInput{
		state:                      out.State,
		includeAll:                 input.IncludeAll || input.Classification != "",
		scope:                      scope,
		reason:                     out.Reason,
		effectiveNamespaces:        clusterNamespaceScope(scope, out.NamespaceScope),
		coverage:                   &coverage,
		omitted:                    omitted,
		reductionLimited:           returned.reductionLimited,
		needsManualReview:          returned.needsManualReview,
		returnedQueryErrors:        returned.queryErrors,
		truncatedQueryErrors:       kept.queryErrors - returned.queryErrors,
		returnedShortHistory:       returned.shortHistory,
		truncatedShortHistory:      kept.shortHistory - returned.shortHistory,
		excludedNamespaces:         out.ExcludedNamespaces,
		namespacesWithoutWorkloads: out.NamespacesWithoutWorkloads,
		workloadsShown:             len(out.Workloads) > 0,
		deadlineExceeded:           deadlineCut,
		batchQueryFailed:           scan.ScanStatus != "cancelled" && hasBatchQueryFailure(scan.Warnings),
	})
	setRightsizingScanGuidance(&out, input)
	return toJSONResult(out)
}

func setRightsizingScanGuidance(out *rightsizingResponse, input getRightsizingInput) {
	if out.ScanStatus == "running" {
		out.State, out.Reason, out.Remediation = prometheuspkg.RightsizingScanPartial, "scan_in_progress", ""
		out.NamespacesWithoutWorkloads = nil
		next := input
		next.ScanID, next.Refresh = out.ScanID, false
		out.NextCall = &rightsizingNextCall{Tool: "get_rightsizing", Arguments: next}
		out.Guidance = []string{"Scan still running. Coverage, counts, and rankings describe only workloads evaluated so far. Wait 5 seconds, then use nextCall to retrieve progress without repeating completed queries."}
	} else if out.ScanID != "" {
		out.Guidance = append(out.Guidance, "This is a retained snapshot from evaluatedAt. Reuse scan_id to change filters without rescanning; omit scan_id and set refresh=true to scan again after changes.")
	}
}

type rowGapCounts struct {
	queryErrors, shortHistory int
	reductionLimited          bool
	needsManualReview         bool
}

// rowGaps counts the unevidenced and clamped rows a set of workloads carries.
func rowGaps(workloads []rightsizingWorkloadDTO) rowGapCounts {
	var counts rowGapCounts
	for _, workload := range workloads {
		for _, row := range workload.Rows {
			counts.reductionLimited = counts.reductionLimited || row.ReductionLimited
			counts.needsManualReview = counts.needsManualReview || row.NeedsManualReview
			switch {
			case row.QueryError != "":
				counts.queryErrors++
			case row.Fit == prometheuspkg.FitInsufficientHistory:
				counts.shortHistory++
			}
		}
	}
	return counts
}

func hasBatchQueryFailure(warnings []prometheuspkg.RightsizingScanWarning) bool {
	return slices.ContainsFunc(warnings, func(w prometheuspkg.RightsizingScanWarning) bool {
		return strings.HasSuffix(w.Code, "_query_failed") && !strings.HasSuffix(w.Code, "owner_metrics_query_failed")
	})
}

func hasWarningCode(warnings []prometheuspkg.RightsizingScanWarning, code string) bool {
	return slices.ContainsFunc(warnings, func(w prometheuspkg.RightsizingScanWarning) bool { return w.Code == code })
}

// rightsizingScanNamespaces validates the namespace inputs for a scan scope and
// returns the requested list, nil for a whole-cluster request.
func rightsizingScanNamespaces(scope string, input getRightsizingInput) ([]string, error) {
	namespace := strings.TrimSpace(input.Namespace)
	identifiers := strings.TrimSpace(input.Kind) != "" || strings.TrimSpace(input.Name) != ""
	if scope == "cluster" {
		if namespace != "" || len(input.Namespaces) > 0 || identifiers {
			return nil, errors.New(`scope="cluster" takes no namespace, namespaces, kind, or name — use scope="namespace" or scope="workload" to narrow`)
		}
		return nil, nil
	}
	if identifiers {
		return nil, errors.New(`scope="namespace" takes no kind or name — use scope="workload" to target one workload, which is far cheaper than scanning the namespace`)
	}
	if namespace != "" && len(input.Namespaces) > 0 {
		return nil, errors.New(`pass namespace for one namespace or namespaces for several, not both`)
	}
	// A comma-joined name passes authorization as a namespace that does not
	// exist, and the scan then reports it complete with no workloads.
	if strings.Contains(namespace, ",") {
		return nil, fmt.Errorf(`namespace takes one name — pass several as namespaces: [%s]`, quotedList(splitCSVStr(namespace)))
	}
	if namespace != "" {
		return []string{namespace}, nil
	}
	requested := make([]string, 0, len(input.Namespaces))
	seen := map[string]bool{}
	for _, raw := range input.Namespaces {
		name := strings.TrimSpace(raw)
		if name == "" || strings.Contains(name, ",") {
			return nil, fmt.Errorf("namespaces entries must each be one non-empty namespace name, got %q", raw)
		}
		if !seen[name] {
			seen[name] = true
			requested = append(requested, name)
		}
	}
	if len(requested) == 0 {
		return nil, errors.New(`scope="namespace" needs a namespace — use scope="cluster" to scan the whole cluster`)
	}
	return requested, nil
}

func quotedList(values []string) string {
	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}
	return strings.Join(quoted, ", ")
}

// excludedRequestedNamespaces names each requested namespace the scope filter
// removed. The pin is checked per name: a pin and an RBAC denial produce the
// same missing entry, and only the pin is a startup flag rather than access.
func excludedRequestedNamespaces(requested, allowed []string) []excludedNamespace {
	if allowed == nil {
		return nil
	}
	kept := make(map[string]bool, len(allowed))
	for _, name := range allowed {
		kept[name] = true
	}
	var excluded []excludedNamespace
	for _, name := range requested {
		if kept[name] {
			continue
		}
		reason := reasonNamespaceAccessDenied
		if !namespaceWithinPin(name) {
			reason = ReasonOutsideNamespaceScope
		}
		excluded = append(excluded, excludedNamespace{Name: name, Reason: reason})
	}
	return excluded
}

// splitByKindAccess partitions namespaces by whether any workload kind covers
// them. A kind with a nil list covers every namespace; a kind that is absent
// covers none. Why a namespace was dropped is the caller's to say — the same
// split runs for RBAC and for the informer cache.
func splitByKindAccess(namespaces []string, byKind map[string][]string) (kept, dropped []string) {
	readable := map[string]bool{}
	for _, kindNamespaces := range byKind {
		if kindNamespaces == nil {
			return namespaces, nil
		}
		for _, name := range kindNamespaces {
			readable[name] = true
		}
	}
	for _, name := range namespaces {
		if readable[name] {
			kept = append(kept, name)
		} else {
			dropped = append(dropped, name)
		}
	}
	return kept, dropped
}

func excludedWithReason(names []string, reason string) []excludedNamespace {
	excluded := make([]excludedNamespace, 0, len(names))
	for _, name := range names {
		excluded = append(excluded, excludedNamespace{Name: name, Reason: reason})
	}
	return excluded
}

// scanClaimsFullCoverage marks the empty-scan reasons that assert the whole
// requested scope was read, which a narrowed scope must override.
func scanClaimsFullCoverage(reason string) bool {
	return reason == "no_workloads" || reason == reasonOnlyDaemonSetsWithoutNodes || reason == reasonRowEvidenceIncomplete
}

// scanCoveredEveryWorkload reports whether a namespace absent from the results
// can be read as "held nothing": a narrowed kind or an unfinished scan leaves
// out workloads that were there.
func scanCoveredEveryWorkload(coverage prometheuspkg.RightsizingScanCoverage) bool {
	return coverage.WorkloadsEvaluated == coverage.WorkloadsDiscovered &&
		len(coverage.RestrictedKinds) == 0 && len(coverage.UnavailableKinds) == 0 && len(coverage.PartiallyCachedKinds) == 0
}

// namespacesWithoutWorkloads leaves out namespaces holding skipped DaemonSets:
// those hold workloads, just none running right now.
func namespacesWithoutWorkloads(scanned []string, workloads []prometheuspkg.RightsizingScanWorkload, skippedDaemonSets []string) []string {
	present := map[string]bool{}
	for _, workload := range workloads {
		present[workload.Namespace] = true
	}
	for _, identity := range skippedDaemonSets {
		namespace, _, _ := strings.Cut(identity, "/")
		present[namespace] = true
	}
	var empty []string
	for _, name := range scanned {
		if !present[name] {
			empty = append(empty, name)
		}
	}
	sort.Strings(empty)
	return empty
}

// clusterNamespaceScope passes effectiveNamespaces to the guidance only where it
// means "narrower than the cluster"; on a namespace list it is what was asked.
func clusterNamespaceScope(scope string, effectiveNamespaces []string) []string {
	if scope != "cluster" {
		return nil
	}
	return effectiveNamespaces
}

// pointQueryErrorsAtWarnings rewrites a scan row's generic usage failure to
// name the warning carrying the query's actual error, which the scan records
// once per batch rather than on every row it affected.
func pointQueryErrorsAtWarnings(rows []rightsizingRowDTO) {
	for i := range rows {
		if rows[i].QueryError == prometheuspkg.RowUsageQueryFailed {
			rows[i].QueryError = fmt.Sprintf("%s usage query failed; see warnings code %s_query_failed", rows[i].Resource, rows[i].Resource)
		}
	}
}

// filteredRows is everything one pass over a workload's raw rows yields.
type filteredRows struct {
	rows    []rightsizingRowDTO
	omitted rightsizingOmissions
	// classification and impact are computed from the RAW rows: a need_data row
	// filtered out of the response still decides how the workload ranks.
	classification prometheuspkg.RightsizingClass
	impact         prometheuspkg.RightsizingImpact
	priority       int
	// incompleteEvidence reads the RAW rows, not what survived filtering, so
	// include_all=true still reports partial when evidence was missing.
	incompleteEvidence       bool
	liveInventoryUnavailable bool
}

// filterRightsizingRows drops correctly-sized rows unless the caller asked for
// them, with four exceptions. keepAll returns every row for scope="workload",
// where the caller named the workload and a handful of rows is the whole answer.
// A scaled-to-zero workload classifies as review, which the Rightsizing screen
// lists among its actions, so dropping its rows would drop the workload. A row
// carrying OOM history, a limit conflict, throttling or autoscaler involvement
// is never dropped: classifyRightsizingFit settles fit from the request alone
// and returns "balanced" before it reaches those checks, so the classic OOM
// shape — request fine, limit too low — would otherwise be filtered out as
// correctly sized, which is the row an agent is looking for. And a returned
// container keeps its unevidenced rows: one failed query makes the container
// need_data, and without that row the workload's class has no visible cause.
func filterRightsizingRows(rows []prometheuspkg.RightsizingRow, includeAll, keepAll bool, replicas int, scaledToZero bool) filteredRows {
	var result filteredRows
	result.rows = make([]rightsizingRowDTO, 0, len(rows))
	result.classification, result.impact = prometheuspkg.ClassifyWorkload(rows, replicas, scaledToZero)
	result.priority = prometheuspkg.RightsizingPriority(result.classification, rows, scaledToZero)
	keep := make([]bool, len(rows))
	containerKept := map[string]bool{}
	for i, row := range rows {
		result.liveInventoryUnavailable = result.liveInventoryUnavailable || row.LiveInventoryUnavailable
		if rowHasIncompleteEvidence(row) {
			result.incompleteEvidence = true
		}
		keep[i] = keepAll || includeAll || scaledToZero || rightsizingRowActionable(row.Fit) ||
			prometheuspkg.NeedsManualReview(row) || prometheuspkg.SignificantlyThrottled(row)
		if keep[i] {
			containerKept[row.Container] = true
		}
	}
	for i, row := range rows {
		if !keep[i] && (!prometheuspkg.RowUnevidenced(row) || !containerKept[row.Container]) {
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
			NeedsManualReview:    prometheuspkg.NeedsManualReview(row),
			ReviewReasons:        prometheuspkg.ManualReviewReasons(row),
			QueryError:           row.QueryError,
		}
		// OOM evidence only ever gates a memory recommendation; on a CPU row a
		// literal false reads as an evidence gap the agent should weigh.
		if row.Resource == "memory" {
			available := row.OOMEvidenceAvailable
			dto.OOMEvidenceAvailable = &available
		}
		// The engine computes a demand target before its HPA, OOM and limit
		// checks run, so one exists on rows whose recommendation was withheld.
		// Emitting it only beside a recommendation keeps it from filling the
		// null those checks deliberately left.
		if row.ReductionLimited && row.RecommendedReq != nil && row.CalculatedReq != nil {
			dto.DemandTarget = &rightsizingDemandTarget{Value: *row.CalculatedReq, Basis: prometheuspkg.DemandTargetBasis(row)}
		}
		if row.Observed != nil {
			dto.Observed = row.Observed.Formatted
			dto.ObservedStatistic = row.Observed.Name
		}
		if row.Peak != nil {
			dto.Peak = row.Peak.Formatted
		}
		// Throttling only ever gates a CPU reduction. Without the flag, an absent
		// throttlePercent reads as "not throttled" rather than "not measured".
		if row.Resource == "cpu" {
			available := row.ThrottleAvailable
			dto.ThrottleAvailable = &available
		}
		if row.ThrottleAvailable && row.ThrottleRatio != nil {
			formatted := math.Round(*row.ThrottleRatio*1000) / 10
			dto.ThrottleRatio = &formatted
		}
		result.rows = append(result.rows, dto)
	}
	return result
}

func selectRightsizingWorkloads(workloads []prometheuspkg.RightsizingScanWorkload, input getRightsizingInput) ([]rightsizingWorkloadDTO, rightsizingOmissions, bool, map[prometheuspkg.RightsizingClass]int) {
	counts := map[prometheuspkg.RightsizingClass]int{
		prometheuspkg.ClassReduction: 0,
		prometheuspkg.ClassIncrease:  0,
		prometheuspkg.ClassReview:    0,
		prometheuspkg.ClassNeedData:  0,
		prometheuspkg.ClassInRange:   0,
	}
	ranked := make([]rightsizingWorkloadDTO, 0, len(workloads))
	var omitted rightsizingOmissions
	incompleteEvidence := false
	for _, workload := range workloads {
		filtered := filterRightsizingRows(workload.Rows, input.IncludeAll, input.Classification != "", workload.Replicas, workload.ScaledToZero)
		counts[filtered.classification]++
		incompleteEvidence = incompleteEvidence || filtered.incompleteEvidence
		if input.Classification != "" && string(filtered.classification) != input.Classification {
			continue
		}
		omitted.add(filtered.omitted)
		if len(filtered.rows) == 0 {
			continue
		}
		pointQueryErrorsAtWarnings(filtered.rows)
		impact := filtered.impact
		ranked = append(ranked, rightsizingWorkloadDTO{
			Kind:                     workload.Kind,
			Namespace:                workload.Namespace,
			Name:                     workload.Name,
			Replicas:                 workload.Replicas,
			ScaledToZero:             workload.ScaledToZero,
			ManagedBy:                workload.ManagedBy,
			Classification:           filtered.classification,
			Impact:                   &impact,
			RequestDelta:             &rightsizingRequestDelta{CPU: impact.CPU, Memory: impact.Memory},
			LiveInventoryUnavailable: filtered.liveInventoryUnavailable,
			Rows:                     filtered.rows,
			priority:                 filtered.priority,
			rankKey:                  workload.Namespace + "/" + workload.Kind + "/" + workload.Name,
		})
	}
	// Safety/action priority first, then replica-weighted absolute change — the Rightsizing
	// screen's ordering rule, applied to a different unit: the screen ranks
	// each CONTAINER as its own entry while this tool returns workloads, so
	// two containers' reductions add up here and the top-N can legitimately
	// differ from the screen's. Same rule, coarser grain — not a parity bug.
	sort.SliceStable(ranked, func(i, j int) bool {
		return prometheuspkg.RightsizingRankLess(
			ranked[i].priority, *ranked[i].Impact, ranked[i].rankKey,
			ranked[j].priority, *ranked[j].Impact, ranked[j].rankKey,
		)
	})
	return ranked, omitted, incompleteEvidence, counts
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
		ScannedAt:   time.Now().UTC().Format(time.RFC3339),
		State:       prometheuspkg.RightsizingScanUnavailable,
		Window:      "7d",
		Reason:      reason,
		Remediation: rightsizingRemediation(scope, reason),
		Workloads:   []rightsizingWorkloadDTO{},
		Guidance: rightsizingGuidance(rightsizingGuidanceInput{
			state: prometheuspkg.RightsizingScanUnavailable, scope: scope, reason: reason,
		}),
	}
}

func rightsizingReasonCode(reason string) string {
	switch reason {
	case prometheuspkg.ReasonWorkloadNoContainers:
		return "no_containers"
	case prometheuspkg.ReasonRightsizingQueriesFailed:
		return "queries_failed"
	case prometheuspkg.ReasonNoOwnerSamples:
		return "no_owner_samples"
	case prometheuspkg.ReasonNoUsageSamples:
		return "no_usage_samples"
	case prometheuspkg.ReasonPodInventoryUnreadable:
		return "pod_inventory_unreadable"
	default:
		return reason
	}
}

func rightsizingRemediation(scope, reason string) string {
	// A workload answer is not a scan and carries no coverage block, so the
	// scan's wording would point at fields this response does not have.
	if scope == "workload" && reason == prometheuspkg.ReasonSomeEvidenceUnavailable {
		return "A query behind this workload's rows did not answer — warnings names which one. The rows themselves are complete: read them, but treat whatever that query decides as unknown rather than settled, and re-read the workload once Prometheus answers again."
	}

	switch rightsizingReasonCode(reason) {
	case "pod_inventory_unreadable":
		return "Restore Radar access to list pods for this workload, then retry; current pod OOM status is unknown."
	case "prometheus_unavailable":
		return "No Prometheus found. Radar auto-discovers it, or start radar with --prometheus-url. Recommendations also need kube-state-metrics for 7 days of workload history."
	case "owner_metrics_query_failed", "deployment_owner_metrics_query_failed":
		return "The workload-ownership query failed. Prometheus may be unreachable, unhealthy, or not scraping kube-state-metrics — the warnings carry the query error, which says which."
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
	case reasonOnlyDaemonSetsWithoutNodes:
		return "The scope holds workloads, but every one is a DaemonSet whose node selector matches no node right now, so none was scanned. A node pool scaled to zero may still have history for them: read one with scope=\"workload\", which reports state=unavailable when it does not."
	case "no_workloads":
		return "The scan covered the requested scope and found no Deployment, StatefulSet or DaemonSet in it."
	case prometheuspkg.ReasonSomeEvidenceUnavailable:
		// This reason also fires when nothing failed and the scope was merely
		// narrowed by RBAC or informer coverage, so it must not assert that a
		// query broke. guidance names whichever cause this response carries.
		return "The scan ran but did not cover everything: either some usage, restart or throttle evidence was missing or its query did not answer (the warnings name which), or coverage.restrictedKinds, unavailableKinds or partiallyCachedKinds narrowed what it could read. Treat the affected containers as unjudged rather than correctly sized."
	case "scan_capacity_exceeded":
		return "This scan exceeds the retained-result limit. Select fewer namespaces and scan again."
	case "scan_cancelled":
		return "The scan was stopped. Available recommendations are retained; omit scan_id and set refresh=true to start a new scan."
	case "scan_incomplete":
		return "The scan did not finish every batch within its budget — coverage.workloadsEvaluated reports evaluated workloads; coverage.successfulBatches counts batches whose queries all succeeded, not batches attempted. The returned rows are a subset; narrow with scope=\"namespace\" or target one workload with scope=\"workload\" for a complete answer."
	case "no_containers":
		return "The workload's pod template declares no runtime containers (init-only or an empty spec), so there is nothing to size. This is the workload's own shape, not missing evidence."
	case "queries_failed":
		return "The rightsizing queries failed. Prometheus may be unreachable, unhealthy, or not scraping cAdvisor/kubelet and kube-state-metrics — each row's queryError names which query failed."
	case "no_owner_samples":
		return "Prometheus holds no current or retained kube_pod_owner samples for this workload, so its pods cannot be mapped to it. Install or repair kube-state-metrics and let it scrape."
	case "no_usage_samples":
		return "Prometheus answered but held no workload usage samples for the 7-day window. Check that it is scraping cAdvisor/kubelet metrics and has 7 days of retention."
	case reasonNamespacesExcluded:
		return "Some requested namespaces were not scanned. excludedNamespaces names each one: access_denied means this identity cannot list workloads there; outside_namespace_scope means radar is pinned with --namespace-scope; not_cached means radar's informer cache does not hold workloads for that namespace (see coverage.partiallyCachedKinds), which is a limit of radar's own access, not this identity's. The rows cover effectiveNamespaces only."
	case reasonNamespaceScopeLimited:
		if pinned, ok := NamespacePinned(); ok {
			return fmt.Sprintf("The scan succeeded but reached only namespace %s, which radar is pinned to with --namespace-scope. Report it as that scope; this identity's permissions are not the limit.", pinned)
		}
		return "The scan succeeded but reached only the namespaces in effectiveNamespaces — scope was resolved from this identity's per-namespace access rather than a cluster-wide grant, so namespaces outside that list were not scanned."
	case reasonNamespaceAccessDenied:
		return "This identity cannot list workloads in the requested scope."
	case ReasonOutsideNamespaceScope:
		// The pinned namespace is not named: this caller was denied, and may have
		// no access to it either.
		return "radar is pinned to a single namespace with --namespace-scope, so the requested scope is outside what it can see. This is a startup flag, not a permissions problem — restart radar without --namespace-scope to scan cluster-wide."
	case prometheuspkg.ReasonScanDeadlineExceeded:
		return "The scan ran out of its three-minute budget before finishing. Narrow it with scope=\"namespace\", or target one workload with scope=\"workload\"."
	default:
		return ""
	}
}

// rightsizingGuidanceInput is what the response actually contains, so the
// guidance can name the causes that are present instead of a fixed paragraph
// pointing at coverage fields that may all be empty.
type rightsizingGuidanceInput struct {
	state               prometheuspkg.RightsizingScanState
	includeAll          bool
	scope               string
	reason              string
	effectiveNamespaces []string
	coverage            *prometheuspkg.RightsizingScanCoverage
	omitted             rightsizingOmissions
	reductionLimited    bool
	needsManualReview   bool
	// deadlineExceeded marks a scan the budget cut, before a batch or inside
	// one. Evaluated below discovered does not: dropped Deployments do that too.
	deadlineExceeded bool
	// batchQueryFailed names batch failures a deadline would otherwise hide,
	// since unrun batches also count short of CompletedBatches.
	batchQueryFailed bool

	returnedQueryErrors        int
	truncatedQueryErrors       int
	returnedShortHistory       int
	truncatedShortHistory      int
	excludedNamespaces         []excludedNamespace
	namespacesWithoutWorkloads []string
	workloadsShown             bool
}

func rightsizingGuidance(in rightsizingGuidanceInput) []string {
	parts := []string{
		"Recommendations come from 7 days of observed usage, not live metrics. Check confidence and sampleCoverage before acting: low confidence means short or sparse history, not correctly sized.",
	}

	// An unavailable response has no rows, no omitted counts and no coverage to
	// read; telling the agent which fields explain them describes a response it
	// did not receive.
	if in.state == prometheuspkg.RightsizingScanUnavailable {
		parts = append(parts, "State is unavailable — no recommendation was produced. Read reason and remediation; do not report this as a finding that the workloads are correctly sized.")
		return parts
	}

	if in.state == prometheuspkg.RightsizingScanPartial {
		parts = append(parts, partialGuidance(in)...)
	}
	if len(in.effectiveNamespaces) > 0 {
		parts = append(parts, fmt.Sprintf("This scan reached only the %d namespace(s) named in effectiveNamespaces, not the whole cluster — report it as that scope, never as cluster-wide.", len(in.effectiveNamespaces)))
	}
	if len(in.excludedNamespaces) > 0 {
		names := make([]string, 0, len(in.excludedNamespaces))
		for _, excluded := range in.excludedNamespaces {
			names = append(names, fmt.Sprintf("%s (%s)", excluded.Name, excluded.Reason))
		}
		parts = append(parts, fmt.Sprintf("Requested namespaces not scanned: %s. Do not report them as having nothing to change.", strings.Join(names, ", ")))
	}
	if len(in.namespacesWithoutWorkloads) > 0 {
		parts = append(parts, fmt.Sprintf("%s had no workload to scan — check the name before reporting it as empty.", strings.Join(in.namespacesWithoutWorkloads, ", ")))
	}
	if in.coverage != nil && in.coverage.DaemonSetsWithoutNodes > 0 {
		parts = append(parts, fmt.Sprintf(`%d DaemonSet(s) match no node right now, so they run no pods and were not scanned (coverage.daemonSetsWithoutNodes, named in skippedDaemonSets). A node pool scaled to zero may still have history for them: read one with scope="workload", which reports state=unavailable when it does not.`, in.coverage.DaemonSetsWithoutNodes))
	}
	if in.returnedQueryErrors > 0 && in.scope != "workload" {
		parts = append(parts, "A workload whose rows carry queryError can still hold a valid recommendation on its other resource: its classification retains known increase or review signals while state=partial reports the missing evidence.")
	}
	if in.reductionLimited {
		parts = append(parts, "Rows with reductionLimited=true were clamped: recommendedRequest is a bounded step, not the full cut: at most half for memory, for CPU of 1 core or more, and for bursty or throttled CPU; up to three quarters for smaller CPU requests. demandTarget is the demand-based end state, not a value to apply: apply recommendedRequest, observe a full 7-day window, then re-check. For memory, demandTarget comes from the 7-day max, so a monthly or batch peak outside the window is not seen and jumping straight to it risks OOM.")
	}
	if in.needsManualReview {
		parts = append(parts, "Rows with needsManualReview=true are excluded from the workload's requestDelta, and reviewReasons says why — an autoscaler owns the request, the container has OOM history, its limit conflicts with the recommendation, or the cut runs against bursty or throttled usage. Most of those rows carry no recommendedRequest at all, because the same signal withheld it; where one is present, weigh it against the reason instead of applying it unattended. The flag marks a row that needs judgement, not one that must never change: raising a request on a container with OOM history is often exactly what the evidence asks for.")
	}
	if in.workloadsShown {
		parts = append(parts, "requestDelta is replica-weighted request capacity, not bill savings. An absent cpu or memory delta means zero net change or no eligible evidence; read rows to distinguish them. Rows lacking evidence or needing manual review are excluded. If liveInventoryUnavailable=true, currentPodOOM being absent does not establish that no pod has OOMed.")
		parts = append(parts, "managedBy names what owns a workload's spec: change requests at that source (Git, chart values, the controller's resource, the add-on configuration), not with a direct patch. A missing managedBy does not mean unmanaged — Argo CD label tracking, Terraform and kubectl apply leave no signal Radar reads.")
	}
	if in.scope != "workload" {
		parts = append(parts, "classificationCounts counts all evaluated workloads before classification filtering, row omissions and limit, including those with missing evidence; it excludes workloads not evaluated. Use classification=increase or classification=review to inspect those classes if absent from the returned list. totalWorkloads counts workloads matching the selection before limit. evaluatedAt is evaluation time, not source freshness.")
	}
	if !in.includeAll && in.scope != "workload" {
		parts = append(parts, "Correctly-sized and unevidenced containers are omitted — see the omitted counts; pass include_all=true to see those rows. Rows carrying OOM history, a limit conflict, throttling of 10% or more, or an autoscaler are always returned regardless of fit, as is every row of a scaledToZero workload and any unevidenced row of a container that is returned.")
	}
	return parts
}

// partialGuidance names only the causes this response carries: the common
// causes are row-level evidence and a narrowed namespace scope, not kinds.
func partialGuidance(in rightsizingGuidanceInput) []string {
	if in.scope == "workload" {
		if in.reason == prometheuspkg.ReasonSomeEvidenceUnavailable {
			return []string{"State is partial because a supporting query did not answer, not because a row is missing evidence — warnings names which one. The rows are intact and their recommendations stand, but a signal that would have qualified them is absent: a lost throttle or peak reading leaves needsManualReview clear and the clamp looser than it would otherwise have been."}
		}
		return []string{"State is partial — some of this workload's containers had no usable evidence. Do not treat their rows as a verdict that the container is correctly sized."}
	}

	var causes []string
	if cov := in.coverage; cov != nil {
		// restrictedKinds/unavailableKinds name kinds, not workloads: a
		// restricted kind can still have returned workloads from the
		// namespaces the caller could read.
		if len(cov.RestrictedKinds) > 0 {
			causes = append(causes, fmt.Sprintf("coverage.restrictedKinds (%s) could not be listed by this identity across the whole requested scope", strings.Join(cov.RestrictedKinds, ", ")))
		}
		if len(cov.UnavailableKinds) > 0 {
			causes = append(causes, fmt.Sprintf("coverage.unavailableKinds (%s) had no readable informer cache, or (for Deployments) no ReplicaSet ownership metrics", strings.Join(cov.UnavailableKinds, ", ")))
		}
		if len(cov.PartiallyCachedKinds) > 0 {
			causes = append(causes, fmt.Sprintf("coverage.partiallyCachedKinds (%s) are cached for only some namespaces, so those kinds were read in a narrower scope than requested", strings.Join(cov.PartiallyCachedKinds, ", ")))
		}
		// CompletedBatches counts batches whose queries all answered, not
		// batches that ran, so a short count alone never means the scan stopped:
		// only the deadline warning does, and then unrun batches are not failures.
		if in.deadlineExceeded {
			// A cut inside the last batch still counts every workload evaluated,
			// so the count alone cannot say the scan stopped early.
			causes = append(causes, fmt.Sprintf("the scan ran out of its budget before its queries finished (%d of %d workloads evaluated; rows in a batch the deadline cut lack evidence)", cov.WorkloadsEvaluated, cov.WorkloadsDiscovered))
			if in.batchQueryFailed {
				causes = append(causes, "some batches that did run had a failed query, so some rows lack evidence (the warnings name which)")
			}
		} else if cov.Batches > 0 && cov.CompletedBatches < cov.Batches {
			causes = append(causes, fmt.Sprintf("%d of %d query batches had a failure, so some rows lack evidence", cov.Batches-cov.CompletedBatches, cov.Batches))
		}
	}
	if in.omitted.InsufficientHistory > 0 {
		causes = append(causes, fmt.Sprintf("%d row(s) had too little history to judge (omitted.insufficientHistory)", in.omitted.InsufficientHistory))
	}
	if in.returnedShortHistory > 0 {
		causes = append(causes, fmt.Sprintf("%d returned row(s) had too little history to judge (fit insufficient_history)", in.returnedShortHistory))
	}
	if in.truncatedShortHistory > 0 {
		causes = append(causes, fmt.Sprintf("%d row(s) with too little history sit on workloads past the limit", in.truncatedShortHistory))
	}
	if failed := in.omitted.QueryError + in.returnedQueryErrors + in.truncatedQueryErrors; failed > 0 {
		if in.returnedQueryErrors == 0 && in.truncatedQueryErrors == 0 {
			causes = append(causes, fmt.Sprintf("%d row(s) failed their usage query (omitted.queryError)", failed))
		} else {
			var split []string
			for _, part := range []struct {
				count int
				where string
			}{
				{in.omitted.QueryError, "in omitted.queryError"},
				{in.returnedQueryErrors, "returned with queryError"},
				{in.truncatedQueryErrors, "on workloads past the limit"},
			} {
				if part.count > 0 {
					split = append(split, fmt.Sprintf("%d %s", part.count, part.where))
				}
			}
			causes = append(causes, fmt.Sprintf("%d row(s) failed their usage query (%s)", failed, strings.Join(split, ", ")))
		}
	}
	if in.reason == reasonRowEvidenceIncomplete {
		// This reason also covers short history, which is counted above, so the
		// sentence must not assert that a withheld recommendation exists.
		causes = append(causes, "any row whose recommendationReason names missing HPA or OOM evidence had its recommendation withheld")
	}

	if len(causes) == 0 {
		// A scope this narrow is not missing evidence: the scan finished, it
		// simply covered less than was asked for, and the sentences naming the
		// scope follow this one. Calling that missing evidence would have the
		// agent distrust recommendations that are complete for what they cover.
		if scopeOnlyPartial(in.reason) {
			return nil
		}
		return []string{fmt.Sprintf("State is partial (reason %q) — some evidence is missing. Do not describe partial rows as cluster-wide.", in.reason)}
	}
	return []string{fmt.Sprintf("State is partial because %s. Do not describe partial rows as cluster-wide.", joinCauses(causes))}
}

// scopeOnlyPartial marks the reasons that narrow what a scan covered without
// leaving any gap in what it read.
func scopeOnlyPartial(reason string) bool {
	return reason == reasonNamespaceScopeLimited || reason == reasonNamespacesExcluded
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
