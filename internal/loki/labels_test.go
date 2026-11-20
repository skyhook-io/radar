package loki

import (
	"strings"
	"testing"
)

func TestDetectLabels(t *testing.T) {
	if got := DetectLabels([]string{"namespace", "pod", "container", "job"}); got != (StreamLabels{Namespace: "namespace", Pod: "pod", Container: "container"}) {
		t.Errorf("plain names: %+v (job is not a Job-name label)", got)
	}
	if got := DetectLabels([]string{"kubernetes_namespace_name", "kubernetes_pod_name", "kubernetes_container_name"}); got != (StreamLabels{Namespace: "kubernetes_namespace_name", Pod: "kubernetes_pod_name", Container: "kubernetes_container_name"}) {
		t.Errorf("fluent-bit names: %+v", got)
	}
	// The Logging operator's fluentd Loki output.
	if got := DetectLabels([]string{"app", "container_image", "container_name", "host", "namespace_name", "pod_name"}); got != (StreamLabels{Namespace: "namespace_name", Pod: "pod_name", Container: "container_name"}) {
		t.Errorf("Logging operator names: %+v", got)
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

func TestClusterLabel(t *testing.T) {
	if got := DetectLabels([]string{"namespace", "pod", "k8s_cluster_name"}); got.Cluster != "k8s_cluster_name" {
		t.Errorf("cluster label: %+v", got)
	}
	shared := StreamLabels{Namespace: "namespace", Pod: "pod", Cluster: "cluster"}
	if r := shared.HistoryReason(); r != "" {
		t.Errorf("without auth a cluster label alone doesn't refuse: %q", r)
	}
	shared.ClusterRequired = true
	if r := shared.HistoryReason(); r == "" || !strings.Contains(r, "--loki-cluster") {
		t.Errorf("with auth, a cluster label and no value must refuse: %q", r)
	}
	shared.ClusterValue = "prod"
	if r := shared.HistoryReason(); r != "" {
		t.Errorf("a configured value reads: %q", r)
	}
	// A value with no label to match it on can't keep other clusters out.
	unmatched := StreamLabels{Namespace: "namespace", Pod: "pod", ClusterValue: "prod"}
	if r := unmatched.HistoryReason(); r == "" {
		t.Error("a cluster value with no cluster label must refuse")
	}
}

func TestParseCluster(t *testing.T) {
	for in, want := range map[string][2]string{"prod": {"", "prod"}, "region_cluster=prod": {"region_cluster", "prod"}, " prod ": {"", "prod"}} {
		if l, v, err := ParseCluster(in); err != nil || l != want[0] || v != want[1] {
			t.Errorf("ParseCluster(%q) = %q, %q, %v", in, l, v, err)
		}
	}
	for _, bad := range []string{`a"} or {x=`, "=prod", "region="} {
		if _, _, err := ParseCluster(bad); err == nil {
			t.Errorf("ParseCluster(%q) accepted", bad)
		}
	}
}
