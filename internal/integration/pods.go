package integration

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/labels"
	v1listers "k8s.io/client-go/listers/core/v1"
)

// listPodsScoped lists pods either cluster-wide (namespaces nil) or across
// the caller's allowed namespaces — the scoping shape every metrics/vitals
// consumer shares.
func ListPodsScoped(podLister v1listers.PodLister, namespaces []string) []*corev1.Pod {
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
