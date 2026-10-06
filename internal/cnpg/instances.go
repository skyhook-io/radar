package cnpg

import (
	"errors"
	"sort"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/cnpg"
)

const (
	clusterLabel          = "cnpg.io/cluster"
	defaultLogContainer   = "postgres"
	defaultLogTailLines   = 200
	logDiscoveryInterval  = 5 * time.Second
	logsEmptyMessage      = "No readable logs from this cluster's instances in this snapshot. Refresh after the instances start."
	logsIntervalEmpty     = "No log lines in this interval from the runs Kubernetes still keeps (the current and previous run of each instance container)."
	logsNoInstanceMessage = "This cluster has no instance Pods yet."
)

// cnpgClusterInstancePods returns the Cluster's instance Pods under the same
// label-and-controller-UID rule the workspace uses, sorted by name.
func clusterInstancePods(cache *k8s.ResourceCache, cluster *unstructured.Unstructured) ([]*corev1.Pod, error) {
	lister := cache.Pods()
	if lister == nil {
		return nil, errors.New("pod cache unavailable")
	}
	namespace, name := cluster.GetNamespace(), cluster.GetName()
	candidates, err := lister.Pods(namespace).List(labels.SelectorFromSet(labels.Set{clusterLabel: name}))
	if err != nil {
		return nil, err
	}
	uids := map[string]types.UID{namespace + "/" + name: cluster.GetUID()}
	pods := make([]*corev1.Pod, 0, len(candidates))
	for _, p := range candidates {
		if p != nil && isCNPGInstancePod(p, uids) {
			pods = append(pods, p)
		}
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods, nil
}

func instanceRole(p *corev1.Pod) string { return cnpg.InstanceRole(p) }

const Group = cnpg.Group
