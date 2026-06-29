package k8s

import (
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
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

func TestTopOwnerForPodIgnoresNonControllerOwnerRefs(t *testing.T) {
	controller := false
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "api-pod",
			Namespace: "default",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "apps/v1",
				Kind:       "Deployment",
				Name:       "api",
				Controller: &controller,
			}},
		},
	}
	if owner := topOwnerForPod(pod); owner != nil {
		t.Fatalf("topOwnerForPod = %+v, want nil for non-controller ownerRef", owner)
	}
	if owner := topOwnerForPodResolved(nil, pod); owner != nil {
		t.Fatalf("topOwnerForPodResolved = %+v, want nil for non-controller ownerRef", owner)
	}
}

func TestTopOwnerForPodResolvedCollapsesJobToCronJob(t *testing.T) {
	defer ResetTestState()
	controller := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "calico-token-refresh-123",
			Namespace: "kube-system",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "CronJob",
				Name:       "calico-token-refresh",
				Controller: &controller,
			}},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "calico-token-refresh-123-abc",
			Namespace: "kube-system",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "Job",
				Name:       "calico-token-refresh-123",
				Controller: &controller,
			}},
		},
	}
	if err := InitTestResourceCache(fake.NewClientset(job)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	owner := topOwnerForPodResolved(GetResourceCache(), pod)
	if owner == nil || owner.Group != "batch" || owner.Kind != "CronJob" || owner.Name != "calico-token-refresh" {
		t.Fatalf("owner = %+v, want batch/CronJob/calico-token-refresh", owner)
	}
}

func TestTopOwnerForPodResolvedKeepsStandaloneJob(t *testing.T) {
	defer ResetTestState()
	controller := true
	job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "manual", Namespace: "prod"}}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "manual-abc",
			Namespace: "prod",
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "Job",
				Name:       "manual",
				Controller: &controller,
			}},
		},
	}
	if err := InitTestResourceCache(fake.NewClientset(job)); err != nil {
		t.Fatalf("InitTestResourceCache: %v", err)
	}

	owner := topOwnerForPodResolved(GetResourceCache(), pod)
	if owner == nil || owner.Group != "batch" || owner.Kind != "Job" || owner.Name != "manual" {
		t.Fatalf("owner = %+v, want batch/Job/manual", owner)
	}
}
