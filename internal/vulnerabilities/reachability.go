package vulnerabilities

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/relations"
)

// SelectingServices are the Services in the workload's namespace that select
// at least one of its Pods, by the same selector rule exposure uses. A
// reachability check probes only these: an Ingress or route can send to
// several Services, and the others belong to other workloads.
func SelectingServices(cache *k8s.ResourceCache, w WorkloadRef) []string {
	if cache == nil || cache.Pods() == nil || cache.Services() == nil {
		return nil
	}
	pods, err := cache.Pods().Pods(w.Namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	var mine []*corev1.Pod
	for _, pod := range pods {
		ref := podWorkload(cache, pod)
		if ref.Kind == w.Kind && ref.Name == w.Name && (w.Group == "" || ref.Group == w.Group) {
			mine = append(mine, pod)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	services, err := cache.Services().Services(w.Namespace).List(labels.Everything())
	if err != nil {
		return nil
	}
	var out []string
	for _, svc := range services {
		selection, ok, err := relations.ServiceSelectors.Selectors[0].Selection(svc)
		if !ok || err != nil {
			continue
		}
		if slices.ContainsFunc(mine, func(pod *corev1.Pod) bool {
			ok, _ := selection.Matches(relations.Candidate{Labels: labels.Set(pod.Labels)}, nil)
			return ok
		}) {
			out = append(out, svc.Name)
		}
	}
	slices.Sort(out)
	return out
}
