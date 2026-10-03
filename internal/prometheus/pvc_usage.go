package prometheus

import (
	"context"
	"errors"
	"fmt"
	"math"

	"github.com/skyhook-io/radar/internal/errorlog"
	"github.com/skyhook-io/radar/pkg/prom"
)

// PVCUsage is one claim's filesystem use as kubelet reports it.
type PVCUsage struct {
	UsedBytes     int64
	CapacityBytes int64
	Ratio         float64
}

const (
	PVCUsageAvailable    = "available"
	PVCUsageNoPrometheus = "no_prometheus"
	PVCUsageQueryFailed  = "query_failed"
	// The claims' series carry more than one cluster identity, or the proven
	// identity is absent from them: a merged value could be another cluster's.
	PVCUsageAmbiguous     = "ambiguous_scope"
	PVCUsageScopeMismatch = "scope_mismatch"
)

// PVCUsageBatch answers for a set of claims in one namespace. Status describes
// the query as a whole; a claim missing from both Usage and Invalid had no
// series, which does not establish whether its driver reports volume stats or
// whether kubelet is scraped at all.
type PVCUsageBatch struct {
	Status  string
	Error   string
	Usage   map[string]PVCUsage
	Invalid map[string]bool
	// Isolation says how the series were tied to this cluster.
	Isolation CNPGIsolation
}

// Claims per query: keeps the regex matcher and the answer small whatever the
// namespace holds.
const pvcUsageBatchSize = 100

// QueryPVCUsage reads kubelet_volume_stats_{used,capacity}_bytes for the named
// claims of one namespace, held to this cluster's identity (anchors are the
// Pods that prove it). Authorization is the caller's: this reads nothing the
// caller did not name.
func QueryPVCUsage(ctx context.Context, namespace string, claims []string, anchors []prom.WorkloadPodIdentity) PVCUsageBatch {
	client := GetClient()
	if client == nil {
		return PVCUsageBatch{Status: PVCUsageNoPrometheus, Usage: map[string]PVCUsage{}, Invalid: map[string]bool{}}
	}
	if _, _, err := client.EnsureConnected(ctx); err != nil {
		return PVCUsageBatch{Status: PVCUsageNoPrometheus, Error: err.Error(), Usage: map[string]PVCUsage{}, Invalid: map[string]bool{}}
	}
	matchers, iso, err := resolveScope(ctx, namespace, pvcScopeProbe(namespace, claims, 0), anchors, nil)
	if out, failed := pvcScopeFailure(err); failed {
		if out.Status == PVCUsageQueryFailed {
			errorlog.Record("prometheus", "warning", "pvc usage scope check failed for namespace %s: %v", namespace, err)
		}
		return out
	}
	out := queryPVCUsage(ctx, client, namespace, claims, matchers)
	out.Isolation = iso.forClaims()
	if out.Status == PVCUsageQueryFailed {
		errorlog.Record("prometheus", "warning", "pvc usage query failed for namespace %s: %s", namespace, out.Error)
	}
	return out
}

func pvcScopeFailure(err error) (PVCUsageBatch, bool) {
	out := PVCUsageBatch{Usage: map[string]PVCUsage{}, Invalid: map[string]bool{}}
	switch {
	case err == nil:
		return out, false
	case errors.Is(err, ErrCNPGScopeAmbiguous):
		out.Status, out.Error = PVCUsageAmbiguous, "these claim names have volume stats under more than one cluster identity in this Prometheus"
	case errors.Is(err, ErrCNPGScopeMismatch):
		out.Status, out.Error = PVCUsageScopeMismatch, "the cluster identity proven for this cluster does not appear on these claims' volume stats"
	default:
		out.Status, out.Error = PVCUsageQueryFailed, err.Error()
	}
	return out, true
}

func queryPVCUsage(ctx context.Context, q cnpgQuerier, namespace string, claims []string, matchers string) PVCUsageBatch {
	out := PVCUsageBatch{Status: PVCUsageAvailable, Usage: map[string]PVCUsage{}, Invalid: map[string]bool{}}
	for _, sel := range CNPGClaimSelectors(namespace, claims) {
		if err := queryPVCUsageBatch(ctx, q, withScope(sel, matchers), &out); err != nil {
			return PVCUsageBatch{Status: PVCUsageQueryFailed, Error: err.Error(), Usage: map[string]PVCUsage{}, Invalid: map[string]bool{}}
		}
	}
	return out
}

func queryPVCUsageBatch(ctx context.Context, q cnpgQuerier, selector string, out *PVCUsageBatch) error {
	used, err := q.Query(ctx, fmt.Sprintf(`max by (persistentvolumeclaim) (kubelet_volume_stats_used_bytes{%s})`, selector))
	if err != nil {
		return err
	}
	capacity, err := q.Query(ctx, fmt.Sprintf(`max by (persistentvolumeclaim) (kubelet_volume_stats_capacity_bytes{%s})`, selector))
	if err != nil {
		return err
	}
	usedBy, capBy := valuesByClaim(used), valuesByClaim(capacity)
	for claim, u := range usedBy {
		c, ok := capBy[claim]
		if !ok {
			continue
		}
		usage, valid := pvcUsageOf(u, c)
		if !valid {
			out.Invalid[claim] = true
			continue
		}
		out.Usage[claim] = usage
	}
	return nil
}

func valuesByClaim(res *prom.QueryResult) map[string]float64 {
	out := map[string]float64{}
	if res == nil {
		return out
	}
	for _, s := range res.Series {
		claim := s.Labels["persistentvolumeclaim"]
		if claim == "" || len(s.DataPoints) == 0 {
			continue
		}
		out[claim] = s.DataPoints[0].Value
	}
	return out
}

// pvcUsageOf applies the same validity rules as the single-claim endpoint:
// NaN, infinities, negative use, a capacity under one byte or values past
// int64 are not measurements.
func pvcUsageOf(used, capacity float64) (PVCUsage, bool) {
	if used != used || capacity != capacity || math.IsInf(used, 0) || math.IsInf(capacity, 0) ||
		used < 0 || capacity < 1 || used >= math.Exp2(63) || capacity >= math.Exp2(63) {
		return PVCUsage{}, false
	}
	return PVCUsage{UsedBytes: int64(used), CapacityBytes: int64(capacity), Ratio: used / capacity}, true
}
