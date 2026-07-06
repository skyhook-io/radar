package audit

import (
	"strconv"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"

	bp "github.com/skyhook-io/radar/pkg/audit"
	"github.com/skyhook-io/radar/pkg/k8score"
)

type testLister[T any] struct {
	items []*T
}

func (l testLister[T]) List(labels.Selector) ([]*T, error) {
	return l.items, nil
}

func TestListNamespacedFiltersBatchResources(t *testing.T) {
	jobs := listNamespaced(&testLister[batchv1.Job]{items: []*batchv1.Job{
		{ObjectMeta: metav1.ObjectMeta{Name: "keep-job", Namespace: "target"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "drop-job", Namespace: "other"}},
	}}, []string{"target"})
	if len(jobs) != 1 || jobs[0].Name != "keep-job" {
		t.Fatalf("expected only target namespace Job, got %#v", jobs)
	}

	cronJobs := listNamespaced(&testLister[batchv1.CronJob]{items: []*batchv1.CronJob{
		{ObjectMeta: metav1.ObjectMeta{Name: "keep-cronjob", Namespace: "target"}},
		{ObjectMeta: metav1.ObjectMeta{Name: "drop-cronjob", Namespace: "other"}},
	}}, []string{"target"})
	if len(cronJobs) != 1 || cronJobs[0].Name != "keep-cronjob" {
		t.Fatalf("expected only target namespace CronJob, got %#v", cronJobs)
	}
}

func TestJoinPodMetrics(t *testing.T) {
	pod := func(ns, name string, cpuMilli, memMi int64) *corev1.Pod {
		return &corev1.Pod{
			ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name},
			Spec: corev1.PodSpec{Containers: []corev1.Container{{
				Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse(fmtMilli(cpuMilli)),
					corev1.ResourceMemory: resource.MustParse(fmtMi(memMi)),
				}},
			}}},
		}
	}
	pods := []*corev1.Pod{
		pod("prod", "web", 200, 256), // requests: 200m cpu, 256Mi mem
		pod("prod", "no-req-but-in-scope", 0, 0),
		pod("prod", "no-metrics", 100, 128), // in scope but no usage sample
	}
	// Usage: CPU in nanocores, Memory in bytes. web uses 20m (=20e6 nanocores);
	// out-of-scope reports usage but isn't in the audited pod set.
	usage := []k8score.TopPodMetrics{
		{Namespace: "prod", Name: "web", CPU: 20_000_000, Memory: 64 * 1024 * 1024},
		{Namespace: "prod", Name: "no-req-but-in-scope", CPU: 5_000_000, Memory: 8 * 1024 * 1024},
		{Namespace: "other", Name: "out-of-scope", CPU: 999_000_000, Memory: 1 << 30},
	}

	got := joinPodMetrics(usage, pods)

	byName := map[string]bp.PodMetricsInput{}
	for _, m := range got {
		byName[m.Name] = m
	}
	if _, ok := byName["out-of-scope"]; ok {
		t.Error("out-of-scope pod (no matching in-scope pod) must be excluded")
	}
	if _, ok := byName["no-metrics"]; ok {
		t.Error("in-scope pod with no usage sample must be excluded (not evaluated)")
	}
	web := byName["web"]
	if web.CPUUsage != 20 { // 20e6 nanocores → 20 millicores
		t.Errorf("web CPUUsage = %d, want 20 millicores", web.CPUUsage)
	}
	if web.CPURequest != 200 {
		t.Errorf("web CPURequest = %d, want 200 millicores", web.CPURequest)
	}
	if web.MemoryUsage != 64*1024*1024 {
		t.Errorf("web MemoryUsage = %d bytes", web.MemoryUsage)
	}
	if web.MemoryRequest != 256*1024*1024 {
		t.Errorf("web MemoryRequest = %d bytes, want 256Mi", web.MemoryRequest)
	}
	// A pod with usage but zero requests is still surfaced (the check itself
	// treats zero-request pods as out of scope, not this join).
	if _, ok := byName["no-req-but-in-scope"]; !ok {
		t.Error("in-scope pod with a usage sample should be surfaced even with zero requests")
	}

	if joinPodMetrics(nil, pods) != nil {
		t.Error("no usage samples must return nil (stays in MissingInputs), not an empty slice")
	}
}

func TestJoinPodMetricsCountsNativeSidecarRequests(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	req := func(cpuMilli, memMi int64) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse(fmtMilli(cpuMilli)),
			corev1.ResourceMemory: resource.MustParse(fmtMi(memMi)),
		}}
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Namespace: "prod", Name: "meshed"},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{{Resources: req(100, 128)}}, // app: 100m / 128Mi
			InitContainers: []corev1.Container{
				{Resources: req(50, 64), RestartPolicy: &always}, // native sidecar: counts
				{Resources: req(999, 999)},                       // plain init: excluded
			},
		},
	}
	usage := []k8score.TopPodMetrics{{Namespace: "prod", Name: "meshed", CPU: 30_000_000, Memory: 1}}

	got := joinPodMetrics(usage, []*corev1.Pod{pod})
	if len(got) != 1 {
		t.Fatalf("expected 1 metric, got %d", len(got))
	}
	// app (100m/128Mi) + native sidecar (50m/64Mi); plain init container excluded.
	if got[0].CPURequest != 150 {
		t.Errorf("CPURequest = %d, want 150m (app + native sidecar, not plain init)", got[0].CPURequest)
	}
	if got[0].MemoryRequest != 192*1024*1024 {
		t.Errorf("MemoryRequest = %d, want 192Mi (app + native sidecar)", got[0].MemoryRequest)
	}
}

func fmtMilli(m int64) string { return itoa(m) + "m" }
func fmtMi(mi int64) string   { return itoa(mi) + "Mi" }
func itoa(n int64) string     { return strconv.FormatInt(n, 10) }
