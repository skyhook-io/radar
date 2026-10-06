package cnpg

import (
	"context"
	"errors"
	"log"
	"net/http"
	"sort"
	"strings"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"

	integration "github.com/skyhook-io/radar/internal/integration"
	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/cnpg"
)

func runtimePermission(namespace string, allowed bool) CNPGRuntimePermission {
	p := CNPGRuntimePermission{Proxy: "allowed", Grant: grantGetPodsProxy.In(namespace).Ref()}
	if !allowed {
		p.Proxy = "denied"
	}
	return p
}

func proxyDenied(namespace string) CNPGRuntimeSource {
	return CNPGRuntimeSource{State: runtimeStateDenied, Error: "reading live data needs get pods/proxy in " + namespace}
}

func (s *Reader) ClusterRuntime(callerCtx context.Context, namespace, name string) (*CNPGClusterRuntimeResponse, error) {
	cache, cluster, err := s.Observations.Cluster(callerCtx, namespace, name, GrantListPods)
	if err != nil {
		return nil, err
	}
	pods, err := clusterInstancePods(cache, cluster)
	if err != nil {
		log.Printf("[cnpg] Failed to list instance Pods for %s/%s: %v", namespace, name, err)
		return nil, &ReadFailure{http.StatusServiceUnavailable, "instance Pods unavailable: " + err.Error()}
	}

	proxyAllowed := s.Access.Permission(callerCtx, grantGetPodsProxy.In(namespace)) != integration.PermissionDenied
	fenced := parseCNPGFenced(cluster.GetAnnotations()[cnpgFencedAnnotation])
	resp := CNPGClusterRuntimeResponse{
		Cluster:    CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: cluster.GetUID()},
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: runtimePermission(namespace, proxyAllowed),
		Instances:  make([]CNPGInstanceRuntime, len(pods)),
	}
	for i, p := range pods {
		resp.Instances[i] = CNPGInstanceRuntime{Pod: p.Name, PodUID: p.UID, Role: runtimeRole(p), Fenced: fenced.Fences(p.Name)}
	}
	if len(pods) == 0 {
		return &resp, nil
	}
	if !proxyAllowed {
		for i := range resp.Instances {
			resp.Instances[i].Status.CNPGRuntimeSource = proxyDenied(namespace)
			resp.Instances[i].Metrics.CNPGRuntimeSource = proxyDenied(namespace)
		}
		return &resp, nil
	}
	client := s.Clients.Proxy
	if client == nil {
		return nil, &ReadFailure{http.StatusServiceUnavailable, "cluster client unavailable"}
	}

	clusterMetricsTLS, _, _ := unstructured.NestedBool(cluster.Object, "spec", "monitoring", "tls", "enabled")
	identity := s.Identity
	run := newCNPGRuntimeRunner(callerCtx)
	for i, p := range pods {
		inst := &resp.Instances[i]
		statusTarget, metricsTarget := cnpgInstanceProxyTargets(p, clusterMetricsTLS)
		run.do(func(ctx context.Context) {
			inst.Status = memoized(ctx, identity, statusTarget, cnpgStatusMemoTTL, func(ctx context.Context) CNPGInstanceStatus {
				return cnpgInstanceStatusFrom(proxyGetWithFallback(ctx, client, statusTarget))
			})
		})
		run.do(func(ctx context.Context) {
			inst.Metrics = memoized(ctx, identity, metricsTarget, metricsMemoTTL, func(ctx context.Context) CNPGInstanceMetrics {
				return cnpgInstanceMetricsFrom(proxyGetWithFallback(ctx, client, metricsTarget))
			})
		})
	}
	run.wait()

	for i := range resp.Instances {
		inst := &resp.Instances[i]
		if inst.Status.State == runtimeStateDenied || inst.Metrics.State == runtimeStateDenied {
			resp.Permission.Proxy = "denied"
		}
		if inst.Fenced {
			explainCNPGFenced(&inst.Status.CNPGRuntimeSource)
			explainCNPGFenced(&inst.Metrics.CNPGRuntimeSource)
		}
		if inst.Status.CNPGInstanceStatusFacts != nil && inst.Metrics.CNPGInstanceMetricFacts != nil {
			inst.Status.CNPGInstanceStatusFacts = withCNPGSlotRetention(inst.Status.CNPGInstanceStatusFacts, inst.Metrics.ReplicationSlotsRetainedBytes)
		}
	}
	return &resp, nil
}

func (s *Reader) Pooler(ctx context.Context, cache *k8s.ResourceCache, namespace, name string) (*unstructured.Unstructured, error) {
	poolers, err := filterCNPGGroup(s.Observations.DynamicList(ctx, cache, "Pooler", Group, namespace))
	if err != nil {
		return nil, err
	}
	for _, p := range poolers {
		if p.GetNamespace() == namespace && p.GetName() == name && p.GroupVersionKind().Group == Group {
			return p, nil
		}
	}
	return nil, nil
}

// cnpgPoolerPods returns the Pods of the Pooler's Deployment, validated along
// the controller chain Pooler → Deployment (named after the Pooler) →
// ReplicaSet → Pod by UID. The poolerName label alone is something any Pod
// can carry.
func poolerPods(cache *k8s.ResourceCache, pooler *unstructured.Unstructured) ([]*corev1.Pod, error) {
	podLister, rsLister, deployLister := cache.Pods(), cache.ReplicaSets(), cache.Deployments()
	if podLister == nil || rsLister == nil || deployLister == nil {
		return nil, errors.New("pod, ReplicaSet or Deployment cache unavailable")
	}
	namespace, name := pooler.GetNamespace(), pooler.GetName()
	deploy, err := deployLister.Deployments(namespace).Get(name)
	if apierrors.IsNotFound(err) {
		return []*corev1.Pod{}, nil
	}
	if err != nil {
		return nil, err
	}
	if !controlledBy(deploy.OwnerReferences, Group, "Pooler", name, pooler.GetUID()) {
		return []*corev1.Pod{}, nil
	}
	candidates, err := podLister.Pods(namespace).List(labels.SelectorFromSet(labels.Set{cnpgPoolerNameLabel: name}))
	if err != nil {
		return nil, err
	}
	pods := make([]*corev1.Pod, 0, len(candidates))
	for _, p := range candidates {
		ref := controllerRef(p.OwnerReferences)
		if ref == nil || ref.Kind != "ReplicaSet" {
			continue
		}
		rs, err := rsLister.ReplicaSets(namespace).Get(ref.Name)
		if err != nil || rs.UID != ref.UID {
			continue
		}
		if controlledBy(rs.OwnerReferences, "apps", "Deployment", deploy.Name, deploy.UID) {
			pods = append(pods, p)
		}
	}
	sort.Slice(pods, func(i, j int) bool { return pods[i].Name < pods[j].Name })
	return pods, nil
}

func controllerRef(refs []metav1.OwnerReference) *metav1.OwnerReference {
	for i := range refs {
		if refs[i].Controller != nil && *refs[i].Controller {
			return &refs[i]
		}
	}
	return nil
}

func controlledBy(refs []metav1.OwnerReference, group, kind, name string, uid types.UID) bool {
	return cnpg.ControlledBy(refs, group, kind, name, uid)
}

func runtimeRole(p *corev1.Pod) string {
	switch role := instanceRole(p); role {
	case "primary", "replica":
		return role
	default:
		return "unknown"
	}
}

func explainCNPGFenced(src *CNPGRuntimeSource) {
	if src.State == runtimeStateUnreachable || src.State == runtimeStateError {
		src.Error = cnpgFencedErrorExplained + " (" + src.Error + ")"
	}
}

func poolerNotStarted(p *corev1.Pod) string {
	if p.Status.Phase == "" || p.Status.Phase == corev1.PodRunning {
		return ""
	}
	reason := "PgBouncer has not started"
	if p.Status.Phase != corev1.PodPending {
		return "PgBouncer is not running (Pod " + string(p.Status.Phase) + ")"
	}
	for _, condition := range p.Status.Conditions {
		if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
			return reason + " (Pod cannot be scheduled)"
		}
	}
	return reason + " (Pod " + string(p.Status.Phase) + ")"
}

func (s *Reader) PoolerRuntime(ctx context.Context, cache *k8s.ResourceCache, pooler *unstructured.Unstructured) (*CNPGPoolerRuntimeResponse, error) {
	namespace, name := pooler.GetNamespace(), pooler.GetName()
	pods, err := poolerPods(cache, pooler)
	if err != nil {
		log.Printf("[cnpg] Failed to list pooler Pods for %s/%s: %v", namespace, name, err)
		return nil, &ReadFailure{Status: http.StatusServiceUnavailable, Message: "pooler Pods unavailable: " + err.Error()}
	}

	proxyAllowed := s.Access.Permission(ctx, grantGetPodsProxy.In(namespace)) != integration.PermissionDenied
	resp := CNPGPoolerRuntimeResponse{
		Pooler:     CNPGRuntimeObjectRef{Namespace: namespace, Name: name, UID: pooler.GetUID()},
		SampledAt:  time.Now().UTC().Format(time.RFC3339),
		Permission: runtimePermission(namespace, proxyAllowed),
		Pods:       make([]CNPGPoolerPodRuntime, len(pods)),
	}
	for i, p := range pods {
		resp.Pods[i] = CNPGPoolerPodRuntime{Pod: p.Name}
		for _, condition := range p.Status.Conditions {
			if condition.Type == corev1.PodScheduled && condition.Status == corev1.ConditionFalse {
				resp.Pods[i].SchedulingReason = strings.TrimSpace(condition.Reason + ": " + condition.Message)
			}
		}
	}
	if len(pods) == 0 {
		return &resp, nil
	}
	if !proxyAllowed {
		for i := range resp.Pods {
			resp.Pods[i].CNPGRuntimeSource = proxyDenied(namespace)
		}
		return &resp, nil
	}
	client := s.Clients.Proxy
	if client == nil {
		return nil, &ReadFailure{Status: http.StatusServiceUnavailable, Message: "cluster client unavailable"}
	}

	poolerTLS, _, _ := unstructured.NestedBool(pooler.Object, "spec", "monitoring", "tls", "enabled")
	identity := s.Identity
	run := newCNPGRuntimeRunner(ctx)
	for i, p := range pods {
		out := &resp.Pods[i]
		target := proxyTarget{
			namespace: namespace, pod: p.Name, podUID: p.UID, port: poolerMetricsPort, path: metricsPath,
			scheme: schemeFor(containerHasFlag(p, pgBouncerContainer, metricsPortTLSFlag) || poolerTLS), limit: runtimeMetricsCap,
		}
		run.do(func(ctx context.Context) {
			got := memoized(ctx, identity, target, metricsMemoTTL, func(ctx context.Context) CNPGPoolerPodRuntime {
				return poolerPodFrom(proxyGetWithFallback(ctx, client, target))
			})
			got.Pod = p.Name
			got.SchedulingReason = out.SchedulingReason
			if reason := poolerNotStarted(p); reason != "" && got.State != runtimeStateDenied && got.CNPGPoolerPodFacts == nil {
				got.CNPGRuntimeSource = CNPGRuntimeSource{State: runtimeStateUnreachable, Reason: reason}
			}
			*out = got
		})
	}
	run.wait()
	for _, p := range resp.Pods {
		if p.State == runtimeStateDenied {
			resp.Permission.Proxy = "denied"
		}
	}
	return &resp, nil
}
