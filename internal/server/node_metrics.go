package server

import (
	"context"
	"encoding/json"
	"log"
	"time"

	"github.com/skyhook-io/radar/internal/k8s"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	v1listers "k8s.io/client-go/listers/core/v1"
)

// Shared metrics-server plumbing — consumed by both /api/dashboard and
// /api/vitals. Lives in neither handler file on purpose: the probe is a
// cluster-metrics concern, not a feature of either endpoint.

// metricsServerTimeout bounds the metrics-server query so a slow or unreachable
// metrics endpoint can't consume the dashboard's whole request budget (the
// handler runs this in a goroutine and blocks on it in wg.Wait()).
const metricsServerTimeout = 8 * time.Second

// fetchNodeUsage probes metrics-server for live node usage (impersonated —
// a caller without metrics.k8s.io access gets ok=false, not someone else's
// numbers). Extracted so /api/vitals can memoize JUST this probe while
// computing capacity/requests fresh from the informer cache on every call.
func (s *Server) fetchNodeUsage(ctx context.Context) (cpuMillis, memBytes int64, ok bool) {
	client := k8s.ClientFromContext(ctx)
	if client == nil {
		return 0, 0, false
	}
	mctx, cancel := context.WithTimeout(ctx, metricsServerTimeout)
	defer cancel()
	data, err := client.CoreV1().RESTClient().Get().
		AbsPath("/apis/metrics.k8s.io/v1beta1/nodes").
		DoRaw(mctx)
	if err != nil {
		log.Printf("[dashboard] node metrics unavailable (showing requests/capacity only): %v", err)
		return 0, 0, false
	}
	var nodeMetricsList struct {
		Items []struct {
			Usage struct {
				CPU    string `json:"cpu"`
				Memory string `json:"memory"`
			} `json:"usage"`
		} `json:"items"`
	}
	if err := json.Unmarshal(data, &nodeMetricsList); err != nil {
		log.Printf("[dashboard] failed to parse node metrics: %v", err)
		return 0, 0, false
	}
	if len(nodeMetricsList.Items) == 0 {
		return 0, 0, false
	}
	for _, item := range nodeMetricsList.Items {
		cpuMillis += parseCPUToMillis(item.Usage.CPU)
		memBytes += parseMemoryToBytes(item.Usage.Memory)
	}
	return cpuMillis, memBytes, true
}

// parseCPUToMillis delegates to k8s.ParseCPUToMillis.
func parseCPUToMillis(s string) int64 { return k8s.ParseCPUToMillis(s) }

// parseMemoryToBytes delegates to k8s.ParseMemoryToBytes.
func parseMemoryToBytes(s string) int64 { return k8s.ParseMemoryToBytes(s) }

// listPodsScoped lists pods either cluster-wide (namespaces nil) or across
// the caller's allowed namespaces — the scoping shape every metrics/vitals
// consumer shares.
func listPodsScoped(podLister v1listers.PodLister, namespaces []string) []*corev1.Pod {
	if podLister == nil {
		return nil
	}
	// Sentinel contract (parseNamespacesForUser): nil = all namespaces;
	// non-nil EMPTY = no namespace access — zero pods, never cluster-wide.
	if namespaces == nil {
		pods, _ := podLister.List(labels.Everything())
		return pods
	}
	var pods []*corev1.Pod
	for _, ns := range namespaces {
		items, _ := podLister.Pods(ns).List(labels.Everything())
		pods = append(pods, items...)
	}
	return pods
}

// capacityRequests carries the informer-derived halves of the capacity
// picture: node capacity plus scheduled-pod requests (completed pods
// excluded). Usage is the metrics-server probe's job (fetchNodeUsage).
type capacityRequests struct {
	cpuCapMillis int64
	memCapBytes  int64
	cpuReqMillis int64
	memReqBytes  int64
}

func computeCapacityRequests(nodes []*corev1.Node, pods []*corev1.Pod) capacityRequests {
	var cr capacityRequests
	for _, n := range nodes {
		cr.cpuCapMillis += n.Status.Capacity.Cpu().MilliValue()
		cr.memCapBytes += n.Status.Capacity.Memory().Value()
	}
	for _, pod := range pods {
		if pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		for _, c := range pod.Spec.Containers {
			if c.Resources.Requests == nil {
				continue
			}
			if cpu, ok := c.Resources.Requests[corev1.ResourceCPU]; ok {
				cr.cpuReqMillis += cpu.MilliValue()
			}
			if mem, ok := c.Resources.Requests[corev1.ResourceMemory]; ok {
				cr.memReqBytes += mem.Value()
			}
		}
	}
	return cr
}
