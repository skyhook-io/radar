package issues

import (
	"fmt"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	gitopsinsights "github.com/skyhook-io/radar/pkg/gitops/insights"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
)

type finalizerObservation struct {
	Text    string
	Running bool
	Healthy bool
}

type finalizerOwnerProvider interface {
	FinalizerOwnerStatus(string, Ref, func(string, string, string) bool) finalizerObservation
}

func (p *CacheProvider) FinalizerOwnerStatus(finalizer string, ref Ref, canList func(string, string, string) bool) finalizerObservation {
	unknown := func(detail string) finalizerObservation {
		return finalizerObservation{Text: "controller unknown (" + detail + ")"}
	}
	if p.cache == nil || p.cache.KindReadinessFor("pods") != k8score.KindReady {
		return unknown("Pod cache is not ready")
	}
	root := &unstructured.Unstructured{}
	root.SetAPIVersion(ref.Group + "/v1")
	root.SetKind(ref.Kind)
	allowed := func(group, resource, namespace string) bool {
		return canList == nil || canList(group, resource, namespace)
	}
	observe := func(controller, namespace string, selector labels.Selector) finalizerObservation {
		if !allowed("", "pods", namespace) {
			return unknown("controller pod inventory is unreadable")
		}
		if !p.cache.KindCoversNamespace("pods", namespace) {
			return unknown("Pod cache does not cover the controller namespace")
		}
		if namespace == "" && !p.cache.IsKindClusterWide("pods") {
			return unknown("Pod cache does not cover the whole cluster")
		}
		var pods []*corev1.Pod
		var err error
		if namespace == "" {
			pods, err = p.cache.Pods().List(selector)
		} else {
			pods, err = p.cache.Pods().Pods(namespace).List(selector)
		}
		if err != nil {
			return unknown("controller pods could not be read")
		}
		var active []*corev1.Pod
		for _, pod := range pods {
			if pod.DeletionTimestamp == nil && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
				active = append(active, pod)
			}
		}
		scope := "across the watched cluster"
		if namespace != "" {
			scope = "in namespace " + namespace
		}
		if len(active) == 0 {
			return finalizerObservation{Text: fmt.Sprintf("%s is not running %s (controller may not be installed, runs elsewhere, or uses different labels)", controller, scope)}
		}
		health := gitopsinsights.SummarizeControllerPods(active)
		return finalizerObservation{Text: gitopsinsights.SummarizeControllerHealth(controller, active) + " " + scope, Running: health.Ready > 0, Healthy: health.Ready == health.Total && health.Total > 0 && health.Crashing == 0}
	}
	var observations []finalizerObservation
	collect := func(resource string, obj metav1.Object, selector *metav1.LabelSelector) {
		if !gitopsinsights.MatchesFinalizerController(finalizer, root, obj) || !allowed("apps", resource, obj.GetNamespace()) {
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
				collect("deployments", workload, workload.Spec.Selector)
			}
		}
	}
	if p.cache.KindReadinessFor("statefulsets") == k8score.KindReady {
		workloads, err := p.cache.StatefulSets().List(labels.Everything())
		if err == nil {
			for _, workload := range workloads {
				collect("statefulsets", workload, workload.Spec.Selector)
			}
		}
	}
	if len(observations) > 0 {
		result := finalizerObservation{}
		var parts []string
		for i, observation := range observations {
			result.Running = result.Running || observation.Running
			result.Healthy = result.Healthy || observation.Healthy
			if i < 3 {
				parts = append(parts, observation.Text)
			}
		}
		if len(observations) > 3 {
			parts = append(parts, fmt.Sprintf("+%d more controller observations", len(observations)-3))
		}
		result.Text = strings.Join(parts, "; ")
		return result
	}
	if owner := gitopsinsights.ResolveFinalizerOwner(finalizer, root); owner != nil {
		return observe(owner.Controller, owner.Namespace, labels.SelectorFromSet(labels.Set{owner.SelectorKey: owner.SelectorValue}))
	}
	return unknown("no confident finalizer-to-controller match")
}

func enrichTerminatingProblem(pr k8s.Detection, p Provider, canList func(string, string, string) bool, memo map[string]finalizerObservation) k8s.Detection {
	if !k8s.HasNonGarbageCollectionFinalizer(pr.TerminatingFinalizers) {
		pr.Cause += " The garbage collector is waiting for dependents."
		pr.Action = "Inspect ownerReferences and deletion-blocking dependents; let the garbage collector finish their deletion."
		return pr
	}
	resolver, available := p.(finalizerOwnerProvider)
	running := false
	healthy := false
	shown := 0
	protected := false
	for _, finalizer := range pr.TerminatingFinalizers {
		if k8s.IsProtectionFinalizer(finalizer) {
			protected = true
			continue
		}
		if finalizer == metav1.FinalizerDeleteDependents || finalizer == metav1.FinalizerOrphanDependents {
			continue
		}
		observation := finalizerObservation{Text: "controller unknown"}
		if available {
			key := pr.Group + "/" + pr.Kind + "/" + finalizer
			var cached bool
			observation, cached = memo[key]
			if !cached {
				observation = resolver.FinalizerOwnerStatus(finalizer, Ref{Group: pr.Group, Kind: pr.Kind, Namespace: pr.Namespace, Name: pr.Name}, canList)
				memo[key] = observation
			}
		}
		running = running || observation.Running
		healthy = healthy || observation.Healthy
		if shown < 3 {
			pr.Cause += fmt.Sprintf(" Finalizer %q: %s.", finalizer, observation.Text)
		}
		shown++
	}
	if shown > 3 {
		pr.Cause += fmt.Sprintf(" +%d more finalizer observations.", shown-3)
	}
	if healthy {
		pr.Severity = "high"
	}
	if running {
		pr.Action = "A matching controller is running and may be working on cleanup. Check its logs, permissions, and deletion-blocking references before taking further action."
		if protected {
			pr.Action += " Kubernetes protection finalizers are in-use guards; resolve objects referencing this resource rather than bypassing them."
		}
	}
	return pr
}

func foldDeletingConditions(in []Issue) []Issue {
	roots := map[string]int{}
	for i, issue := range in {
		if issue.Category == issuesapi.CategoryTerminationStuck {
			roots[resourceKey(issue.Group, issue.Kind, issue.Namespace, issue.Name)] = i
		}
	}
	drop := map[int]bool{}
	for i, issue := range in {
		if issue.Source != SourceCondition || issue.Reason != "Ready: Deleting" {
			continue
		}
		if root, ok := roots[resourceKey(issue.Group, issue.Kind, issue.Namespace, issue.Name)]; ok {
			if issue.Message != "" {
				in[root].Cause += " Ready=False (Deleting): " + issue.Message
			}
			drop[i] = true
		}
	}
	out := make([]Issue, 0, len(in))
	for i, issue := range in {
		if !drop[i] {
			out = append(out, issue)
		}
	}
	return out
}
