package issuecorrelation

import (
	"testing"
	"time"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/skyhook-io/radar/internal/k8s"
	"github.com/skyhook-io/radar/internal/meaningfulchanges"
	"github.com/skyhook-io/radar/pkg/issuesapi"
)

func TestSecretCorrelationHistoryComplete(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	deployment := &appsv1.Deployment{
		ObjectMeta: metav1.ObjectMeta{Name: "reader", Namespace: "shop", UID: "reader", CreationTimestamp: metav1.NewTime(now)},
		Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
			Name: "app", Image: "example:v1",
			Env: []corev1.EnvVar{{Name: "PASSWORD", ValueFrom: &corev1.EnvVarSource{SecretKeyRef: &corev1.SecretKeySelector{LocalObjectReference: corev1.LocalObjectReference{Name: "db"}, Key: "password"}}}},
		}}}}},
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "db", Namespace: "shop", CreationTimestamp: metav1.NewTime(now.Add(-time.Hour))}}
	initCache(t, deployment, secret)
	coverage := issuesapi.HistoryCoverage{Incomplete: true, CompleteSince: now.Add(-time.Minute)}
	if historyCoversObjects(deployment, meaningfulchanges.DirectConfigReferences(deployment), "shop", coverage) {
		t.Fatal("old referenced Secret incorrectly treated as complete")
	}
	fresh := secret.DeepCopy()
	fresh.CreationTimestamp = metav1.NewTime(now)
	k8s.ResetTestState()
	initCache(t, deployment, fresh)
	if !historyCoversObjects(deployment, meaningfulchanges.DirectConfigReferences(deployment), "shop", coverage) {
		t.Fatal("new referenced Secret should cover its whole lifetime")
	}
}

// A Rollout that takes its template from another workload changes when that
// workload does, so its own age doesn't make its history complete.
func TestRolloutWithWorkloadRefHistoryNotComplete(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	rollout := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "argoproj.io/v1alpha1", "kind": "Rollout",
		"metadata": map[string]any{"name": "roll", "namespace": "shop", "creationTimestamp": now.Format(time.RFC3339)},
		"spec":     map[string]any{"workloadRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "api"}}}}
	initCache(t)
	coverage := issuesapi.HistoryCoverage{Incomplete: true, CompleteSince: now.Add(-time.Minute)}
	if historyCoversObjects(rollout, nil, "shop", coverage) {
		t.Fatal("a Rollout templated from another workload treated as complete")
	}
}
