package health

import (
	"fmt"
	"strconv"
	"strings"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

type RolloutPhase string

const (
	RolloutIdle             RolloutPhase = "idle"
	RolloutApplying         RolloutPhase = "applying"
	RolloutProgressing      RolloutPhase = "progressing"
	RolloutWaiting          RolloutPhase = "waiting"
	RolloutPaused           RolloutPhase = "paused"
	RolloutPartitionReached RolloutPhase = "partition-reached"
	RolloutStalled          RolloutPhase = "stalled"
)

type WorkloadRolloutActivity struct {
	Phase     RolloutPhase `json:"phase"`
	Active    bool         `json:"active"`
	Manual    bool         `json:"manual"`
	Label     string       `json:"label"`
	Detail    string       `json:"detail,omitempty"`
	Desired   int32        `json:"desired"`
	Updated   int32        `json:"updated"`
	Ready     int32        `json:"ready"`
	Available int32        `json:"available"`
}

func WorkloadRollout(obj any) WorkloadRolloutActivity {
	switch o := obj.(type) {
	case *appsv1.Deployment:
		return deploymentRollout(o)
	case *appsv1.StatefulSet:
		return statefulSetRollout(o)
	case *appsv1.DaemonSet:
		return daemonSetRollout(o)
	case *unstructured.Unstructured:
		if strings.EqualFold(o.GetKind(), "Rollout") && strings.HasPrefix(o.GetAPIVersion(), "argoproj.io/") {
			return argoRollout(o)
		}
	}
	return activity(RolloutIdle, false, "Stable", "", 0, 0, 0, 0)
}

func deploymentRollout(d *appsv1.Deployment) WorkloadRolloutActivity {
	desired := specReplicas(d.Spec.Replicas)
	updated := d.Status.UpdatedReplicas
	ready := d.Status.ReadyReplicas
	available := d.Status.AvailableReplicas
	base := activityCounts(desired, updated, ready, available)

	if d.Status.ObservedGeneration < d.Generation {
		return base.with(RolloutApplying, true, "Applying change", "Waiting for the Deployment controller to observe generation")
	}
	if conditionFailed(d.Status.Conditions, "Progressing", "ProgressDeadlineExceeded") {
		return base.with(RolloutStalled, false, "Rollout stalled", conditionMessage(d.Status.Conditions, "Progressing", "Progress deadline exceeded"))
	}
	old := d.Status.Replicas - updated
	if old < 0 {
		old = 0
	}
	pending := updated < desired || old > 0
	if d.Spec.Paused {
		detail := "No rollout is pending"
		if pending {
			detail = fmt.Sprintf("Paused at %d/%d updated", updated, desired)
		}
		return base.with(RolloutPaused, pending, "Rollout paused", detail)
	}
	if desired == 0 {
		return base.with(RolloutIdle, false, "Scaled to zero", "No replicas requested")
	}
	if !pending {
		return base.with(RolloutIdle, false, "Stable", fmt.Sprintf("%d/%d available", available, desired))
	}
	if d.Spec.Strategy.Type == appsv1.RecreateDeploymentStrategyType {
		if updated == 0 && old > 0 {
			return base.with(RolloutWaiting, true, "Replacing replicas", fmt.Sprintf("Waiting for old replicas to stop · %d available", available))
		}
		if updated == 0 {
			return base.with(RolloutWaiting, true, "Waiting for new revision", replicaDetail(updated, desired, available))
		}
		return base.with(RolloutProgressing, true, "Replacing replicas", replicaDetail(updated, desired, available))
	}
	if updated == 0 {
		return base.with(RolloutWaiting, true, "Waiting for new revision", replicaDetail(updated, desired, available))
	}
	if old == 0 && updated == d.Status.Replicas {
		return base.with(RolloutProgressing, true, "Scaling", replicaDetail(updated, desired, available))
	}
	return base.with(RolloutProgressing, true, "Rolling out", replicaDetail(updated, desired, available))
}

func statefulSetRollout(s *appsv1.StatefulSet) WorkloadRolloutActivity {
	desired := specReplicas(s.Spec.Replicas)
	updated := s.Status.UpdatedReplicas
	ready := s.Status.ReadyReplicas
	base := activityCounts(desired, updated, ready, ready)

	if s.Status.ObservedGeneration < s.Generation {
		return base.with(RolloutApplying, true, "Applying change", "Waiting for the StatefulSet controller to observe generation")
	}
	if desired == 0 {
		return base.with(RolloutIdle, false, "Scaled to zero", "No replicas requested")
	}
	if s.Spec.UpdateStrategy.Type == appsv1.OnDeleteStatefulSetStrategyType {
		if updated == desired && ready == desired {
			return base.with(RolloutIdle, false, "Stable", fmt.Sprintf("%d/%d ready", ready, desired))
		}
		return base.withAction(RolloutWaiting, true, "Waiting for Pod restart", fmt.Sprintf("OnDelete strategy · %d/%d updated", updated, desired))
	}
	partition := int32(0)
	if s.Spec.UpdateStrategy.RollingUpdate != nil && s.Spec.UpdateStrategy.RollingUpdate.Partition != nil {
		partition = *s.Spec.UpdateStrategy.RollingUpdate.Partition
	}
	target := desired - partition
	if target < 0 {
		target = 0
	}
	if updated >= target {
		if partition > 0 && s.Status.CurrentRevision != "" && s.Status.UpdateRevision != "" && s.Status.CurrentRevision != s.Status.UpdateRevision {
			return base.with(RolloutPartitionReached, false, "Partition reached", fmt.Sprintf("%d/%d Pods intentionally retained", partition, desired))
		}
		return base.with(RolloutIdle, false, "Stable", fmt.Sprintf("%d/%d ready", ready, desired))
	}
	if updated == 0 {
		return base.with(RolloutWaiting, true, "Waiting for new revision", fmt.Sprintf("%d/%d target Pods updated · %d/%d ready", updated, target, ready, desired))
	}
	return base.with(RolloutProgressing, true, "Rolling out", fmt.Sprintf("%d/%d target Pods updated · %d/%d ready", updated, target, ready, desired))
}

func daemonSetRollout(d *appsv1.DaemonSet) WorkloadRolloutActivity {
	desired := d.Status.DesiredNumberScheduled
	updated := d.Status.UpdatedNumberScheduled
	ready := d.Status.NumberReady
	available := d.Status.NumberAvailable
	base := activityCounts(desired, updated, ready, available)

	if d.Status.ObservedGeneration < d.Generation {
		return base.with(RolloutApplying, true, "Applying change", "Waiting for the DaemonSet controller to observe generation")
	}
	if desired == 0 {
		return base.with(RolloutIdle, false, "No targets", "Selector matches no nodes")
	}
	if d.Spec.UpdateStrategy.Type == appsv1.OnDeleteDaemonSetStrategyType {
		if updated == desired && available == desired {
			return base.with(RolloutIdle, false, "Stable", fmt.Sprintf("%d/%d available", available, desired))
		}
		return base.withAction(RolloutWaiting, true, "Waiting for Pod restart", fmt.Sprintf("OnDelete strategy · %d/%d updated", updated, desired))
	}
	if updated == desired {
		return base.with(RolloutIdle, false, "Stable", fmt.Sprintf("%d/%d available", available, desired))
	}
	if updated == 0 {
		return base.with(RolloutWaiting, true, "Waiting for new revision", replicaDetail(updated, desired, available))
	}
	return base.with(RolloutProgressing, true, "Rolling out", replicaDetail(updated, desired, available))
}

func argoRollout(r *unstructured.Unstructured) WorkloadRolloutActivity {
	desired := nestedInt32(r.Object, "spec", "replicas")
	if _, found, _ := unstructured.NestedFieldNoCopy(r.Object, "spec", "replicas"); !found {
		desired = nestedInt32(r.Object, "status", "replicas")
		if desired == 0 {
			desired = 1
		}
	}
	updated := nestedInt32(r.Object, "status", "updatedReplicas")
	ready := nestedInt32(r.Object, "status", "readyReplicas")
	available := nestedInt32(r.Object, "status", "availableReplicas")
	base := activityCounts(desired, updated, ready, available)
	phase, _, _ := unstructured.NestedString(r.Object, "status", "phase")
	message, _, _ := unstructured.NestedString(r.Object, "status", "message")
	aborted, _, _ := unstructured.NestedBool(r.Object, "status", "abort")
	if observed, found, _ := unstructured.NestedString(r.Object, "status", "observedGeneration"); found {
		generation, err := strconv.ParseInt(observed, 10, 64)
		if err == nil && generation > 0 && generation < r.GetGeneration() {
			return base.with(RolloutApplying, true, "Applying change", "Waiting for the Rollout controller to observe generation")
		}
	}
	if failedMessage, failed := argoFailureMessage(r); failed {
		if failedMessage == "" {
			failedMessage = message
		}
		if failedMessage == "" {
			failedMessage = "The Rollout controller reported a failed revision"
		}
		return base.with(RolloutStalled, false, "Rollout stalled", failedMessage)
	}
	if aborted {
		if message == "" {
			message = "The Rollout was aborted"
		}
		return base.with(RolloutStalled, false, "Rollout stalled", message)
	}
	switch strings.ToLower(phase) {
	case "degraded", "error", "failed", "aborted":
		if message == "" {
			message = "The Rollout controller reported a failed revision"
		}
		return base.with(RolloutStalled, false, "Rollout stalled", message)
	case "paused":
		return base.with(RolloutPaused, true, "Rollout paused", argoStepDetail(r, updated, desired, available))
	case "progressing":
		if updated == 0 {
			return base.with(RolloutWaiting, true, "Waiting for new revision", argoStepDetail(r, updated, desired, available))
		}
		return base.with(RolloutProgressing, true, "Rolling out", argoStepDetail(r, updated, desired, available))
	case "healthy":
		return base.with(RolloutIdle, false, "Stable", fmt.Sprintf("%d/%d available", available, desired))
	default:
		if desired > 0 && (updated < desired || available < desired) {
			return base.with(RolloutApplying, true, "Applying change", argoStepDetail(r, updated, desired, available))
		}
		return base.with(RolloutIdle, false, "Stable", message)
	}
}

func argoFailureMessage(r *unstructured.Unstructured) (string, bool) {
	conditions, _, _ := unstructured.NestedSlice(r.Object, "status", "conditions")
	for _, raw := range conditions {
		condition, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		conditionType, _ := condition["type"].(string)
		conditionStatus, _ := condition["status"].(string)
		reason, _ := condition["reason"].(string)
		failed := (conditionType == "InvalidSpec" && conditionStatus == "True") ||
			(conditionType == "Progressing" && (conditionStatus == "False" || reason == "ProgressDeadlineExceeded"))
		if !failed {
			continue
		}
		if message, _ := condition["message"].(string); message != "" {
			return message, true
		}
		return reason, true
	}
	return "", false
}

func argoStepDetail(r *unstructured.Unstructured, updated, desired, available int32) string {
	steps, stepsFound, _ := unstructured.NestedSlice(r.Object, "spec", "strategy", "canary", "steps")
	if step, found, _ := unstructured.NestedInt64(r.Object, "status", "currentStepIndex"); found && stepsFound && len(steps) > 0 {
		displayStep := step + 1
		if displayStep < 1 {
			displayStep = 1
		}
		if displayStep > int64(len(steps)) {
			displayStep = int64(len(steps))
		}
		return fmt.Sprintf("Step %d · %d/%d updated · %d available", displayStep, updated, desired, available)
	}
	return replicaDetail(updated, desired, available)
}

func conditionFailed(conditions []appsv1.DeploymentCondition, conditionType appsv1.DeploymentConditionType, reason string) bool {
	for _, condition := range conditions {
		if condition.Type == conditionType && (condition.Status == "False" || condition.Reason == reason) {
			return true
		}
	}
	return false
}

func conditionMessage(conditions []appsv1.DeploymentCondition, conditionType appsv1.DeploymentConditionType, fallback string) string {
	for _, condition := range conditions {
		if condition.Type == conditionType && condition.Message != "" {
			return condition.Message
		}
	}
	return fallback
}

func nestedInt32(obj map[string]any, fields ...string) int32 {
	value, found, _ := unstructured.NestedInt64(obj, fields...)
	if found {
		return int32(value)
	}
	return 0
}

func activityCounts(desired, updated, ready, available int32) WorkloadRolloutActivity {
	return activity(RolloutIdle, false, "Stable", "", desired, updated, ready, available)
}

func activity(phase RolloutPhase, active bool, label, detail string, desired, updated, ready, available int32) WorkloadRolloutActivity {
	return WorkloadRolloutActivity{Phase: phase, Active: active, Label: label, Detail: detail, Desired: desired, Updated: updated, Ready: ready, Available: available}
}

func (a WorkloadRolloutActivity) with(phase RolloutPhase, active bool, label, detail string) WorkloadRolloutActivity {
	a.Phase = phase
	a.Active = active
	a.Manual = false
	a.Label = label
	a.Detail = detail
	return a
}

func (a WorkloadRolloutActivity) withAction(phase RolloutPhase, active bool, label, detail string) WorkloadRolloutActivity {
	a = a.with(phase, active, label, detail)
	a.Manual = true
	return a
}

func replicaDetail(updated, desired, available int32) string {
	return fmt.Sprintf("%d/%d updated · %d available", updated, desired, available)
}
