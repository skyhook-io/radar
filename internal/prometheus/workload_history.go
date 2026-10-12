package prometheus

import (
	"context"
	"errors"
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

const historyScopeRemedy = "Historical cluster scope could not be verified. An operator can configure a verified scope override; see Troubleshooting identity matching in About these metrics."

type workloadPartitionMemo struct {
	generation uint64
	expires    time.Time
	scope      prom.WorkloadMetricsScope
	namespace  string
	identities string
	unlabeled  bool
}

type workloadPartitionProof struct {
	scope     prom.WorkloadMetricsScope
	unlabeled bool
}

type workloadHistoryPlan struct {
	history *prom.WorkloadHistory
	reason  string
	err     error
}

func (p workloadHistoryPlan) awaitingAttribution(state string) bool {
	return p.history == nil && state != "available"
}

func (c *Client) historicalClusterScope(ctx context.Context, scope PodScope, cache *k8s.ResourceCache) (prom.WorkloadMetricsScope, error) {
	proof, err := c.historicalClusterProof(ctx, scope, cache)
	return proof.scope, err
}

func (c *Client) historicalClusterProof(ctx context.Context, scope PodScope, cache *k8s.ResourceCache) (workloadPartitionProof, error) {
	if config, _, configured := c.workloadMetricsConfig(); configured {
		return workloadPartitionProof{scope: config}, nil
	}
	c.mu.RLock()
	generation := c.discoveryGen
	epoch := c.connectionEpoch
	if memo := c.workloadPartition; memo != nil && memo.generation == generation && time.Now().Before(memo.expires) && !c.retired && len(memo.scope.ClusterLabels) > 0 {
		proof := workloadPartitionProof{scope: memo.scope, unlabeled: memo.unlabeled}
		c.mu.RUnlock()
		return proof, nil
	}
	c.mu.RUnlock()
	anchors := append([]prom.WorkloadPodIdentity(nil), scope.Identities...)
	if len(anchors) == 0 && cache != nil && cache.Pods() != nil {
		pods, err := cache.Pods().Pods(scope.Namespace).List(labels.Everything())
		if err != nil {
			return workloadPartitionProof{}, err
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
		return workloadPartitionProof{}, nil
	}
	sort.Slice(anchors, func(i, j int) bool {
		if anchors[i].Name != anchors[j].Name {
			return anchors[i].Name < anchors[j].Name
		}
		return anchors[i].UID < anchors[j].UID
	})
	identities := fmt.Sprint(anchors)
	c.mu.RLock()
	if memo := c.workloadPartition; memo != nil && memo.generation == generation && time.Now().Before(memo.expires) && !c.retired &&
		(len(memo.scope.ClusterLabels) > 0 || (memo.namespace == scope.Namespace && memo.identities == identities)) {
		proof := workloadPartitionProof{scope: memo.scope, unlabeled: memo.unlabeled}
		c.mu.RUnlock()
		return proof, nil
	}
	c.mu.RUnlock()
	// Missing evidence depends on the exact Pods checked, even in one namespace.
	ch := c.workloadPartitionSF.DoChan(fmt.Sprintf("%d#%d#%s#%s", generation, epoch, scope.Namespace, identities), func() (any, error) {
		flightCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discoveryTimeout+8*time.Second)
		defer cancel()
		if _, _, err := c.EnsureConnected(flightCtx); err != nil {
			return nil, err
		}
		c.mu.RLock()
		if c.discoveryGen != generation || c.connectionEpoch != epoch || c.retired || c.baseURL == "" {
			c.mu.RUnlock()
			return nil, fmt.Errorf("metrics connection changed")
		}
		tr := prom.NewHTTPTransport(c.baseURL, c.basePath, c.httpClient)
		tr.Headers, tr.MaxResponseBytes = copyHeaders(c.headers), 4<<20
		c.mu.RUnlock()
		probeCtx, probeCancel := context.WithTimeout(flightCtx, 8*time.Second)
		defer probeCancel()
		proof, err := probeHistoricalPartition(probeCtx, prom.NewClient(tr), scope.Namespace, anchors)
		if err != nil {
			return nil, err
		}
		c.mu.Lock()
		defer c.mu.Unlock()
		if c.discoveryGen != generation || c.connectionEpoch != epoch || c.retired {
			return nil, fmt.Errorf("metrics connection changed")
		}
		if len(proof.scope.ClusterLabels) == 0 && !proof.unlabeled {
			return proof, nil
		}
		ttl := 5 * time.Minute
		if len(proof.scope.ClusterLabels) == 0 {
			ttl = 30 * time.Second
		}
		memo := c.workloadPartition
		if len(proof.scope.ClusterLabels) > 0 || memo == nil || memo.generation != generation ||
			len(memo.scope.ClusterLabels) == 0 || !time.Now().Before(memo.expires) {
			c.workloadPartition = &workloadPartitionMemo{generation: generation, expires: time.Now().Add(ttl), scope: proof.scope, namespace: scope.Namespace, identities: identities, unlabeled: proof.unlabeled}
		}
		return proof, nil
	})
	select {
	case <-ctx.Done():
		return workloadPartitionProof{}, ctx.Err()
	case result := <-ch:
		if result.Err != nil {
			return workloadPartitionProof{}, result.Err
		}
		return result.Val.(workloadPartitionProof), nil
	}
}

func probeHistoricalPartition(ctx context.Context, client *prom.Client, namespace string, anchors []prom.WorkloadPodIdentity) (workloadPartitionProof, error) {
	names := make([]string, len(anchors))
	for i, pod := range anchors {
		names[i] = regexp.QuoteMeta(pod.Name)
	}
	query := "topk(257,count by (namespace,pod,uid," + strings.Join(partitionLabels, ",") + ") (kube_pod_info{namespace=" + strconv.Quote(namespace) + ",pod=~" + strconv.Quote(strings.Join(names, "|")) + "}))"
	result, err := client.QueryEvidence(ctx, query)
	if err != nil {
		return workloadPartitionProof{}, err
	}
	if result.ResultType != "vector" || len(result.Series) > 256 {
		return workloadPartitionProof{}, nil
	}
	partition, _ := identifyPartitionWithUnlabeled(anchors, result.Series, true)
	proof := workloadPartitionProof{scope: prom.WorkloadMetricsScope{ClusterLabels: partition}}
	for _, value := range partition {
		if value == "" {
			// Workload history still requires a positive cluster label; only PVC
			// stats may use UID-backed evidence for an unlabeled partition.
			proof.scope.ClusterLabels, proof.unlabeled = nil, true
			break
		}
	}
	return proof, nil
}

func chooseWorkloadHistory(ctx context.Context, client RangeQuerier, scope PodScope, config prom.WorkloadMetricsScope, start, end time.Time, step time.Duration) workloadHistoryPlan {
	if _, err := config.Matchers(); err != nil {
		return workloadHistoryPlan{reason: historyScopeRemedy}
	}
	plan := workloadHistoryPlan{reason: "Workload history needs retained kube-state-metrics Pod ownership (and ReplicaSet ownership for Deployments), or the standard workload ownership recording rule."}
	var earliest int64
	var lookupErr error
	bestCount := 0
	for _, recorded := range []bool{false, true} {
		history := prom.WorkloadHistory{Kind: scope.Kind, Namespace: scope.Namespace, Name: scope.Name, Scope: config, Recorded: recorded}
		owner, err := history.OwnerQuery()
		if err != nil {
			return workloadHistoryPlan{err: err}
		}
		result, err := client.QueryRange(ctx, "count("+owner+")", start, end, step)
		if err != nil {
			lookupErr = errors.Join(lookupErr, err)
			if ctx.Err() != nil {
				break
			}
			continue
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
	if plan.history == nil && lookupErr != nil {
		return workloadHistoryPlan{err: lookupErr, reason: "Historical ownership lookup failed; retry when the metrics backend is healthy."}
	}
	return plan
}
