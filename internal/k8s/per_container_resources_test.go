package k8s

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

// resReq builds a ResourceRequirements from CPU/memory request+limit strings.
// Empty strings are omitted so callers can model "no request" or "no limit".
func resReq(cpuReq, cpuLim, memReq, memLim string) corev1.ResourceRequirements {
	req := corev1.ResourceList{}
	lim := corev1.ResourceList{}
	if cpuReq != "" {
		req[corev1.ResourceCPU] = resource.MustParse(cpuReq)
	}
	if memReq != "" {
		req[corev1.ResourceMemory] = resource.MustParse(memReq)
	}
	if cpuLim != "" {
		lim[corev1.ResourceCPU] = resource.MustParse(cpuLim)
	}
	if memLim != "" {
		lim[corev1.ResourceMemory] = resource.MustParse(memLim)
	}
	return corev1.ResourceRequirements{Requests: req, Limits: lim}
}

func alwaysRestart() *corev1.ContainerRestartPolicy {
	p := corev1.ContainerRestartPolicyAlways
	return &p
}

// mixedPod: one regular container, one native sidecar (initContainer with
// restartPolicy Always), one regular init container (no restartPolicy).
func mixedPod() *corev1.Pod {
	return &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Resources: resReq("50m", "100m", "64Mi", "128Mi")},
			},
			InitContainers: []corev1.Container{
				// native sidecar — counted
				{Name: "istio-proxy", RestartPolicy: alwaysRestart(), Resources: resReq("100m", "200m", "128Mi", "256Mi")},
				// regular init container — excluded
				{Name: "setup", Resources: resReq("500m", "500m", "512Mi", "512Mi")},
			},
		},
	}
}

func TestSumRunningContainerResources(t *testing.T) {
	got := SumRunningContainerResources(mixedPod())

	// CPU nanocores = MilliValue * 1_000_000. app 50/100m + sidecar 100/200m.
	wantCPUReq := int64((50 + 100) * 1_000_000)
	wantCPULim := int64((100 + 200) * 1_000_000)
	// Memory bytes. app 64/128Mi + sidecar 128/256Mi.
	wantMemReq := int64((64 + 128) * 1024 * 1024)
	wantMemLim := int64((128 + 256) * 1024 * 1024)

	if got.CPURequest != wantCPUReq {
		t.Errorf("CPURequest = %d, want %d", got.CPURequest, wantCPUReq)
	}
	if got.CPULimit != wantCPULim {
		t.Errorf("CPULimit = %d, want %d", got.CPULimit, wantCPULim)
	}
	if got.MemoryRequest != wantMemReq {
		t.Errorf("MemoryRequest = %d, want %d", got.MemoryRequest, wantMemReq)
	}
	if got.MemoryLimit != wantMemLim {
		t.Errorf("MemoryLimit = %d, want %d", got.MemoryLimit, wantMemLim)
	}

	// The regular init container "setup" (500m / 512Mi) must be excluded — its
	// resources would inflate every total if native-sidecar detection leaked.
	if got.CPULimit >= int64(500*1_000_000) {
		t.Errorf("regular init container leaked into CPULimit: %d", got.CPULimit)
	}
}

func TestIsNativeSidecar(t *testing.T) {
	native := corev1.Container{Name: "sidecar", RestartPolicy: alwaysRestart()}
	if !isNativeSidecar(&native) {
		t.Error("initContainer with restartPolicy Always should be a native sidecar")
	}

	noPolicy := corev1.Container{Name: "init"}
	if isNativeSidecar(&noPolicy) {
		t.Error("initContainer with no restartPolicy should not be a native sidecar")
	}

	onFailure := corev1.ContainerRestartPolicy("OnFailure")
	other := corev1.Container{Name: "init2", RestartPolicy: &onFailure}
	if isNativeSidecar(&other) {
		t.Error("initContainer with restartPolicy != Always should not be a native sidecar")
	}
}

func TestBuildPodContainerMetrics(t *testing.T) {
	usage := map[string]ContainerResourceMetrics{
		"app":         {Name: "app", CPU: 40 * 1_000_000, Memory: 60 * 1024 * 1024},
		"istio-proxy": {Name: "istio-proxy", CPU: 90 * 1_000_000, Memory: 130 * 1024 * 1024},
		// usage-only container: present in metrics, absent from spec.
		"extra": {Name: "extra", CPU: 5 * 1_000_000, Memory: 5 * 1024 * 1024},
	}

	got := BuildPodContainerMetrics(mixedPod(), usage)

	byName := map[string]ContainerResourceMetrics{}
	for _, c := range got {
		byName[c.Name] = c
	}

	// Only the pod's declared long-running containers are reported: the regular
	// container and the native sidecar. The regular init container is excluded,
	// and so is the usage-only "extra" — a name present only in the metrics
	// buffer (a completed init / removed ephemeral container whose buffer was
	// never pruned) must not surface as a phantom row.
	if _, ok := byName["app"]; !ok {
		t.Error("regular container app missing from breakdown")
	}
	if _, ok := byName["istio-proxy"]; !ok {
		t.Error("native sidecar istio-proxy missing from breakdown")
	}
	if _, ok := byName["extra"]; ok {
		t.Error("usage-only container extra (not in spec) should be excluded as stale")
	}
	if _, ok := byName["setup"]; ok {
		t.Error("regular init container setup should be excluded from breakdown")
	}
	if len(got) != 2 {
		t.Fatalf("len(breakdown) = %d, want 2", len(got))
	}

	// app carries both usage (from map) and request/limit (from spec).
	app := byName["app"]
	if app.CPU != 40*1_000_000 {
		t.Errorf("app.CPU = %d, want %d", app.CPU, 40*1_000_000)
	}
	if app.CPULimit != 100*1_000_000 {
		t.Errorf("app.CPULimit = %d, want %d", app.CPULimit, 100*1_000_000)
	}
	if app.MemoryRequest != 64*1024*1024 {
		t.Errorf("app.MemoryRequest = %d, want %d", app.MemoryRequest, 64*1024*1024)
	}
}

func TestBuildPodContainerMetricsOmitsSingleContainer(t *testing.T) {
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "solo", Resources: resReq("50m", "100m", "64Mi", "128Mi")},
			},
		},
	}
	if got := BuildPodContainerMetrics(pod, nil); got != nil {
		t.Errorf("single-container pod should return nil breakdown, got %+v", got)
	}
}

func TestBuildPodContainerMetricsKeepsAppPlusSidecar(t *testing.T) {
	// One app + one native sidecar = two running containers → NOT omitted.
	pod := &corev1.Pod{
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{Name: "app", Resources: resReq("50m", "100m", "64Mi", "128Mi")},
			},
			InitContainers: []corev1.Container{
				{Name: "istio-proxy", RestartPolicy: alwaysRestart(), Resources: resReq("100m", "200m", "128Mi", "256Mi")},
			},
		},
	}
	got := BuildPodContainerMetrics(pod, nil)
	if len(got) != 2 {
		t.Fatalf("app+sidecar pod should return 2 entries, got %d (%+v)", len(got), got)
	}
}
