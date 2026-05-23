package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestNormalizeTopMetricsOptions(t *testing.T) {
	got := NormalizeTopMetricsOptions(TopMetricsOptions{Kind: "workload", Sort: "memory"})
	if got.Kind != TopMetricsKindWorkloads {
		t.Fatalf("Kind = %q, want workloads", got.Kind)
	}
	if got.Sort != TopMetricsSortMemory {
		t.Fatalf("Sort = %q, want memory", got.Sort)
	}
	if got.Limit != DefaultTopMetricsLimit {
		t.Fatalf("Limit = %d, want default %d", got.Limit, DefaultTopMetricsLimit)
	}

	got = NormalizeTopMetricsOptions(TopMetricsOptions{Kind: "nodes", Sort: "bogus", Limit: 1000})
	if got.Sort != TopMetricsSortCPU {
		t.Fatalf("Sort = %q, want cpu", got.Sort)
	}
	if got.Limit != MaxTopMetricsLimit {
		t.Fatalf("Limit = %d, want max %d", got.Limit, MaxTopMetricsLimit)
	}
}

func TestSortAndLimitTopMetrics(t *testing.T) {
	resp := TopMetricsResponse{
		Items: []TopMetricsItem{
			{Name: "low", CPU: 10, Memory: 100},
			{Name: "high", CPU: 30, Memory: 10},
			{Name: "mid", CPU: 20, Memory: 300},
		},
	}
	sortAndLimitTopMetrics(&resp, TopMetricsSortCPU, 2)
	if len(resp.Items) != 2 || resp.Items[0].Name != "high" || resp.Items[1].Name != "mid" {
		t.Fatalf("CPU sort/limit got %+v", resp.Items)
	}

	resp = TopMetricsResponse{
		Workloads: []TopWorkloadMetrics{
			{Name: "low", CPU: 10, Memory: 100},
			{Name: "high", CPU: 30, Memory: 10},
			{Name: "mid", CPU: 20, Memory: 300},
		},
	}
	sortAndLimitTopMetrics(&resp, TopMetricsSortMemory, 2)
	if len(resp.Workloads) != 2 || resp.Workloads[0].Name != "mid" || resp.Workloads[1].Name != "low" {
		t.Fatalf("memory sort/limit got %+v", resp.Workloads)
	}
}

func TestTopOwnerForPodStripsReplicaSetHash(t *testing.T) {
	controller := true
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: "api-7d5-pod",
			OwnerReferences: []metav1.OwnerReference{{
				Kind:       "ReplicaSet",
				Name:       "api-7d5",
				Controller: &controller,
			}},
		},
	}
	owner := topOwnerForPod(pod)
	if owner == nil || owner.Kind != "Deployment" || owner.Name != "api" {
		t.Fatalf("owner = %+v, want Deployment/api", owner)
	}
}
