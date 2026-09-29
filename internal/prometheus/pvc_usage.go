package prometheus

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

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
}

// Claims per query: keeps the regex matcher and the answer small whatever the
// namespace holds.
const pvcUsageBatchSize = 100

// QueryPVCUsage reads kubelet_volume_stats_{used,capacity}_bytes for the named
// claims of one namespace, two instant queries per batch. Authorization is the
// caller's: this reads nothing the caller did not name.
func QueryPVCUsage(ctx context.Context, namespace string, claims []string) PVCUsageBatch {
	out := PVCUsageBatch{Status: PVCUsageAvailable, Usage: map[string]PVCUsage{}, Invalid: map[string]bool{}}
	client := GetClient()
	if client == nil {
		out.Status = PVCUsageNoPrometheus
		return out
	}
	if _, _, err := client.EnsureConnected(ctx); err != nil {
		out.Status, out.Error = PVCUsageNoPrometheus, err.Error()
		return out
	}
	names := append([]string(nil), claims...)
	sort.Strings(names)
	for start := 0; start < len(names); start += pvcUsageBatchSize {
		end := min(start+pvcUsageBatchSize, len(names))
		if err := queryPVCUsageBatch(ctx, client, namespace, names[start:end], &out); err != nil {
			errorlog.Record("prometheus", "warning", "pvc usage query failed for namespace %s: %v", namespace, err)
			return PVCUsageBatch{Status: PVCUsageQueryFailed, Error: err.Error(), Usage: map[string]PVCUsage{}, Invalid: map[string]bool{}}
		}
	}
	return out
}

func queryPVCUsageBatch(ctx context.Context, client *Client, namespace string, claims []string, out *PVCUsageBatch) error {
	escaped := make([]string, len(claims))
	for i, c := range claims {
		escaped[i] = prom.EscapeRegexMeta(prom.SanitizeLabelValue(c))
	}
	selector := fmt.Sprintf(`namespace='%s',persistentvolumeclaim=~'%s'`, prom.SanitizeLabelValue(namespace), strings.Join(escaped, "|"))
	used, err := client.Query(ctx, fmt.Sprintf(`max by (persistentvolumeclaim) (kubelet_volume_stats_used_bytes{%s})`, selector))
	if err != nil {
		return err
	}
	capacity, err := client.Query(ctx, fmt.Sprintf(`max by (persistentvolumeclaim) (kubelet_volume_stats_capacity_bytes{%s})`, selector))
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
