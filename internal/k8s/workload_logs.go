package k8s

import (
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func IsJobTerminal(job *batchv1.Job) bool {
	for _, condition := range job.Status.Conditions {
		if (condition.Type == batchv1.JobComplete || condition.Type == batchv1.JobFailed) && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}

func IsWorkflowTerminal(workflow map[string]any) bool {
	phase, _, _ := unstructured.NestedString(workflow, "status", "phase")
	return phase == "Succeeded" || phase == "Failed" || phase == "Error"
}

func WorkflowArchiveLogsConfigured(workflow map[string]any) bool {
	if archiveLogs, ok, _ := unstructured.NestedBool(workflow, "spec", "archiveLogs"); ok && archiveLogs {
		return true
	}
	templates, ok, _ := unstructured.NestedSlice(workflow, "spec", "templates")
	if !ok {
		return false
	}
	for _, template := range templates {
		templateMap, ok := template.(map[string]any)
		if !ok {
			continue
		}
		if archiveLogs, ok, _ := unstructured.NestedBool(templateMap, "archiveLocation", "archiveLogs"); ok && archiveLogs {
			return true
		}
	}
	return false
}
