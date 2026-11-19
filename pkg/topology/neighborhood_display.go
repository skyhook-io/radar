package topology

import (
	"fmt"
	"slices"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	autoscalingv2 "k8s.io/api/autoscaling/v2"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	policyv1 "k8s.io/api/policy/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"

	"github.com/skyhook-io/radar/pkg/resourceid"
)

// neighborhoodDisplayPolicyEffect is whether the display builder computes
// policy coverage badges. It never does for the walk; tests turn it on to
// check the walk withholds a badge computed from the admitted objects alone.
var neighborhoodDisplayPolicyEffect = false

// The display builder sees only admitted objects. Its edges and synthesized
// nodes never participate in admission or traversal; the family walk owns both.
// Its edges are returned only to label the walk's own.
func neighborhoodDisplayNodes(objects map[resourceid.Ref]runtime.Object, provider ResourceProvider, dp DynamicProvider) (map[resourceid.Ref]Node, []Edge, error) {
	p := newNeighborhoodDisplayProvider(objects, provider)
	opts := DefaultBuildOptions()
	opts.IncludeReplicaSets, opts.IncludeSecrets, opts.ForRelationshipCache = true, true, true
	opts.MaxIndividualPods = int(^uint(0) >> 1)
	opts.ShowPolicyEffect = neighborhoodDisplayPolicyEffect
	var dynamic DynamicProvider
	if dp != nil {
		dynamic = newNeighborhoodDisplayDynamic(objects, p.dynamic, dp)
	}
	topo, err := NewBuilder(p).WithDynamic(dynamic).Build(opts)
	if err != nil {
		return nil, nil, err
	}
	out := map[resourceid.Ref]Node{}
	byKey := map[string]resourceid.Ref{}
	for r := range objects {
		byKey[r.Key()] = r
	}
	for _, n := range topo.Nodes {
		for _, key := range nodeResourceKeys(&n) {
			if r, ok := byKey[key]; ok {
				out[r] = n
			}
		}
	}

	return out, topo.Edges, nil
}

type neighborhoodDisplayProvider struct {
	ResourceProvider
	pods            []*corev1.Pod
	services        []*corev1.Service
	deployments     []*appsv1.Deployment
	daemonSets      []*appsv1.DaemonSet
	statefulSets    []*appsv1.StatefulSet
	replicaSets     []*appsv1.ReplicaSet
	jobs            []*batchv1.Job
	cronJobs        []*batchv1.CronJob
	ingresses       []*networkingv1.Ingress
	configMaps      []*corev1.ConfigMap
	secrets         []*corev1.Secret
	serviceAccounts []*corev1.ServiceAccount
	namespaces      []*corev1.Namespace
	pvcs            []*corev1.PersistentVolumeClaim
	pvs             []*corev1.PersistentVolume
	hpas            []*autoscalingv2.HorizontalPodAutoscaler
	pdbs            []*policyv1.PodDisruptionBudget
	networkPolicies []*networkingv1.NetworkPolicy
	nodes           []*corev1.Node
	dynamic         []*unstructured.Unstructured
}

func newNeighborhoodDisplayProvider(objects map[resourceid.Ref]runtime.Object, provider ResourceProvider) *neighborhoodDisplayProvider {
	p := &neighborhoodDisplayProvider{ResourceProvider: provider}
	refs := make([]resourceid.Ref, 0, len(objects))
	for r := range objects {
		refs = append(refs, r)
	}
	slices.SortFunc(refs, func(a, b resourceid.Ref) int { return strings.Compare(a.Key(), b.Key()) })
	for _, r := range refs {
		switch obj := objects[r].(type) {
		case *corev1.Pod:
			p.pods = append(p.pods, obj)
		case *corev1.Service:
			p.services = append(p.services, obj)
		case *appsv1.Deployment:
			p.deployments = append(p.deployments, obj)
		case *appsv1.DaemonSet:
			p.daemonSets = append(p.daemonSets, obj)
		case *appsv1.StatefulSet:
			p.statefulSets = append(p.statefulSets, obj)
		case *appsv1.ReplicaSet:
			p.replicaSets = append(p.replicaSets, obj)
		case *batchv1.Job:
			p.jobs = append(p.jobs, obj)
		case *batchv1.CronJob:
			p.cronJobs = append(p.cronJobs, obj)
		case *networkingv1.Ingress:
			p.ingresses = append(p.ingresses, obj)
		case *corev1.ConfigMap:
			p.configMaps = append(p.configMaps, obj)
		case *corev1.Secret:
			p.secrets = append(p.secrets, obj)
		case *corev1.ServiceAccount:
			p.serviceAccounts = append(p.serviceAccounts, obj)
		case *corev1.Namespace:
			p.namespaces = append(p.namespaces, obj)
		case *corev1.PersistentVolumeClaim:
			p.pvcs = append(p.pvcs, obj)
		case *corev1.PersistentVolume:
			p.pvs = append(p.pvs, obj)
		case *autoscalingv2.HorizontalPodAutoscaler:
			p.hpas = append(p.hpas, obj)
		case *policyv1.PodDisruptionBudget:
			p.pdbs = append(p.pdbs, obj)
		case *networkingv1.NetworkPolicy:
			p.networkPolicies = append(p.networkPolicies, obj)
		case *corev1.Node:
			p.nodes = append(p.nodes, obj)
		case *unstructured.Unstructured:
			p.dynamic = append(p.dynamic, obj)
		}
	}
	return p
}
func (p *neighborhoodDisplayProvider) GetResourceStatus(kind, namespace, name string) *ResourceStatus {
	if p.ResourceProvider == nil {
		return nil
	}
	return p.ResourceProvider.GetResourceStatus(kind, namespace, name)
}
func (p *neighborhoodDisplayProvider) Pods() ([]*corev1.Pod, error) {
	return p.pods, nil
}
func (p *neighborhoodDisplayProvider) Services() ([]*corev1.Service, error) {
	return p.services, nil
}
func (p *neighborhoodDisplayProvider) Deployments() ([]*appsv1.Deployment, error) {
	return p.deployments, nil
}
func (p *neighborhoodDisplayProvider) DaemonSets() ([]*appsv1.DaemonSet, error) {
	return p.daemonSets, nil
}
func (p *neighborhoodDisplayProvider) StatefulSets() ([]*appsv1.StatefulSet, error) {
	return p.statefulSets, nil
}
func (p *neighborhoodDisplayProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error) {
	return p.replicaSets, nil
}
func (p *neighborhoodDisplayProvider) Jobs() ([]*batchv1.Job, error) {
	return p.jobs, nil
}
func (p *neighborhoodDisplayProvider) CronJobs() ([]*batchv1.CronJob, error) {
	return p.cronJobs, nil
}
func (p *neighborhoodDisplayProvider) Ingresses() ([]*networkingv1.Ingress, error) {
	return p.ingresses, nil
}
func (p *neighborhoodDisplayProvider) ConfigMaps() ([]*corev1.ConfigMap, error) {
	return p.configMaps, nil
}
func (p *neighborhoodDisplayProvider) Secrets() ([]*corev1.Secret, error) {
	return p.secrets, nil
}
func (p *neighborhoodDisplayProvider) ServiceAccounts() ([]*corev1.ServiceAccount, error) {
	return p.serviceAccounts, nil
}
func (p *neighborhoodDisplayProvider) Namespaces() ([]*corev1.Namespace, error) {
	return p.namespaces, nil
}
func (p *neighborhoodDisplayProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	return p.pvcs, nil
}
func (p *neighborhoodDisplayProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) {
	return p.pvs, nil
}
func (p *neighborhoodDisplayProvider) HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	return p.hpas, nil
}
func (p *neighborhoodDisplayProvider) PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error) {
	return p.pdbs, nil
}
func (p *neighborhoodDisplayProvider) NetworkPolicies() ([]*networkingv1.NetworkPolicy, error) {
	return p.networkPolicies, nil
}
func (p *neighborhoodDisplayProvider) Nodes() ([]*corev1.Node, error) {
	return p.nodes, nil
}

type neighborhoodDisplayDynamic struct {
	DynamicProvider
	objects map[resourceid.Ref]runtime.Object
	byKind  map[resourceid.GroupKind][]*unstructured.Unstructured
	kinds   map[schema.GroupVersionResource]string
	aliases map[resourceid.GroupKind]bool
	names   map[string]bool
	gvrs    map[resourceid.GroupKind]schema.GroupVersionResource
	watched []schema.GroupVersionResource
}

func newNeighborhoodDisplayDynamic(objects map[resourceid.Ref]runtime.Object, ordered []*unstructured.Unstructured, dp DynamicProvider) *neighborhoodDisplayDynamic {
	p := &neighborhoodDisplayDynamic{DynamicProvider: dp, objects: objects,
		byKind: map[resourceid.GroupKind][]*unstructured.Unstructured{}, kinds: map[schema.GroupVersionResource]string{},
		aliases: map[resourceid.GroupKind]bool{}, names: map[string]bool{}, gvrs: map[resourceid.GroupKind]schema.GroupVersionResource{}}
	for _, obj := range ordered {
		gk := resourceid.GroupKind{Group: resourceid.GroupFromAPIVersion(obj.GetAPIVersion()), Kind: obj.GetKind()}
		if _, seen := p.byKind[gk]; !seen {
			name := strings.ToLower(gk.Kind)
			alias := resourceid.GroupKind{Group: gk.Group, Kind: name}
			p.aliases[alias], p.names[name] = true, true
			if gvr, ok := dp.GetGVRWithGroup(gk.Kind, gk.Group); ok {
				p.gvrs[alias] = gvr
				if _, known := p.kinds[gvr]; !known {
					p.kinds[gvr] = dp.GetKindForGVR(gvr)
					p.watched = append(p.watched, gvr)
				}
				p.aliases[resourceid.GroupKind{Group: gk.Group, Kind: strings.ToLower(gvr.Resource)}] = true
				p.names[strings.ToLower(gvr.Resource)] = true
			}
		}
		p.byKind[gk] = append(p.byKind[gk], obj)
	}
	slices.SortFunc(p.watched, func(a, b schema.GroupVersionResource) int { return strings.Compare(a.String(), b.String()) })
	return p
}

func (p *neighborhoodDisplayDynamic) GetGVRWithGroup(name, group string) (schema.GroupVersionResource, bool) {
	if group == "" {
		return p.GetGVR(name)
	}
	key := resourceid.GroupKind{Group: group, Kind: strings.ToLower(name)}
	if !p.aliases[key] {
		return schema.GroupVersionResource{}, false
	}
	if gvr, ok := p.gvrs[key]; ok {
		return gvr, true
	}
	return p.DynamicProvider.GetGVRWithGroup(name, group)
}
func (p *neighborhoodDisplayDynamic) GetGVR(name string) (schema.GroupVersionResource, bool) {
	if !p.names[strings.ToLower(name)] {
		return schema.GroupVersionResource{}, false
	}
	return p.DynamicProvider.GetGVR(name)
}
func (p *neighborhoodDisplayDynamic) GetKindForGVR(gvr schema.GroupVersionResource) string {
	return p.kinds[gvr]
}
func (p *neighborhoodDisplayDynamic) IsCRD(name string) bool {
	return p.names[strings.ToLower(name)] && p.DynamicProvider.IsCRD(name)
}
func (p *neighborhoodDisplayDynamic) List(gvr schema.GroupVersionResource, namespace string) ([]*unstructured.Unstructured, error) {
	var namespaces []string
	if namespace != "" {
		namespaces = []string{namespace}
	}
	return p.ListNamespaces(gvr, namespaces)
}
func (p *neighborhoodDisplayDynamic) ListNamespaces(gvr schema.GroupVersionResource, namespaces []string) ([]*unstructured.Unstructured, error) {
	var out []*unstructured.Unstructured
	for _, obj := range p.byKind[resourceid.GroupKind{Group: gvr.Group, Kind: p.GetKindForGVR(gvr)}] {
		if len(namespaces) == 0 || obj.GetNamespace() == "" || slices.Contains(namespaces, obj.GetNamespace()) {
			out = append(out, obj)
		}
	}
	return out, nil
}
func (p *neighborhoodDisplayDynamic) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	u, _ := p.objects[resourceid.NewRef(gvr.Group, p.GetKindForGVR(gvr), namespace, name)].(*unstructured.Unstructured)
	return u, nil
}
func (p *neighborhoodDisplayDynamic) GetWatchedResources() []schema.GroupVersionResource {
	return slices.Clone(p.watched)
}

func neighborhoodStandaloneNode(r resourceid.Ref, obj runtime.Object, node Node) Node {
	switch o := obj.(type) {
	case *corev1.ConfigMap:
		return configMapNode(o)
	case *corev1.Secret:
		return secretNode(o)
	case *corev1.PersistentVolumeClaim:
		return persistentVolumeClaimNode(o)
	case *corev1.ServiceAccount:
		return serviceAccountNode(o)
	case *unstructured.Unstructured:
		if r.Group == "bitnami.com" && r.Kind == "SealedSecret" {
			return sealedSecretNode(o)
		}
		node.ID = fmt.Sprintf("%s/%s/%s/%s", strings.ToLower(r.Kind), r.Namespace, r.Name, r.Group)
		node.Status = extractGenericStatus(o)
	}
	if meta, err := apimeta.Accessor(obj); err == nil {
		node.Data["labels"] = meta.GetLabels()
	}
	if n, ok := obj.(*corev1.Node); ok {
		// The topology's ID for a Node, which the display builder draws only
		// beside a NodeClaim or a Machine.
		node.ID = "node//" + n.Name
		node.Status = extractNodeStatus(*n)
		node.Data["instanceType"] = n.Labels["node.kubernetes.io/instance-type"]
	}
	return node
}
