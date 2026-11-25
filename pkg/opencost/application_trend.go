package opencost

import (
	"context"
	"fmt"
	"log"
	"sort"
	"strings"
	"time"

	"github.com/skyhook-io/radar/pkg/prom"
)

type ApplicationTrendOptions struct {
	Range             string
	Workloads         []ApplicationWorkloadRef
	Unavailable       []ApplicationWorkloadStatus
	UnavailableReason string
	// ScopeFor decides one namespace's scope; a non-empty reason marks that
	// namespace's workloads unavailable with it and sends no query. Nil
	// leaves every query as it always was.
	ScopeFor func(namespace string) (StoreScope, string)
}

func ComputeApplicationCostTrendFromProm(ctx context.Context, client *prom.Client, opts ApplicationTrendOptions) *ApplicationCostTrendResponse {
	start, end, step, label := resolveTrendRange(opts.Range)
	supported, unsupported := splitApplicationTrendRefs(opts.Workloads)
	resp := &ApplicationCostTrendResponse{
		Range: label,
		Coverage: ApplicationCostCoverage{
			Total:       len(supported) + len(opts.Unavailable) + len(unsupported),
			Unavailable: append([]ApplicationWorkloadStatus(nil), opts.Unavailable...),
			Unsupported: unsupported,
		},
	}
	if client == nil {
		reason := opts.UnavailableReason
		if reason == "" {
			reason = ReasonNoPrometheus
		}
		resp.Available = false
		resp.Reason = reason
		resp.Coverage.Unavailable = append(resp.Coverage.Unavailable, applicationStatusesForRefs(supported, reason)...)
		sortApplicationStatuses(resp.Coverage.Unavailable)
		resp.Partial = len(resp.Coverage.Unsupported) > 0 || len(opts.Unavailable) > 0
		return resp
	}
	if len(supported) == 0 {
		resp.Available = false
		resp.Reason = applicationUnavailableReason(resp.Coverage.Unavailable)
		resp.Partial = len(resp.Coverage.Unsupported) > 0 || len(resp.Coverage.Unavailable) > 0
		return resp
	}

	byNamespace := groupApplicationRefsByNamespace(supported)
	withheld := false
	for _, namespace := range sortedNamespaceKeys(byNamespace) {
		refs := byNamespace[namespace]
		var scope StoreScope
		reason := ""
		if opts.ScopeFor != nil {
			scope, reason = opts.ScopeFor(namespace)
			// A namespace withheld for cluster scope has the scope worth
			// showing; any other reason (a failed check) carries none.
			scopeWithheld := reason == ReasonClusterScope && scope.ClusterScope != nil
			if resp.ClusterScope == nil || (scopeWithheld && !withheld) {
				resp.ClusterScope = scope.ClusterScope
			}
			withheld = withheld || scopeWithheld
		}
		var result *prom.QueryResult
		if reason == "" {
			result, reason = queryApplicationTrendNamespace(ctx, client, scope, namespace, refs, start, end, step)
		}
		if reason != "" {
			resp.Coverage.Unavailable = append(resp.Coverage.Unavailable, applicationStatusesForRefs(refs, reason)...)
			continue
		}

		seen := make(map[string]bool, len(refs))
		requested := make(map[string]ApplicationWorkloadRef, len(refs))
		for _, ref := range refs {
			requested[applicationRefSortKey(ref)] = ref
		}
		for _, series := range result.Series {
			kind := series.Labels["owner_kind"]
			name := series.Labels["owner_name"]
			ref := ApplicationWorkloadRef{Namespace: namespace, Kind: kind, Name: name}
			key := applicationRefSortKey(ref)
			canonicalRef, ok := requested[key]
			if !ok || len(series.DataPoints) == 0 {
				continue
			}
			points := roundedCostDataPoints(series.DataPoints)
			resp.Series = append(resp.Series, ApplicationCostTrendSeries{
				ApplicationWorkloadRef: canonicalRef,
				WindowTotalCost:        roundTo(integrateHourlyCost(points), 4),
				DataPoints:             points,
			})
			seen[key] = true
		}
		for _, ref := range refs {
			if !seen[applicationRefSortKey(ref)] {
				resp.Coverage.Unavailable = append(resp.Coverage.Unavailable, ApplicationWorkloadStatus{
					ApplicationWorkloadRef: ref,
					Reason:                 ReasonNoMetrics,
				})
			}
		}
	}

	sort.Slice(resp.Series, func(i, j int) bool {
		if resp.Series[i].WindowTotalCost != resp.Series[j].WindowTotalCost {
			return resp.Series[i].WindowTotalCost > resp.Series[j].WindowTotalCost
		}
		return applicationRefSortKey(resp.Series[i].ApplicationWorkloadRef) < applicationRefSortKey(resp.Series[j].ApplicationWorkloadRef)
	})
	sortApplicationRefs(resp.Coverage.Unsupported)
	sortApplicationStatuses(resp.Coverage.Unavailable)
	resp.Coverage.Included = len(resp.Series)
	resp.Partial = len(resp.Coverage.Unsupported) > 0 || len(resp.Coverage.Unavailable) > 0
	resp.Available = resp.Coverage.Included > 0
	if resp.Available {
		resp.DataPoints = mergeApplicationCostDataPoints(resp.Series)
		resp.WindowTotalCost = roundTo(integrateHourlyCost(resp.DataPoints), 4)
		return resp
	}
	resp.Reason = applicationUnavailableReason(resp.Coverage.Unavailable)
	return resp
}

func queryApplicationTrendNamespace(ctx context.Context, client *prom.Client, scope StoreScope, namespace string, refs []ApplicationWorkloadRef, start, end time.Time, step time.Duration) (*prom.QueryResult, string) {
	f := scope.Filter
	query := buildApplicationTrendQuery(f, namespace, refs, false)
	if query == "" {
		return nil, ReasonNoMetrics
	}
	q := &costQuerier{client: client}
	result, err := q.QueryRange(ctx, query, start, end, step)
	if err != nil {
		log.Print("[opencost] app workload trend range query failed; trying opencost_container_* fallback")
		result, err = q.QueryRange(ctx, buildApplicationTrendQuery(f, namespace, refs, true), start, end, step)
		if err != nil {
			log.Print("[opencost] app workload trend fallback query failed")
			return nil, ReasonQueryError
		}
	}
	if scope.partial(q) {
		return nil, ReasonPartialData
	}
	if result == nil || len(result.Series) == 0 {
		return nil, ReasonNoMetrics
	}
	return result, ""
}

func buildApplicationTrendQuery(f prom.ClusterFilter, namespace string, refs []ApplicationWorkloadRef, fallback bool) string {
	safeNS := prom.SanitizeLabelValue(namespace)
	podCost := workloadPodCostExpr(f, safeNS, fallback)
	namesByKind := make(map[string][]string)
	for _, ref := range refs {
		namesByKind[ref.Kind] = append(namesByKind[ref.Kind], ref.Name)
	}

	exprs := make([]string, 0, 3)
	if names := namesByKind["Deployment"]; len(names) > 0 {
		exprs = append(exprs, fmt.Sprintf(`(
  (%s)
  * on(namespace, pod) group_left(replicaset)
    max by (namespace, pod, replicaset) (
      label_replace(%s, "replicaset", "$1", "owner_name", "(.+)")
    )
  * on(namespace, replicaset) group_left(owner_kind, owner_name)
    max by (namespace, replicaset, owner_kind, owner_name) (
      %s
    )
)`, podCost,
			sel(f, MetricPodOwner, `namespace="`+safeNS+`", owner_kind="ReplicaSet", owner_is_controller="true"`),
			sel(f, MetricReplicaSetOwner, `namespace="`+safeNS+`", owner_kind="Deployment", owner_name=~"`+promRegexAlternation(names)+`", owner_is_controller="true"`)))
	}
	for _, kind := range []string{"StatefulSet", "DaemonSet"} {
		if names := namesByKind[kind]; len(names) > 0 {
			exprs = append(exprs, fmt.Sprintf(`(
  (%s)
  * on(namespace, pod) group_left(owner_kind, owner_name)
    max by (namespace, pod, owner_kind, owner_name) (
      %s
    )
)`, podCost, sel(f, MetricPodOwner, `namespace="`+safeNS+`", owner_kind="`+kind+`", owner_name=~"`+promRegexAlternation(names)+`", owner_is_controller="true"`)))
		}
	}
	if len(exprs) == 0 {
		return ""
	}
	return "sum by (owner_kind, owner_name) (\n" + strings.Join(exprs, "\n or \n") + "\n)"
}

func splitApplicationTrendRefs(refs []ApplicationWorkloadRef) ([]ApplicationWorkloadRef, []ApplicationWorkloadRef) {
	supported := make([]ApplicationWorkloadRef, 0, len(refs))
	unsupported := make([]ApplicationWorkloadRef, 0)
	seen := make(map[string]bool, len(refs))
	for _, ref := range refs {
		if ref.Namespace == "" || ref.Name == "" {
			continue
		}
		kind, ok := CanonicalWorkloadKind(ref.Kind)
		normalized := ApplicationWorkloadRef{Kind: kind, Namespace: ref.Namespace, Name: ref.Name}
		if !ok {
			normalized.Kind = ref.Kind
			key := applicationRefSortKey(normalized)
			if !seen[key] {
				unsupported = append(unsupported, normalized)
				seen[key] = true
			}
			continue
		}
		key := applicationRefSortKey(normalized)
		if seen[key] {
			continue
		}
		supported = append(supported, normalized)
		seen[key] = true
	}
	sortApplicationRefs(supported)
	sortApplicationRefs(unsupported)
	return supported, unsupported
}

func groupApplicationRefsByNamespace(refs []ApplicationWorkloadRef) map[string][]ApplicationWorkloadRef {
	out := make(map[string][]ApplicationWorkloadRef)
	for _, ref := range refs {
		out[ref.Namespace] = append(out[ref.Namespace], ref)
	}
	for ns := range out {
		sortApplicationRefs(out[ns])
	}
	return out
}

func sortedNamespaceKeys(refs map[string][]ApplicationWorkloadRef) []string {
	namespaces := make([]string, 0, len(refs))
	for namespace := range refs {
		namespaces = append(namespaces, namespace)
	}
	sort.Strings(namespaces)
	return namespaces
}

func applicationStatusesForRefs(refs []ApplicationWorkloadRef, reason string) []ApplicationWorkloadStatus {
	statuses := make([]ApplicationWorkloadStatus, 0, len(refs))
	for _, ref := range refs {
		statuses = append(statuses, ApplicationWorkloadStatus{
			ApplicationWorkloadRef: ref,
			Reason:                 reason,
		})
	}
	sortApplicationStatuses(statuses)
	return statuses
}

func roundedCostDataPoints(points []prom.DataPoint) []CostDataPoint {
	out := make([]CostDataPoint, 0, len(points))
	for _, point := range points {
		out = append(out, CostDataPoint{
			Timestamp: point.Timestamp,
			Value:     roundTo(point.Value, 4),
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Timestamp < out[j].Timestamp })
	return out
}

func mergeApplicationCostDataPoints(series []ApplicationCostTrendSeries) []CostDataPoint {
	byTS := make(map[int64]float64)
	for _, s := range series {
		for _, dp := range s.DataPoints {
			byTS[dp.Timestamp] += dp.Value
		}
	}
	points := make([]CostDataPoint, 0, len(byTS))
	for ts, value := range byTS {
		points = append(points, CostDataPoint{Timestamp: ts, Value: roundTo(value, 4)})
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Timestamp < points[j].Timestamp })
	return points
}

func promRegexAlternation(values []string) string {
	copied := append([]string(nil), values...)
	sort.Strings(copied)
	unique := copied[:0]
	var prev string
	for i, value := range copied {
		if i > 0 && value == prev {
			continue
		}
		unique = append(unique, prom.EscapeRegexMeta(prom.SanitizeLabelValue(value)))
		prev = value
	}
	return strings.Join(unique, "|")
}
