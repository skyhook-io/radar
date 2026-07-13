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
	for _, namespace := range sortedNamespaceKeys(byNamespace) {
		refs := byNamespace[namespace]
		result, reason := queryApplicationTrendNamespace(ctx, client, namespace, refs, start, end, step)
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

func queryApplicationTrendNamespace(ctx context.Context, client *prom.Client, namespace string, refs []ApplicationWorkloadRef, start, end time.Time, step time.Duration) (*prom.QueryResult, string) {
	query := buildApplicationTrendQuery(namespace, refs, false)
	if query == "" {
		return nil, ReasonNoMetrics
	}
	result, err := client.QueryRange(ctx, query, start, end, step)
	if err != nil {
		log.Print("[opencost] app workload trend range query failed; trying opencost_container_* fallback")
		result, err = client.QueryRange(ctx, buildApplicationTrendQuery(namespace, refs, true), start, end, step)
		if err != nil {
			log.Print("[opencost] app workload trend fallback query failed")
			return nil, ReasonQueryError
		}
	}
	if result == nil || len(result.Series) == 0 {
		return nil, ReasonNoMetrics
	}
	return result, ""
}

func buildApplicationTrendQuery(namespace string, refs []ApplicationWorkloadRef, fallback bool) string {
	safeNS := prom.SanitizeLabelValue(namespace)
	podCost := workloadPodCostExpr(safeNS, fallback)
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
      label_replace(kube_pod_owner{namespace="%s", owner_kind="ReplicaSet", owner_is_controller="true"}, "replicaset", "$1", "owner_name", "(.+)")
    )
  * on(namespace, replicaset) group_left(owner_kind, owner_name)
    max by (namespace, replicaset, owner_kind, owner_name) (
      kube_replicaset_owner{namespace="%s", owner_kind="Deployment", owner_name=~"%s", owner_is_controller="true"}
    )
)`, podCost, safeNS, safeNS, promRegexAlternation(names)))
	}
	for _, kind := range []string{"StatefulSet", "DaemonSet"} {
		if names := namesByKind[kind]; len(names) > 0 {
			exprs = append(exprs, fmt.Sprintf(`(
  (%s)
  * on(namespace, pod) group_left(owner_kind, owner_name)
    max by (namespace, pod, owner_kind, owner_name) (
      kube_pod_owner{namespace="%s", owner_kind="%s", owner_name=~"%s", owner_is_controller="true"}
    )
)`, podCost, safeNS, kind, promRegexAlternation(names)))
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
