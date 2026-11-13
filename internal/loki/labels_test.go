package loki

import "testing"

func TestDetectLabels(t *testing.T) {
	if got := DetectLabels([]string{"namespace", "pod", "container", "job"}); got != (StreamLabels{Namespace: "namespace", Pod: "pod", Container: "container"}) {
		t.Errorf("plain names: %+v (job is not a Job-name label)", got)
	}
	if got := DetectLabels([]string{"kubernetes_namespace_name", "kubernetes_pod_name", "kubernetes_container_name"}); got != (StreamLabels{Namespace: "kubernetes_namespace_name", Pod: "kubernetes_pod_name", Container: "kubernetes_container_name"}) {
		t.Errorf("fluent-bit names: %+v", got)
	}
	if got := DetectLabels([]string{"pod", "namespace", "job_name"}); got.Job != "job_name" {
		t.Errorf("job label: %+v", got)
	}
	if got := DetectLabels([]string{"namespace", "kubernetes_namespace_name", "pod"}); got.Namespace != "namespace" {
		t.Errorf("first candidate in list order wins: %+v", got)
	}
	if r := DetectLabels([]string{"app", "filename"}).HistoryReason(); r != "Loki has no namespace or pod stream label; History needs both" {
		t.Errorf("reason = %q", r)
	}
	if r := DetectLabels([]string{"namespace", "pod"}).HistoryReason(); r != "" {
		t.Errorf("reason = %q", r)
	}
}
