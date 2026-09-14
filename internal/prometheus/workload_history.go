package prometheus

import (
	"context"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/prom"
	"k8s.io/apimachinery/pkg/labels"
)

const historyScopeRemedy = "Historical cluster scope could not be verified. For a backend dedicated to this cluster, use --prometheus-single-cluster; for a shared backend, use --prometheus-cluster-label with its exact cluster label."

type workloadPartitionMemo struct {
	generation uint64
	expires    time.Time
	scope      prom.WorkloadMetricsScope
}

type workloadHistoryPlan struct {
	history *prom.WorkloadHistory
	reason  string
	err     error
}

func (c *Client) historicalClusterScope(ctx context.Context, scope PodScope, cache *k8s.ResourceCache) (prom.WorkloadMetricsScope, error) {
	if config, _, configured := c.workloadMetricsConfig(); configured {
		return config, nil
	}
	c.mu.RLock()
	generation := c.discoveryGen
	if memo := c.workloadPartition; memo != nil && memo.generation == generation && time.Now().Before(memo.expires) && !c.retired {
		config := memo.scope
		c.mu.RUnlock()
		return config, nil
	}
	c.mu.RUnlock()
	anchors := append([]prom.WorkloadPodIdentity(nil), scope.Identities...)
	if len(anchors) == 0 && cache != nil && cache.Pods() != nil {
		pods, err := cache.Pods().Pods(scope.Namespace).List(labels.Everything())
		if err != nil {
			return prom.WorkloadMetricsScope{}, err
		}
		sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
		for _, pod := range pods {
			if len(anchors) == 100 {
				break
			}
			anchors = append(anchors, prom.WorkloadPodIdentity{Name: pod.Name, UID: string(pod.UID)})
		}
	}
	if len(anchors) == 0 {
		return prom.WorkloadMetricsScope{}, nil
	}
	// One proof flight per connection; waiters never gain access to its anchor namespace.
	ch := c.workloadPartitionSF.DoChan(strconv.FormatUint(generation, 10), func() (any, error) {
		probeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 8*time.Second)
		defer cancel()
		if _, _, err := c.EnsureConnected(probeCtx); err != nil {
			return nil, err
		}
		c.mu.RLock()
		if c.discoveryGen != generation || c.retired || c.baseURL == "" {
			c.mu.RUnlock()
			return nil, fmt.Errorf("metrics connection changed")
		}
		tr := prom.NewHTTPTransport(c.baseURL, c.basePath, c.httpClient)
		tr.Headers, tr.MaxResponseBytes = copyHeaders(c.headers), 4<<20
		c.mu.RUnlock()
		config, err := probeHistoricalPartition(probeCtx, prom.NewClient(tr), scope.Namespace, anchors)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.discoveryGen != generation || c.retired {
			return nil, fmt.Errorf("metrics connection changed")
		}
		if len(config.ClusterLabels) > 0 {
			c.workloadPartition = &workloadPartitionMemo{generation: generation, expires: time.Now().Add(5 * time.Minute), scope: config}
		}
		return config, nil
	})
	select {
	case <-ctx.Done():
		return prom.WorkloadMetricsScope{}, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return prom.WorkloadMetricsScope{}, result.Err
		}
		return result.Val.(prom.WorkloadMetricsScope), nil
	}
}

func probeHistoricalPartition(ctx context.Context, client *prom.Client, namespace string, anchors []prom.WorkloadPodIdentity) (prom.WorkloadMetricsScope, error) {
	names := make([]string, len(anchors))
	for i, pod := range anchors {
		names[i] = regexp.QuoteMeta(pod.Name)
	}
	query := "topk(257,count by (namespace,pod,uid," + strings.Join(partitionLabels, ",") + ") (kube_pod_info{namespace=" + strconv.Quote(namespace) + ",pod=~" + strconv.Quote(strings.Join(names, "|")) + "}))"
	result, err := client.QueryEvidence(ctx, query)
	if err != nil {
		return prom.WorkloadMetricsScope{}, err
	}
	if result.ResultType != "vector" || len(result.Series) > 256 {
		return prom.WorkloadMetricsScope{}, nil
	}
	partition, _ := identifyPartition(anchors, result.Series)
	return prom.WorkloadMetricsScope{ClusterLabels: partition}, nil
}

func chooseWorkloadHistory(ctx context.Context, client RangeQuerier, scope PodScope, config prom.WorkloadMetricsScope, start, end time.Time, step time.Duration) workloadHistoryPlan {
	if _, err := config.Matchers(); err != nil {
		return workloadHistoryPlan{reason: historyScopeRemedy}
	}
	plan := workloadHistoryPlan{reason: "Workload history needs retained kube-state-metrics Pod ownership (and ReplicaSet ownership for Deployments), or the standard workload ownership recording rule."}
	var earliest int64
	bestCount := 0
	for _, recorded := range []bool{false, true} {
		history := prom.WorkloadHistory{Kind: scope.Kind, Namespace: scope.Namespace, Name: scope.Name, Scope: config, Recorded: recorded}
		owner, err := history.OwnerQuery()
		if err != nil {
			return workloadHistoryPlan{err: err}
		}
		result, err := client.QueryRange(ctx, "count("+owner+")", start, end, step)
		if err != nil {
			return workloadHistoryPlan{err: err, reason: "Historical ownership lookup failed; retry when the metrics backend is healthy."}
		}
		first, count := int64(0), 0
		for _, series := range result.Series {
			for _, point := range series.DataPoints {
				if point.Value > 0 && !math.IsInf(point.Value, 0) && !math.IsNaN(point.Value) {
					if first == 0 || point.Timestamp < first {
						first = point.Timestamp
					}
					count++
				}
			}
		}
		if count > 0 && (plan.history == nil || first < earliest || first == earliest && count > bestCount) {
			plan.history, plan.reason, earliest, bestCount = &history, "", first, count
		}
	}
	return plan
}
