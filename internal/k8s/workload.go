package k8s

import (
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// GetWorkloadSelector returns the label selector for a workload from cache.
// kind must be "deployments", "statefulsets", or "daemonsets".
func GetWorkloadSelector(cache *ResourceCache, kind, namespace, name string) (*metav1.LabelSelector, error) {
	switch kind {
	case "deployments":
		lister := cache.Deployments()
		if lister == nil {
			return nil, fmt.Errorf("insufficient permissions to list deployments")
		}
		dep, err := lister.Deployments(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("deployment %s/%s not found: %w", namespace, name, err)
		}
		return dep.Spec.Selector, nil

	case "statefulsets":
		lister := cache.StatefulSets()
		if lister == nil {
			return nil, fmt.Errorf("insufficient permissions to list statefulsets")
		}
		sts, err := lister.StatefulSets(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("statefulset %s/%s not found: %w", namespace, name, err)
		}
		return sts.Spec.Selector, nil

	case "daemonsets":
		lister := cache.DaemonSets()
		if lister == nil {
			return nil, fmt.Errorf("insufficient permissions to list daemonsets")
		}
		ds, err := lister.DaemonSets(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("daemonset %s/%s not found: %w", namespace, name, err)
		}
		return ds.Spec.Selector, nil

	default:
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
}

// GetContainersForPod returns container names to target for log collection.
// If selectedContainer is non-empty, validates it against containers.
// If includeInit is true, also checks init containers.
// If selectedContainer is empty, returns all main container names.
func GetContainersForPod(pod *corev1.Pod, selectedContainer string, includeInit bool) []string {
	if selectedContainer != "" {
		for _, c := range pod.Spec.Containers {
			if c.Name == selectedContainer {
				return []string{selectedContainer}
			}
		}
		if includeInit {
			for _, c := range pod.Spec.InitContainers {
				if c.Name == selectedContainer {
					return []string{selectedContainer}
				}
			}
		}
		return nil
	}
	containers := make([]string, 0, len(pod.Spec.Containers))
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	return containers
}
