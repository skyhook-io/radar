package prometheus

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/skyhook-io/radar/pkg/prom"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
)

func resourceList(pairs ...string) corev1.ResourceList {
	list := corev1.ResourceList{}
	for i := 0; i < len(pairs); i += 2 {
		list[corev1.ResourceName(pairs[i])] = resource.MustParse(pairs[i+1])
	}
	return list
}

func TestFixedPodLevelRequest(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	tests := []struct {
		name    string
		spec    corev1.PodSpec
		wantCPU string
		wantMem string
	}{
		{name: "no pod-level resources", spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}},
		{
			name:    "explicit pod-level requests",
			spec:    corev1.PodSpec{Resources: &corev1.ResourceRequirements{Requests: resourceList("cpu", "200m", "memory", "128Mi")}, Containers: []corev1.Container{{Name: "app"}}},
			wantCPU: "200m", wantMem: "128Mi",
		},
		{
			name: "explicit pod-level request wins even when a container sets its own",
			spec: corev1.PodSpec{Resources: &corev1.ResourceRequirements{Requests: resourceList("cpu", "300m")}, Containers: []corev1.Container{
				{Name: "app", Resources: corev1.ResourceRequirements{Requests: resourceList("cpu", "100m")}},
			}},
			wantCPU: "300m",
		},
		{
			// The request defaults to the limit, but adding container requests
			// changes it, so container sizing still applies.
			name: "a pod-level limit alone does not fix the request",
			spec: corev1.PodSpec{Resources: &corev1.ResourceRequirements{Limits: resourceList("memory", "256Mi")}, Containers: []corev1.Container{{Name: "app"}}},
		},
		{
			name: "pod-level limit with a container request derives the request from containers",
			spec: corev1.PodSpec{Resources: &corev1.ResourceRequirements{Limits: resourceList("cpu", "2")}, Containers: []corev1.Container{
				{Name: "a", Resources: corev1.ResourceRequirements{Requests: resourceList("cpu", "500m")}},
				{Name: "b"},
			}},
		},
		{
			name: "a container limit also derives the request, through the container's own defaulting",
			spec: corev1.PodSpec{Resources: &corev1.ResourceRequirements{Limits: resourceList("memory", "256Mi")}, Containers: []corev1.Container{
				{Name: "a", Resources: corev1.ResourceRequirements{Limits: resourceList("memory", "128Mi")}},
			}},
		},
		{
			name: "an init container's request also derives it",
			spec: corev1.PodSpec{Resources: &corev1.ResourceRequirements{Limits: resourceList("memory", "256Mi")},
				Containers:     []corev1.Container{{Name: "app"}},
				InitContainers: []corev1.Container{{Name: "proxy", RestartPolicy: &always, Resources: corev1.ResourceRequirements{Requests: resourceList("memory", "32Mi")}}},
			},
		},
		{
			name: "per resource",
			spec: corev1.PodSpec{Resources: &corev1.ResourceRequirements{Requests: resourceList("memory", "128Mi"), Limits: resourceList("cpu", "1", "memory", "256Mi")}, Containers: []corev1.Container{
				{Name: "app", Resources: corev1.ResourceRequirements{Requests: resourceList("cpu", "100m")}},
			}},
			wantMem: "128Mi",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			for name, want := range map[corev1.ResourceName]string{corev1.ResourceCPU: tc.wantCPU, corev1.ResourceMemory: tc.wantMem} {
				got := fixedPodLevelRequest(&tc.spec, name)
				switch {
				case want == "" && got != nil:
					t.Errorf("%s = %s, want none", name, got.String())
				case want != "" && (got == nil || got.String() != want):
					t.Errorf("%s = %v, want %s", name, got, want)
				}
			}
		})
	}
}

func podLevelQuerier(cpu, mem map[string]float64, samples float64) fakeRightsizingQuerier {
	coverage := map[string]float64{}
	for name := range cpu {
		coverage[name] = samples
	}
	return fakeRightsizingQuerier(func(query string) (*prom.QueryResult, error) {
		switch {
		case strings.HasPrefix(query, "sum(count_over_time"):
			return &prom.QueryResult{Series: []prom.Series{{DataPoints: []prom.DataPoint{{Value: 1}}}}}, nil
		case strings.HasPrefix(query, "quantile_over_time"):
			return containerResult(cpu), nil
		case strings.HasPrefix(query, "count_over_time"):
			return containerResult(coverage), nil
		case strings.HasPrefix(query, "max_over_time") && strings.Contains(query, "container_memory_working_set_bytes"):
			return containerResult(mem), nil
		case strings.HasPrefix(query, "max_over_time") && strings.Contains(query, "container_cpu_cfs_throttled"):
			return containerResult(map[string]float64{}), nil
		case strings.Contains(query, "kube_pod_container_status_restarts_total"):
			return containerResult(map[string]float64{"app": 0, "sidecar": 0}), nil
		case strings.Contains(query, "last_terminated_timestamp"):
			return terminationResult(nil), nil
		default:
			return nil, errors.New("unexpected query")
		}
	})
}

func rowFor(rows []RightsizingRow, container, resourceName string) RightsizingRow {
	for _, row := range rows {
		if row.Container == container && row.Resource == resourceName {
			return row
		}
	}
	return RightsizingRow{}
}

func TestComputeRightsizing_PodLevelRequest(t *testing.T) {
	podBudget := &corev1.ResourceRequirements{Requests: resourceList("cpu", "200m", "memory", "128Mi"), Limits: resourceList("cpu", "500m", "memory", "256Mi")}
	own := corev1.ResourceRequirements{Requests: resourceList("cpu", "100m", "memory", "64Mi"), Limits: resourceList("memory", "128Mi")}
	cpu := map[string]float64{"app": 0.4, "sidecar": 0.002}
	mem := map[string]float64{"app": 60 * 1024 * 1024, "sidecar": 4 * 1024 * 1024}
	workloadFor := func(spec corev1.PodSpec) rightsizingWorkload {
		return rightsizingWorkload{containers: extractRuntimeContainers(&spec), currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}, hpaAvailable: true, replicas: 2}
	}

	t.Run("pod-level only", func(t *testing.T) {
		workload := workloadFor(corev1.PodSpec{Resources: podBudget, Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}}})
		resp := computeRightsizing(context.Background(), podLevelQuerier(cpu, mem, 2016), "Deployment", "ns", "api", workload)
		for _, row := range resp.Rows {
			if row.Fit != FitPodLevelRequest || row.RecommendationReason != ReasonPodLevelRequest {
				t.Errorf("%s/%s fit=%s reason=%q, want pod_level_request", row.Container, row.Resource, row.Fit, row.RecommendationReason)
			}
			if row.RecommendedReq != nil || row.CalculatedReq != nil {
				t.Errorf("%s/%s suggests a container request under a pod-level request: %+v", row.Container, row.Resource, row)
			}
			if row.Observed == nil {
				t.Errorf("%s/%s lost its observed usage", row.Container, row.Resource)
			}
		}
		if got := rowFor(resp.Rows, "app", "cpu").PodLevelRequest; got == nil || *got != "200m" {
			t.Errorf("cpu podLevelRequest = %v, want 200m", got)
		}
		if got := rowFor(resp.Rows, "sidecar", "memory").PodLevelRequest; got == nil || *got != "128Mi" {
			t.Errorf("memory podLevelRequest = %v, want 128Mi", got)
		}
		if got := rowFor(resp.Rows, "app", "cpu").PodLevelRequestValue; got == nil || *got != 0.2 {
			t.Errorf("cpu podLevelRequestValue = %v, want 0.2 cores", got)
		}
		if got := rowFor(resp.Rows, "app", "memory").PodLevelRequestValue; got == nil || *got != 128*1024*1024 {
			t.Errorf("memory podLevelRequestValue = %v, want 128Mi in bytes", got)
		}
		class, impact := ClassifyWorkload(resp.Rows, 2, false)
		if class != ClassReview || impact.CPUChange != 0 || impact.MemoryChange != 0 {
			t.Errorf("class=%s impact=%+v, want review with no impact", class, impact)
		}
		if reasons := ManualReviewReasons(resp.Rows[0]); !slices.Contains(reasons, "pod_level_request") {
			t.Errorf("review reasons = %v", reasons)
		}
	})

	t.Run("mixed: a container's own request is not sized against a pod-level request", func(t *testing.T) {
		workload := workloadFor(corev1.PodSpec{Resources: podBudget, Containers: []corev1.Container{{Name: "app", Resources: own}, {Name: "sidecar"}}})
		resp := computeRightsizing(context.Background(), podLevelQuerier(cpu, mem, 2016), "Deployment", "ns", "api", workload)
		app := rowFor(resp.Rows, "app", "cpu")
		if app.Fit != FitPodLevelRequest || app.CurrentRequest == nil || *app.CurrentRequest != "100m" {
			t.Errorf("app cpu row = %+v", app)
		}
	})

	t.Run("short history still names the pod-level request", func(t *testing.T) {
		workload := workloadFor(corev1.PodSpec{Resources: podBudget, Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}}})
		resp := computeRightsizing(context.Background(), podLevelQuerier(cpu, mem, 10), "Deployment", "ns", "api", workload)
		if row := rowFor(resp.Rows, "app", "memory"); row.Fit != FitPodLevelRequest {
			t.Errorf("row = %+v, want pod_level_request rather than a promise of a later verdict", row)
		}
	})

	t.Run("a failed usage query keeps its answer", func(t *testing.T) {
		workload := workloadFor(corev1.PodSpec{Resources: podBudget, Containers: []corev1.Container{{Name: "app"}}})
		failing := fakeRightsizingQuerier(func(string) (*prom.QueryResult, error) { return nil, errors.New("boom") })
		resp := computeRightsizing(context.Background(), failing, "Deployment", "ns", "api", workload)
		if row := rowFor(resp.Rows, "app", "cpu"); row.QueryError == "" || row.Fit == FitPodLevelRequest {
			t.Errorf("row = %+v, want the query error to stand", row)
		}
	})

	t.Run("container-only is unchanged", func(t *testing.T) {
		workload := workloadFor(corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}}})
		resp := computeRightsizing(context.Background(), podLevelQuerier(cpu, mem, 2016), "Deployment", "ns", "api", workload)
		for _, row := range resp.Rows {
			if row.Fit != FitMissingRequest || row.RecommendedReq == nil || row.PodLevelRequest != nil {
				t.Errorf("%s/%s = %+v, want missing_request with a suggestion", row.Container, row.Resource, row)
			}
		}
	})

	t.Run("pod limit with container requests: containers still decide the request", func(t *testing.T) {
		workload := workloadFor(corev1.PodSpec{Resources: &corev1.ResourceRequirements{Limits: resourceList("cpu", "2")},
			Containers: []corev1.Container{{Name: "app", Resources: own}, {Name: "sidecar"}}})
		resp := computeRightsizing(context.Background(), podLevelQuerier(cpu, mem, 2016), "Deployment", "ns", "api", workload)
		if row := rowFor(resp.Rows, "sidecar", "cpu"); row.Fit != FitMissingRequest {
			t.Errorf("sidecar cpu = %+v, want missing_request", row)
		}
	})
}

func TestComputeRightsizing_PodLimitOnlyStillSizesContainers(t *testing.T) {
	// Pod limit only: the admitted request defaults to the limit (1 core), and
	// container requests replace that default, so sizing them is meaningful.
	spec := corev1.PodSpec{Resources: &corev1.ResourceRequirements{Limits: resourceList("cpu", "1", "memory", "256Mi")},
		Containers: []corev1.Container{{Name: "app"}, {Name: "sidecar"}}}
	workload := rightsizingWorkload{podSpec: &spec, containers: extractRuntimeContainers(&spec), currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}, hpaAvailable: true}
	cpu := map[string]float64{"app": 0.44, "sidecar": 0.3}
	mem := map[string]float64{"app": 66 * 1024 * 1024, "sidecar": 40 * 1024 * 1024}
	rows := computeRightsizing(context.Background(), podLevelQuerier(cpu, mem, 2016), "Deployment", "ns", "api", workload).Rows
	for _, want := range []struct{ container, resource, rec string }{{"app", "cpu", "550m"}, {"sidecar", "cpu", "350m"}, {"app", "memory", "96Mi"}, {"sidecar", "memory", "64Mi"}} {
		row := rowFor(rows, want.container, want.resource)
		if row.Fit != FitMissingRequest || row.RecommendedReq == nil || *row.RecommendedReq != want.rec || row.PodLevelRequest != nil {
			t.Errorf("%s %s = %+v, want missing_request suggesting %s", want.container, want.resource, row, want.rec)
		}
	}
}

func TestBuildScanRow_PodLevelRequest(t *testing.T) {
	key := scanKey{namespace: "prod", kind: "Deployment", workload: "api", container: "app"}
	spec := corev1.PodSpec{Resources: &corev1.ResourceRequirements{Requests: resourceList("memory", "128Mi")}, Containers: []corev1.Container{{Name: "app"}}}
	workload := rightsizingWorkload{containers: extractRuntimeContainers(&spec), currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}, hpaAvailable: true}
	evidence := scanBatchEvidence{
		cpu:      map[scanKey][]float64{key: repeatedValues(0.1)},
		memory:   map[scanKey][]float64{key: repeatedValues(50 * 1024 * 1024)},
		restarts: map[scanKey]float64{key: 0}, terminations: map[scanKey]terminationEvidence{}, errors: map[string]error{},
	}
	memory := buildScanRow(workload.containers[0], "memory", key, rightsizingMinSamples, workload, evidence)
	if memory.Fit != FitPodLevelRequest || memory.RecommendedReq != nil || memory.PodLevelRequest == nil || *memory.PodLevelRequest != "128Mi" ||
		memory.PodLevelRequestValue == nil || *memory.PodLevelRequestValue != 128*1024*1024 {
		t.Errorf("memory row = %+v", memory)
	}
	cpu := buildScanRow(workload.containers[0], "cpu", key, rightsizingMinSamples, workload, evidence)
	if cpu.Fit != FitMissingRequest {
		t.Errorf("cpu has no pod-level request and must still be judged per container: %+v", cpu)
	}
}

func TestComputeRightsizing_PodLevelRowWithoutSamplesStaysUnevidenced(t *testing.T) {
	spec := corev1.PodSpec{Resources: &corev1.ResourceRequirements{Requests: resourceList("cpu", "200m", "memory", "128Mi")}, Containers: []corev1.Container{{Name: "app"}}}
	workload := rightsizingWorkload{containers: extractRuntimeContainers(&spec), currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}, hpaAvailable: true}
	// CPU has samples, memory has none.
	resp := computeRightsizing(context.Background(), podLevelQuerier(map[string]float64{"app": 0.1}, map[string]float64{}, 2016), "Deployment", "ns", "api", workload)
	memory := rowFor(resp.Rows, "app", "memory")
	if memory.Fit != FitInsufficientHistory || !RowUnevidenced(memory) {
		t.Fatalf("memory row without samples = %+v, want insufficient_history", memory)
	}
	if cpu := rowFor(resp.Rows, "app", "cpu"); cpu.Fit != FitPodLevelRequest {
		t.Fatalf("cpu row = %+v, want pod_level_request", cpu)
	}
}

func TestComputeRightsizing_IncreasesStayWithinPodLimit(t *testing.T) {
	always := corev1.ContainerRestartPolicyAlways
	req := func(cpu string) corev1.ResourceRequirements {
		return corev1.ResourceRequirements{Requests: resourceList("cpu", cpu)}
	}
	mem := map[string]float64{"app": 60 * 1024 * 1024, "sidecar": 60 * 1024 * 1024}
	run := func(spec corev1.PodSpec, usage map[string]float64) []RightsizingRow {
		workload := rightsizingWorkload{podSpec: &spec, containers: extractRuntimeContainers(&spec), currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}, hpaAvailable: true}
		return computeRightsizing(context.Background(), podLevelQuerier(usage, mem, 2016), "Deployment", "ns", "api", workload).Rows
	}
	podLimit := func(cpu string) *corev1.ResourceRequirements {
		return &corev1.ResourceRequirements{Limits: resourceList("cpu", cpu)}
	}
	withheld := func(row RightsizingRow) bool {
		return row.RecommendedReq == nil && row.LimitConflict && row.RecommendationReason == ReasonRequestsExceedPodLimit
	}

	t.Run("an increase that takes the derived pod request over the limit is withheld", func(t *testing.T) {
		// 400m -> 600m with the sibling at 400m: 1000m fits a 1-core limit, 1100m would not.
		rows := run(corev1.PodSpec{Resources: podLimit("900m"), Containers: []corev1.Container{{Name: "app", Resources: req("400m")}, {Name: "sidecar", Resources: req("400m")}}},
			map[string]float64{"app": 0.5, "sidecar": 0.3})
		if row := rowFor(rows, "app", "cpu"); !withheld(row) {
			t.Errorf("app cpu = %+v, want the increase withheld", row)
		}
	})

	t.Run("increases that fit alone but not together are withheld", func(t *testing.T) {
		rows := run(corev1.PodSpec{Resources: podLimit("1"), Containers: []corev1.Container{{Name: "app", Resources: req("400m")}, {Name: "sidecar", Resources: req("400m")}}},
			map[string]float64{"app": 0.5, "sidecar": 0.5})
		for _, name := range []string{"app", "sidecar"} {
			if row := rowFor(rows, name, "cpu"); !withheld(row) {
				t.Errorf("%s cpu = %+v, want withheld: 600m + 600m exceeds 1 core", name, row)
			}
		}
	})

	t.Run("an increase landing exactly on the limit is kept", func(t *testing.T) {
		rows := run(corev1.PodSpec{Resources: podLimit("1"), Containers: []corev1.Container{{Name: "app", Resources: req("400m")}, {Name: "sidecar", Resources: req("400m")}}},
			map[string]float64{"app": 0.5, "sidecar": 0.3})
		if row := rowFor(rows, "app", "cpu"); row.RecommendedReq == nil || *row.RecommendedReq != "600m" || row.LimitConflict {
			t.Errorf("app cpu = %+v, want 600m kept: 600m + 400m equals the 1-core limit", row)
		}
	})

	t.Run("a sibling's limit counts as its request", func(t *testing.T) {
		rows := run(corev1.PodSpec{Resources: podLimit("1"), Containers: []corev1.Container{
			{Name: "app", Resources: req("100m")},
			{Name: "sidecar", Resources: corev1.ResourceRequirements{Limits: resourceList("cpu", "800m")}},
		}}, map[string]float64{"app": 0.25})
		if row := rowFor(rows, "app", "cpu"); !withheld(row) {
			t.Errorf("app cpu = %+v, want withheld: 300m + the sidecar's defaulted 800m exceeds 1 core", row)
		}
	})

	t.Run("a limit-only sibling's suggested reduction is not counted", func(t *testing.T) {
		// sidecar's request defaults to its 800m limit; its 150m suggestion is a reduction.
		rows := run(corev1.PodSpec{Resources: podLimit("1"), Containers: []corev1.Container{
			{Name: "app", Resources: req("100m")},
			{Name: "sidecar", Resources: corev1.ResourceRequirements{Limits: resourceList("cpu", "800m")}},
		}}, map[string]float64{"app": 0.5, "sidecar": 0.12})
		if row := rowFor(rows, "app", "cpu"); !withheld(row) {
			t.Errorf("app cpu = %+v, want withheld: 600m beside the sidecar's 800m exceeds 1 core", row)
		}
	})

	t.Run("init containers count the way Kubernetes counts them", func(t *testing.T) {
		rows := run(corev1.PodSpec{Resources: podLimit("1"),
			Containers: []corev1.Container{{Name: "app", Resources: req("100m")}},
			InitContainers: []corev1.Container{
				{Name: "sidecar", RestartPolicy: &always, Resources: req("100m")},
				{Name: "migrate", Resources: req("800m")},
			},
		}, map[string]float64{"app": 0.02, "sidecar": 0.5})
		if row := rowFor(rows, "sidecar", "cpu"); !withheld(row) {
			t.Errorf("sidecar cpu = %+v, want withheld: during init the sidecar's 600m runs beside migrate's 800m", row)
		}
	})

	t.Run("an increase is checked against a sibling's current request, not its suggested reduction", func(t *testing.T) {
		// app's reduction 800m -> 350m is a separate suggestion; sidecar's 600m must fit without it.
		rows := run(corev1.PodSpec{Resources: podLimit("1"), Containers: []corev1.Container{{Name: "app", Resources: req("800m")}, {Name: "sidecar", Resources: req("100m")}}},
			map[string]float64{"app": 0.3, "sidecar": 0.5})
		if row := rowFor(rows, "sidecar", "cpu"); !withheld(row) {
			t.Errorf("sidecar cpu = %+v, want withheld: 600m beside the current 800m exceeds 1 core", row)
		}
	})

	t.Run("a fixed pod-level request skips the check", func(t *testing.T) {
		spec := corev1.PodSpec{Resources: &corev1.ResourceRequirements{Requests: resourceList("cpu", "500m"), Limits: resourceList("cpu", "1")},
			Containers: []corev1.Container{{Name: "app", Resources: req("100m")}}}
		if row := rowFor(run(spec, map[string]float64{"app": 0.9}), "app", "cpu"); row.Fit != FitPodLevelRequest || row.LimitConflict {
			t.Errorf("app cpu = %+v, want pod_level_request", row)
		}
	})
}

func TestBuildScanWorkload_IncreasesStayWithinPodLimit(t *testing.T) {
	spec := corev1.PodSpec{Resources: &corev1.ResourceRequirements{Limits: resourceList("cpu", "900m")}, Containers: []corev1.Container{
		{Name: "app", Resources: corev1.ResourceRequirements{Requests: resourceList("cpu", "400m")}},
		{Name: "sidecar", Resources: corev1.ResourceRequirements{Requests: resourceList("cpu", "400m")}},
	}}
	input := scanWorkload{kind: "Deployment", namespace: "prod", name: "api", replicas: 1, workload: rightsizingWorkload{
		podSpec: &spec, containers: extractRuntimeContainers(&spec), currentPodOOM: map[string]bool{}, hpaManaged: map[string]bool{}, hpaAvailable: true,
	}}
	key := func(container string) scanKey {
		return scanKey{namespace: "prod", kind: "Deployment", workload: "api", container: container}
	}
	evidence := scanBatchEvidence{
		cpu:    map[scanKey][]float64{key("app"): repeatedValues(0.5), key("sidecar"): repeatedValues(0.3)},
		memory: map[scanKey][]float64{}, restarts: map[scanKey]float64{}, terminations: map[scanKey]terminationEvidence{}, errors: map[string]error{},
	}
	out := buildScanWorkload(input, evidence)
	if row := rowFor(out.Rows, "app", "cpu"); row.RecommendedReq != nil || row.RecommendationReason != ReasonRequestsExceedPodLimit {
		t.Errorf("scan app cpu = %+v, want the increase withheld", row)
	}
}
