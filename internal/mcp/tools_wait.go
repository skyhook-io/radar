package mcp

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	appsv1 "k8s.io/api/apps/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/pkg/health"
)

const (
	waitDefaultTimeout = 120 * time.Second
	waitMaxTimeout     = 600 * time.Second
)

// waitPollInterval is a var so tests don't wait real seconds.
var waitPollInterval = 2 * time.Second

type waitForInput struct {
	Kind           string `json:"kind" jsonschema:"resource kind, e.g. deployment, job, pod"`
	Group          string `json:"group,omitempty" jsonschema:"API group when the kind is ambiguous"`
	Namespace      string `json:"namespace,omitempty" jsonschema:"namespace for namespaced kinds"`
	Name           string `json:"name,omitempty" jsonschema:"object name; or set label_selector instead"`
	LabelSelector  string `json:"label_selector,omitempty" jsonschema:"every matching object must meet the condition; with deleted, none may remain"`
	All            bool   `json:"all,omitempty" jsonschema:"every object of the kind in the namespace"`
	Condition      string `json:"condition,omitempty" jsonschema:"rollout, complete, ready, deleted, or a status condition type; defaults by kind"`
	TimeoutSeconds int    `json:"timeout_seconds,omitempty" jsonschema:"default 120, max 600"`
}

type waitObjectState struct {
	Name    string `json:"name"`
	Met     bool   `json:"met"`
	Message string `json:"message,omitempty"`
}

// waitCheck reports whether one object meets the condition. A non-empty
// failure means it never will without another change, so waiting stops.
type waitCheck func(obj *unstructured.Unstructured) (met bool, message, failure string)

func handleWaitFor(ctx context.Context, req *mcp.CallToolRequest, input waitForInput) (*mcp.CallToolResult, any, error) {
	if err := requireConnected(); err != nil {
		return nil, nil, err
	}
	dyn := k8s.DynamicClientFromContext(ctx)
	if dyn == nil {
		return nil, nil, errNotConnected()
	}
	return waitFor(ctx, dyn, input)
}

func waitFor(ctx context.Context, dyn dynamic.Interface, input waitForInput) (*mcp.CallToolResult, any, error) {
	kind, name, selector := strings.TrimSpace(input.Kind), strings.TrimSpace(input.Name), strings.TrimSpace(input.LabelSelector)
	if kind == "" {
		return nil, nil, fmt.Errorf("kind is required")
	}
	if targets := btoi(name != "") + btoi(selector != "") + btoi(input.All); targets != 1 {
		return nil, nil, fmt.Errorf("set exactly one of name, label_selector or all")
	}
	timeout, err := boundedTimeout(input.TimeoutSeconds, waitDefaultTimeout, waitMaxTimeout)
	if err != nil {
		return nil, nil, err
	}
	gvr, namespaced, err := resolveMutationGVR(kind, strings.TrimSpace(input.Group))
	if err != nil {
		return nil, nil, err
	}
	namespace := strings.TrimSpace(input.Namespace)
	if namespaced && namespace == "" {
		return nil, nil, fmt.Errorf("namespace is required for namespaced kind %q", kind)
	}
	var client dynamic.ResourceInterface = dyn.Resource(gvr)
	if namespaced {
		client = dyn.Resource(gvr).Namespace(namespace)
	}
	condition := strings.TrimSpace(input.Condition)
	if condition == "" {
		condition = defaultWaitCondition(gvr.Resource)
		if condition == "" {
			return nil, nil, fmt.Errorf("condition is required for kind %q: deleted, or a status condition type such as Ready", kind)
		}
	}
	deleted := strings.EqualFold(condition, "deleted")
	var check waitCheck
	if !deleted {
		if check, err = waitCheckFor(gvr.Resource, condition); err != nil {
			return nil, nil, err
		}
	}

	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	start := time.Now()
	result := map[string]any{"kind": kind, "namespace": namespace, "condition": condition}
	if name != "" {
		result["name"] = name
	} else {
		result["label_selector"] = selector
	}
	if input.All {
		result["all"] = true
	}

	// deleted by name tracks the first UID seen: a replacement with the same
	// name is a new object, and the one being waited on is gone.
	var firstUID types.UID
	ticker := time.NewTicker(waitPollInterval)
	defer ticker.Stop()
	for poll := 0; ; poll++ {
		objs, readErr := readWaitTargets(waitCtx, client, name, selector)
		var states []waitObjectState
		met, failure := false, ""
		switch {
		case readErr != nil && waitCtx.Err() != nil:
			// The deadline cut a read short; report the last full poll below.
		case readErr != nil:
			return nil, nil, fmt.Errorf("failed to read %s: %w", kind, readErr)
		case deleted:
			if name != "" && len(objs) == 1 && firstUID == "" {
				firstUID = objs[0].GetUID()
			}
			remaining := 0
			for _, obj := range objs {
				if name == "" || obj.GetUID() == firstUID {
					remaining++
					states = append(states, waitObjectState{Name: obj.GetName(), Message: deletionState(obj)})
				}
			}
			met = remaining == 0
		default:
			if len(objs) == 0 {
				if poll == 0 {
					return nil, nil, fmt.Errorf("no %s matches %s in %q", kind, waitTargetLabel(name, selector), namespace)
				}
				failure = "the objects were deleted while waiting"
				break
			}
			met = true
			for _, obj := range objs {
				ok, message, fail := check(obj)
				states = append(states, waitObjectState{Name: obj.GetName(), Met: ok, Message: message})
				met = met && ok
				if fail != "" && failure == "" {
					failure = obj.GetName() + ": " + fail
				}
			}
		}
		if states != nil {
			result["objects"] = states
		}
		result["elapsedMs"] = time.Since(start).Milliseconds()
		switch {
		case met:
			result["met"] = true
			return toJSONResult(result)
		case failure != "":
			result["met"] = false
			result["failed"] = failure
			return toJSONResult(result)
		}
		select {
		case <-waitCtx.Done():
			result["met"] = false
			if ctx.Err() == nil {
				result["timedOut"] = true
				result["message"] = fmt.Sprintf("condition %s not met within %ds; objects show the last state seen", condition, int(timeout.Seconds()))
			}
			return toJSONResult(result)
		case <-ticker.C:
		}
	}
}

func readWaitTargets(ctx context.Context, client dynamic.ResourceInterface, name, selector string) ([]*unstructured.Unstructured, error) {
	if name != "" {
		obj, err := client.Get(ctx, name, metav1.GetOptions{})
		if apierrors.IsNotFound(err) {
			return nil, nil
		}
		if err != nil {
			return nil, err
		}
		return []*unstructured.Unstructured{obj}, nil
	}
	list, err := client.List(ctx, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return nil, err
	}
	out := make([]*unstructured.Unstructured, 0, len(list.Items))
	for i := range list.Items {
		out = append(out, &list.Items[i])
	}
	return out, nil
}

func btoi(b bool) int {
	if b {
		return 1
	}
	return 0
}

func waitTargetLabel(name, selector string) string {
	if name != "" {
		return "name " + name
	}
	if selector == "" {
		return "all"
	}
	return "selector " + selector
}

func deletionState(obj *unstructured.Unstructured) string {
	if obj.GetDeletionTimestamp() == nil {
		return "not being deleted"
	}
	if finalizers := obj.GetFinalizers(); len(finalizers) > 0 {
		return "terminating; waiting on finalizers " + strings.Join(finalizers, ", ")
	}
	return "terminating"
}

func defaultWaitCondition(resource string) string {
	switch resource {
	case "deployments", "statefulsets", "daemonsets", "rollouts":
		return "rollout"
	case "jobs":
		return "complete"
	case "pods":
		return "ready"
	}
	return ""
}

func waitCheckFor(resource, condition string) (waitCheck, error) {
	switch strings.ToLower(condition) {
	case "rollout":
		switch resource {
		case "deployments", "statefulsets", "daemonsets", "rollouts":
			return rolloutStatus, nil
		}
		return nil, fmt.Errorf("condition rollout applies to Deployment, StatefulSet, DaemonSet and Rollout, not %s", resource)
	case "complete":
		if resource != "jobs" {
			return nil, fmt.Errorf("condition complete applies to Job, not %s; name a status condition type instead", resource)
		}
		return jobComplete, nil
	case "ready":
		if resource == "pods" {
			return podReadyCheck, nil
		}
		return statusConditionCheck("Ready"), nil
	}
	return statusConditionCheck(condition), nil
}

// rolloutStatus mirrors kubectl rollout status for the built-in workloads
// (k8s.io/kubectl/pkg/polymorphichelpers/rollout_status.go): the controller
// has observed the spec, every replica is updated, old replicas are gone and
// the updated ones are available. An exceeded progress deadline fails. Argo
// Rollouts use Radar's rollout health.
func rolloutStatus(obj *unstructured.Unstructured) (bool, string, string) {
	switch obj.GetKind() {
	case "Deployment":
		d := &appsv1.Deployment{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, d); err != nil {
			return false, "", err.Error()
		}
		if d.Generation > d.Status.ObservedGeneration {
			return false, "waiting for the deployment controller to observe the new spec", ""
		}
		for _, c := range d.Status.Conditions {
			if c.Type == appsv1.DeploymentProgressing && c.Reason == "ProgressDeadlineExceeded" {
				return false, "", "progress deadline exceeded: " + c.Message
			}
		}
		desired := int32(1)
		if d.Spec.Replicas != nil {
			desired = *d.Spec.Replicas
		}
		s := d.Status
		switch {
		case s.UpdatedReplicas < desired:
			return false, fmt.Sprintf("%d of %d new replicas updated", s.UpdatedReplicas, desired), ""
		case s.Replicas > s.UpdatedReplicas:
			return false, fmt.Sprintf("%d old replicas pending termination", s.Replicas-s.UpdatedReplicas), ""
		case s.AvailableReplicas < s.UpdatedReplicas:
			return false, fmt.Sprintf("%d of %d updated replicas available", s.AvailableReplicas, s.UpdatedReplicas), ""
		}
		return true, fmt.Sprintf("rolled out: %d/%d available", s.AvailableReplicas, desired), ""
	case "StatefulSet":
		sts := &appsv1.StatefulSet{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, sts); err != nil {
			return false, "", err.Error()
		}
		if sts.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
			return false, "", "rollout status needs the RollingUpdate strategy; this StatefulSet uses OnDelete, so wait on its pods instead"
		}
		if sts.Status.ObservedGeneration == 0 || sts.Generation > sts.Status.ObservedGeneration {
			return false, "waiting for the statefulset controller to observe the new spec", ""
		}
		desired := int32(1)
		if sts.Spec.Replicas != nil {
			desired = *sts.Spec.Replicas
		}
		if sts.Status.ReadyReplicas < desired {
			return false, fmt.Sprintf("%d of %d pods ready", sts.Status.ReadyReplicas, desired), ""
		}
		if ru := sts.Spec.UpdateStrategy.RollingUpdate; ru != nil && ru.Partition != nil {
			if target := desired - *ru.Partition; sts.Status.UpdatedReplicas < target {
				return false, fmt.Sprintf("%d of %d partitioned pods updated", sts.Status.UpdatedReplicas, target), ""
			}
			return true, fmt.Sprintf("partitioned rollout done: %d pods updated", sts.Status.UpdatedReplicas), ""
		}
		if sts.Status.UpdateRevision != sts.Status.CurrentRevision {
			return false, fmt.Sprintf("%d pods at revision %s", sts.Status.UpdatedReplicas, sts.Status.UpdateRevision), ""
		}
		return true, fmt.Sprintf("rolled out: %d/%d ready", sts.Status.ReadyReplicas, desired), ""
	case "DaemonSet":
		ds := &appsv1.DaemonSet{}
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, ds); err != nil {
			return false, "", err.Error()
		}
		if ds.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
			return false, "", "rollout status needs the RollingUpdate strategy; this DaemonSet uses OnDelete, so wait on its pods instead"
		}
		if ds.Generation > ds.Status.ObservedGeneration {
			return false, "waiting for the daemonset controller to observe the new spec", ""
		}
		s := ds.Status
		if s.UpdatedNumberScheduled < s.DesiredNumberScheduled {
			return false, fmt.Sprintf("%d of %d new pods updated", s.UpdatedNumberScheduled, s.DesiredNumberScheduled), ""
		}
		if s.NumberAvailable < s.DesiredNumberScheduled {
			return false, fmt.Sprintf("%d of %d updated pods available", s.NumberAvailable, s.DesiredNumberScheduled), ""
		}
		return true, fmt.Sprintf("rolled out: %d/%d available", s.NumberAvailable, s.DesiredNumberScheduled), ""
	}
	activity := health.WorkloadRollout(obj)
	switch {
	case activity.Phase == health.RolloutStalled:
		return false, "", activity.Detail
	case activity.Phase == health.RolloutPaused && activity.Active:
		return false, "", "paused: " + activity.Detail + "; promote or abort it with manage_rollout"
	case activity.Active:
		return false, activity.Label + ": " + activity.Detail, ""
	}
	return true, activity.Label + ": " + activity.Detail, ""
}

func jobComplete(obj *unstructured.Unstructured) (bool, string, string) {
	if status, message := conditionStatus(obj, "Failed"); status == "True" {
		return false, "", "job failed: " + message
	}
	if status, _ := conditionStatus(obj, "Complete"); status == "True" {
		return true, "complete", ""
	}
	active, _, _ := unstructured.NestedInt64(obj.Object, "status", "active")
	succeeded, _, _ := unstructured.NestedInt64(obj.Object, "status", "succeeded")
	return false, fmt.Sprintf("%d active, %d succeeded", active, succeeded), ""
}

func podReadyCheck(obj *unstructured.Unstructured) (bool, string, string) {
	phase, _, _ := unstructured.NestedString(obj.Object, "status", "phase")
	if phase == "Succeeded" || phase == "Failed" {
		return false, "", "pod is " + phase + " and will not become Ready"
	}
	status, message := conditionStatus(obj, "Ready")
	if status == "True" {
		return true, "Ready", ""
	}
	if message == "" {
		message = "phase " + phase
	}
	return false, "not Ready: " + message, ""
}

func statusConditionCheck(condType string) waitCheck {
	return func(obj *unstructured.Unstructured) (bool, string, string) {
		status, message := conditionStatus(obj, condType)
		// Like kubectl wait: a condition the controller reported for an older
		// generation says nothing about the current spec.
		if gen, observed := obj.GetGeneration(), conditionObservedGeneration(obj, condType); gen > 0 && observed > 0 && observed < gen {
			return false, fmt.Sprintf("%s=%s was reported for generation %d; waiting for the controller to observe generation %d", condType, status, observed, gen), ""
		}
		switch status {
		case "True":
			return true, condType + "=True", ""
		case "":
			return false, "no " + condType + " condition yet", ""
		}
		if message != "" {
			return false, condType + "=" + status + ": " + message, ""
		}
		return false, condType + "=" + status, ""
	}
}

// conditionObservedGeneration is the generation the named condition was
// computed for: its own observedGeneration, else status.observedGeneration,
// else 0 when the object reports neither.
func conditionObservedGeneration(obj *unstructured.Unstructured, condType string) int64 {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conditions {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := c["type"].(string); strings.EqualFold(t, condType) {
			if g, ok := c["observedGeneration"].(int64); ok && g > 0 {
				return g
			}
			break
		}
	}
	g, _, _ := unstructured.NestedInt64(obj.Object, "status", "observedGeneration")
	return g
}

// conditionStatus returns the status and message of the named condition,
// matched case-insensitively, or "" when absent.
func conditionStatus(obj *unstructured.Unstructured, condType string) (string, string) {
	conditions, _, _ := unstructured.NestedSlice(obj.Object, "status", "conditions")
	for _, raw := range conditions {
		c, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if t, _ := c["type"].(string); strings.EqualFold(t, condType) {
			status, _ := c["status"].(string)
			message, _ := c["message"].(string)
			return status, message
		}
	}
	return "", ""
}
