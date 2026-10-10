package issues

import (
	"fmt"
	"strings"

	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

type finalizerOwnerProvider interface {
	FinalizerOwnerStatus(string, Ref, func(Ref) bool) string
}

func (p *CacheProvider) FinalizerOwnerStatus(finalizer string, ref Ref, canRead func(Ref) bool) string {
	const unknown = "controller unknown"
	if p.cache == nil || p.cache.KindReadinessFor("pods") != k8score.KindReady {
		return unknown + " (Pod cache is not ready)"
	}
	root := &unstructured.Unstructured{}
	root.SetAPIVersion(ref.Group + "/v1")
	root.SetKind(ref.Kind)
	allowed := func(ref Ref) bool { return canRead == nil || canRead(ref) }
	observe := func(controller, namespace string, selector labels.Selector) string {
		if !allowed(Ref{Kind: "Pod", Namespace: namespace}) {
			return unknown + " (controller pod inventory is unreadable)"
		}
		if !p.cache.KindCoversNamespace("pods", namespace) {
			return unknown + " (Pod cache does not cover the controller namespace)"
		}
		if namespace == "" && !p.cache.IsKindClusterWide("pods") {
			return unknown + " (Pod cache does not cover the whole cluster)"
		}
		var pods []*corev1.Pod
		var err error
		if namespace == "" {
			pods, err = p.cache.Pods().List(selector)
		} else {
			pods, err = p.cache.Pods().Pods(namespace).List(selector)
		}
		if err != nil {
			return unknown + " (controller pods could not be read)"
		}
		running := 0
		for _, pod := range pods {
			if pod.Status.Phase == corev1.PodRunning && pod.DeletionTimestamp == nil {
				running++
			}
		}
		domain, _, _ := strings.Cut(finalizer, "/")
		scope := "across the watched cluster"
		if namespace != "" {
			scope = "in namespace " + namespace
		}
		if running == 0 {
			return fmt.Sprintf("no running pods found matching the controller for %s (%s, %s); it may run elsewhere or use different labels", domain, controller, scope)
		}
		return fmt.Sprintf("%d running pod(s) found matching %s %s; this does not establish that finalizer cleanup is working", running, controller, scope)
	}
	var observations []string
	collect := func(kind string, obj metav1.Object, selector *metav1.LabelSelector) {
		if !gitopsinsights.MatchesFinalizerController(finalizer, root, obj) || !allowed(Ref{Group: "apps", Kind: kind, Namespace: obj.GetNamespace(), Name: obj.GetName()}) {
			return
		}
		if selector == nil {
			return
		}
		sel, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil || sel.Empty() {
			return
		}
		observations = append(observations, observe(obj.GetName(), obj.GetNamespace(), sel))
	}
	if p.cache.KindReadinessFor("deployments") == k8score.KindReady {
		workloads, err := p.cache.Deployments().List(labels.Everything())
		if err == nil {
			for _, workload := range workloads {
				collect("Deployment", workload, workload.Spec.Selector)
			}
		}
	}
	if p.cache.KindReadinessFor("statefulsets") == k8score.KindReady {
		workloads, err := p.cache.StatefulSets().List(labels.Everything())
		if err == nil {
			for _, workload := range workloads {
				collect("StatefulSet", workload, workload.Spec.Selector)
			}
		}
	}
	if len(observations) > 0 {
		return strings.Join(observations, "; ")
	}
	if owner := gitopsinsights.ResolveFinalizerOwner(finalizer, root); owner != nil {
		return observe(owner.Controller, owner.Namespace, labels.SelectorFromSet(labels.Set{owner.SelectorKey: owner.SelectorValue}))
	}
	return unknown + " (no confident finalizer-to-controller match)"
}
