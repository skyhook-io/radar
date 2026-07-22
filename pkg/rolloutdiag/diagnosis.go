package rolloutdiag

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
)

const (
	reasonAllReplicasUnavailableWithoutSurge = "all_replicas_unavailable_without_surge"
	Description                              = "A RollingUpdate that permits every replica to be unavailable while allowing no surge can remove all old pods before replacements are ready. Workloads that intentionally require no overlap should use Recreate to express full replacement explicitly."
	Remediation                              = "Set maxSurge to at least 1 (or a positive percentage) and/or lower maxUnavailable below the full replica count; use strategy.type: Recreate only when a full replacement is intentional."
)

type Risk struct {
	Reason                 string
	Replicas               int32
	MaxSurge               string
	MaxUnavailable         string
	ResolvedMaxSurge       int32
	ResolvedMaxUnavailable int32
	Message                string
	Remediation            string
}

func Applicable(deployment *appsv1.Deployment) bool {
	// This signal targets rollout policies that erase existing multi-replica availability.
	if deployment == nil || desiredReplicas(deployment) < 2 {
		return false
	}
	return deployment.Spec.Strategy.Type == "" || deployment.Spec.Strategy.Type == appsv1.RollingUpdateDeploymentStrategyType
}

func Analyze(deployment *appsv1.Deployment) *Risk {
	if !Applicable(deployment) {
		return nil
	}

	rollingUpdate := deployment.Spec.Strategy.RollingUpdate
	if rollingUpdate == nil || rollingUpdate.MaxSurge == nil || rollingUpdate.MaxUnavailable == nil {
		return nil
	}

	replicas := desiredReplicas(deployment)
	maxSurge, maxUnavailable, err := resolveFenceposts(rollingUpdate.MaxSurge, rollingUpdate.MaxUnavailable, replicas)
	if err != nil || maxSurge != 0 || maxUnavailable < int(replicas) {
		return nil
	}

	return &Risk{
		Reason:                 reasonAllReplicasUnavailableWithoutSurge,
		Replicas:               replicas,
		MaxSurge:               rollingUpdate.MaxSurge.String(),
		MaxUnavailable:         rollingUpdate.MaxUnavailable.String(),
		ResolvedMaxSurge:       int32(maxSurge),
		ResolvedMaxUnavailable: int32(maxUnavailable),
		Message: fmt.Sprintf(
			"RollingUpdate maxUnavailable=%s and maxSurge=%s permit the controller to remove all %d old replicas with no surge capacity, so a rollout can drop to zero available pods and leave no old-version fallback if replacements fail readiness.",
			rollingUpdate.MaxUnavailable.String(), rollingUpdate.MaxSurge.String(), replicas,
		),
		Remediation: Remediation,
	}
}

func desiredReplicas(deployment *appsv1.Deployment) int32 {
	if deployment.Spec.Replicas == nil {
		return 1
	}
	return *deployment.Spec.Replicas
}

func resolveFenceposts(maxSurge, maxUnavailable *intstr.IntOrString, replicas int32) (int, int, error) {
	resolvedSurge, err := intstr.GetScaledValueFromIntOrPercent(maxSurge, int(replicas), true)
	if err != nil {
		return 0, 0, err
	}
	resolvedUnavailable, err := intstr.GetScaledValueFromIntOrPercent(maxUnavailable, int(replicas), false)
	if err != nil {
		return 0, 0, err
	}
	// The Deployment controller forces progress when both rounded values are zero.
	if resolvedSurge == 0 && resolvedUnavailable == 0 {
		resolvedUnavailable = 1
	}
	return resolvedSurge, resolvedUnavailable, nil
}
