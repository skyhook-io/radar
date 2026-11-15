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

// The display builder sees only admitted objects. Its edges and synthesized
// nodes never participate in admission or traversal; the family walk owns both.
func neighborhoodDisplayNodes(objects map[resourceid.Ref]runtime.Object, provider ResourceProvider, dp DynamicProvider) (map[resourceid.Ref]Node, error) {
	p := &neighborhoodDisplayProvider{ResourceProvider: provider, objects: objects}
	opts := DefaultBuildOptions()
	opts.IncludeReplicaSets, opts.IncludeSecrets, opts.ForRelationshipCache = true, true, true
	opts.MaxIndividualPods = int(^uint(0) >> 1)
	var dynamic DynamicProvider
	if dp != nil {
		dynamic = &neighborhoodDisplayDynamic{DynamicProvider: dp, objects: objects}
	}
	topo, err := NewBuilder(p).WithDynamic(dynamic).Build(opts)
	if err != nil {
		return nil, err
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

	return out, nil
}

type neighborhoodDisplayProvider struct {
	ResourceProvider
	objects map[resourceid.Ref]runtime.Object
}

func neighborhoodDisplayObjects[T runtime.Object](p *neighborhoodDisplayProvider) []T {
	refs := make([]resourceid.Ref, 0, len(p.objects))
	for r := range p.objects {
		refs = append(refs, r)
	}
	slices.SortFunc(refs, func(a, b resourceid.Ref) int {
		if a.Key() < b.Key() {
			return -1
		}
		if a == b {
			return 0
		}
		return 1
	})
	var out []T
	for _, r := range refs {
		if obj, ok := p.objects[r].(T); ok {
			out = append(out, obj)
		}
	}
	return out
}
func (p *neighborhoodDisplayProvider) GetResourceStatus(kind, namespace, name string) *ResourceStatus {
	if p.ResourceProvider == nil {
		return nil
	}
	return p.ResourceProvider.GetResourceStatus(kind, namespace, name)
}
func (p *neighborhoodDisplayProvider) Pods() ([]*corev1.Pod, error) {
	return neighborhoodDisplayObjects[*corev1.Pod](p), nil
}
func (p *neighborhoodDisplayProvider) Services() ([]*corev1.Service, error) {
	return neighborhoodDisplayObjects[*corev1.Service](p), nil
}
func (p *neighborhoodDisplayProvider) Deployments() ([]*appsv1.Deployment, error) {
	return neighborhoodDisplayObjects[*appsv1.Deployment](p), nil
}
func (p *neighborhoodDisplayProvider) DaemonSets() ([]*appsv1.DaemonSet, error) {
	return neighborhoodDisplayObjects[*appsv1.DaemonSet](p), nil
}
func (p *neighborhoodDisplayProvider) StatefulSets() ([]*appsv1.StatefulSet, error) {
	return neighborhoodDisplayObjects[*appsv1.StatefulSet](p), nil
}
func (p *neighborhoodDisplayProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error) {
	return neighborhoodDisplayObjects[*appsv1.ReplicaSet](p), nil
}
func (p *neighborhoodDisplayProvider) Jobs() ([]*batchv1.Job, error) {
	return neighborhoodDisplayObjects[*batchv1.Job](p), nil
}
func (p *neighborhoodDisplayProvider) CronJobs() ([]*batchv1.CronJob, error) {
	return neighborhoodDisplayObjects[*batchv1.CronJob](p), nil
}
func (p *neighborhoodDisplayProvider) Ingresses() ([]*networkingv1.Ingress, error) {
	return neighborhoodDisplayObjects[*networkingv1.Ingress](p), nil
}
func (p *neighborhoodDisplayProvider) ConfigMaps() ([]*corev1.ConfigMap, error) {
	return neighborhoodDisplayObjects[*corev1.ConfigMap](p), nil
}
func (p *neighborhoodDisplayProvider) Secrets() ([]*corev1.Secret, error) {
	return neighborhoodDisplayObjects[*corev1.Secret](p), nil
}
func (p *neighborhoodDisplayProvider) ServiceAccounts() ([]*corev1.ServiceAccount, error) {
	return neighborhoodDisplayObjects[*corev1.ServiceAccount](p), nil
}
func (p *neighborhoodDisplayProvider) Namespaces() ([]*corev1.Namespace, error) {
	return neighborhoodDisplayObjects[*corev1.Namespace](p), nil
}
func (p *neighborhoodDisplayProvider) PersistentVolumeClaims() ([]*corev1.PersistentVolumeClaim, error) {
	return neighborhoodDisplayObjects[*corev1.PersistentVolumeClaim](p), nil
}
func (p *neighborhoodDisplayProvider) PersistentVolumes() ([]*corev1.PersistentVolume, error) {
	return neighborhoodDisplayObjects[*corev1.PersistentVolume](p), nil
}
func (p *neighborhoodDisplayProvider) HorizontalPodAutoscalers() ([]*autoscalingv2.HorizontalPodAutoscaler, error) {
	return neighborhoodDisplayObjects[*autoscalingv2.HorizontalPodAutoscaler](p), nil
}
func (p *neighborhoodDisplayProvider) PodDisruptionBudgets() ([]*policyv1.PodDisruptionBudget, error) {
	return neighborhoodDisplayObjects[*policyv1.PodDisruptionBudget](p), nil
}
func (p *neighborhoodDisplayProvider) NetworkPolicies() ([]*networkingv1.NetworkPolicy, error) {
	return neighborhoodDisplayObjects[*networkingv1.NetworkPolicy](p), nil
}
func (p *neighborhoodDisplayProvider) Nodes() ([]*corev1.Node, error) {
	return neighborhoodDisplayObjects[*corev1.Node](p), nil
}

type neighborhoodDisplayDynamic struct {
	DynamicProvider
	objects map[resourceid.Ref]runtime.Object
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
	kind := p.GetKindForGVR(gvr)
	for r, o := range p.objects {
		if r.Group != gvr.Group || r.Kind != kind || len(namespaces) > 0 && r.Namespace != "" && !slices.Contains(namespaces, r.Namespace) {
			continue
		}
		if u, ok := o.(*unstructured.Unstructured); ok {
			out = append(out, u)
		}
	}
	slices.SortFunc(out, func(a, b *unstructured.Unstructured) int {
		if a.GetNamespace()+"/"+a.GetName() < b.GetNamespace()+"/"+b.GetName() {
			return -1
		}
		return 1
	})
	return out, nil
}
func (p *neighborhoodDisplayDynamic) Get(gvr schema.GroupVersionResource, namespace, name string) (*unstructured.Unstructured, error) {
	u, _ := p.objects[resourceid.NewRef(gvr.Group, p.GetKindForGVR(gvr), namespace, name)].(*unstructured.Unstructured)
	return u, nil
}
func (p *neighborhoodDisplayDynamic) GetWatchedResources() []schema.GroupVersionResource {
	var out []schema.GroupVersionResource
	for r := range p.objects {
		if _, ok := p.objects[r].(*unstructured.Unstructured); !ok {
			continue
		}
		if gvr, ok := p.GetGVRWithGroup(r.Kind, r.Group); ok && !slices.Contains(out, gvr) {
			out = append(out, gvr)
		}
	}
	slices.SortFunc(out, func(a, b schema.GroupVersionResource) int {
		if a.String() < b.String() {
			return -1
		}
		return 1
	})
	return out
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
	return node
}
