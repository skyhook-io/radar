package k8s

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/k8score"
	topology "github.com/skyhook-io/radar/pkg/topology"
)

// topologyResourceProvider adapts *ResourceCache to topology.ResourceProvider.
type topologyResourceProvider struct {
	cache     *ResourceCache
	namespace string
}

// NewTopologyResourceProvider wraps a ResourceCache as a topology.ResourceProvider.
// Returns nil if cache is nil; topology.Builder.Build() will return an error in that case.
func NewTopologyResourceProvider(cache *ResourceCache) topology.ResourceProvider {
	if cache == nil {
		return nil
	}
	return &topologyResourceProvider{cache: cache}
}

func (a *topologyResourceProvider) ForNamespace(namespace string) topology.ResourceProvider {
	return &topologyResourceProvider{cache: a.cache, namespace: namespace}
}

// checkReady guards every typed list, including listers exposed before their
// initial LIST completes. An incomplete store cannot establish absence.
func (a *topologyResourceProvider) checkReady(key string) error {
	switch a.cache.KindReadinessFor(key) {
	case k8score.KindPending:
		return fmt.Errorf("%s inventory is still syncing", key)
	case k8score.KindFailed:
		return fmt.Errorf("%s inventory sync failed", key)
	default:
		return nil // Unavailable retains the lister's existing availability error.
	}
}

func (a *topologyResourceProvider) Pods() ([]*corev1.Pod, error) {
	if err := a.checkReady(k8score.Pods); err != nil {
		return nil, err
	}
	lister := a.cache.Pods()
	if lister == nil {
		return nil, fmt.Errorf("pods not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.Pods(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Services() ([]*corev1.Service, error) {
	if err := a.checkReady(k8score.Services); err != nil {
		return nil, err
	}
	lister := a.cache.Services()
	if lister == nil {
		return nil, fmt.Errorf("services not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.Services(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Deployments() ([]*appsv1.Deployment, error) {
	if err := a.checkReady(k8score.Deployments); err != nil {
		return nil, err
	}
	lister := a.cache.Deployments()
	if lister == nil {
		return nil, fmt.Errorf("deployments not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.Deployments(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) DaemonSets() ([]*appsv1.DaemonSet, error) {
	if err := a.checkReady(k8score.DaemonSets); err != nil {
		return nil, err
	}
	lister := a.cache.DaemonSets()
	if lister == nil {
		return nil, fmt.Errorf("daemonsets not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.DaemonSets(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) StatefulSets() ([]*appsv1.StatefulSet, error) {
	if err := a.checkReady(k8score.StatefulSets); err != nil {
		return nil, err
	}
	lister := a.cache.StatefulSets()
	if lister == nil {
		return nil, fmt.Errorf("statefulsets not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.StatefulSets(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error) {
	if err := a.checkReady(k8score.ReplicaSets); err != nil {
		return nil, err
	}
	lister := a.cache.ReplicaSets()
	if lister == nil {
		return nil, fmt.Errorf("replicasets not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.ReplicaSets(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Jobs() ([]*batchv1.Job, error) {
	if err := a.checkReady(k8score.Jobs); err != nil {
		return nil, err
	}
	lister := a.cache.Jobs()
	if lister == nil {
		return nil, fmt.Errorf("jobs not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.Jobs(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) CronJobs() ([]*batchv1.CronJob, error) {
	if err := a.checkReady(k8score.CronJobs); err != nil {
		return nil, err
	}
	lister := a.cache.CronJobs()
	if lister == nil {
		return nil, fmt.Errorf("cronjobs not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.CronJobs(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Ingresses() ([]*networkingv1.Ingress, error) {
	if err := a.checkReady(k8score.Ingresses); err != nil {
		return nil, err
	}
	lister := a.cache.Ingresses()
	if lister == nil {
		return nil, fmt.Errorf("ingresses not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.Ingresses(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) ConfigMaps() ([]*corev1.ConfigMap, error) {
	if err := a.checkReady(k8score.ConfigMaps); err != nil {
		return nil, err
	}
	lister := a.cache.ConfigMaps()
	if lister == nil {
		return nil, fmt.Errorf("configmaps not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.ConfigMaps(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Secrets() ([]*corev1.Secret, error) {
	if err := a.checkReady(k8score.Secrets); err != nil {
		return nil, err
	}
	lister := a.cache.Secrets()
	if lister == nil {
		return nil, fmt.Errorf("secrets not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.Secrets(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) ServiceAccounts() ([]*corev1.ServiceAccount, error) {
	if err := a.checkReady(k8score.ServiceAccounts); err != nil {
		return nil, err
	}
	lister := a.cache.ServiceAccounts()
	if lister == nil {
		return nil, fmt.Errorf("serviceaccounts not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.ServiceAccounts(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Namespaces() ([]*corev1.Namespace, error) {
	if err := a.checkReady(k8score.Namespaces); err != nil {
		return nil, err
	}
	lister := a.cache.Namespaces()
	if lister == nil {
		return nil, fmt.Errorf("namespaces not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	if err := a.checkReady(k8score.PersistentVolumeClaims); err != nil {
		return nil, err
	}
	lister := a.cache.PersistentVolumeClaims()
	if lister == nil {
		return nil, fmt.Errorf("persistentvolumeclaims not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.PersistentVolumeClaims(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) {
	if err := a.checkReady(k8score.PersistentVolumes); err != nil {
		return nil, err
	}
	lister := a.cache.PersistentVolumes()
	if lister == nil {
		return nil, fmt.Errorf("persistentvolumes not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	if err := a.checkReady(k8score.HorizontalPodAutoscalers); err != nil {
		return nil, err
	}
	lister := a.cache.HorizontalPodAutoscalers()
	if lister == nil {
		return nil, fmt.Errorf("horizontalpodautoscalers not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.HorizontalPodAutoscalers(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error) {
	if err := a.checkReady(k8score.PodDisruptionBudgets); err != nil {
		return nil, err
	}
	lister := a.cache.PodDisruptionBudgets()
	if lister == nil {
		return nil, fmt.Errorf("poddisruptionbudgets not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.PodDisruptionBudgets(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) NetworkPolicies() ([]*networkingv1.NetworkPolicy, error) {
	if err := a.checkReady(k8score.NetworkPolicies); err != nil {
		return nil, err
	}
	lister := a.cache.NetworkPolicies()
	if lister == nil {
		return nil, fmt.Errorf("networkpolicies not available (RBAC not granted)")
	}
	if a.namespace != "" {
		return lister.NetworkPolicies(a.namespace).List(labels.Everything())
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) Nodes() ([]*corev1.Node, error) {
	if err := a.checkReady(k8score.Nodes); err != nil {
		return nil, err
	}
	lister := a.cache.Nodes()
	if lister == nil {
		return nil, fmt.Errorf("nodes not available (RBAC not granted)")
	}
	return lister.List(labels.Everything())
}

func (a *topologyResourceProvider) GetResourceStatus(kind, namespace, name string) *topology.ResourceStatus {
	return a.cache.GetResourceStatus(kind, namespace, name)
}

// topologyDynamicProvider adapts *DynamicResourceCache + *ResourceDiscovery to topology.DynamicProvider.
type topologyDynamicProvider struct {
	dynCache  *DynamicResourceCache
	discovery *ResourceDiscovery
}

// NewTopologyDynamicProvider wraps DynamicResourceCache and ResourceDiscovery as a topology.DynamicProvider.
func NewTopologyDynamicProvider(dynCache *DynamicResourceCache, discovery *ResourceDiscovery) topology.DynamicProvider {
	if dynCache == nil || discovery == nil {
		return nil
	}
	return &topologyDynamicProvider{dynCache: dynCache, discovery: discovery}
}

func (a *topologyDynamicProvider) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	return a.dynCache.List(gvr, namespace)
}

func (a *topologyDynamicProvider) ListNamespaces(gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, error) {
	return a.dynCache.ListNamespaces(gvr, namespaces)
}

func (a *topologyDynamicProvider) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	return a.dynCache.Get(gvr, namespace, name)
}

func (a *topologyDynamicProvider) GetWatchedResources() []schema.GroupVersionResource {
	return a.dynCache.GetWatchedResources()
}

func (a *topologyDynamicProvider) GetDiscoveryStatus() k8score.CRDDiscoveryStatus {
	return a.dynCache.GetDiscoveryStatus()
}

func (a *topologyDynamicProvider) GetGVR(kindOrName string) (schema.GroupVersionResource, bool) {
	return a.discovery.GetGVR(kindOrName)
}

func (a *topologyDynamicProvider) GetGVRWithGroup(kindOrName, group string) (schema.GroupVersionResource, bool) {
	return a.discovery.GetGVRWithGroup(kindOrName, group)
}

func (a *topologyDynamicProvider) GetKindForGVR(gvr schema.GroupVersionResource) string {
	return a.discovery.GetKindForGVR(gvr)
}

func (a *topologyDynamicProvider) IsCRD(kind string) bool {
	return a.discovery.IsCRD(kind)
}

func (a *topologyDynamicProvider) IsCRDGVR(gvr schema.GroupVersionResource) bool {
	return a.discovery.IsCRDGVR(gvr)
}
