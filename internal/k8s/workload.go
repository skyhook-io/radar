package k8s

import (
	"context"
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/skyhook-io/radar/pkg/rollouts"
)

var ErrWorkloadAccessDenied = errors.New("workload access denied")

// ErrWorkloadCacheWarming separates "not loaded yet" from "not permitted".
// Both leave the pod set unknown, but only one of them is the caller's
// permissions, and reporting a warming cache as a denial sends an operator
// (or the agent) after an RBAC problem that does not exist.
var ErrWorkloadCacheWarming = errors.New("workload cache is still loading")

// ErrWorkloadNotFound is a confirmed absence: the covering, synced cache has
// no such workload. Charts read it as "no current Pods"; a request naming one
// workload reports it as not found.
var ErrWorkloadNotFound = errors.New("workload not found")
var ErrWorkloadSelectorUnavailable = errors.New("workload selector unavailable")

// GetWorkloadSelector returns the label selector for a workload from cache.
// kind is case-insensitive and accepts either singular ("deployment") or plural
// ("deployments") — matches K8s canonical Kind or REST-style plural.
func GetWorkloadSelector(cache *ResourceCache, kind, namespace, name string) (*metav1.LabelSelector, error) {
	switch kind {
	case "deployment", "deployments":
		lister := cache.Deployments()
		if lister == nil {
			return nil, fmt.Errorf("%w: list deployments", ErrWorkloadAccessDenied)
		}
		dep, err := lister.Deployments(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("deployment %s/%s: %w", namespace, name, err)
		}
		return dep.Spec.Selector, nil

	case "statefulset", "statefulsets":
		lister := cache.StatefulSets()
		if lister == nil {
			return nil, fmt.Errorf("%w: list statefulsets", ErrWorkloadAccessDenied)
		}
		sts, err := lister.StatefulSets(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("statefulset %s/%s: %w", namespace, name, err)
		}
		return sts.Spec.Selector, nil

	case "daemonset", "daemonsets":
		lister := cache.DaemonSets()
		if lister == nil {
			return nil, fmt.Errorf("%w: list daemonsets", ErrWorkloadAccessDenied)
		}
		ds, err := lister.DaemonSets(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("daemonset %s/%s: %w", namespace, name, err)
		}
		return ds.Spec.Selector, nil

	case "job", "jobs":
		lister := cache.Jobs()
		if lister == nil {
			return nil, fmt.Errorf("%w: list jobs", ErrWorkloadAccessDenied)
		}
		job, err := lister.Jobs(namespace).Get(name)
		if err != nil {
			return nil, fmt.Errorf("job %s/%s: %w", namespace, name, err)
		}
		if job.Spec.Selector == nil {
			return &metav1.LabelSelector{
				MatchLabels: map[string]string{"batch.kubernetes.io/job-name": name},
			}, nil
		}
		return job.Spec.Selector, nil

	case "workflow", "workflows":
		if _, err := cache.GetDynamicWithGroup(context.Background(), "Workflow", namespace, name, "argoproj.io"); err != nil {
			return nil, fmt.Errorf("workflow %s/%s: %w", namespace, name, err)
		}
		return &metav1.LabelSelector{
			MatchLabels: map[string]string{"workflows.argoproj.io/workflow": name},
		}, nil

	case "rollout", "rollouts":
		rollout, err := cache.GetDynamicWithGroup(context.Background(), "Rollout", namespace, name, "argoproj.io")
		if err != nil {
			return nil, fmt.Errorf("rollout %s/%s: %w", namespace, name, err)
		}
		return ResolveRolloutSelector(cache, rollout)

	default:
		return nil, fmt.Errorf("unsupported workload kind: %s", kind)
	}
}

// ResolveRolloutSelector returns only pods created by the Rollout controller,
// even while a workloadRef Deployment is still running with the same labels.
func ResolveRolloutSelector(cache *ResourceCache, rollout *unstructured.Unstructured) (*metav1.LabelSelector, error) {
	namespace, name := rollout.GetNamespace(), rollout.GetName()
	raw, found, err := unstructured.NestedFieldNoCopy(rollout.Object, "spec", "selector")
	if err != nil {
		return nil, fmt.Errorf("rollout %s/%s selector: %w", namespace, name, err)
	}
	if found && raw != nil {
		selectorMap, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("rollout %s/%s selector has type %T, want object", namespace, name, raw)
		}
		var selector metav1.LabelSelector
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(selectorMap, &selector); err != nil {
			return nil, fmt.Errorf("rollout %s/%s selector: %w", namespace, name, err)
		}
		return rolloutPodSelector(&selector), nil
	}

	target, err := rollouts.ResolveTemplateTarget(rollout)
	if err != nil {
		return nil, err
	}
	if target.GVR == rollouts.GVR {
		return nil, fmt.Errorf("rollout %s/%s has no selector: %w", namespace, name, ErrWorkloadSelectorUnavailable)
	}

	var selector *metav1.LabelSelector
	switch target.GVR.Resource {
	case "deployments":
		lister := cache.Deployments()
		if lister == nil {
			return nil, fmt.Errorf("%w: list deployments", ErrWorkloadAccessDenied)
		}
		deployment, err := lister.Deployments(namespace).Get(target.Name)
		if err != nil {
			return nil, fmt.Errorf("deployment %s/%s referenced by rollout %s: %w", namespace, target.Name, name, err)
		}
		selector = deployment.Spec.Selector
	case "replicasets":
		lister := cache.ReplicaSets()
		if lister == nil {
			return nil, fmt.Errorf("%w: list replicasets", ErrWorkloadAccessDenied)
		}
		replicaSet, err := lister.ReplicaSets(namespace).Get(target.Name)
		if err != nil {
			return nil, fmt.Errorf("replicaset %s/%s referenced by rollout %s: %w", namespace, target.Name, name, err)
		}
		selector = replicaSet.Spec.Selector
	default:
		return nil, fmt.Errorf("rollout %s/%s cannot derive a selector from %s %s: %w", namespace, name, target.GVR.Resource, target.Name, ErrWorkloadSelectorUnavailable)
	}
	if selector == nil {
		return nil, fmt.Errorf("rollout %s/%s referenced workload has no selector: %w", namespace, name, ErrWorkloadSelectorUnavailable)
	}
	return rolloutPodSelector(selector), nil
}

func rolloutPodSelector(selector *metav1.LabelSelector) *metav1.LabelSelector {
	selector = selector.DeepCopy()
	selector.MatchExpressions = append(selector.MatchExpressions, metav1.LabelSelectorRequirement{
		Key:      rollouts.PodTemplateHashLabel,
		Operator: metav1.LabelSelectorOpExists,
	})
	return selector
}

// GetContainersForPod returns container names to target for log collection.
// A non-empty selectedContainer is validated against the Pod's containers, and
// against its init containers when includeInit is set. An empty one means every
// main container and, with includeInit, each init container that is failing
// while the Pod is still initializing: the main containers have no output then,
// and the failure is why the Pod is stuck. Init steps that succeeded or are
// still making progress are left out, so routine startup during a rollout
// doesn't flood the merged view; they stay selectable by name. A native sidecar
// that has started isn't blocking initialization and is left out too.
func GetContainersForPod(pod *corev1.Pod, selectedContainer string, includeInit bool) []string {
	if selectedContainer != "" {
		for _, c := range pod.Spec.Containers {
			if c.Name == selectedContainer {
				return []string{selectedContainer}
			}
		}
		if includeInit {
			for _, c := range pod.Spec.InitContainers {
				if c.Name == selectedContainer {
					return []string{selectedContainer}
				}
			}
		}
		return nil
	}
	containers := make([]string, 0, len(pod.Spec.Containers))
	if includeInit && !podInitialized(pod) {
		sidecars := map[string]bool{}
		for _, c := range pod.Spec.InitContainers {
			if c.RestartPolicy != nil && *c.RestartPolicy == corev1.ContainerRestartPolicyAlways {
				sidecars[c.Name] = true
			}
		}
		for _, status := range pod.Status.InitContainerStatuses {
			if sidecars[status.Name] && status.Started != nil && *status.Started {
				continue
			}
			if initContainerFailing(status, sidecars[status.Name]) {
				containers = append(containers, status.Name)
			}
		}
	}
	for _, c := range pod.Spec.Containers {
		containers = append(containers, c.Name)
	}
	return containers
}

// initContainerFailing reports an init container whose last run failed, or
// that is backing off or retrying after one. A run fails when it exits non-zero
// or is OOM-killed, as the kubelet judges it; a native sidecar is restarted
// whatever its exit code, so any exit before it has started counts.
func initContainerFailing(status corev1.ContainerStatus, sidecar bool) bool {
	failed := func(run *corev1.ContainerStateTerminated) bool {
		return run != nil && (sidecar || run.ExitCode != 0 || run.Reason == "OOMKilled")
	}
	if terminated := status.State.Terminated; terminated != nil {
		return failed(terminated)
	}
	if waiting := status.State.Waiting; waiting != nil && waiting.Reason == "CrashLoopBackOff" {
		return true
	}
	return failed(status.LastTerminationState.Terminated)
}

func podInitialized(pod *corev1.Pod) bool {
	for _, condition := range pod.Status.Conditions {
		if condition.Type == corev1.PodInitialized {
			return condition.Status == corev1.ConditionTrue
		}
	}
	return len(pod.Spec.InitContainers) == 0
}
