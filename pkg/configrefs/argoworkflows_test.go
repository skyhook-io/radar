package configrefs

import (
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func TestArgoWorkflowCronOwnerNamePrefersControllerOwnerReference(t *testing.T) {
	controller := true
	wf := &unstructured.Unstructured{}
	wf.SetLabels(map[string]string{"workflows.argoproj.io/cron-workflow": "label-owner"})
	wf.SetOwnerReferences([]metav1.OwnerReference{{
		APIVersion: "argoproj.io/v1alpha1",
		Kind:       "CronWorkflow",
		Name:       "owner-ref",
		Controller: &controller,
	}})

	if got := ArgoWorkflowCronOwnerName(wf); got != "owner-ref" {
		t.Fatalf("ArgoWorkflowCronOwnerName = %q, want owner-ref", got)
	}
}
