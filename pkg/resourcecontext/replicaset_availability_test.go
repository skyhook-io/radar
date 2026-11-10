package resourcecontext

import (
	"context"
	"fmt"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestBuild_ReplicaSetConsumerSourceUnavailable(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "settings"}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "previous"}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}}}}}}}
	rc := Build(context.Background(), cm, Options{Tier: TierBasic, Provider: unavailableConsumerProvider{mockResourceProvider: mockResourceProvider{replicaSets: []*appsv1.ReplicaSet{rs}}, failedKind: "ReplicaSet"}})
	if rc.ReferencedBy != nil || !slices.Contains(rc.Omitted, OmittedField{Field: "referencedBy", Reason: OmittedUnavailable}) {
		t.Fatalf("failed consumer list presented as known result: %+v", rc)
	}
}

type unavailableConsumerProvider struct {
	mockResourceProvider
	failedKind string
}

func (p unavailableConsumerProvider) Deployments() ([]*appsv1.Deployment, error) {
	if p.failedKind == "Deployment" {
		return p.deploys, fmt.Errorf("fixture failure")
	}
	return p.deploys, nil
}
func (p unavailableConsumerProvider) StatefulSets() ([]*appsv1.StatefulSet, error) {
	if p.failedKind == "StatefulSet" {
		return p.statefulSets, fmt.Errorf("fixture failure")
	}
	return p.statefulSets, nil
}
func (p unavailableConsumerProvider) DaemonSets() ([]*appsv1.DaemonSet, error) {
	if p.failedKind == "DaemonSet" {
		return p.daemonSets, fmt.Errorf("fixture failure")
	}
	return p.daemonSets, nil
}
func (p unavailableConsumerProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error) {
	if p.failedKind == "ReplicaSet" {
		return p.replicaSets, fmt.Errorf("fixture failure")
	}
	return p.replicaSets, nil
}
func (p unavailableConsumerProvider) Jobs() ([]*batchv1.Job, error) {
	if p.failedKind == "Job" {
		return p.jobs, fmt.Errorf("fixture failure")
	}
	return p.jobs, nil
}
func (p unavailableConsumerProvider) CronJobs() ([]*batchv1.CronJob, error) {
	if p.failedKind == "CronJob" {
		return p.cronJobs, fmt.Errorf("fixture failure")
	}
	return p.cronJobs, nil
}
func (p unavailableConsumerProvider) Pods() ([]*corev1.Pod, error) {
	if p.failedKind == "Pod" {
		return p.pods, fmt.Errorf("fixture failure")
	}
	return p.pods, nil
}

func TestBuild_EveryConsumerFailureRetainsOtherSources(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "team"}}
	meta := metav1.ObjectMeta{Name: "consumer", Namespace: "team"}
	spec := corev1.PodSpec{Containers: []corev1.Container{{EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}}}}
	template := corev1.PodTemplateSpec{Spec: spec}
	provider := mockResourceProvider{
		deploys:      []*appsv1.Deployment{{ObjectMeta: meta, Spec: appsv1.DeploymentSpec{Template: template}}},
		statefulSets: []*appsv1.StatefulSet{{ObjectMeta: meta, Spec: appsv1.StatefulSetSpec{Template: template}}},
		daemonSets:   []*appsv1.DaemonSet{{ObjectMeta: meta, Spec: appsv1.DaemonSetSpec{Template: template}}},
		replicaSets:  []*appsv1.ReplicaSet{{ObjectMeta: meta, Spec: appsv1.ReplicaSetSpec{Template: template}}},
		jobs:         []*batchv1.Job{{ObjectMeta: meta, Spec: batchv1.JobSpec{Template: template}}},
		cronJobs:     []*batchv1.CronJob{{ObjectMeta: meta, Spec: batchv1.CronJobSpec{JobTemplate: batchv1.JobTemplateSpec{Spec: batchv1.JobSpec{Template: template}}}}},
		pods:         []*corev1.Pod{{ObjectMeta: meta, Spec: spec}},
	}
	for _, kind := range []string{"Deployment", "StatefulSet", "DaemonSet", "ReplicaSet", "Job", "CronJob", "Pod"} {
		t.Run(kind, func(t *testing.T) {
			rc := Build(context.Background(), cm, Options{Tier: TierBasic, Provider: unavailableConsumerProvider{provider, kind}})
			if !slices.Contains(rc.Omitted, OmittedField{Field: "referencedBy", Reason: OmittedUnavailable}) || rc.ReferencedBy == nil || rc.ReferencedBy.Total != 6 {
				t.Fatalf("failed source hid coverage or loaded references: %+v", rc)
			}
			for _, ref := range rc.ReferencedBy.Items {
				if ref.Kind == kind {
					t.Fatalf("failed source emitted stale reference: %+v", ref)
				}
			}
		})
	}
}
func TestBuild_FailedPodSourceWithoutOtherReferences(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Name: "settings", Namespace: "team"}}
	rc := Build(context.Background(), cm, Options{Tier: TierBasic, Provider: unavailableConsumerProvider{failedKind: "Pod"}})
	if rc.ReferencedBy != nil || !slices.Contains(rc.Omitted, OmittedField{Field: "referencedBy", Reason: OmittedUnavailable}) {
		t.Fatalf("failed Pod inventory masqueraded as empty: %+v", rc)
	}
}
