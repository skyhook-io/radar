package server

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/skyhook-io/radar/pkg/k8score"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func comparisonJob(name, role string) *batchv1.Job {
	controller := true
	return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "training", UID: types.UID(name), Labels: map[string]string{replicatedJobNameLabel: role}, OwnerReferences: []metav1.OwnerReference{{APIVersion: jobSetAPIVersion, Kind: "JobSet", Name: "wide", UID: "root", Controller: &controller}}}}
}

func TestJobSetFiltersAndSelectionPrecedeCap(t *testing.T) {
	root := testJobSet("training", "wide", "root")
	jobs := []*batchv1.Job{}
	for i := 0; i < 250; i++ {
		jobs = append(jobs, comparisonJob(fmt.Sprintf("worker-%03d", i), "workers"))
	}
	jobs = append(jobs, comparisonJob("z-leader", "leader"))
	jobs[240].Status.Active = 1
	query := jobSetMemberQuery{Role: "leader", Selected: "jobs/training/worker-249"}
	got := jobSetMemberRuns(root, jobs, query)
	if got.Total != 251 || *got.FilteredTotal != 1 || got.Truncated || len(got.Runs) != 1 || got.Runs[0].Name != "z-leader" || got.Selected == nil || got.Selected.Name != "worker-249" {
		t.Fatalf("filtered: %+v", got)
	}
	got = jobSetMemberRuns(root, jobs, jobSetMemberQuery{Search: "240", State: "active"})
	if len(got.Runs) != 1 || got.Runs[0].Name != "worker-240" {
		t.Fatalf("search/state: %+v", got)
	}
	jobs[249].OwnerReferences[0].UID = "old-root"
	got = jobSetMemberRuns(root, jobs, query)
	if got.Selected != nil || got.Total != 250 {
		t.Fatalf("stale member resolved: %+v", got)
	}
}

func comparisonPod(job *batchv1.Job, now time.Time) *corev1.Pod {
	controller := true
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: job.Name + "-pod", Namespace: job.Namespace, UID: types.UID(job.Name + "-pod-uid"), CreationTimestamp: metav1.NewTime(now.Add(-time.Hour)), OwnerReferences: []metav1.OwnerReference{{APIVersion: "batch/v1", Kind: "Job", Name: job.Name, UID: job.UID, Controller: &controller}}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "work", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m"), corev1.ResourceMemory: resource.MustParse("64Mi")}}}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, ContainerStatuses: []corev1.ContainerStatus{{Name: "work", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}}}
}

func comparisonMetric(now time.Time) k8score.PodMetrics {
	return k8score.PodMetrics{Timestamp: now.Add(-10 * time.Second).Format(time.RFC3339Nano), Window: "30s", Containers: []k8score.ContainerMetrics{{Name: "work", Usage: k8score.ResourceUsage{CPU: "25m", Memory: "32Mi"}}}}
}

func TestJobSetResourceCoverageAndOwnership(t *testing.T) {
	now := time.Now()
	root := testJobSet("training", "wide", "root")
	jobs := []*batchv1.Job{comparisonJob("a", "leader"), comparisonJob("b", "workers"), comparisonJob("c", "prepare")}
	pods := []*corev1.Pod{comparisonPod(jobs[0], now), comparisonPod(jobs[1], now), comparisonPod(jobs[2], now)}
	pods[2].Status.Phase = corev1.PodSucceeded
	spoof := pods[0].DeepCopy()
	spoof.Name = "spoof"
	spoof.OwnerReferences[0].UID = "replaced-job"
	pods = append(pods, spoof)
	owned := jobSetOwnedPods(root, jobs, pods)
	if len(owned) != 3 {
		t.Fatalf("ownership: %d", len(owned))
	}
	metric := comparisonMetric(now)
	metrics := map[string]k8score.PodMetrics{pods[0].Name: metric, pods[2].Name: metric, spoof.Name: metric}
	got := buildJobSetResources(root, jobs, owned, metrics, jobSetMemberQuery{Role: "prepare"}, now)
	if got.Total.RunningPods != 2 || got.Total.ReportingPods != 1 || *got.Total.CPU != 25000000 || got.Total.CPURequest != 100000000 {
		t.Fatalf("root coverage: %+v", got.Total)
	}
	if len(got.Members) != 1 || got.Members["c"].CPU != nil || got.Members["c"].RunningPods != 0 {
		t.Fatalf("completed member: %+v", got.Members)
	}
	got = buildJobSetResources(root, jobs, owned, metrics, jobSetMemberQuery{Role: "prepare", Selected: "jobs/training/a"}, now)
	if len(got.Members) != 2 || got.Members["a"] == nil || got.Members["a"].CPU == nil || *got.Members["a"].CPU != 25000000 {
		t.Fatalf("selected member outside role filter lost usage: %+v", got.Members)
	}
	metric.Timestamp = now.Add(-3 * time.Minute).Format(time.RFC3339Nano)
	metrics[pods[1].Name] = metric
	got = buildJobSetResources(root, jobs, owned, metrics, jobSetMemberQuery{}, now)
	if got.Total.StalePods != 1 || got.Total.ReportingPods != 1 {
		t.Fatalf("stale: %+v", got.Total)
	}
}

func TestJobSetMetricSamplesPreserveZeroAndRejectIncompleteOrOldLifetime(t *testing.T) {
	now := time.Now()
	pod := comparisonPod(comparisonJob("a", "leader"), now)
	metric := comparisonMetric(now)
	metric.Containers[0].Usage.CPU = "0"
	metric.Containers[0].Usage.Memory = "0"
	cpu, memory, _, state := currentPodUsage(pod, metric, now)
	if state != "available" || cpu != 0 || memory != 0 {
		t.Fatalf("zero: %d %d %s", cpu, memory, state)
	}
	metric.Containers[0].Usage.CPU = "invalid"
	if _, _, _, state = currentPodUsage(pod, metric, now); state != "missing" {
		t.Fatal(state)
	}
	metric = comparisonMetric(now)
	pod.CreationTimestamp = metav1.NewTime(now.Add(-20 * time.Second))
	if _, _, _, state = currentPodUsage(pod, metric, now); state != "missing" {
		t.Fatal("sample window crosses object lifetime")
	}
	pod.CreationTimestamp = metav1.NewTime(now.Add(-time.Hour))
	always := corev1.ContainerRestartPolicyAlways
	pod.Spec.InitContainers = []corev1.Container{{Name: "sidecar", RestartPolicy: &always}, {Name: "setup"}}
	pod.Status.InitContainerStatuses = []corev1.ContainerStatus{{Name: "sidecar", State: corev1.ContainerState{Running: &corev1.ContainerStateRunning{}}}}
	if _, _, _, state = currentPodUsage(pod, metric, now); state != "missing" {
		t.Fatal("missing native sidecar sample accepted")
	}
	metric.Containers = append(metric.Containers, k8score.ContainerMetrics{Name: "sidecar", Usage: k8score.ResourceUsage{CPU: "1m", Memory: "1Mi"}})
	if _, _, _, state = currentPodUsage(pod, metric, now); state != "available" {
		t.Fatal("ordinary init container should not be required", state)
	}
}

func TestJobSetExtendedRequestsAreDeclaredAndIncludeInitMaximum(t *testing.T) {
	now := time.Now()
	root := testJobSet("training", "wide", "root")
	job := comparisonJob("a", "leader")
	pod := comparisonPod(job, now)
	pod.Spec.Containers[0].Resources.Requests["nvidia.com/gpu"] = resource.MustParse("1")
	pod.Spec.InitContainers = []corev1.Container{{Name: "setup", Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{"nvidia.com/gpu": resource.MustParse("2")}}}}
	got := buildJobSetResources(root, []*batchv1.Job{job}, []jobSetPod{{Pod: pod, Job: job}}, nil, jobSetMemberQuery{}, now)
	q := got.Total.ExtendedRequests["nvidia.com/gpu"]
	if q.Value() != 2 || got.Total.CPU != nil {
		t.Fatalf("requests must not imply utilization: %+v", got.Total)
	}
}

func TestSnapshotBoundsAndPartialFailures(t *testing.T) {
	var active, peak, count atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := active.Add(1)
		defer active.Add(-1)
		count.Add(1)
		for old := peak.Load(); n > old; old = peak.Load() {
			if peak.CompareAndSwap(old, n) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		if strings.Contains(r.URL.Path, "pod-00/") {
			http.Error(w, "unreadable", http.StatusForbidden)
			return
		}
		if strings.Contains(r.URL.Path, "pod-01/") {
			fmt.Fprint(w, strings.Repeat("2026-09-22T00:00:00Z line\n", 5000))
			return
		}
		fmt.Fprint(w, "2026-09-22T00:00:00Z hello\n")
	}))
	defer server.Close()
	client, err := kubernetes.NewForConfig(&rest.Config{Host: server.URL, QPS: 1000, Burst: 1000})
	if err != nil {
		t.Fatal(err)
	}
	pods := []*corev1.Pod{}
	for i := 0; i < 45; i++ {
		pod := comparisonPod(comparisonJob("source", "workers"), time.Now())
		pod.Name = fmt.Sprintf("pod-%02d", i)
		pod.CreationTimestamp = metav1.Time{}
		pods = append(pods, pod)
	}
	pending := comparisonPod(comparisonJob("pending", "workers"), time.Now())
	pending.Status.ContainerStatuses = nil
	pods = append(pods, pending)
	got := collectLogsFromPods(context.Background(), client, "training", pods, "", 1000, nil, true)
	if count.Load() != 40 || peak.Load() > 8 || len(got.SourcePods) != 40 {
		t.Fatalf("bounds: calls=%d peak=%d pods=%d", count.Load(), peak.Load(), len(got.SourcePods))
	}
	for _, text := range []string{"40 of 45", "64 KiB", "1 sources could not be read"} {
		if !strings.Contains(got.Notice, text) {
			t.Fatalf("notice %q missing %q", got.Notice, text)
		}
	}
	if len(got.Logs) == 0 {
		t.Fatal("partial success lost")
	}
	count.Store(0)
	unbounded := collectLogsFromPods(context.Background(), client, "training", pods, "", 2000, nil, false)
	if count.Load() != 46 || strings.Contains(unbounded.Notice, "64 KiB") || strings.Contains(unbounded.Notice, "Showing") {
		t.Fatalf("existing snapshot route was capped: calls=%d notice=%s", count.Load(), unbounded.Notice)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if got := collectLogsFromPods(ctx, client, "training", pods, "", 100, nil, true); got.Notice == "" {
		t.Fatal("cancellation hidden")
	}
}

func TestSnapshotSortsFractionalTimestamps(t *testing.T) {
	logs := []workloadLogEntry{{Timestamp: "2026-09-22T00:00:00.1Z"}, {Timestamp: "2026-09-22T00:00:00Z"}, {Timestamp: "2026-09-22T00:00:00.01Z"}}
	sortLogsByTimestamp(logs)
	if logs[0].Timestamp != "2026-09-22T00:00:00Z" || logs[2].Timestamp != "2026-09-22T00:00:00.1Z" {
		t.Fatalf("order: %+v", logs)
	}
}

func TestJobSetMetricsIgnoreExitedContainersAndPreserveOldestSample(t *testing.T) {
	now := time.Now()
	pod := comparisonPod(comparisonJob("a", "leader"), now)
	pod.Spec.Containers = append(pod.Spec.Containers, corev1.Container{Name: "helper"})
	pod.Status.ContainerStatuses = append(pod.Status.ContainerStatuses, corev1.ContainerStatus{Name: "helper", State: corev1.ContainerState{Terminated: &corev1.ContainerStateTerminated{}}})
	if _, _, _, state := currentPodUsage(pod, comparisonMetric(now), now); state != "available" {
		t.Fatal(state)
	}
	pod.Status.ContainerStatuses[0].State = corev1.ContainerState{Waiting: &corev1.ContainerStateWaiting{}}
	if _, _, _, state := currentPodUsage(pod, comparisonMetric(now), now); state != "missing" {
		t.Fatal("no running containers", state)
	}
	total := jobSetUsage{ObservedAt: "2026-09-22T00:00:00.1Z"}
	addJobSetUsage(&total, jobSetUsage{ObservedAt: "2026-09-22T00:00:00Z"})
	if total.ObservedAt != "2026-09-22T00:00:00Z" {
		t.Fatal(total.ObservedAt)
	}
}
