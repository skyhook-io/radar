package issues

import (
	"fmt"
	"strconv"
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
	Text string
	// Controller names the identified controller; empty when Radar could not
	// tell which controller owns the finalizer.
	Controller string
	Running    bool
	Healthy    bool
	// Stopped means the identified controller was found not running wherever
	// it could be. It is the only evidence that justifies offering finalizer
	// removal.
	Stopped bool
	// ReleasesInfrastructure means removal strands nodes or cloud resources
	// even while the controller is stopped.
	ReleasesInfrastructure bool
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
	scopeOf := func(namespace string) string {
		if namespace == "" {
			return "across the watched cluster"
		}
		return "in namespace " + namespace
	}
	// empty reports a complete search that found no active controller pods.
	observe := func(controller, namespace string, selector labels.Selector) (observation finalizerObservation, empty bool) {
		unreadable := func(detail string) (finalizerObservation, bool) {
			o := unknown(detail)
			o.Controller = controller
			return o, false
		}
		if !allowed("", "pods", namespace) {
			return unreadable("controller pod inventory is unreadable")
		}
		if !p.cache.KindCoversNamespace("pods", namespace) {
			return unreadable("Pod cache does not cover the controller namespace")
		}
		if namespace == "" && !p.cache.IsKindClusterWide("pods") {
			return unreadable("Pod cache does not cover the whole cluster")
		}
		var pods []*corev1.Pod
		var err error
		if namespace == "" {
			pods, err = p.cache.Pods().List(selector)
		} else {
			pods, err = p.cache.Pods().Pods(namespace).List(selector)
		}
		if err != nil {
			return unreadable("controller pods could not be read")
		}
		var active []*corev1.Pod
		for _, pod := range pods {
			if pod.DeletionTimestamp == nil && pod.Status.Phase != corev1.PodSucceeded && pod.Status.Phase != corev1.PodFailed {
				active = append(active, pod)
			}
		}
		if len(active) == 0 {
			return finalizerObservation{Controller: controller, Stopped: true, Text: fmt.Sprintf("%s is not running %s (controller may not be installed, runs elsewhere, or uses different labels)", controller, scopeOf(namespace))}, true
		}
		health := gitopsinsights.SummarizeControllerPods(active)
		return finalizerObservation{
			Controller: controller,
			Text:       gitopsinsights.SummarizeControllerHealth(controller, active) + " " + scopeOf(namespace),
			Running:    health.Ready > 0,
			Healthy:    health.Ready == health.Total && health.Total > 0 && health.Crashing == 0,
			Stopped:    health.Ready == 0,
		}, false
	}
	owner := gitopsinsights.ResolveFinalizerOwner(finalizer, root)
	workloadsSearched := true
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
		observation, _ := observe(obj.GetName(), obj.GetNamespace(), sel)
		observations = append(observations, observation)
	}
	for _, resource := range []string{"deployments", "statefulsets"} {
		if p.cache.KindReadinessFor(resource) != k8score.KindReady || !p.cache.IsKindClusterWide(resource) || !allowed("apps", resource, "") {
			workloadsSearched = false
		}
	}
	if p.cache.KindReadinessFor("deployments") == k8score.KindReady {
		workloads, err := p.cache.Deployments().List(labels.Everything())
		if err == nil {
			for _, workload := range workloads {
				collect("deployments", workload, workload.Spec.Selector)
			}
		} else {
			workloadsSearched = false
		}
	}
	if p.cache.KindReadinessFor("statefulsets") == k8score.KindReady {
		workloads, err := p.cache.StatefulSets().List(labels.Everything())
		if err == nil {
			for _, workload := range workloads {
				collect("statefulsets", workload, workload.Spec.Selector)
			}
		} else {
			workloadsSearched = false
		}
	}
	releasesInfrastructure := owner != nil && owner.ReleasesInfrastructure
	// A controller seen down in the visible namespaces may still run healthy
	// in ones Radar could not search.
	unsearched := ", but Deployments and StatefulSets could not be searched cluster-wide"
	if len(observations) > 0 {
		result := finalizerObservation{Controller: observations[0].Controller, Stopped: true, ReleasesInfrastructure: releasesInfrastructure}
		var parts []string
		for i, observation := range observations {
			result.Running = result.Running || observation.Running
			result.Healthy = result.Healthy || observation.Healthy
			result.Stopped = result.Stopped && observation.Stopped
			if i < 3 {
				parts = append(parts, observation.Text)
			}
		}
		if len(observations) > 3 {
			parts = append(parts, fmt.Sprintf("+%d more controller observations", len(observations)-3))
		}
		result.Text = strings.Join(parts, "; ")
		if result.Stopped && !workloadsSearched {
			result.Stopped = false
			result.Text += unsearched
		}
		return result
	}
	if owner == nil {
		return unknown("no confident finalizer-to-controller match")
	}
	observation, empty := observe(owner.Controller, owner.Namespace, labels.SelectorFromSet(labels.Set{owner.SelectorKey: owner.SelectorValue}))
	switch {
	case empty && owner.OffClusterOfferings != "":
		observation = finalizerObservation{Controller: owner.Controller, Text: fmt.Sprintf("%s has no pods %s; it may run outside the cluster (%s)", owner.Controller, scopeOf(owner.Namespace), owner.OffClusterOfferings)}
	case empty && !workloadsSearched:
		observation = finalizerObservation{Controller: owner.Controller, Text: fmt.Sprintf("%s has no pods %s", owner.Controller, scopeOf(owner.Namespace)) + unsearched}
	case observation.Stopped && !workloadsSearched:
		observation.Stopped = false
		observation.Text += unsearched
	}
	observation.ReleasesInfrastructure = releasesInfrastructure
	return observation
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
	stopped := false
	shown := 0
	var guards, unconfirmed, unidentified, removals []string
	for _, finalizer := range pr.TerminatingFinalizers {
		if finalizer == metav1.FinalizerDeleteDependents || finalizer == metav1.FinalizerOrphanDependents {
			continue
		}
		if k8s.IsProtectionFinalizer(finalizer) {
			guards = append(guards, fmt.Sprintf("Finalizer %q is an in-use guard. Find and resolve objects referencing this %s (for example Gateways referencing a GatewayClass, or VolumeSnapshots referencing snapshot content); do not bypass the guard.", finalizer, pr.Kind))
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
		switch {
		case observation.Running:
		case observation.Stopped && observation.ReleasesInfrastructure:
			stopped = true
			unconfirmed = append(unconfirmed, fmt.Sprintf("Get %s running again rather than removing finalizer %q.", observation.Controller, finalizer))
		case observation.Stopped:
			stopped = true
			if preview := pr.TerminatingRemovalPreviews[finalizer]; preview != "" {
				removals = append(removals, preview)
			}
		case observation.Controller != "":
			unconfirmed = append(unconfirmed, fmt.Sprintf("Radar can't confirm from this cluster that %s, which owns finalizer %q, is gone; check it first.", observation.Controller, finalizer))
		default:
			unidentified = append(unidentified, strconv.Quote(finalizer))
		}
		if shown < 3 {
			pr.Cause += fmt.Sprintf(" Finalizer %q: %s.", finalizer, observation.Text)
		}
		shown++
	}
	if shown > 3 {
		pr.Cause += fmt.Sprintf(" +%d more finalizer observations.", shown-3)
	}
	// Escalating to critical needs a controller seen down or an in-use guard;
	// an unconfirmed controller may simply still be cleaning up.
	if pr.Severity == "critical" && (healthy || (!running && !stopped && len(guards) == 0)) {
		pr.Severity = "high"
	}
	if running {
		pr.Action = "A matching controller is running and may be working on cleanup. Check its logs, permissions, and deletion-blocking references before taking further action."
		if len(guards) > 0 {
			pr.Action += " Kubernetes protection finalizers are in-use guards; resolve objects referencing this resource rather than bypassing them."
		}
		return pr
	}
	var parts []string
	if len(unidentified) > 0 {
		subject := "finalizer " + unidentified[0]
		if len(unidentified) > 1 {
			subject = "finalizers " + strings.Join(unidentified, ", ")
		}
		parts = append(parts, fmt.Sprintf("Radar couldn't identify which controller owns %s; the finalizer's domain usually names it. Find that controller and check it first.", subject))
	}
	parts = append(parts, unconfirmed...)
	if len(parts) > 0 {
		parts = append(parts, "Removing a finalizer skips its cleanup and can orphan external resources such as cloud infrastructure, volumes or nodes.")
	}
	if len(removals) > 0 {
		parts = append(parts, "Check the controller's logs and permissions first. Only if the controller is intentionally removed, consider removing its finalizer; this skips its cleanup and may leave external resources behind.")
		parts = append(parts, removals...)
		parts = append(parts, "Review the preview before applying with dry_run=false. Remove only one finalizer at a time and re-read the object before preparing the next patch; indices can change.")
	}
	parts = append(parts, guards...)
	if len(parts) > 0 {
		pr.Action = strings.Join(parts, " ")
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
