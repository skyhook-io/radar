package k8score

import (
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/informers"
	listersappsv1 "k8s.io/client-go/listers/apps/v1"
	listersautoscalingv2 "k8s.io/client-go/listers/autoscaling/v2"
	listersbatchv1 "k8s.io/client-go/listers/batch/v1"
	listerscorev1 "k8s.io/client-go/listers/core/v1"
	listersnetworkingv1 "k8s.io/client-go/listers/networking/v1"
	listerspolicyv1 "k8s.io/client-go/listers/policy/v1"
	listersrbacv1 "k8s.io/client-go/listers/rbac/v1"
	listersstoragev1 "k8s.io/client-go/listers/storage/v1"
	"k8s.io/client-go/tools/cache"
)

// factoryFor resolves the informer factory the given enabled kind is wired
// to. With ResourceScopes a kind may live in the cluster-wide factory or in
// a per-namespace factory; reading the lister from the wrong one returns
// an empty result with no error (the kind never registered an informer
// against that factory). Callers MUST go through this helper.
func (rc *ResourceCache) factoryFor(key string) informers.SharedInformerFactory {
	if f, ok := rc.factoryByKind[key]; ok {
		return f
	}
	return rc.factory
}

func (rc *ResourceCache) indexerFor(key string) cache.Indexer {
	if rc == nil || rc.indexerByKind == nil {
		return nil
	}
	return rc.indexerByKind[key]
}

func (rc *ResourceCache) Services() listerscorev1.ServiceLister {
	if rc == nil || !rc.isEnabled(Services) {
		return nil
	}
	if idx := rc.indexerFor(Services); idx != nil {
		return listerscorev1.NewServiceLister(idx)
	}
	return rc.factoryFor(Services).Core().V1().Services().Lister()
}

func (rc *ResourceCache) Pods() listerscorev1.PodLister {
	if rc == nil || !rc.isEnabled(Pods) {
		return nil
	}
	if idx := rc.indexerFor(Pods); idx != nil {
		return listerscorev1.NewPodLister(idx)
	}
	if inf := rc.pagedInformers[Pods]; inf != nil {
		return listerscorev1.NewPodLister(inf.GetIndexer())
	}
	return rc.factoryFor(Pods).Core().V1().Pods().Lister()
}

func (rc *ResourceCache) Nodes() listerscorev1.NodeLister {
	if rc == nil || !rc.isEnabled(Nodes) {
		return nil
	}
	return rc.factoryFor(Nodes).Core().V1().Nodes().Lister()
}

func (rc *ResourceCache) Namespaces() listerscorev1.NamespaceLister {
	if rc == nil || !rc.isEnabled(Namespaces) {
		return nil
	}
	return rc.factoryFor(Namespaces).Core().V1().Namespaces().Lister()
}

func (rc *ResourceCache) ConfigMaps() listerscorev1.ConfigMapLister {
	if rc == nil || !rc.isReady(ConfigMaps) {
		return nil
	}
	if idx := rc.indexerFor(ConfigMaps); idx != nil {
		return listerscorev1.NewConfigMapLister(idx)
	}
	return rc.factoryFor(ConfigMaps).Core().V1().ConfigMaps().Lister()
}

func (rc *ResourceCache) Secrets() listerscorev1.SecretLister {
	if rc == nil || !rc.isReady(Secrets) {
		return nil
	}
	if idx := rc.indexerFor(Secrets); idx != nil {
		return listerscorev1.NewSecretLister(idx)
	}
	return rc.factoryFor(Secrets).Core().V1().Secrets().Lister()
}

func (rc *ResourceCache) Events() listerscorev1.EventLister {
	if rc == nil || !rc.isReady(Events) {
		return nil
	}
	if idx := rc.indexerFor(Events); idx != nil {
		return listerscorev1.NewEventLister(idx)
	}
	return rc.factoryFor(Events).Core().V1().Events().Lister()
}

func (rc *ResourceCache) PersistentVolumeClaims() listerscorev1.PersistentVolumeClaimLister {
	if rc == nil || !rc.isReady(PersistentVolumeClaims) {
		return nil
	}
	if idx := rc.indexerFor(PersistentVolumeClaims); idx != nil {
		return listerscorev1.NewPersistentVolumeClaimLister(idx)
	}
	return rc.factoryFor(PersistentVolumeClaims).Core().V1().PersistentVolumeClaims().Lister()
}

func (rc *ResourceCache) PersistentVolumes() listerscorev1.PersistentVolumeLister {
	if rc == nil || !rc.isReady(PersistentVolumes) {
		return nil
	}
	return rc.factoryFor(PersistentVolumes).Core().V1().PersistentVolumes().Lister()
}

func (rc *ResourceCache) Deployments() listersappsv1.DeploymentLister {
	if rc == nil || !rc.isEnabled(Deployments) {
		return nil
	}
	if idx := rc.indexerFor(Deployments); idx != nil {
		return listersappsv1.NewDeploymentLister(idx)
	}
	return rc.factoryFor(Deployments).Apps().V1().Deployments().Lister()
}

func (rc *ResourceCache) DaemonSets() listersappsv1.DaemonSetLister {
	if rc == nil || !rc.isEnabled(DaemonSets) {
		return nil
	}
	if idx := rc.indexerFor(DaemonSets); idx != nil {
		return listersappsv1.NewDaemonSetLister(idx)
	}
	return rc.factoryFor(DaemonSets).Apps().V1().DaemonSets().Lister()
}

func (rc *ResourceCache) StatefulSets() listersappsv1.StatefulSetLister {
	if rc == nil || !rc.isEnabled(StatefulSets) {
		return nil
	}
	if idx := rc.indexerFor(StatefulSets); idx != nil {
		return listersappsv1.NewStatefulSetLister(idx)
	}
	return rc.factoryFor(StatefulSets).Apps().V1().StatefulSets().Lister()
}

func (rc *ResourceCache) ReplicaSets() listersappsv1.ReplicaSetLister {
	if rc == nil || !rc.isEnabled(ReplicaSets) {
		return nil
	}
	if idx := rc.indexerFor(ReplicaSets); idx != nil {
		return listersappsv1.NewReplicaSetLister(idx)
	}
	if inf := rc.pagedInformers[ReplicaSets]; inf != nil {
		return listersappsv1.NewReplicaSetLister(inf.GetIndexer())
	}
	return rc.factoryFor(ReplicaSets).Apps().V1().ReplicaSets().Lister()
}

func (rc *ResourceCache) Ingresses() listersnetworkingv1.IngressLister {
	if rc == nil || !rc.isEnabled(Ingresses) {
		return nil
	}
	if idx := rc.indexerFor(Ingresses); idx != nil {
		return listersnetworkingv1.NewIngressLister(idx)
	}
	return rc.factoryFor(Ingresses).Networking().V1().Ingresses().Lister()
}

func (rc *ResourceCache) IngressClasses() listersnetworkingv1.IngressClassLister {
	if rc == nil || !rc.isEnabled(IngressClasses) {
		return nil
	}
	return rc.factoryFor(IngressClasses).Networking().V1().IngressClasses().Lister()
}

func (rc *ResourceCache) Jobs() listersbatchv1.JobLister {
	if rc == nil || !rc.isEnabled(Jobs) {
		return nil
	}
	if idx := rc.indexerFor(Jobs); idx != nil {
		return listersbatchv1.NewJobLister(idx)
	}
	return rc.factoryFor(Jobs).Batch().V1().Jobs().Lister()
}

func (rc *ResourceCache) CronJobs() listersbatchv1.CronJobLister {
	if rc == nil || !rc.isEnabled(CronJobs) {
		return nil
	}
	if idx := rc.indexerFor(CronJobs); idx != nil {
		return listersbatchv1.NewCronJobLister(idx)
	}
	return rc.factoryFor(CronJobs).Batch().V1().CronJobs().Lister()
}

func (rc *ResourceCache) HorizontalPodAutoscalers() listersautoscalingv2.HorizontalPodAutoscalerLister {
	if rc == nil || !rc.isEnabled(HorizontalPodAutoscalers) {
		return nil
	}
	if idx := rc.indexerFor(HorizontalPodAutoscalers); idx != nil {
		return listersautoscalingv2.NewHorizontalPodAutoscalerLister(idx)
	}
	return rc.factoryFor(HorizontalPodAutoscalers).Autoscaling().V2().HorizontalPodAutoscalers().Lister()
}

func (rc *ResourceCache) StorageClasses() listersstoragev1.StorageClassLister {
	if rc == nil || !rc.isReady(StorageClasses) {
		return nil
	}
	return rc.factoryFor(StorageClasses).Storage().V1().StorageClasses().Lister()
}

func (rc *ResourceCache) PodDisruptionBudgets() listerspolicyv1.PodDisruptionBudgetLister {
	if rc == nil || !rc.isReady(PodDisruptionBudgets) {
		return nil
	}
	if idx := rc.indexerFor(PodDisruptionBudgets); idx != nil {
		return listerspolicyv1.NewPodDisruptionBudgetLister(idx)
	}
	return rc.factoryFor(PodDisruptionBudgets).Policy().V1().PodDisruptionBudgets().Lister()
}

func (rc *ResourceCache) NetworkPolicies() listersnetworkingv1.NetworkPolicyLister {
	if rc == nil || !rc.isReady(NetworkPolicies) {
		return nil
	}
	if idx := rc.indexerFor(NetworkPolicies); idx != nil {
		return listersnetworkingv1.NewNetworkPolicyLister(idx)
	}
	return rc.factoryFor(NetworkPolicies).Networking().V1().NetworkPolicies().Lister()
}

func (rc *ResourceCache) ServiceAccounts() listerscorev1.ServiceAccountLister {
	if rc == nil || !rc.isEnabled(ServiceAccounts) {
		return nil
	}
	if idx := rc.indexerFor(ServiceAccounts); idx != nil {
		return listerscorev1.NewServiceAccountLister(idx)
	}
	return rc.factoryFor(ServiceAccounts).Core().V1().ServiceAccounts().Lister()
}

func (rc *ResourceCache) Roles() listersrbacv1.RoleLister {
	if rc == nil || !rc.isEnabled(Roles) {
		return nil
	}
	if idx := rc.indexerFor(Roles); idx != nil {
		return listersrbacv1.NewRoleLister(idx)
	}
	return rc.factoryFor(Roles).Rbac().V1().Roles().Lister()
}

func (rc *ResourceCache) ClusterRoles() listersrbacv1.ClusterRoleLister {
	if rc == nil || !rc.isEnabled(ClusterRoles) {
		return nil
	}
	return rc.factoryFor(ClusterRoles).Rbac().V1().ClusterRoles().Lister()
}

func (rc *ResourceCache) RoleBindings() listersrbacv1.RoleBindingLister {
	if rc == nil || !rc.isEnabled(RoleBindings) {
		return nil
	}
	if idx := rc.indexerFor(RoleBindings); idx != nil {
		return listersrbacv1.NewRoleBindingLister(idx)
	}
	return rc.factoryFor(RoleBindings).Rbac().V1().RoleBindings().Lister()
}

func (rc *ResourceCache) ClusterRoleBindings() listersrbacv1.ClusterRoleBindingLister {
	if rc == nil || !rc.isEnabled(ClusterRoleBindings) {
		return nil
	}
	return rc.factoryFor(ClusterRoleBindings).Rbac().V1().ClusterRoleBindings().Lister()
}

func (rc *ResourceCache) LimitRanges() listerscorev1.LimitRangeLister {
	if rc == nil || !rc.isEnabled(LimitRanges) {
		return nil
	}
	if idx := rc.indexerFor(LimitRanges); idx != nil {
		return listerscorev1.NewLimitRangeLister(idx)
	}
	return rc.factoryFor(LimitRanges).Core().V1().LimitRanges().Lister()
}

func (rc *ResourceCache) ResourceQuotas() listerscorev1.ResourceQuotaLister {
	if rc == nil || !rc.isEnabled(ResourceQuotas) {
		return nil
	}
	if idx := rc.indexerFor(ResourceQuotas); idx != nil {
		return listerscorev1.NewResourceQuotaLister(idx)
	}
	return rc.factoryFor(ResourceQuotas).Core().V1().ResourceQuotas().Lister()
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
		listersnetworkingv1.IngressClassLister, listersrbacv1.ClusterRoleLister,
		listersrbacv1.ClusterRoleBindingLister:
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
	case listerscorev1.ResourceQuotaLister:
		items, _ := l.ResourceQuotas(ns).List(labels.Everything())
		return len(items)
	case listersrbacv1.RoleLister:
		items, _ := l.Roles(ns).List(labels.Everything())
		return len(items)
	case listersrbacv1.RoleBindingLister:
		items, _ := l.RoleBindings(ns).List(labels.Everything())
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
	case listerscorev1.ResourceQuotaLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersrbacv1.RoleLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersrbacv1.ClusterRoleLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersrbacv1.RoleBindingLister:
		items, _ := l.List(labels.Everything())
		return len(items)
	case listersrbacv1.ClusterRoleBindingLister:
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
