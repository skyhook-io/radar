package issues

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/finalizers"
	"github.com/skyhook-io/radar/pkg/health"
	"github.com/skyhook-io/radar/pkg/issuesapi"
	"github.com/skyhook-io/radar/pkg/k8score"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
)

type finalizerObservation struct {
	Text string
	// Controller names the catalog controller that owns the finalizer; empty
	// when Radar could not identify it.
	Controller string
	// Candidate names a workload that only resembles the finalizer's API group
	// by name. It is never treated as the owner.
	Candidate string
	Running   bool
	Healthy   bool
	// NotFound means a complete search recognized no ready or installed copy
	// of the catalog controller: nothing matched its name, labels or image, or
	// every match is scaled to 0 replicas (ScaledDown). A negative search is
	// not proof the controller is gone: it may be stopped for now, installed
	// in a way Radar doesn't recognize, or running from outside the cluster.
	// So it keeps the issue at warning and supports only a conditional
	// cleanup.
	NotFound   bool
	ScaledDown bool
	// Down means the identified controller is installed but has no ready pod
	// (crashlooping, pending, or unable to create pods). Removing its finalizer would skip
	// cleanup it still intends to do.
	Down bool
	// State describes a Down controller, such as "CrashLoopBackOff (1/1 pods)".
	State string
	// ReleasesInfrastructure means removal strands nodes or cloud resources
	// even while the controller is stopped.
	ReleasesInfrastructure  bool
	AllowConditionalCleanup bool
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

func unknownController(detail string) finalizerObservation {
	return finalizerObservation{Text: "controller unknown (" + detail + ")"}
}

func scopeOf(namespace string) string {
	if namespace == "" {
		return "across the watched cluster"
	}
	return "in namespace " + namespace
}

// A controller seen missing or down in the visible namespaces may still run
// healthy in ones Radar could not search.
const workloadsUnsearched = ", but Deployments, StatefulSets and DaemonSets could not be searched cluster-wide"

func (p *CacheProvider) FinalizerOwnerStatus(finalizer string, ref Ref, canList func(string, string, string) bool) finalizerObservation {
	root := &unstructured.Unstructured{}
	root.SetAPIVersion(ref.Group + "/v1")
	root.SetKind(ref.Kind)
	owner := finalizers.ResolveOwner(finalizer, root)
	if p.cache == nil || p.cache.KindReadinessFor("pods") != k8score.KindReady {
		if owner != nil {
			// The catalog identity is static; only the controller's state is
			// unavailable.
			return finalizerObservation{Controller: owner.Controller, ReleasesInfrastructure: owner.ReleasesInfrastructure, Text: owner.Controller + " state unknown (Pod cache is not ready)"}
		}
		return unknownController("Pod cache is not ready")
	}
	inventory := &controllerInventory{cache: p.cache, canList: canList, attributed: map[types.NamespacedName]bool{}}
	if owner == nil {
		return inventory.candidate(finalizer, root)
	}
	return inventory.catalogController(owner)
}

// controllerInventory reads controller workloads and pods from the cache,
// limited to what the caller can list.
type controllerInventory struct {
	cache   *k8s.ResourceCache
	canList func(group, resource, namespace string) bool
	// attributed holds the active pods already observed through a matched
	// workload, so the catalog's cluster-wide pod search covers the rest.
	attributed map[types.NamespacedName]bool
}

func (inv *controllerInventory) allowed(group, resource, namespace string) bool {
	return inv.canList == nil || inv.canList(group, resource, namespace)
}

// eachWorkload calls visit for every cached Deployment, StatefulSet and
// DaemonSet the caller can list. It reports whether that covered every such
// workload in the cluster.
func (inv *controllerInventory) eachWorkload(visit func(obj metav1.Object, selector *metav1.LabelSelector, template corev1.PodTemplateSpec, workload matchedWorkload)) bool {
	complete := true
	ready := func(resource string) bool {
		if inv.cache.KindReadinessFor(resource) != k8score.KindReady {
			complete = false
			return false
		}
		if !inv.cache.KindAbsenceVerifiable(resource, "") || !inv.allowed("apps", resource, "") {
			complete = false
		}
		return true
	}
	if ready("deployments") {
		workloads, err := inv.cache.Deployments().List(labels.Everything())
		complete = complete && err == nil
		for _, w := range workloads {
			if inv.allowed("apps", "deployments", w.Namespace) {
				visit(w, w.Spec.Selector, w.Spec.Template, matchedWorkload{desired: replicasOrDefault(w.Spec.Replicas), scalable: true})
			}
		}
	}
	if ready("statefulsets") {
		workloads, err := inv.cache.StatefulSets().List(labels.Everything())
		complete = complete && err == nil
		for _, w := range workloads {
			if inv.allowed("apps", "statefulsets", w.Namespace) {
				visit(w, w.Spec.Selector, w.Spec.Template, matchedWorkload{desired: replicasOrDefault(w.Spec.Replicas), scalable: true})
			}
		}
	}
	if ready("daemonsets") {
		workloads, err := inv.cache.DaemonSets().List(labels.Everything())
		complete = complete && err == nil
		for _, w := range workloads {
			if inv.allowed("apps", "daemonsets", w.Namespace) {
				visit(w, w.Spec.Selector, w.Spec.Template, matchedWorkload{})
			}
		}
	}
	return complete
}

// observe reports the controller pods that selector and match select in
// namespace ("" for cluster-wide). workload is nil for the catalog's pod
// search, which skips pods already observed through a matched workload;
// empty then reports that it found no active pods.
func (inv *controllerInventory) observe(controller, namespace string, selector labels.Selector, match func(*corev1.Pod) bool, workload *matchedWorkload) (observation finalizerObservation, empty bool) {
	unreadable := func(detail string) (finalizerObservation, bool) {
		return finalizerObservation{Controller: controller, Text: controller + " state unknown (" + detail + ")"}, false
	}
	if !inv.allowed("", "pods", namespace) {
		return unreadable("controller pod inventory is unreadable")
	}
	if namespace == "" && !inv.cache.IsKindClusterWide("pods") {
		return unreadable("Pod cache does not cover the whole cluster")
	}
	if !inv.cache.KindCoversNamespace("pods", namespace) {
		return unreadable("Pod cache does not cover the controller namespace")
	}
	if !inv.cache.KindAbsenceVerifiable("pods", namespace) {
		return unreadable("Pod cache is refreshed by periodic list, not a watch")
	}
	var pods []*corev1.Pod
	var err error
	if namespace == "" {
		pods, err = inv.cache.Pods().List(selector)
	} else {
		pods, err = inv.cache.Pods().Pods(namespace).List(selector)
	}
	if err != nil {
		return unreadable("controller pods could not be read")
	}
	var active []*corev1.Pod
	for _, pod := range pods {
		if pod.DeletionTimestamp != nil || pod.Status.Phase == corev1.PodSucceeded || pod.Status.Phase == corev1.PodFailed {
			continue
		}
		if match != nil && !match(pod) {
			continue
		}
		key := types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name}
		if workload == nil && inv.attributed[key] {
			continue
		}
		if workload != nil {
			inv.attributed[key] = true
		}
		active = append(active, pod)
	}
	if len(active) > 0 {
		podHealth := health.SummarizeControllerPods(active)
		if podHealth.Ready > 0 {
			return finalizerObservation{
				Controller: controller,
				Text:       health.SummarizeControllerHealth(controller, active) + " " + scopeOf(namespace),
				Running:    true,
				Healthy:    podHealth.Ready == podHealth.Total && podHealth.Crashing == 0,
			}, false
		}
	}
	down := func(state string) (finalizerObservation, bool) {
		return finalizerObservation{Controller: controller, Down: true, State: state, Text: fmt.Sprintf("%s is %s %s", controller, state, scopeOf(namespace))}, false
	}
	switch {
	case workload != nil && workload.scalable && workload.desired == 0:
		return finalizerObservation{Controller: controller, NotFound: true, ScaledDown: true, Text: fmt.Sprintf("%s is scaled to 0 replicas %s", controller, scopeOf(namespace))}, false
	case len(active) > 0:
		return down(health.DescribeControllerHealth(active))
	case workload != nil && workload.scalable:
		replicas := "replicas"
		if workload.desired == 1 {
			replicas = "replica"
		}
		return down(fmt.Sprintf("not running (no pods for %d desired %s)", workload.desired, replicas))
	case workload != nil:
		return down("not running (no DaemonSet pods)")
	}
	return finalizerObservation{Controller: controller}, true
}

// catalogController observes a catalog owner's controller wherever it runs:
// workloads recognized by name, labels, pod template labels or image, and
// catalog pods outside them (another install, a bare pod).
func (inv *controllerInventory) catalogController(owner *finalizers.Owner) finalizerObservation {
	var observations []finalizerObservation
	unconfirmed := finalizerObservation{Controller: owner.Controller, Text: owner.Controller + " state unknown (matching name or labels, but unconfirmed image identity)"}
	searched := inv.eachWorkload(func(obj metav1.Object, selector *metav1.LabelSelector, template corev1.PodTemplateSpec, workload matchedWorkload) {
		if !owner.MatchesWorkload(obj, template) {
			if owner.MatchesWorkloadIdentity(obj, template) {
				observations = append(observations, unconfirmed)
			}
			return
		}
		if selector == nil {
			return
		}
		sel, err := metav1.LabelSelectorAsSelector(selector)
		if err != nil || sel.Empty() {
			return
		}
		observation, _ := inv.observe(obj.GetName(), obj.GetNamespace(), sel, nil, &workload)
		observations = append(observations, observation)
	})
	// Searched even when a matched workload is healthy: a failing copy
	// elsewhere must keep the controller from reading as healthy.
	catalog, empty := inv.observe(owner.Controller, "", labels.Everything(), func(pod *corev1.Pod) bool {
		if owner.MatchesPod(pod.Labels, pod.Spec) {
			return true
		}
		if pod.Labels[owner.SelectorKey] == owner.SelectorValue {
			observations = append(observations, unconfirmed)
		}
		return false
	}, nil)
	var result finalizerObservation
	switch {
	case len(observations) == 0 && !empty:
		result = catalog
	case len(observations) == 0 && owner.OffClusterOfferings != "":
		return finalizerObservation{Controller: owner.Controller, ReleasesInfrastructure: owner.ReleasesInfrastructure, Text: fmt.Sprintf("%s has no pods %s; it may run outside the cluster (%s)", owner.Controller, scopeOf(""), owner.OffClusterOfferings)}
	case len(observations) == 0:
		result = finalizerObservation{Controller: owner.Controller, NotFound: true, Text: fmt.Sprintf("no recognized controller found: no workload or pod %s matches %s by name, labels or image", scopeOf(""), owner.Controller)}
		if !searched {
			result = finalizerObservation{Controller: owner.Controller, Text: fmt.Sprintf("%s has no pods %s", owner.Controller, scopeOf("")) + workloadsUnsearched}
		}
	default:
		if !empty {
			if !catalog.Running && !catalog.Down {
				catalog.Text = fmt.Sprintf("%s pods %s could not be checked", owner.Controller, scopeOf(""))
			}
			observations = append(observations, catalog)
		}
		result = mergeControllerObservations(observations)
	}
	result.ReleasesInfrastructure = owner.ReleasesInfrastructure
	result.AllowConditionalCleanup = owner.AllowConditionalCleanup
	if (result.NotFound || result.Down) && !searched {
		result.NotFound, result.ScaledDown, result.Down = false, false, false
		result.Text += workloadsUnsearched
	}
	// A stopped in-cluster copy says nothing about a managed install that
	// runs outside the cluster.
	if result.NotFound && owner.OffClusterOfferings != "" {
		result.NotFound, result.ScaledDown = false, false
		result.Text += fmt.Sprintf("; %s may run outside the cluster (%s)", owner.Controller, owner.OffClusterOfferings)
	}
	return result
}

// mergeControllerObservations folds every observed copy of one controller.
// Healthy means every copy that should be running is fully ready: a
// crashlooping or partly ready copy beside a healthy one isn't. Down and
// NotFound need every copy to agree; an unreadable copy may be the running
// one.
func mergeControllerObservations(observations []finalizerObservation) finalizerObservation {
	result := finalizerObservation{Controller: observations[0].Controller}
	running, degraded, unknown, notFound := false, false, false, true
	var down *finalizerObservation
	var parts []string
	for i, observation := range observations {
		running = running || observation.Running
		degraded = degraded || observation.Down || (observation.Running && !observation.Healthy)
		notFound = notFound && observation.NotFound
		switch {
		case observation.Down && down == nil:
			down = &observations[i]
		case !observation.Running && !observation.NotFound && !observation.Down:
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
	switch {
	case running:
		result.Running, result.Healthy = true, !degraded
	case unknown:
	case down != nil:
		result.Down, result.Controller, result.State = true, down.Controller, down.State
	case notFound:
		// Only matched workloads report NotFound, and only when scaled to 0.
		result.NotFound, result.ScaledDown = true, true
	}
	return result
}

// candidate names a workload that only resembles an uncataloged finalizer's
// API group. It describes the candidate's state for the reader but claims
// nothing about ownership.
func (inv *controllerInventory) candidate(finalizer string, root *unstructured.Unstructured) finalizerObservation {
	var names, states []string
	inv.eachWorkload(func(obj metav1.Object, selector *metav1.LabelSelector, _ corev1.PodTemplateSpec, workload matchedWorkload) {
		if !finalizers.ControllerCandidate(finalizer, root, obj) {
			return
		}
		names = append(names, obj.GetName())
		if len(states) == 2 {
			return
		}
		state := obj.GetName() + " " + scopeOf(obj.GetNamespace())
		if sel, err := metav1.LabelSelectorAsSelector(selector); selector != nil && err == nil && !sel.Empty() {
			observation, _ := inv.observe(obj.GetName(), obj.GetNamespace(), sel, nil, &workload)
			state = observation.Text
		}
		states = append(states, state)
	})
	if len(names) == 0 {
		return unknownController("no confident finalizer-to-controller match")
	}
	if len(names) > len(states) {
		states = append(states, fmt.Sprintf("+%d more", len(names)-len(states)))
	}
	return finalizerObservation{Candidate: names[0], Text: "no confident owner (name-only candidate: " + strings.Join(states, "; ") + ")"}
}

func enrichTerminatingProblem(pr k8s.Detection, p Provider, canList func(string, string, string) bool, memo map[string]finalizerObservation) k8s.Detection {
	if !k8s.HasNonGarbageCollectionFinalizer(pr.TerminatingFinalizers) {
		if slices.Contains(pr.TerminatingFinalizers, metav1.FinalizerDeleteDependents) {
			pr.Cause += " The garbage collector is waiting for dependents."
			pr.Action = "Inspect ownerReferences and deletion-blocking dependents; let the garbage collector finish their deletion."
			return pr
		}
		pr.Cause += " The garbage collector is orphaning its dependents: it removes their ownerReferences and keeps them."
		pr.Action = "Orphaning preserves the dependents; none of them will be deleted. If it stays stuck, check the garbage collector in kube-controller-manager and whether the dependents' ownerReferences can be updated."
		return pr
	}
	resolver, available := p.(finalizerOwnerProvider)
	running := false
	healthy := false
	// down covers identified controllers that are installed but not ready;
	// it justifies escalating to critical.
	down := false
	shown := 0
	var fixes, restores, guards, unconfirmed, candidates, unidentified []string
	var cleanups []finalizerCleanup
	root := &unstructured.Unstructured{}
	root.SetAPIVersion(pr.Group + "/v1")
	root.SetKind(pr.Kind)
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
			key := pr.Group + "/" + finalizer
			if owner := finalizers.ResolveOwner(finalizer, root); owner != nil {
				key = "controller/" + owner.Controller
			}
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
			fixes = append(fixes, fmt.Sprintf("Fix %s, which is %s, so it can resume cleanup. Removing finalizer %q would skip its cleanup.", observation.Controller, observation.State, finalizer))
		case observation.NotFound && (observation.ReleasesInfrastructure || !observation.AllowConditionalCleanup):
			unconfirmed = append(unconfirmed, fmt.Sprintf("Get %s running again rather than removing finalizer %q.", observation.Controller, finalizer))
		case observation.NotFound && observation.ScaledDown:
			restores = append(restores, fmt.Sprintf("No running controller found for finalizer %q: %s. If it was stopped temporarily, scale it back up so it can resume cleanup.", finalizer, observation.Text))
			if removal, ok := pr.TerminatingRemovals[finalizer]; ok && observation.AllowConditionalCleanup {
				cleanups = append(cleanups, finalizerCleanup{condition: observation.Controller + " was scaled to 0 on purpose and won't be restarted", removal: removal})
			}
		case observation.NotFound:
			restores = append(restores, fmt.Sprintf("No recognized controller found for finalizer %q. If %s should be running, restore it so it can resume cleanup. It may also be installed under a name, labels or image Radar doesn't recognize, or run outside this cluster.", finalizer, observation.Controller))
			if removal, ok := pr.TerminatingRemovals[finalizer]; ok && observation.AllowConditionalCleanup {
				cleanups = append(cleanups, finalizerCleanup{condition: observation.Controller + " was uninstalled on purpose", removal: removal})
			}
		case observation.Controller != "":
			unconfirmed = append(unconfirmed, fmt.Sprintf("Radar can't confirm with your access to this cluster whether %s, which owns finalizer %q, is running; check it first.", observation.Controller, finalizer))
		case observation.Candidate != "":
			candidates = append(candidates, fmt.Sprintf("Radar couldn't confirm which controller owns finalizer %q; %s resembles its API group by name only. Check whether it handles this finalizer first.", finalizer, observation.Candidate))
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
	// Escalating to critical needs a controller seen down or an in-use guard.
	// A controller Radar didn't find may be stopped on purpose, installed in a
	// way Radar doesn't recognize, or still cleaning up from elsewhere.
	if pr.Severity == "critical" && (healthy || (!running && !down && len(guards) == 0)) {
		pr.Severity = "high"
	}
	parts := slices.Concat(fixes, restores)
	if running {
		parts = append([]string{"A matching controller is running and may be working on cleanup. Check its logs, permissions, and deletion-blocking references before taking further action."}, parts...)
	}
	var cautions []string
	if len(unidentified) > 0 {
		subject := "finalizer " + unidentified[0]
		if len(unidentified) > 1 {
			subject = "finalizers " + strings.Join(unidentified, ", ")
		}
		cautions = append(cautions, fmt.Sprintf("Radar couldn't identify which controller owns %s; the finalizer's domain usually names it. Find that controller and check it first.", subject))
	}
	cautions = append(cautions, candidates...)
	cautions = append(cautions, unconfirmed...)
	if len(cautions) > 0 {
		parts = append(parts, cautions...)
		parts = append(parts, "Removing a finalizer skips its cleanup and can orphan external resources such as cloud infrastructure, volumes or nodes.")
	}
	parts = append(parts, guards...)
	if len(parts) > 0 {
		pr.Action = strings.Join(parts, " ")
	}
	if !running {
		pr.ConditionalCleanup = conditionalFinalizerCleanup(cleanups)
	}
	return pr
}

type finalizerCleanup struct {
	condition string
	removal   issuesapi.FinalizerRemoval
}

// conditionalFinalizerCleanup offers finalizer removal for controllers Radar
// found no running copy of, conditioned on the reader knowing they were
// removed on purpose: Radar can't tell an intentional uninstall from a
// temporary stop or an install it doesn't recognize.
func conditionalFinalizerCleanup(cleanups []finalizerCleanup) *issuesapi.ConditionalCleanup {
	if len(cleanups) == 0 {
		return nil
	}
	var conditions []string
	removals := make([]issuesapi.FinalizerRemoval, 0, len(cleanups))
	for _, cleanup := range cleanups {
		if !slices.Contains(conditions, cleanup.condition) {
			conditions = append(conditions, cleanup.condition)
		}
		removals = append(removals, cleanup.removal)
	}
	object := "the finalizer"
	if len(removals) > 1 {
		object = "the finalizers, one at a time"
	}
	return &issuesapi.ConditionalCleanup{
		Condition:     fmt.Sprintf("If %s (and nothing else should finalize this object), remove %s", strings.Join(conditions, " and "), object),
		AgentGuidance: "Not the next step: restore the controller first (see action). Propose this only after the user confirms the condition, as a step whose precondition is that condition, never as the recommended fix. Preview with dry_run=true or --dry-run=server, and apply one removal at a time only after the user reviews the preview.",
		Caution:       "Removing a finalizer skips its controller's cleanup. Kubernetes and external resources can remain; dependents with their own finalizers can stay stuck deleting. Both commands are dry runs: after reviewing, apply with dry_run=false or without --dry-run=server. Re-read the object after each removal before preparing the next patch: finalizer indices change.",
		Removals:      removals,
	}
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
