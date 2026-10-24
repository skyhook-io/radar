package audit

import (
	"reflect"
	"slices"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func resourceBudgets(cpu, memory string) corev1.ResourceList {
	values := corev1.ResourceList{}
	if cpu != "" {
		values[corev1.ResourceCPU] = resource.MustParse(cpu)
	}
	if memory != "" {
		values[corev1.ResourceMemory] = resource.MustParse(memory)
	}
	return values
}

func resourceTemplateInput(spec corev1.PodSpec, version string, lrs ...*corev1.LimitRange) *CheckInput {
	if lrs == nil {
		lrs = []*corev1.LimitRange{}
	}
	return &CheckInput{
		Deployments:    []*appsv1.Deployment{{ObjectMeta: metav1.ObjectMeta{Name: "app", Namespace: "team"}, Spec: appsv1.DeploymentSpec{Template: corev1.PodTemplateSpec{Spec: spec}}}},
		ClusterVersion: version, LimitRanges: lrs,
	}
}

func assertResourceCheck(t *testing.T, result *ScanResults, id string, evaluated, passed int) {
	t.Helper()
	if got := result.CheckCounts[id]; got.Evaluated != evaluated || got.Passed != passed {
		t.Errorf("%s counts = %+v, want evaluated=%d passed=%d", id, got, evaluated, passed)
	}
	found := false
	for _, f := range result.Findings {
		found = found || f.CheckID == id
	}
	if found != (evaluated > passed) {
		t.Errorf("%s finding=%v, evaluated=%d passed=%d", id, found, evaluated, passed)
	}
}

func TestResourceDefaultsVersionAndGateAgreement(t *testing.T) {
	spec := corev1.PodSpec{
		Resources:  &corev1.ResourceRequirements{Requests: resourceBudgets("500m", "")},
		Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: resourceBudgets("", "128Mi")}}, {Name: "sidecar"}},
	}
	for _, version := range []string{"v1.32.0", "1.33", "v1.34.1-gke.100", "v1.36.1-eks-a1", "1.37", "v1.37.1", "", "not-a-version", "2.0"} {
		t.Run(version, func(t *testing.T) {
			result := RunChecks(resourceTemplateInput(spec, version))
			unknown := version == "1.37" || version == "v1.37.1" || version == "" || version == "not-a-version" || version == "2.0"
			if unknown {
				assertResourceCheck(t, result, "memoryRequestMissing", 0, 0)
			} else {
				assertResourceCheck(t, result, "memoryRequestMissing", 1, 0)
			}
			if slices.Contains(result.MissingInputs, "pod-resource-defaulting") != unknown {
				t.Errorf("unexpected missing inputs: %v", result.MissingInputs)
			}
			assertResourceCheck(t, result, "cpuRequestMissing", 1, 1)
			assertResourceCheck(t, result, "cpuLimitMissing", 1, 0)
		})
	}
	// Equality with the aggregate is configured coverage, not a sizing failure.
	spec.Resources.Requests[corev1.ResourceMemory] = resource.MustParse("128Mi")
	assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.37")), "memoryRequestMissing", 1, 1)
}

func TestStoredPodResourcesAreNotDefaultedAgain(t *testing.T) {
	defaults := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, DefaultRequest: resourceBudgets("100m", "64Mi"), Default: resourceBudgets("500m", "128Mi")}}}}
	for _, lrs := range [][]*corev1.LimitRange{nil, {}, {defaults}} {
		pod := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "stored", Namespace: "team"}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Limits: resourceBudgets("500m", "128Mi")}}}}}
		before := pod.DeepCopy()
		input := &CheckInput{Pods: []*corev1.Pod{pod}, LimitRanges: lrs, ClusterVersion: "1.37"}
		result := RunChecks(input)
		assertResourceCheck(t, result, "cpuRequestMissing", 1, 0)
		assertResourceCheck(t, result, "memoryRequestMissing", 1, 0)
		assertResourceCheck(t, result, "cpuLimitMissing", 1, 1)
		pod.Spec.Resources = &corev1.ResourceRequirements{Requests: resourceBudgets("500m", "128Mi")}
		assertResourceCheck(t, RunChecks(input), "cpuRequestMissing", 1, 1)
		pod.Spec.Resources = nil
		if !reflect.DeepEqual(pod, before) {
			t.Fatal("audit mutated stored Pod")
		}
	}
}

func TestResourceDefaultsKeepExplicitZero(t *testing.T) {
	defaults := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, DefaultRequest: resourceBudgets("100m", "64Mi"), Default: resourceBudgets("500m", "128Mi")}}}}
	for _, version := range []string{"1.35", "1.37", ""} {
		spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "zero", Resources: corev1.ResourceRequirements{Requests: resourceBudgets("0", "0"), Limits: resourceBudgets("0", "0")}}}}
		result := RunChecks(resourceTemplateInput(spec, version, defaults))
		for _, id := range podSpecEfficiencyCheckIDs {
			assertResourceCheck(t, result, id, 1, 0)
		}
	}
}

func TestResourceDefaultsForContainerLimits(t *testing.T) {
	spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Limits: resourceBudgets("100m", "64Mi")}}}}
	for _, id := range podSpecEfficiencyCheckIDs {
		assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.37")), id, 1, 1)
	}
}

func TestResourceDefaultsSidecarsAndOrdinaryInit(t *testing.T) {
	spec := corev1.PodSpec{
		Containers:     []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: resourceBudgets("100m", "64Mi"), Limits: resourceBudgets("500m", "128Mi")}}},
		InitContainers: []corev1.Container{{Name: "setup"}},
	}
	for _, id := range podSpecEfficiencyCheckIDs {
		assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.37")), id, 1, 1)
	}
	spec.InitContainers = append(spec.InitContainers, corev1.Container{Name: "helper", RestartPolicy: ptr(corev1.ContainerRestartPolicyAlways)})
	result := RunChecks(resourceTemplateInput(spec, "1.37"))
	for _, id := range podSpecEfficiencyCheckIDs {
		assertResourceCheck(t, result, id, 1, 0)
	}
	for _, f := range result.Findings {
		if slices.Contains(podSpecEfficiencyCheckIDs, f.CheckID) && f.Message != "Sidecar \"helper\" has no "+map[string]string{"cpuRequestMissing": "CPU request", "memoryRequestMissing": "memory request", "cpuLimitMissing": "CPU limit", "memoryLimitMissing": "memory limit"}[f.CheckID] {
			t.Errorf("wrong sidecar attribution: %s", f.Message)
		}
	}
	spec.Resources = &corev1.ResourceRequirements{Requests: resourceBudgets("500m", "256Mi"), Limits: resourceBudgets("1", "512Mi")}
	for _, id := range podSpecEfficiencyCheckIDs {
		assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.37")), id, 1, 1)
	}
}

func TestResourceDefaultsZeroLimitsCanDeriveSharedLimits(t *testing.T) {
	spec := corev1.PodSpec{Resources: &corev1.ResourceRequirements{Requests: resourceBudgets("100m", "64Mi")}, Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Limits: resourceBudgets("0", "0")}}}}
	for _, id := range []string{"cpuLimitMissing", "memoryLimitMissing"} {
		assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.35")), id, 1, 0)
		assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.37")), id, 0, 0)
	}
}

func TestResourceDefaultsConflictingLimitRanges(t *testing.T) {
	makeRange := func(cpu string) *corev1.LimitRange {
		return &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, DefaultRequest: resourceBudgets(cpu, "")}}}}
	}
	zero, positive := makeRange("0"), makeRange("100m")
	for _, lrs := range [][]*corev1.LimitRange{{zero, positive}, {positive, zero}} {
		spec := corev1.PodSpec{Containers: []corev1.Container{{Name: "app"}}}
		result := RunChecks(resourceTemplateInput(spec, "1.35", lrs...))
		assertResourceCheck(t, result, "cpuRequestMissing", 0, 0)
		assertResourceCheck(t, result, "memoryRequestMissing", 1, 0)
		spec.Containers = append(spec.Containers, corev1.Container{Name: "explicit-zero", Resources: corev1.ResourceRequirements{Requests: resourceBudgets("0", "")}})
		assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.35", lrs...)), "cpuRequestMissing", 1, 0)
	}
}

func TestResourceDefaultsHugepageActivation(t *testing.T) {
	spec := corev1.PodSpec{Resources: &corev1.ResourceRequirements{Requests: resourceBudgets("500m", "")}, Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: resourceBudgets("", "128Mi"), Limits: corev1.ResourceList{corev1.ResourceName("hugepages-2Mi"): resource.MustParse("2Mi")}}}, {Name: "sidecar"}}}
	assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.32")), "memoryRequestMissing", 1, 0)
	assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.33")), "memoryRequestMissing", 1, 1)
	assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.35")), "memoryRequestMissing", 1, 1)
	spec.Resources.Requests[corev1.ResourceName("hugepages-2Mi")] = resource.MustParse("2Mi")
	for _, version := range []string{"1.33", "1.34"} {
		candidate := podResourceCandidates(spec, false, version, nil)[0]
		_, present := candidate.Resources.Limits[corev1.ResourceName("hugepages-2Mi")]
		if present != (version == "1.33") {
			t.Errorf("%s: unexpected hugepage limit defaulting with explicit request", version)
		}
	}
}

func TestResourceDefaultsAfterMetadataWebhookPatch(t *testing.T) {
	name := corev1.ResourceName("hugepages-2Mi")
	lr := &corev1.LimitRange{ObjectMeta: metav1.ObjectMeta{Namespace: "team"}, Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Default: corev1.ResourceList{name: resource.MustParse("2Mi")}, DefaultRequest: corev1.ResourceList{name: resource.MustParse("2Mi")}}}}}
	spec := corev1.PodSpec{Resources: &corev1.ResourceRequirements{Requests: resourceBudgets("500m", "")}, Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Requests: resourceBudgets("", "128Mi")}}, {Name: "helper", Resources: corev1.ResourceRequirements{Requests: resourceBudgets("100m", "")}}}}
	for _, version := range []string{"1.33", "1.35", "1.37", ""} {
		result := RunChecks(resourceTemplateInput(spec, version, lr))
		assertResourceCheck(t, result, "memoryRequestMissing", 0, 0)
		if !slices.Contains(result.MissingInputs, "pod-resource-defaulting") {
			t.Errorf("%s: missing uncertain admission result", version)
		}
	}
	assertResourceCheck(t, RunChecks(resourceTemplateInput(spec, "1.32", lr)), "memoryRequestMissing", 1, 0)
}

func TestResourceDefaultCandidatesDoNotMutateInputs(t *testing.T) {
	spec := corev1.PodSpec{Resources: &corev1.ResourceRequirements{Limits: resourceBudgets("1", "512Mi")}, Containers: []corev1.Container{{Name: "app", Resources: corev1.ResourceRequirements{Limits: resourceBudgets("100m", "64Mi")}}}, InitContainers: []corev1.Container{{Name: "setup", Resources: corev1.ResourceRequirements{Limits: resourceBudgets("200m", "128Mi")}}}}
	lr := &corev1.LimitRange{Spec: corev1.LimitRangeSpec{Limits: []corev1.LimitRangeItem{{Type: corev1.LimitTypeContainer, Default: resourceBudgets("500m", "256Mi")}}}}
	before, lrBefore := spec.DeepCopy(), lr.DeepCopy()
	for _, candidate := range podResourceCandidates(spec, false, "1.37", []*corev1.LimitRange{lr}) {
		candidate.Resources.Requests[corev1.ResourceCPU] = resource.MustParse("99")
	}
	if !reflect.DeepEqual(spec, *before) || !reflect.DeepEqual(lr, lrBefore) {
		t.Fatal("defaulting mutated cached resources")
	}
}
