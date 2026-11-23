package prometheus

import (
	"context"
	"github.com/skyhook-io/radar/internal/k8s"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"slices"

	"github.com/skyhook-io/radar/pkg/prom"
	"github.com/skyhook-io/radar/pkg/rightsizing"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	resourcehelper "k8s.io/component-helpers/resource"
	"k8s.io/utils/ptr"
)

// A status from an older controller generation cannot vouch for the current replicas.
func sizingControllerCohortReady(owner metav1.Object, observed int64, desired, total, updated, ready int32) bool {
	return owner.GetDeletionTimestamp() == nil && owner.GetGeneration() > 0 && observed == owner.GetGeneration() &&
		desired > 0 && total == desired && updated == desired && ready == desired
}

type sizingPodRestarts struct {
	knownPods int
	restarts  int
}

// Only Pods already resolved through this workload's controller UID reach this method.
func (w *rightsizingWorkload) observeSizingPodIdentity(ctx context.Context, cache *k8s.ResourceCache, p *corev1.Pod) {
	if p.Status.Phase == corev1.PodSucceeded || p.Status.Phase == corev1.PodFailed {
		return
	}
	if w.podIdentityIncomplete {
		return
	}
	ready := slices.ContainsFunc(p.Status.Conditions, func(c corev1.PodCondition) bool { return c.Type == corev1.PodReady && c.Status == corev1.ConditionTrue })
	if p.UID == "" || p.DeletionTimestamp != nil || p.Status.Phase != corev1.PodRunning || !ready || w.podSpec == nil || !SizingPodMatches(p, *w.podSpec) {
		w.podIdentityIncomplete = true
		return
	}
	var images []string
	rewritten := false
	for i, expected := range [][]corev1.Container{w.podSpec.Containers, w.podSpec.InitContainers} {
		actual := p.Spec.Containers
		if i == 1 {
			actual = p.Spec.InitContainers
		}
		for _, container := range expected {
			j := slices.IndexFunc(actual, func(c corev1.Container) bool { return c.Name == container.Name })
			if j < 0 {
				w.podIdentityIncomplete = true
				return
			}
			images = append(images, actual[j].Image)
			rewritten = rewritten || actual[j].Image != container.Image
		}
	}
	if (len(w.podIdentities) > 0 && !slices.Equal(w.admittedImages, images)) || (rewritten && !w.podRevisionMatches(ctx, cache, p)) {
		w.podIdentityIncomplete = true
		return
	}
	if len(w.podIdentities) == 0 {
		w.admittedImages = images
	}
	w.podIdentities = append(w.podIdentities, prom.WorkloadPodIdentity{Name: p.Name, UID: string(p.UID)})
	if w.podRestartEvidence == nil {
		w.podRestartEvidence = map[string]sizingPodRestarts{}
	}
	for _, statuses := range [][]corev1.ContainerStatus{p.Status.ContainerStatuses, p.Status.InitContainerStatuses} {
		for _, c := range statuses {
			if !c.Ready || c.State.Running == nil {
				continue
			}
			// Kubernetes retains only the last termination. A single successful
			// completion accounts for the whole restart history of this Pod.
			// For other terminations keep the existing positive OOM signal and
			// avoid ruling out OOM when an earlier or ambiguous exit is unknown.
			last := c.LastTerminationState.Terminated
			noRestarts := c.RestartCount == 0 && last == nil
			restartedCleanly := c.RestartCount == 1 && last != nil && last.Reason == "Completed" && last.ExitCode == 0 && last.Signal == 0
			if noRestarts || restartedCleanly {
				r := w.podRestartEvidence[c.Name]
				r.knownPods++
				r.restarts += int(c.RestartCount)
				w.podRestartEvidence[c.Name] = r
			}
		}
	}
}

// Admission may pin or mirror the Pod's images. Only its controller's
// revision can then establish which workload template produced it.
func (w *rightsizingWorkload) podRevisionMatches(ctx context.Context, cache *k8s.ResourceCache, p *corev1.Pod) bool {
	owner := metav1.GetControllerOf(p)
	if owner == nil || owner.UID == "" || cache == nil {
		return false
	}
	switch owner.Kind {
	case "ReplicaSet":
		if cache.ReplicaSets() == nil {
			return false
		}
		rs, err := cache.ReplicaSets().ReplicaSets(p.Namespace).Get(owner.Name)
		if err != nil || rs.UID != owner.UID {
			return false
		}
		hash := rs.Labels[appsv1.DefaultDeploymentUniqueLabelKey]
		return hash != "" && p.Labels[appsv1.DefaultDeploymentUniqueLabelKey] == hash && equality.Semantic.DeepEqual(rs.Spec.Template.Spec, *w.podSpec)
	case "StatefulSet":
		if cache.StatefulSets() == nil {
			return false
		}
		ss, err := cache.StatefulSets().StatefulSets(p.Namespace).Get(owner.Name)
		return err == nil && ss.UID == owner.UID && ss.Status.UpdateRevision != "" && p.Labels[appsv1.ControllerRevisionHashLabelKey] == ss.Status.UpdateRevision && equality.Semantic.DeepEqual(ss.Spec.Template.Spec, *w.podSpec)
	case "DaemonSet":
		hash := p.Labels[appsv1.ControllerRevisionHashLabelKey]
		if hash == "" {
			return false
		}
		if matched, ok := w.daemonRevisionMatches[hash]; ok {
			return matched
		}
		selector := labels.Set{appsv1.ControllerRevisionHashLabelKey: hash}.AsSelector().String()
		revisions, truncated, err := cache.ListDirectSelectedWithGroup(ctx, "ControllerRevision", p.Namespace, "apps", selector, 64)
		matched := err == nil && !truncated && slices.ContainsFunc(revisions, func(r *unstructured.Unstructured) bool {
			if !k8s.ControllerMatchesIdentity(metav1.GetControllerOf(r), "DaemonSet", owner.Name, "apps", owner.UID) || r.GetLabels()[appsv1.ControllerRevisionHashLabelKey] != hash {
				return false
			}
			spec, found, err := unstructured.NestedMap(r.Object, "data", "spec", "template", "spec")
			var recorded corev1.PodSpec
			return err == nil && found && runtime.DefaultUnstructuredConverter.FromUnstructured(spec, &recorded) == nil && equality.Semantic.DeepEqual(recorded, *w.podSpec)
		})
		if w.daemonRevisionMatches == nil {
			w.daemonRevisionMatches = map[string]bool{}
		}
		w.daemonRevisionMatches[hash] = matched
		return matched
	}
	return false
}

// SizingResources projects the requests and limits used by the sizing policy.
func SizingResources(r *corev1.ResourceRequirements) *corev1.ResourceRequirements {
	if r == nil {
		return nil
	}
	project := func(in corev1.ResourceList) corev1.ResourceList {
		var out corev1.ResourceList
		for _, key := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
			if q, ok := in[key]; ok {
				if out == nil {
					out = corev1.ResourceList{}
				}
				out[key] = q
			}
		}
		return out
	}
	return &corev1.ResourceRequirements{Requests: project(r.Requests), Limits: project(r.Limits)}
}

// SizingPodMatches checks that admitted resources still match the workload template.
func SizingPodMatches(p *corev1.Pod, spec corev1.PodSpec) bool {
	match := func(desired, actual []corev1.Container) bool {
		// With a shared Pod limit, admission-injected containers can use the
		// budget the sizing policy would otherwise assign to a template container.
		if spec.Resources != nil && len(spec.Resources.Limits) > 0 && len(actual) != len(desired) {
			return false
		}
		for _, c := range desired {
			if !slices.ContainsFunc(actual, func(a corev1.Container) bool {
				if c.Name != a.Name || !ptr.Equal(c.RestartPolicy, a.RestartPolicy) {
					return false
				}
				expected := SizingResources(&c.Resources)
				expected.Requests = rightsizing.EffectiveContainerRequests(*expected)
				return equality.Semantic.DeepEqual(SizingResources(&a.Resources), expected)
			}) {
				return false
			}
		}
		return true
	}
	return match(spec.Containers, p.Spec.Containers) && match(spec.InitContainers, p.Spec.InitContainers) && sizingPodResourcesMatch(p, spec.Resources)
}

func sizingPodResourcesMatch(p *corev1.Pod, template *corev1.ResourceRequirements) bool {
	actual, expected := SizingResources(p.Spec.Resources), SizingResources(template)
	if equality.Semantic.DeepEqual(actual, expected) {
		return true
	}
	if expected == nil || len(expected.Limits) == 0 {
		return false
	}
	// With a Pod limit, Kubernetes defaults missing Pod requests from admitted
	// container requests, then from the Pod limit. Other Pod defaults vary by
	// API server feature gate, so leave those differences unverified.
	containerRequests := resourcehelper.AggregateContainerRequests(p, resourcehelper.PodResourcesOptions{})
	if expected.Requests == nil {
		expected.Requests = corev1.ResourceList{}
	}
	for _, name := range []corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory} {
		if _, exists := expected.Requests[name]; exists {
			continue
		}
		if q, ok := containerRequests[name]; ok {
			expected.Requests[name] = q
		} else if q, ok := expected.Limits[name]; ok {
			expected.Requests[name] = q
		}
	}
	return equality.Semantic.DeepEqual(actual, expected)
}
