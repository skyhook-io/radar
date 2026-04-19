package k8score

import (
	"k8s.io/apimachinery/pkg/labels"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
	listersautoscalingv2 "k8s.io/client-go/listers/autoscaling/v2"
	listersbatchv1 "k8s.io/client-go/listers/batch/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	listersnetworkingv1 "k8s.io/client-go/listers/networking/v1"
	listerspolicyv1 "k8s.io/client-go/listers/policy/v1"
	listersstoragev1 "k8s.io/client-go/listers/storage/v1"
)

func (rc *ResourceCache) Services() listerscorev1.ServiceLister {
	if rc == nil || !rc.isEnabled(Services) {
		return nil
	}
	return rc.factory.Core().V1().Services().Lister()
}

func (rc *ResourceCache) Pods() listerscorev1.PodLister {
	if rc == nil || !rc.isEnabled(Pods) {
		return nil
	}
	return rc.factory.Core().V1().Pods().Lister()
}

func (rc *ResourceCache) Nodes() listerscorev1.NodeLister {
	if rc == nil || !rc.isEnabled(Nodes) {
		return nil
	}
	return rc.factory.Core().V1().Nodes().Lister()
}

func (rc *ResourceCache) Namespaces() listerscorev1.NamespaceLister {
	if rc == nil || !rc.isEnabled(Namespaces) {
		return nil
	}
	return rc.factory.Core().V1().Namespaces().Lister()
}

func (rc *ResourceCache) ConfigMaps() listerscorev1.ConfigMapLister {
	if rc == nil || !rc.isReady(ConfigMaps) {
		return nil
	}
	return rc.factory.Core().V1().ConfigMaps().Lister()
}

func (rc *ResourceCache) Secrets() listerscorev1.SecretLister {
	if rc == nil || !rc.isReady(Secrets) {
		return nil
	}
	return rc.factory.Core().V1().Secrets().Lister()
}

func (rc *ResourceCache) Events() listerscorev1.EventLister {
	if rc == nil || !rc.isReady(Events) {
		return nil
	}
	return rc.factory.Core().V1().Events().Lister()
}

func (rc *ResourceCache) PersistentVolumeClaims() listerscorev1.PersistentVolumeClaimLister {
	if rc == nil || !rc.isReady(PersistentVolumeClaims) {
		return nil
	}
	return rc.factory.Core().V1().PersistentVolumeClaims().Lister()
}

func (rc *ResourceCache) PersistentVolumes() listerscorev1.PersistentVolumeLister {
	if rc == nil || !rc.isReady(PersistentVolumes) {
		return nil
	}
	return rc.factory.Core().V1().PersistentVolumes().Lister()
}

func (rc *ResourceCache) Deployments() listersappsv1.DeploymentLister {
	if rc == nil || !rc.isEnabled(Deployments) {
		return nil
	}
	return rc.factory.Apps().V1().Deployments().Lister()
}

func (rc *ResourceCache) DaemonSets() listersappsv1.DaemonSetLister {
	if rc == nil || !rc.isEnabled(DaemonSets) {
		return nil
	}
	return rc.factory.Apps().V1().DaemonSets().Lister()
}

func (rc *ResourceCache) StatefulSets() listersappsv1.StatefulSetLister {
	if rc == nil || !rc.isEnabled(StatefulSets) {
		return nil
	}
	return rc.factory.Apps().V1().StatefulSets().Lister()
}

func (rc *ResourceCache) ReplicaSets() listersappsv1.ReplicaSetLister {
	if rc == nil || !rc.isEnabled(ReplicaSets) {
		return nil
	}
	return rc.factory.Apps().V1().ReplicaSets().Lister()
}

func (rc *ResourceCache) Ingresses() listersnetworkingv1.IngressLister {
	if rc == nil || !rc.isEnabled(Ingresses) {
		return nil
	}
	return rc.factory.Networking().V1().Ingresses().Lister()
}

func (rc *ResourceCache) IngressClasses() listersnetworkingv1.IngressClassLister {
	if rc == nil || !rc.isEnabled(IngressClasses) {
		return nil
	}
	return rc.factory.Networking().V1().IngressClasses().Lister()
}

func (rc *ResourceCache) Jobs() listersbatchv1.JobLister {
	if rc == nil || !rc.isEnabled(Jobs) {
		return nil
	}
	return rc.factory.Batch().V1().Jobs().Lister()
}

func (rc *ResourceCache) CronJobs() listersbatchv1.CronJobLister {
	if rc == nil || !rc.isEnabled(CronJobs) {
		return nil
	}
	return rc.factory.Batch().V1().CronJobs().Lister()
}

func (rc *ResourceCache) HorizontalPodAutoscalers() listersautoscalingv2.HorizontalPodAutoscalerLister {
	if rc == nil || !rc.isEnabled(HorizontalPodAutoscalers) {
		return nil
	}
	return rc.factory.Autoscaling().V2().HorizontalPodAutoscalers().Lister()
}

func (rc *ResourceCache) StorageClasses() listersstoragev1.StorageClassLister {
	if rc == nil || !rc.isReady(StorageClasses) {
		return nil
	}
	return rc.factory.Storage().V1().StorageClasses().Lister()
}

func (rc *ResourceCache) PodDisruptionBudgets() listerspolicyv1.PodDisruptionBudgetLister {
	if rc == nil || !rc.isReady(PodDisruptionBudgets) {
		return nil
	}
	return rc.factory.Policy().V1().PodDisruptionBudgets().Lister()
}

func (rc *ResourceCache) NetworkPolicies() listersnetworkingv1.NetworkPolicyLister {
	if rc == nil || !rc.isReady(NetworkPolicies) {
		return nil
	}
	return rc.factory.Networking().V1().NetworkPolicies().Lister()
}

func (rc *ResourceCache) ServiceAccounts() listerscorev1.ServiceAccountLister {
	if rc == nil || !rc.isEnabled(ServiceAccounts) {
		return nil
	}
	return rc.factory.Core().V1().ServiceAccounts().Lister()
}

func (rc *ResourceCache) LimitRanges() listerscorev1.LimitRangeLister {
	if rc == nil || !rc.isEnabled(LimitRanges) {
		return nil
	}
	return rc.factory.Core().V1().LimitRanges().Lister()
}

// listCountNamespaced counts items from a lister filtered to specific namespaces.
// If namespaces is empty, it returns the total count (same as listCount).
func listCountNamespaced(lister any, namespaces []string) int {
	if lister == nil {
		return 0
	}
	if len(namespaces) == 0 {
		return listCount(lister)
	}
	// Cluster-scoped resources ignore namespace filter
	if isClusterScoped(lister) {
		return listCount(lister)
	}
	total := 0
	for _, ns := range namespaces {
		total += listCountInNamespace(lister, ns)
	}
	return total
}

func isClusterScoped(lister any) bool {
	switch lister.(type) {
	case listerscorev1.NodeLister, listerscorev1.NamespaceLister,
		listerscorev1.PersistentVolumeLister, listersstoragev1.StorageClassLister,
		listersnetworkingv1.IngressClassLister:
		return true
	}
	return false
}

func listCountInNamespace(lister any, ns string) int {
	switch l := lister.(type) {
	case listerscorev1.PodLister:
		items, _ := l.Pods(ns).List(labels.Everything())
		return len(items)
	case listerscorev1.ServiceLister:
		items, _ := l.Services(ns).List(labels.Everything())
		return len(items)
	case listerscorev1.ConfigMapLister:
		items, _ := l.ConfigMaps(ns).List(labels.Everything())
		return len(items)
	case listerscorev1.SecretLister:
		items, _ := l.Secrets(ns).List(labels.Everything())
		return len(items)
	case listerscorev1.EventLister:
		items, _ := l.Events(ns).List(labels.Everything())
		return len(items)
	case listerscorev1.PersistentVolumeClaimLister:
		items, _ := l.PersistentVolumeClaims(ns).List(labels.Everything())
		return len(items)
	case listerscorev1.ServiceAccountLister:
		items, _ := l.ServiceAccounts(ns).List(labels.Everything())
		return len(items)
	case listerscorev1.LimitRangeLister:
		items, _ := l.LimitRanges(ns).List(labels.Everything())
		return len(items)
	case listersappsv1.DeploymentLister:
		items, _ := l.Deployments(ns).List(labels.Everything())
		return len(items)
	case listersappsv1.DaemonSetLister:
		items, _ := l.DaemonSets(ns).List(labels.Everything())
		return len(items)
	case listersappsv1.StatefulSetLister:
		items, _ := l.StatefulSets(ns).List(labels.Everything())
		return len(items)
	case listersappsv1.ReplicaSetLister:
		items, _ := l.ReplicaSets(ns).List(labels.Everything())
		return len(items)
	case listersnetworkingv1.IngressLister:
		items, _ := l.Ingresses(ns).List(labels.Everything())
		return len(items)
	case listersbatchv1.JobLister:
		items, _ := l.Jobs(ns).List(labels.Everything())
		return len(items)
	case listersbatchv1.CronJobLister:
		items, _ := l.CronJobs(ns).List(labels.Everything())
		return len(items)
	case listersautoscalingv2.HorizontalPodAutoscalerLister:
		items, _ := l.HorizontalPodAutoscalers(ns).List(labels.Everything())
		return len(items)
	case listerspolicyv1.PodDisruptionBudgetLister:
		items, _ := l.PodDisruptionBudgets(ns).List(labels.Everything())
		return len(items)
	case listersnetworkingv1.NetworkPolicyLister:
		items, _ := l.NetworkPolicies(ns).List(labels.Everything())
		return len(items)
	}
	return 0
}

// ListCountNamespaced is the exported version of listCountNamespaced for use by server handlers.
func ListCountNamespaced(lister any, namespaces []string) int {
	return listCountNamespaced(lister, namespaces)
}

// listCount is a helper that counts items from any known lister type.
func listCount(lister any) int {
	if lister == nil {
		return 0
	}
	switch l := lister.(type) {
	case listerscorev1.PodLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.ServiceLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.NodeLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.NamespaceLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.ConfigMapLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.SecretLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.EventLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.PersistentVolumeClaimLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.PersistentVolumeLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.ServiceAccountLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerscorev1.LimitRangeLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersappsv1.DeploymentLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersappsv1.DaemonSetLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersappsv1.StatefulSetLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersappsv1.ReplicaSetLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersnetworkingv1.IngressLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersnetworkingv1.IngressClassLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersbatchv1.JobLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersbatchv1.CronJobLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersautoscalingv2.HorizontalPodAutoscalerLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersstoragev1.StorageClassLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listerspolicyv1.PodDisruptionBudgetLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersnetworkingv1.NetworkPolicyLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	}
	return 0
}
