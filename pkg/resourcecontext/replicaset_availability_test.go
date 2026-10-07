package resourcecontext

import (
	"context"
	"fmt"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type unavailableReplicaSetProvider struct{ mockResourceProvider }

func (p unavailableReplicaSetProvider) ReplicaSets() ([]*appsv1.ReplicaSet, error) {
	return p.replicaSets, fmt.Errorf("unavailable fixture source")
}

func TestBuild_ReplicaSetConsumerSourceUnavailable(t *testing.T) {
	cm := &corev1.ConfigMap{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "settings"}}
	rs := &appsv1.ReplicaSet{ObjectMeta: metav1.ObjectMeta{Namespace: "team", Name: "previous"}, Spec: appsv1.ReplicaSetSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "settings"}}}}}}}}}}
	rc := Build(context.Background(), cm, Options{Tier: TierBasic, Provider: unavailableReplicaSetProvider{mockResourceProvider{replicaSets: []*appsv1.ReplicaSet{rs}}}})
	if rc.ReferencedBy != nil || !slices.Contains(rc.Omitted, OmittedField{Field: "referencedBy", Reason: OmittedUnavailable}) {
		t.Fatalf("failed consumer list presented as known result: %+v", rc)
	}
}
