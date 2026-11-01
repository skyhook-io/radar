package mcp

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestFlaggerDiagnosisSeparatesDeclaredTargetFromObservedPrimary(t *testing.T) {
	yes, zero := true, int32(0)
	root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "flagger.app/v1beta1", "kind": "Canary", "metadata": map[string]any{"name": "release", "namespace": "observe", "uid": "release-current"}, "spec": map[string]any{"targetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "target"}}, "status": map[string]any{"phase": "Failed"}}}
	target := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "observe", UID: "target"}, Spec: appsv1.DeploymentSpec{Replicas: &zero, Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "target"}}}}
	primary := target.DeepCopy()
	primary.Name = "observed-primary"
	primary.UID = "primary"
	primary.OwnerReferences = []metav1.OwnerReference{{APIVersion: "flagger.app/v1beta1", Kind: "Canary", Name: "release", UID: "release-current", Controller: &yes}}
	event := &corev1.Event{ObjectMeta: metav1.ObjectMeta{Name: "rollout-failed", Namespace: "observe"}, Type: "Warning", Reason: "Synced", Message: "metric check failed", InvolvedObject: corev1.ObjectReference{APIVersion: "flagger.app/v1beta1", Kind: "Canary", Namespace: "observe", Name: "release", UID: "release-current"}}
	old := event.DeepCopy()
	old.Name = "previous-incarnation"
	old.Message = "old failure"
	old.InvolvedObject.UID = "release-old"
	initOperatorDiagnosisCache(t, root, target, primary, event, old)
	result, _, err := handleDiagnose(context.Background(), nil, testDiagnoseInput("canary", "observe", "release"))
	if err != nil {
		t.Fatal(err)
	}
	var got operatorDiagnoseResponse
	if err := json.Unmarshal([]byte(extractText(t, result)), &got); err != nil {
		t.Fatal(err)
	}
	if got.DeclaredTarget == nil || got.DeclaredTarget.Ref.Name != "target" || got.DeclaredTarget.Diagnosis == nil || got.DeclaredTarget.Diagnosis.Pods != 0 {
		t.Fatalf("declared target lost or zero treated as failure: %s", extractText(t, result))
	}
	if len(got.OwnedWorkloads) != 1 || got.OwnedWorkloads[0].Ref.Name != "observed-primary" {
		t.Fatalf("primary inferred from names or target: %+v", got.OwnedWorkloads)
	}
	if len(got.Events) != 1 || got.Events[0].Message != "metric check failed" {
		t.Fatalf("wrong incarnation events: %+v", got.Events)
	}
	if strings.Contains(extractText(t, result), "old failure") {
		t.Fatal("predecessor event leaked into current evidence")
	}
	if len(got.Warnings) == 0 {
		t.Fatal("excluded history was not qualified")
	}
}

func TestFlaggerDiagnosisWithholdsUnreadableDeclaredTarget(t *testing.T) {
	root := &unstructured.Unstructured{Object: map[string]any{"apiVersion": "flagger.app/v1beta1", "kind": "Canary", "metadata": map[string]any{"name": "release", "namespace": "observe", "uid": "release"}, "spec": map[string]any{"targetRef": map[string]any{"apiVersion": "apps/v1", "kind": "Deployment", "name": "target"}}}}
	target := &appsv1.Deployment{ObjectMeta: metav1.ObjectMeta{Name: "target", Namespace: "observe", UID: "target"}, Spec: appsv1.DeploymentSpec{Selector: &metav1.LabelSelector{MatchLabels: map[string]string{"app": "target"}}, Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "private-image-marker", Image: "private-image-marker"}}}}}}
	initOperatorDiagnosisCache(t, root, target)
	ctx := withTestUserPerms(t, "canary-reader", nil, []string{"observe"})
	perms := getPermCache().Get("canary-reader", nil)
	perms.SetCanI("get", "flagger.app", "canaries", "observe", true)
	perms.SetCanI("get", "apps", "deployments", "observe", false)
	result, _, err := handleDiagnose(ctx, nil, testDiagnoseInput("canary", "observe", "release"))
	if err != nil {
		t.Fatal(err)
	}
	text := extractText(t, result)
	if strings.Contains(text, "private-image-marker") {
		t.Fatalf("denied target body leaked: %s", text)
	}
	var got operatorDiagnoseResponse
	if err := json.Unmarshal([]byte(text), &got); err != nil {
		t.Fatal(err)
	}
	if got.DeclaredTarget == nil || got.DeclaredTarget.Diagnosis != nil || !strings.Contains(got.DeclaredTarget.Error, "forbidden") {
		t.Fatalf("denied target not qualified: %s", text)
	}
}
