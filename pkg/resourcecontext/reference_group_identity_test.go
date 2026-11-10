package resourcecontext

import (
	"context"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"testing"
)

func TestReferencedByPodSpecsOnlyClaimsCoreConfigTargets(t *testing.T) {
	provider := mockResourceProvider{deploys: []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Namespace: "app", Name: "consumer"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", EnvFrom: []corev1.EnvFromSource{{ConfigMapRef: &corev1.ConfigMapEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "shared"}}}, {SecretRef: &corev1.SecretEnvSource{LocalObjectReference: corev1.LocalObjectReference{Name: "shared"}}}}}}}}}}}}
	for _, kind := range []string{"ConfigMap", "Secret"} {
		for _, apiVersion := range []string{"v1", "custom.example.io/v1"} {
			t.Run(kind+"/"+apiVersion, func(t *testing.T) {
				obj := &unstructured.Unstructured{Object: map[string]any{"apiVersion": apiVersion, "kind": kind, "metadata": map[string]any{"namespace": "app", "name": "shared"}}}
				rc := Build(context.Background(), obj, Options{Tier: TierBasic, Provider: provider, AccessChecker: allowAllChecker{}})
				if apiVersion == "v1" {
					if rc.ReferencedBy == nil || rc.ReferencedBy.Total != 1 {
						t.Errorf("core target references=%+v", rc.ReferencedBy)
					}
				} else if rc.ReferencedBy != nil {
					t.Errorf("custom target claimed core references: %+v", rc.ReferencedBy)
				}
			})
		}
	}
}
