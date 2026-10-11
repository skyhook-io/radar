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
	// Absent means a complete search found no workload for the identified
	// controller, or only workloads scaled to zero replicas. It is the only
	// evidence that justifies offering finalizer removal.
	Absent bool
	// Down means the identified controller is installed but has no ready pod
	// (crashlooping, pending, or unable to create pods). It will finish the
	// deletion once fixed, so removing its finalizer would skip cleanup it
	// still intends to do.
	Down bool
	// State describes a Down controller, such as "CrashLoopBackOff (1/1 pods)".
	State string
	// ReleasesInfrastructure means removal strands nodes or cloud resources
	// even while the controller is stopped.
	ReleasesInfrastructure bool
}

// matchedWorkload is what a controller workload declares about its pods.
type matchedWorkload struct {
	desired int32
	// scalable is false for DaemonSets: their pod count follows node
	// placement, so zero desired pods doesn't show an intentional stop.
	scalable bool
}

func replicasOrDefault(replicas *int32) int32 {
	if replicas == nil {
		return 1
	}
	return *replicas
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
	// workload is nil when observing a catalog pod selector with no matched
	// workload. empty reports such a selector-only search that found no
	// active pods.
	observe := func(controller, namespace string, selector labels.Selector, workload *matchedWorkload) (observation finalizerObservation, empty bool) {
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
		if len(active) > 0 {
			health := gitopsinsights.SummarizeControllerPods(active)
			if health.Ready > 0 {
				return finalizerObservation{
					Controller: controller,
					Text:       gitopsinsights.SummarizeControllerHealth(controller, active) + " " + scopeOf(namespace),
					Running:    true,
					Healthy:    health.Ready == health.Total && health.Crashing == 0,
				}, false
			}
		}
		down := func(state string) (finalizerObservation, bool) {
			return finalizerObservation{Controller: controller, Down: true, State: state, Text: fmt.Sprintf("%s is %s %s", controller, state, scopeOf(namespace))}, false
		}
		switch {
		case workload != nil && workload.scalable && workload.desired == 0:
			return finalizerObservation{Controller: controller, Absent: true, Text: fmt.Sprintf("%s is scaled to 0 replicas %s", controller, scopeOf(namespace))}, false
		case len(active) > 0:
			return down(gitopsinsights.DescribeControllerHealth(active))
		case workload != nil && workload.scalable:
			replicas := "replicas"
			if workload.desired == 1 {
				replicas = "replica"
			}
			return down(fmt.Sprintf("not running (no pods for %d desired %s)", workload.desired, replicas))
		case workload != nil:
			return down("not running (no DaemonSet pods)")
		}
		return finalizerObservation{Controller: controller, Absent: true, Text: fmt.Sprintf("%s is not running %s (controller may not be installed, runs elsewhere, or uses different labels)", controller, scopeOf(namespace))}, true
	}
	owner := gitopsinsights.ResolveFinalizerOwner(finalizer, root)
	workloadsSearched := true
	var observations []finalizerObservation
	collect := func(resource string, obj metav1.Object, selector *metav1.LabelSelector, template map[string]string, workload matchedWorkload) {
		// A catalog controller's pod labels identify its workload even when the
		// workload's own name and labels don't.
		catalogPods := owner != nil && template[owner.SelectorKey] == owner.SelectorValue
		if !(catalogPods || gitopsinsights.MatchesFinalizerController(finalizer, root, obj)) || !allowed("apps", resource, obj.GetNamespace()) {
			return
		}
		if selector == nil {
			return
		}
		sel, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil || sel.Empty() {
			return
		}
		observation, _ := observe(obj.GetName(), obj.GetNamespace(), sel, &workload)
		observations = append(observations, observation)
	}
	for _, resource := range []string{"deployments", "statefulsets", "daemonsets"} {
		if p.cache.KindReadinessFor(resource) != k8score.KindReady || !p.cache.IsKindClusterWide(resource) || !allowed("apps", resource, "") {
			workloadsSearched = false
		}
	}
	if p.cache.KindReadinessFor("deployments") == k8score.KindReady {
		workloads, err := p.cache.Deployments().List(labels.Everything())
		if err == nil {
			for _, workload := range workloads {
				collect("deployments", workload, workload.Spec.Selector, workload.Spec.Template.Labels, matchedWorkload{desired: replicasOrDefault(workload.Spec.Replicas), scalable: true})
			}
		} else {
			workloadsSearched = false
		}
	}
	if p.cache.KindReadinessFor("statefulsets") == k8score.KindReady {
		workloads, err := p.cache.StatefulSets().List(labels.Everything())
		if err == nil {
			for _, workload := range workloads {
				collect("statefulsets", workload, workload.Spec.Selector, workload.Spec.Template.Labels, matchedWorkload{desired: replicasOrDefault(workload.Spec.Replicas), scalable: true})
			}
		} else {
			workloadsSearched = false
		}
	}
	if p.cache.KindReadinessFor("daemonsets") == k8score.KindReady {
		workloads, err := p.cache.DaemonSets().List(labels.Everything())
		if err == nil {
			for _, workload := range workloads {
				collect("daemonsets", workload, workload.Spec.Selector, workload.Spec.Template.Labels, matchedWorkload{})
			}
		} else {
			workloadsSearched = false
		}
	}
	releasesInfrastructure := owner != nil && owner.ReleasesInfrastructure
	// Catalog pods are searched cluster-wide even when the catalog names a
	// conventional namespace: a controller can't be called absent while a
	// catalog-labelled pod might run in a namespace Radar didn't look in.
	var catalogPods labels.Selector
	if owner != nil {
		catalogPods = labels.SelectorFromSet(labels.Set{owner.SelectorKey: owner.SelectorValue})
	}
	// A controller seen absent or down in the visible namespaces may still run
	// healthy in ones Radar could not search.
	unsearched := ", but Deployments, StatefulSets and DaemonSets could not be searched cluster-wide"
	if len(observations) > 0 {
		result := finalizerObservation{Controller: observations[0].Controller, Absent: true, ReleasesInfrastructure: releasesInfrastructure}
		var down *finalizerObservation
		unknown := false
		// Healthy means every matched copy that should be running is fully
		// ready; a crashlooping or partly ready copy beside a healthy one isn't.
		anyHealthy, degraded := false, false
		var parts []string
		for i, observation := range observations {
			result.Running = result.Running || observation.Running
			anyHealthy = anyHealthy || observation.Healthy
			degraded = degraded || observation.Down || (observation.Running && !observation.Healthy)
			result.Absent = result.Absent && observation.Absent
			switch {
			case observation.Down && down == nil:
				down = &observations[i]
			case !observation.Running && !observation.Absent && !observation.Down:
				unknown = true
			}
			if i < 3 {
				parts = append(parts, observation.Text)
			}
		}
		if len(observations) > 3 {
			parts = append(parts, fmt.Sprintf("+%d more controller observations", len(observations)-3))
		}
		result.Text = strings.Join(parts, "; ")
		result.Healthy = anyHealthy && !degraded
		if down != nil && !result.Running && !unknown {
			result.Down, result.Controller, result.State = true, down.Controller, down.State
		}
		// Catalog controller pods outside the matched workloads, such as a
		// DaemonSet or another install, may still be running.
		if owner != nil && (result.Absent || result.Down) {
			catalog, _ := observe(owner.Controller, "", catalogPods, nil)
			switch {
			case catalog.Running:
				result.Absent, result.Down, result.Running, result.Healthy = false, false, true, catalog.Healthy && !degraded
				result.Text += "; " + catalog.Text
			case catalog.Down && result.Absent:
				result.Absent, result.Down, result.Controller, result.State = false, true, owner.Controller, catalog.State
				result.Text += "; " + catalog.Text
			case !catalog.Absent && !catalog.Down:
				result.Absent, result.Down = false, false
				result.Text += fmt.Sprintf("; %s pods %s could not be checked", owner.Controller, scopeOf(""))
			}
		}
		// A stopped in-cluster copy says nothing about a managed install that
		// runs outside the cluster.
		if result.Absent && owner != nil && owner.OffClusterOfferings != "" {
			result.Absent = false
			result.Text += fmt.Sprintf("; %s may run outside the cluster (%s)", owner.Controller, owner.OffClusterOfferings)
		}
		if (result.Absent || result.Down) && !workloadsSearched {
			result.Absent, result.Down = false, false
			result.Text += unsearched
		}
		return result
	}
	if owner == nil {
		return unknown("no confident finalizer-to-controller match")
	}
	observation, empty := observe(owner.Controller, "", catalogPods, nil)
	switch {
	case empty && owner.OffClusterOfferings != "":
		observation = finalizerObservation{Controller: owner.Controller, Text: fmt.Sprintf("%s has no pods %s; it may run outside the cluster (%s)", owner.Controller, scopeOf(""), owner.OffClusterOfferings)}
	case empty && !workloadsSearched:
		observation = finalizerObservation{Controller: owner.Controller, Text: fmt.Sprintf("%s has no pods %s", owner.Controller, scopeOf("")) + unsearched}
	case observation.Down && !workloadsSearched:
		observation.Down = false
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
	// down covers identified controllers seen absent or installed but not
	// ready; either justifies escalating to critical.
	down := false
	shown := 0
	var fixes, guards, unconfirmed, unidentified, removals []string
	for _, finalizer := range pr.TerminatingFinalizers {
		if finalizer == metav1.FinalizerDeleteDependents || finalizer == metav1.FinalizerOrphanDependents {
			continue
		}
		if k8s.IsProtectionFinalizer(finalizer) {
			guards = append(guards, fmt.Sprintf("Finalizer %q is an in-use guard. Find and resolve the objects that still use or reference this %s; do not bypass the guard.", finalizer, pr.Kind))
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
		case observation.Down:
			down = true
			fixes = append(fixes, fmt.Sprintf("Fix %s, which is %s; it will finish the deletion once running. Removing finalizer %q would skip its cleanup.", observation.Controller, observation.State, finalizer))
		case observation.Absent && observation.ReleasesInfrastructure:
			down = true
			unconfirmed = append(unconfirmed, fmt.Sprintf("Get %s running again rather than removing finalizer %q.", observation.Controller, finalizer))
		case observation.Absent:
			down = true
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
	if pr.Severity == "critical" && (healthy || (!running && !down && len(guards) == 0)) {
		pr.Severity = "high"
	}
	if running {
		pr.Action = "A matching controller is running and may be working on cleanup. Check its logs, permissions, and deletion-blocking references before taking further action."
		if len(guards) > 0 {
			pr.Action += " Kubernetes protection finalizers are in-use guards; resolve objects referencing this resource rather than bypassing them."
		}
		return pr
	}
	parts := fixes
	var cautions []string
	if len(unidentified) > 0 {
		subject := "finalizer " + unidentified[0]
		if len(unidentified) > 1 {
			subject = "finalizers " + strings.Join(unidentified, ", ")
		}
		cautions = append(cautions, fmt.Sprintf("Radar couldn't identify which controller owns %s; the finalizer's domain usually names it. Find that controller and check it first.", subject))
	}
	cautions = append(cautions, unconfirmed...)
	if len(cautions) > 0 {
		parts = append(parts, cautions...)
		parts = append(parts, "Removing a finalizer skips its cleanup and can orphan external resources such as cloud infrastructure, volumes or nodes.")
	}
	if len(removals) > 0 {
		parts = append(parts, "If the controller should still be running, restore it; it will finish the deletion. Only if it was intentionally removed or scaled down, consider removing its finalizer; this skips its cleanup and may leave external resources behind.")
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
